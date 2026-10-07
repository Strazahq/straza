package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
)

// TestIdPDiscovery pins the login-discovery document: standalone
// advertises the built-in issuer, enterprise the external IdP, and an
// enterprise server without one answers with an actionable error instead
// of a dead end.
func TestIdPDiscovery(t *testing.T) {
	t.Parallel()
	t.Run("standalone advertises the built-in issuer", func(t *testing.T) {
		_, base := testApp(t)
		doc, status := getIdPDoc(t, base)
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		if doc["issuer"] != base {
			t.Errorf("issuer = %q, want %q (the built-in issuer)", doc["issuer"], base)
		}
		if doc["client_id"] != "" {
			t.Errorf("client_id = %q, want empty (clients use their own)", doc["client_id"])
		}
		if doc["nhi_issuer"] != base {
			t.Errorf("nhi_issuer = %q, want %q (NHIs authenticate at Straza)", doc["nhi_issuer"], base)
		}
		// The advertised issuer must actually serve OIDC discovery with the
		// device-flow endpoints; that is what clients dereference next.
		resp, err := http.Get(doc["issuer"] + "/.well-known/openid-configuration")
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("issuer discovery: %v (%d)", err, resp.StatusCode)
		}
		var disc map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&disc)
		_ = resp.Body.Close()
		if disc["device_authorization_endpoint"] == "" || disc["token_endpoint"] == "" {
			t.Errorf("issuer discovery lacks device-flow endpoints: %v", disc)
		}
	})

	t.Run("enterprise splits the human and NHI issuers", func(t *testing.T) {
		idpApp, idpBase := testApp(t)
		_ = idpApp
		_, base := testApp(t, func(cfg *config.Config) {
			cfg.Profile = config.ProfileEnterprise
			cfg.OIDC.Issuer = idpBase
			cfg.OIDC.ClientID = "straza"
		})
		doc, status := getIdPDoc(t, base)
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		if doc["issuer"] != idpBase {
			t.Errorf("issuer = %q, want the human IdP %q", doc["issuer"], idpBase)
		}
		if doc["nhi_issuer"] != base {
			t.Errorf("nhi_issuer = %q, want %q (NHIs authenticate at Straza)", doc["nhi_issuer"], base)
		}
	})

	t.Run("enterprise without an issuer answers actionably", func(t *testing.T) {
		_, base := testApp(t, func(cfg *config.Config) {
			cfg.Profile = config.ProfileEnterprise
		})
		doc, status := getIdPDoc(t, base)
		if status != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", status)
		}
		if !strings.Contains(doc["error"], "oidc.issuer") {
			t.Errorf("error %q does not tell the operator what to set", doc["error"])
		}
	})
}

func getIdPDoc(t *testing.T, base string) (map[string]string, int) {
	t.Helper()
	resp, err := http.Get(base + "/.well-known/straza/idp.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	doc := map[string]string{}
	_ = json.NewDecoder(resp.Body).Decode(&doc)
	return doc, resp.StatusCode
}
