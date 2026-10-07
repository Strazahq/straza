package authn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/store/storetest"
)

// splitCanonical is the issuer name the split IdP puts in its tokens. It is
// never dialed: the document and the keys are served at the test server.
const splitCanonical = "http://idp.example/realms/x"

// splitIDP is an identity provider whose issuer name is not its address, the
// way Keycloak answers on a private network with its backchannel URLs
// dynamic: the document fetched at the dial address names the canonical
// issuer and puts the keys on the dial address.
type splitIDP struct {
	srv *httptest.Server
	// tokens signs with iss = splitCanonical, atDial with the same keys and
	// iss = the dial address.
	tokens, atDial *TokenService
}

// discoveryURL is where the split IdP serves its discovery document.
func (p *splitIDP) discoveryURL() string {
	return p.srv.URL + "/realms/x/.well-known/openid-configuration"
}

// newSplitIDP serves a discovery document naming docIssuer, with status as
// the document's HTTP status.
func newSplitIDP(t *testing.T, docIssuer string, status int) *splitIDP {
	t.Helper()
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "split.db")
	storetest.SeedSQLite(t, dsn)
	s, err := store.Open(config.Config{Store: config.Store{Driver: config.DriverSQLite, DSN: dsn}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	tokens, err := NewTokenService(ctx, s.SigningKeys(), splitCanonical, 0)
	if err != nil {
		t.Fatal(err)
	}
	atDial, err := NewTokenService(ctx, s.SigningKeys(), srv.URL+"/realms/x", 0)
	if err != nil {
		t.Fatal(err)
	}
	mux.HandleFunc("GET /realms/x/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                docIssuer,
			"authorization_endpoint":                splitCanonical + "/protocol/openid-connect/auth",
			"token_endpoint":                        srv.URL + "/realms/x/protocol/openid-connect/token",
			"jwks_uri":                              srv.URL + "/realms/x/protocol/openid-connect/certs",
			"id_token_signing_alg_values_supported": []string{"EdDSA"},
		})
	})
	mux.HandleFunc("GET /realms/x/protocol/openid-connect/certs", func(w http.ResponseWriter, _ *http.Request) {
		doc, _ := tokens.JWKS()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(doc)
	})
	return &splitIDP{srv: srv, tokens: tokens, atDial: atDial}
}

// TestExternalVerifierDiscoveryURL pins oidc.discoveryUrl: strazad fetches
// the document at the dial address, the document must name oidc.issuer byte
// for byte, and a boot that cannot confirm that fails.
func TestExternalVerifierDiscoveryURL(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		docIssuer string
		status    int
		unreached bool
		wantErr   []string
	}{
		{name: "document names the issuer", docIssuer: splitCanonical, status: http.StatusOK},
		{name: "document names another issuer", docIssuer: "http://other.example/realms/x", status: http.StatusOK,
			wantErr: []string{`names issuer "http://other.example/realms/x"`, `oidc.issuer is "` + splitCanonical + `"`, "Set oidc.issuer"}},
		{name: "trailing slash is another issuer", docIssuer: splitCanonical + "/", status: http.StatusOK,
			wantErr: []string{`names issuer "` + splitCanonical + `/"`}},
		{name: "document answers 404", docIssuer: splitCanonical, status: http.StatusNotFound,
			wantErr: []string{"answered HTTP 404", "oidc.discoveryUrl"}},
		{name: "discovery address unreachable", docIssuer: splitCanonical, status: http.StatusOK, unreached: true,
			wantErr: []string{"oidc.discoveryUrl", "reach"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp := newSplitIDP(t, tc.docIssuer, tc.status)
			at := idp.discoveryURL()
			if tc.unreached {
				idp.srv.Close()
			}
			_, err := NewExternalVerifier(ctx, splitCanonical, at, "straza", platformUsers(t), true)
			if len(tc.wantErr) == 0 {
				if err != nil {
					t.Fatalf("NewExternalVerifier: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("NewExternalVerifier succeeded, want a refused boot")
			}
			for _, want := range append(tc.wantErr, at) {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not say %q", err, want)
				}
			}
		})
	}
}

// TestExternalVerifierDiscoveryURLKeepsIssStrict proves the split moves only
// where strazad fetches, never what it accepts: a token must carry the
// canonical issuer and the client id, whatever address served the keys.
func TestExternalVerifierDiscoveryURLKeepsIssStrict(t *testing.T) {
	ctx := context.Background()
	idp := newSplitIDP(t, splitCanonical, http.StatusOK)
	v, err := NewExternalVerifier(ctx, splitCanonical, idp.discoveryURL(), "straza", platformUsers(t), true)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mint   *TokenService
		aud    string
		wantOK bool
	}{
		{"canonical issuer and client id", idp.tokens, "straza", true},
		{"issuer is the dial address", idp.atDial, "straza", false},
		{"another audience", idp.tokens, "midpoint-mcp", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := tc.mint.MintIDToken("sub-"+tc.aud, tc.aud, time.Minute, "ext-lee", "ext-lee@idp.example")
			if err != nil {
				t.Fatal(err)
			}
			u, err := v.VerifyLogin(ctx, raw)
			if tc.wantOK {
				if err != nil || u.Username != "ext-lee" {
					t.Fatalf("VerifyLogin = %q, %v, want ext-lee", u.Username, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("VerifyLogin accepted the token as %q, want a refusal", u.Username)
			}
		})
	}
}
