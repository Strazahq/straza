package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/clientassertion"
	"github.com/strazahq/straza/internal/store"
)

// adminRoute is one admin route a test fires, and the status a caller that
// its guard passes gets there.
type adminRoute struct {
	method, path string
	pass         int
}

// authnFailures lists the login failure records for userID, with reason
// when reason is not empty. It reads the outbox, which the emit writes
// before the answer goes out, so a count right after a request is exact.
func authnFailures(t *testing.T, app *App, userID, reason string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, d := range outboxDataFor(t, app, "straza.audit.authn") {
		if d["action"] == "login" && d["outcome"] == "failure" && d["userId"] == userID && (reason == "" || d["reason"] == reason) {
			out = append(out, d)
		}
	}
	return out
}

// sessionAs checks name in under harness with a fresh login and answers the
// session token and its id.
func sessionAs(t *testing.T, base, name, harness string) (string, string) {
	t.Helper()
	code, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    loginDeviceFlow(t, base, name, "hunter2!"),
		"harness":     map[string]string{"name": harness, "version": "1"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:x"}},
	})
	tok, _ := checkin["session_token"].(string)
	id, _ := checkin["session_id"].(string)
	if code != http.StatusOK || tok == "" {
		t.Fatalf("check-in of %s under %s = %d %v", name, harness, code, checkin)
	}
	return tok, id
}

// nhiGrantToken registers a fresh key for the agent as root and answers the
// ID token the built-in issuer's client_credentials grant mints for it.
func nhiGrantToken(t *testing.T, base, root string, agent store.User) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if code := adminReq(t, http.MethodPut, base+"/v1/admin/users/"+agent.ID+"/nhi-key", root,
		map[string]any{"public_key": base64.StdEncoding.EncodeToString(pub)}, nil); code != http.StatusOK {
		t.Fatalf("register the key of %s = %d", agent.Username, code)
	}
	assertion, err := clientassertion.Mint(priv, agent.Username, base, 0)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.PostForm(base+"/oidc/token", url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {agent.Username},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	idt, _ := body["id_token"].(string)
	if resp.StatusCode != http.StatusOK || idt == "" {
		t.Fatalf("grant for %s = %d %v", agent.Username, resp.StatusCode, body)
	}
	return idt
}

