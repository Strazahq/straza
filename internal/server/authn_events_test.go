package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// authnData lists the audit chain's straza.audit.authn payloads in seq order.
func authnData(t *testing.T, app *App) []map[string]any {
	t.Helper()
	recs, err := app.store.Audit().List(context.Background(), 0, 1000)
	if err != nil {
		t.Fatalf("audit list: %v", err)
	}
	var out []map[string]any
	for _, r := range recs {
		var ce struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(r.CE), &ce); err != nil {
			continue
		}
		if ce.Type == "straza.audit.authn" {
			out = append(out, ce.Data)
		}
	}
	return out
}

// waitAuthn polls the chain until an authn payload satisfies pred.
func waitAuthn(t *testing.T, app *App, what string, pred func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, d := range authnData(t, app) {
			if pred(d) {
				return d
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("audit chain never carried the authn event: %s", what)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func countAuthn(t *testing.T, app *App, pred func(map[string]any) bool) int {
	t.Helper()
	n := 0
	for _, d := range authnData(t, app) {
		if pred(d) {
			n++
		}
	}
	return n
}

// TestAuthnEventProducers drives the spec/events rev 21 producer matrix on
// one live app: login success on the id-token lane (with harness, sourceIp,
// userAgent), refresh successes unlogged, every refusal lane, the password
// lane via the built-in issuer (post-zeroing username), session.end on self
// revoke (transition only, replay silent), admin revoke, and the janitor's
// idle close (no client-connection fields).
func TestAuthnEventProducers(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)

	// 1. Interactive login success: id-token checkin from the self-service page.
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	code, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "self-service", "version": "1"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	if code != 200 {
		t.Fatalf("checkin = %d %v", code, checkin)
	}
	sesID, _ := checkin["session_id"].(string)
	sesTok, _ := checkin["session_token"].(string)
	success := waitAuthn(t, app, "login success", func(d map[string]any) bool {
		return d["action"] == "login" && d["outcome"] == "success" && d["session"] == sesID
	})
	if success["via"] != "id-token" || success["user"] != "kim" || success["userId"] != kim.ID {
		t.Errorf("success payload = %v", success)
	}
	if success["harness"] != "self-service/1" {
		t.Errorf("success harness = %v, want self-service/1", success["harness"])
	}
	if ip, _ := success["sourceIp"].(string); ip == "" || strings.Contains(ip, ":") {
		t.Errorf("success sourceIp = %v, want a bare transport peer host", success["sourceIp"])
	}
	if ua, _ := success["userAgent"].(string); ua == "" {
		t.Errorf("success userAgent = %v, want the client UA", success["userAgent"])
	}

	// 2. A refresh success emits nothing: refresh, then force a refusal as the
	// ordering fence, then recount successes.
	if code, _ := postJSON(t, base+"/v1/checkin", map[string]any{
		"session_token": sesTok,
		"attestation":   map[string]any{"managed": false, "hashes": map[string]string{}},
	}); code != 200 {
		t.Fatalf("refresh = %d", code)
	}

	// 3. Refusal lanes, one per credential kind.
	if code, _ := postJSON(t, base+"/v1/checkin", map[string]any{
		"session_token": "not-a-session-token",
	}); code != 401 {
		t.Fatal("bad session token accepted")
	}
	sesFail := waitAuthn(t, app, "session-token refusal", func(d map[string]any) bool {
		return d["outcome"] == "failure" && d["via"] == "session-token"
	})
	if _, present := sesFail["user"]; present {
		t.Errorf("session-token refusal fabricated user: %v", sesFail)
	}
	if r, _ := sesFail["reason"].(string); r == "" {
		t.Errorf("session-token refusal carries no reason")
	}
	if n := countAuthn(t, app, func(d map[string]any) bool {
		return d["outcome"] == "success" && d["userId"] == kim.ID
	}); n != 1 {
		t.Errorf("kim success events = %d, want exactly 1 (refresh must not log)", n)
	}

	if code, _ := postJSON(t, base+"/v1/checkin", map[string]any{
		"device_token": "not-a-device-token",
	}); code != 401 {
		t.Fatal("bad device token accepted")
	}
	waitAuthn(t, app, "device-token refusal", func(d map[string]any) bool {
		return d["outcome"] == "failure" && d["via"] == "device-token"
	})

	if code, _ := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token": "not-an-id-token",
	}); code != 401 {
		t.Fatal("bad id token accepted")
	}
	waitAuthn(t, app, "id-token refusal", func(d map[string]any) bool {
		return d["outcome"] == "failure" && d["via"] == "id-token"
	})

	// 4. A 400 (no credential at all) is not an authentication outcome.
	if code, _ := postJSON(t, base+"/v1/checkin", map[string]any{}); code != 400 {
		t.Fatal("empty checkin != 400")
	}

	// 5. Password lane via the built-in issuer: wrong password for a real
	// user carries the username; an unknown user carries no user field.
	form := func(user, pass string) {
		t.Helper()
		resp, err := http.PostForm(base+"/oidc/device_authorization", url.Values{"client_id": {"straza"}})
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		userCode, _ := body["user_code"].(string)
		resp2, err := http.PostForm(base+"/oidc/device", url.Values{
			"user_code": {userCode}, "username": {user}, "password": {pass},
		})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp2.Body.Close()
	}
	form("kim", "wrong-password")
	pw := waitAuthn(t, app, "password refusal (known user)", func(d map[string]any) bool {
		return d["outcome"] == "failure" && d["via"] == "password" && d["user"] == "kim"
	})
	if ip, _ := pw["sourceIp"].(string); ip == "" {
		t.Errorf("password refusal sourceIp missing: %v", pw)
	}
	if _, present := pw["userId"]; present {
		t.Errorf("password refusal fabricated userId (the issuer never resolved one): %v", pw)
	}
	form("ghost", "whatever")
	anon := waitAuthn(t, app, "password refusal (unknown user)", func(d map[string]any) bool {
		if d["outcome"] != "failure" || d["via"] != "password" {
			return false
		}
		_, present := d["user"]
		return !present
	})
	if _, present := anon["userId"]; present {
		t.Errorf("unknown-user refusal fabricated userId: %v", anon)
	}

	// 6. Self revoke: session.end revoked-self on the transition, with the
	// client-connection fields; the idempotent replay emits nothing new.
	if code := adminReq(t, "POST", base+"/v1/session/revoke", sesTok, nil, nil); code != 200 {
		t.Fatalf("self revoke = %d", code)
	}
	end := waitAuthn(t, app, "session.end revoked-self", func(d map[string]any) bool {
		return d["action"] == "session.end" && d["outcome"] == "revoked-self" && d["session"] == sesID
	})
	if end["userId"] != kim.ID {
		t.Errorf("revoked-self userId = %v, want %s", end["userId"], kim.ID)
	}
	if ip, _ := end["sourceIp"].(string); ip == "" {
		t.Errorf("revoked-self sourceIp missing (a client connection produced it): %v", end)
	}
	if code := adminReq(t, "POST", base+"/v1/session/revoke", sesTok, nil, nil); code != 200 {
		t.Fatalf("self revoke replay = %d", code)
	}
	// Fence: a fresh failure lands after the replay, then the count holds.
	if code, _ := postJSON(t, base+"/v1/checkin", map[string]any{
		"session_token": "replay-fence-token",
	}); code != 401 {
		t.Fatal("fence refusal not refused")
	}
	waitAuthn(t, app, "post-replay fence", func(d map[string]any) bool {
		return d["outcome"] == "failure" && d["via"] == "session-token" &&
			strings.Contains(asString(d["reason"]), "rejected")
	})
	if n := countAuthn(t, app, func(d map[string]any) bool {
		return d["action"] == "session.end" && d["session"] == sesID
	}); n != 1 {
		t.Errorf("session.end events for %s = %d, want exactly 1 (replay must not re-emit)", sesID, n)
	}

	// 7. Admin single revoke: revoked-admin on the transition only.
	adminTok, _ := checkinToken(t, app, base)
	idToken2 := loginDeviceFlow(t, base, "kim", "hunter2!")
	code, checkin2 := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    idToken2,
		"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	if code != 200 {
		t.Fatalf("second checkin = %d", code)
	}
	ses2, _ := checkin2["session_id"].(string)
	if code := adminReq(t, "POST", base+"/v1/admin/sessions/"+ses2+"/revoke", adminTok, nil, nil); code != 200 {
		t.Fatalf("admin revoke = %d", code)
	}
	waitAuthn(t, app, "session.end revoked-admin", func(d map[string]any) bool {
		return d["action"] == "session.end" && d["outcome"] == "revoked-admin" && d["session"] == ses2
	})

	// 8. Janitor idle close: userId rides, the client-connection fields do
	// not (no client connection produced the event).
	idle, err := app.store.Sessions().Create(ctx, store.Session{UserID: kim.ID, HarnessName: "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.Sessions().Touch(ctx, idle.ID, time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	app.closeIdleSessions(ctx)
	closed := waitAuthn(t, app, "session.end idle-closed", func(d map[string]any) bool {
		return d["action"] == "session.end" && d["outcome"] == "idle-closed" && d["session"] == idle.ID
	})
	if closed["userId"] != kim.ID {
		t.Errorf("idle-closed userId = %v, want %s", closed["userId"], kim.ID)
	}
	for _, k := range []string{"sourceIp", "userAgent"} {
		if _, present := closed[k]; present {
			t.Errorf("idle-closed carries %s; janitor events must not: %v", k, closed)
		}
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// TestAuthnMinAttestationDeny pins the checkin.denied retirement and the
// approvals exemption: under minAttestation managed an unmanaged agent
// harness is refused WITH a chained authn failure naming both levels (and no
// un-chained identity.updated checkin.denied event exists anymore), while
// the approvals surface rides the admin-harness bootstrap exemption exactly
// like the console it replaces.
func TestAuthnMinAttestationDeny(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(cfg *config.Config) {
		cfg.Governance.MinAttestation = "managed"
	})
	_ = seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	code, body := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	if code != 403 {
		t.Fatalf("unattested agent under managed minimum = %d %v", code, body)
	}
	// The refusal is now a CHAINED authn failure naming both levels; the
	// outbox read below is the retirement pin for the old un-chained
	// identity.updated action (insert is synchronous with the 403, so if the
	// producer still existed the row would be sitting there; best-effort
	// because the relay may already have drained it).
	deny := waitAuthn(t, app, "attestation refusal", func(d map[string]any) bool {
		return d["outcome"] == "failure" && strings.Contains(asString(d["reason"]), "attestation")
	})
	if deny["user"] != "kim" || deny["via"] != "id-token" {
		t.Errorf("attestation refusal payload = %v", deny)
	}
	if !strings.Contains(asString(deny["reason"]), "managed") {
		t.Errorf("attestation refusal reason does not name the required level: %v", deny["reason"])
	}
	pending, err := app.store.Outbox().ListUnpublished(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range pending {
		if strings.Contains(ev.CE, "checkin.denied") {
			t.Errorf("retired checkin.denied identity event still produced: %s", ev.CE)
		}
	}

	// The self-service page rides the same bootstrap exemption as the
	// console: an unmanaged sign-in from it still mints.
	if code, body := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "self-service", "version": "1"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
	}); code != 200 {
		t.Fatalf("self-service under managed minimum = %d %v (admin-harness exemption)", code, body)
	}
}
