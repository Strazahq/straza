package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/manager"
)

// testServerCatalog is a tier-1 catalog with two proxied servers, a and b,
// and the built-in straza app. a links a view from two tools, one under
// _meta.ui.resourceUri and one under the legacy flat key.
func testServerCatalog() *sessionCatalog {
	aShow := gwTool{Name: "a__show", InputSchema: map[string]any{"type": "object"},
		Meta: map[string]any{"ui": map[string]any{"resourceUri": "ui://a/panel"}, "x/kept": "yes"}}
	aOld := gwTool{Name: "a__old", Meta: map[string]any{"ui/resourceUri": "ui://a/legacy"}}
	aPlain := gwTool{Name: "a__plain"}
	bTool := gwTool{Name: "b__echo"}
	return &sessionCatalog{
		key: "dev|1|snap",
		tools: []gwTool{aOld, aPlain, aShow, bTool,
			{Name: "straza__approval_await"}},
		targets: map[string]gwTarget{
			"a__show": {app: "a", tool: "show"}, "a__old": {app: "a", tool: "old"},
			"a__plain": {app: "a", tool: "plain"}, "b__echo": {app: "b", tool: "echo"},
			"straza__approval_await": {app: nativeAppName, tool: "approval_await"},
		},
		servers: map[string]*serverCatalog{
			"a": {tools: []gwTool{aOld, aPlain, aShow}, viewsOn: true, views: []manager.View{
				{URI: "ui://a/legacy"}, {URI: "ui://a/orphan"}, {URI: "ui://a/panel"},
			}},
			"b": {tools: []gwTool{bTool}},
		},
	}
}

// TestServerTools pins the server endpoint's list: own names, the overlay's
// hidden tools left out and its justification schema swapped in, _meta kept
// verbatim, and the tier-1 entries untouched.
func TestServerTools(t *testing.T) {
	t.Parallel()
	tier1 := testServerCatalog()
	ov := &catalogOverlay{
		hidden:  map[string]bool{"a__old": true},
		schemas: map[string]any{"a__show": map[string]any{"injected": true}},
	}
	got := serverTools("a", tier1.servers["a"], ov)
	var names []string
	for _, tl := range got {
		names = append(names, tl.Name)
	}
	if !reflect.DeepEqual(names, []string{"plain", "show"}) {
		t.Fatalf("names = %v, want [plain show]", names)
	}
	if got[1].Meta["x/kept"] != "yes" || got[1].InputSchema.(map[string]any)["injected"] != true {
		t.Errorf("show = %+v, want _meta kept and the injected schema", got[1])
	}
	if tier1.servers["a"].tools[2].Name != "a__show" || tier1.servers["a"].tools[2].InputSchema.(map[string]any)["injected"] != nil {
		t.Error("serverTools mutated the tier-1 entry")
	}
}

// TestServerScopeTarget pins name resolution on a server endpoint: the
// server's own name resolves, and a prefixed name, another server's tool,
// a built-in straza tool and an unknown name do not.
func TestServerScopeTarget(t *testing.T) {
	t.Parallel()
	tier1 := testServerCatalog()
	rows := []struct {
		server, name string
		want         bool
	}{
		{"a", "show", true},
		{"a", "a__show", false},
		{"a", "echo", false},
		{"b", "echo", true},
		{"a", "approval_await", false},
		{"a", "nope", false},
	}
	for _, row := range rows {
		s := &serverScope{name: row.server, tier1: tier1, cat: tier1.servers[row.server]}
		if _, ok := s.target(row.name); ok != row.want {
			t.Errorf("server %s target(%q) = %v, want %v", row.server, row.name, ok, row.want)
		}
	}
}

// TestLinkedViews pins the read rule: a view is served exactly when a tool
// the caller can see links it, under _meta.ui.resourceUri or the legacy flat
// key, in the order the views are held. A view no visible tool links is not
// served.
func TestLinkedViews(t *testing.T) {
	t.Parallel()
	views := testServerCatalog().servers["a"].views
	show := gwTool{Name: "show", Meta: map[string]any{"ui": map[string]any{"resourceUri": "ui://a/panel"}}}
	old := gwTool{Name: "old", Meta: map[string]any{"ui/resourceUri": "ui://a/legacy"}}
	both := gwTool{Name: "both", Meta: map[string]any{"ui": map[string]any{"resourceUri": "ui://a/panel"}, "ui/resourceUri": "ui://a/legacy"}}
	odd := gwTool{Name: "odd", Meta: map[string]any{"ui": "ui://a/panel", "ui/resourceUri": 7}}
	rows := []struct {
		name  string
		tools []gwTool
		want  []string
	}{
		{"both link keys", []gwTool{show, old}, []string{"ui://a/legacy", "ui://a/panel"}},
		{"one tool under both keys", []gwTool{both}, []string{"ui://a/legacy", "ui://a/panel"}},
		{"the linking tool is hidden", []gwTool{old}, []string{"ui://a/legacy"}},
		{"no tool links a view", []gwTool{{Name: "plain"}}, nil},
		{"links of the wrong type", []gwTool{odd}, nil},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			var got []string
			for _, v := range linkedViews(row.tools, views) {
				got = append(got, v.URI)
			}
			if !reflect.DeepEqual(got, row.want) {
				t.Errorf("views = %v, want %v", got, row.want)
			}
		})
	}
}

