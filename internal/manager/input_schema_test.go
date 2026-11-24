package manager

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/store"
)

// inputSchemas rewrites the input schemas a go-sdk server lists, by tool
// name, because the SDK's own AddTool refuses to register the schemas this
// file tests. A schema is given as JSON text, and "absent" lists the tool
// with a null schema.
type inputSchemas struct {
	mu     sync.Mutex
	byTool map[string]string
}

// set lists the tool name with the schema text from the next listing on.
func (s *inputSchemas) set(name, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byTool[name] = text
}

// install makes srv list its tools with the schemas of s.
func (s *inputSchemas) install(t *testing.T, srv *mcp.Server) {
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			list, ok := res.(*mcp.ListToolsResult)
			if !ok {
				return res, err
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			out := &mcp.ListToolsResult{NextCursor: list.NextCursor}
			for _, tl := range list.Tools {
				c := *tl
				if text, has := s.byTool[tl.Name]; has {
					c.InputSchema = nil
					if text != "absent" {
						if uErr := json.Unmarshal([]byte(text), &c.InputSchema); uErr != nil {
							t.Errorf("%s: %v", text, uErr)
						}
					}
				}
				out.Tools = append(out.Tools, &c)
			}
			return out, err
		}
	})
}

// TestInventoryListsObjectInputSchemasOnly pins which tools of a server the
// inventory takes by their input schema, read off the wire: a missing
// schema and an object schema with no type are listed as object schemas,
// and a schema that is no JSON object, or of another type, leaves its tool
// out. The left-out tools are logged in one Warn line over two probes. A
// tool whose schema turns bad later is left out too, which reads as drift,
// and logs the new set once.
func TestInventoryListsObjectInputSchemasOnly(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name, schema string
		listed       string // the schema the inventory lists, or "" for a tool left out
	}{
		{"object", `{"type":"object","properties":{"a":{"type":"string"}}}`, `{"properties":{"a":{"type":"string"}},"type":"object"}`},
		{"absent", `absent`, `{"type":"object"}`},
		{"untyped", `{"properties":{"a":{"type":"string"}},"required":["a"]}`, `{"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`},
		{"empty", `{}`, `{"type":"object"}`},
		{"text", `{"type":"string"}`, ""},
		{"list", `{"type":["object","null"]}`, ""},
		{"nulltype", `{"type":null}`, ""},
		{"word", `"object"`, ""},
		{"array", `[{"type":"object"}]`, ""},
		{"yes", `true`, ""},
		{"number", `7`, ""},
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "schemas", Version: "1.0.0"}, nil)
	schemas := &inputSchemas{byTool: map[string]string{}}
	for _, row := range rows {
		srv.AddTool(&mcp.Tool{Name: row.name, InputSchema: map[string]any{"type": "object"}},
			func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			})
		schemas.set(row.name, row.schema)
	}
	schemas.install(t, srv)
	up := newViewsUpstream(t, srv)
	mgr, sink, logs := viewsManager(t)
	ctx := context.Background()
	if _, err := mgr.Install(ctx, viewsManifest(t, "schemas", up.URL, `["*"]`, false), store.AppSourceAPI); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, mgr, "schemas", StatusRunning)
	v, _ := mgr.HealthCheckOne(ctx, "schemas")

	got := map[string]string{}
	for _, tl := range v.Tools {
		b, err := json.Marshal(tl.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		got[tl.Name] = string(b)
	}
	for _, row := range rows {
		if got[row.name] != row.listed {
			t.Errorf("tool %s with the schema %s is listed with %q, want %q", row.name, row.schema, got[row.name], row.listed)
		}
	}
	const line = "msg=\"manager: tools of an MCP server are not listed, because the input schema"
	out := logs.String()
	if n := strings.Count(out, line); n != 1 {
		t.Errorf("%d Warn lines for the left-out tools over two probes, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "app=schemas") || !strings.Contains(out, "tool=\"array, list, nulltype, number, text, word, yes\"") {
		t.Errorf("the Warn line does not name the server and the left-out tools:\n%s", out)
	}
	if v.Status != StatusRunning || sink.count("straza.apps.drift") != 0 {
		t.Errorf("status = %s with %d drift events, want a running server and no drift", v.Status, sink.count("straza.apps.drift"))
	}

	schemas.set("object", `{"type":"string"}`)
	v, _ = mgr.HealthCheckOne(ctx, "schemas")
	if slices.ContainsFunc(v.Tools, func(tl *mcp.Tool) bool { return tl.Name == "object" }) {
		t.Errorf("the tool whose schema turned into a string schema is still listed")
	}
	if v.Status != StatusDegraded || !strings.Contains(v.Detail, "object") || sink.count("straza.apps.drift") != 1 {
		t.Errorf("status = %s (%s) with %d drift events, want one drift that names the removed tool", v.Status, v.Detail, sink.count("straza.apps.drift"))
	}
	if n := strings.Count(logs.String(), line); n != 2 {
		t.Errorf("%d Warn lines after the left-out set changed, want 2:\n%s", n, logs.String())
	}
}
