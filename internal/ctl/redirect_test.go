package ctl

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/sameorigin"
)

// TestClientRedirectKeepsCredentialsOnOrigin pins strazactl's redirect rule:
// a request that carries a credential, the admin or login token in the
// Authorization header or a session, device or ID token in the check-in
// body, follows a redirect only within its scheme, host and port. Every
// other redirect is refused before anything is sent there, with a sentence
// that names the redirect and does not call strazad unreachable.
func TestClientRedirectKeepsCredentialsOnOrigin(t *testing.T) {
	const secret = "secret-credential-9c1e"
	lanes := []struct {
		name     string
		statuses []int
		call     func(ctx context.Context, c *Client) error
	}{
		{"admin call, token in the header", []int{http.StatusFound},
			func(ctx context.Context, c *Client) error {
				_, _, err := c.doRaw(ctx, http.MethodGet, "/v1/admin/users", secret, nil)
				return err
			}},
		{"session refresh, session token in the body", []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect},
			func(ctx context.Context, c *Client) error {
				_, err := c.checkin(ctx, map[string]any{"session_token": secret})
				return err
			}},
		{"device check-in, device token in the body", []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect},
			func(ctx context.Context, c *Client) error {
				_, err := c.checkin(ctx, map[string]any{"device_token": secret})
				return err
			}},
		{"login check-in, ID token in the body", []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect},
			func(ctx context.Context, c *Client) error {
				_, err := c.checkin(ctx, map[string]any{"id_token": secret})
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
					c.CredsPath = filepath.Join(t.TempDir(), "credentials.json")
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

// TestReauthKeepsTheRedirectSentence pins that a refused redirect on the
// session refresh or on the device check-in reaches the person as the
// redirect sentence alone, because a new login would meet the same
// redirect. A refused check-in keeps the advice to log in (positive
// control).
func TestReauthKeepsTheRedirectSentence(t *testing.T) {
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	const redirect, refuse = "redirect", "refuse"
	cases := []struct {
		name            string
		refresh, device string
		deviceCred      string
		want            string
	}{
		{"refresh redirects, no device credential", redirect, "", "", ""},
		{"refresh refused, device check-in redirects", refuse, redirect, "dev-cred", ""},
		{"refresh and device check-in redirect", redirect, redirect, "dev-cred", ""},
		{"refresh refused, no device credential", refuse, "", "",
			"session refresh failed (run `strazactl login`): checkin refused: session expired"},
		{"refresh and device check-in refused", refuse, refuse, "dev-cred",
			"session re-establish failed (run `strazactl login`): checkin refused: session expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				answer := tc.refresh
				if strings.Contains(string(body), `"device_token"`) {
					answer = tc.device
				}
				switch {
				case r.URL.Path != "/v1/checkin":
					w.WriteHeader(http.StatusTeapot)
				case answer == redirect:
					http.Redirect(w, r, other.URL+"/v1/checkin", http.StatusTemporaryRedirect)
				default:
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":"session expired"}`))
				}
			}))
			defer origin.Close()
			c := NewClient(origin.URL)
			c.CredsPath = filepath.Join(t.TempDir(), "credentials.json")
			if err := c.saveCreds(credentials{Server: origin.URL, SessionToken: "stale", DeviceToken: tc.deviceCred}); err != nil {
				t.Fatal(err)
			}
			want := tc.want
			if want == "" {
				want = (&sameorigin.RedirectError{From: origin.URL, To: other.URL}).Error()
			}
			err := c.Do(context.Background(), http.MethodGet, "/v1/admin/users", nil, nil)
			if err == nil || err.Error() != want {
				t.Fatalf("err\n got %v\nwant %s", err, want)
			}
		})
	}
}
