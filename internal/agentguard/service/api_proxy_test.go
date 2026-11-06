package service

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// tokenTransport stands for the credential half's transport: it sets the
// session's own credentials on every request it sends.
type tokenTransport struct{ base http.RoundTripper }

func (t tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "DPoP session-token")
	r.Header.Set("DPoP", "service-proof")
	return t.base.RoundTrip(r)
}

// upstreamSeen is what strazad saw of one proxied request.
type upstreamSeen struct {
	method, path, rawPath, query, body string
	auth, dpop, cookie                 string
}

// proxyDouble returns a double whose session points at a fake strazad that
// records each request into seen and answers with handle.
func proxyDouble(t *testing.T, handle http.HandlerFunc) (*apiDouble, chan upstreamSeen) {
	t.Helper()
	seen := make(chan upstreamSeen, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen <- upstreamSeen{method: r.Method, path: r.URL.Path, rawPath: r.URL.EscapedPath(), query: r.URL.RawQuery, body: string(body),
			auth: r.Header.Get("Authorization"), dpop: r.Header.Get("DPoP"), cookie: r.Header.Get("Cookie")}
		handle(w, r)
	}))
	t.Cleanup(srv.Close)
	s := liveSession()
	s.ServerURL = srv.URL + "/"
	return &apiDouble{session: s, transport: tokenTransport{base: srv.Client().Transport}}, seen
}

// proxyReq is a request to path with the harness headers of claude-code
// window w1 and the credentials a caller might try to pass along.
func proxyReq(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, localapi.BaseURL+path, strings.NewReader(body))
	r.Header.Set(localapi.HeaderHarness, "claude-code")
	r.Header.Set(localapi.HeaderHarnessVersion, "mcp")
	r.Header.Set("Authorization", "Bearer stolen")
	r.Header.Set("DPoP", "caller-proof")
	r.Header.Set("Cookie", "straza_session=stolen")
	return r
}

// TestProxyPassesToStrazad pins the MCP and connect pass-through: each
// local path maps to its strazad path with the query, method and body
// kept, the caller's credentials never reach strazad, and the session's
// own come from the credential half's transport.
func TestProxyPassesToStrazad(t *testing.T) {
	rows := []struct {
		method, path, body string
		want               upstreamSeen
	}{
		{http.MethodPost, localapi.RouteMCP, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
			upstreamSeen{method: http.MethodPost, path: "/mcp", body: `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`}},
		{http.MethodGet, localapi.RouteMCP + "/views-demo", "", upstreamSeen{method: http.MethodGet, path: "/mcp/views-demo"}},
		{http.MethodGet, strings.TrimSuffix(localapi.RouteConnect, "/") + "?user=kim", "",
			upstreamSeen{method: http.MethodGet, path: "/v1/connect", query: "user=kim"}},
		{http.MethodPatch, localapi.RouteConnect + "midpoint", `{"allowAgents":true}`,
			upstreamSeen{method: http.MethodPatch, path: "/v1/connect/midpoint", body: `{"allowAgents":true}`}},
		{http.MethodDelete, localapi.RouteConnect + "midpoint?user=bot", "",
			upstreamSeen{method: http.MethodDelete, path: "/v1/connect/midpoint", query: "user=bot"}},
		{http.MethodGet, localapi.RouteConnect + "callback?code=c1&state=s1", "",
			upstreamSeen{method: http.MethodGet, path: "/v1/connect/callback", query: "code=c1&state=s1"}},
	}
	for _, row := range rows {
		t.Run(row.method+" "+row.path, func(t *testing.T) {
			f, seen := proxyDouble(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
			a, _ := testAPI(f, false)
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, asCaller(proxyReq(row.method, row.path, row.body), "1000"))
			if w.Code != http.StatusOK || w.Body.String() != "ok" {
				t.Fatalf("answer = %d %q", w.Code, w.Body.String())
			}
			got := <-seen
			if row.want.rawPath == "" {
				row.want.rawPath = row.want.path
			}
			if got.method != row.want.method || got.path != row.want.path || got.rawPath != row.want.rawPath ||
				got.query != row.want.query || got.body != row.want.body {
				t.Errorf("strazad saw %+v, want %+v", got, row.want)
			}
			if got.auth != "DPoP session-token" || got.dpop != "service-proof" || got.cookie != "" {
				t.Errorf("strazad saw credentials %q, %q, %q; want only the session's own", got.auth, got.dpop, got.cookie)
			}
			if c := f.sessionCalls; len(c) == 0 || c[0].harness != (localapi.Harness{Name: "claude-code", Version: "mcp"}) || c[0].caller.UID != "1000" {
				t.Errorf("Session calls = %+v, want the caller's newest live claude-code session", c)
			}
		})
	}
}

