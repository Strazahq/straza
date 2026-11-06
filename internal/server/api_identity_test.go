package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/logging"
	"github.com/strazahq/straza/internal/store"
)

// testApp boots a full standalone strazad (sqlite + embedded NATS) on an
// ephemeral port and returns the App plus its base URL. Mutators may adjust
// the config before boot.
func testApp(t *testing.T, mutators ...func(*config.Config)) (*App, string) {
	t.Helper()
	return testAppPreRun(t, nil, mutators...)
}

// testAppPreRun is testApp with post-construction, pre-Run App hooks, for
// tests that must shape App state (cached pins, config knobs whose files must
// not be loaded at construction) without racing the Run goroutine.
func testAppPreRun(t *testing.T, preRun []func(*App), mutators ...func(*config.Config)) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		Profile: config.ProfileStandalone,
		DataDir: dir,
		Server:  config.Server{Listen: "127.0.0.1:0"},
		Log:     config.Log{Level: "error", Format: "json"},
		Store:   config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")},
		Events:  config.Events{Embedded: true},
		Apps:    config.Apps{AllowLoopbackUpstreams: true},
		Governance: config.Governance{
			OfflineGraceTTL:   15 * time.Minute,
			LocalToolDefault:  config.EffectAllow,
			AuditBackpressure: config.BackpressureDrop,
		},
	}
	for _, mutate := range mutators {
		mutate(&cfg)
	}
	seedStoreTemplate(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	app, err := New(ctx, cfg, logging.New(cfg.Log, io.Discard))
	if err != nil {
		cancel()
		t.Fatalf("New: %v", err)
	}
	// PublicURL must match the bound port for issuer/token issuer identity.
	base := "http://" + app.Addr()
	app.cfg.Server.PublicURL = base
	tokens, err := authn.NewTokenService(ctx, app.store.SigningKeys(), base, 0)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	app.tokens = tokens
	app.http.Handler = app.routes() // re-bind routes over the updated issuer URL

	// preRun hooks mutate the constructed App BEFORE the Run goroutine exists:
	// the goroutine-spawn edge is what orders these writes before Run's boot
	// reads (the announce lines) and every handler read. Mutating app/cfg after
	// this point races with Run; the race detector caught exactly that.
	for _, pre := range preRun {
		pre(app)
	}

	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("app shutdown timed out")
		}
	})
	return app, base
}

// seedIdentity creates a local user (password login), roles dev⇒reader, and
// a knowledge pack bound to reader (delivered transitively via implication).
func seedIdentity(t *testing.T, app *App) store.User {
	t.Helper()
	ctx := context.Background()
	hash, err := testPasswordHash("hunter2!")
	if err != nil {
		t.Fatal(err)
	}
	u, err := app.store.Users().Create(ctx, store.User{Username: "kim", Email: "kim@x.io", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	// Application kind: the fixture dev carries direct tool bindings, which
	// is the application shape, and revision 16 lets only application-kind
	// roles appear in match.roles (most policy fixtures match [dev]).
	dev, _ := app.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	reader, _ := app.store.Roles().Create(ctx, store.Role{Name: "reader"})
	_ = app.store.Roles().AddImplication(ctx, dev.ID, reader.ID)
	if _, err := app.store.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: dev.ID,
	}); err != nil {
		t.Fatal(err)
	}
	pack, _ := app.store.Packs().Create(ctx, store.KnowledgePack{Name: "golang-style", Version: "1", Content: "use gofmt", Checksum: "c1"})
	_ = app.store.Packs().Bind(ctx, reader.ID, pack.ID)
	app.resolver.Bump()
	return u
}

