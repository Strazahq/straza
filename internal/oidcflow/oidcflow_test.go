package oidcflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// idpServer fakes an external IdP: OIDC discovery advertising device-flow
// endpoints (Keycloak-shaped).
func idpServer(t *testing.T, withDeviceEndpoint bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		doc := map[string]any{
			"issuer":         srv.URL,
			"token_endpoint": srv.URL + "/protocol/openid-connect/token",
		}
		if withDeviceEndpoint {
			doc["device_authorization_endpoint"] = srv.URL + "/protocol/openid-connect/auth/device"
		}
		_ = json.NewEncoder(w).Encode(doc)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// strazadServer fakes strazad's idp.json answer (or its absence).
func strazadServer(t *testing.T, status int, doc map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/straza/idp.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if doc != nil {
			_ = json.NewEncoder(w).Encode(doc)
		} else if status >= 400 {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "no login issuer configured. Set oidc.issuer"})
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDiscoverExternalIdP(t *testing.T) {
	idp := idpServer(t, true)
	wd := strazadServer(t, http.StatusOK, map[string]string{"issuer": idp.URL, "client_id": "straza"})

	flow, err := Discover(context.Background(), http.DefaultClient, wd.URL, "straza")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if flow.ClientID != "straza" {
		t.Errorf("clientID = %q, want straza (server-dictated)", flow.ClientID)
	}
	if flow.Issuer != idp.URL {
		t.Errorf("issuer = %q, want %q", flow.Issuer, idp.URL)
	}
	if flow.DeviceAuthURL != idp.URL+"/protocol/openid-connect/auth/device" {
		t.Errorf("deviceAuthURL = %q", flow.DeviceAuthURL)
	}
	if flow.TokenURL != idp.URL+"/protocol/openid-connect/token" {
		t.Errorf("tokenURL = %q", flow.TokenURL)
	}
}

func TestDiscoverEmptyClientIDUsesDefault(t *testing.T) {
	idp := idpServer(t, true)
	wd := strazadServer(t, http.StatusOK, map[string]string{"issuer": idp.URL})

	flow, err := Discover(context.Background(), http.DefaultClient, wd.URL, "strazactl")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if flow.ClientID != "strazactl" {
		t.Errorf("clientID = %q, want the caller's default", flow.ClientID)
	}
}

// TestDiscoverLegacyFallback pins compatibility: a strazad without idp.json
// means the built-in issuer at the server's own paths.
func TestDiscoverLegacyFallback(t *testing.T) {
	wd := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(wd.Close)

	flow, err := Discover(context.Background(), http.DefaultClient, wd.URL, "straza")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if flow.DeviceAuthURL != wd.URL+"/oidc/device_authorization" {
		t.Errorf("deviceAuthURL = %q, want the legacy path", flow.DeviceAuthURL)
	}
	if flow.TokenURL != wd.URL+"/oidc/token" {
		t.Errorf("tokenURL = %q, want the legacy path", flow.TokenURL)
	}
	if flow.ClientID != "straza" || flow.Issuer != wd.URL {
		t.Errorf("flow = %+v", flow)
	}
}

// selfIssuerServer fakes a strazad that is its own issuer: idp.json naming
// itself (optionally as nhi_issuer too) plus OIDC discovery on the same host.
func selfIssuerServer(t *testing.T, withNHIIssuer bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("GET /.well-known/straza/idp.json", func(w http.ResponseWriter, _ *http.Request) {
		doc := map[string]string{"issuer": srv.URL}
		if withNHIIssuer {
			doc["nhi_issuer"] = srv.URL
		}
		_ = json.NewEncoder(w).Encode(doc)
	})
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer": srv.URL, "token_endpoint": srv.URL + "/oidc/token",
		})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestDiscoverNHI pins where a headless NHI authenticates: the advertised