// TestProxyStreams pins that the proxy flushes each piece of a stream as
// strazad sends it, which the standalone GET stream and the gateway's
// approval hold need: the first event arrives while strazad still holds
// the answer open.
func TestProxyStreams(t *testing.T) {
	release := make(chan struct{})
	f, _ := proxyDouble(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message\ndata: first\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "event: message\ndata: second\n\n")
	})
	defer close(release)
	a, _ := testAPI(f, false)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.routes().ServeHTTP(w, asCaller(r, "1000"))
	}))
	defer local.Close()
	req := proxyReq(http.MethodGet, localapi.RouteMCP, "")
	req.RequestURI, req.URL.Scheme, req.URL.Host = "", "http", strings.TrimPrefix(local.URL, "http://")
	resp, err := local.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		got <- line
	}()
	select {
	case line := <-got:
		if line != "event: message\n" {
			t.Errorf("first line = %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first event did not arrive while strazad held the stream open, so the proxy buffers")
	}
}

// TestProxySession pins how the proxy picks the session: the caller's live
// one when there is one, a start when there is none, which spends one of
// the caller's start tokens, and the credential half's refusal otherwise.
func TestProxySession(t *testing.T) {
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	t.Run("a live session starts nothing", func(t *testing.T) {
		f, _ := proxyDouble(t, ok)
		a, _ := testAPI(f, false)
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, asCaller(proxyReq(http.MethodPost, localapi.RouteMCP, "{}"), "1000"))
		if w.Code != http.StatusNoContent || len(f.sessionCalls) != 1 || f.sessionCalls[0].start {
			t.Errorf("answer %d after %+v, want one lookup", w.Code, f.sessionCalls)
		}
	})
	t.Run("no live session starts one", func(t *testing.T) {
		f, _ := proxyDouble(t, ok)
		f.liveErr = &Refused{Status: http.StatusUnauthorized, Sentence: "no live session"}
		a, _ := testAPI(f, false)
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, asCaller(proxyReq(http.MethodPost, localapi.RouteMCP, "{}"), "1000"))
		if w.Code != http.StatusNoContent || len(f.sessionCalls) != 2 || !f.sessionCalls[1].start {
			t.Errorf("answer %d after %+v, want a lookup and then a start", w.Code, f.sessionCalls)
		}
		fixed := time.Now()
		a.starts.now = func() time.Time { return fixed }
		for a.starts.allow("1000") {
		}
		w = httptest.NewRecorder()
		a.routes().ServeHTTP(w, asCaller(proxyReq(http.MethodPost, localapi.RouteMCP, "{}"), "1000"))
		if w.Code != http.StatusTooManyRequests || refusalOf(t, w) != tooManyStarts {
			t.Errorf("a start past the limit = %d %q, want 429", w.Code, w.Body.String())
		}
	})
	t.Run("a named window never starts from the proxy", func(t *testing.T) {
		f, _ := proxyDouble(t, ok)
		f.liveErr = &Refused{Status: http.StatusForbidden, Sentence: "session revoked (by an admin)."}
		a, _ := testAPI(f, false)
		r := proxyReq(http.MethodPost, localapi.RouteMCP, "{}")
		r.Header.Set(localapi.HeaderHarnessSession, "w1")
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, asCaller(r, "1000"))
		if w.Code != http.StatusForbidden || !strings.Contains(refusalOf(t, w), "revoked") {
			t.Errorf("answer = %d %q, want the window's refusal", w.Code, w.Body.String())
		}
		if c := f.sessionCalls; len(c) != 1 || c[0].start || c[0].harness.Session != "w1" {
			t.Errorf("Session calls = %+v, want one lookup of window w1 and no start", c)
		}
	})
	t.Run("a refused start passes its sentence", func(t *testing.T) {
		f, _ := proxyDouble(t, ok)
		f.sessionErr = &Refused{Status: http.StatusForbidden, Sentence: "This machine isn't enrolled for you yet. Run `straza enroll` and sign in with your work account."}
		a, _ := testAPI(f, false)
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, asCaller(proxyReq(http.MethodPost, localapi.RouteMCP, "{}"), "1000"))
		if w.Code != http.StatusForbidden || !strings.Contains(refusalOf(t, w), "isn't enrolled") {
			t.Errorf("answer = %d %q", w.Code, w.Body.String())
		}
	})
}

