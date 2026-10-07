package authn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/strazahq/straza/internal/store"
)

// mintAssertion builds an RFC 7523-style EdDSA client assertion for tests.
func mintAssertion(t *testing.T, key ed25519.PrivateKey, iss, sub, aud string, ttl time.Duration, jti string) string {
	t.Helper()
	iat := time.Now().UTC().Truncate(time.Second)
	tok, err := jwt.NewBuilder().
		Issuer(iss).Subject(sub).Audience([]string{aud}).
		IssuedAt(iat).Expiration(iat.Add(ttl)).JwtID(jti).Build()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA(), key))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

// TestClientCredentialsGrant pins the headless NHI grant: a client assertion
// signed with the registered per-NHI Ed25519 key mints the same short ID
// token the device flow does. Everything else is refused with the right
// RFC 6749 error code: fail closed, no enumeration.
func TestClientCredentialsGrant(t *testing.T) {
	f := newIssuerFixture(t)
	ctx := context.Background()

	nhi, err := f.users.Create(ctx, store.User{
		Username: "ci-bot", Email: "ci@x.io", Attrs: `{"kind":"nhi"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, wrongPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	// The lookup closure owns identity policy (active + kind=nhi + key
	// registered); the issuer treats every closure error as failed client
	// authentication. This test's closure mimics the server's.
	f.issuer.NHIKeys = func(ctx context.Context, clientID string) (store.User, ed25519.PublicKey, error) {
		if clientID != nhi.Username {
			return store.User{}, nil, fmt.Errorf("no such NHI client")
		}
		u, err := f.users.GetByUsername(ctx, clientID)
		if err != nil || u.Status != store.UserActive {
			return store.User{}, nil, fmt.Errorf("client not eligible")
		}
		return u, pub, nil
	}

	form := func(clientID, assertion string) url.Values {
		v := url.Values{
			"grant_type": {"client_credentials"},
			"client_id":  {clientID},
		}
		if assertion != "" {
			v.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
			v.Set("client_assertion", assertion)
		}
		return v
	}
	aud := f.srv.URL

	cases := []struct {
		name      string
		form      url.Values
		wantCode  int
		wantError string // OAuth error code; "" = success
	}{
		{"happy path", form(nhi.Username,
			mintAssertion(t, priv, nhi.Username, nhi.Username, aud, time.Minute, "jti-happy")), 200, ""},
		{"wrong signing key", form(nhi.Username,
			mintAssertion(t, wrongPriv, nhi.Username, nhi.Username, aud, time.Minute, "jti-wrongkey")), 400, "invalid_client"},
		{"unknown client", form("ghost",
			mintAssertion(t, priv, "ghost", "ghost", aud, time.Minute, "jti-ghost")), 400, "invalid_client"},
		{"expired assertion", form(nhi.Username,
			mintAssertion(t, priv, nhi.Username, nhi.Username, aud, -2*time.Minute, "jti-expired")), 400, "invalid_client"},
		{"audience mismatch", form(nhi.Username,
			mintAssertion(t, priv, nhi.Username, nhi.Username, "https://elsewhere.example", time.Minute, "jti-aud")), 400, "invalid_client"},
		{"issuer/subject not the client", form(nhi.Username,
			mintAssertion(t, priv, "someone-else", nhi.Username, aud, time.Minute, "jti-iss")), 400, "invalid_client"},
		{"assertion lifetime too long", form(nhi.Username,
			mintAssertion(t, priv, nhi.Username, nhi.Username, aud, time.Hour, "jti-long")), 400, "invalid_client"},
		{"missing assertion", form(nhi.Username, ""), 400, "invalid_request"},
		{"missing client_id", form("",
			mintAssertion(t, priv, nhi.Username, nhi.Username, aud, time.Minute, "jti-noclient")), 400, "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := f.postForm(t, "/oidc/token", tc.form)
			if code != tc.wantCode {
				t.Fatalf("status = %d body %v, want %d", code, body, tc.wantCode)
			}
			if tc.wantError == "" {
				raw, _ := body["id_token"].(string)
				if raw == "" {
					t.Fatalf("no id_token in %v", body)
				}
				claims, err := f.tokens.VerifyIDToken(raw, "straza")
				if err != nil {
					t.Fatalf("minted token does not verify for straza: %v", err)
				}
				if claims.Subject != nhi.ID || claims.Username != nhi.Username {
					t.Errorf("claims = %+v, want sub %s username %s", claims, nhi.ID, nhi.Username)
				}
				if at, _ := body["access_token"].(string); at != raw {
					t.Errorf("access_token should mirror id_token (Straza API convention)")
				}
			} else if got, _ := body["error"].(string); got != tc.wantError {
				t.Errorf("error = %q (%v), want %q", got, body, tc.wantError)
			}
		})
	}

	t.Run("replayed jti refused", func(t *testing.T) {
		assertion := mintAssertion(t, priv, nhi.Username, nhi.Username, aud, time.Minute, "jti-replay")
		code, _ := f.postForm(t, "/oidc/token", form(nhi.Username, assertion))
		if code != 200 {
			t.Fatalf("first use: status %d", code)
		}
		code, body := f.postForm(t, "/oidc/token", form(nhi.Username, assertion))
		if code != 400 {
			t.Fatalf("replay accepted: %v", body)
		}
		if got, _ := body["error"].(string); got != "invalid_grant" {
			t.Errorf("replay error = %q, want invalid_grant", got)
		}
	})

	t.Run("assertion is not a wildcard credential", func(t *testing.T) {
		// A valid ASSERTION must never pass where a session/device/ID token
		// is expected: it has use-none semantics only at the token endpoint.
		assertion := mintAssertion(t, priv, nhi.Username, nhi.Username, aud, time.Minute, "jti-notatoken")
		if _, err := f.tokens.Verify(assertion); err == nil {
			t.Error("assertion verified as a session token")
		}
		if _, err := f.tokens.VerifyDeviceToken(assertion); err == nil {
			t.Error("assertion verified as a device token")
		}
		if _, err := f.tokens.VerifyIDToken(assertion, "straza"); err == nil {
			t.Error("assertion verified as an id token")
		}
	})

	t.Run("hooks observe every judged outcome exactly once", func(t *testing.T) {
		type call struct {
			userID, username, reason string
			success                  bool
		}
		var calls []call
		f.issuer.OnNHIGrant = func(userID, username string, r *http.Request) {
			calls = append(calls, call{userID: userID, username: username, success: true})
		}
		f.issuer.OnNHIGrantFailed = func(userID, username, reason string, r *http.Request) {
			calls = append(calls, call{userID: userID, username: username, reason: reason})
		}
		defer func() { f.issuer.OnNHIGrant, f.issuer.OnNHIGrantFailed = nil, nil }()

		cases := []struct {
			name string
			form url.Values
			want []call // appended per case; nil = no hook fires
		}{
			{"success", form(nhi.Username,
				mintAssertion(t, priv, nhi.Username, nhi.Username, aud, time.Minute, "jti-hook-ok")),
				[]call{{userID: nhi.ID, username: nhi.Username, success: true}}},
			{"unknown client stays anonymous", form("ghost",
				mintAssertion(t, priv, "ghost", "ghost", aud, time.Minute, "jti-hook-ghost")),
				[]call{{reason: "unknown or ineligible client"}}},
			{"bad assertion names the known client", form(nhi.Username,
				mintAssertion(t, wrongPriv, nhi.Username, nhi.Username, aud, time.Minute, "jti-hook-bad")),
				[]call{{userID: nhi.ID, username: nhi.Username, reason: "client assertion rejected"}}},
			{"malformed request judges nothing", form(nhi.Username, ""), nil},
		}
		for _, tc := range cases {
			calls = nil
			f.postForm(t, "/oidc/token", tc.form)
			if len(calls) != len(tc.want) {
				t.Fatalf("%s: %d hook calls (%v), want %d", tc.name, len(calls), calls, len(tc.want))
			}
			for i, want := range tc.want {
				if calls[i] != want {
					t.Errorf("%s: call = %+v, want %+v", tc.name, calls[i], want)
				}
			}
		}

		calls = nil
		replay := mintAssertion(t, priv, nhi.Username, nhi.Username, aud, time.Minute, "jti-hook-replay")
		f.postForm(t, "/oidc/token", form(nhi.Username, replay))
		f.postForm(t, "/oidc/token", form(nhi.Username, replay))
		if len(calls) != 2 || !calls[0].success || calls[1].reason != "client assertion replayed" {
			t.Errorf("replay pair = %+v, want one success then one replay failure", calls)
		}
	})

	t.Run("grant disabled without a lookup", func(t *testing.T) {
		f.issuer.NHIKeys = nil
		defer func() {
			f.issuer.NHIKeys = func(ctx context.Context, clientID string) (store.User, ed25519.PublicKey, error) {
				return store.User{}, nil, fmt.Errorf("unused")
			}
		}()
		code, body := f.postForm(t, "/oidc/token",
			form(nhi.Username, mintAssertion(t, priv, nhi.Username, nhi.Username, aud, time.Minute, "jti-disabled")))
		if code != 400 {
			t.Fatalf("status %d %v", code, body)
		}
		if got, _ := body["error"].(string); got != "unsupported_grant_type" {
			t.Errorf("error = %q, want unsupported_grant_type", got)
		}
	})
}
