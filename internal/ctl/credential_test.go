package ctl

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/strazahq/straza/internal/connect"
)

// bearerServer answers every request with 200 and records the method, the
// path and the bearer of each.
type bearerServer struct {
	mu   sync.Mutex
	seen []string
}

func (s *bearerServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen = append(s.seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *bearerServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// wrapperCall runs one of the three authenticated wrappers.
type wrapperCall struct {
	name string
	call func(c *Client, method, path string) error
}

var wrapperCalls = []wrapperCall{
	{"Do", func(c *Client, method, path string) error {
		return c.Do(context.Background(), method, path, nil, nil)
	}},
	{"DoBytes", func(c *Client, method, path string) error {
		_, err := c.DoBytes(context.Background(), method, path)
		return err
	}},
	{"DoRawBody", func(c *Client, method, path string) error {
		return c.DoRawBody(context.Background(), method, path, "application/yaml", []byte("kind: App\n"), nil)
	}},
}

// TestAPITokenIsTheCredential pins the token path: every call sends the
// token as the bearer, on the admin API and on /v1/connect alike, and a
// refused token is final, so the call is sent once and the server's sentence
// comes back.
func TestAPITokenIsTheCredential(t *testing.T) {
	for _, w := range wrapperCalls {
		for _, path := range []string{"/v1/admin/roles", "/v1/connect/github"} {
			t.Run(w.name+" sends the token on "+path, func(t *testing.T) {
				s := &bearerServer{}
				c := tokenClient(t, s.start(t).URL)
				if err := w.call(c, http.MethodPost, path); err != nil {
					t.Fatalf("token write: %v", err)
				}
				want := []string{"POST " + path + " Bearer " + testToken}
				if got := s.requests(); strings.Join(got, "|") != strings.Join(want, "|") {
					t.Errorf("requests = %q, want %q", got, want)
				}
			})
		}
		t.Run(w.name+" stops at a refused token", func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				rw.Header().Set("Content-Type", "application/json")
				rw.WriteHeader(http.StatusUnauthorized)
				_, _ = rw.Write([]byte(`{"error":"admin API token rejected: this token is unknown, expired or revoked. Mint a new token in the console under Settings, API tokens, or ask a Straza administrator to mint one. Then set STRAZA_API_TOKEN, or the token setting of the connector that sent this request, to the new token."}`))
			}))
			t.Cleanup(srv.Close)
			err := w.call(tokenClient(t, srv.URL), http.MethodGet, "/v1/admin/roles")
			if err == nil || err.Error() != "admin API token rejected: this token is unknown, expired or revoked. Mint a new token in the console under Settings, API tokens, or ask a Straza administrator to mint one. Then set STRAZA_API_TOKEN, or the token setting of the connector that sent this request, to the new token." {
				t.Fatalf("err = %v, want the server's sentence", err)
			}
			if n := calls.Load(); n != 1 {
				t.Errorf("requests = %d, want 1 and no renewal", n)
			}
		})
	}
}

// TestPolicyByNameSendsTheToken pins the by-name policy read, which keeps
// the status code and so has its own call site, to the same credential as
// every other admin call.
func TestPolicyByNameSendsTheToken(t *testing.T) {
	s := &bearerServer{}
	c := tokenClient(t, s.start(t).URL)
	if _, err := c.PolicyByName(context.Background(), "set-a"); err != nil {
		t.Fatalf("policy read on a token: %v", err)
	}
	want := []string{"GET /v1/admin/policies/set-a Bearer " + testToken}
	if got := s.requests(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("requests = %q, want %q", got, want)
	}
}

// TestConnectCallsSendTheToken pins the /v1/connect calls strazactl makes
// for another user: each goes out on the token and names the user where
// strazad reads it, in the query of a read or a removal and in the body of
// a change.
func TestConnectCallsSendTheToken(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		call func(c *Client) error
		want string
	}{
		{"list", func(c *Client) error { _, err := connect.List(ctx, c, "joe"); return err },
			"GET /v1/connect?user=joe"},
		{"paste a token", func(c *Client) error {
			_, err := connect.PasteToken(ctx, c, "github", "ghp_x", "", "joe")
			return err
		}, `POST /v1/connect/github {"token":"ghp_x","user":"joe"}`},
		{"allow agents", func(c *Client) error { return connect.SetAllowAgents(ctx, c, "github", "joe", true) },
			`PATCH /v1/connect/github {"allow_agents":true,"user":"joe"}`},
		{"remove", func(c *Client) error { return connect.Remove(ctx, c, "github", "joe") },
			"DELETE /v1/connect/github?user=joe"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var got []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				got = append(got, strings.TrimSpace(r.Method+" "+r.URL.RequestURI()+" "+string(body)), r.Header.Get("Authorization"))
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`[]`))
					return
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			t.Cleanup(srv.Close)
			if err := tc.call(tokenClient(t, srv.URL)); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			want := []string{tc.want, "Bearer " + testToken}
			mu.Lock()
			defer mu.Unlock()
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("requests = %q, want %q", got, want)
			}
		})
	}
}
