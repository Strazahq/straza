package agentguard

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/version"
)

// The tool fields the bridge mirrors beyond the contract: title, annotations
// and the output schema, which go-sdk Server.AddTool takes only as a JSON
// object whose type is "object".

// listingGateway is a fake per-server endpoint for the server "demo" whose
// tools/list answers tools, each written as the gateway sends it. When
// result is set, every tools/call answers it.
func listingGateway(t *testing.T, tools []string, result string) *viewsGateway {
	t.Helper()
	intercept := map[string]rpcIntercept{
		"tools/list": func(w http.ResponseWriter, req rpcRequest) bool {
			list := json.RawMessage("[" + strings.Join(tools, ",") + "]")
			writeRPC(w, req.ID, map[string]any{"result": map[string]any{"tools": list}})
			return true
		},
	}
	if result != "" {
		intercept["tools/call"] = func(w http.ResponseWriter, req rpcRequest) bool {
			writeRPC(w, req.ID, map[string]any{"result": json.RawMessage(result)})
			return true
		}
	}
	up := mcp.NewServer(&mcp.Implementation{Name: "gateway", Version: "1.0.0"}, nil)
	return newViewsGateway(t, "demo", up, intercept)
}

// canonJSON is the JSON text raw with its object keys sorted, so two
// encodings of one value compare equal.
func canonJSON(t *testing.T, raw string) string {
	t.Helper()
	var tree any
	if err := json.Unmarshal([]byte(raw), &tree); err != nil {
		t.Fatalf("canonJSON(%s): %v", raw, err)
	}
	return jsonOf(t, tree)
}

// addToolTakes reports whether go-sdk Server.AddTool takes a tool whose
// output schema is schema. AddTool panics on a schema it refuses, and the
// panic leaves took false.
func addToolTakes(schema any) (took bool) {
	defer func() { _ = recover() }()
	srv := mcp.NewServer(&mcp.Implementation{Name: "probe", Version: "1.0.0"}, nil)
	srv.AddTool(&mcp.Tool{Name: "probe", InputSchema: map[string]any{"type": "object"}, OutputSchema: schema},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return nil, nil })
	return true
}

