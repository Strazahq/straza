package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
)

// testBodyBound is the read bound the served tests use, short enough to
// wait out and long enough that a loaded test box still reads a whole small
// body well inside it.
const testBodyBound = 300 * time.Millisecond

// serveBody starts h on a test server shaped like strazad's, with
// ReadTimeout at testBodyBound and with TLS and HTTP/2 when h2 is set.
// shape, when not nil, changes the server before it starts.
func serveBody(t *testing.T, h http.Handler, h2 bool, shape func(*http.Server)) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ReadHeaderTimeout = 5 * time.Second
	srv.Config.ReadTimeout = testBodyBound
	srv.Config.IdleTimeout = 120 * time.Second
	if shape != nil {
		shape(srv.Config)
	}
	if h2 {
		srv.EnableHTTP2 = true
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv
}

// protoOf names the protocol a served test runs on.
func protoOf(h2 bool) string {
	if h2 {
		return "HTTP/2"
	}
	return "HTTP/1.1"
}

// TestReadTimeoutCutsAStalledBody sends all of a body but its last byte and
// then waits, as a client that pins a connection's buffered body does. The
// handler's read must fail with the deadline error within the bound, on
// HTTP/1.1 and on HTTP/2.
func TestReadTimeoutCutsAStalledBody(t *testing.T) {
	t.Parallel()
	for _, h2 := range []bool{false, true} {
		t.Run(protoOf(h2), func(t *testing.T) {
			t.Parallel()
			type result struct {
				n       int
				err     error
				elapsed time.Duration
			}
			done := make(chan result, 1)
			srv := serveBody(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				start := time.Now()
				b, err := io.ReadAll(r.Body)
				done <- result{len(b), err, time.Since(start)}
				w.WriteHeader(http.StatusBadRequest)
			}), h2, nil)

			pr, pw := io.Pipe()
			t.Cleanup(func() { _ = pw.Close() })
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/oidc/device", pr)
			if err != nil {
				t.Fatal(err)
			}
			req.ContentLength = 1000
			go func() {
				if resp, err := srv.Client().Do(req); err == nil {
					_ = resp.Body.Close()
				}
			}()
			if _, err := pw.Write(make([]byte, 999)); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-done:
				if !errors.Is(got.err, os.ErrDeadlineExceeded) {
					t.Fatalf("the stalled body read ended with %d bytes and error %v, want the read deadline error", got.n, got.err)
				}
			case <-time.After(20 * testBodyBound):
				t.Fatalf("the stalled body read still waits after %s, want it cut at %s", 20*testBodyBound, testBodyBound)
			}
		})
	}
}

// streamPastTheBound reads r's body to its end, or only its first byte when
// partial is set, then streams six chunks over three bounds. It answers the
// request context's error at the end, or the moment the context ends.
func streamPastTheBound(w http.ResponseWriter, r *http.Request, partial bool) error {
	body := r.Body
	if partial {
		body = io.NopCloser(io.LimitReader(r.Body, 1))
	}
	if _, err := io.Copy(io.Discard, body); err != nil {
		return fmt.Errorf("body read: %w", err)
	}
	rc := http.NewResponseController(w)
	for i := range 6 {
		select {
		case <-r.Context().Done():
			return r.Context().Err()
		case <-time.After(testBodyBound / 2):
		}
		_, _ = fmt.Fprintf(w, "data: %d\n\n", i)
		_ = rc.Flush()
	}
	return r.Context().Err()
}

// TestReadTimeoutSparesLongReplies pins the pre-mortem of the body bound:
// a reply that streams for three bounds after its body was read, one whose
// handler read only part of its body, and a stream that never had a body
// must all keep their request context to the end, because net/http clears
// the read deadline when it starts its background read. The last row is the
// positive control: a WriteTimeout of the same bound does cut the reply,
// which is why strazad sets none and why this test can see a cut.
func TestReadTimeoutSparesLongReplies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		h2         bool
		method     string
		partial    bool
		shape      func(*http.Server)
		wantCancel bool
	}{
		{name: "a streamed reply to a POST on HTTP/1.1", method: http.MethodPost},
		{name: "a streamed reply to a POST on HTTP/2", h2: true, method: http.MethodPost},
		{name: "a reply whose handler read part of its body", method: http.MethodPost, partial: true},
		{name: "an event stream without a body on HTTP/1.1", method: http.MethodGet},
		{name: "an event stream without a body on HTTP/2", h2: true, method: http.MethodGet},
		{name: "a WriteTimeout cuts the reply", method: http.MethodPost, wantCancel: true,
			shape: func(s *http.Server) { s.WriteTimeout = testBodyBound }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ended := make(chan error, 1)
			srv := serveBody(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ended <- streamPastTheBound(w, r, c.partial)
			}), c.h2, c.shape)
			var body io.Reader
			if c.method == http.MethodPost {
				body = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call"}`)
			}
			req, err := http.NewRequestWithContext(context.Background(), c.method, srv.URL+"/mcp", body)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			var handlerErr error
			select {
			case handlerErr = <-ended:
			case <-time.After(20 * testBodyBound):
				t.Fatal("the handler never ended")
			}
			if c.wantCancel {
				if !errors.Is(handlerErr, context.Canceled) {
					t.Fatalf("the reply ended with %v, want its context canceled, or this test cannot see a cut reply", handlerErr)
				}
				return
			}
			if handlerErr != nil || strings.Count(string(got), "data: ") != 6 {
				t.Fatalf("the reply ended with %v after %q, want all six chunks and a live context", handlerErr, got)
			}
		})
	}
}

