package agentguard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// The one-server bridge (`straza mcp <server>`) against a fake per-server
// endpoint: tools under their own names with _meta, views mirrored as
// resources, reads through the live gateway session, and the exits for a
// server the gateway refuses.

const (
	viewMIME  = "text/html;profile=mcp-app"
	panelURI  = "ui://demo/panel"
	panelHTML = "<!doctype html><title>panel</title>"
	// unknownServerMsg is the gateway's catalog refusal on /mcp/{server}.
	unknownServerMsg = "Straza: no MCP server named %q is in your catalog. Check the name, or ask an administrator for access to it."
)

// viewsGateway is a fake gateway serving one server on /mcp/{server}. A go-sdk
// server stands in for that server's tools and views. Any other server name,
// and every request while refuse is set, answers the catalog refusal as
// JSON-RPC error -32602, as the gateway's refuseServer does.
type viewsGateway struct {
	*fakeGateway
	inits  atomic.Int64 // initialize requests on any /mcp/{server}
	refuse atomic.Bool  // the server left the caller's catalog mid-session
}

// rpcIntercept answers one JSON-RPC method by hand. It reports false to hand
// the request on to the go-sdk server unanswered.
type rpcIntercept func(http.ResponseWriter, rpcRequest) bool

func newViewsGateway(t *testing.T, server string, srv *mcp.Server, intercept map[string]rpcIntercept) *viewsGateway {
	t.Helper()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	g := &viewsGateway{fakeGateway: &fakeGateway{token: "ses-token-1", mcpSrv: srv}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", g.handleCheckin)
	mux.HandleFunc("/mcp/{server}", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		want := "Bearer " + g.token
		g.mu.Unlock()
		if r.Header.Get("Authorization") != want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		g.calls.Add(1)
		name := r.PathValue("server")
		if r.Method != http.MethodPost {
			if name != server {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			handler.ServeHTTP(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var req rpcRequest
		_ = json.Unmarshal(body, &req)
		if req.Method == "initialize" {
			g.inits.Add(1)
		}
		if name != server || g.refuse.Load() {
			writeRPC(w, req.ID, map[string]any{"error": map[string]any{
				"code": -32602, "message": fmt.Sprintf(unknownServerMsg, name)}})
			return
		}
		if h := intercept[req.Method]; h != nil && h(w, req) {
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		handler.ServeHTTP(w, r)
	})
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g
}

// heldSlot is the decision slot a held result carries on a per-server endpoint.
var heldSlot = map[string]any{
	"v": 1, "decision": "held", "audited": true, "source": "Straza", "ref": "apr-1",
	"expiresAt": "2026-10-01T12:00:00Z",
	"reason":    "This action waits for a person's approval. Once it is approved, do it again.",
}

// viewsUpstream is the go-sdk server behind the fake per-server endpoint: the
// tool "show" links panelURI, and its call answers a held result carrying the
// decision slot and structured content.
func viewsUpstream(opts *mcp.ServerOptions) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "gateway", Version: "1.0.0"}, opts)
	srv.AddTool(&mcp.Tool{Name: "show", Description: "show the panel",
		InputSchema: map[string]any{"type": "object"},
		Meta:        mcp.Meta{"ui": map[string]any{"resourceUri": panelURI}},
	}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: "Straza: held"}},
			StructuredContent: map[string]any{"state": "held"},
			IsError:           true,
			Meta:              mcp.Meta{"intermediary/decision": heldSlot},
		}, nil
	})
	return srv
}