// loginDeviceFlow runs the RFC 8628 flow as straza would and returns the
// ID token. Single poll after approval, no interval waits needed.
func loginDeviceFlow(t *testing.T, base, username, password string) string {
	t.Helper()
	resp, err := http.PostForm(base+"/oidc/device_authorization", url.Values{"client_id": {"straza"}})
	if err != nil {
		t.Fatal(err)
	}
	var auth map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&auth)
	_ = resp.Body.Close()

	resp, err = http.PostForm(base+"/oidc/device", url.Values{
		"user_code": {auth["user_code"].(string)}, "username": {username}, "password": {password},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(page), "Signed in") {
		t.Fatalf("login failed: %s", page)
	}

	resp, err = http.PostForm(base+"/oidc/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {auth["device_code"].(string)},
		"client_id":   {"straza"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var tok map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	_ = resp.Body.Close()
	idt, _ := tok["id_token"].(string)
	if idt == "" {
		t.Fatalf("no id_token: %v", tok)
	}
	return idt
}

func postJSON(t *testing.T, urlStr string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(urlStr, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("non-JSON from %s: %v", urlStr, err)
	}
	return resp.StatusCode, out
}

// TestEnrollAndCheckinFlow drives enrollment end to end: device-code login,
// enroll, checkin with packs, offline token verification, refresh, and the
// disabled-user refusal.
func TestEnrollAndCheckinFlow(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	// Enroll a machine key; a new key on the same fingerprint takes over the
	// same device.
	enroll := newKeyHolder(t, base).enroll(idToken, "sha256:fp1")
	if enroll["device_id"] == "" || enroll["user_id"] != user.ID {
		t.Fatalf("enroll = %v", enroll)
	}
	h := newKeyHolder(t, base)
	if again := h.enroll(idToken, "sha256:fp1"); again["device_id"] != enroll["device_id"] {
		t.Fatalf("re-enroll took another device: %v vs %v", again, enroll)
	}
	deviceID := h.device

	// Checkin: session starts, roles resolve (dev + implied reader), the
	// reader-bound pack arrives, attestation caps at advisory.
	code, checkin := h.checkin()
	if code != http.StatusOK {
		t.Fatalf("checkin = %d %v", code, checkin)
	}
	if got := checkin["attestation"]; got != "advisory" {
		t.Errorf("attestation = %v, want advisory", got)
	}
	roles, _ := checkin["roles"].([]any)
	if len(roles) != 2 || roles[0] != "dev" || roles[1] != "reader" {
		t.Errorf("roles = %v, want [dev reader]", roles)
	}
	packs, _ := checkin["knowledge_packs"].([]any)
	if len(packs) != 1 || packs[0].(map[string]any)["name"] != "golang-style" {
		t.Errorf("packs = %v", packs)
	}
	if got := checkin["start_text"]; got != agentguard.StartText("kim", true) {
		t.Errorf("start_text = %v, want the governance paragraph for kim with the whoami sentence", got)
	}
	if e, ok := app.subjects.lookup(checkin["session_id"].(string)); !ok ||
		!slices.Equal(e.facts.packs, []whoamiPack{{Name: "golang-style", Version: packs[0].(map[string]any)["version"].(string)}}) {
		t.Errorf("the cached facts carry packs %+v (%v), want the delivered golang-style pack", e.facts.packs, ok)
	}
	if ttl := checkin["expires_in"].(float64); ttl < 250 || ttl > 300 {
		t.Errorf("expires_in = %v, want ~300", ttl)
	}

	// The session token verifies offline with the right claims.
	claims, err := app.tokens.Verify(checkin["session_token"].(string))
	if err != nil {
		t.Fatalf("offline verify: %v", err)
	}
	sessionID := checkin["session_id"].(string)
	if claims.Session != sessionID || claims.Subject != user.ID ||
		claims.Device != deviceID || claims.Harness != "claude-code/2.1.0" ||
		claims.Attestation != "advisory" {
		t.Errorf("token claims = %+v", claims)
	}

	// Refresh via session token, with a proof from the key, keeps the same
	// session.
	token := checkin["session_token"].(string)
	code, refresh := h.call("/v1/checkin", token, map[string]any{
		"session_token": token,
		"harness":       map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation":   map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:abc123"}},
	})
	if code != http.StatusOK || refresh["session_id"] != sessionID {
		t.Fatalf("refresh = %d %v", code, refresh)
	}

	// Revoked session refuses refresh.
	_ = app.store.Sessions().SetStatus(context.Background(), sessionID, store.SessionRevoked)
	token = refresh["session_token"].(string)
	code, body := h.call("/v1/checkin", token, map[string]any{
		"session_token": token,
		"harness":       map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation":   map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("revoked session refresh = %d %v", code, body)
	}

	// Disabled user is refused at checkin.
	u := user
	u.Status = store.UserDisabled
	if _, err := app.store.Users().Update(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	code, body = postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	if code != http.StatusForbidden || !strings.Contains(body["error"].(string), "disabled") {
		t.Fatalf("disabled user checkin = %d %v", code, body)
	}

	// Outbox recorded the identity events (producer path).
	pending, err := app.store.Outbox().ListUnpublished(context.Background(), 50)
	if err != nil || len(pending) < 2 {
		t.Errorf("outbox events = %d, %v", len(pending), err)
	}
}

// TestRefreshPreservesDeviceAndAttestation pins the session-scoped claim
// contract: a refresh cannot re-derive device binding or attestation from the
// request payload; both are fixed at session start from the session row.
// The daemon refreshes with an empty attestation and no device_id
// (client.Refresh has no device field). Deriving from the payload would
// strip claims.Device (breaking device revocation) and downgrade
// attestation to "none".
func TestRefreshPreservesDeviceAndAttestation(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	h := newKeyHolder(t, base)
	h.enroll(idToken, "sha256:fp-refresh")
	deviceID := h.device
	token, sessionID := h.mustCheckin()

	// Refresh exactly as the client does: session token only, empty attestation.
	code, refresh := h.call("/v1/checkin", token, map[string]any{
		"session_token": token,
		"harness":       map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation":   map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	if code != http.StatusOK || refresh["session_id"] != sessionID {
		t.Fatalf("refresh = %d %v", code, refresh)
	}
	if got := refresh["attestation"]; got != "advisory" {
		t.Errorf("refresh attestation = %v, want advisory (preserved from session start)", got)
	}

	claims, err := app.tokens.Verify(refresh["session_token"].(string))
	if err != nil {
		t.Fatalf("verify refreshed token: %v", err)
	}
	if claims.Device != deviceID {
		t.Errorf("refreshed claims.Device = %q, want %q (device revocation must keep matching)", claims.Device, deviceID)
	}
	if claims.Attestation != "advisory" {
		t.Errorf("refreshed claims.Attestation = %q, want advisory", claims.Attestation)
	}
	sub, ok := app.subjects.get(sessionID)
	if !ok {
		t.Fatal("subject cache lost the session on refresh")
	}
	if sub.DeviceCert {
		t.Error("subject cache DeviceCert = true: no device certificate exists until D6 ships; honest-false everywhere (TestSubjectDeviceCertHonestFalse pins the checkin side)")
	}
	if sub.Attestation != "advisory" {
		t.Errorf("subject cache Attestation = %q after refresh, want advisory", sub.Attestation)
	}
}

// TestCheckinContractFixtures replays the spec/attestation examples verbatim
// (contract tests come from spec fixtures, not private copies). The examples
// that name a device replay against the kit row without a key and the device
// token that a straza client from before the key lane holds, so the three
// bearer examples assert the retired lane's refusal.
func TestCheckinContractFixtures(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	legacy, legacyToken := mintDevice(t, app, kim.ID, store.DeviceClientKit, "sha256:fx")

	fixtureDir := filepath.Join("..", "..", "spec", "attestation", "examples")
	cases := []struct {
		file        string
		wantCode    int
		wantAttest  string
		wantError   string
		needsDevice bool
	}{
		{"checkin-valid-advisory.json", http.StatusForbidden, "", wantOldClientMsg, true},
		{"checkin-valid-managed.json", http.StatusForbidden, "", wantOldClientMsg, true},
		{"checkin-valid-none.json", http.StatusOK, "none", "", false},
		{"checkin-valid-device-token.json", http.StatusForbidden, "", wantOldClientMsg, true},
		{"checkin-valid-client.json", http.StatusOK, "none", "", false},
		{"checkin-invalid-no-auth.json", http.StatusBadRequest, "", "", false},
		{"checkin-invalid-harness-name-long.json", http.StatusBadRequest, "", fmt.Sprintf(harnessNameTooLongMsg, 257, checkinTextMax), false},
		{"checkin-invalid-harness-name-nul.json", http.StatusBadRequest, "", fmt.Sprintf(checkinNULMsg, "harness.name", nulStepStraza), false},
		{"checkin-valid-key.json", http.StatusUnauthorized, "", "", true},
		{"checkin-invalid-key-counter-zero.json", http.StatusBadRequest, "", "", true},
		{"checkin-invalid-key-with-token.json", http.StatusBadRequest, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(fixtureDir, tc.file))
			if err != nil {
				t.Fatalf("fixture: %v", err)
			}
			payload := strings.ReplaceAll(string(raw), "${ID_TOKEN}", idToken)
			if tc.needsDevice {
				payload = strings.ReplaceAll(payload, "${DEVICE_ID}", legacy.ID)
				payload = strings.ReplaceAll(payload, "${DEVICE_TOKEN}", legacyToken)
			}
			resp, err := http.Post(base+"/v1/checkin", "application/json", strings.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			var body map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&body)
			if resp.StatusCode != tc.wantCode {
				t.Fatalf("code = %d %v, want %d", resp.StatusCode, body, tc.wantCode)
			}
			if tc.wantAttest != "" && body["attestation"] != tc.wantAttest {
				t.Errorf("attestation = %v, want %s", body["attestation"], tc.wantAttest)
			}
			if tc.wantError != "" && body["error"] != tc.wantError {
				t.Errorf("error = %v, want %q", body["error"], tc.wantError)
			}
		})
	}
}

// TestDeviceTokenOpensNoDoor pins that a device credential strazactl holds
// from before it stopped signing in opens no door: check-in refuses it with
// the strazactl sentence, a disabled user's or a denylisted device's
// credential is refused for that first, and the admin API and the gateway
// refuse it.
func TestDeviceTokenOpensNoDoor(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	u := seedIdentity(t, app)
	d, devTok := mintDevice(t, app, u.ID, store.DeviceClientHuman, "host:kim-laptop")
	checkin := func() (int, map[string]any) { return checkinDeviceAs(t, base, devTok, "strazactl", "2.1.0") }

	if code, body := checkin(); code != http.StatusForbidden || body["error"] != wantStrazactlMsg {
		t.Fatalf("device_token checkin = %d %v, want 403 with the strazactl sentence", code, body)
	}
	if code := adminReq(t, "GET", base+"/v1/admin/users", devTok, nil, nil); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Errorf("admin API with device token = %d, want refusal", code)
	}
	req, _ := http.NewRequest("GET", base+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+devTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("gateway with device token = %d, want 401", resp.StatusCode)
	}

	// Store-side revocation: a disabled user's device credential is refused.
	u.Status = store.UserDisabled
	if _, err := app.store.Users().Update(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if code, body := checkin(); code != http.StatusForbidden || body["error"] != disabledUserMsg {
		t.Fatalf("disabled-user device checkin = %d %v, want 403 for the disabled user", code, body)
	}
	u.Status = store.UserActive
	if _, err := app.store.Users().Update(context.Background(), u); err != nil {
		t.Fatal(err)
	}

	// Kill-switch path: an in-memory device denylist entry is enforced
	// before any store read (push → instant).
	app.denylist.RevokeDevice(d.ID)
	if code, body := checkin(); code != http.StatusForbidden || body["error"] != revokedIdentityMsg {
		t.Fatalf("denylisted-device checkin = %d %v, want 403 for the revocation", code, body)
	}
}

// TestJoinHarness pins the one join every harness record and claim uses: the
// name/version form when a version is known, the bare name when it is not,
// so a hook without a version never records a dangling slash.
func TestJoinHarness(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, version, want string
	}{
		{"claude-code", "2.1.0", "claude-code/2.1.0"},
		{"codex", "", "codex"},
		{"", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/"+tc.version, func(t *testing.T) {
			if got := joinHarness(tc.name, tc.version); got != tc.want {
				t.Errorf("joinHarness(%q, %q) = %q, want %q", tc.name, tc.version, got, tc.want)
			}
		})
	}
}