// TestCatalogCursorDigestScope pins that /mcp keeps its cursor digest bytes
// and that a server endpoint's cursor binds that endpoint, so no cursor
// pages another endpoint's list.
func TestCatalogCursorDigestScope(t *testing.T) {
	t.Parallel()
	tier1 := testServerCatalog()
	ov := &catalogOverlay{key: "ov|1"}
	sum := sha256.Sum256([]byte(tier1.key + "\x00" + ov.key))
	if got := catalogCursorDigest(tier1, ov, ""); got != hex.EncodeToString(sum[:16]) {
		t.Errorf("combined digest = %s, want the historical sha256(tier1.key NUL overlay.key)", got)
	}
	cursors := map[string]string{}
	for _, server := range []string{"", "a", "b"} {
		cursors[server] = catalogCursor(tier1, ov, server, 1)
	}
	for minted, cursor := range cursors {
		for _, replayed := range []string{"", "a", "b"} {
			_, ok := parseCatalogCursor(cursor, tier1, ov, replayed, 3)
			if want := minted == replayed; ok != want {
				t.Errorf("cursor of %q replayed on %q accepted = %v, want %v", minted, replayed, ok, want)
			}
		}
	}
}

// TestMethodFor pins the method table: both endpoints serve the tool
// methods, and only a server endpoint whose views are on serves resources.
func TestMethodFor(t *testing.T) {
	t.Parallel()
	off := &serverScope{name: "a", cat: &serverCatalog{}}
	on := &serverScope{name: "a", cat: &serverCatalog{viewsOn: true}}
	rows := []struct {
		method string
		srv    *serverScope
		want   bool
	}{
		{"tools/call", nil, true},
		{"tools/call", on, true},
		{"initialize", off, true},
		{"resources/list", nil, false},
		{"resources/list", off, false},
		{"resources/list", on, true},
		{"resources/read", on, true},
		{"resources/templates/list", on, true},
		{"prompts/list", on, false},
	}
	for _, row := range rows {
		if _, ok := methodFor(row.method, row.srv); ok != row.want {
			t.Errorf("methodFor(%q, views %v) = %v, want %v", row.method, row.srv.viewsOn(), ok, row.want)
		}
	}
	if got := unknownMethodMsg("resources/list", nil); got != `method "resources/list" is not served by this gateway (tools only in v0)` {
		t.Errorf("combined unknown-method sentence = %q", got)
	}
	if got := unknownMethodMsg("resources/list", off); !strings.HasPrefix(got, "Straza: ") || !strings.Contains(got, `"a"`) {
		t.Errorf("server unknown-method sentence = %q", got)
	}
}

// TestSelfCappedGatewayPaths pins the body-cap exemption: /mcp and a server
// endpoint cap their own bodies, and no other path under /mcp is exempt.
func TestSelfCappedGatewayPaths(t *testing.T) {
	t.Parallel()
	rows := []struct {
		path string
		want bool
	}{
		{"/mcp", true},
		{"/mcp/echoapp", true},
		{"/mcp/", false},
		{"/mcp/a/b", false},
		{"/mcpx", false},
		{"/v1/audit/batch", true},
		{"/v1/admin/apps", false},
	}
	for _, row := range rows {
		if got := selfCapped(row.path); got != row.want {
			t.Errorf("selfCapped(%q) = %v, want %v", row.path, got, row.want)
		}
	}
}

// mcpServerCall posts one JSON-RPC message to the endpoint of server.
func mcpServerCall(t *testing.T, base, server, token, method string, params any) (int, map[string]any, []byte) {
	t.Helper()
	return mcpPost(t, base+"/mcp/"+server, token, method, params)
}

// rpcErrorOf returns the JSON-RPC error code and message of an answer, or
// fails when the answer is not an error.
func rpcErrorOf(t *testing.T, res map[string]any, raw []byte) (int, string) {
	t.Helper()
	errObj, _ := res["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("answer %s is not a JSON-RPC error", raw)
	}
	code, _ := errObj["code"].(float64)
	msg, _ := errObj["message"].(string)
	return int(code), msg
}

// resultOf returns the result object of an answer, or fails.
func resultOf(t *testing.T, res map[string]any, raw []byte) map[string]any {
	t.Helper()
	result, _ := res["result"].(map[string]any)
	if result == nil {
		t.Fatalf("answer %s has no result", raw)
	}
	return result
}

// TestGatewayServerEndpointMethods pins GET and DELETE on a server endpoint,
// which behave as on /mcp, and that the endpoint needs the session token.
func TestGatewayServerEndpointMethods(t *testing.T) {
	t.Parallel()
	_, base := testApp(t)
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		req, _ := http.NewRequest(method, base+"/mcp/echoapp", strings.NewReader(`{}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without a token = %d, want 401", method, resp.StatusCode)
		}
	}
}
