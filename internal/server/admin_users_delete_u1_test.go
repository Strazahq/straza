package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestUsersDeleteRevokesApproverDevices pins the approver half of the admin
// user delete: every approver device of the deleted person goes with the
// user, one straza.identity.updated per device names the device and the
// reason, the admin approvers list shows none of them, and a request with
// one of their approver tokens answers device_revoked. Another person's
// device stays enrolled and keeps working.
func TestUsersDeleteRevokesApproverDevices(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminTok, _ := checkinToken(t, app, base)

	pia := mkHuman(t, app, "pia")
	_, phone, phoneTok := enrollApprover(t, app, pia.ID)
	_, tablet, _ := enrollApprover(t, app, pia.ID)
	kara := mkHuman(t, app, "kara")
	_, keptDevice, keptTok := enrollApprover(t, app, kara.ID)

	listed := func() map[string]string {
		t.Helper()
		var rows []approverDevicePayload
		if code := adminReq(t, http.MethodGet, base+"/v1/admin/approvers", adminTok, nil, &rows); code != http.StatusOK {
			t.Fatalf("approvers list = %d", code)
		}
		out := map[string]string{}
		for _, row := range rows {
			out[row.ID] = row.UserID
		}
		return out
	}
	pending := func(tok string) (int, map[string]any) {
		t.Helper()
		code, _, out := callJSON(t, http.MethodGet, base+"/v1/approver/pending?scope=decidable", tok, nil)
		return code, out
	}
	if got := listed(); got[phone] != pia.ID || got[tablet] != pia.ID {
		t.Fatalf("control, approvers before the delete = %v, want both of pia's devices", got)
	}
	if code, out := pending(phoneTok); code != http.StatusOK {
		t.Fatalf("control, pia's phone before the delete = %d %v, want 200", code, out)
	}

	var out map[string]string
	if code := adminReq(t, http.MethodDelete, base+"/v1/admin/users/"+pia.ID, adminTok, nil, &out); code != http.StatusOK || out["status"] != "deleted" {
		t.Fatalf("delete = %d %v", code, out)
	}

	if devs, err := app.store.Approvers().ListDevices(ctx, pia.ID); err != nil || len(devs) != 0 {
		t.Errorf("pia's device rows after the delete = %d (%v), want none", len(devs), err)
	}
	got := listed()
	for _, id := range []string{phone, tablet} {
		if _, ok := got[id]; ok {
			t.Errorf("approvers list still shows pia's device %s", id)
		}
	}
	if got[keptDevice] != kara.ID {
		t.Errorf("approvers list lost kara's device: %v", got)
	}

	revoked := map[string]int{}
	for _, d := range outboxDataFor(t, app, "straza.identity.updated") {
		if d["action"] != "approver-device-revoked" || d["user"] != pia.ID {
			continue
		}
		device, _ := d["device"].(string)
		revoked[device]++
		if d["reason"] != "user deleted by admin" || d["via"] != "admin" {
			t.Errorf("identity event = %v, want reason %q and via admin", d, "user deleted by admin")
		}
	}
	if len(revoked) != 2 || revoked[phone] != 1 || revoked[tablet] != 1 {
		t.Errorf("approver-device-revoked events per device = %v, want exactly one for %s and one for %s", revoked, phone, tablet)
	}

	records := map[string]int{}
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] != "approver.revoke" {
			continue
		}
		device, _ := ev["device"].(string)
		records[device]++
		want := map[string]any{
			"target": device, "user": pia.ID, "username": "pia", "reason": "user deleted by admin",
			"actor": "kim", "actorId": admin.ID, "actorVia": "session",
		}
		for k, v := range want {
			if ev[k] != v {
				t.Errorf("approver.revoke record %s = %v, want %v (record %v)", k, ev[k], v, ev)
			}
		}
	}
	if len(records) != 2 || records[phone] != 1 || records[tablet] != 1 {
		t.Errorf("approver.revoke records per device = %v, want exactly one for %s and one for %s", records, phone, tablet)
	}

	if code, out := pending(phoneTok); code != http.StatusUnauthorized || out["code"] != codeDeviceRevoked {
		t.Errorf("pia's phone after the delete = %d %v, want 401 %s", code, out, codeDeviceRevoked)
	}
	if code, out := pending(keptTok); code != http.StatusOK {
		t.Errorf("kara's phone after the delete = %d %v, want 200", code, out)
	}
}

