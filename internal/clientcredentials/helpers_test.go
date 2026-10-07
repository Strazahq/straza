package clientcredentials

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testAssertion is shaped like the signed JWT the real signer returns, so a
// leak test can look for it and for its opening letters.
const testAssertion = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJzYW0tc3JlLWFnZW50In0.c2lnbmF0dXJlLWJ5dGVz"

// fakeSigner signs by counting: every call returns a fresh value, as the real
// signer does with its jti, or the scripted error.
type fakeSigner struct {
	mu       sync.Mutex
	calls    int
	clientID string
	audience string
	err      error
}

func (s *fakeSigner) SignAssertion(_ time.Time, clientID, audience string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	s.calls++
	s.clientID, s.audience = clientID, audience
	return fmt.Sprintf("%s%d", testAssertion, s.calls), nil
}

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// provider is a token endpoint double. It answers a 300 second Bearer token
// named after the client and the request count unless handler is set.
type provider struct {
	srv      *httptest.Server
	requests atomic.Int64
	mu       sync.Mutex
	forms    []url.Values
	headers  []http.Header
	handler  http.HandlerFunc
}

func newProvider(t *testing.T) *provider {
	t.Helper()
	p := &provider{}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *provider) serve(w http.ResponseWriter, r *http.Request) {
	n := p.requests.Add(1)
	_ = r.ParseForm()
	p.mu.Lock()
	p.forms = append(p.forms, r.PostForm)
	p.headers = append(p.headers, r.Header.Clone())
	h := p.handler
	p.mu.Unlock()
	if h != nil {
		h(w, r)
		return
	}
	answer(w, http.StatusOK, map[string]any{
		"access_token": fmt.Sprintf("tok-%s-%d", r.PostForm.Get("client_id"), n),
		"token_type":   "Bearer", "expires_in": 300,
	})
}

func (p *provider) setHandler(h http.HandlerFunc) {
	p.mu.Lock()
	p.handler = h
	p.mu.Unlock()
}

func (p *provider) lastForm() url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.forms[len(p.forms)-1]
}

func answer(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// rig is one Tokens under test with its doubles.
type rig struct {
	tokens *Tokens
	signer *fakeSigner
	clock  *clock
	idp    *provider
	logs   *syncBuffer
}

// syncBuffer is a log sink the fetch goroutine and the test may share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

const (
	testAudience = "https://idp.example/realms/x"
	testKeysURL  = "https://straza.example/.well-known/straza/client-assertion-jwks.json"
)

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{signer: &fakeSigner{}, clock: newClock(), idp: newProvider(t), logs: &syncBuffer{}}
	r.tokens = New(Options{
		Providers: map[string]Provider{"keycloak": {TokenURL: r.idp.srv.URL + "/token?realm=x", Audience: testAudience, Scopes: []string{"midpoint-mcp", "profile"}}},
		Signer:    r.signer,
		KeysURL:   testKeysURL,
		Log:       slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:       r.clock.now,
	})
	return r
}

func sam() Request {
	return Request{Provider: "keycloak", Server: "midpoint", ServerID: "app-1", UserID: "u-sam", ClientID: "sam-sre-agent", Session: "ses-sam"}
}

func joe() Request {
	return Request{Provider: "keycloak", Server: "midpoint", ServerID: "app-1", UserID: "u-joe", ClientID: "joe-java-developer-agent", Session: "ses-joe"}
}

// waitRequests waits until the provider has seen n requests, for the fetches
// that run in the background.
func waitRequests(t *testing.T, p *provider, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for p.requests.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("the provider saw %d requests, want %d", p.requests.Load(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitIdle waits until no fetch is in flight, so a test reads a settled cache.
func waitIdle(t *testing.T, tk *Tokens) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		tk.mu.Lock()
		busy := false
		for _, e := range tk.entries {
			busy = busy || e.flight != nil
		}
		tk.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a fetch is still in flight")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