// TestServersBoundBodyReads pins the wiring on both listeners: the main
// server and the dedicated approver server bound a request's header and
// body read at bodyReadTimeout, keep the 5 second header bound, and set no
// WriteTimeout, which would cut the event streams and long polls.
func TestServersBoundBodyReads(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t, func(cfg *config.Config) {
		cfg.Server.ApproverTLS.AutoMint = ptrBool(true)
		cfg.Server.ApproverTLS.Listen = "127.0.0.1:0"
	})
	for name, srv := range map[string]*http.Server{"main": app.http, "approver": app.approverHTTP} {
		if srv == nil {
			t.Fatalf("the %s server was not built", name)
		}
		if srv.ReadTimeout != bodyReadTimeout || srv.ReadHeaderTimeout != 5*time.Second || srv.WriteTimeout != 0 {
			t.Errorf("the %s server reads with ReadTimeout %s, ReadHeaderTimeout %s and WriteTimeout %s, want %s, 5s and none",
				name, srv.ReadTimeout, srv.ReadHeaderTimeout, srv.WriteTimeout, bodyReadTimeout)
		}
	}
}

// TestExtendBodyReadLetsASlowBulkBodyFinish pins the longer read of the bulk
// routes on HTTP/1.1 and HTTP/2: under a server ReadTimeout of one bound, a
// body that arrives over two bounds is read whole once extendBodyRead gave
// it four, and is cut without it. A stream without a body outlives the
// extension on both protocols: HTTP/1.1 gets no deadline, and on HTTP/2 the
// deadline ends only the request body.
func TestExtendBodyReadLetsASlowBulkBodyFinish(t *testing.T) {
	t.Parallel()
	for _, h2 := range []bool{false, true} {
		for _, extend := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s extended %v", protoOf(h2), extend), func(t *testing.T) {
				t.Parallel()
				type result struct {
					n   int
					err error
				}
				done := make(chan result, 1)
				srv := serveBody(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if extend {
						extendBodyRead(w, r, 4*testBodyBound)
					}
					b, err := io.ReadAll(r.Body)
					done <- result{len(b), err}
				}), h2, nil)
				pr, pw := io.Pipe()
				req, err := http.NewRequest(http.MethodPost, srv.URL+"/mcp", pr)
				if err != nil {
					t.Fatal(err)
				}
				req.ContentLength = 1000
				go func() {
					if resp, err := srv.Client().Do(req); err == nil {
						_ = resp.Body.Close()
					}
				}()
				_, _ = pw.Write(make([]byte, 500))
				time.Sleep(2 * testBodyBound)
				_, _ = pw.Write(make([]byte, 500))
				_ = pw.Close()
				select {
				case got := <-done:
					if extend && (got.err != nil || got.n != 1000) {
						t.Fatalf("the slow body read %d bytes with error %v, want all 1000", got.n, got.err)
					}
					if !extend && !errors.Is(got.err, os.ErrDeadlineExceeded) {
						t.Fatalf("without the extension the slow body read ended with %v, want the deadline error", got.err)
					}
				case <-time.After(20 * testBodyBound):
					t.Fatal("the handler never ended")
				}
			})
		}
	}
	for _, h2 := range []bool{false, true} {
		t.Run("a stream without a body on "+protoOf(h2), func(t *testing.T) {
			t.Parallel()
			ended := make(chan error, 1)
			srv := serveBody(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				extendBodyRead(w, r, 2*testBodyBound)
				ended <- streamPastTheBound(w, r, false)
			}), h2, nil)
			resp, err := srv.Client().Get(srv.URL + "/mcp")
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err := <-ended; err != nil || strings.Count(string(got), "data: ") != 6 {
				t.Fatalf("the stream ended with %v after %q, want all six chunks", err, got)
			}
		})
	}
}

// deadlineRecorder is a response recorder that takes read deadlines, as a
// served connection does, and records each one it is given.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	set []time.Time
}

func (d *deadlineRecorder) SetReadDeadline(t time.Time) error {
	d.set = append(d.set, t)
	return nil
}

