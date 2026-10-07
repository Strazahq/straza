package agentguard

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

// refusedRedirect is the fixed part of the redirect refusal.
const refusedRedirect = "so the request was not followed and its credentials were not sent there"

// landing records whether the requests a redirect target receives carry the
// secret, in the Authorization header or in the body.
type landing struct {
	mu      sync.Mutex
	hits    int
	carried int
}

func (l *landing) record(r *http.Request, secret string) {
	body, _ := io.ReadAll(r.Body)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hits++
	if secret != "" && (strings.Contains(r.Header.Get("Authorization"), secret) || strings.Contains(string(body), secret)) {
		l.carried++
	}
}

func (l *landing) seen() (hits, carried int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hits, l.carried
}

// redirectLane is one request of the straza client that carries a
// credential. call sends it to origin with secret as that credential,
// trusting origin's certificate when origin serves https.
type redirectLane struct {
	name string
	// optional is set when the lane may send the request without a secret.
	optional bool
	// fixedTLS is set when the lane builds its own transport, so no test
	// can make it trust the test certificate.
	fixedTLS bool
	// statuses are the redirects the lane is tried with: a 302 for a GET,
	// and a 307 and a 308 for a POST, the two that re-send its body.
	statuses []int
	call     func(ctx context.Context, t *testing.T, origin *httptest.Server, secret string) error
}

// tlsFrom returns the transport that trusts origin when it serves https.
func tlsFrom(origin *httptest.Server, base http.RoundTripper) http.RoundTripper {
	if origin.TLS != nil {
		return origin.Client().Transport
	}
	return base
}