// TestProxyFailures pins the proxy's own refusals. strazad that cannot be
// dialed is a 503 sentence, because the request never left this machine
// and a later try can pass. Any other transport failure is a 502 sentence,
// because strazad may have run the request before its answer was lost. A
// transport the credential half refused passes that refusal's status and
// sentence. Every refusal made before the send, and only those, carries
// HeaderNotSent, so the MCP bridge can tell it from an ingress's 503.
func TestProxyFailures(t *testing.T) {
	dial := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
	for _, row := range []struct {
		name       string
		err        error
		sessionErr error // the refusal of the session lookup, before any send
		status     int
		says       string
		notSent    bool
	}{
		{name: "strazad cannot be dialed", err: dial, status: http.StatusServiceUnavailable, says: "could not reach the Straza server", notSent: true},
		{name: "a wrapped dial failure", err: fmt.Errorf("send: %w", dial), status: http.StatusServiceUnavailable,
			says: "could not reach the Straza server", notSent: true},
		{name: "the answer was lost", err: &net.OpError{Op: "read", Net: "tcp", Err: io.ErrUnexpectedEOF}, status: http.StatusBadGateway,
			says: "did not get an answer from the Straza server"},
		{name: "the session was revoked", err: &Refused{Status: http.StatusUnauthorized, Sentence: "This Straza session was revoked."},
			status: http.StatusUnauthorized, says: "was revoked", notSent: true},
		{name: "the service waits for a check-in", sessionErr: &Refused{Status: http.StatusServiceUnavailable, Sentence: waitingSentence},
			status: http.StatusServiceUnavailable, says: "waiting for the server", notSent: true},
		{name: "too many starts", sessionErr: &Refused{Status: http.StatusTooManyRequests, Sentence: tooManyStarts},
			status: http.StatusTooManyRequests, says: "too many", notSent: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := &apiDouble{session: liveSession(), transport: failingTransport{row.err}, sessionErr: row.sessionErr}
			a, logs := testAPI(f, false)
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, asCaller(proxyReq(http.MethodPost, localapi.RouteMCP, "{}"), "1000"))
			if w.Code != row.status || !strings.Contains(refusalOf(t, w), row.says) || len(logLines(t, logs)) != 1 {
				t.Errorf("answer = %d %q with log %s, want %d saying %q and one log line", w.Code, w.Body.String(), logs, row.status, row.says)
			}
			if got := w.Header().Get(localapi.HeaderNotSent) != ""; got != row.notSent {
				t.Errorf("not-sent mark = %v, want %v", got, row.notSent)
			}
		})
	}
}

// TestProxyDropsUpstreamNotSent pins that only the service marks a refusal
// as made before the send: the mark on an answer strazad, or anything in
// front of it, sends is removed, and a passed-through answer carries none.
func TestProxyDropsUpstreamNotSent(t *testing.T) {
	f, _ := proxyDouble(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(localapi.HeaderNotSent, "1")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	a, _ := testAPI(f, false)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, asCaller(proxyReq(http.MethodPost, localapi.RouteMCP, "{}"), "1000"))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get(localapi.HeaderNotSent) != "" {
		t.Errorf("answer = %d with mark %q, want strazad's 503 without the mark", w.Code, w.Header().Get(localapi.HeaderNotSent))
	}
}

// failingTransport fails every request with err.
type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// TestProxyRefusesEncodedPaths pins that the proxies forward only plain
// paths: an encoded slash, encoded dot segments, double encoding or a name
// outside the server name rule is refused with a sentence before any
// session is looked up, and strazad never sees the request. The clean rows
// are the positive control.
func TestProxyRefusesEncodedPaths(t *testing.T) {
	rows := []struct {
		name, path string
		upstream   string // the path strazad sees, "" for a refusal
		says       string
	}{
		{"clean server name", localapi.RouteMCP + "/views-demo", "/mcp/views-demo", ""},
		{"clean connect path", localapi.RouteConnect + "midpoint", "/v1/connect/midpoint", ""},
		{"encoded slash in the server name", localapi.RouteMCP + "/a%2F..%2F..%2Fv1%2Fdecide", "", "percent-encoded"},
		{"encoded dot segment as the server name", localapi.RouteMCP + "/%2e%2e", "", "percent-encoded"},
		{"double-encoded slash in the server name", localapi.RouteMCP + "/a%252Fb", "", "percent-encoded"},
		{"server name outside the rule", localapi.RouteMCP + "/Views_Demo", "", "is not valid"},
		{"encoded slash under connect", localapi.RouteConnect + "x%2F..%2F..%2Fdecide", "", "percent-encoded"},
		{"encoded dot segments under connect", localapi.RouteConnect + "%2e%2e/%2e%2e/audit/batch", "", "percent-encoded"},
		{"double-encoded dot segment under connect", localapi.RouteConnect + "%252e%252e/decide", "", "percent-encoded"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f, seen := proxyDouble(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
			a, logs := testAPI(f, false)
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, asCaller(proxyReq(http.MethodPost, row.path, "{}"), "1000"))
			if row.upstream != "" {
				if got := <-seen; w.Code != http.StatusNoContent || got.path != row.upstream {
					t.Fatalf("answer %d after strazad saw %q, want %q", w.Code, got.path, row.upstream)
				}
				return
			}
			if w.Code != http.StatusBadRequest || !strings.Contains(refusalOf(t, w), row.says) || len(logLines(t, logs)) != 1 {
				t.Errorf("answer = %d %q with log %s, want 400 saying %q and one log line", w.Code, w.Body.String(), logs, row.says)
			}
			if len(seen) != 0 || len(f.sessionCalls) != 0 {
				t.Errorf("strazad saw %d requests after %d session lookups, want none", len(seen), len(f.sessionCalls))
			}
		})
	}
}
