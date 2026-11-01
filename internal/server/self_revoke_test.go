package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

// revocationCounter counts, per session id, the straza.revocation.session
// control events (singular and inside plural bulk envelopes) and the
// per-target straza.push.session.<id> publishes, the two halves of the
// revoke trio that leave this pod. Deterministic despite the async relay
// because both lanes preserve order: the outbox drains oldest-first and core
// pushes ride one connection, so once a LATER sentinel revocation is
// observed, every earlier count is final.
type revocationCounter struct {
	mu     sync.Mutex
	events map[string]int // singular control events per session id
	plural map[string]int // appearances inside straza.revocation.sessions sets
	pushes map[string]int // per-target push publishes per session id
}

func newRevocationCounter(t *testing.T, app *App) *revocationCounter {
	t.Helper()
	c := &revocationCounter{events: map[string]int{}, plural: map[string]int{}, pushes: map[string]int{}}
	unsub1, err := app.bus.SubscribeCore("straza.revocation.session", func(_ string, data []byte) {
		var env struct {
			Data struct {
				Session string `json:"session"`
			} `json:"data"`
		}
		_ = json.Unmarshal(data, &env)
		c.mu.Lock()
		c.events[env.Data.Session]++
		c.mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unsub1)
	unsub2, err := app.bus.SubscribeCore("straza.revocation.sessions", func(_ string, data []byte) {
		var env struct {
			Data struct {
				Sessions []string `json:"sessions"`
			} `json:"data"`
		}
		_ = json.Unmarshal(data, &env)
		c.mu.Lock()
		for _, id := range env.Data.Sessions {
			c.plural[id]++
		}
		c.mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unsub2)
	unsub3, err := app.bus.SubscribeCore("straza.push.session.>", func(subject string, _ []byte) {
		c.mu.Lock()
		c.pushes[strings.TrimPrefix(subject, "straza.push.session.")]++
		c.mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unsub3)
	return c
}

func (c *revocationCounter) snapshot(id string) (events, plural, pushes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.events[id], c.plural[id], c.pushes[id]
}

// waitSeen blocks until the session has at least one event (singular or
// plural) AND one push, or fails the test: the sentinel that makes earlier
// counts final.
func (c *revocationCounter) waitSeen(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ev, pl, pu := c.snapshot(id)
		if ev+pl > 0 && pu > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no control event/push observed for %s (events=%d plural=%d pushes=%d)", id, ev, pl, pu)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// revocationRows counts persisted session-revocation rows for one id.
func revocationRows(t *testing.T, app *App, id string) int {
	t.Helper()
	revs, err := app.store.Revocations().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, rv := range revs {
		if rv.Kind == store.RevokeSession && rv.TargetID == id {
			n++
		}
	}
	return n
}

// TestSessionSelfRevoke pins POST /v1/session/revoke: the authenticated caller
// ends the session its own token names: no id parameter, no role gate. The
// route exists so `strazactl logout` can revoke server-side for every caller,
// not only holders of straza-admin; the admin route stays admin-gated and
// untouched. Confused-deputy-proof by construction: the only session the
// handler can touch is the one in the verified token's claims.
//
// The replay half is the adversarial pin: tokens.Verify never consults the
// denylist, so the revoked session's token stays VALID for its whole TTL and
// can replay this route freely. Every replay must be the idempotent 200 the
// CLI contract promises and fire NOTHING (no new revocation row, no control
// event, no push), or one logout becomes an authenticated write-amplification
// primitive against the outbox.
func TestSessionSelfRevoke(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app) // kim, role dev, NOT straza-admin
	ctx := context.Background()

	tokenA, sesA := checkinToken(t, app, base)
	tokenB, sesB := checkinToken(t, app, base) // a second live session: must survive A's revoke
	counter := newRevocationCounter(t, app)

	// The admin route is unchanged: the same non-admin token is refused there.
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/sessions/"+sesA+"/revoke", tokenA, nil, nil); code != http.StatusForbidden {
		t.Fatalf("admin revoke as non-admin = %d, want 403 (role gate unchanged)", code)
	}

	// Auth matrix: only a verified SESSION token opens the route.
	authMatrix := []struct {
		name   string
		bearer string
	}{
		{"no token", ""},
		{"garbage token", "not-a-jwt"},
		{"id token names no session", loginDeviceFlow(t, base, "kim", "hunter2!")},
	}
	for _, tc := range authMatrix {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := postJSONAuth(t, base+"/v1/session/revoke", tc.bearer, map[string]any{}); code != http.StatusUnauthorized {
				t.Errorf("self revoke with %s = %d, want 401", tc.name, code)
			}
		})
	}

	// The non-admin caller revokes their own session, and ONLY that one.
	code, out := postJSONAuth(t, base+"/v1/session/revoke", tokenA, map[string]any{})
	if code != http.StatusOK || out["status"] != "revoked" || out["session"] != sesA {
		t.Fatalf("self revoke = %d %v, want 200 revoked %s", code, out, sesA)
	}
	ses, err := app.store.Sessions().GetByID(ctx, sesA)
	if err != nil || ses.Status != store.SessionRevoked {
		t.Errorf("session row = %+v (%v), want status revoked", ses, err)
	}
	other, err := app.store.Sessions().GetByID(ctx, sesB)
	if err != nil || other.Status != store.SessionActive {
		t.Errorf("sibling session = %+v (%v), want untouched and active", other, err)
	}

	// The full kill-switch cascade fired, same as the admin revoke: exactly
	// one revocation row, one control event, one push, and the very next
	// decide fails closed.
	counter.waitSeen(t, sesA)
	if n := revocationRows(t, app, sesA); n != 1 {
		t.Errorf("revocation rows after first revoke = %d, want exactly 1", n)
	}
	code, dec := decide(t, base, tokenA, map[string]any{
		"kind": "tool.pre", "tool": "shell.exec", "command": "git status",
	})
	if code != http.StatusOK || dec["effect"] != "deny" || dec["ruleId"] != "revoked" {
		t.Errorf("post-revoke decision = %d %v, want deny by rule \"revoked\"", code, dec)
	}
	if reason, _ := dec["reason"].(string); !strings.Contains(reason, "revoked") {
		t.Errorf("deny reason not actionable: %v", dec)
	}

	// Replays: still-verifying token, already-dead session. Idempotent 200,
	// id echoed, NOTHING re-fires. (404 stays reserved for servers that
	// predate the route, the CLI's fall-back-to-admin signal.)
	for i := 0; i < 2; i++ {
		if code, out := postJSONAuth(t, base+"/v1/session/revoke", tokenA, map[string]any{}); code != http.StatusOK || out["session"] != sesA {
			t.Fatalf("replay %d = %d %v, want idempotent 200 echoing %s", i+1, code, out, sesA)
		}
	}
	// Sentinel: revoke sesB the same way. Once ITS event and push are
	// observed, every earlier sesA emission would have arrived too (ordered
	// lanes), so the counts below are final, not a sleep-and-hope.
	if code, out := postJSONAuth(t, base+"/v1/session/revoke", tokenB, map[string]any{}); code != http.StatusOK || out["session"] != sesB {
		t.Fatalf("sentinel self revoke = %d %v", code, out)
	}
	counter.waitSeen(t, sesB)
	if ev, pl, pu := counter.snapshot(sesA); ev != 1 || pl != 0 || pu != 1 {
		t.Errorf("sesA control traffic after replays = events %d, plural %d, pushes %d; want exactly 1/0/1", ev, pl, pu)
	}
	if n := revocationRows(t, app, sesA); n != 1 {
		t.Errorf("revocation rows after replays = %d, want still exactly 1", n)
	}
}

