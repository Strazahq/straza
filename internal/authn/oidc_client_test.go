package authn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/store/storetest"
)

// idpFixture is a fully OIDC-discoverable "external IdP": the built-in
// issuer with its JWKS mounted, exactly what go-oidc needs. Using it keeps
// the external-client test hermetic, with no Dex container.
type idpFixture struct {
	srv    *httptest.Server
	store  store.Store
	tokens *TokenService
}

func newIDPFixture(t *testing.T) *idpFixture {
	t.Helper()
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "idp.db")
	storetest.SeedSQLite(t, dsn)
	s, err := store.Open(config.Config{Store: config.Store{
		Driver: config.DriverSQLite,
		DSN:    dsn,
	}})
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

	tokens, err := NewTokenService(ctx, s.SigningKeys(), srv.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	iss := NewIssuer(s.Users(), tokens, srv.URL)
	iss.interval = 5 * time.Millisecond
	iss.Routes(mux)
	mux.HandleFunc("GET /.well-known/straza/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		doc, _ := tokens.JWKS()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(doc)
	})
	return &idpFixture{srv: srv, store: s, tokens: tokens}
}

// login creates the user at the IdP (if absent) and runs the device flow,
// returning an ID token with audience clientID.
func (f *idpFixture) login(t *testing.T, username, clientID string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := f.store.Users().GetByUsername(ctx, username); errors.Is(err, store.ErrNotFound) {
		hash, _ := storetest.PasswordHash("pw!" + username)
		if _, err := f.store.Users().Create(ctx, store.User{
			Username: username, Email: username + "@idp.example", PasswordHash: hash,
		}); err != nil {
			t.Fatal(err)
		}
	}

	resp, err := http.PostForm(f.srv.URL+"/oidc/device_authorization", url.Values{"client_id": {clientID}})
	if err != nil {
		t.Fatal(err)
	}
	var auth map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&auth)
	_ = resp.Body.Close()

	resp, err = http.PostForm(f.srv.URL+"/oidc/device", url.Values{
		"user_code": {auth["user_code"].(string)}, "username": {username}, "password": {"pw!" + username},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	resp, err = http.PostForm(f.srv.URL+"/oidc/token", url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {auth["device_code"].(string)},
		"client_id":   {clientID},
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

func platformUsers(t *testing.T) store.UserRepo {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "platform.db")
	storetest.SeedSQLite(t, dsn)
	s, err := store.Open(config.Config{Store: config.Store{
		Driver: config.DriverSQLite,
		DSN:    dsn,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s.Users()
}

func TestExternalVerifierJITAndMapping(t *testing.T) {
	ctx := context.Background()
	idp := newIDPFixture(t)
	users := platformUsers(t)
	idToken := idp.login(t, "ext-kim", "straza")

	// JIT off: unknown identity refused (the enterprise default).
	strict, err := NewExternalVerifier(ctx, idp.srv.URL, "", "straza", users, false)
	if err != nil {
		t.Fatalf("NewExternalVerifier: %v", err)
	}
	if _, err := strict.VerifyLogin(ctx, idToken); !errors.Is(err, ErrUnknownIdentity) {
		t.Fatalf("JIT off: want ErrUnknownIdentity, got %v", err)
	}

	// JIT on: user provisioned with linked external id.
	jit, err := NewExternalVerifier(ctx, idp.srv.URL, "", "straza", users, true)
	if err != nil {
		t.Fatal(err)
	}
	u, err := jit.VerifyLogin(ctx, idToken)
	if err != nil {
		t.Fatalf("JIT login: %v", err)
	}
	if u.Username != "ext-kim" || u.Email != "ext-kim@idp.example" || u.ExternalID == "" {
		t.Errorf("provisioned user: %+v", u)
	}

	// Second login resolves the same user (by external id), no duplicate.
	again, err := jit.VerifyLogin(ctx, idp.login(t, "ext-kim", "straza"))
	if err != nil || again.ID != u.ID {
		t.Fatalf("re-login: %+v, %v", again, err)
	}
	all, _ := users.List(ctx)
	if len(all) != 1 {
		t.Errorf("users = %d, want 1", len(all))
	}

	// Disabled platform user is refused even with a valid IdP token.
	u.Status = store.UserDisabled
	if _, err := users.Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, err := jit.VerifyLogin(ctx, idp.login(t, "ext-kim", "straza")); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled user: %v", err)
	}
}

func TestExternalVerifierAccountLinking(t *testing.T) {
	ctx := context.Background()
	idp := newIDPFixture(t)
	users := platformUsers(t)

	// Pre-provisioned user (e.g. via admin) without external_id links on
	// first login.
	pre, err := users.Create(ctx, store.User{Username: "ext-lee", Email: "old@corp"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewExternalVerifier(ctx, idp.srv.URL, "", "straza", users, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.VerifyLogin(ctx, idp.login(t, "ext-lee", "straza"))
	if err != nil || got.ID != pre.ID || got.ExternalID == "" {
		t.Fatalf("linking: %+v, %v", got, err)
	}

	// A username already bound to a DIFFERENT subject must not be hijacked.
	otherIdp := newIDPFixture(t)
	v2, err := NewExternalVerifier(ctx, otherIdp.srv.URL, "", "straza", users, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v2.VerifyLogin(ctx, otherIdp.login(t, "ext-lee", "straza")); err == nil || !strings.Contains(err.Error(), "bound to another identity") {
		t.Fatalf("hijack attempt: %v", err)
	}
}

// TestExternalVerifierBootstrapCarveOut pins the first-admin bootstrap: with
// JIT off (the enterprise default) unknown identities stay refused, EXCEPT
// the configured bootstrap username. That one is provisioned so a
// pure-enterprise store can reach its first admin. The carve-out only
// creates the user. The straza-admin grant is the server's job.
func TestExternalVerifierBootstrapCarveOut(t *testing.T) {
	ctx := context.Background()
	idp := newIDPFixture(t)
	users := platformUsers(t)
	v, err := NewExternalVerifier(ctx, idp.srv.URL, "", "straza", users, false)
	if err != nil {
		t.Fatal(err)
	}
	v.BootstrapUsername = "root-kim"

	// Everyone else keeps the JIT-off refusal.
	if _, err := v.VerifyLogin(ctx, idp.login(t, "someone-else", "straza")); !errors.Is(err, ErrUnknownIdentity) {
		t.Fatalf("non-bootstrap identity: want ErrUnknownIdentity, got %v", err)
	}

	// The bootstrap username is provisioned and linked.
	u, err := v.VerifyLogin(ctx, idp.login(t, "root-kim", "straza"))
	if err != nil {
		t.Fatalf("bootstrap login: %v", err)
	}
	if u.Username != "root-kim" || u.ExternalID == "" {
		t.Errorf("provisioned user: %+v", u)
	}

	// Re-login resolves the same user; no duplicates.
	again, err := v.VerifyLogin(ctx, idp.login(t, "root-kim", "straza"))
	if err != nil || again.ID != u.ID {
		t.Fatalf("re-login: %+v, %v", again, err)
	}
	if all, _ := users.List(ctx); len(all) != 1 {
		t.Errorf("users = %d, want 1 (someone-else must not have been created)", len(all))
	}
}

func TestExternalVerifierRejectsWrongAudience(t *testing.T) {
	ctx := context.Background()
	idp := newIDPFixture(t)
	users := platformUsers(t)
	v, err := NewExternalVerifier(ctx, idp.srv.URL, "", "straza", users, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.VerifyLogin(ctx, idp.login(t, "ext-kim", "other-client")); err == nil {
		t.Fatal("token for another client accepted")
	}
	if _, err := v.VerifyLogin(ctx, "garbage.token.here"); err == nil {
		t.Fatal("garbage token accepted")
	}
}

// TestExternalVerifierProtectedAccount pins that no external identity
// resolves to the protected local account: not through the username
// fallback, which would link the account, not through a link that already
// exists, and not through provisioning. Every other account still links, and
// without a protected name the verifier maps as before.
func TestExternalVerifierProtectedAccount(t *testing.T) {
	const protected = "break-glass"
	cases := []struct {
		name string
		// row says the platform holds the protected account, and linked says
		// the row already carries the external subject.
		row, linked bool
		jit         bool
		bootstrap   string
		protect     string
		wantErr     error
	}{
		{name: "username fallback", row: true, protect: protected, wantErr: ErrProtectedAccount},
		{name: "account already linked", row: true, linked: true, protect: protected, wantErr: ErrProtectedAccount},
		{name: "provisioning", jit: true, protect: protected, wantErr: ErrProtectedAccount},
		{name: "bootstrap admin named like the account", row: true, bootstrap: protected, protect: protected, wantErr: ErrProtectedAccount},
		{name: "no protected name, the fallback links as before", row: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			idp := newIDPFixture(t)
			users := platformUsers(t)
			idToken := idp.login(t, protected, "straza")
			var before store.User
			if tc.row {
				row := store.User{Username: protected}
				if tc.linked {
					atIDP, err := idp.store.Users().GetByUsername(ctx, protected)
					if err != nil {
						t.Fatal(err)
					}
					row.ExternalID = atIDP.ID
				}
				var err error
				if before, err = users.Create(ctx, row); err != nil {
					t.Fatal(err)
				}
			}
			v, err := NewExternalVerifier(ctx, idp.srv.URL, "", "straza", users, tc.jit)
			if err != nil {
				t.Fatal(err)
			}
			v.BootstrapUsername, v.ProtectedUsername = tc.bootstrap, tc.protect

			got, err := v.VerifyLogin(ctx, idToken)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("VerifyLogin = %+v, %v, want the error %v", got, err, tc.wantErr)
			}
			if tc.wantErr == nil {
				if got.ID != before.ID || got.ExternalID == "" {
					t.Errorf("linked user = %+v, want the row %s with an external id", got, before.ID)
				}
				return
			}
			if got.ID != "" {
				t.Errorf("a refused login returned the user %+v", got)
			}
			if !tc.row {
				if all, _ := users.List(ctx); len(all) != 0 {
					t.Errorf("users = %d, want none provisioned", len(all))
				}
				other, err := v.VerifyLogin(ctx, idp.login(t, "ext-lee", "straza"))
				if err != nil || other.Username != "ext-lee" {
					t.Errorf("another identity = %+v, %v, want it provisioned", other, err)
				}
				return
			}
			after, err := users.GetByID(ctx, before.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.ExternalID != before.ExternalID {
				t.Errorf("the refused login changed the external id from %q to %q", before.ExternalID, after.ExternalID)
			}
		})
	}
}