// cancelOnSoftDelete is a store whose user soft delete cancels the request's
// context once the row is marked deleted, as a client that disconnects at
// that moment does. fired counts the cancels, the test's positive control.
type cancelOnSoftDelete struct {
	store.Store
	cancel context.CancelFunc
	fired  int
}

func (s *cancelOnSoftDelete) Users() store.UserRepo { return cancelUsers{s.Store.Users(), s} }

type cancelUsers struct {
	store.UserRepo
	s *cancelOnSoftDelete
}

func (u cancelUsers) SoftDelete(ctx context.Context, id string) ([]store.RoleAssignment, error) {
	removed, err := u.UserRepo.SoftDelete(ctx, id)
	if err == nil && u.s.cancel != nil {
		u.s.cancel()
		u.s.fired++
	}
	return removed, err
}

// TestUsersDeleteOutlivesDisconnect pins that a client which disconnects
// right after the soft delete leaves nothing behind: the kill switch still
// writes its revocation row, marks the user's session revoked and chains
// user.killed, the deactivated identity event still goes out, and the
// person's approver devices, the AI agent's key and its OAuth grant still
// go, each with its event or record, because a retried delete answers 404
// and never runs the cascade again.
func TestUsersDeleteOutlivesDisconnect(t *testing.T) {
	t.Parallel()
	hook := &cancelOnSoftDelete{}
	app, _ := testAppPreRun(t, []func(*App){func(a *App) { hook.Store = a.store; a.store = hook }})
	ctx := context.Background()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	quinn := mkHuman(t, app, "quinn")
	_, phone, _ := enrollApprover(t, app, quinn.ID)
	bot := seedNHIWithKey(t, app, "gone-bot", pub)
	_, cred := grantUserApp(t, app, "github", bot.ID)
	sessions := map[string]string{}
	for _, u := range []store.User{quinn, bot} {
		ses, err := app.store.Sessions().Create(ctx, store.Session{UserID: u.ID, HarnessName: "console"})
		if err != nil {
			t.Fatal(err)
		}
		sessions[u.ID] = ses.ID
	}
	killSwitch := func(t *testing.T, u store.User) {
		t.Helper()
		revs, err := app.store.Revocations().List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		rows := 0
		for _, rv := range revs {
			if rv.Kind == store.RevokeUser && rv.TargetID == u.ID {
				rows++
			}
		}
		if rows != 1 {
			t.Errorf("user revocation rows = %d, want 1", rows)
		}
		if ses, err := app.store.Sessions().GetByID(ctx, sessions[u.ID]); err != nil || ses.Status != store.SessionRevoked {
			t.Errorf("session = %q (%v), want revoked", ses.Status, err)
		}
		killed, deactivated := 0, 0
		for _, d := range outboxDataFor(t, app, "straza.audit.identity") {
			if d["action"] == "user.killed" && d["user"] == u.ID && d["sessionsRevoked"] == float64(1) {
				killed++
			}
		}
		for _, d := range outboxDataFor(t, app, "straza.identity.deactivated") {
			if d["id"] == u.ID {
				deactivated++
			}
		}
		if killed != 1 || deactivated != 1 {
			t.Errorf("user.killed records with one session = %d, straza.identity.deactivated events = %d, want 1 and 1", killed, deactivated)
		}
	}

	cases := []struct {
		name  string
		user  store.User
		check func(t *testing.T)
	}{
		{"a person with an approver device", quinn, func(t *testing.T) {
			if devs, err := app.store.Approvers().ListDevices(ctx, quinn.ID); err != nil || len(devs) != 0 {
				t.Errorf("quinn's device rows = %d (%v), want none", len(devs), err)
			}
			events := 0
			for _, d := range outboxDataFor(t, app, "straza.identity.updated") {
				if d["action"] == "approver-device-revoked" && d["device"] == phone {
					events++
				}
			}
			if events != 1 {
				t.Errorf("approver-device-revoked events for the phone = %d, want 1", events)
			}
			records := 0
			for _, ev := range adminAuditEvents(t, app) {
				if ev["action"] == "approver.revoke" && ev["device"] == phone && ev["username"] == "quinn" {
					records++
				}
			}
			if records != 1 {
				t.Errorf("approver.revoke records for the phone = %d, want 1", records)
			}
		}},
		{"an AI agent with a key and an OAuth grant", bot, func(t *testing.T) {
			if _, err := app.store.Settings().Get(ctx, nhiKeyPrefix+bot.ID); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("key row: err = %v, want ErrNotFound", err)
			}
			if n := len(keyRemoveEvents(t, app, bot.ID)); n != 1 {
				t.Errorf("nhi-key.removed records = %d, want 1", n)
			}
			if rows := userGrants(t, app, bot.ID); len(rows) != 0 {
				t.Errorf("grant rows = %d, want none", len(rows))
			}
			removes := grantRemoveEvents(t, app, bot.ID)
			if len(removes) != 1 || removes[0]["credentialId"] != cred.ID {
				t.Errorf("apps.grant.remove records = %v, want one for %s", removes, cred.ID)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reqCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			hook.cancel = cancel
			fired := hook.fired
			req := httptest.NewRequest(http.MethodDelete, "/v1/admin/users/"+tc.user.ID, nil).WithContext(reqCtx)
			req.SetPathValue("id", tc.user.ID)
			rec := httptest.NewRecorder()
			app.handleUsersDelete(rec, req)
			if rec.Code != http.StatusOK || hook.fired != fired+1 {
				t.Fatalf("delete = %d, cancels %d, want 200 and the context cancelled after the soft delete", rec.Code, hook.fired-fired)
			}
			killSwitch(t, tc.user)
			tc.check(t)
		})
	}
}

