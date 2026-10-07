package agentguard

// Gateway-session revival tests. Without revival, an 8h-idle claude-code
// session fails every straza call with "client is closing" until the
// harness restarts. The latch is simulated by closing the proxy's live session:
// the go-sdk client refuses every subsequent send exactly like one whose
// standalone SSE stream exhausted reconnection, same closing state and same
// error spelling, without waiting out the sdk's real retry schedule.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// countDials wraps p.dial to count revival attempts (boot already happened
// in buildProxy, so the counter sees revivals only).
func countDials(p *mcpProxy) *atomic.Int64 {
	var n atomic.Int64
	base := p.dial
	p.dial = func(ctx context.Context) (*mcp.ClientSession, error) {
		n.Add(1)
		return base(ctx)
	}
	return &n
}

func echoCall(ctx context.Context, harness *mcp.ClientSession, text string) (string, error) {
	out, err := harness.CallTool(ctx, &mcp.CallToolParams{
		Name: "everything__echo", Arguments: map[string]any{"text": text}})
	if err != nil {
		return "", err
	}
	return out.Content[0].(*mcp.TextContent).Text, nil
}

func TestMCPProxyRevivesLatchedSession(t *testing.T) {
	g := newFakeGateway(t)
	store := proxyTestStore(t, g.URL)
	ctx, p, harness := buildProxy(t, g, store)
	p.jitterMax = 0 // revive arms a scheduled resync; fire it immediately
	dials := countDials(p)

	dead := p.session()
	_ = dead.Close()

	// Classification pin against the REAL closed client: the pre-send
	// refusal must read as session-dead AND provably never-sent. If an sdk
	// upgrade changes the spelling, this fails before any behavior does.
	_, derr := dead.CallTool(ctx, &mcp.CallToolParams{
		Name: "everything__echo", Arguments: map[string]any{"text": "x"}})
	if derr == nil || !sessionDeadErr(derr) || !neverSentErr(derr) {
		t.Fatalf("closed-client error classification broke: err=%v dead=%v neverSent=%v",
			derr, sessionDeadErr(derr), neverSentErr(derr))
	}

	// The harness call must heal transparently: revive once, replay once.
	txt, err := echoCall(ctx, harness, "revive me")
	if err != nil {
		t.Fatalf("call through latched session must transparently heal, got: %v", err)
	}
	if txt != "gateway: revive me" {
		t.Errorf("healed call result = %q", txt)
	}
	if n := dials.Load(); n != 1 {
		t.Errorf("revival dials = %d, want exactly 1", n)
	}
	if p.session() == dead {
		t.Error("proxy still holds the corpse")
	}
}

func TestMCPProxyRevivalSingleFlight(t *testing.T) {
	g := newFakeGateway(t)
	store := proxyTestStore(t, g.URL)
	ctx, p, harness := buildProxy(t, g, store)
	p.jitterMax = 0 // revive arms a scheduled resync; fire it immediately
	dials := countDials(p)
	_ = p.session().Close()

	const callers = 8
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = echoCall(ctx, harness, "concurrent")
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d failed through revival: %v", i, err)
		}
	}
	if n := dials.Load(); n != 1 {
		t.Errorf("concurrent revival dials = %d, want exactly 1 (single-flight)", n)
	}
}

func TestMCPProxyRevivalFailureBacksOffThenRecovers(t *testing.T) {
	g := newFakeGateway(t)
	store := proxyTestStore(t, g.URL)
	ctx, p, harness := buildProxy(t, g, store)
	p.jitterMax = 0 // revive arms a scheduled resync; fire it immediately
	orig := p.dial
	var attempts atomic.Int64
	p.dial = func(context.Context) (*mcp.ClientSession, error) {
		attempts.Add(1)
		return nil, errors.New("dial refused (test)")
	}
	_ = p.session().Close()

	// First call: revival attempted, fails; the error names both halves.
	_, err := echoCall(ctx, harness, "x")
	if err == nil {
		t.Fatal("call must fail while the gateway is unreachable")
	}
	if !strings.Contains(err.Error(), "Straza:") || !strings.Contains(err.Error(), "reconnect") {
		t.Errorf("failure must carry the prefix and name the reconnect attempt: %v", err)
	}
	if n := attempts.Load(); n != 1 {
		t.Fatalf("revival attempts = %d, want 1", n)
	}

	// Immediate second call: the backoff gate refuses fast, no dial storm.
	_, err = echoCall(ctx, harness, "y")
	if err == nil || !strings.Contains(err.Error(), "backing off") {
		t.Errorf("second call must hit the backoff gate: %v", err)
	}
	if n := attempts.Load(); n != 1 {
		t.Errorf("backoff window dialed anyway: attempts = %d", n)
	}

	// Gateway back + gate cleared: the next call heals. No latch is ever
	// permanent.
	p.dial = orig
	p.csMu.Lock()
	p.redialNext = time.Time{}
	p.csMu.Unlock()
	txt, err := echoCall(ctx, harness, "healed")
	if err != nil || txt != "gateway: healed" {
		t.Fatalf("post-recovery call = %q, %v", txt, err)
	}
}