// TestBodyCapExtendsNoRoute pins that bodyCap sets no read deadline on any
// route, a self-capped one included and whatever the byte cap, because the
// handler of a self-capped route extends the read itself once the session
// verified.
func TestBodyCapExtendsNoRoute(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, method, path, body string
		limit                    int64
	}{
		{"the gateway", http.MethodPost, "/mcp", `{"jsonrpc":"2.0"}`, 1 << 20},
		{"a server's own gateway endpoint", http.MethodPost, "/mcp/github", `{"jsonrpc":"2.0"}`, 1 << 20},
		{"the audit upload", http.MethodPost, "/v1/audit/batch", `{"events":[]}`, 1 << 20},
		{"the gateway with the byte cap off", http.MethodPost, "/mcp", `{}`, 0},
		{"the gateway stream without a body", http.MethodGet, "/mcp", "", 1 << 20},
		{"a capped route", http.MethodPost, "/v1/checkin", `{}`, 1 << 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := bodyCap(c.limit, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
			}))
			var body io.Reader
			if c.body != "" {
				body = strings.NewReader(c.body)
			}
			w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			h.ServeHTTP(w, httptest.NewRequest(c.method, c.path, body))
			if len(w.set) != 0 {
				t.Fatalf("read deadlines %v, want none past the server's own", w.set)
			}
		})
	}
}

// bulkRoutes are the self-capped routes with a body each accepts from a
// session: the gateway, a server's own gateway endpoint and the audit
// upload.
var bulkRoutes = []struct{ path, body string }{
	{"/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`},
	{"/mcp/github", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`},
	{"/v1/audit/batch", `{"events":[]}`},
}

// TestBulkRoutesExtendTheReadForASessionOnly pins where the bulk read
// window is given, through strazad's whole handler: a POST with a session
// to each bulk route sets one read deadline of bulkBodyReadTimeout, and an
// anonymous POST or one with a rejected token sets none and gets 401, so
// its unread body drains under the server's bodyReadTimeout.
func TestBulkRoutesExtendTheReadForASessionOnly(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	seedIdentity(t, app)
	token := sessionToken(t, base, "kim")
	callers := []struct {
		name, auth string
		session    bool
	}{
		{"with a session", "Bearer " + token, true},
		{"anonymous", "", false},
		{"with a rejected token", "Bearer not-a-session-token", false},
	}
	// The subtests run in turn, right after the token is minted. As parallel
	// subtests they could wait past the token's five minutes in a full race
	// run and read 401.
	for _, rt := range bulkRoutes {
		for _, c := range callers {
			t.Run(rt.path+" "+c.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPost, rt.path, strings.NewReader(rt.body))
				req.Header.Set("Content-Type", "application/json")
				if c.auth != "" {
					req.Header.Set("Authorization", c.auth)
				}
				w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
				before := time.Now()
				app.http.Handler.ServeHTTP(w, req)
				if !c.session {
					if w.Code != http.StatusUnauthorized || len(w.set) != 0 {
						t.Fatalf("answer %d with read deadlines %v, want 401 and none past the server's own", w.Code, w.set)
					}
					return
				}
				if w.Code != http.StatusOK || len(w.set) != 1 {
					t.Fatalf("answer %d with read deadlines %v, want 200 and one", w.Code, w.set)
				}
				if got := w.set[0].Sub(before); got < bulkBodyReadTimeout || got > bulkBodyReadTimeout+5*time.Second {
					t.Fatalf("the body may arrive for %s, want %s", got, bulkBodyReadTimeout)
				}
			})
		}
	}
}

// TestStalledAnonymousBulkBodyIsAnsweredInTheBound sends an anonymous POST
// to each bulk route with all of its body but the last byte, and then
// waits, as a client that pins a connection's buffered body does. Its 401
// must come within a few of the server's read bounds, long before the bulk
// window, on HTTP/1.1 and on HTTP/2, the shape where net/http drains the
// unread body before it writes the 401.
func TestStalledAnonymousBulkBodyIsAnsweredInTheBound(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	for _, rt := range bulkRoutes {
		for _, h2 := range []bool{false, true} {
			t.Run(rt.path+" on "+protoOf(h2), func(t *testing.T) {
				t.Parallel()
				srv := serveBody(t, app.http.Handler, h2, nil)
				pr, pw := io.Pipe()
				t.Cleanup(func() { _ = pw.Close() })
				req, err := http.NewRequest(http.MethodPost, srv.URL+rt.path, pr)
				if err != nil {
					t.Fatal(err)
				}
				req.ContentLength = 1000
				status := make(chan int, 1)
				go func() {
					resp, err := srv.Client().Do(req)
					if err != nil {
						status <- 0
						return
					}
					_ = resp.Body.Close()
					status <- resp.StatusCode
				}()
				if _, err := pw.Write(make([]byte, 999)); err != nil {
					t.Fatal(err)
				}
				select {
				case got := <-status:
					if got != http.StatusUnauthorized {
						t.Fatalf("the stalled anonymous POST got %d, want 401", got)
					}
				case <-time.After(20 * testBodyBound):
					t.Fatalf("the stalled anonymous POST has no answer after %s, want its 401 within %s", 20*testBodyBound, testBodyBound)
				}
			})
		}
	}
}
