package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// The views upstream's two views. show links panelURI under
// _meta.ui.resourceUri, and old links legacyURI under the legacy flat key.
const (
	panelURI  = "ui://viewapp/panel"
	legacyURI = "ui://viewapp/legacy"
)

// viewHTML is the document the views upstream answers for uri.
func viewHTML(uri string) string { return "<!doctype html><title>" + uri + "</title>" }

// startViewsUpstream runs an MCP server whose tools link MCP Apps views:
// show links panelURI and also carries an unrelated _meta key, old links
// legacyURI under the legacy flat key, and plain links none. Each tool
// answers "<name>: <text>", and each view is a ui:// resource of the MCP
// Apps media type with its own _meta.
func startViewsUpstream(t *testing.T) *gatewayUpstream {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "views-upstream", Version: "1.0.0"}, nil)
	type textArgs struct {
		Text string `json:"text"`
	}
	answer := func(word string) func(context.Context, *mcp.CallToolRequest, textArgs) (*mcp.CallToolResult, any, error) {
		return func(_ context.Context, _ *mcp.CallToolRequest, a textArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: word + ": " + a.Text}}}, nil, nil
		}
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "show", Description: "show", Meta: mcp.Meta{
		"ui": map[string]any{"resourceUri": panelURI}, "x/kept": "yes",
	}}, answer("show"))
	mcp.AddTool(srv, &mcp.Tool{Name: "old", Description: "old", Meta: mcp.Meta{"ui/resourceUri": legacyURI}}, answer("old"))
	mcp.AddTool(srv, &mcp.Tool{Name: "plain", Description: "plain"}, answer("plain"))
	for _, uri := range []string{panelURI, legacyURI} {
		srv.AddResource(&mcp.Resource{URI: uri, Name: path.Base(uri), MIMEType: manager.ViewMIMEType},
			func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
					URI: req.Params.URI, MIMEType: manager.ViewMIMEType, Text: viewHTML(req.Params.URI),
					Meta: mcp.Meta{"ui": map[string]any{"prefersBorder": true}},
				}}}, nil
			})
	}
	up := &gatewayUpstream{Server: httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))}
	t.Cleanup(up.Close)
	return up
}

// installViewsApp installs the views upstream as name with
// straza.exposure.views set to views, a manifest rate cap of rps (0 is
// uncapped) and the extra lines of the straza block, gives dev reach into
// every tool through a role of the server's own, and waits until the server
// runs.
func installViewsApp(t *testing.T, app *App, up *gatewayUpstream, name string, views bool, rps int, extra ...string) {
	t.Helper()
	limits := ""
	if rps > 0 {
		limits = fmt.Sprintf("\n  limits: {rps: %d}", rps)
	}
	limits += strings.Join(extra, "")
	mf, err := managerParse(t, fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: %s}
server: {name: straza.test/%s, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
  exposure: {tools: ["*"], views: %t}%s
`, name, name, up.URL, views, limits))
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.manager.Install(context.Background(), mf, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	catReach(t, app, "dev", row, `["*"]`)
	catWaitRunning(t, app, name)
}

// viewsPolicy denies old with a reason and allows show and plain on the
// servers apps, for the role dev.
func viewsPolicy(apps ...string) string {
	return fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: views-policy}
spec:
  match: {roles: [dev]}
  rules:
    - id: deny-old
      tools: [mcp.call]
      apps: [%[1]s]
      toolNames: {deny: ["old"]}
      effect: deny
      reason: "old views are retired"
    - id: allow-rest
      tools: [mcp.call]
      apps: [%[1]s]
      toolNames: {allow: ["show", "plain"]}
      effect: allow
`, strings.Join(apps, ", "))
}

