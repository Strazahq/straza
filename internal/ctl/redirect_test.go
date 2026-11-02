package ctl

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestClientRedirectKeepsCredentialsOnOrigin pins strazactl's redirect rule:
// a request that carries a credential, the admin API token in the
// Authorization header or a pasted connection token in the body, follows a
// redirect only within its scheme, host and port. Every other
// redirect is refused before anything is sent there, with a sentence that
// names the redirect and does not call strazad unreachable.
func TestClientRedirectKeepsCredentialsOnOrigin(t *testing.T) {
	const secret = "secret-credential-9c1e"
	lanes := []struct {
		name     string
		statuses []int
		call     func(ctx context.Context, c *Client) error
	}{
		{"admin call, token in the header", []int{http.StatusFound},
			func(ctx context.Context, c *Client) error {
				_, _, err := c.doRaw(ctx, http.MethodGet, "/v1/admin/users", nil)
				return err
			}},
		{"pasted connection token in the body", []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect},
			func(ctx context.Context, c *Client) error {
				_, _, err := c.doRaw(ctx, http.MethodPost, "/v1/connect/github", map[string]string{"token": secret})
				return err
			}},
	}
	cases := []struct {
		name     string
		tls      bool
		target   func(origin, other *httptest.Server) string
		followed bool
	}{
		{"same host and port", false, func(o, _ *httptest.Server) string { return o.URL + "/landing" }, true},
		{"another port", false, func(_, x *httptest.Server) string { return x.URL + "/landing" }, false},
		{"https to http on the same port", true, func(o, _ *httptest.Server) string { return "http://" + o.Listener.Addr().String() + "/landing" }, false},
		{"another host", false, func(_, x *httptest.Server) string {
			_, port, _ := net.SplitHostPort(x.Listener.Addr().String())
			return "http://localhost:" + port + "/landing"
		}, false},
	}
	for _, lane := range lanes {
		for _, status := range lane.statuses {
			for _, tc := range cases {
				t.Run(lane.name+", "+http.StatusText(status)+", "+tc.name, func(t *testing.T) {
					var mu sync.Mutex
					hits, carried := 0, 0
					var target string
					serve := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path != "/landing" {
							http.Redirect(w, r, target, status)
							return
						}
						body, _ := io.ReadAll(r.Body)
						mu.Lock()
						hits++
						if strings.Contains(r.Header.Get("Authorization"), secret) || strings.Contains(string(body), secret) {
							carried++
						}
						mu.Unlock()
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"session_id":"s1","session_token":"tok","user":"kim"}`))
					})
					var origin *httptest.Server
					if tc.tls {
						origin = httptest.NewTLSServer(serve)
					} else {
						origin = httptest.NewServer(serve)
					}
					defer origin.Close()
					other := httptest.NewServer(serve)
					defer other.Close()
					target = tc.target(origin, other)

					c := NewClient(origin.URL)
					c.APIToken = secret
					if tc.tls {
						c.HTTP.Transport = origin.Client().Transport
					}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					err := lane.call(ctx, c)

					mu.Lock()
					defer mu.Unlock()
					if !tc.followed {
						if err == nil || !strings.HasPrefix(err.Error(), origin.URL+" answered with a redirect to ") ||
							!strings.Contains(err.Error(), "so the request was not followed and its credentials were not sent there") {
							t.Fatalf("err = %v, want the refused-redirect sentence on its own", err)
						}
						if strings.Contains(err.Error(), secret) {
							t.Errorf("the error text carries the credential: %v", err)
						}
						if hits != 0 {
							t.Errorf("the redirect target got %d requests, want none", hits)
						}
						return
					}
					if err != nil || hits != 1 || carried != 1 {
						t.Errorf("err %v, %d requests at the landing with %d carrying the credential; want one that carries it", err, hits, carried)
					}
				})
			}
		}
	}
}
