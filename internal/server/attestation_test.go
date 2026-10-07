package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestVerifyAttestation pins the level computation against the expected-hash
// registry, which feeds token issuance.
func TestVerifyAttestation(t *testing.T) {
	t.Parallel()
	registry := []store.AttestationHash{
		{Artifact: "self", Platform: "linux/amd64", Hash: "sha256:aa11"},
		{Artifact: "self", Platform: "linux/amd64", Hash: "sha256:aa22"}, // upgrade window: 2 allowed
		{Artifact: "config", Hash: "sha256:cfg1"},
		{Artifact: "hooks.claude-code", Harness: "claude-code", Hash: "sha256:cc33"},
	}
	good := map[string]string{
		"self": "sha256:aa11", "config": "sha256:cfg1", "hooks.claude-code": "sha256:cc33",
	}

	cases := []struct {
		name     string
		payload  attestationPayload
		harness  string
		expected []store.AttestationHash
		want     string
	}{
		{"no measurements", attestationPayload{Managed: true}, "claude-code", registry, store.AttestationNone},
		{"unmanaged is advisory at best",
			attestationPayload{Managed: false, Platform: "linux/amd64", Hashes: good},
			"claude-code", registry, store.AttestationAdvisory},
		{"managed claim with empty registry caps at advisory",
			attestationPayload{Managed: true, Platform: "linux/amd64", Hashes: good},
			"claude-code", nil, store.AttestationAdvisory},
		{"all registered artifacts match → managed",
			attestationPayload{Managed: true, Platform: "linux/amd64", Hashes: good},
			"claude-code", registry, store.AttestationManaged},
		{"alternate allowed hash also verifies",
			attestationPayload{Managed: true, Platform: "linux/amd64", Hashes: map[string]string{
				"self": "sha256:aa22", "config": "sha256:cfg1", "hooks.claude-code": "sha256:cc33"}},
			"claude-code", registry, store.AttestationManaged},
		{"tampered artifact → none",
			attestationPayload{Managed: true, Platform: "linux/amd64", Hashes: map[string]string{
				"self": "sha256:aa11", "config": "sha256:cfg1", "hooks.claude-code": "sha256:EVIL"}},
			"claude-code", registry, store.AttestationNone},
		{"missing registered artifact → none",
			attestationPayload{Managed: true, Platform: "linux/amd64", Hashes: map[string]string{
				"self": "sha256:aa11", "config": "sha256:cfg1"}},
			"claude-code", registry, store.AttestationNone},
		{"other harness's wiring row is not required",
			attestationPayload{Managed: true, Platform: "linux/amd64", Hashes: map[string]string{
				"self": "sha256:aa11", "config": "sha256:cfg1"}},
			"codex", registry, store.AttestationManaged},
		{"extra unregistered measurement is ignored",
			attestationPayload{Managed: true, Platform: "linux/amd64", Hashes: map[string]string{
				"self": "sha256:aa11", "config": "sha256:cfg1", "hooks.claude-code": "sha256:cc33",
				"hooks.extra": "sha256:zz99"}},
			"claude-code", registry, store.AttestationManaged},
		{"unregistered platform filters platform-scoped rows; config-only registry still verifies",
			attestationPayload{Managed: true, Platform: "windows/arm64", Hashes: map[string]string{
				"config": "sha256:cfg1", "hooks.claude-code": "sha256:cc33"}},
			"claude-code", registry, store.AttestationManaged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := verifyAttestation(tc.payload, tc.harness, tc.expected); got != tc.want {
				t.Errorf("verifyAttestation = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCheckinAttestationGate pins the attestation gate over HTTP: with
// governance.minAttestation=managed, an advisory check-in gets no token, a
// verified managed check-in gets one, and a tampered artifact yields
// att=none → no token.
func TestCheckinAttestationGate(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) {
		c.Governance.MinAttestation = config.AttestationManaged
	})
	_ = seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	_, enroll := postJSON(t, base+"/v1/enroll", map[string]any{
		"id_token": idToken,
		"device":   map[string]string{"name": "kim-laptop", "platform": "linux", "fingerprint": "sha256:fp-gate"},
	})
	deviceID := enroll["device_id"].(string)

	checkin := func(managed bool, hashes map[string]string) (int, map[string]any) {
		return postJSON(t, base+"/v1/checkin", map[string]any{
			"id_token":  idToken,
			"device_id": deviceID,
			"harness":   map[string]string{"name": "claude-code", "version": "2.1.0"},
			"attestation": map[string]any{
				"managed": managed, "platform": "linux/amd64", "hashes": hashes,
			},
		})
	}

	// Advisory (user-mode) install: below the managed minimum → no token.
	code, body := checkin(false, map[string]string{"self": "sha256:aa11"})
	if code != http.StatusForbidden {
		t.Fatalf("advisory checkin = %d %v, want 403", code, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "advisory") || !strings.Contains(msg, "managed") {
		t.Errorf("deny reason %q must name the actual and required levels", msg)
	}
	// The remedy the user is handed must actually parse. `straza install` is
	// positional-only, so a bare `--managed` form would error out on paste.
	// Pinned so a syntax change breaks here, not in a customer's shell.
	if msg, _ := body["error"].(string); !strings.Contains(msg, "`straza install --managed <harness>`") {
		t.Errorf("deny reason %q must hand back a command that parses", msg)
	}
	if sessions, _ := app.store.Sessions().List(context.Background(), ""); len(sessions) != 0 {
		t.Errorf("denied checkin must not create a session, got %d", len(sessions))
	}

	// Register the expected hashes (as install --managed would).
	ctx := context.Background()
	for artifact, h := range map[string]string{"self": "sha256:aa11", "hooks.claude-code": "sha256:cc33"} {
		row := store.AttestationHash{Artifact: artifact, Hash: h, Platform: "linux/amd64"}
		if artifact != "self" {
			row.Harness = "claude-code"
		}
		if _, err := app.store.AttestationHashes().Create(ctx, row); err != nil {
			t.Fatal(err)
		}
	}

	// Verified managed install → token with att=managed.
	code, body = checkin(true, map[string]string{"self": "sha256:aa11", "hooks.claude-code": "sha256:cc33"})
	if code != http.StatusOK || body["attestation"] != "managed" {
		t.Fatalf("managed checkin = %d %v, want 200 managed", code, body)
	}
	claims, err := app.tokens.Verify(body["session_token"].(string))
	if err != nil || claims.Attestation != "managed" {
		t.Fatalf("token claims = %+v, %v", claims, err)
	}

	// Tampered wiring → att=none → no token.
	code, body = checkin(true, map[string]string{"self": "sha256:aa11", "hooks.claude-code": "sha256:EVIL"})
	if code != http.StatusForbidden {
		t.Fatalf("tampered checkin = %d %v, want 403", code, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "none") {
		t.Errorf("tamper deny reason %q must state the computed level none", msg)
	}
}

// TestCheckinRefreshGate: raising the minimum cuts existing sessions at their
// next refresh (bounded by token TTL).
func TestCheckinRefreshGate(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	code, body := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:x"}},
	})
	if code != http.StatusOK || body["attestation"] != "advisory" {
		t.Fatalf("checkin = %d %v", code, body)
	}

	app.cfg.Governance.MinAttestation = config.AttestationManaged
	code, refresh := postJSON(t, base+"/v1/checkin", map[string]any{
		"session_token": body["session_token"],
		"harness":       map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation":   map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	if code != http.StatusForbidden {
		t.Fatalf("refresh after raise = %d %v, want 403", code, refresh)
	}

	// Re-labeling the refresh as the admin CLI must not dodge the gate; the
	// session row's harness is authoritative.
	code, refresh = postJSON(t, base+"/v1/checkin", map[string]any{
		"session_token": body["session_token"],
		"harness":       map[string]string{"name": "strazactl", "version": "dev"},
		"attestation":   map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	if code != http.StatusForbidden {
		t.Fatalf("re-labeled refresh = %d %v, want 403", code, refresh)
	}
}

// TestAttestationGateAdminCLIAndGateway pins the bootstrap story:
// strazactl can always log in under require-managed (else no admin could ever
// register the first hash), but its unattested token is refused by the
// gateway PEP: the exemption never reaches the data plane.
func TestAttestationGateAdminCLIAndGateway(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) {
		c.Governance.MinAttestation = config.AttestationManaged
	})
	user := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	grantAdmin(t, app, user.ID)

	// Admin CLI checkin: exempt at the gate, token issued with att=none.
	code, body := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "strazactl", "version": "dev"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
	})
	if code != http.StatusOK {
		t.Fatalf("strazactl checkin under require-managed = %d %v, want 200 (bootstrap)", code, body)
	}
	token := body["session_token"].(string)

	// The admin surface works (this is how the registry gets bootstrapped)...
	if code := adminReq(t, "GET", base+"/v1/admin/attestation-hashes", token, nil, nil); code != http.StatusOK {
		t.Errorf("admin API with exempted token = %d, want 200", code)
	}

	// ...but the MCP data plane refuses the same token.
	req, _ := http.NewRequest("GET", base+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("gateway with unattested token = %d, want 403", resp.StatusCode)
	}
	var gw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&gw); err == nil {
		if msg, _ := gw["error"].(string); !strings.Contains(msg, "attestation") {
			t.Errorf("gateway refusal %q must name attestation", msg)
		}
		// Same tripwire as the check-in gate: the remedy must be a command
		// the positional-only CLI accepts.
		if msg, _ := gw["error"].(string); !strings.Contains(msg, "`straza install --managed <harness>`") {
			t.Errorf("gateway refusal %q must hand back a command that parses", msg)
		}
	}
}

// TestAttestationRegistryAdminAPI covers the /v1/admin/attestation-hashes
// CRUD surface used by strazactl and install --managed registration.
func TestAttestationRegistryAdminAPI(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	grantAdmin(t, app, user.ID)

	// Create.
	var created map[string]any
	code := adminReq(t, "POST", base+"/v1/admin/attestation-hashes", idToken, map[string]string{
		"artifact": "self", "platform": "linux/amd64", "hash": "sha256:aa1122", "note": "straza v0.5.0",
	}, &created)
	if code != http.StatusCreated || created["id"] == "" {
		t.Fatalf("create = %d %v", code, created)
	}
	// Validation: artifact and well-formed hash are required.
	if code := adminReq(t, "POST", base+"/v1/admin/attestation-hashes", idToken,
		map[string]string{"artifact": "self", "hash": "md5:nope"}, nil); code != http.StatusBadRequest {
		t.Errorf("bad hash = %d, want 400", code)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/attestation-hashes", idToken,
		map[string]string{"hash": "sha256:aa1122"}, nil); code != http.StatusBadRequest {
		t.Errorf("missing artifact = %d, want 400", code)
	}
	// Duplicate row conflicts.
	if code := adminReq(t, "POST", base+"/v1/admin/attestation-hashes", idToken, map[string]string{
		"artifact": "self", "platform": "linux/amd64", "hash": "sha256:aa1122",
	}, nil); code != http.StatusConflict {
		t.Errorf("duplicate = %d, want 409", code)
	}

	// List: the created row plus the boot-registered harness-config renders
	// (api_harnesscfg.go: harness x GOOS x GOARCH, additive every boot).
	bootRows := len(harnessCfgHarnesses) * len(agentguard.SupportedGOOS) * len(harnessCfgArches)
	var list []map[string]any
	if code := adminReq(t, "GET", base+"/v1/admin/attestation-hashes", idToken, nil, &list); code != http.StatusOK || len(list) != 1+bootRows {
		t.Fatalf("list = %d, %d rows (want %d)", code, len(list), 1+bootRows)
	}
	found := false
	for _, row := range list {
		found = found || row["id"] == created["id"]
	}
	if !found {
		t.Fatalf("created row %v missing from list", created["id"])
	}

	// Delete.
	id := created["id"].(string)
	if code := adminReq(t, "DELETE", base+"/v1/admin/attestation-hashes/"+id, idToken, nil, nil); code != http.StatusOK {
		t.Errorf("delete = %d", code)
	}
	if code := adminReq(t, "DELETE", base+"/v1/admin/attestation-hashes/"+id, idToken, nil, nil); code != http.StatusNotFound {
		t.Errorf("delete gone = %d, want 404", code)
	}
}