// addView publishes one view on the upstream with a resource _meta and a
// content _meta, so a test can see both pass through the bridge.
func addView(srv *mcp.Server, uri, title string) {
	srv.AddResource(&mcp.Resource{URI: uri, Name: "panel", Title: title,
		Description: "the demo panel", MIMEType: viewMIME,
		Meta: mcp.Meta{"ui": map[string]any{"prefersBorder": true}},
	}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: uri, MIMEType: viewMIME, Text: panelHTML,
			Meta: mcp.Meta{"ui": map[string]any{"csp": map[string]any{"connectDomains": []any{}}}},
		}}}, nil
	})
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGatewayEndpoint(t *testing.T) {
	for _, tc := range []struct{ server, want string }{
		{"", "https://gw.example/mcp"},
		{"everything", "https://gw.example/mcp/everything"},
		{"a b/c", "https://gw.example/mcp/a%20b%2Fc"},
	} {
		if got := gatewayEndpoint("https://gw.example", tc.server); got != tc.want {
			t.Errorf("gatewayEndpoint(%q) = %q, want %q", tc.server, got, tc.want)
		}
	}
}

// TestMirrorToolDigest pins that the combined bridge mirrors and hashes a tool
// without _meta exactly as its three contract fields, and that _meta counts.
func TestMirrorToolDigest(t *testing.T) {
	schema := map[string]any{"type": "object"}
	plain := &mcp.Tool{Name: "everything__echo", Description: "echo", InputSchema: schema, Title: "not mirrored"}
	const plainJSON = `{"description":"echo","inputSchema":{"type":"object"},"name":"everything__echo"}`
	if got := jsonOf(t, mirrorTool(plain)); got != plainJSON {
		t.Fatalf("mirrored tool without _meta = %s, want %s", got, plainJSON)
	}
	sum := sha256.Sum256([]byte(plainJSON))
	plainDigest := hex.EncodeToString(sum[:])

	withMeta := func(uri string) *mcp.Tool {
		tl := *plain
		tl.Meta = mcp.Meta{"ui": map[string]any{"resourceUri": uri}}
		return &tl
	}
	for _, tc := range []struct {
		name      string
		tool      *mcp.Tool
		samePlain bool
	}{
		{"no _meta hashes as the contract fields alone", plain, true},
		{"empty _meta is omitted", func() *mcp.Tool { tl := *plain; tl.Meta = mcp.Meta{}; return &tl }(), true},
		{"a view link changes the digest", withMeta("ui://demo/a"), false},
	} {
		got, ok := toolDigest(mirrorTool(tc.tool))
		if !ok {
			t.Fatalf("%s: digest failed", tc.name)
		}
		if (got == plainDigest) != tc.samePlain {
			t.Errorf("%s: digest %s, plain %s, want same=%v", tc.name, got, plainDigest, tc.samePlain)
		}
	}
	a, _ := toolDigest(mirrorTool(withMeta("ui://demo/a")))
	b, _ := toolDigest(mirrorTool(withMeta("ui://demo/b")))
	if a == b {
		t.Error("a changed view link kept the digest, so resync would not re-add the tool")
	}
}

func TestMCPProxyServerModeCapabilities(t *testing.T) {
	for _, tc := range []struct {
		server string
		views  bool
	}{
		{"", false},
		{"demo", true},
	} {
		srv := viewsUpstream(nil)
		var g *fakeGateway
		if tc.server == "" {
			g = newFakeGateway(t)
		} else {
			g = newViewsGateway(t, tc.server, srv, nil).fakeGateway
		}
		store := proxyTestStore(t, g.URL)
		_, _, harness := buildProxyFor(t, g, store, tc.server)
		caps := harness.InitializeResult().Capabilities
		_, ext := caps.Extensions[uiExtension]
		if (caps.Resources != nil) != tc.views || ext != tc.views {
			t.Errorf("server %q: resources=%v extension=%v, want both %v", tc.server, caps.Resources != nil, ext, tc.views)
		}
	}
}

