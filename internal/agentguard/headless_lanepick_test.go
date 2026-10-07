package agentguard_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
)

// fakeHumanIdP serves the minimum a human IdP needs for the client-secret
// lane: OIDC discovery with device+token endpoints and a token endpoint that
// answers client_credentials.
func fakeHumanIdP(t *testing.T, tokenHits *int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                        srv.URL,
			"device_authorization_endpoint": srv.URL + "/device",
			"token_endpoint":                srv.URL + "/token",
		})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		*tokenHits++
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": "stub-idp-token"})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// fakeEnterpriseStrazad serves what EnrollHeadless dereferences on a new
// enterprise server: idp.json splitting the issuers, narrow NHI discovery,
// the token endpoint, and snapshot keys. The last client_assertion posted to
// /oidc/token is recorded for inspection. nhiIssuer overrides the advertised
// nhi_issuer; empty means the server names itself.
func fakeEnterpriseStrazad(t *testing.T, humanIssuer, nhiIssuer string, lastAssertion *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("GET /.well-known/straza/idp.json", func(w http.ResponseWriter, _ *http.Request) {
		nhi := nhiIssuer
		if nhi == "" {
			nhi = srv.URL
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer": humanIssuer, "client_id": "straza", "nhi_issuer": nhi,
		})
	})
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer": srv.URL, "token_endpoint": srv.URL + "/oidc/token",
		})
	})
	mux.HandleFunc("POST /oidc/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		*lastAssertion = r.PostForm.Get("client_assertion")
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": "stub-token"})
	})
	mux.HandleFunc("GET /.well-known/straza/snapshot-keys.json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{"kid": "k1", "key": "c3R1Yg=="}},
		})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// assertionClaims decodes a JWT payload without verifying: tests only.