// sessionStoreFor enrols a store against origin with a live session holding
// token, the state the MCP proxy and the session API read their token from.
func sessionStoreFor(t *testing.T, origin *httptest.Server, token string) *Store {
	t.Helper()
	store := pinStore(t, origin.URL, nil)
	if err := store.SaveSession(Session{
		SessionID: "s1", SessionToken: token, Harness: "claude-code/mcp",
		IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

// clientFor is NewClient against origin, trusting its certificate.
func clientFor(origin *httptest.Server) *Client {
	c := NewClient(origin.URL)
	c.HTTP.Transport = tlsFrom(origin, c.HTTP.Transport)
	return c
}

var (
	getRedirects  = []int{http.StatusFound}
	bodyRedirects = []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect}
)

var redirectLanes = []redirectLane{
	{name: "snapshot fetch", optional: true, statuses: getRedirects,
		call: func(ctx context.Context, _ *testing.T, origin *httptest.Server, secret string) error {
			_, _, _, err := clientFor(origin).FetchSnapshot(ctx, secret, "")
			return err
		}},
	{name: "refresh, session token in the body", statuses: bodyRedirects,
		call: func(ctx context.Context, _ *testing.T, origin *httptest.Server, secret string) error {
			_, err := clientFor(origin).Refresh(ctx, secret, "claude-code", "2.1.0", Attestation{})
			return err
		}},
	{name: "device check-in, device token in the body", statuses: bodyRedirects,
		call: func(ctx context.Context, _ *testing.T, origin *httptest.Server, secret string) error {
			_, err := clientFor(origin).CheckinDevice(ctx, secret, "claude-code", "2.1.0", Attestation{})
			return err
		}},
	{name: "enrol, ID token in the body", statuses: bodyRedirects,
		call: func(ctx context.Context, _ *testing.T, origin *httptest.Server, secret string) error {
			_, err := clientFor(origin).Enroll(ctx, secret, "laptop", "linux", "fp")
			return err
		}},
	{name: "MCP proxy", statuses: getRedirects,
		call: func(ctx context.Context, t *testing.T, origin *httptest.Server, secret string) error {
			store := sessionStoreFor(t, origin, secret)
			hc := gatewayHTTPClient(store, NewClient(origin.URL), "claude-code")
			tr := hc.Transport.(*sessionAuthTransport)
			tr.base = tlsFrom(origin, tr.base)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.URL+"/mcp", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := hc.Do(req)
			if err == nil {
				_ = resp.Body.Close()
			}
			return err
		}},
	{name: "session API", statuses: getRedirects,
		call: func(ctx context.Context, t *testing.T, origin *httptest.Server, secret string) error {
			api, err := NewSessionAPI(sessionStoreFor(t, origin, secret))
			if err != nil {
				t.Fatal(err)
			}
			tr := api.client.HTTP.Transport.(*sessionAuthTransport)
			tr.base = tlsFrom(origin, tr.base)
			return api.Do(ctx, http.MethodGet, "/v1/self", nil, nil)
		}},
	{name: "push stream", statuses: getRedirects,
		call: func(ctx context.Context, _ *testing.T, origin *httptest.Server, secret string) error {
			e := newEdgePush(origin.URL, nil, nil, nil, nil)
			e.http.Transport = tlsFrom(origin, e.http.Transport)
			_, _, err := e.stream(ctx, secret)
			return err
		}},
	{name: "doctor push probe", fixedTLS: true, statuses: getRedirects,
		call: func(ctx context.Context, _ *testing.T, origin *httptest.Server, secret string) error {
			_, err := probeEdgePush(ctx, origin.URL, secret)
			return err
		}},
}

// TestClientRedirectKeepsTokenOnOrigin pins the redirect rule on every
// request of the straza client that carries a credential, in the
// Authorization header or in the body. It follows a redirect only within its
// scheme, host and port, and every other redirect is refused with a sentence
// before anything is sent there. The snapshot fetch, whose client also makes
// requests with no credential, keeps Go's policy for those.
func TestClientRedirectKeepsTokenOnOrigin(t *testing.T) {
	cases := []struct {
		name     string
		tls      bool // the origin serves https
		noSecret bool
		target   func(origin, other *httptest.Server) string
		followed bool
	}{
		{name: "same host and port",
			target: func(o, _ *httptest.Server) string { return o.URL + "/landing" }, followed: true},
		{name: "another port",
			target: func(_, x *httptest.Server) string { return x.URL + "/landing" }},
		{name: "https to http on the same port", tls: true,
			target: func(o, _ *httptest.Server) string { return "http://" + o.Listener.Addr().String() + "/landing" }},
		{name: "another host",
			target: func(_, x *httptest.Server) string {
				_, port, _ := net.SplitHostPort(x.Listener.Addr().String())
				return "http://localhost:" + port + "/landing"
			}},
		{name: "no credential, another port", noSecret: true,
			target: func(_, x *httptest.Server) string { return x.URL + "/landing" }, followed: true},
	}
	for _, lane := range redirectLanes {
		for _, status := range lane.statuses {
			for _, tc := range cases {
				if (tc.noSecret && !lane.optional) || (tc.tls && lane.fixedTLS) {
					continue
				}
				t.Run(lane.name+", "+http.StatusText(status)+", "+tc.name, func(t *testing.T) {
					secret := "secret-credential-7f3a"
					if tc.noSecret {
						secret = ""
					}
					var target string
					atOrigin, atOther := &landing{}, &landing{}
					serve := func(l *landing) http.HandlerFunc {
						return func(w http.ResponseWriter, r *http.Request) {
							if r.URL.Path != "/landing" {
								http.Redirect(w, r, target, status)
								return
							}
							l.record(r, secret)
							w.Header().Set("Content-Type", "application/json")
							w.Header().Set("X-Straza-Snapshot-Id", "snap-1")
							_, _ = w.Write([]byte("{}"))
						}
					}
					var origin *httptest.Server
					if tc.tls {
						origin = httptest.NewTLSServer(serve(atOrigin))
					} else {
						origin = httptest.NewServer(serve(atOrigin))
					}
					defer origin.Close()
					other := httptest.NewServer(serve(atOther))
					defer other.Close()
					target = tc.target(origin, other)

					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					err := lane.call(ctx, t, origin, secret)

					originHits, originCarried := atOrigin.seen()
					otherHits, _ := atOther.seen()
					if !tc.followed {
						if err == nil || !strings.Contains(err.Error(), refusedRedirect) {
							t.Fatalf("err = %v, want the refused-redirect sentence", err)
						}
						if strings.Contains(err.Error(), "secret-credential") {
							t.Errorf("the error text carries the credential: %v", err)
						}
						if originHits+otherHits != 0 {
							t.Errorf("the redirect target got %d requests, want none", originHits+otherHits)
						}
						return
					}
					if err != nil {
						t.Fatalf("err = %v, want the landing's answer", err)
					}
					wantCarried := 1
					if secret == "" {
						wantCarried = 0
					}
					if originHits+otherHits != 1 || originCarried != wantCarried {
						t.Errorf("the landing got %d requests, %d with the credential; want 1 and %d", originHits+otherHits, originCarried, wantCarried)
					}
				})
			}
		}
	}
}