func TestMCPProxyServerModeMirrorsToolsAndViews(t *testing.T) {
	up := viewsUpstream(nil)
	addView(up, panelURI, "Panel")
	g := newViewsGateway(t, "demo", up, nil)
	store := proxyTestStore(t, g.URL)
	ctx, p, harness := buildProxyFor(t, g.fakeGateway, store, "demo")

	tools, err := harness.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "show" {
		t.Fatalf("tools = %+v, %v", tools, err)
	}
	if got, want := jsonOf(t, tools.Tools[0].Meta), `{"ui":{"resourceUri":"ui://demo/panel"}}`; got != want {
		t.Errorf("tool _meta = %s, want %s", got, want)
	}

	res, err := harness.ListResources(ctx, nil)
	if err != nil || len(res.Resources) != 1 {
		t.Fatalf("resources = %+v, %v", res, err)
	}
	want := `{"_meta":{"ui":{"prefersBorder":true}},"description":"the demo panel","mimeType":"text/html;profile=mcp-app","name":"panel","title":"Panel","uri":"ui://demo/panel"}`
	if got := jsonOf(t, res.Resources[0]); got != want {
		t.Errorf("mirrored view = %s, want %s", got, want)
	}

	read, err := harness.ReadResource(ctx, &mcp.ReadResourceParams{URI: panelURI})
	if err != nil || len(read.Contents) != 1 {
		t.Fatalf("read = %+v, %v", read, err)
	}
	c := read.Contents[0]
	if c.Text != panelHTML || c.MIMEType != viewMIME || jsonOf(t, c.Meta) != `{"ui":{"csp":{"connectDomains":[]}}}` {
		t.Errorf("read content = %+v, want the gateway's content unchanged", c)
	}

	// A changed link re-adds the tool. A changed title re-adds the view. A view
	// the gateway stops listing leaves the mirror. The maps are read under the
	// proxy's lock, because a notification-driven resync may run alongside.
	digestOf := func() (string, int) {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.registered["show"], len(p.views)
	}
	before, _ := digestOf()
	up.AddTool(&mcp.Tool{Name: "show", Description: "show the panel", InputSchema: map[string]any{"type": "object"},
		Meta: mcp.Meta{"ui": map[string]any{"resourceUri": "ui://demo/other"}}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	addView(up, panelURI, "Panel, renamed")
	p.resync(ctx)
	if after, _ := digestOf(); after == before {
		t.Error("a changed tool _meta kept the digest")
	}
	res, err = harness.ListResources(ctx, nil)
	if err != nil || len(res.Resources) != 1 || res.Resources[0].Title != "Panel, renamed" {
		t.Errorf("after a title change, resources = %+v, %v", res, err)
	}
	up.RemoveResources(panelURI)
	p.resync(ctx)
	res, err = harness.ListResources(ctx, nil)
	if _, mirrored := digestOf(); err != nil || len(res.Resources) != 0 || mirrored != 0 {
		t.Errorf("a view the gateway dropped is still mirrored: %+v, %d, %v", res, mirrored, err)
	}
}

// TestMCPProxyCallResultPassthrough pins that a call result reaches the host
// unchanged, structured content and the decision slot included.
func TestMCPProxyCallResultPassthrough(t *testing.T) {
	g := newViewsGateway(t, "demo", viewsUpstream(nil), nil)
	store := proxyTestStore(t, g.URL)
	ctx, _, harness := buildProxyFor(t, g.fakeGateway, store, "demo")

	out, err := harness.CallTool(ctx, &mcp.CallToolParams{Name: "show", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !out.IsError || jsonOf(t, out.StructuredContent) != `{"state":"held"}` {
		t.Errorf("result = %+v, want the held result unchanged", out)
	}
	if got, want := jsonOf(t, out.Meta), jsonOf(t, map[string]any{"intermediary/decision": heldSlot}); got != want {
		t.Errorf("result _meta = %s, want %s", got, want)
	}
}

func TestMCPProxyViewsListing(t *testing.T) {
	methodNotFound := func(w http.ResponseWriter, req rpcRequest) {
		writeRPC(w, req.ID, map[string]any{"error": map[string]any{"code": -32601, "message": "method not found"}})
	}
	badURI := func(w http.ResponseWriter, req rpcRequest) {
		writeRPC(w, req.ID, map[string]any{"result": map[string]any{"resources": []any{
			map[string]any{"uri": panelURI, "name": "panel", "mimeType": viewMIME},
			map[string]any{"uri": "ui://demo/%zz", "name": "broken", "mimeType": viewMIME},
		}}})
	}
	// Each case resyncs twice (boot, then once more), so a listing that is
	// walked shows two resources/list calls.
	for _, tc := range []struct {
		name      string
		opts      *mcp.ServerOptions
		views     []string
		list      func(http.ResponseWriter, rpcRequest) // nil: the go-sdk server answers
		want      []string
		wantLog   string
		wantLists int64 // -1: not counted
	}{
		{name: "pages are walked to the end", opts: &mcp.ServerOptions{PageSize: 1},
			views: []string{"ui://demo/a", "ui://demo/b", "ui://demo/c"},
			want:  []string{"ui://demo/a", "ui://demo/b", "ui://demo/c"}, wantLists: -1},
		{name: "-32601 means no views", list: methodNotFound, wantLists: 2},
		{name: "a URI url.Parse refuses is skipped", list: badURI,
			want: []string{panelURI}, wantLog: "ui://demo/%zz", wantLists: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := viewsUpstream(tc.opts)
			for _, uri := range tc.views {
				addView(up, uri, uri)
			}
			var lists atomic.Int64
			intercept := map[string]rpcIntercept{}
			if tc.list != nil {
				intercept["resources/list"] = func(w http.ResponseWriter, req rpcRequest) bool {
					lists.Add(1)
					tc.list(w, req)
					return true
				}
			}
			g := newViewsGateway(t, "demo", up, intercept)
			store := proxyTestStore(t, g.URL)
			var mu sync.Mutex
			var logged []string
			ctx, p, harness := buildProxyFor(t, g.fakeGateway, store, "demo")
			p.logf = func(_, msg string) { mu.Lock(); logged = append(logged, msg); mu.Unlock() }
			p.resync(ctx)

			res, err := harness.ListResources(ctx, nil)
			if err != nil {
				t.Fatalf("list resources: %v", err)
			}
			var got []string
			for _, r := range res.Resources {
				got = append(got, r.URI)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("mirrored views = %v, want %v", got, tc.want)
			}
			if tools, err := harness.ListTools(ctx, nil); err != nil || len(tools.Tools) != 1 {
				t.Errorf("tools must mirror whatever the views do: %+v, %v", tools, err)
			}
			if tc.wantLists >= 0 && lists.Load() != tc.wantLists {
				t.Errorf("resources/list calls = %d, want %d", lists.Load(), tc.wantLists)
			}
			mu.Lock()
			defer mu.Unlock()
			if tc.wantLog != "" && !strings.Contains(strings.Join(logged, "\n"), tc.wantLog) {
				t.Errorf("logged %q, want a line naming %s", logged, tc.wantLog)
			}
		})
	}
}

func TestMCPProxyViewRead(t *testing.T) {
	refusal := "Straza: the view \"ui://demo/panel\" is not available to you on this server. A view is shown only to people who can use a tool that opens it."
	refuse := func(w http.ResponseWriter, req rpcRequest) bool {
		writeRPC(w, req.ID, map[string]any{"error": map[string]any{"code": -32002, "message": refusal}})
		return true
	}
	unavailable := func(w http.ResponseWriter, _ rpcRequest) bool {
		w.WriteHeader(http.StatusServiceUnavailable)
		return true
	}
	for _, tc := range []struct {
		name       string
		read       rpcIntercept
		latch      bool
		wantCode   int64 // 0: the read succeeds
		wantPrefix string
	}{
		{name: "a read through a latched session revives and replays", latch: true},
		{name: "the gateway's refusal passes through with its code", read: refuse,
			wantCode: -32002, wantPrefix: refusal},
		{name: "a transport failure answers the bridge's sentence, not a go-sdk local code", read: unavailable,
			wantCode: jsonrpc.CodeInternalError, wantPrefix: "Straza: could not read the view ui://demo/panel through the gateway: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := viewsUpstream(nil)
			addView(up, panelURI, "Panel")
			intercept := map[string]rpcIntercept{}
			if tc.read != nil {
				intercept["resources/read"] = tc.read
			}
			g := newViewsGateway(t, "demo", up, intercept)
			store := proxyTestStore(t, g.URL)
			ctx, p, harness := buildProxyFor(t, g.fakeGateway, store, "demo")
			p.jitterMax = 0
			dials := countDials(p)
			if tc.latch {
				_ = p.session().Close()
			}

			read, err := harness.ReadResource(ctx, &mcp.ReadResourceParams{URI: panelURI})
			if tc.wantCode == 0 {
				if err != nil || len(read.Contents) != 1 || read.Contents[0].Text != panelHTML {
					t.Fatalf("read = %+v, %v", read, err)
				}
			} else {
				var rpcErr *jsonrpc.Error
				if !errors.As(err, &rpcErr) || rpcErr.Code != tc.wantCode || !strings.HasPrefix(rpcErr.Message, tc.wantPrefix) {
					t.Fatalf("read error = %#v, want code %d starting %q", err, tc.wantCode, tc.wantPrefix)
				}
			}
			if want := map[bool]int64{true: 1, false: 0}[tc.latch]; dials.Load() != want {
				t.Errorf("revival dials = %d, want %d", dials.Load(), want)
			}
		})
	}
}

// TestMCPProxyServerRefusalExits pins the bridge's exit when the gateway
// refuses the server: at once, without the boot retries, naming what to do.
func TestMCPProxyServerRefusalExits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		older bool
		want  []string
	}{
		{name: "a server not in the catalog", want: []string{
			`Straza: no MCP server named "nope" is in your catalog. Check the name, or ask an administrator for access to it. If the server name "nope" is wrong, fix the argument after mcp in this chat app's MCP configuration, then restart the chat app`}},
		{name: "a gateway without per-server endpoints", older: true, want: []string{
			"Straza: the gateway has no endpoint for one server at ", "/mcp/nope, so it likely runs an older Straza server. ",
			`Ask your administrator to upgrade it, or remove "nope" after mcp in this chat app's MCP configuration to use all your servers through one endpoint, without views`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var url string
			var inits func() int64 // nil: the older gateway's 404 never reaches a handler
			if tc.older {
				url = newFakeGateway(t).URL
			} else {
				g := newViewsGateway(t, "demo", viewsUpstream(nil), nil)
				url, inits = g.URL, g.inits.Load
			}
			proxyTestStore(t, url)
			_, st := mcp.NewInMemoryTransports()
			start := time.Now()
			err := runMCPProxy(context.Background(), "claude-code", "nope", st)
			if err == nil {
				t.Fatal("the bridge must exit when the gateway refuses the server")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("exit error = %q, want it to contain %q", err, w)
				}
			}
			if inits != nil && inits() != 1 {
				t.Errorf("initialize attempts = %d, want 1 (a refusal is not retried)", inits())
			}
			if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
				t.Errorf("refusal took %s, want an exit without the boot retries", elapsed)
			}
		})
	}
}