func assertionClaims(t *testing.T, assertion string) map[string]any {
	t.Helper()
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion is not a JWT: %q", assertion)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

// TestHeadlessEnterpriseKeyLane is the enterprise twin of TestHeadlessNHIDemo:
// against a real enterprise strazad (external IdP for humans), an NHI with a
// Straza-registered key enrolls headless and starts a governed session with
// no per-agent IdP client and no IdP round-trip on the agent box.
func TestHeadlessEnterpriseKeyLane(t *testing.T) {
	idpBase, adminPW := bootStrazad(t) // kim exists at the IdP only
	platformBase := bootEnterprise(t, idpBase)

	home := t.TempDir()
	t.Setenv("STRAZA_HOME", home)
	env := []string{"STRAZA_HOME=" + home, "CLAUDECODE=1"}
	store, err := agentguard.OpenStore()
	if err != nil {
		t.Fatal(err)
	}

	// 1. keygen on the agent box.
	var out bytes.Buffer
	if err := agentguard.Keygen(store, "ci-bot", false, &out); err != nil {
		t.Fatal(err)
	}
	key, err := store.LoadNHIKey()
	if err != nil {
		t.Fatal(err)
	}
	seed, err := base64.StdEncoding.DecodeString(key.Seed)
	if err != nil {
		t.Fatal(err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))

	// 2. The admin (bootstrapped through the IdP) provisions the NHI at the
	// PLATFORM and registers the key there: no IdP object for the agent.
	adminTok := deviceLogin(t, idpBase, "straza", "kim", adminPW)
	code, created := adminJSON(t, http.MethodPost, platformBase+"/v1/admin/users", adminTok,
		map[string]any{"username": "ci-bot", "kind": "nhi"})
	if code != http.StatusCreated {
		t.Fatalf("create nhi = %d %v", code, created)
	}
	nhiID, _ := created["id"].(string)
	if code, body := adminJSON(t, http.MethodPut, platformBase+"/v1/admin/users/"+nhiID+"/nhi-key", adminTok,
		map[string]any{"public_key": pubB64}); code != http.StatusOK {
		t.Fatalf("register key = %d %v", code, body)
	}

	// 3. Headless enroll picks the key lane off nhi_issuer.
	out.Reset()
	if err := agentguard.EnrollHeadless(context.Background(), store, platformBase, "", &out); err != nil {
		t.Fatalf("EnrollHeadless: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), agentguard.HeadlessKey) {
		t.Errorf("enroll output does not name the key lane:\n%s", out.String())
	}

	// 4. A session starts through the hooks: grant at the platform, deviceless
	// checkin, governed like a user.
	if hookOut, code := hook(t, "claude-code",
		`{"hook_event_name":"SessionStart","session_id":"e1","cwd":"/work"}`, env); code != 0 {
		t.Fatalf("enterprise headless session.start blocked (exit %d): %s", code, hookOut)
	}
	ses, err := store.LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	if ses.User != "ci-bot" {
		t.Errorf("session user = %q, want ci-bot", ses.User)
	}
}

// TestHeadlessLanePick pins how enroll --headless chooses its credential on
// an enterprise server that advertises nhi_issuer: the local key wins when
// one exists (and the assertion aims at the NHI issuer, never the human
// IdP), the IdP client secret stays the fallback, and a box with neither
// gets an error naming both lanes.
func TestHeadlessLanePick(t *testing.T) {
	ctx := context.Background()

	t.Run("local key prefers the key lane at the NHI issuer", func(t *testing.T) {
		t.Setenv("STRAZA_HOME", t.TempDir())
		t.Setenv("STRAZA_CLIENT_ID", "")
		t.Setenv("STRAZA_CLIENT_SECRET", "")
		var idpHits int
		idp := fakeHumanIdP(t, &idpHits)
		var lastAssertion string
		wd := fakeEnterpriseStrazad(t, idp.URL, "", &lastAssertion)

		store, err := agentguard.OpenStore()
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := agentguard.Keygen(store, "pod-bot", false, &out); err != nil {
			t.Fatal(err)
		}
		if err := agentguard.EnrollHeadless(ctx, store, wd.URL, "", &out); err != nil {
			t.Fatalf("EnrollHeadless: %v\n%s", err, out.String())
		}
		id, err := store.LoadIdentity()
		if err != nil {
			t.Fatal(err)
		}
		if id.Headless != agentguard.HeadlessKey || id.Username != "pod-bot" {
			t.Errorf("identity = %+v, want the key lane as pod-bot", id)
		}
		claims := assertionClaims(t, lastAssertion)
		if aud, _ := claims["aud"].([]any); len(aud) == 0 || aud[0] != wd.URL {
			t.Errorf("assertion aud = %v, want the NHI issuer %s (never the human IdP)", claims["aud"], wd.URL)
		}
		if idpHits != 0 {
			t.Errorf("the human IdP was contacted %d times on the key lane, want 0", idpHits)
		}
	})

	t.Run("no local key falls back to the IdP secret lane", func(t *testing.T) {
		t.Setenv("STRAZA_HOME", t.TempDir())
		t.Setenv("STRAZA_CLIENT_ID", "svc-bot")
		t.Setenv("STRAZA_CLIENT_SECRET", "s3cret")
		var idpHits int
		idp := fakeHumanIdP(t, &idpHits)
		var lastAssertion string
		wd := fakeEnterpriseStrazad(t, idp.URL, "", &lastAssertion)

		store, err := agentguard.OpenStore()
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := agentguard.EnrollHeadless(ctx, store, wd.URL, "svc-bot", &out); err != nil {
			t.Fatalf("EnrollHeadless: %v\n%s", err, out.String())
		}
		id, err := store.LoadIdentity()
		if err != nil {
			t.Fatal(err)
		}
		if id.Headless != agentguard.HeadlessClientCreds {
			t.Errorf("identity = %+v, want the client-credentials lane", id)
		}
		if idpHits != 1 || lastAssertion != "" {
			t.Errorf("idp token hits = %d, assertion = %q; want the secret judged at the IdP", idpHits, lastAssertion)
		}
	})

	t.Run("neither credential names both lanes", func(t *testing.T) {
		t.Setenv("STRAZA_HOME", t.TempDir())
		t.Setenv("STRAZA_CLIENT_ID", "")
		t.Setenv("STRAZA_CLIENT_SECRET", "")
		var idpHits int
		idp := fakeHumanIdP(t, &idpHits)
		var lastAssertion string
		wd := fakeEnterpriseStrazad(t, idp.URL, "", &lastAssertion)

		store, err := agentguard.OpenStore()
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err = agentguard.EnrollHeadless(ctx, store, wd.URL, "svc-bot", &out)
		if err == nil {
			t.Fatal("enroll with no credential succeeded")
		}
		for _, want := range []string{"STRAZA_CLIENT_ID", "keygen"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %s", err, want)
			}
		}
	})

	t.Run("undialable nhi_issuer with no key still enrolls the secret lane", func(t *testing.T) {
		t.Setenv("STRAZA_HOME", t.TempDir())
		t.Setenv("STRAZA_CLIENT_ID", "svc-bot")
		t.Setenv("STRAZA_CLIENT_SECRET", "s3cret")
		var idpHits int
		idp := fakeHumanIdP(t, &idpHits)
		var lastAssertion string
		wd := fakeEnterpriseStrazad(t, idp.URL, "http://127.0.0.1:1", &lastAssertion)

		store, err := agentguard.OpenStore()
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := agentguard.EnrollHeadless(ctx, store, wd.URL, "svc-bot", &out); err != nil {
			t.Fatalf("EnrollHeadless: %v\n%s", err, out.String())
		}
		id, err := store.LoadIdentity()
		if err != nil {
			t.Fatal(err)
		}
		if id.Headless != agentguard.HeadlessClientCreds {
			t.Errorf("identity = %+v, want the client-credentials lane", id)
		}
	})

	t.Run("undialable nhi_issuer with a key fails hard, never downgrades", func(t *testing.T) {
		t.Setenv("STRAZA_HOME", t.TempDir())
		t.Setenv("STRAZA_CLIENT_ID", "svc-bot")
		t.Setenv("STRAZA_CLIENT_SECRET", "s3cret")
		var idpHits int
		idp := fakeHumanIdP(t, &idpHits)
		var lastAssertion string
		wd := fakeEnterpriseStrazad(t, idp.URL, "http://127.0.0.1:1", &lastAssertion)

		store, err := agentguard.OpenStore()
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := agentguard.Keygen(store, "pod-bot", false, &out); err != nil {
			t.Fatal(err)
		}
		err = agentguard.EnrollHeadless(ctx, store, wd.URL, "", &out)
		if err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
			t.Fatalf("err = %v, want a hard error naming the unreachable issuer", err)
		}
		if idpHits != 0 || lastAssertion != "" {
			t.Errorf("idp hits = %d, assertion = %q; want no silent downgrade off the key lane", idpHits, lastAssertion)
		}
	})
}