// TestUsersDeleteGuardFailsClosed pins that the break-glass guard of the
// user delete fails closed: a store error on its user read answers 500 and
// deletes nothing, so a transient fault cannot let the break-glass admin be
// deleted. Without the fault the guard answers its 403, the control.
func TestUsersDeleteGuardFailsClosed(t *testing.T) {
	t.Parallel()
	app, base, fs := testAppFault(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	// An admin API token reads no user on the way in, so the fault reaches
	// the guard's read and nothing before it.
	var minted struct {
		Token string `json:"token"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", idToken, map[string]any{"name": "guard", "scope": "full"}, &minted); code != http.StatusCreated {
		t.Fatalf("mint = %d", code)
	}
	bg := setPasswordDirect(t, app, BreakGlassUsername, "vaulted-secret")

	fs.arm("users", errors.New("boom"))
	code, _, out := callJSON(t, http.MethodDelete, base+"/v1/admin/users/"+bg.ID, minted.Token, nil)
	fs.disarm()
	if code != http.StatusInternalServerError || out["error"] != "lookup failed" {
		t.Errorf("delete with the guard's read failing = %d %v, want 500 lookup failed", code, out)
	}
	if _, err := app.store.Users().GetByID(context.Background(), bg.ID); err != nil {
		t.Fatalf("break-glass row after the refused delete: %v", err)
	}
	code, _, out = callJSON(t, http.MethodDelete, base+"/v1/admin/users/"+bg.ID, minted.Token, nil)
	if code != http.StatusForbidden {
		t.Errorf("control, delete of the break-glass admin = %d %v, want 403", code, out)
	}
}