// TestAdminSessionRevokeReplayIdempotent pins the same replay contract on the
// admin-gated siblings (lower severity, the caller holds straza-admin, but
// the idempotency contract matches the self route): a re-revoke answers the
// same 200 it always did and fires nothing, and the bulk face counts and
// emits only sessions that actually transitioned, exactly as its openapi text
// ("unknown/already-dead ids are skipped; the response counts what was
// actually revoked") always claimed.
func TestAdminSessionRevokeReplayIdempotent(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	_, sesA := checkinToken(t, app, base)
	_, sesB := checkinToken(t, app, base)
	counter := newRevocationCounter(t, app)

	// First single revoke: the real transition.
	var out map[string]string
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/sessions/"+sesA+"/revoke", adminTok, nil, &out); code != http.StatusOK || out["status"] != "revoked" {
		t.Fatalf("revoke = %d %v", code, out)
	}
	counter.waitSeen(t, sesA)

	// Replays: the answer stays 200 "revoked" (the route's long-standing
	// shape for a session that exists), but nothing re-fires.
	for i := 0; i < 2; i++ {
		if code := adminReq(t, http.MethodPost, base+"/v1/admin/sessions/"+sesA+"/revoke", adminTok, nil, &out); code != http.StatusOK || out["status"] != "revoked" {
			t.Fatalf("replay %d = %d %v, want 200 revoked", i+1, code, out)
		}
	}
	// Bulk face, all-dead set: counts zero, emits nothing.
	var bulk struct {
		Revoked int `json:"revoked"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/sessions/revoke", adminTok,
		map[string]any{"sessions": []string{sesA, "no-such-session"}}, &bulk); code != http.StatusOK || bulk.Revoked != 0 {
		t.Fatalf("bulk of dead ids = %d revoked=%d, want 200 with 0", code, bulk.Revoked)
	}
	// Bulk face, mixed set: only the live session counts, and the plural
	// event (the sentinel that finalizes sesA's counts) names ONLY it.
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/sessions/revoke", adminTok,
		map[string]any{"sessions": []string{sesA, sesB}}, &bulk); code != http.StatusOK || bulk.Revoked != 1 {
		t.Fatalf("mixed bulk = %d revoked=%d, want 200 with 1", code, bulk.Revoked)
	}
	counter.waitSeen(t, sesB)

	if ev, pl, pu := counter.snapshot(sesA); ev != 1 || pl != 0 || pu != 1 {
		t.Errorf("sesA control traffic = events %d, plural %d, pushes %d; want exactly 1/0/1", ev, pl, pu)
	}
	if evB, plB, puB := counter.snapshot(sesB); evB != 0 || plB != 1 || puB != 1 {
		t.Errorf("sesB control traffic = events %d, plural %d, pushes %d; want exactly 0/1/1", evB, plB, puB)
	}
	if n := revocationRows(t, app, sesA); n != 1 {
		t.Errorf("sesA revocation rows = %d, want exactly 1 across single replays and bulk", n)
	}
	if n := revocationRows(t, app, sesB); n != 1 {
		t.Errorf("sesB revocation rows = %d, want exactly 1", n)
	}
	// An id that never existed keeps 404 on the single face.
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/sessions/no-such-session/revoke", adminTok, nil, nil); code != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404 (single-face contract unchanged)", code)
	}
}

// revokeTarget is what one route of TestRevokeRefusedWriteChangesNothing
// ends: a session, with its token when it has one, or a device with the key
// holder that enrolled it, and the user that owns either.
type revokeTarget struct {
	session, token string
	device         string
	holder         *keyHolder
	user           store.User
}

// sessionRevokeEvents counts the straza.revocation.session events and the
// straza.revocation.sessions sets that name the session id.
func sessionRevokeEvents(t *testing.T, app *App, id string) int {
	t.Helper()
	n := len(matching(t, app, "straza.revocation.session", func(d map[string]any) bool { return d["session"] == id }))
	return n + len(matching(t, app, "straza.revocation.sessions", func(d map[string]any) bool {
		ids, _ := d["sessions"].([]any)
		return slices.Contains(ids, any(id))
	}))
}

// sessionEnds returns the session.end records of the session id.
func sessionEnds(t *testing.T, app *App, id string) []map[string]any {
	t.Helper()
	return matching(t, app, "straza.audit.authn", func(d map[string]any) bool {
		return d["action"] == "session.end" && d["session"] == id
	})
}

// requireRevokeWrites asserts the durable writes of one revoke target: its
// row, its revocation rows, its revocation events and its session.end
// records, wantRows of each kind, and that this server's denylist refuses it.
func requireRevokeWrites(t *testing.T, app *App, tg revokeTarget, wantRows, wantEnds int) {
	t.Helper()
	ctx := context.Background()
	if tg.device != "" {
		_, err := app.store.Devices().GetByID(ctx, tg.device)
		revs, revErr := app.store.Revocations().ListByTarget(ctx, store.RevokeDevice, tg.device)
		events := matching(t, app, "straza.revocation.device", func(d map[string]any) bool { return d["device"] == tg.device })
		if gone := errors.Is(err, store.ErrNotFound); gone != (wantRows == 1) || revErr != nil || len(revs) != wantRows || len(events) != wantRows {
			t.Errorf("device %s: row gone %v (%v), %d revocation rows (%v), %d events, want %d of each with the row gone only then",
				tg.device, gone, err, len(revs), revErr, len(events), wantRows)
		}
		if !app.denylist.deviceBlocked(tg.device) {
			t.Errorf("device %s is not on this server's denylist", tg.device)
		}
		return
	}
	ses, err := app.store.Sessions().GetByID(ctx, tg.session)
	wantStatus := store.SessionActive
	if wantRows == 1 {
		wantStatus = store.SessionRevoked
	}
	if err != nil || ses.Status != wantStatus {
		t.Errorf("session %s: status %q (%v), want %q", tg.session, ses.Status, err, wantStatus)
	}
	if rows, events, ends := revocationRows(t, app, tg.session), sessionRevokeEvents(t, app, tg.session), len(sessionEnds(t, app, tg.session)); rows != wantRows || events != wantRows || ends != wantEnds {
		t.Errorf("session %s: %d revocation rows, %d revocation events, %d session.end records, want %d, %d and %d",
			tg.session, rows, events, ends, wantRows, wantRows, wantEnds)
	}
	if !app.denylist.blocked(authn.Claims{Session: tg.session}) {
		t.Errorf("session %s is not on this server's denylist", tg.session)
	}
}

// TestRevokeRefusedWriteChangesNothing pins every route that ends a session
// or a device against a database that refuses the revoke transaction. The
// answer says what failed and names the request id, the target stays active
// or enrolled, this server's denylist refuses it at once, and no revocation
// row, revocation event, session.end or stand-down record is written. Once
// the database answers again, the rerun the answer names ends the target
// with exactly one row, one event and the route's one session.end.
func TestRevokeRefusedWriteChangesNothing(t *testing.T) {
	t.Parallel()
	app, base, fs := testAppFault(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	admin, _ := checkinToken(t, app, base)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	ctx := context.Background()

	session := func(t *testing.T) revokeTarget {
		tok, id := checkinToken(t, app, base)
		return revokeTarget{session: id, token: tok, user: kim}
	}
	enrolled := 0
	device := func(reuse bool) func(t *testing.T) revokeTarget {
		return func(t *testing.T) revokeTarget {
			enrolled++
			h := newKeyHolder(t, base)
			h.enroll(idToken, fmt.Sprintf("sha256:l4-revoke-%d", enrolled))
			if reuse {
				h.counter = 2
				h.mustCheckin()
			}
			return revokeTarget{device: h.device, holder: h, user: kim}
		}
	}
	agents := 0
	agentSession := func(withKey bool) func(t *testing.T) revokeTarget {
		return func(t *testing.T) revokeTarget {
			agents++
			var created map[string]any
			if code := adminReq(t, http.MethodPost, base+"/v1/admin/users", admin,
				map[string]any{"username": fmt.Sprintf("l4-revoke-bot-%d", agents), "kind": "nhi"}, &created); code != http.StatusCreated {
				t.Fatalf("create agent = %d %v", code, created)
			}
			u, err := app.store.Users().GetByID(ctx, created["id"].(string))
			if err != nil {
				t.Fatal(err)
			}
			if withKey {
				pub, _ := newEd25519(t)
				if code := adminReq(t, http.MethodPut, base+"/v1/admin/users/"+u.ID+"/nhi-key", admin,
					map[string]any{"public_key": base64.StdEncoding.EncodeToString(pub)}, nil); code != http.StatusOK {
					t.Fatalf("set the agent's key = %d", code)
				}
			}
			ses, err := app.store.Sessions().Create(ctx, store.Session{UserID: u.ID, HarnessName: "claude-code", Status: store.SessionActive})
			if err != nil {
				t.Fatal(err)
			}
			return revokeTarget{session: ses.ID, user: u}
		}
	}

	type answer = func(t *testing.T, tg revokeTarget) (int, http.Header, map[string]any)
	adminRevoke := func(t *testing.T, tg revokeTarget) (int, http.Header, map[string]any) {
		return callJSON(t, http.MethodPost, base+"/v1/admin/sessions/"+tg.session+"/revoke", admin, nil)
	}
	selfRevoke := func(t *testing.T, tg revokeTarget) (int, http.Header, map[string]any) {
		return callJSON(t, http.MethodPost, base+"/v1/session/revoke", tg.token, map[string]any{})
	}
	standDown := func(t *testing.T, tg revokeTarget) (int, http.Header, map[string]any) {
		return callJSON(t, http.MethodPost, base+"/v1/admin/sessions/revoke", admin, map[string]any{"sessions": []string{tg.session}})
	}
	removeKey := func(t *testing.T, tg revokeTarget) (int, http.Header, map[string]any) {
		return callJSON(t, http.MethodDelete, base+"/v1/admin/users/"+tg.user.ID+"/nhi-key", admin, nil)
	}
	stampedCheckin := func(t *testing.T, tg revokeTarget) (int, http.Header, map[string]any) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/checkin", nil)
		if app.keepStampedSession(rec, r, tg.user, tg.session, "a-stamp-of-no-registered-key", harnessInfo{Name: "claude-code"}) {
			t.Fatal("a stamp of no registered key was kept")
		}
		var out map[string]any
		_ = json.NewDecoder(rec.Body).Decode(&out)
		return rec.Code, rec.Header(), out
	}
	deviceRevoke := func(t *testing.T, tg revokeTarget) (int, http.Header, map[string]any) {
		return callJSON(t, http.MethodDelete, base+"/v1/admin/users/"+tg.user.ID+"/devices/"+tg.device, admin, nil)
	}
	reusedKey := func(t *testing.T, tg revokeTarget) (int, http.Header, map[string]any) {
		code, out := tg.holder.call("/v1/checkin", "", tg.holder.checkinBody(1))
		return code, http.Header{}, out
	}

	routes := []struct {
		name   string
		fault  string
		target func(t *testing.T) revokeTarget
		call   answer
		status int
		// says is the armed answer's sentence, given the target and the
		// answer's request id.
		says  func(tg revokeTarget, id string) string
		rerun answer
		// rerunStatus answers the rerun, and outcome and reason are its
		// session.end record, "" when the route writes none.
		rerunStatus     int
		outcome, reason string
		standDowns      int
	}{
		{name: "the admin session revoke", fault: "sessions.revoke", target: session, call: adminRevoke,
			status: http.StatusInternalServerError,
			says: func(tg revokeTarget, id string) string {
				return fmt.Sprintf(sessionNotRevokedMsg, tg.session, tg.session, id)
			},
			rerun: adminRevoke, rerunStatus: http.StatusOK, outcome: "revoked-admin", reason: "revoked by admin"},
		{name: "the self revoke", fault: "sessions.revoke", target: session, call: selfRevoke,
			status: http.StatusInternalServerError,
			says: func(tg revokeTarget, id string) string {
				return fmt.Sprintf(sessionNotEndedMsg, tg.session, tg.session, id)
			},
			rerun: selfRevoke, rerunStatus: http.StatusOK, outcome: "revoked-self", reason: "revoked by session owner (logout)"},
		{name: "the stand-down", fault: "sessions.revoke", target: session, call: standDown,
			status: http.StatusInternalServerError,
			says:   func(_ revokeTarget, id string) string { return fmt.Sprintf(sessionsNotEndedMsg, "0 sessions", id) },
			rerun:  standDown, rerunStatus: http.StatusOK, standDowns: 1},
		{name: "the agent key removal", fault: "sessions.revoke", target: agentSession(true), call: removeKey,
			status: http.StatusInternalServerError,
			says: func(tg revokeTarget, _ string) string {
				return "The key of " + tg.user.Username + " is removed" + nhiKeySessionsLeftMsg
			},
			rerun: adminRevoke, rerunStatus: http.StatusOK, outcome: "revoked-admin", reason: "revoked by admin"},
		{name: "the second key stamp check of a check-in", fault: "sessions.revoke", target: agentSession(false), call: stampedCheckin,
			status: http.StatusUnauthorized, says: func(revokeTarget, string) string { return staleKeyStampMsg },
			rerun: stampedCheckin, rerunStatus: http.StatusUnauthorized},
		{name: "the admin device revoke", fault: "devices.revoke", target: device(false), call: deviceRevoke,
			status: http.StatusInternalServerError,
			says:   func(tg revokeTarget, id string) string { return fmt.Sprintf(deviceNotRevokedMsg, tg.device, id) },
			rerun:  deviceRevoke, rerunStatus: http.StatusOK},
		{name: "the key reuse refusal", fault: "devices.revoke", target: device(true), call: reusedKey,
			status: http.StatusForbidden, says: func(revokeTarget, string) string { return keyReusedMsg },
			rerun: deviceRevoke, rerunStatus: http.StatusOK},
	}
	standDownRecords := func() int {
		n := 0
		for _, ev := range adminAuditEvents(t, app) {
			if ev["action"] == "sessions.bulk-revoke" {
				n++
			}
		}
		return n
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			tg := rt.target(t)
			standDownsBefore := standDownRecords()
			fs.arm(rt.fault, pgDown)
			code, hdr, out := rt.call(t, tg)
			fs.disarm()
			id := hdr.Get(requestIDHeader)
			if want := rt.says(tg, id); code != rt.status || out["error"] != want {
				t.Fatalf("with the write refused = %d %q, want %d %q", code, out["error"], rt.status, want)
			}
			if rt.status >= http.StatusInternalServerError && (id == "" || out[correlationKey] != id) {
				t.Errorf("the answer's correlation id %v, header %q, want both the request id", out[correlationKey], id)
			}
			requireRevokeWrites(t, app, tg, 0, 0)
			if tg.token != "" {
				if code, dec := decide(t, base, tg.token, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "git status"}); code != http.StatusOK || dec["ruleId"] != "revoked" {
					t.Errorf("a decision on the session = %d %v, want this server to deny it by rule revoked", code, dec)
				}
			}

			if code, _, out := rt.rerun(t, tg); code != rt.rerunStatus {
				t.Fatalf("the rerun = %d %v, want %d", code, out, rt.rerunStatus)
			}
			wantEnds := 0
			if rt.outcome != "" {
				wantEnds = 1
			}
			requireRevokeWrites(t, app, tg, 1, wantEnds)
			if ends := sessionEnds(t, app, tg.session); wantEnds == 1 && len(ends) == 1 {
				if e := ends[0]; e["userId"] != tg.user.ID || e["outcome"] != rt.outcome || e["reason"] != rt.reason {
					t.Errorf("session.end = %v, want userId %s, outcome %s and reason %q", e, tg.user.ID, rt.outcome, rt.reason)
				}
			}
			if n := standDownRecords() - standDownsBefore; n != rt.standDowns {
				t.Errorf("sessions.bulk-revoke records = %d, want %d, and none while the write was refused", n, rt.standDowns)
			}
		})
	}
}

// TestRevokeOnAGoneClient pins the revokes a client's hang-up cannot stop.
// A logout and a key reuse refusal whose request context has ended still
// end their target with one revocation row, one event and the route's
// session.end, because the write runs on afterCommit's context. A
// stand-down whose context ended before its first chunk ends nothing, and
// this server's denylist refuses every id it did not try.
func TestRevokeOnAGoneClient(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	gone, leave := context.WithCancel(context.Background())
	leave()
	session := func(t *testing.T) revokeTarget {
		tok, id := checkinToken(t, app, base)
		return revokeTarget{session: id, token: tok, user: kim}
	}
	cases := []struct {
		name       string
		target     func(t *testing.T) revokeTarget
		run        func(t *testing.T, tg revokeTarget)
		rows, ends int
	}{
		{"a logout whose client left", session, func(t *testing.T, tg revokeTarget) {
			if rec := serveOn(gone, app, http.MethodPost, "/v1/session/revoke", tg.token, "{}"); rec.Code != http.StatusOK {
				t.Fatalf("self revoke = %d %s, want 200", rec.Code, rec.Body.String())
			}
			if ends := sessionEnds(t, app, tg.session); len(ends) == 1 && (ends[0]["outcome"] != "revoked-self" || ends[0]["userId"] != kim.ID) {
				t.Errorf("session.end = %v, want outcome revoked-self for %s", ends[0], kim.ID)
			}
		}, 1, 1},
		{"a key reuse whose client left", func(t *testing.T) revokeTarget {
			h := newKeyHolder(t, base)
			h.enroll(idToken, "sha256:l4-gone-client")
			return revokeTarget{device: h.device, holder: h, user: kim}
		}, func(t *testing.T, tg revokeTarget) {
			r := httptest.NewRequest(http.MethodPost, "/v1/checkin", nil).WithContext(gone)
			if !app.revokeReusedKey(r, deviceRow(t, app, tg.device), 1) {
				t.Fatal("revokeReusedKey reported the row already gone")
			}
			if n := len(reuseRecords(t, app, tg.device)); n != 1 {
				t.Errorf("device.key.reused records = %d, want 1", n)
			}
		}, 1, 0},
		{"a stand-down whose context ended before its first chunk", session, func(t *testing.T, tg revokeTarget) {
			if ended, err := app.endSessions(gone, []string{tg.session}, "l4 stand-down"); len(ended) != 0 || !errors.Is(err, context.Canceled) {
				t.Fatalf("endSessions = %v, %v, want nothing ended and context.Canceled", ended, err)
			}
		}, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tg := tc.target(t)
			tc.run(t, tg)
			requireRevokeWrites(t, app, tg, tc.rows, tc.ends)
		})
	}
}

// TestRevokeRaceAndReplayRead pins two answers of the revoke routes. A
// device that another revoke deleted between the route's lookup and its
// revoke answers the lookup's 404 and runs nothing else. A replayed session
// revoke whose read of the row fails answers the 500 that says nothing
// ended and names the request id, and fires nothing again.
func TestRevokeRaceAndReplayRead(t *testing.T) {
	t.Parallel()
	app, base, fs := testAppFault(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	admin, _ := checkinToken(t, app, base)

	t.Run("a device another revoke deleted first", func(t *testing.T) {
		h := newKeyHolder(t, base)
		h.enroll(loginDeviceFlow(t, base, "kim", "hunter2!"), "sha256:l4-race")
		fs.arm("devices.revoke", store.ErrNotFound)
		code, _, out := callJSON(t, http.MethodDelete, base+"/v1/admin/users/"+kim.ID+"/devices/"+h.device, admin, nil)
		fs.disarm()
		if code != http.StatusNotFound || out["error"] != "no such device for this user" {
			t.Fatalf("revoke of a device gone in between = %d %v, want 404 no such device for this user", code, out)
		}
		events := matching(t, app, "straza.revocation.device", func(d map[string]any) bool { return d["device"] == h.device })
		if app.denylist.deviceBlocked(h.device) || len(events) != 0 {
			t.Errorf("denylisted %v with %d events, want nothing run for a row another revoke ended", app.denylist.deviceBlocked(h.device), len(events))
		}
	})
	t.Run("a replay whose read of the session fails", func(t *testing.T) {
		_, id := checkinToken(t, app, base)
		path := base + "/v1/admin/sessions/" + id + "/revoke"
		if code, _, out := callJSON(t, http.MethodPost, path, admin, nil); code != http.StatusOK {
			t.Fatalf("first revoke = %d %v", code, out)
		}
		fs.arm("session-read:"+id, pgDown)
		code, hdr, out := callJSON(t, http.MethodPost, path, admin, nil)
		fs.disarm()
		rid := hdr.Get(requestIDHeader)
		if want := fmt.Sprintf(sessionUncheckedMsg, id, id, rid); code != http.StatusInternalServerError || out["error"] != want || out[correlationKey] != rid {
			t.Fatalf("replay with the read failing = %d %v, want 500 %q", code, out, want)
		}
		requireRevokeWrites(t, app, revokeTarget{session: id, user: kim}, 1, 1)
	})
}
