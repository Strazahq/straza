package agentguard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/version"
)

// fakeGateway is an authed in-process /mcp plus the /v1 endpoints the proxy
// needs: it accepts exactly one bearer token at a time and can rotate it,
// 401ing the old one (the shape of a real session-token rotation).
type fakeGateway struct {
	*httptest.Server
	mcpSrv    *mcp.Server // the backing catalog; tests mutate it to exercise resync
	mu        sync.Mutex
	token     string
	checkins  atomic.Int64
	refreshes atomic.Int64
	calls     atomic.Int64

	// Pagination knobs, exercised only by newPagedGateway. When pageSize > 0 the
	// /mcp handler answers tools/list itself (slicing pagedTools into
	// cursor-linked pages) instead of forwarding to the SDK server, which gives
	// a test exact control over page boundaries, mid-listing errors, and the
	// tool set. pagedTools is guarded by mu so a test can grow it mid-flight.
	pageSize   int
	pagedTools []string
	failCursor atomic.Bool  // arm to answer the next cursored page with -32602
	listCalls  atomic.Int64 // tools/list requests served (coalescing/retry pins)
}

// handleCheckin answers /v1/checkin, minting a fresh session token each time and
// counting device checkins vs. token refreshes. Shared by every gateway shape.
func (g *fakeGateway) handleCheckin(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body["device_token"] != nil {
		g.checkins.Add(1)
	} else {
		g.refreshes.Add(1)
	}
	g.mu.Lock()
	g.token = "ses-token-" + time.Now().Format("150405.000000")
	tok := g.token
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"session": "s-mcp", "session_token": tok, "expires_in": 300,
		"user": "bob", "roles": []string{"dev"}, "snapshot": "snap-1", "attestation": "advisory",
	})
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "gateway", Version: "1.0.0"}, nil)
	type echoArgs struct {
		Text string `json:"text"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "everything__echo", Description: "echo through the gateway"},
		func(_ context.Context, _ *mcp.CallToolRequest, a echoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "gateway: " + a.Text}}}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

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
		n := g.calls.Add(1)
		// Every authorized answer names its correlation id the way strazad
		// does (X-Request-Id), so the proxy's journal can be pinned
		// on the exact answer a call rode: "gw-<n>" for the n-th call served.
		w.Header().Set("X-Request-Id", "gw-"+strconv.FormatInt(n, 10))
		handler.ServeHTTP(w, r)
	})
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g
}

// rotate invalidates the current token; the next gateway request 401s until
// the proxy refreshes.
func (g *fakeGateway) rotate() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.token = "rotated-" + g.token
}

// newPagedGateway builds a gateway whose /mcp handler paginates tools/list over
// the given tool names at the given page size. The SDK streamable handler still
// serves the initialize handshake and the standing SSE stream; only tools/list
// is intercepted, which lets a test control page boundaries, cursors, and
// mid-listing errors exactly. The SDK server's own pagination would hand out
// only valid cursors, never the stale one the retry path needs.
func newPagedGateway(t *testing.T, pageSize int, tools ...string) *fakeGateway {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "gateway", Version: "1.0.0"}, nil)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	g := &fakeGateway{token: "ses-token-1", mcpSrv: srv, pageSize: pageSize, pagedTools: tools}
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
		g.calls.Add(1)
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			var req rpcRequest
			if json.Unmarshal(body, &req) == nil && req.Method == "tools/list" {
				g.serveToolsPage(w, req)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body)) // restore for the SDK handler
		}
		handler.ServeHTTP(w, r)
	})
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g
}

// addTool appends a tool to the paged catalog and emits NO notification. The
// whole point of the backstop test is a change the proxy is never told about.
func (g *fakeGateway) addTool(name string) {
	g.mu.Lock()
	g.pagedTools = append(g.pagedTools, name)
	g.mu.Unlock()
}

// rpcRequest is the sliver of a JSON-RPC request the paged handler inspects.
type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Cursor string `json:"cursor"`
	} `json:"params"`
}

// serveToolsPage answers one tools/list page. The cursor is simply the decimal
// offset into pagedTools (empty = start); nextCursor is present iff tools
// remain past this page.
func (g *fakeGateway) serveToolsPage(w http.ResponseWriter, req rpcRequest) {
	g.listCalls.Add(1)
	offset := 0
	if req.Params.Cursor != "" {
		// A catalog change between pages makes the offset the cursor encoded
		// meaningless; the real gateway answers -32602, and so do we when armed.
		if g.failCursor.CompareAndSwap(true, false) {
			writeRPCError(w, req.ID)
			return
		}
		n, err := strconv.Atoi(req.Params.Cursor)
		if err != nil {
			writeRPCError(w, req.ID)
			return
		}
		offset = n
	}
	g.mu.Lock()
	names := append([]string(nil), g.pagedTools...)
	g.mu.Unlock()
	if offset > len(names) {
		offset = len(names)
	}
	end := min(offset+g.pageSize, len(names))
	tools := make([]map[string]any, 0, end-offset)
	for _, n := range names[offset:end] {
		tools = append(tools, map[string]any{
			"name": n, "description": "", "inputSchema": map[string]any{"type": "object"},
		})
	}
	result := map[string]any{"tools": tools}
	if end < len(names) {
		result["nextCursor"] = strconv.Itoa(end)
	}
	writeRPC(w, req.ID, map[string]any{"result": result})
}

// writeRPCError answers with the cursor contract's -32602 (JSON-RPC "invalid
// params") and the exact stale-cursor message.
func writeRPCError(w http.ResponseWriter, id json.RawMessage) {
	writeRPC(w, id, map[string]any{"error": map[string]any{
		"code":    -32602,
		"message": "invalid or stale cursor: re-issue tools/list from the start",
	}})
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, fields map[string]any) {
	env := map[string]any{"jsonrpc": "2.0"}
	if len(id) > 0 {
		env["id"] = id
	}
	for k, v := range fields {
		env[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(env)
}

func proxyTestStore(t *testing.T, serverURL string) *Store {
	t.Helper()
	t.Setenv("STRAZA_HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(Config{ServerURL: serverURL}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveIdentity(Identity{DeviceToken: "dev-token", DeviceID: "d1", Username: "bob"}); err != nil {
		t.Fatal(err)
	}
	return store
}

// buildProxy assembles the proxy against the fake gateway and connects an
// in-memory MCP client to its server half (what the harness would be). It
// returns the request context and the proxy itself so a test can drive resync
// directly after mutating the gateway catalog; startProxy wraps it for tests
// that only need the harness.
func buildProxy(t *testing.T, g *fakeGateway, store *Store) (context.Context, *mcpProxy, *mcp.ClientSession) {
	t.Helper()
	return buildProxyFor(t, g, store, "")
}

// buildProxyFor is buildProxy for the one-server bridge when server is set:
// the stdio server takes the bridge's options and the dialer the per-server
// endpoint, exactly as runMCPProxy wires them.
func buildProxyFor(t *testing.T, g *fakeGateway, store *Store, server string) (context.Context, *mcpProxy, *mcp.ClientSession) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := NewClient(g.URL)
	if _, err := ensureSession(ctx, store, client, "claude-code"); err != nil {
		t.Fatalf("ensureSession: %v", err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "straza", Version: version.Version}, proxyServerOptions(server))
	p := newMCPProxy(srv)
	p.ctx = ctx // scheduled/periodic resyncs the new tests drive need a context
	p.server = server
	// Production wiring: boot and revival share the dialer (redial tests
	// wrap p.dial to count attempts).
	p.dial = newGatewayDialer(p, store, client, "claude-code", gatewayEndpoint(g.URL, server))
	cs, err := p.dial(ctx)
	if err != nil {
		t.Fatalf("connect gateway: %v", err)
	}
	t.Cleanup(func() { _ = p.session().Close() })
	p.cs = cs
	p.resync(ctx)

	ct, st := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, st) }()
	harness, err := mcp.NewClient(&mcp.Implementation{Name: "harness", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("connect harness side: %v", err)
	}
	t.Cleanup(func() { _ = harness.Close() })
	// Registered last = runs first: wait out scheduled resync timers while
	// the gateway and store are still alive, so no stray goroutine races
	// the TempDir teardown (the store lock's create/remove window).
	t.Cleanup(p.stopResyncs)
	return ctx, p, harness
}

// startProxy assembles the proxy against the fake gateway and connects an
// in-memory MCP client to its server half (what the harness would be).
func startProxy(t *testing.T, g *fakeGateway, store *Store) *mcp.ClientSession {
	t.Helper()
	_, _, harness := buildProxy(t, g, store)
	return harness
}

func TestMCPProxyEndToEnd(t *testing.T) {
	g := newFakeGateway(t)
	store := proxyTestStore(t, g.URL)
	harness := startProxy(t, g, store)
	ctx := context.Background()

	// The gateway's catalog appears through the proxy, schema passthrough.
	tools, err := harness.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "everything__echo" {
		t.Fatalf("tools = %+v, %v", tools, err)
	}

	// A call forwards and returns the gateway's result.
	out, err := harness.CallTool(ctx, &mcp.CallToolParams{
		Name: "everything__echo", Arguments: map[string]any{"text": "hi"}})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if txt := out.Content[0].(*mcp.TextContent).Text; txt != "gateway: hi" {
		t.Errorf("result = %q", txt)
	}

	// Token rotation mid-session: the gateway 401s the old token; the proxy
	// must refresh once and the SAME logical call succeed.
	g.rotate()
	out, err = harness.CallTool(ctx, &mcp.CallToolParams{
		Name: "everything__echo", Arguments: map[string]any{"text": "after-rotate"}})
	if err != nil {
		t.Fatalf("post-rotation call: %v", err)
	}
	if txt := out.Content[0].(*mcp.TextContent).Text; txt != "gateway: after-rotate" {
		t.Errorf("post-rotation result = %q", txt)
	}
	if g.refreshes.Load()+g.checkins.Load() < 2 {
		t.Errorf("expected a refresh after rotation (checkins=%d refreshes=%d)",
			g.checkins.Load(), g.refreshes.Load())
	}
}

func TestEnsureSessionAdoptsAndLocks(t *testing.T) {
	g := newFakeGateway(t)
	store := proxyTestStore(t, g.URL)
	ctx := context.Background()
	client := NewClient(g.URL)

	// A fresh session on disk is ADOPTED, not re-minted.
	if err := store.SaveSession(Session{
		SessionID: "existing", SessionToken: "tok-existing", Harness: "claude-code/2.1",
		User: "bob", Roles: []string{"dev"},
		IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	ses, err := ensureSession(ctx, store, client, "claude-code")
	if err != nil || ses.SessionID != "existing" {
		t.Fatalf("adopt = %+v, %v", ses, err)
	}
	if g.checkins.Load() != 0 {
		t.Errorf("adoption must not checkin (got %d)", g.checkins.Load())
	}

	// Concurrent ensureSession calls with NO session spend few checkins
	// (the lock serializes; late arrivals adopt the winner's session).
	if err := os.Remove(store.statePath("session.json")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ensureSession(ctx, store, client, "claude-code"); err != nil {
				t.Errorf("ensureSession: %v", err)
			}
		}()
	}
	wg.Wait()
	if n := g.checkins.Load(); n > 2 {
		t.Errorf("8 concurrent ensureSession = %d checkins, want <=2 (lock + adopt)", n)
	}
}

func TestMCPProxyFailClosedMessage(t *testing.T) {
	g := newFakeGateway(t)
	store := proxyTestStore(t, g.URL)
	harness := startProxy(t, g, store)
	// Gateway gone mid-session: sever the listener AND live connections,
	// because a graceful Close would block on the proxy's standalone SSE stream.
	_ = g.Listener.Close()
	g.CloseClientConnections()

	_, err := harness.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "everything__echo", Arguments: map[string]any{"text": "x"}})
	if err == nil {
		t.Fatal("call must fail with the gateway down")
	}
	if !strings.Contains(err.Error(), "Straza:") {
		t.Errorf("failure must carry the actionable Straza: prefix, got: %v", err)
	}
}

// harnessSchemaHas reports whether the harness's view of tool `name` carries a
// property named `prop` anywhere in its input schema. It re-lists on every call
// so it observes the CURRENT mirrored contract, not a cached one.
func harnessSchemaHas(t *testing.T, ctx context.Context, harness *mcp.ClientSession, name, prop string) bool {
	t.Helper()
	tools, err := harness.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	for _, tl := range tools.Tools {
		if tl.Name != name {
			continue
		}
		b, err := json.Marshal(tl.InputSchema)
		if err != nil {
			t.Fatalf("marshal schema: %v", err)
		}
		return strings.Contains(string(b), prop)
	}
	t.Fatalf("tool %q not found in harness catalog", name)
	return false
}

// TestMCPProxyResyncPropagatesSchemaChange: a tool whose schema changes
// UNDER A STABLE NAME must reach the harness on the next resync. Here the
// gateway starts requiring _straza_justification (as a mode:approve policy
// would inject); a name-only skip would strand the model on the stale
// contract, so it would never supply the justification the approver reads.
func TestMCPProxyResyncPropagatesSchemaChange(t *testing.T) {
	g := newFakeGateway(t)
	store := proxyTestStore(t, g.URL)
	ctx, p, harness := buildProxy(t, g, store)

	// Before: the mirrored echo has no justification field.
	if harnessSchemaHas(t, ctx, harness, "everything__echo", "_straza_justification") {
		t.Fatal("precondition: echo must not yet carry _straza_justification")
	}
	before := p.registered["everything__echo"]
	if before == "" {
		t.Fatal("echo should be registered after the first resync")
	}

	// The gateway revises echo IN PLACE: same name, gains a required field.
	type approvedEchoArgs struct {
		Text                string `json:"text"`
		StrazaJustification string `json:"_straza_justification" jsonschema:"why the human approver should allow this"`
	}
	mcp.AddTool(g.mcpSrv, &mcp.Tool{Name: "everything__echo", Description: "echo through the gateway"},
		func(_ context.Context, _ *mcp.CallToolRequest, a approvedEchoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "gateway: " + a.Text}}}, nil, nil
		})

	// A second resync must carry the NEW schema through to the harness.
	p.resync(ctx)
	if !harnessSchemaHas(t, ctx, harness, "everything__echo", "_straza_justification") {
		t.Error("resync did not propagate the changed schema to the harness (T18)")
	}
	if after := p.registered["everything__echo"]; after == before {
		t.Errorf("digest unchanged across a real schema change (%q)", after)
	}
}

// TestMCPProxyResyncSkipsUnchanged asserts a resync over an unchanged catalog
// does not churn: each tool's stored digest is byte-stable, which is exactly
// the state the skip branch keys on (p.registered[name] == digest). The go-sdk
// Server exposes no per-tool AddTool counter, so digest stability is the
// observable proxy for "not re-added"; the sibling test above covers the
// opposite branch (a changed digest DOES re-add).
func TestMCPProxyResyncSkipsUnchanged(t *testing.T) {
	g := newFakeGateway(t)
	store := proxyTestStore(t, g.URL)
	ctx, p, _ := buildProxy(t, g, store)

	first := p.registered["everything__echo"]
	if first == "" {
		t.Fatal("echo should be registered after the first resync")
	}
	// Re-list the identical catalog several times: the digest must not move,
	// so every pass takes the skip branch.
	for i := 0; i < 3; i++ {
		p.resync(ctx)
		if got := p.registered["everything__echo"]; got != first {
			t.Fatalf("resync %d changed a stable tool's digest: %q -> %q", i, first, got)
		}
	}
	if len(p.registered) != 1 {
		t.Errorf("registered = %v, want exactly the one gateway tool", p.registered)
	}
}

// TestMCPProxyResyncFollowsCursorPages pins cursor following: once the
// gateway paginates, a single ListTools would mirror only page 1 and strand
// every later tool. The proxy must follow nextCursor to the end.
func TestMCPProxyResyncFollowsCursorPages(t *testing.T) {
	names := []string{"a__one", "a__two", "a__three", "a__four", "a__five"}
	g := newPagedGateway(t, 2, names...) // 5 tools at page size 2 => 3 pages
	store := proxyTestStore(t, g.URL)
	_, p, _ := buildProxy(t, g, store) // buildProxy performs the initial resync

	if len(p.registered) != len(names) {
		t.Fatalf("registered %d tools, want all %d across pages (saw only page 1?)",
			len(p.registered), len(names))
	}
	for _, n := range names {
		if _, ok := p.registered[n]; !ok {
			t.Errorf("tool %q from a later page never reached the mirror", n)
		}
	}
	if got := g.listCalls.Load(); got < 3 {
		t.Errorf("tools/list calls = %d, want >=3 to walk 3 pages", got)
	}
}

// TestMCPProxyResyncRetriesStaleCursorMidListing exercises the bounded retry: a
// catalog that changes between our page fetches invalidates the cursor and the
// gateway answers -32602 mid-walk. A single pass would abandon the listing and
// leave the mirror on the OLD catalog; the retry must re-walk from the start and
// land the full new set.
func TestMCPProxyResyncRetriesStaleCursorMidListing(t *testing.T) {
	g := newPagedGateway(t, 1, "a__one", "a__two") // start: 2 tools, 1 per page
	store := proxyTestStore(t, g.URL)
	_, p, _ := buildProxy(t, g, store) // clean initial resync
	p.retryPause = 0                   // no real sleep between attempts

	// Grow the catalog AND arm a stale cursor on the next cursored page, the
	// exact mid-listing race the retry exists for.
	g.mu.Lock()
	g.pagedTools = []string{"a__one", "a__two", "a__three", "a__four"}
	g.mu.Unlock()
	before := g.listCalls.Load()
	g.failCursor.Store(true)

	p.resync(context.Background())

	want := []string{"a__one", "a__two", "a__three", "a__four"}
	if len(p.registered) != len(want) {
		t.Fatalf("registered %d tools after retry, want %d", len(p.registered), len(want))
	}
	for _, n := range want {
		if _, ok := p.registered[n]; !ok {
			t.Errorf("tool %q missing: the retry did not rebuild the full catalog", n)
		}
	}
	if g.failCursor.Load() {
		t.Error("stale-cursor injection never fired; the retry path was not exercised")
	}
	// A clean 4-page walk is 4 calls; the aborted first attempt adds more.
	if delta := g.listCalls.Load() - before; delta <= 4 {
		t.Errorf("tools/list calls after the failure = %d, want >4 (a retry occurred)", delta)
	}
}

// TestMCPProxyCoalescesRapidNotifications pins the jittered/coalesced handler: a
// single logical change fans out several list_changed (the go-sdk server
// re-notifies per tool edit, and a broadcast hits the whole fleet), and they
// must fold into exactly one resync rather than one walk apiece.
func TestMCPProxyCoalescesRapidNotifications(t *testing.T) {
	g := newPagedGateway(t, 2, "a__one", "a__two", "a__three") // 3 tools => 2 pages
	store := proxyTestStore(t, g.URL)
	_, p, _ := buildProxy(t, g, store)
	p.jitterMax = 50 * time.Millisecond

	before := g.listCalls.Load()
	for i := 0; i < 3; i++ {
		p.scheduleResync()
	}
	// Wait for the single scheduled walk, then settle to catch any stray second.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && g.listCalls.Load() == before {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(250 * time.Millisecond)

	const pages = 2 // 3 tools at page size 2
	if delta := g.listCalls.Load() - before; delta != pages {
		t.Errorf("3 notifications caused %d tools/list calls, want one walk (%d)", delta, pages)
	}
}

// TestMCPProxyPeriodicResyncCatchesSilentChange pins the backstop: the SSE hub
// drops on slow consumers and offers no resumability, so a proxy can miss a
// list_changed outright. The periodic pass must still converge. addTool emits
// no notification (and the paged gateway never broadcasts), so ONLY the
// backstop can find the added tool.
func TestMCPProxyPeriodicResyncCatchesSilentChange(t *testing.T) {
	g := newPagedGateway(t, 10, "a__one") // one page, room to grow
	store := proxyTestStore(t, g.URL)
	ctx, p, _ := buildProxy(t, g, store)
	p.periodicEvery = 5 * time.Millisecond
	p.periodicJitter = 0

	// Own the loop's lifetime: stop it and WAIT before teardown, else a
	// mid-flight resync races the TempDir removal (store lock window).
	pctx, pcancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); p.runPeriodicResync(pctx) }()
	t.Cleanup(func() { pcancel(); <-done })

	g.addTool("a__two") // no notification accompanies this

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		_, ok := p.registered["a__two"]
		p.mu.Unlock()
		if ok {
			return // the backstop converged
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("periodic resync never picked up the silently-added tool")
}
