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

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
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
	store := proxyTestStore(t, g)
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
	store := proxyTestStore(t, g)
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
	store := proxyTestStore(t, g)
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
	store := proxyTestStore(t, g)
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

// newAnsweringGateway is a real sdk-served gateway that counts every
// tools/call for "everything__write" it receives. When code is not 0, it
// answers that call with the JSON-RPC error code and message instead of
// running it. The call has then reached the gateway, so it may have run.
func newAnsweringGateway(t *testing.T, code int, message string) (*fakeGateway, *atomic.Int64) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "gateway", Version: "1.0.0"}, nil)
	type writeArgs struct {
		Text string `json:"text"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "everything__write", Description: "a write that is not idempotent"},
		func(_ context.Context, _ *mcp.CallToolRequest, a writeArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "wrote " + a.Text}}}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	var received atomic.Int64
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
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					Name string `json:"name"`
				} `json:"params"`
			}
			if json.Unmarshal(body, &req) == nil && req.Method == "tools/call" &&
				req.Params.Name == "everything__write" {
				received.Add(1)
				if code != 0 {
					writeRPC(w, req.ID, map[string]any{"error": map[string]any{"code": code, "message": message}})
					return
				}
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		handler.ServeHTTP(w, r)
	})
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g, &received
}

// TestMCPProxyReconnectAnswer pins what the agent reads when the gateway
// session dies under a call, and that no call that may have run is sent
// twice. A call that may have reached the gateway is never sent again, and
// the agent reads that its outcome is unknown. Only a call that provably
// never left the bridge is sent once more. An error the gateway answered
// passes through without a reconnect, whatever its text says.
func TestMCPProxyReconnectAnswer(t *testing.T) {
	const (
		lost    = "The gateway session was lost while this call was in flight. "
		unknown = "The call may or may not have run. " +
			"Check whether its effect happened, or ask the user, before you send the call again"
		notRun = "The call did not run, because the gateway could not be reached. " +
			"Send it again once the gateway answers, and run `straza doctor` if it stays out of reach"
		// closing is the answer the sdk's server gives a call that arrives
		// while it shuts down. The bridge's client reads it as a dead session.
		closing = "server is closing"
		// relayed is strazad's answer when its own client to the upstream
		// server is latched closed. It passes the upstream failure through
		// word for word, so the text names the sdk's latched spelling.
		relayed = `upstream call failed: connection closed: calling "tools/call": client is closing`
	)
	cases := []struct {
		name string
		// code and message make the gateway answer the call with that
		// JSON-RPC error. A zero code runs the call.
		code    int
		message string
		// latch closes the live session first, so the call never leaves.
		latch bool
		// dialFails refuses the reconnect dial, and backoff arms the gate
		// that refuses a reconnect after a failed one without dialing.
		dialFails, backoff bool
		// wantEnd is how the agent's error text ends, empty when the call
		// succeeds, and wantCode is the error's JSON-RPC code.
		wantEnd  string
		wantCode int64
		// wantReceived is how often the gateway received the call, and
		// wantDials how often the bridge dialed a new session.
		wantReceived, wantDials int64
	}{
		{name: "call in flight when the session dies is not sent again",
			code: -32004, message: closing,
			wantEnd:      ". " + lost + "Straza reconnected the session but did not send the call again. " + unknown,
			wantReceived: 1, wantDials: 1},
		{name: "call that never left the bridge is sent once on the new session",
			latch: true, wantReceived: 1, wantDials: 1},
		{name: "call in flight with a failed reconnect has an unknown outcome",
			code: -32004, message: closing, dialFails: true,
			wantEnd:      "; gateway reconnect also failed: dial refused (test). " + lost + unknown,
			wantReceived: 1, wantDials: 1},
		{name: "call in flight while the reconnect backs off has an unknown outcome",
			code: -32004, message: closing, backoff: true,
			wantEnd:      "; gateway reconnect also failed: gateway reconnect backing off after 1 failed attempts. " + lost + unknown,
			wantReceived: 1, wantDials: 0},
		{name: "call that never left the bridge with a failed reconnect did not run",
			latch: true, dialFails: true,
			wantEnd:      "; gateway reconnect also failed: dial refused (test). " + notRun,
			wantReceived: 0, wantDials: 1},
		{name: "gateway answer that names a closing client passes through",
			code: -32000, message: relayed,
			wantEnd: relayed, wantCode: -32000, wantReceived: 1, wantDials: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, received := newAnsweringGateway(t, tc.code, tc.message)
			store := proxyTestStore(t, g)
			ctx, p, harness := buildProxy(t, g, store)
			p.jitterMax = 0 // a revive arms a resync, and this fires it at once
			if tc.dialFails {
				p.dial = func(context.Context) (*mcp.ClientSession, error) {
					return nil, errors.New("dial refused (test)")
				}
			}
			if tc.backoff {
				p.csMu.Lock()
				p.redialFails, p.redialNext = 1, time.Now().Add(time.Hour)
				p.csMu.Unlock()
			}
			dials := countDials(p)
			if tc.latch {
				_ = p.session().Close()
			}

			out, err := harness.CallTool(ctx, &mcp.CallToolParams{
				Name: "everything__write", Arguments: map[string]any{"text": "row"}})
			if n := received.Load(); n != tc.wantReceived {
				t.Errorf("gateway received the call %d times, want %d", n, tc.wantReceived)
			}
			if n := dials.Load(); n != tc.wantDials {
				t.Errorf("reconnect dials = %d, want %d", n, tc.wantDials)
			}
			if tc.wantEnd == "" {
				if err != nil {
					t.Fatalf("call must succeed, got: %v", err)
				}
				if txt := out.Content[0].(*mcp.TextContent).Text; txt != "wrote row" {
					t.Errorf("result = %q, want the gateway's answer", txt)
				}
				return
			}
			// The bridge answers with a JSON-RPC error. Its own text starts
			// "Straza: gateway call failed:" and its code is 0. The
			// gateway's own answer passes as the gateway wrote it, code
			// and all.
			var rpcErr *jsonrpc.Error
			if !errors.As(err, &rpcErr) {
				t.Fatalf("the bridge must answer with a JSON-RPC error, got: %v", err)
			}
			msg := rpcErr.Message
			relayed := tc.wantCode != 0 && msg == tc.wantEnd
			bridged := tc.wantCode == 0 && strings.HasPrefix(msg, "Straza: gateway call failed: ") && strings.HasSuffix(msg, tc.wantEnd)
			if rpcErr.Code != tc.wantCode || !relayed && !bridged {
				t.Errorf("error code %d, text:\n%s\nwant code %d and the text, or the Straza prefix and the ending:\n%s",
					rpcErr.Code, msg, tc.wantCode, tc.wantEnd)
			}
			if strings.Contains(msg, "retry the call") {
				t.Errorf("the agent must not be told to retry a call that may have run: %s", msg)
			}
			if (tc.dialFails || tc.backoff) && strings.Contains(msg, "reconnected") {
				t.Errorf("a failed reconnect must not be called a reconnect: %s", msg)
			}
		})
	}
}

func TestMCPProxyResyncRevivesAndCatchesDrift(t *testing.T) {
	g := newFakeGateway(t)
	store := proxyTestStore(t, g)
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
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(released) }) })

	proxyTestStore(t, g)
	ctx, cancel := context.WithCancel(context.Background())
	// The abandoned handshake keeps running after dialBounded returns, so
	// the fake service's home goes only after that dial has ended.
	dialDone := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		release.Do(func() { close(released) })
		select {
		case <-dialDone:
		case <-time.After(5 * time.Second):
		}
	})
	p := newMCPProxy(mcp.NewServer(&mcp.Implementation{Name: "straza", Version: "test"}, nil))
	p.ctx = ctx
	p.redialTimeout = 100 * time.Millisecond
	dial := newGatewayDialer(p, serviceHTTPClient(NewServiceClient(), "claude-code"), gatewayEndpoint(""))
	var once sync.Once
	p.dial = func(c context.Context) (*mcp.ClientSession, error) {
		defer once.Do(func() { close(dialDone) })
		return dial(c)
	}

	start := time.Now()
	_, err := p.dialBounded(ctx)
	if err == nil || !strings.Contains(err.Error(), "handshake exceeded") {
		t.Fatalf("black-hole dial must fail at the bound, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("bounded dial took %s", elapsed)
	}
}