// TestAdminPlaneRefusesAgents pins that only a person uses the admin API: a
// person, a user with no type signal and an admin API token pass every
// guard, and an agent or a service account is refused on each guard built
// on authenticateAdmin and on the routes that decide a request, whatever
// roles it holds and whichever lane it signs in on, with an answer that
// carries nothing of an approval record, pending or decided. Each refused
// request writes exactly one login failure record that names the user and
// the reason, and a passing request writes none.
func TestAdminPlaneRefusesAgents(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	hash, err := testPasswordHash("hunter2!")
	if err != nil {
		t.Fatal(err)
	}
	typed := func(name, userType string) store.User {
		t.Helper()
		u, err := app.store.Users().Create(ctx, store.User{Username: name, PasswordHash: hash, UserType: userType})
		if err != nil {
			t.Fatal(err)
		}
		grantAdmin(t, app, u.ID)
		return u
	}
	hana := typed("hana", store.UserTypeHuman)
	hanaSession, _ := sessionAs(t, base, "hana", "strazactl")
	var minted struct {
		Token string `json:"token"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", root, map[string]any{"name": "ci", "scope": "full"}, &minted); code != http.StatusCreated {
		t.Fatalf("mint = %d", code)
	}
	ciBot := seedAgent(t, app, "ci-bot", "kim", AdminRole)
	svc := typed("svc", store.UserTypeService)
	svcToken, err := app.tokens.MintIDToken(svc.ID, "straza", time.Minute, "svc", "")
	if err != nil {
		t.Fatal(err)
	}
	legacy, legacyToken := untypedNHI(t, app, "legacy")
	grantAdmin(t, app, legacy.ID)
	pia := typed("pia", store.UserTypeHuman)
	piaSession, piaSessionID := sessionAs(t, base, "pia", "console")
	pia.UserType = store.UserTypeAgent
	if _, err := app.store.Users().Update(ctx, pia); err != nil {
		t.Fatal(err)
	}
	coder := seedAgent(t, app, "coder", "kim", AdminRole)
	coderSession, coderSessionID := sessionAs(t, base, "coder", "claude-code")
	pending, approved := seedApproval(t, app, "u-other", []string{"dev"}), seedApproval(t, app, "u-other", []string{"dev"})
	kimConsole, _ := checkinTokenAs(t, base, "console")
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/approvals/"+approved.ID+"/approve", kimConsole, nil, nil); code != http.StatusOK {
		t.Fatalf("kim's approve = %d", code)
	}

	reads := []adminRoute{
		{http.MethodGet, "/v1/admin/users", http.StatusOK},
		{http.MethodGet, "/v1/admin/apps", http.StatusOK},
		{http.MethodGet, "/v1/admin/drafts", http.StatusOK},
	}
	every := append(append([]adminRoute{}, reads...),
		adminRoute{http.MethodPost, "/v1/admin/signing-keys/client-assertion/rotate", 0},
		adminRoute{http.MethodPost, "/v1/admin/drafts/1/contact", 0},
		adminRoute{http.MethodPost, "/v1/admin/approvals/" + pending.ID + "/approve", 0},
		adminRoute{http.MethodPost, "/v1/admin/approvals/" + pending.ID + "/deny", 0},
		adminRoute{http.MethodPost, "/v1/admin/approvals/" + approved.ID + "/approve", 0},
		adminRoute{http.MethodPost, "/v1/approvals/self/enroll-token", 0})
	const reason = "only a person can use the admin API or decide a request"
	refusal := func(name, word string) string {
		return "the user " + name + " is " + word + ", and only a person can use the admin API or decide a request. " +
			"For automation, a person mints an admin API token with strazactl api-token create, " +
			"and an AI agent or a service account proposes config changes through the built-in straza MCP server's drafting tools, " +
			"which the role straza-draft-config lists."
	}
	cases := []struct {
		name, bearer string
		user         store.User
		says         string
		via, session string
	}{
		{"a person's strazactl session", hanaSession, hana, "", "", ""},
		{"a person's ID token with the audience straza", loginDeviceFlow(t, base, "hana", "hunter2!"), hana, "", "", ""},
		{"a user with no type signal", root, kim, "", "", ""},
		{"an admin API token with the scope full", minted.Token, store.User{}, "", "", ""},
		{"an agent's ID token from the NHI grant", nhiGrantToken(t, base, root, ciBot), ciBot, refusal("ci-bot", "an agent"), "id-token", ""},
		{"a service account's ID token", svcToken, svc, refusal("svc", "a service account"), "id-token", ""},
		{"an agent typed only by attrs kind nhi", legacyToken, legacy, refusal("legacy", "an agent"), "id-token", ""},
		{"a person's console session after the type changed to agent", piaSession, pia, refusal("pia", "an agent"), "session-token", piaSessionID},
		{"an agent's coding harness session", coderSession, coder, refusal("coder", "an agent"), "session-token", coderSessionID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.says == "" {
				for _, rt := range reads {
					if code, _, out := callJSON(t, rt.method, base+rt.path, tc.bearer, nil); code != rt.pass {
						t.Errorf("%s %s = %d %v, want %d", rt.method, rt.path, code, out, rt.pass)
					}
				}
				if tc.user.ID != "" {
					if got := authnFailures(t, app, tc.user.ID, ""); len(got) != 0 {
						t.Errorf("a passing caller wrote login failures: %v", got)
					}
				}
				return
			}
			for _, rt := range every {
				before := len(authnFailures(t, app, tc.user.ID, reason))
				code, _, out := callJSON(t, rt.method, base+rt.path, tc.bearer, map[string]string{"object": "App/probe"})
				if msg, _ := out["error"].(string); code != http.StatusForbidden || msg != tc.says || len(out) != 1 {
					t.Errorf("%s %s = %d %v, want 403 with %q alone", rt.method, rt.path, code, out, tc.says)
				}
				recs := authnFailures(t, app, tc.user.ID, reason)
				if len(recs) != before+1 {
					t.Fatalf("%s %s wrote %d records, want exactly 1", rt.method, rt.path, len(recs)-before)
				}
				rec := recs[len(recs)-1]
				if rec["user"] != tc.user.Username || rec["via"] != tc.via || asString(rec["session"]) != tc.session {
					t.Errorf("record = %v, want user %s via %s session %q", rec, tc.user.Username, tc.via, tc.session)
				}
			}
		})
	}
	for id, want := range map[string]string{pending.ID: "pending", approved.ID: "approved"} {
		if rec, err := app.approval.Get(ctx, id); err != nil || string(rec.State) != want || (want == "approved" && rec.DecidedByName != "kim") {
			t.Errorf("approval %s reads %s by %q (%v), want %s as kim left it", id, rec.State, rec.DecidedByName, err, want)
		}
	}
}

// TestAdminSessionReadsUserState pins that a session on the admin plane and
// on the routes that decide is judged as a refresh judges it, on every
// request: a disabled, deleted, locked or revoked user, a revoked session
// and a revoked device are refused with the refresh's words and one login
// failure record per request, a user row that cannot be read while the
// store answers is refused in words that say so, and a store outage
// answers 503 with no record.
func TestAdminSessionReadsUserState(t *testing.T) {
	t.Parallel()
	app, base, fs := testAppFault(t)
	ctx := context.Background()
	routes := []adminRoute{
		{http.MethodGet, "/v1/admin/users", http.StatusOK},
		{http.MethodGet, "/v1/admin/apps", http.StatusOK},
		{http.MethodGet, "/v1/admin/drafts", http.StatusOK},
		{http.MethodPost, "/v1/admin/approvals/no-such-request/approve", http.StatusNotFound},
		{http.MethodGet, "/v1/self", http.StatusOK},
	}
	const disabled = "user is disabled. Contact your administrator"
	cases := []struct {
		name         string
		arm          func(u store.User, device, session string)
		code         int
		says, reason string
	}{
		{"an active person", func(store.User, string, string) {}, 0, "", ""},
		{"a user disabled in the store with the session row still active", func(u store.User, _, _ string) {
			u.Status = store.UserDisabled
			if _, err := app.store.Users().Update(ctx, u); err != nil {
				t.Fatal(err)
			}
		}, http.StatusForbidden, disabled, "user is disabled"},
		{"a user deleted with the session row still active", func(u store.User, _, _ string) {
			if _, err := app.store.Users().SoftDelete(ctx, u.ID); err != nil {
				t.Fatal(err)
			}
		}, http.StatusForbidden, disabled, "user is disabled"},
		{"a user on the denylist", func(u store.User, _, _ string) { app.denylist.revokeUser(u.ID) },
			http.StatusForbidden, revokedIdentityMsg, "device or user revoked"},
		{"a session on the denylist", func(_ store.User, _, session string) { app.denylist.revokeSession(session) },
			http.StatusUnauthorized, "session is no longer active", "session is no longer active"},
		{"a device on the denylist", func(_ store.User, device, _ string) { app.denylist.RevokeDevice(device) },
			http.StatusForbidden, revokedIdentityMsg, "device or user revoked"},
		{"the users read down", func(store.User, string, string) { fs.arm("users", pgDown) },
			http.StatusServiceUnavailable, loginOutageBody, ""},
		{"a user row that cannot be read while the store answers", func(store.User, string, string) { fs.arm("users", errors.New("boom")) },
			http.StatusForbidden, "Straza could not read your user record, so it refuses the request. Try again, and check the strazad log if it keeps failing.",
			"user record could not be read"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := fmt.Sprintf("sam%d", i)
			u := mkHuman(t, app, name, AdminRole)
			d, deviceToken := mintDevice(t, app, u.ID, store.DeviceClientHuman, "fp-"+name)
			code, checkin := checkinDeviceAs(t, base, deviceToken, "console", "1")
			tok, _ := checkin["session_token"].(string)
			sesID, _ := checkin["session_id"].(string)
			if code != http.StatusOK || tok == "" {
				t.Fatalf("console check-in = %d %v", code, checkin)
			}
			tc.arm(u, d.ID, sesID)
			defer fs.disarm()
			for _, rt := range routes {
				before := len(authnFailures(t, app, u.ID, ""))
				code, hdr, out := callJSON(t, rt.method, base+rt.path, tok, nil)
				msg, _ := out["error"].(string)
				recs := authnFailures(t, app, u.ID, "")
				switch tc.code {
				case 0:
					if code != rt.pass {
						t.Errorf("%s %s = %d %q, want %d", rt.method, rt.path, code, msg, rt.pass)
					}
					if len(recs) != before {
						t.Errorf("%s %s wrote a login failure for an active person: %v", rt.method, rt.path, recs[before:])
					}
				case http.StatusServiceUnavailable:
					requireOutage(t, rt.path, code, hdr, out)
					if len(recs) != before {
						t.Errorf("%s %s wrote a login failure for an outage: %v", rt.method, rt.path, recs[before:])
					}
				default:
					if code != tc.code || msg != tc.says {
						t.Errorf("%s %s = %d %q, want %d %q", rt.method, rt.path, code, msg, tc.code, tc.says)
					}
					if len(recs) != before+1 {
						t.Fatalf("%s %s wrote %d login failures, want exactly 1", rt.method, rt.path, len(recs)-before)
					}
					if rec := recs[len(recs)-1]; rec["reason"] != tc.reason || rec["via"] != "session-token" || rec["session"] != sesID {
						t.Errorf("record = %v, want reason %q via session-token session %s", rec, tc.reason, sesID)
					}
				}
			}
		})
	}
}

// SetStatus fails the status write of the one session armed as
// "session-status:<id>".
func (s faultSessions) SetStatus(ctx context.Context, id, status string) error {
	if err := s.f.fault("session-status:" + id); err != nil {
		return err
	}
	return s.SessionRepo.SetStatus(ctx, id, status)
}

// ListByUser fails when "session-list" is armed.
func (s faultSessions) ListByUser(ctx context.Context, userID string) ([]store.Session, error) {
	if err := s.f.fault("session-list"); err != nil {
		return nil, err
	}
	return s.SessionRepo.ListByUser(ctx, userID)
}

// faultRevocations fails the revocation record write when "revocations" is
// armed.
type faultRevocations struct {
	store.RevocationRepo
	f *faultStore
}

func (r faultRevocations) Create(ctx context.Context, rv store.Revocation) (store.Revocation, error) {
	if err := r.f.fault("revocations"); err != nil {
		return store.Revocation{}, err
	}
	return r.RevocationRepo.Create(ctx, rv)
}

func (f *faultStore) Revocations() store.RevocationRepo {
	return faultRevocations{f.Store.Revocations(), f}
}

// faultOutbox fails the outbox write of the subject armed as
// "outbox:<subject>".
type faultOutbox struct {
	store.OutboxRepo
	f *faultStore
}

func (o faultOutbox) Insert(ctx context.Context, e store.OutboxEvent) (store.OutboxEvent, error) {
	if err := o.f.fault("outbox:" + e.Subject); err != nil {
		return store.OutboxEvent{}, err
	}
	return o.OutboxRepo.Insert(ctx, e)
}

func (f *faultStore) Outbox() store.OutboxRepo { return faultOutbox{f.Store.Outbox(), f} }

// TestRevokeUserCountsOnlyWrittenSessions pins that a user revoke logs every
// write it could not make at Error, the revocation row, the session list,
// each session row and its two outbox records, counts in user.killed only
// the sessions whose row it marked revoked, and leaves no session the admin
// API admits. A revoked count of -1 means no user.killed record is stored.
func TestRevokeUserCountsOnlyWrittenSessions(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	app, base, fs := testAppFaultLog(t, log)
	ctx := context.Background()
	const (
		sessionLine = "user revoke: the session could not be marked revoked, so it stays active in the store and the denylist refuses it"
		listLine    = "user revoke: the user's sessions could not be listed, so none was marked revoked and the denylist refuses them"
		recordLine  = "user revoke: the revocation record could not be written, so the block holds on this replica until it restarts"
		eventLine   = "user revoke: the revocation event could not be written, so the other replicas miss the block until they restart"
		killedLine  = "user revoke: the user.killed record could not be written, so the audit chain holds no record of this revoke"
	)
	cases := []struct {
		name     string
		arm      func(sessions []string)
		sessions int
		revoked  float64
		line     string
		active   int
	}{
		{"one of two session writes fails", func(s []string) { fs.arm("session-status:"+s[0], pgDown) }, 2, 1, sessionLine, 1},
		{"the session list fails", func([]string) { fs.arm("session-list", pgDown) }, 1, 0, listLine, 1},
		{"the revocation record write fails", func([]string) { fs.arm("revocations", pgDown) }, 1, 1, recordLine, 0},
		{"the revocation event write fails", func([]string) { fs.arm("outbox:straza.revocation.user", pgDown) }, 1, 1, eventLine, 0},
		{"the user.killed write fails", func([]string) { fs.arm("outbox:straza.audit.identity", pgDown) }, 1, -1, killedLine, 0},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := fmt.Sprintf("rex%d", i)
			u := mkHuman(t, app, name, AdminRole)
			_, deviceToken := mintDevice(t, app, u.ID, store.DeviceClientHuman, "fp-"+name)
			var tokens, ids []string
			for range tc.sessions {
				code, checkin := checkinDeviceAs(t, base, deviceToken, "console", "1")
				if code != http.StatusOK {
					t.Fatalf("console check-in = %d %v", code, checkin)
				}
				tokens = append(tokens, checkin["session_token"].(string))
				ids = append(ids, checkin["session_id"].(string))
			}
			tc.arm(ids)
			app.revokeUserCtx(ctx, u.ID, "test", store.RevocationOriginAdmin)
			fs.disarm()
			var killed []map[string]any
			for _, d := range outboxDataFor(t, app, "straza.audit.identity") {
				if d["action"] == "user.killed" && d["user"] == u.ID {
					killed = append(killed, d)
				}
			}
			switch {
			case tc.revoked < 0 && len(killed) != 0:
				t.Errorf("user.killed = %v, want none after a failed write", killed)
			case tc.revoked >= 0 && (len(killed) != 1 || killed[0]["sessionsRevoked"] != tc.revoked):
				t.Errorf("user.killed = %v, want one record with sessionsRevoked %v", killed, tc.revoked)
			}
			var lines []string
			for _, line := range errorRecords(buf) {
				if strings.Contains(line, "user="+u.ID) {
					lines = append(lines, line)
				}
			}
			if len(lines) != 1 || !strings.Contains(lines[0], tc.line) {
				t.Errorf("Error records = %q, want exactly one with %q", lines, tc.line)
			}
			if tc.line == sessionLine && len(lines) == 1 && !strings.Contains(lines[0], "session="+ids[0]) {
				t.Errorf("Error record %q does not name the session %s", lines[0], ids[0])
			}
			active := 0
			for _, id := range ids {
				if ses, err := app.store.Sessions().GetByID(ctx, id); err == nil && ses.Status == store.SessionActive {
					active++
				}
			}
			if active != tc.active {
				t.Errorf("%d sessions stay active in the store, want %d", active, tc.active)
			}
			for _, tok := range tokens {
				if code, _, out := callJSON(t, http.MethodGet, base+"/v1/admin/users", tok, nil); code != http.StatusForbidden || out["error"] != revokedIdentityMsg {
					t.Errorf("the admin API answers a revoked user's session with %d %v, want 403 %q", code, out, revokedIdentityMsg)
				}
			}
		})
	}
}