// TestMCPProxyMirrorsToolFields pins what the harness lists for a gateway
// tool: the title, the annotations and an object output schema as the
// gateway sent them, no key for a field the gateway left out or sent as
// null, and no outputSchema for a schema AddTool refuses. For each schema in
// the form the bridge's client decoded it, a real go-sdk server shows that
// the mirror keeps exactly the schemas AddTool takes.
func TestMCPProxyMirrorsToolFields(t *testing.T) {
	cases := []struct {
		name, tool string
		// gateway is the tool as the gateway lists it. harness is the tool
		// the harness lists, or empty when it is the gateway's unchanged.
		gateway, harness string
	}{
		{name: "title, annotations and an object output schema reach the harness", tool: "full",
			gateway: `{"name":"full","title":"Add numbers","description":"adds","inputSchema":{"type":"object"},` +
				`"annotations":{"title":"Adder","readOnlyHint":true,"openWorldHint":false},` +
				`"outputSchema":{"type":"object","properties":{"sum":{"type":"number"}},"required":["sum"]}}`},
		{name: "null annotations and no output schema leave both keys out", tool: "bare",
			gateway: `{"name":"bare","description":"plain","inputSchema":{"type":"object"},"annotations":null}`,
			harness: `{"name":"bare","description":"plain","inputSchema":{"type":"object"}}`},
		{name: "an output schema that is a string is dropped", tool: "out_string",
			gateway: `{"name":"out_string","inputSchema":{"type":"object"},"outputSchema":"object"}`,
			harness: `{"name":"out_string","inputSchema":{"type":"object"}}`},
		{name: "an output schema that is an array is dropped", tool: "out_array",
			gateway: `{"name":"out_array","inputSchema":{"type":"object"},"outputSchema":[{"type":"object"}]}`,
			harness: `{"name":"out_array","inputSchema":{"type":"object"}}`},
		{name: "an object output schema without a type is dropped", tool: "out_untyped",
			gateway: `{"name":"out_untyped","inputSchema":{"type":"object"},"outputSchema":{"properties":{}}}`,
			harness: `{"name":"out_untyped","inputSchema":{"type":"object"}}`},
		{name: "an output schema of type array is dropped", tool: "out_array_type",
			gateway: `{"name":"out_array_type","inputSchema":{"type":"object"},"outputSchema":{"type":"array"}}`,
			harness: `{"name":"out_array_type","inputSchema":{"type":"object"}}`},
		{name: "an output schema that is a boolean is dropped", tool: "out_bool",
			gateway: `{"name":"out_bool","inputSchema":{"type":"object"},"outputSchema":true}`,
			harness: `{"name":"out_bool","inputSchema":{"type":"object"}}`},
		{name: "an output schema whose type is a list is dropped", tool: "out_type_list",
			gateway: `{"name":"out_type_list","inputSchema":{"type":"object"},"outputSchema":{"type":["object","null"]}}`,
			harness: `{"name":"out_type_list","inputSchema":{"type":"object"}}`},
	}
	var tools []string
	for _, tc := range cases {
		tools = append(tools, tc.gateway)
	}
	g := listingGateway(t, tools, "")
	store := proxyTestStore(t, g.fakeGateway)
	ctx, p, harness := buildProxyFor(t, g.fakeGateway, store, "demo")

	listed, err := harness.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("harness list: %v", err)
	}
	mirrored := map[string]string{}
	for _, tl := range listed.Tools {
		mirrored[tl.Name] = canonJSON(t, jsonOf(t, tl))
	}
	decoded, ok := p.listAllTools(ctx)
	if !ok {
		t.Fatal("the bridge could not list the gateway")
	}
	fromGateway := map[string]*mcp.Tool{}
	for _, tl := range decoded {
		fromGateway[tl.Name] = tl
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			listedTool := tc.harness
			if listedTool == "" {
				listedTool = tc.gateway
			}
			if got, want := mirrored[tc.tool], canonJSON(t, listedTool); got != want {
				t.Errorf("harness lists %s, want %s", got, want)
			}
			tl := fromGateway[tc.tool]
			if tl == nil || tl.OutputSchema == nil {
				return
			}
			if kept, took := mirrorTool(tl).OutputSchema != nil, addToolTakes(tl.OutputSchema); kept != took {
				t.Errorf("mirror keeps the output schema %T %v: %v, AddTool takes it: %v", tl.OutputSchema, tl.OutputSchema, kept, took)
			}
		})
	}
}