// nhi_issuer when the server names one, the server itself when it IS the
// issuer (standalone, old or new, and servers without idp.json), and nowhere
// via Straza when an old enterprise server only names a human IdP (the
// client then falls back to the IdP client-secret lane).
func TestDiscoverNHI(t *testing.T) {
	t.Run("advertised nhi_issuer wins", func(t *testing.T) {
		humanIdP := idpServer(t, true)
		nhiIdP := idpServer(t, false) // no device endpoint: the narrow mount's shape
		wd := strazadServer(t, http.StatusOK, map[string]string{
			"issuer": humanIdP.URL, "nhi_issuer": nhiIdP.URL,
		})
		flow, offered, err := DiscoverNHI(context.Background(), http.DefaultClient, wd.URL)
		if err != nil || !offered {
			t.Fatalf("DiscoverNHI: offered=%v err=%v", offered, err)
		}
		if flow.Issuer != nhiIdP.URL || flow.TokenURL != nhiIdP.URL+"/protocol/openid-connect/token" {
			t.Errorf("flow = %+v, want the NHI issuer's endpoints", flow)
		}
	})

	t.Run("new standalone advertises itself", func(t *testing.T) {
		wd := selfIssuerServer(t, true)
		flow, offered, err := DiscoverNHI(context.Background(), http.DefaultClient, wd.URL)
		if err != nil || !offered || flow.TokenURL != wd.URL+"/oidc/token" {
			t.Fatalf("offered=%v err=%v flow=%+v", offered, err, flow)
		}
	})

	t.Run("old standalone offers itself without the field", func(t *testing.T) {
		wd := selfIssuerServer(t, false)
		flow, offered, err := DiscoverNHI(context.Background(), http.DefaultClient, wd.URL)
		if err != nil || !offered || flow.TokenURL != wd.URL+"/oidc/token" {
			t.Fatalf("offered=%v err=%v flow=%+v", offered, err, flow)
		}
	})

	t.Run("pre-P5.8 server offers itself", func(t *testing.T) {
		wd := httptest.NewServer(http.NotFoundHandler())
		t.Cleanup(wd.Close)
		flow, offered, err := DiscoverNHI(context.Background(), http.DefaultClient, wd.URL)
		if err != nil || !offered || flow.TokenURL != wd.URL+"/oidc/token" {
			t.Fatalf("offered=%v err=%v flow=%+v", offered, err, flow)
		}
	})

	t.Run("old enterprise offers nothing", func(t *testing.T) {
		idp := idpServer(t, true)
		wd := strazadServer(t, http.StatusOK, map[string]string{"issuer": idp.URL})
		_, offered, err := DiscoverNHI(context.Background(), http.DefaultClient, wd.URL)
		if err != nil || offered {
			t.Fatalf("offered=%v err=%v, want not offered, no error", offered, err)
		}
	})

	t.Run("misconfigured server surfaces its error", func(t *testing.T) {
		wd := strazadServer(t, http.StatusServiceUnavailable, nil)
		_, _, err := DiscoverNHI(context.Background(), http.DefaultClient, wd.URL)
		if err == nil || !strings.Contains(err.Error(), "oidc.issuer") {
			t.Fatalf("want the server's actionable error, got %v", err)
		}
	})

	t.Run("nhi issuer without a token endpoint is actionable", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": "x"})
		})
		bare := httptest.NewServer(mux)
		t.Cleanup(bare.Close)
		wd := strazadServer(t, http.StatusOK, map[string]string{
			"issuer": "http://human.example", "nhi_issuer": bare.URL,
		})
		_, _, err := DiscoverNHI(context.Background(), http.DefaultClient, wd.URL)
		if err == nil || !strings.Contains(err.Error(), "token_endpoint") {
			t.Fatalf("want token_endpoint error, got %v", err)
		}
	})
}

func TestDiscoverErrors(t *testing.T) {
	t.Run("misconfigured server surfaces its error", func(t *testing.T) {
		wd := strazadServer(t, http.StatusServiceUnavailable, nil)
		_, err := Discover(context.Background(), http.DefaultClient, wd.URL, "straza")
		if err == nil || !strings.Contains(err.Error(), "oidc.issuer") {
			t.Fatalf("want the server's actionable error, got %v", err)
		}
	})
	t.Run("empty issuer rejected", func(t *testing.T) {
		wd := strazadServer(t, http.StatusOK, map[string]string{"issuer": ""})
		_, err := Discover(context.Background(), http.DefaultClient, wd.URL, "straza")
		if err == nil || !strings.Contains(err.Error(), "issuer") {
			t.Fatalf("want issuer error, got %v", err)
		}
	})
	t.Run("unreachable issuer named in the error", func(t *testing.T) {
		wd := strazadServer(t, http.StatusOK, map[string]string{"issuer": "http://127.0.0.1:1/nope"})
		_, err := Discover(context.Background(), http.DefaultClient, wd.URL, "straza")
		if err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
			t.Fatalf("want error naming the issuer, got %v", err)
		}
		if next := "Check that this machine can open http://127.0.0.1:1/nope, for example in a browser here, then sign in again"; !strings.Contains(err.Error(), next) {
			t.Fatalf("error %q does not say what to do next: %q", err, next)
		}
	})
	t.Run("issuer without device grant is actionable", func(t *testing.T) {
		idp := idpServer(t, false)
		wd := strazadServer(t, http.StatusOK, map[string]string{"issuer": idp.URL})
		_, err := Discover(context.Background(), http.DefaultClient, wd.URL, "straza")
		if err == nil || !strings.Contains(err.Error(), "device_authorization_endpoint") {
			t.Fatalf("want device-grant hint, got %v", err)
		}
	})
}

// TestNHIOfferLazyResolve pins the split that keeps lane choice independent
// of reachability: reading the offer never dials the advertised issuer, and
// resolving it, which happens only once the key lane is picked, names the
// issuer and the config knob when the dial fails.
func TestNHIOfferLazyResolve(t *testing.T) {
	undialable := "http://127.0.0.1:1"
	wd := strazadServer(t, http.StatusOK, map[string]string{
		"issuer": "http://human.example", "nhi_issuer": undialable,
	})

	offer, err := DiscoverNHIOffer(context.Background(), http.DefaultClient, wd.URL)
	if err != nil || !offer.Offered || offer.Issuer != undialable {
		t.Fatalf("offer = %+v err = %v, want the offer read without dialing it", offer, err)
	}

	_, err = ResolveNHI(context.Background(), http.DefaultClient, offer)
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") || !strings.Contains(err.Error(), "publicUrl") {
		t.Fatalf("resolve error = %v, want it naming the issuer and the config knob", err)
	}
}
