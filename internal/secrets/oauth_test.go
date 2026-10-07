package secrets

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testProvider(t *testing.T, handler http.HandlerFunc) ProviderConfig {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return ProviderConfig{
		Name:     "github",
		ClientID: "cid-1", ClientSecret: "csec-1",
		AuthURL:  srv.URL + "/authorize",
		TokenURL: srv.URL + "/token",
	}
}

func TestAuthorizeURL(t *testing.T) {
	p := ProviderConfig{
		ClientID: "cid 1", // space forces escaping through the whole chain
		AuthURL:  "https://github.example/login/oauth/authorize",
	}
	got := AuthorizeURL(p, "https://straza.example/v1/connect/callback", "st&ate", []string{"repo", "read:org"})
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("AuthorizeURL not a URL: %v", err)
	}
	q := u.Query()
	if q.Get("client_id") != "cid 1" ||
		q.Get("redirect_uri") != "https://straza.example/v1/connect/callback" ||
		q.Get("state") != "st&ate" ||
		q.Get("scope") != "repo read:org" ||
		q.Get("response_type") != "code" {
		t.Errorf("query = %s", u.RawQuery)
	}
	if !strings.HasPrefix(got, "https://github.example/login/oauth/authorize?") {
		t.Errorf("base = %s", got)
	}
}

// TestExchangeCode drives the code→token exchange against a fake provider,
// covering the GitHub reference shape (JSON on Accept: application/json,
// error-in-200-body quirk) and the RFC 6749 shapes.
func TestExchangeCode(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		formBody   bool // respond form-encoded (provider ignores Accept)
		wantErr    string
		wantAccess string
		wantRefr   string
		wantExpiry bool
	}{
		{
			name:       "github app tokens with expiry and refresh",
			status:     200,
			body:       `{"access_token":"gho_A","refresh_token":"ghr_A","expires_in":28800,"token_type":"bearer"}`,
			wantAccess: "gho_A", wantRefr: "ghr_A", wantExpiry: true,
		},
		{
			name:       "classic non-expiring token",
			status:     200,
			body:       `{"access_token":"gho_B","token_type":"bearer","scope":"repo"}`,
			wantAccess: "gho_B",
		},
		{
			name:    "github error in a 200 body",
			status:  200,
			body:    `{"error":"bad_verification_code","error_description":"The code passed is incorrect or expired."}`,
			wantErr: "bad_verification_code",
		},
		{
			name:    "rfc 6749 error with 400",
			status:  400,
			body:    `{"error":"invalid_grant"}`,
			wantErr: "invalid_grant",
		},
		{
			name:    "success shape with no token",
			status:  200,
			body:    `{"token_type":"bearer"}`,
			wantErr: "no access token",
		},
		{
			name:    "non-2xx without an error body",
			status:  502,
			body:    `Bad Gateway`,
			wantErr: "502",
		},
		{
			name:    "garbage body",
			status:  200,
			body:    `<!doctype html><body>login</body>`,
			wantErr: "unparseable",
		},
		{
			name:       "form-encoded response (provider ignored Accept)",
			status:     200,
			body:       `access_token=gho_C&token_type=bearer&expires_in=3600&refresh_token=ghr_C`,
			formBody:   true,
			wantAccess: "gho_C", wantRefr: "ghr_C", wantExpiry: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/token" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Accept"); got != "application/json" {
					t.Errorf("Accept = %q, want application/json", got)
				}
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				for k, want := range map[string]string{
					"grant_type": "authorization_code", "code": "code-1",
					"redirect_uri": "https://straza.example/cb",
					"client_id":    "cid-1", "client_secret": "csec-1",
				} {
					if got := r.PostForm.Get(k); got != want {
						t.Errorf("form %s = %q, want %q", k, got, want)
					}
				}
				ct := "application/json"
				if tc.formBody {
					ct = "application/x-www-form-urlencoded; charset=utf-8"
				}
				w.Header().Set("Content-Type", ct)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})

			grant, expiry, err := ExchangeCode(context.Background(), http.DefaultClient, p, "code-1", "https://straza.example/cb")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExchangeCode: %v", err)
			}
			if grant.AccessToken != tc.wantAccess || grant.RefreshToken != tc.wantRefr {
				t.Errorf("grant = %+v", grant)
			}
			if tc.wantExpiry {
				if expiry == nil || time.Until(*expiry) <= 0 {
					t.Errorf("expiry = %v, want a future time", expiry)
				}
			} else if expiry != nil {
				t.Errorf("expiry = %v, want nil (non-expiring)", expiry)
			}
		})
	}
}

func TestRefreshGrant(t *testing.T) {
	t.Run("rotates both tokens", func(t *testing.T) {
		p := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			for k, want := range map[string]string{
				"grant_type": "refresh_token", "refresh_token": "ghr_old",
				"client_id": "cid-1", "client_secret": "csec-1",
			} {
				if got := r.PostForm.Get(k); got != want {
					t.Errorf("form %s = %q, want %q", k, got, want)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"gho_new","refresh_token":"ghr_new","expires_in":28800}`))
		})
		grant, expiry, err := RefreshGrant(context.Background(), http.DefaultClient, p, "ghr_old")
		if err != nil {
			t.Fatalf("RefreshGrant: %v", err)
		}
		if grant.AccessToken != "gho_new" || grant.RefreshToken != "ghr_new" {
			t.Errorf("grant = %+v", grant)
		}
		if expiry == nil {
			t.Error("expiry = nil, want a time")
		}
	})

	t.Run("keeps the old refresh token when the provider omits one", func(t *testing.T) {
		p := testProvider(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"gho_new2","expires_in":3600}`))
		})
		grant, _, err := RefreshGrant(context.Background(), http.DefaultClient, p, "ghr_keep")
		if err != nil {
			t.Fatalf("RefreshGrant: %v", err)
		}
		if grant.RefreshToken != "ghr_keep" {
			t.Errorf("refresh token = %q, want the old one kept", grant.RefreshToken)
		}
	})

	t.Run("invalid_grant fails", func(t *testing.T) {
		p := testProvider(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		})
		if _, _, err := RefreshGrant(context.Background(), http.DefaultClient, p, "ghr_dead"); err == nil ||
			!strings.Contains(err.Error(), "invalid_grant") {
			t.Fatalf("err = %v, want invalid_grant", err)
		}
	})

	t.Run("unreachable provider fails", func(t *testing.T) {
		p := ProviderConfig{Name: "github", ClientID: "c", ClientSecret: "s",
			TokenURL: "http://127.0.0.1:1/token"}
		if _, _, err := RefreshGrant(context.Background(), http.DefaultClient, p, "ghr"); err == nil {
			t.Fatal("want error for unreachable provider")
		}
	})
}
