package entragraph

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The made-up identities of the tests.
const (
	testTenant   = "aaaaaaaa-0000-4000-8000-000000000001"
	testClientID = "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"
	testOwner    = "0d0d0d0d-0000-4000-8000-000000000001"
	testKID      = "6f1c2f0e-3b7a-4d19-9a55-2f4c8e1b7d10"
	roleGroup    = "9a9a9a9a-1111-4111-8111-111111111111"
	nestedGroup  = "9b9b9b9b-2222-4222-8222-222222222222"
	userPat      = "1a1a1a1a-3333-4333-8333-333333333333"
	userKim      = "1b1b1b1b-4444-4444-8444-444444444444"
	agentUser    = "2a2a2a2a-5555-4555-8555-555555555555"
	agentID      = "3a3a3a3a-6666-4666-8666-666666666666"
	uniqueName   = "straza-test-role-0f3c9a"
	// testAssertion is shaped like the certificate assertion the real signer
	// returns, so a leak test can look for it.
	testAssertion = "eyJhbGciOiJSUzI1NiIsIng1dCNTMjU2IjoidGh1bWIifQ.eyJzdWIiOiJzZXJ2aWNlIn0.Y2VydC1zaWduYXR1cmU"
	// graphHost is replaced by the Graph double's address in served bodies.
	graphHost = "https://graph.microsoft.com"
	// tokenMark is replaced by the last issued token in a served body.
	tokenMark = "{{token}}"
)

// fakeSigner records what it was asked to sign and returns testAssertion, or
// the scripted error.
type fakeSigner struct {
	mu       sync.Mutex
	calls    int
	clientID string
	audience string
	err      error
}

func (s *fakeSigner) SignCertificateAssertion(_ time.Time, clientID, audience string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.clientID, s.audience = clientID, audience
	if s.err != nil {
		return "", "", s.err
	}
	return testAssertion, testKID, nil
}

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

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

// jwtOf returns a token shaped like a JWT whose payload holds claims.
func jwtOf(claims map[string]any) string {
	enc := base64.RawURLEncoding
	payload, _ := json.Marshal(claims)
	return enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc.EncodeToString(payload) + ".c2lnbmF0dXJl"
}

// step is one scripted answer of the Graph double, matched to the requests
// in order. path is matched against the request's raw path, or against its
// raw path and query when it holds a question mark. A body that starts with
// @ is a file in testdata.
type step struct {
	method string
	path   string
	status int
	body   string
	header map[string]string
}

// seen is one request a double received.
type seen struct {
	method string
	uri    string
	header http.Header
	body   []byte
	// bearer says the request carried the last token the login double issued.
	bearer bool
}

// fixture is a client wired to a Graph double, a login double and a third
// server that must never be reached.
type fixture struct {
	t      *testing.T
	graph  *httptest.Server
	login  *httptest.Server
	away   *httptest.Server
	clock  *clock
	signer *fakeSigner
	client *Client

	mu     sync.Mutex
	steps  []step
	got    []seen
	forms  []url.Values
	tokens []string
	// loginAuth counts token requests that carried an Authorization header.
	loginAuth int
	awayHits  int
	// tokenAnswer, when set, answers the n-th token request instead of the
	// default 200 with a token for testOwner.
	tokenAnswer func(n int) (int, string)
}

// newFixture starts the doubles and a client of them, with steps as the
// Graph script.
func newFixture(t *testing.T, steps ...step) *fixture {
	t.Helper()
	f := &fixture{t: t, steps: steps, signer: &fakeSigner{}, clock: &clock{t: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}}
	f.graph = httptest.NewServer(http.HandlerFunc(f.serveGraph))
	f.login = httptest.NewServer(http.HandlerFunc(f.serveLogin))
	f.away = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.awayHits++
		f.mu.Unlock()
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(func() { f.graph.Close(); f.login.Close(); f.away.Close() })
	f.client = f.newClient(f.graph.URL, nil)
	return f
}

// newClient builds a client of the login double and the Graph endpoint base.
func (f *fixture) newClient(base string, hc *http.Client) *Client {
	f.t.Helper()
	c, err := New(Options{
		HTTP: hc, Tenant: testTenant, ClientID: testClientID, Signer: f.signer, Now: f.clock.now,
		graphBase: base, tokenURL: f.login.URL + "/" + testTenant + "/oauth2/v2.0/token",
	})
	if err != nil {
		f.t.Fatalf("New: %v", err)
	}
	return c
}