// TestMCPProxyServerStartupRetriesTransientFailure pins that a transport
// failure at startup in server mode takes the normal boot retries. go-sdk
// reports it as its own *jsonrpc.Error (-32005), which is no gateway refusal.
func TestMCPProxyServerStartupRetriesTransientFailure(t *testing.T) {
	var failed atomic.Bool
	g := newViewsGateway(t, "demo", viewsUpstream(nil), map[string]rpcIntercept{
		"initialize": func(w http.ResponseWriter, _ rpcRequest) bool {
			if failed.CompareAndSwap(false, true) {
				w.WriteHeader(http.StatusServiceUnavailable)
				return true
			}
			return false
		},
	})
	proxyTestStore(t, g.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ct, st := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- runMCPProxy(ctx, "claude-code", "demo", st) }()

	deadline := time.Now().Add(10 * time.Second)
	for g.inits.Load() < 2 && time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("the bridge exited on a 503 at startup instead of retrying: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	harness, err := mcp.NewClient(&mcp.Implementation{Name: "harness", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("connect harness after the retry: %v", err)
	}
	if tools, err := harness.ListTools(ctx, nil); err != nil || len(tools.Tools) != 1 {
		t.Errorf("tools after the retry = %+v, %v", tools, err)
	}
	_ = harness.Close()
	cancel()
	<-done
}