// TestEmitEventTimePrecision pins one stamp precision across the server's
// event producers: the envelope time parses back to an instant inside the
// emit window at nanosecond resolution, which a whole-second stamp fails
// whenever the window opened after a second boundary.
func TestEmitEventTimePrecision(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()
	cases := []struct {
		subject string
		data    map[string]any
	}{
		{"straza.audit.identity", map[string]any{"action": "user.reactivated", "user": "u1", "origin": "admin"}},
		{"straza.revocation.lift", map[string]any{"user": "u1", "origin": "admin"}},
	}
	for _, tc := range cases {
		t.Run(tc.subject, func(t *testing.T) {
			before := time.Now().UTC()
			app.emitEventCtx(ctx, tc.subject, tc.data)
			after := time.Now().UTC()
			recent, err := app.store.Outbox().ListRecent(ctx, 20)
			if err != nil {
				t.Fatalf("ListRecent: %v", err)
			}
			var stamp string
			for _, ev := range recent {
				if ev.Subject != tc.subject {
					continue
				}
				var ce struct {
					Time string `json:"time"`
				}
				if err := json.Unmarshal([]byte(ev.CE), &ce); err != nil {
					t.Fatalf("decode CE: %v", err)
				}
				stamp = ce.Time
				break
			}
			if stamp == "" {
				t.Fatalf("no %s row in the outbox", tc.subject)
			}
			at, err := time.Parse(time.RFC3339Nano, stamp)
			if err != nil {
				t.Fatalf("time %q: %v", stamp, err)
			}
			if at.Before(before) || at.After(after) {
				t.Errorf("time %q outside the emit window [%s, %s]: the stamp lost precision",
					stamp, before.Format(time.RFC3339Nano), after.Format(time.RFC3339Nano))
			}
		})
	}
}
