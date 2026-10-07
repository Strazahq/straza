package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/clientassertion"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// seedNHIWithKey provisions an active NHI and registers pub as its assertion
// key straight in the store: the admin/SCIM registration surfaces have their
// own suites, and these tests are about the grant mount.
func seedNHIWithKey(t *testing.T, app *App, username string, pub ed25519.PublicKey) store.User {
	t.Helper()
	ctx := context.Background()
	u, err := app.store.Users().Create(ctx, store.User{
		Username: username, Email: username + "@x.io", Attrs: `{"kind":"nhi"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(nhiKeyMeta{
		PublicKey: base64.StdEncoding.EncodeToString(pub), Created: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.Settings().Set(ctx, nhiKeyPrefix+u.ID, string(meta)); err != nil {
		t.Fatal(err)
	}
	return u
}

// postGrantForm posts an /oidc/token form and decodes the JSON answer.
func postGrantForm(t *testing.T, base string, form url.Values) (int, map[string]any) {
	t.Helper()
	resp, err := http.PostForm(base+"/oidc/token", form)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

func assertionForm(t *testing.T, priv ed25519.PrivateKey, clientID, aud string) url.Values {
	t.Helper()
	assertion, err := clientassertion.Mint(priv, clientID, aud, 0)
	if err != nil {
		t.Fatal(err)
	}
	return url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {clientID},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion},
	}
}

// TestEnterpriseNHIGrant pins the enterprise assertion lane: the issuer
// mount serves the client_credentials grant beside the emergency sign-in,
// an NHI with a registered key mints a token and checks in deviceless, and
// a device_code nobody approved is refused as unknown.
func TestEnterpriseNHIGrant(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(cfg *config.Config) {
		cfg.Profile = config.ProfileEnterprise
	})
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	nhi := seedNHIWithKey(t, app, "pod-bot", pub)

	t.Run("discovery advertises both grants", func(t *testing.T) {
		resp, err := http.Get(base + "/.well-known/openid-configuration")
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("discovery: %v (%d)", err, resp.StatusCode)
		}
		var disc map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&disc)
		_ = resp.Body.Close()
		grants, _ := disc["grant_types_supported"].([]any)
		if len(grants) != 2 || grants[0] != "urn:ietf:params:oauth:grant-type:device_code" || grants[1] != "client_credentials" {
			t.Errorf("grant_types_supported = %v, want the device_code and client_credentials grants", grants)
		}
		if disc["device_authorization_endpoint"] != base+"/oidc/device_authorization" {
			t.Errorf("device_authorization_endpoint = %v, want the emergency sign-in flow", disc["device_authorization_endpoint"])
		}
	})

	t.Run("unknown device_code refused", func(t *testing.T) {
		code, body := postGrantForm(t, base, url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {"x"}, "client_id": {"strazactl"},
		})
		if code != http.StatusBadRequest || body["error"] != "invalid_grant" {
			t.Fatalf("device_code on enterprise = %d %v", code, body)
		}
	})

	t.Run("grant then deviceless checkin", func(t *testing.T) {
		code, body := postGrantForm(t, base, assertionForm(t, priv, "pod-bot", base))
		if code != http.StatusOK {
			t.Fatalf("grant = %d %v", code, body)
		}
		idToken, _ := body["id_token"].(string)
		code, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
			"id_token":    idToken,
			"harness":     map[string]string{"name": "exec-wrapper", "version": "1.0"},
			"attestation": map[string]any{"managed": false, "hashes": map[string]string{}},
		})
		if code != http.StatusOK {
			t.Fatalf("checkin = %d %v", code, checkin)
		}
		ses, err := app.store.Sessions().GetByID(context.Background(), checkin["session_id"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if ses.DeviceID != "" {
			t.Errorf("NHI session has device id %q, want none", ses.DeviceID)
		}

		// Audit: the grant emitted exactly one client-assertion login success
		// (the checkin's id-token event is a separate, also-true fact).
		success := waitAuthn(t, app, "grant success", func(d map[string]any) bool {
			return d["action"] == "login" && d["outcome"] == "success" && d["via"] == "client-assertion"
		})
		if success["user"] != "pod-bot" || success["userId"] != nhi.ID {
			t.Errorf("grant success payload = %v", success)
		}
		if ip, _ := success["sourceIp"].(string); ip == "" {
			t.Errorf("grant success carries no sourceIp: %v", success)
		}
		if n := countAuthn(t, app, func(d map[string]any) bool {
			return d["outcome"] == "success" && d["via"] == "client-assertion"
		}); n != 1 {
			t.Errorf("grant success emitted %d times, want exactly once", n)
		}
	})

	t.Run("failures land on the chain honestly", func(t *testing.T) {
		_, wrongPriv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if code, _ := postGrantForm(t, base, assertionForm(t, wrongPriv, "pod-bot", base)); code != http.StatusBadRequest {
			t.Fatalf("wrong-key grant = %d", code)
		}
		known := waitAuthn(t, app, "wrong-key failure", func(d map[string]any) bool {
			return d["outcome"] == "failure" && d["via"] == "client-assertion" && d["user"] == "pod-bot"
		})
		if reason, _ := known["reason"].(string); reason == "" {
			t.Errorf("wrong-key failure has no reason: %v", known)
		}

		if code, _ := postGrantForm(t, base, assertionForm(t, wrongPriv, "ghost", base)); code != http.StatusBadRequest {
			t.Fatalf("unknown-client grant = %d", code)
		}
		anon := waitAuthn(t, app, "unknown-client failure", func(d map[string]any) bool {
			return d["outcome"] == "failure" && d["via"] == "client-assertion" && d["user"] == nil
		})
		if anon["userId"] != nil {
			t.Errorf("unknown client fabricated an identity: %v", anon)
		}
	})
}

// TestStandaloneNHIGrantAudited pins that the SAME producer covers the
// standalone grant (spec/events rev 22).
func TestStandaloneNHIGrantAudited(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	seedNHIWithKey(t, app, "ci-bot", pub)
	if code, body := postGrantForm(t, base, assertionForm(t, priv, "ci-bot", base)); code != http.StatusOK {
		t.Fatalf("grant = %d %v", code, body)
	}
	waitAuthn(t, app, "standalone grant success", func(d map[string]any) bool {
		return d["action"] == "login" && d["outcome"] == "success" &&
			d["via"] == "client-assertion" && d["user"] == "ci-bot"
	})
}

// TestEnterpriseNHIGrantThrottle pins that the per-IP login throttle wraps
// the enterprise token endpoint: assertion brute force is bounded.
func TestEnterpriseNHIGrantThrottle(t *testing.T) {
	t.Parallel()
	_, base := testApp(t, func(cfg *config.Config) {
		cfg.Profile = config.ProfileEnterprise
		cfg.Server.LoginPerIPRPS = 1
	})
	junk := url.Values{"grant_type": {"client_credentials"}, "client_id": {"x"}}
	sawLimited := false
	for i := 0; i < 5 && !sawLimited; i++ {
		code, _ := postGrantForm(t, base, junk)
		sawLimited = code == http.StatusTooManyRequests
	}
	if !sawLimited {
		t.Fatal("five rapid grant posts never hit the throttle")
	}
}