func (f *fixture) serveGraph(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	n := len(f.got)
	last := ""
	if len(f.tokens) > 0 {
		last = f.tokens[len(f.tokens)-1]
	}
	f.got = append(f.got, seen{method: r.Method, uri: r.RequestURI, header: r.Header.Clone(), body: body,
		bearer: last != "" && r.Header.Get("Authorization") == "Bearer "+last})
	var s step
	scripted := n < len(f.steps)
	if scripted {
		s = f.steps[n]
	}
	f.mu.Unlock()
	if !scripted {
		f.t.Errorf("the Graph double got request %d, %s %s, past the end of its script", n+1, r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	got := r.RequestURI
	if !strings.Contains(s.path, "?") {
		got, _, _ = strings.Cut(got, "?")
	}
	if r.Method != s.method || got != s.path {
		f.t.Errorf("Graph request %d is %s %s, want %s %s", n+1, r.Method, got, s.method, s.path)
	}
	for k, v := range s.header {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(s.status)
	_, _ = io.WriteString(w, strings.ReplaceAll(f.bodyOf(s.body), tokenMark, last))
}

func (f *fixture) serveLogin(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	f.forms = append(f.forms, r.PostForm)
	if r.Header.Get("Authorization") != "" {
		f.loginAuth++
	}
	n := len(f.forms)
	answer := f.tokenAnswer
	f.mu.Unlock()
	if answer != nil {
		status, body := answer(n)
		w.Header().Set("Content-Type", "application/json")
		if status >= 300 && status < 400 {
			w.Header().Set("Location", f.away.URL+"/token")
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, f.bodyOf(body))
		return
	}
	f.issue(w, jwtOf(map[string]any{"aud": graphHost, "oid": testOwner, "tid": testTenant, "n": n}))
}

// issue answers a token request with token and records it as the last one.
func (f *fixture) issue(w http.ResponseWriter, token string) {
	f.mu.Lock()
	f.tokens = append(f.tokens, token)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 3599})
}

// bodyOf resolves a step body: a testdata file for @name, with every Graph
// address pointed at the double.
func (f *fixture) bodyOf(body string) string {
	if name, ok := strings.CutPrefix(body, "@"); ok {
		body = load(f.t, name)
	}
	return strings.ReplaceAll(body, graphHost, f.graph.URL)
}

// load reads a file of testdata.
func load(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	return string(data)
}

// requests returns what the Graph double received.
func (f *fixture) requests() []seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seen(nil), f.got...)
}

// script replaces the Graph script, for a test whose steps name the doubles.
func (f *fixture) script(steps ...step) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = steps
}

// answerTokens makes answer answer every token request, and a redirect
// points at the away server. A token the answer issues is passed in tokens,
// so that failure messages withhold it.
func (f *fixture) answerTokens(answer func(n int) (int, string), tokens ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenAnswer = answer
	f.tokens = append(f.tokens, tokens...)
}

// form returns the form of the n-th token request, counted from zero.
func (f *fixture) form(n int) url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forms[n]
}

// tokenRequests counts the requests the login double received.
func (f *fixture) tokenRequests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.forms)
}

// done fails the test unless the whole script was asked for, every Graph
// request carried the last issued token, no token request carried an
// Authorization header and the away server was never reached.
func (f *fixture) done() {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.got) != len(f.steps) {
		f.t.Errorf("the Graph double got %d requests, want %d", len(f.got), len(f.steps))
	}
	for i, s := range f.got {
		if !s.bearer {
			f.t.Errorf("Graph request %d did not carry the last issued token", i+1)
		}
	}
	if f.loginAuth != 0 {
		f.t.Errorf("%d token requests carried an Authorization header", f.loginAuth)
	}
	if f.awayHits != 0 {
		f.t.Errorf("the away server got %d requests", f.awayHits)
	}
}

// show is err as a failure message may print it: every issued token and
// the assertion replaced by a placeholder.
func (f *fixture) show(err error) string {
	if err == nil {
		return "<nil>"
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	msg := strings.ReplaceAll(err.Error(), testAssertion, "<assertion>")
	for _, tok := range f.tokens {
		msg = strings.ReplaceAll(msg, tok, "<token>")
	}
	return msg
}

// noSecret fails the test when err repeats an issued token or the assertion.
func (f *fixture) noSecret(err error) {
	f.t.Helper()
	if err == nil {
		return
	}
	if f.show(err) != err.Error() {
		f.t.Errorf("the error repeats a token or the assertion: %s", f.show(err))
	}
}

// wantError fails the test unless err is not nil and holds every part.
func (f *fixture) wantError(err error, parts ...string) {
	f.t.Helper()
	if err == nil {
		f.t.Fatalf("err = nil, want an error holding %q", parts)
	}
	for _, p := range parts {
		if !strings.Contains(err.Error(), p) {
			f.t.Errorf("err = %s, want it to hold %q", f.show(err), p)
		}
	}
	f.noSecret(err)
}

// memberPage is a page of a member read that lists users by ID and links to
// next, a path on the Graph double, when next is not empty.
func memberPage(next string, ids ...string) string {
	var b strings.Builder
	b.WriteString(`{"value":[`)
	for i, id := range ids {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"@odata.type":"#microsoft.graph.user","id":%q}`, id)
	}
	b.WriteString("]")
	if next != "" {
		fmt.Fprintf(&b, `,"@odata.nextLink":%q`, graphHost+next)
	}
	b.WriteString("}")
	return b.String()
}