// TestMCPProxyServerNameRule pins that an operand outside the manifest name
// rule is refused before any check-in or gateway request. "." and ".." would
// otherwise reach the combined /mcp through the mux's path cleaning.
func TestMCPProxyServerNameRule(t *testing.T) {
	for _, name := range []string{".", "..", "Demo", "a/b", "-demo", "demo-", strings.Repeat("a", 65)} {
		g := newFakeGateway(t)
		proxyTestStore(t, g.URL)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, st := mcp.NewInMemoryTransports()
		err := runMCPProxy(ctx, "claude-code", name, st)
		cancel()
		want := fmt.Sprintf("the server name %q is not valid. A server name has only lowercase letters, digits and hyphens", name)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "strazactl apps list") {
			t.Errorf("runMCPProxy(%q) = %v, want the name rule refusal", name, err)
		}
		if n := g.checkins.Load() + g.calls.Load(); n != 0 {
			t.Errorf("runMCPProxy(%q) made %d requests, want none", name, n)
		}
	}
}

// TestMCPProxyServerRefusedMidSession pins that a server endpoint refusing the
// first page (-32602, the server left the caller's catalog) empties the
// mirror of tools and views instead of keeping the old ones.
func TestMCPProxyServerRefusedMidSession(t *testing.T) {
	up := viewsUpstream(nil)
	addView(up, panelURI, "Panel")
	g := newViewsGateway(t, "demo", up, nil)
	store := proxyTestStore(t, g.URL)
	ctx, p, harness := buildProxyFor(t, g.fakeGateway, store, "demo")
	p.retryPause = 0

	g.refuse.Store(true)
	p.resync(ctx)
	tools, err := harness.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 0 {
		t.Errorf("tools after the refusal = %+v, %v, want none", tools, err)
	}
	res, err := harness.ListResources(ctx, nil)
	if err != nil || len(res.Resources) != 0 {
		t.Errorf("views after the refusal = %+v, %v, want none", res, err)
	}

	g.refuse.Store(false)
	p.resync(ctx)
	if tools, err := harness.ListTools(ctx, nil); err != nil || len(tools.Tools) != 1 {
		t.Errorf("tools after access returned = %+v, %v", tools, err)
	}
}

// TestMCPProxyViewsSwitchedOnAfterConnect pins that the bridge walks
// resources/list on every resync in server mode, so views switched on after
// it connected appear, although that initialize advertised no resources.
func TestMCPProxyViewsSwitchedOnAfterConnect(t *testing.T) {
	up := viewsUpstream(nil) // no resources: initialize advertises none
	g := newViewsGateway(t, "demo", up, nil)
	store := proxyTestStore(t, g.URL)
	ctx, p, harness := buildProxyFor(t, g.fakeGateway, store, "demo")
	if p.session().InitializeResult().Capabilities.Resources != nil {
		t.Fatal("precondition: the gateway must not advertise resources at connect")
	}

	addView(up, panelURI, "Panel")
	p.resync(ctx)
	res, err := harness.ListResources(ctx, nil)
	if err != nil || len(res.Resources) != 1 || res.Resources[0].URI != panelURI {
		t.Errorf("views after they were switched on = %+v, %v", res, err)
	}
}