// TestMCPProxyReconcileReaddsChangedTool pins that a change to only the
// title, only one annotation or only the output schema changes the mirror's
// digest, so reconcile adds the tool again and the harness lists the change.
func TestMCPProxyReconcileReaddsChangedTool(t *testing.T) {
	const name = "everything__add"
	full := func() *mcp.Tool {
		return &mcp.Tool{Name: name, Title: "Add", InputSchema: map[string]any{"type": "object"},
			Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true},
			OutputSchema: map[string]any{"type": "object"}}
	}
	for _, tc := range []struct {
		name   string
		change func(*mcp.Tool)
	}{
		{"title", func(tl *mcp.Tool) { tl.Title = "Add numbers" }},
		{"one annotation", func(tl *mcp.Tool) {
			tl.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
		}},
		{"output schema", func(tl *mcp.Tool) {
			tl.OutputSchema = map[string]any{"type": "object", "required": []any{"sum"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			srv := mcp.NewServer(&mcp.Implementation{Name: "straza", Version: version.Version}, nil)
			p := newMCPProxy(srv)
			p.reconcile([]*mcp.Tool{full()})
			before := p.registered[name]

			ct, st := mcp.NewInMemoryTransports()
			go func() { _ = srv.Run(ctx, st) }()
			harness, err := mcp.NewClient(&mcp.Implementation{Name: "harness", Version: "test"}, nil).Connect(ctx, ct, nil)
			if err != nil {
				t.Fatalf("connect harness side: %v", err)
			}
			t.Cleanup(func() { _ = harness.Close() })

			changed := full()
			tc.change(changed)
			p.reconcile([]*mcp.Tool{changed})
			if p.registered[name] == before {
				t.Errorf("a changed %s kept the digest %s, so reconcile did not add the tool again", tc.name, before)
			}
			listed, err := harness.ListTools(ctx, nil)
			if err != nil || len(listed.Tools) != 1 {
				t.Fatalf("harness lists %+v, %v", listed, err)
			}
			if got, want := canonJSON(t, jsonOf(t, listed.Tools[0])), canonJSON(t, jsonOf(t, changed)); got != want {
				t.Errorf("harness lists %s, want %s", got, want)
			}
		})
	}
}

// TestMCPProxyOutputSchemaResultsPassThrough pins that a tool mirrored with
// an output schema still hands the harness the gateway's result unchanged,
// whether or not the result matches the schema. A Straza refusal is an
// isError result with one text item and no structured content.
func TestMCPProxyOutputSchemaResultsPassThrough(t *testing.T) {
	const tool = `{"name":"count","inputSchema":{"type":"object"},` +
		`"outputSchema":{"type":"object","properties":{"n":{"type":"number"}},"required":["n"]}}`
	for _, tc := range []struct{ name, result string }{
		{"an isError text result", `{"content":[{"type":"text","text":"Straza: this call was denied."}],"isError":true}`},
		{"structured content the schema refuses", `{"content":[{"type":"text","text":"{\"n\":\"many\"}"}],"structuredContent":{"n":"many"}}`},
		{"no structured content and no error", `{"content":[{"type":"text","text":"3"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := listingGateway(t, []string{tool}, tc.result)
			store := proxyTestStore(t, g.fakeGateway)
			ctx, _, harness := buildProxyFor(t, g.fakeGateway, store, "demo")

			listed, err := harness.ListTools(ctx, nil)
			if err != nil || len(listed.Tools) != 1 {
				t.Fatalf("harness lists %+v, %v", listed, err)
			}
			if got, want := canonJSON(t, jsonOf(t, listed.Tools[0])), canonJSON(t, tool); got != want {
				t.Fatalf("harness lists %s, want %s", got, want)
			}
			out, err := harness.CallTool(ctx, &mcp.CallToolParams{Name: "count", Arguments: map[string]any{}})
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if got, want := canonJSON(t, jsonOf(t, out)), canonJSON(t, tc.result); got != want {
				t.Errorf("harness received %s, want the gateway's %s", got, want)
			}
		})
	}
}

// addToolTakesInput reports whether go-sdk Server.AddTool takes a tool whose
// input schema is schema. AddTool panics on a schema it refuses, and the
// panic leaves took false.
func addToolTakesInput(schema any) (took bool) {
	defer func() { _ = recover() }()
	srv := mcp.NewServer(&mcp.Implementation{Name: "probe", Version: "1.0.0"}, nil)
	srv.AddTool(&mcp.Tool{Name: "probe", InputSchema: schema},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return nil, nil })
	return true
}

// TestMCPProxySkipsToolsAddToolRefuses pins a gateway that lists tools whose
// input schema go-sdk Server.AddTool panics on: the bridge keeps running,
// the harness lists the other tools, and the skipped tools are logged once
// over two resyncs. For each schema in the form the bridge's client decoded
// it, a real go-sdk server shows that the bridge skips exactly the tools
// AddTool refuses.
func TestMCPProxySkipsToolsAddToolRefuses(t *testing.T) {
	cases := []struct {
		tool, gateway string
		mirrored      bool
	}{
		{"good", `{"name":"good","inputSchema":{"type":"object","properties":{"a":{"type":"string"}}}}`, true},
		{"in_absent", `{"name":"in_absent","description":"no schema"}`, false},
		{"in_null", `{"name":"in_null","inputSchema":null}`, false},
		{"in_string", `{"name":"in_string","inputSchema":"object"}`, false},
		{"in_array", `{"name":"in_array","inputSchema":[{"type":"object"}]}`, false},
		{"in_bool", `{"name":"in_bool","inputSchema":true}`, false},
		{"in_number", `{"name":"in_number","inputSchema":7}`, false},
		{"in_untyped", `{"name":"in_untyped","inputSchema":{"properties":{}}}`, false},
		{"in_string_type", `{"name":"in_string_type","inputSchema":{"type":"string"}}`, false},
		{"in_type_list", `{"name":"in_type_list","inputSchema":{"type":["object","null"]}}`, false},
		{"last", `{"name":"last","inputSchema":{"type":"object"}}`, true},
	}
	var tools []string
	for _, tc := range cases {
		tools = append(tools, tc.gateway)
	}
	g := listingGateway(t, tools, "")
	store := proxyTestStore(t, g.fakeGateway)
	ctx, p, harness := buildProxyFor(t, g.fakeGateway, store, "demo")
	var mu sync.Mutex
	var logged []string
	// The bridge synced once while it was built, before a test can read its
	// log, so the skipped set is cleared to see the line the first sync logs.
	p.mu.Lock()
	p.logf = func(_, msg string) { mu.Lock(); logged = append(logged, msg); mu.Unlock() }
	p.skipped = ""
	p.mu.Unlock()
	p.resync(ctx)
	p.resync(ctx)

	listed, err := harness.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("harness list: %v", err)
	}
	var names []string
	for _, tl := range listed.Tools {
		names = append(names, tl.Name)
	}
	if got := strings.Join(names, " "); got != "good last" {
		t.Errorf("harness lists %q, want the two tools with an object schema", got)
	}
	mu.Lock()
	lines := strings.Join(logged, "\n")
	mu.Unlock()
	if n := strings.Count(lines, "skipped the tools "); n != 1 {
		t.Errorf("%d log lines for the skipped tools over two resyncs, want 1:\n%s", n, lines)
	}
	for _, tc := range cases {
		if named := strings.Contains(lines, `"`+tc.tool+`"`); named == tc.mirrored {
			t.Errorf("the log names %s: %v, want %v:\n%s", tc.tool, named, !tc.mirrored, lines)
		}
	}
	decoded, ok := p.listAllTools(ctx)
	if !ok {
		t.Fatal("the bridge could not list the gateway")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, tl := range decoded {
		if _, kept := p.registered[tl.Name]; kept != addToolTakesInput(tl.InputSchema) {
			t.Errorf("the bridge mirrors %s with the input schema %T %v: %v, and AddTool takes it: %v", tl.Name, tl.InputSchema, tl.InputSchema, kept, !kept)
		}
	}
}

// TestMCPProxyDropsToolWhoseInputSchemaTurnsBad pins a mirrored tool whose
// input schema stops being an object schema: reconcile drops it from the
// harness and keeps the tool beside it.
func TestMCPProxyDropsToolWhoseInputSchemaTurnsBad(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := mcp.NewServer(&mcp.Implementation{Name: "straza", Version: version.Version}, nil)
	p := newMCPProxy(srv)
	object := map[string]any{"type": "object"}
	p.reconcile([]*mcp.Tool{{Name: "stays", InputSchema: object}, {Name: "turns", InputSchema: object}})

	ct, st := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, st) }()
	harness, err := mcp.NewClient(&mcp.Implementation{Name: "harness", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("connect harness side: %v", err)
	}
	t.Cleanup(func() { _ = harness.Close() })

	p.reconcile([]*mcp.Tool{{Name: "stays", InputSchema: object}, {Name: "turns"}})
	listed, err := harness.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 1 || listed.Tools[0].Name != "stays" {
		t.Fatalf("harness lists %+v, %v, want the tool stays alone", listed, err)
	}
}
