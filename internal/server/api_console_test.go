package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
)

// TestConsoleConfigEndpoint pins the /v1/admin/config contract:
// effective values reported, anything
// connection-shaped redacted to presence booleans.
func TestConsoleConfigEndpoint(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) {
		c.Governance.MinAttestation = config.AttestationAdvisory
	})
	user := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	// Admin-gated like the rest of the surface.
	if code := adminReq(t, "GET", base+"/v1/admin/config", "", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", code)
	}
	grantAdmin(t, app, user.ID)

	var cfg map[string]any
	if code := adminReq(t, "GET", base+"/v1/admin/config", idToken, nil, &cfg); code != http.StatusOK {
		t.Fatalf("config = %d", code)
	}
	gov, _ := cfg["governance"].(map[string]any)
	if gov["min_attestation"] != "advisory" || gov["audit_backpressure"] != "drop-with-counter" {
		t.Errorf("governance payload = %v", gov)
	}
	events, _ := cfg["events"].(map[string]any)
	if _, ok := events["embedded"]; !ok {
		t.Errorf("events payload lacks embedded: %v", events)
	}
	if _, ok := events["push_configured"]; ok {
		t.Errorf("events payload carries push_configured, a field of the removed direct NATS client lane: %v", events)
	}
	// The approval row is RESOLVED: zero config surfaces the built-in 120s
	// default the gateway actually holds, a set knob
	// surfaces itself.
	appr, _ := cfg["approval"].(map[string]any)
	if appr["gateway_hold_seconds"] != float64(120) {
		t.Errorf("gateway_hold_seconds = %v, want the 120 built-in default surfaced", appr["gateway_hold_seconds"])
	}
	app.cfg.Approval.GatewayHoldSeconds = 45
	if code := adminReq(t, "GET", base+"/v1/admin/config", idToken, nil, &cfg); code != http.StatusOK {
		t.Fatalf("config = %d", code)
	}
	appr, _ = cfg["approval"].(map[string]any)
	if appr["gateway_hold_seconds"] != float64(45) {
		t.Errorf("gateway_hold_seconds = %v, want the configured 45", appr["gateway_hold_seconds"])
	}
	app.cfg.Approval.GatewayHoldSeconds = 0
	if cfg["tls"] != false || cfg["store_driver"] != "sqlite" || cfg["profile"] != "standalone" {
		t.Errorf("config payload = %v", cfg)
	}

	// Redaction: raw response must not leak the NATS URL, DSN, or any path.
	req, _ := http.NewRequest("GET", base+"/v1/admin/config", nil)
	req.Header.Set("Authorization", "Bearer "+idToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	for _, secret := range []string{"nats://", "push.internal", app.cfg.Store.DSN, app.cfg.DataDir} {
		if secret != "" && strings.Contains(string(raw), secret) {
			t.Errorf("config response leaks %q: %s", secret, raw)
		}
	}

	// Capture posture: with no active set opting in, the
	// operator sees 0 capturing sets, no mode, the default 720 h retention,
	// and inline bodies.
	capt, _ := cfg["capture"].(map[string]any)
	if capt["policy_sets"] != float64(0) || capt["body_store"] != "inline" || capt["retention_hours"] != float64(720) {
		t.Errorf("capture payload = %v", capt)
	}
	if _, ok := capt["mode"]; ok {
		t.Errorf("mode must be omitted while nothing captures: %v", capt)
	}

	// A DRAFT capture set must not count; only activation flips the chip.
	const captureSet = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: capture-eu }
spec:
  match: { roles: [dev] }
  capture: { conversations: true, mode: redact }
  rules:
    - id: allow-read
      tools: [file.read]
      effect: allow