// newKillerGateway is a real sdk-served gateway whose mux kills the TCP
// connection on every tools/call for "everything__boom" AFTER receiving the
// request. That is the indeterminate class: sent, possibly executed, never
// answered.
func newKillerGateway(t *testing.T) (*fakeGateway, *atomic.Int64) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "gateway", Version: "1.0.0"}, nil)
	type echoArgs struct {
		Text string `json:"text"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "everything__echo", Description: "echo"},
		func(_ context.Context, _ *mcp.CallToolRequest, a echoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "gateway: " + a.Text}}}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "everything__boom", Description: "dies mid-flight"},
		func(_ context.Context, _ *mcp.CallToolRequest, a echoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "boom ran"}}}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	var boomReceived atomic.Int64
	g := &fakeGateway{token: "ses-token-1", mcpSrv: srv}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", g.handleCheckin)
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		want := "Bearer " + g.token
		g.mu.Unlock()
		if r.Header.Get("Authorization") != want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			var req struct {
				Method string `json:"method"`
				Params struct {
					Name string `json:"name"`
				} `json:"params"`
			}
			if json.Unmarshal(body, &req) == nil && req.Method == "tools/call" &&
				req.Params.Name == "everything__boom" {
				boomReceived.Add(1)
				hj, ok := w.(http.Hijacker)
				if !ok {
					t.Error("test server must support hijack")
					return
				}
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close() // request received, connection torn down, no answer
				}
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		handler.ServeHTTP(w, r)
	})
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g, &boomReceived
}

func TestMCPProxyMidflightErrorNeverReplays(t *testing.T) {
	g, boomReceived := newKillerGateway(t)
	store := proxyTestStore(t, g.URL)
	ctx, p, harness := buildProxy(t, g, store)
	dials := countDials(p)

	// The killed call errors and is NOT replayed: the gateway may have
	// executed it, and plumbing never replays a possibly-executed governed
	// call. This is the safety pin of revival.
	_, err := harness.CallTool(ctx, &mcp.CallToolParams{
		Name: "everything__boom", Arguments: map[string]any{"text": "x"}})
	if err == nil {
		t.Fatal("killed call must surface an error")
	}
	if n := boomReceived.Load(); n != 1 {
		t.Fatalf("gateway received the killed call %d times, want exactly 1 (no replay)", n)
	}
	if n := dials.Load(); n != 0 {
		t.Errorf("indeterminate error must not revive (dials = %d): the session is not proven dead", n)
	}

	// One failed POST does not kill the client: the next call rides the
	// SAME session.
	txt, err := echoCall(ctx, harness, "still alive")
	if err != nil || txt != "gateway: still alive" {
		t.Fatalf("session must survive a mid-flight failure: %q, %v", txt, err)
	}
	if n := dials.Load(); n != 0 {
		t.Errorf("healthy session was revived anyway (dials = %d)", n)
	}
}

func TestMCPProxyResyncRevivesAndCatchesDrift(t *testing.T) {
	g := newFakeGateway(t)
	store := proxyTestStore(t, g.URL)
	ctx, p, _ := buildProxy(t, g, store)
	p.jitterMax = 0 // revive arms a scheduled resync; fire it immediately
	dials := countDials(p)

	// Session dies; the catalog drifts while the lane is dark (the
	// list_changed notification has no live stream to ride).
	_ = p.session().Close()
	type lateArgs struct {
		Text string `json:"text"`
	}
	mcp.AddTool(g.mcpSrv, &mcp.Tool{Name: "everything__late", Description: "added during the outage"},
		func(_ context.Context, _ *mcp.CallToolRequest, a lateArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: a.Text}}}, nil, nil
		})

	// The periodic backstop's pass revives the lane and sees the drift. This
	// is what turns "idle sessions brick on deploy" into "self-heal within
	// one backstop interval".
	p.resync(ctx)
	if n := dials.Load(); n != 1 {
		t.Errorf("resync revival dials = %d, want 1", n)
	}
	p.mu.Lock()
	_, sawLate := p.registered["everything__late"]
	p.mu.Unlock()
	if !sawLate {
		t.Error("post-revival resync missed the catalog drift")
	}
}

func TestMCPProxyDialBoundedBlackHole(t *testing.T) {
	// A black-holed gateway accepts the connection and answers nothing. The
	// handshake must fail at the bound, not hang holding the lock.
	g := &fakeGateway{token: "ses-token-1"}
	released := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", g.handleCheckin)
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // drain, else conn-close is invisible
		select {
		case <-r.Context().Done():
		case <-released:
		}
	})
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	// LIFO: release the hung handler BEFORE Close waits on it. The abandoned
	// dial's POST is not bound to the test context.
	t.Cleanup(func() { close(released) })

	store := proxyTestStore(t, g.URL)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := NewClient(g.URL)
	if _, err := ensureSession(ctx, store, client, "claude-code"); err != nil {
		t.Fatalf("ensureSession: %v", err)
	}
	p := newMCPProxy(mcp.NewServer(&mcp.Implementation{Name: "straza", Version: "test"}, nil))
	p.ctx = ctx
	p.redialTimeout = 100 * time.Millisecond
	p.dial = newGatewayDialer(p, store, client, "claude-code", gatewayEndpoint(g.URL, ""))

	start := time.Now()
	_, err := p.dialBounded(ctx)
	if err == nil || !strings.Contains(err.Error(), "handshake exceeded") {
		t.Fatalf("black-hole dial must fail at the bound, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("bounded dial took %s", elapsed)
	}
}