// TestGatewayServerEndpoint pins /mcp/{server} for a server whose views are
// off, and pins that /mcp is unchanged: the combined list carries no _meta,
// the server endpoint lists the server's own names with _meta, resolves
// only those names, refuses a server outside the caller's catalog with one
// sentence, and names the server in its records.
func TestGatewayServerEndpoint(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	up := startViewsUpstream(t)
	kim := seedGatewayUser(t, app, "kim", "dev")
	seedGatewayUser(t, app, "lee", "intern")
	installViewsApp(t, app, up, "viewapp", false, 0)
	catActivate(t, app, "views-policy", viewsPolicy("viewapp"))
	tok, sid := gatewaySession(t, base, "kim")
	leeTok, _ := gatewaySession(t, base, "lee")

	// /mcp: the upstream's _meta never reaches the combined list, which
	// keeps exactly the fields it served before.
	_, list, raw := mcpCall(t, base, tok, "tools/list", nil)
	if got := toolNamesOf(t, list); !reflect.DeepEqual(got, []string{"viewapp__old", "viewapp__plain", "viewapp__show"}) {
		t.Fatalf("combined names = %v", got)
	}
	for _, tl := range resultOf(t, list, raw)["tools"].([]any) {
		var keys []string
		for k := range tl.(map[string]any) {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if !reflect.DeepEqual(keys, []string{"annotations", "description", "inputSchema", "name"}) {
			t.Errorf("combined tool keys = %v, want exactly annotations, description, inputSchema and name", keys)
		}
	}

	// The server endpoint: tools only, own names, _meta verbatim minus the
	// view links the switch strips.
	_, init, raw := mcpServerCall(t, base, "viewapp", tok, "initialize", map[string]any{"protocolVersion": "2025-06-18"})
	if caps := resultOf(t, init, raw)["capabilities"]; !reflect.DeepEqual(caps, map[string]any{"tools": map[string]any{"listChanged": true}}) {
		t.Errorf("views-off capabilities = %v, want tools only", caps)
	}
	_, list, raw = mcpServerCall(t, base, "viewapp", tok, "tools/list", nil)
	if got := toolNamesOf(t, list); !reflect.DeepEqual(got, []string{"old", "plain", "show"}) {
		t.Fatalf("server names = %v", got)
	}
	metas := map[string]any{}
	for _, tl := range resultOf(t, list, raw)["tools"].([]any) {
		m := tl.(map[string]any)
		metas[m["name"].(string)] = m["_meta"]
	}
	if !reflect.DeepEqual(metas, map[string]any{"old": nil, "plain": nil, "show": map[string]any{"x/kept": "yes"}}) {
		t.Errorf("server _meta = %v, want show's x/kept only, with the view links stripped", metas)
	}

	// Calls take the server's own names only.
	if _, _, raw := mcpServerCall(t, base, "viewapp", tok, "tools/call", map[string]any{"name": "plain", "arguments": map[string]string{"text": "hi"}}); !strings.Contains(string(raw), "plain: hi") {
		t.Errorf("own-name call = %s, want the upstream answer", raw)
	}
	for _, name := range []string{"viewapp__plain", "approval_await"} {
		res := second(mcpServerCall(t, base, "viewapp", tok, "tools/call", map[string]any{"name": name}))
		if code, msg := rpcErrorOf(t, res, nil); code != -32602 || msg != fmt.Sprintf("unknown tool %q", name) {
			t.Errorf("call %q = %d %q, want -32602 unknown tool", name, code, msg)
		}
	}
	res := second(mcpServerCall(t, base, "viewapp", tok, "resources/list", nil))
	if code, msg := rpcErrorOf(t, res, nil); code != -32601 || msg != unknownMethodMsg("resources/list", &serverScope{name: "viewapp"}) {
		t.Errorf("resources/list with views off = %d %q", code, msg)
	}

	// One sentence for an unknown, a built-in and an ungranted server.
	for _, c := range []struct{ token, server string }{{tok, "nope"}, {tok, "straza"}, {leeTok, "viewapp"}} {
		for _, method := range []string{"initialize", "tools/list", "tools/call", "resources/read"} {
			res := second(mcpServerCall(t, base, c.server, c.token, method, map[string]any{"name": "x", "uri": "ui://x"}))
			if code, msg := rpcErrorOf(t, res, nil); code != -32602 || msg != notInCatalogMsg(c.server) {
				t.Errorf("%s on %q = %d %q, want -32602 %q", method, c.server, code, msg, notInCatalogMsg(c.server))
			}
		}
	}

	// GET and DELETE answer as on /mcp.
	for _, c := range []struct {
		method string
		want   int
	}{{http.MethodDelete, http.StatusOK}, {http.MethodGet, http.StatusMethodNotAllowed}} {
		req, _ := http.NewRequest(c.method, base+"/mcp/viewapp", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s /mcp/viewapp = %d, want %d", c.method, resp.StatusCode, c.want)
		}
	}

	// Records: the server's list names it, each probe leaves one deny.
	list1 := awaitMCPRecords(t, app, 1, func(d map[string]any) bool { return d["event"] == "tools.list" && d["app"] == "viewapp" })[0]
	if list1["count"] != float64(3) || list1["session"] != sid || list1["user"] != kim.ID || list1["reason"] != "catalog served" {
		t.Errorf("server tools.list record = %v", list1)
	}
	awaitMCPRecords(t, app, 1, func(d map[string]any) bool { return d["event"] == "tools.list" && d["app"] == "" })
	allow := awaitMCPRecords(t, app, 1, func(d map[string]any) bool { return d["app"] == "viewapp" && d["toolName"] == "plain" })[0]
	if allow["effect"] != "allow" || allow["ruleId"] != "allow-rest" {
		t.Errorf("own-name call record = %v", allow)
	}
	probe := awaitMCPRecords(t, app, 1, func(d map[string]any) bool { return d["app"] == "viewapp" && d["toolName"] == "viewapp__plain" })[0]
	if probe["effect"] != "deny" || !strings.HasPrefix(probe["reason"].(string), `unknown tool "viewapp__plain"`) {
		t.Errorf("prefixed-name probe record = %v", probe)
	}
	for _, server := range []string{"nope", "straza"} {
		rec := awaitMCPRecords(t, app, 1, func(d map[string]any) bool {
			return d["event"] == "tool.pre" && d["app"] == server && d["toolName"] == "x"
		})[0]
		if rec["effect"] != "deny" || rec["reason"] != notInCatalogMsg(server) || rec["session"] != sid {
			t.Errorf("probe record of %q = %v", server, rec)
		}
		awaitMCPRecords(t, app, 1, func(d map[string]any) bool {
			return d["event"] == eventResourcesRead && d["app"] == server && d["uri"] == "ui://x" && d["effect"] == "deny"
		})
	}
}

// TestGatewayServerEndpointPaging pins paging on a server endpoint: its
// list pages under the server's own names, and a cursor of one endpoint is
// refused on the other.
func TestGatewayServerEndpointPaging(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) { c.Apps.Catalog.PageSize = 1 })
	up := startViewsUpstream(t)
	seedGatewayUser(t, app, "kim", "dev")
	installViewsApp(t, app, up, "viewapp", false, 0)
	tok, _ := gatewaySession(t, base, "kim")

	var names []string
	var first string
	var params any
	for page := 0; page < 5; page++ {
		_, res, raw := mcpServerCall(t, base, "viewapp", tok, "tools/list", params)
		names = append(names, toolNamesOf(t, res)...)
		next, ok := resultOf(t, res, raw)["nextCursor"].(string)
		if !ok {
			break
		}
		if first == "" {
			first = next
		}
		params = map[string]any{"cursor": next}
	}
	if !reflect.DeepEqual(names, []string{"old", "plain", "show"}) {
		t.Fatalf("paged server names = %v", names)
	}
	assertCursorRejected(t, second(mcpCall(t, base, tok, "tools/list", map[string]any{"cursor": first})))
	_, res, raw := mcpCall(t, base, tok, "tools/list", nil)
	combined, _ := resultOf(t, res, raw)["nextCursor"].(string)
	assertCursorRejected(t, second(mcpServerCall(t, base, "viewapp", tok, "tools/list", map[string]any{"cursor": combined})))
}