`
	if code, b, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", idToken, "application/yaml", []byte(captureSet)); code != http.StatusCreated {
		t.Fatalf("policy apply = %d %s", code, b)
	}
	if code := adminReq(t, "GET", base+"/v1/admin/config", idToken, nil, &cfg); code != http.StatusOK {
		t.Fatalf("config = %d", code)
	}
	capt, _ = cfg["capture"].(map[string]any)
	if capt["policy_sets"] != float64(0) {
		t.Errorf("draft capture set counted as active: %v", capt)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/policies/capture-eu/activate", idToken, map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}
	if code := adminReq(t, "GET", base+"/v1/admin/config", idToken, nil, &cfg); code != http.StatusOK {
		t.Fatalf("config = %d", code)
	}
	capt, _ = cfg["capture"].(map[string]any)
	if capt["policy_sets"] != float64(1) || capt["mode"] != "redact" {
		t.Errorf("capture payload after activate = %v", capt)
	}
}

// TestConsoleConfigAppsStatus pins the apps section against the fact it
// claims: `gitops_dir_enabled` reports whether the pod actually runs the
// watcher, never `cfg.AppsDir() != ""`, which the accessor's
// "<dataDir>/apps" fallback makes unconditionally true. Both answers are
// reachable in code; see appsStatus for why only `true` is reachable in a
// deployment.
func TestConsoleConfigAppsStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		watching    bool
		upstream    time.Duration
		wantEnabled bool
		wantTimeout int
	}{
		{"watcher running", true, 0, true, 30},
		{"no watcher", false, 0, false, 30},
		{"configured timeout survives", true, 12 * time.Second, true, 12},
		{"no watcher, configured timeout", false, 12 * time.Second, false, 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{}
			cfg.Apps.UpstreamTimeout = tc.upstream
			got := appsStatus(&cfg, tc.watching)
			if got.GitopsDirEnabled != tc.wantEnabled {
				t.Errorf("gitops_dir_enabled = %v, want %v", got.GitopsDirEnabled, tc.wantEnabled)
			}
			if got.UpstreamTimeoutSeconds != tc.wantTimeout {
				t.Errorf("upstream_timeout_seconds = %d, want %d", got.UpstreamTimeoutSeconds, tc.wantTimeout)
			}
		})
	}

	// The accessor cannot answer this question:
	// with everything unset it resolves to a path, so `!= ""` is true. This
	// assertion is the regression guard: if it ever fails, AppsDir gained a
	// disable path and the payload can read from config again.
	empty := config.Config{}
	if empty.AppsDir() == "" {
		t.Error("AppsDir() returned empty: the gitops disable path exists now, revisit appsStatus")
	}
}

// TestConsoleOverviewEndpoint seeds a known state and asserts the dashboard
// aggregates.
func TestConsoleOverviewEndpoint(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app) // bootstrap admin + kim = 2 users; roles dev+reader
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	grantAdmin(t, app, user.ID)

	// One active session (kim's checkin), one revoked user entry on the denylist.
	code, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:abc123"}},
	})
	if code != http.StatusOK {
		t.Fatalf("checkin = %d %v", code, checkin)
	}
	app.denylist.revokeUser("some-former-user")

	var ov map[string]any
	if code := adminReq(t, "GET", base+"/v1/admin/overview", idToken, nil, &ov); code != http.StatusOK {
		t.Fatalf("overview = %d", code)
	}
	users, _ := ov["users"].(map[string]any)
	if users["total"].(float64) < 2 || users["active"].(float64) < 2 {
		t.Errorf("users = %v", users)
	}
	sessions, _ := ov["sessions"].(map[string]any)
	if sessions["active"].(float64) < 1 {
		t.Errorf("sessions = %v", sessions)
	}
	if deny, _ := ov["denylist"].(map[string]any); deny["entries"].(float64) != 1 {
		t.Errorf("denylist = %v", deny)
	}
	if ov["snapshot_id"] == "" {
		t.Error("snapshot_id missing")
	}
	if _, ok := ov["audit"].(map[string]any); !ok {
		t.Error("audit head missing")
	}
}

// TestConsoleStaticServing: the embedded placeholder serves at /console/
// with same-origin assets only (no CORS surface).
func TestConsoleStaticServing(t *testing.T) {
	t.Parallel()
	_, base := testApp(t)

	resp, err := http.Get(base + "/console/")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "Straza") {
		t.Fatalf("console = %d: %.80s", resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), "http://") || strings.Contains(string(raw), "https://") {
		t.Error("placeholder must not reference external resources")
	}
	// Embedded assets have no validators: no-cache is what keeps a browser
	// from rendering a bundle baked into an OLDER binary after an upgrade.
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}

	// Bare /console redirects to the directory form.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err = client.Get(base + "/console")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/console/" {
		t.Errorf("redirect = %d → %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// TestConsoleClientIDAndGateExemption: the console logs in with its own
// client id, is exempt from the require-managed checkin gate (like
// strazactl), and its unattested token is still refused by the gateway.
func TestConsoleClientIDAndGateExemption(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) {
		c.Governance.MinAttestation = config.AttestationManaged
	})
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	idToken := deviceLoginAs(t, base, "console", "kim", "hunter2!")

	code, body := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "console", "version": "1"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	if code != http.StatusOK {
		t.Fatalf("console checkin under require-managed = %d %v, want 200", code, body)
	}
	token := body["session_token"].(string)
	if code := adminReq(t, "GET", base+"/v1/admin/overview", token, nil, nil); code != http.StatusOK {
		t.Errorf("admin API with console token = %d", code)
	}
	req, _ := http.NewRequest("GET", base+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("gateway with console token = %d, want 403", resp.StatusCode)
	}
}

// deviceLoginAs runs the device flow with an arbitrary client id.
func deviceLoginAs(t *testing.T, base, clientID, username, password string) string {
	t.Helper()
	resp, err := http.Post(base+"/oidc/device_authorization", "application/x-www-form-urlencoded",
		strings.NewReader("client_id="+clientID))
	if err != nil {
		t.Fatal(err)
	}
	var auth map[string]any
	decodeJSONBody(t, resp, &auth)

	resp, err = http.Post(base+"/oidc/device", "application/x-www-form-urlencoded",
		strings.NewReader("user_code="+auth["user_code"].(string)+"&username="+username+"&password="+password))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	resp, err = http.Post(base+"/oidc/token", "application/x-www-form-urlencoded",
		strings.NewReader("grant_type=urn:ietf:params:oauth:grant-type:device_code&device_code="+
			auth["device_code"].(string)+"&client_id="+clientID))
	if err != nil {
		t.Fatal(err)
	}
	var tok map[string]any
	decodeJSONBody(t, resp, &tok)
	idt, _ := tok["id_token"].(string)
	if idt == "" {
		t.Fatalf("no id_token for client %s: %v", clientID, tok)
	}
	return idt
}

func decodeJSONBody(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("non-JSON: %v: %s", err, raw)
	}
}
