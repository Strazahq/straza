package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/manager"
)

// mcpRequest sends one request of method to the gateway endpoint url with
// header, body and the session token when one is given, and returns the
// status and the raw answer.
func mcpRequest(t *testing.T, method, url, token string, header http.Header, body string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// apiErrorOf returns the error sentence of an apiError answer, or empty when
// raw is not one.
func apiErrorOf(raw []byte) string {
	var m map[string]string
	_ = json.Unmarshal(raw, &m)
	return m["error"]
}

// fieldsOutput is the output schema the fields upstream's tool linked
// publishes.
func fieldsOutput() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "integer"}}}
}

// startFieldsUpstream runs an MCP server with three tools. linked links
// panelURI under _meta.ui.resourceUri and legacyURI under the flat key,
// carries com.example/flag beside them, and publishes fieldsOutput and a
// read-only annotation. bare carries none of these. badout publishes an
// output schema whose property is not an object. Both views are ui://
// resources of the MCP Apps media type.
func startFieldsUpstream(t *testing.T) *gatewayUpstream {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "fields-upstream", Version: "1.0.0"}, nil)
	done := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	}
	srv.AddTool(&mcp.Tool{
		Name: "linked", InputSchema: map[string]any{"type": "object"}, OutputSchema: fieldsOutput(),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Meta:        mcp.Meta{"ui": map[string]any{"resourceUri": panelURI}, "ui/resourceUri": legacyURI, "com.example/flag": true},
	}, done)
	srv.AddTool(&mcp.Tool{Name: "bare", InputSchema: map[string]any{"type": "object"}}, done)
	srv.AddTool(&mcp.Tool{Name: "badout", InputSchema: map[string]any{"type": "object"},
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"a": true}}}, done)
	for _, uri := range []string{panelURI, legacyURI} {
		srv.AddResource(&mcp.Resource{URI: uri, Name: path.Base(uri), MIMEType: manager.ViewMIMEType},
			func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
					URI: req.Params.URI, MIMEType: manager.ViewMIMEType, Text: viewHTML(req.Params.URI),
				}}}, nil
			})
	}
	up := &gatewayUpstream{Server: httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))}
	t.Cleanup(up.Close)
	return up
}

// listedTools returns the tools of a tools/list answer by name, each as its
// raw JSON object decoded.
func listedTools(t *testing.T, raw []byte) map[string]map[string]any {
	t.Helper()
	var res map[string]any
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("answer %s is not JSON: %v", raw, err)
	}
	out := map[string]map[string]any{}
	for _, tl := range resultOf(t, res, raw)["tools"].([]any) {
		m := tl.(map[string]any)
		out[m["name"].(string)] = m
	}
	return out
}

// TestGatewayToolFields pins the fields of a listed tool on both endpoints:
// outputSchema and annotations as the upstream sent them and no key when it
// sent none or sent a malformed output schema, and _meta verbatim on the
// server endpoint and without the two view link keys on /mcp, with views on
// and off. The manager's cached tool keeps its links.
func TestGatewayToolFields(t *testing.T) {
	t.Parallel()
	flagOnly := map[string]any{"com.example/flag": true}
	rows := []struct {
		name       string
		views      bool
		serverMeta map[string]any
	}{
		{"views on", true, map[string]any{"ui": map[string]any{"resourceUri": panelURI}, "ui/resourceUri": legacyURI, "com.example/flag": true}},
		{"views off", false, flagOnly},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			app, base := testApp(t)
			seedGatewayUser(t, app, "kim", "dev")
			installViewsApp(t, app, startFieldsUpstream(t), "fieldsapp", row.views, 0)
			if row.views {
				waitViews(t, app, "fieldsapp", 2)
			}
			tok, _ := gatewaySession(t, base, "kim")

			_, _, raw := mcpCall(t, base, tok, "tools/list", nil)
			combined := listedTools(t, raw)
			_, _, raw = mcpServerCall(t, base, "fieldsapp", tok, "tools/list", nil)
			own := listedTools(t, raw)
			for _, c := range []struct {
				endpoint             string
				linked, bare, badout map[string]any
				meta                 map[string]any
			}{
				{"/mcp", combined["fieldsapp__linked"], combined["fieldsapp__bare"], combined["fieldsapp__badout"], flagOnly},
				{"/mcp/fieldsapp", own["linked"], own["bare"], own["badout"], row.serverMeta},
			} {
				if c.linked == nil || c.bare == nil || c.badout == nil {
					t.Fatalf("%s lists linked %v, bare %v and badout %v, want all three", c.endpoint, c.linked, c.bare, c.badout)
				}
				if v, ok := c.badout["outputSchema"]; ok {
					t.Errorf("%s badout carries outputSchema = %v, want the malformed schema left out", c.endpoint, v)
				}
				if !reflect.DeepEqual(c.linked["outputSchema"], fieldsOutput()) {
					t.Errorf("%s linked outputSchema = %v, want the upstream's", c.endpoint, c.linked["outputSchema"])
				}
				if !reflect.DeepEqual(c.linked["annotations"], map[string]any{"readOnlyHint": true}) {
					t.Errorf("%s linked annotations = %v, want the upstream's", c.endpoint, c.linked["annotations"])
				}
				if !reflect.DeepEqual(c.linked["_meta"], c.meta) {
					t.Errorf("%s linked _meta = %v, want %v", c.endpoint, c.linked["_meta"], c.meta)
				}
				for _, key := range []string{"outputSchema", "annotations", "_meta"} {
					if v, ok := c.bare[key]; ok {
						t.Errorf("%s bare carries %s = %v, want no such key", c.endpoint, key, v)
					}
				}
			}

			if !row.views {
				return
			}
			// With views on and both views served, the manager's view holds
			// the cached tool itself, which the combined list must not write.
			v, _ := app.manager.View("fieldsapp")
			for _, tl := range v.Tools {
				if _, hasUI := tl.Meta["ui"]; tl.Name == "linked" && (!hasUI || tl.Meta["ui/resourceUri"] != legacyURI) {
					t.Errorf("the manager's cached linked _meta = %v, want both view links kept", tl.Meta)
				}
			}
		})
	}
}

// nestedSchema is an object schema whose walk reaches depth: the top
// schema and a chain of items maps below it.
func nestedSchema(depth int) map[string]any {
	top := map[string]any{"type": "object"}
	cur := top
	for range depth - 1 {
		next := map[string]any{}
		cur["items"] = next
		cur = next
	}
	return top
}

// wideSchema is an object schema of maps maps in all: the top schema, its
// properties map and maps-2 empty property schemas.
func wideSchema(maps int) map[string]any {
	props := map[string]any{}
	for i := range maps - 2 {
		props[fmt.Sprintf("p%d", i)] = map[string]any{}
	}
	return map[string]any{"type": "object", "properties": props}
}

// TestServedOutputSchema pins which output schemas a catalog lists, each
// given as the go-sdk client decodes it: a plain schema comes back as the
// same value, and a schema with a reference, an identifier, a pattern, a
// malformed keyword or past the walk's bounds comes back nil. A property
// named like a keyword and data under enum are not keywords. A schema of
// 40,000 patternProperties keys is refused at once.
func TestServedOutputSchema(t *testing.T) {
	t.Parallel()
	j := func(s string) any {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		return v
	}
	rows := []struct {
		name   string
		schema any
		kept   bool
	}{
		{"a bare object", j(`{"type":"object"}`), true},
		{"properties and required", j(`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`), true},
		{"nested objects and arrays", j(`{"type":"object","properties":{"list":{"type":"array","items":{"type":"object","properties":{"n":{"type":"integer"}}}}}}`), true},
		{"no additional properties", j(`{"type":"object","additionalProperties":false}`), true},
		{"empty properties and required", j(`{"type":"object","properties":{},"required":[]}`), true},
		{"properties named like keywords", j(`{"type":"object","properties":{"pattern":{"type":"string"},"$ref":{"type":"string"},"$id":{"type":"string"},"enum":{"type":"string"},"type":{"type":"string"},"required":{"type":"boolean"}}}`), true},
		{"a boolean subschema below the top", j(`{"type":"object","properties":{"a":{"type":"object","properties":{"b":true}}}}`), true},
		{"a type list on a property", j(`{"type":"object","properties":{"a":{"type":["string","null"]}}}`), true},
		{"enum data that looks like a keyword", j(`{"type":"object","properties":{"a":{"enum":[{"$ref":"x"}]}}}`), true},
		{"a format", j(`{"type":"object","properties":{"at":{"type":"string","format":"date-time"}}}`), true},
		{"if, then and else", j(`{"type":"object","if":{"properties":{"k":{"const":"a"}}},"then":{"required":["a"]},"else":{"required":["b"]}}`), true},
		{"allOf, anyOf and oneOf", j(`{"type":"object","allOf":[{"type":"object"}],"anyOf":[{"required":["a"]},{"required":["b"]}],"oneOf":[{"properties":{"a":{"type":"string"}}}]}`), true},
		{"a draft URL in $schema", j(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}`), true},
		{"$defs nothing references", j(`{"type":"object","$defs":{"name":{"type":"string"}}}`), true},
		{"32 levels deep", nestedSchema(32), true},
		{"2,000 maps", wideSchema(2000), true},

		{"no schema", nil, false},
		{"a string", j(`"object"`), false},
		{"an array", j(`[{"type":"object"}]`), false},
		{"an array type", j(`{"type":"array"}`), false},
		{"no type", j(`{"properties":{}}`), false},
		{"a type list at the top", j(`{"type":["object","null"]}`), false},
		{"a boolean top-level property", j(`{"type":"object","properties":{"a":true}}`), false},
		{"properties a string", j(`{"type":"object","properties":"x"}`), false},
		{"required a string", j(`{"type":"object","required":"a"}`), false},
		{"required a number", j(`{"type":"object","required":[1]}`), false},
		{"a nested required a string", j(`{"type":"object","properties":{"a":{"type":"object","required":"b"}}}`), false},
		{"an internal $ref", j(`{"type":"object","properties":{"a":{"$ref":"#/$defs/n"}},"$defs":{"n":{"type":"string"}}}`), false},
		{"a $ref to a missing definition", j(`{"type":"object","properties":{"a":{"$ref":"#/$defs/missing"}}}`), false},
		{"a $ref to an http URL", j(`{"type":"object","properties":{"a":{"$ref":"http://schemas.example/a.json"}}}`), false},
		{"a $ref loop", j(`{"type":"object","properties":{"a":{"$ref":"#"}}}`), false},
		{"a $dynamicRef", j(`{"type":"object","properties":{"a":{"$dynamicRef":"#node"}}}`), false},
		{"an $id at the top", j(`{"type":"object","$id":"https://schemas.example/out"}`), false},
		{"a nested $id", j(`{"type":"object","properties":{"a":{"$id":"a","type":"string"}}}`), false},
		{"an $anchor", j(`{"type":"object","properties":{"a":{"$anchor":"a","type":"string"}}}`), false},
		{"a pattern on a property", j(`{"type":"object","properties":{"phone":{"type":"string","pattern":"^\\d{3}\\-\\d{4}$"}}}`), false},
		{"patternProperties", j(`{"type":"object","patternProperties":{"^a":{"type":"string"}}}`), false},
		{"an unknown type name", j(`{"type":"object","properties":{"a":{"type":"strng"}}}`), false},
		{"an empty type list", j(`{"type":"object","properties":{"a":{"type":[]}}}`), false},
		{"a number type", j(`{"type":"object","properties":{"a":{"type":5}}}`), false},
		{"an empty enum", j(`{"type":"object","properties":{"a":{"enum":[]}}}`), false},
		{"an enum that is not an array", j(`{"type":"object","properties":{"a":{"enum":"x"}}}`), false},
		{"33 levels deep", nestedSchema(33), false},
		{"2,001 maps", wideSchema(2001), false},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			got := servedOutputSchema(row.schema)
			if !row.kept {
				if got != nil {
					t.Errorf("servedOutputSchema kept %v, want nil", got)
				}
				return
			}
			if got == nil || reflect.ValueOf(got).Pointer() != reflect.ValueOf(row.schema).Pointer() {
				t.Errorf("servedOutputSchema = %v, want the schema itself", got)
			}
		})
	}

	t.Run("40,000 patternProperties keys", func(t *testing.T) {
		t.Parallel()
		keys := map[string]any{}
		for i := range 40000 {
			keys[fmt.Sprintf("^k%05d-[a-z]+$", i)] = map[string]any{"type": "string"}
		}
		big := map[string]any{"type": "object", "patternProperties": keys}
		start := time.Now()
		got := servedOutputSchema(big)
		if took := time.Since(start); got != nil || took > 100*time.Millisecond {
			t.Errorf("servedOutputSchema kept %v after %v, want nil well under 100ms", got != nil, took)
		}
	})
}

// startCountingUpstream runs an MCP server whose one tool, plain, adds one
// to calls on each call and answers "plain: <text>".
func startCountingUpstream(t *testing.T, calls *atomic.Int32) *gatewayUpstream {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "counting-upstream", Version: "1.0.0"}, nil)
	type textArgs struct {
		Text string `json:"text"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "plain", Description: "plain"},
		func(_ context.Context, _ *mcp.CallToolRequest, a textArgs) (*mcp.CallToolResult, any, error) {
			calls.Add(1)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "plain: " + a.Text}}}, nil, nil
		})
	up := &gatewayUpstream{Server: httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))}
	t.Cleanup(up.Close)
	return up
}

// TestGatewayWithoutID pins that a JSON-RPC message whose id is absent or
// null is never run: on /mcp, on a server endpoint and on an unknown server
// name it gets 202 with no body, the upstream is not called and no record
// is written. A message with an id keeps its answer.
func TestGatewayWithoutID(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	var calls atomic.Int32
	seedGatewayUser(t, app, "kim", "dev")
	installViewsApp(t, app, startCountingUpstream(t, &calls), "viewapp", false, 0)
	catActivate(t, app, "views-policy", viewsPolicy("viewapp"))
	tok, _ := gatewaySession(t, base, "kim")

	type row struct{ name, path, body string }
	var rows []row
	for _, ep := range []struct{ path, tool string }{{"/mcp", "viewapp__plain"}, {"/mcp/viewapp", "plain"}, {"/mcp/nope", "plain"}} {
		call := `"method":"tools/call","params":{"name":"` + ep.tool + `","arguments":{"text":"x"}}}`
		rows = append(rows,
			row{"tools/call with no id on " + ep.path, ep.path, `{"jsonrpc":"2.0",` + call},
			row{"tools/call with id null on " + ep.path, ep.path, `{"jsonrpc":"2.0","id":null,` + call},
			row{"tools/list with no id on " + ep.path, ep.path, `{"jsonrpc":"2.0","method":"tools/list"}`},
			row{"an unknown notification on " + ep.path, ep.path, `{"jsonrpc":"2.0","method":"notifications/nope"}`},
			row{"notifications/initialized on " + ep.path, ep.path, `{"jsonrpc":"2.0","method":"notifications/initialized"}`},
		)
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if code, raw := mcpRequest(t, http.MethodPost, base+r.path, tok, nil, r.body); code != http.StatusAccepted || len(bytes.TrimSpace(raw)) != 0 {
				t.Errorf("answer = %d %q, want 202 and no body", code, raw)
			}
		})
	}

	// With an id, notifications/initialized still gets 202 and a posted
	// response still gets -32601.
	if code, raw := mcpRequest(t, http.MethodPost, base+"/mcp", tok, nil, `{"jsonrpc":"2.0","id":7,"method":"notifications/initialized"}`); code != http.StatusAccepted || len(bytes.TrimSpace(raw)) != 0 {
		t.Errorf("notifications/initialized with an id = %d %q, want 202 and no body", code, raw)
	}
	code, raw := mcpRequest(t, http.MethodPost, base+"/mcp", tok, nil, `{"jsonrpc":"2.0","id":8,"result":{}}`)
	var res map[string]any
	_ = json.Unmarshal(raw, &res)
	if c, _ := rpcErrorOf(t, res, raw); code != http.StatusOK || c != -32601 || res["id"] != float64(8) {
		t.Errorf("a posted response = %d %s, want 200 and -32601 with its id", code, raw)
	}

	// The control call runs once and leaves the one record of the tool.
	if _, _, raw := mcpCall(t, base, tok, "tools/call", map[string]any{"name": "viewapp__plain", "arguments": map[string]string{"text": "hi"}}); !strings.Contains(string(raw), "plain: hi") {
		t.Fatalf("control call = %s, want the upstream answer", raw)
	}
	awaitMCPRecords(t, app, 1, func(d map[string]any) bool { return d["toolName"] == "plain" })
	awaitMCPRecords(t, app, 0, func(d map[string]any) bool { return d["event"] == "tools.list" })
	if n := calls.Load(); n != 1 {
		t.Errorf("the upstream ran %d calls, want the control call only", n)
	}
}

// TestGatewayOrigin pins the Origin check of both MCP routes on POST, GET
// and DELETE: no Origin and the public URL's origin in other letter case
// pass, and a foreign origin and null get 403 with the sentence, before
// authentication, with or without a token, and leave no record.
func TestGatewayOrigin(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	seedGatewayUser(t, app, "kim", "dev")
	tok, _ := gatewaySession(t, base, "kim")
	rows := []struct {
		name, origin, token string
		refused             bool
	}{
		{"no Origin", "", tok, false},
		{"the public URL's origin in capitals", strings.ToUpper(base), tok, false},
		{"a foreign origin without a token", "https://evil.example", "", true},
		{"a foreign origin with a token", "https://evil.example", tok, true},
		{"the null origin", "null", "", true},
	}
	passed := map[string]int{http.MethodPost: http.StatusOK, http.MethodGet: http.StatusMethodNotAllowed, http.MethodDelete: http.StatusOK}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			header := http.Header{}
			if row.origin != "" {
				header.Set("Origin", row.origin)
			}
			for _, p := range []string{"/mcp", "/mcp/nope"} {
				for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
					body := ""
					if method == http.MethodPost {
						body = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
					}
					code, raw := mcpRequest(t, method, base+p, row.token, header, body)
					switch {
					case row.refused && (code != http.StatusForbidden || apiErrorOf(raw) != fmt.Sprintf(originRefusedMsg, row.origin)):
						t.Errorf("%s %s = %d %s, want 403 and the origin sentence", method, p, code, raw)
					case !row.refused && code != passed[method]:
						t.Errorf("%s %s = %d %s, want %d", method, p, code, raw, passed[method])
					}
				}
			}
		})
	}
	// Only the two passing lists on /mcp are recorded.
	awaitMCPRecords(t, app, 2, func(d map[string]any) bool { return d["event"] == "tools.list" })
}

// TestGatewayProtocolVersionHeader pins the MCP-Protocol-Version check of
// both MCP routes: no header and each version the gateway speaks pass, and
// any other value gets 400 with the sentence on POST, GET and DELETE, after
// authentication and before a message without an id is answered. An
// initialize answers whatever the header names.
func TestGatewayProtocolVersionHeader(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	seedGatewayUser(t, app, "kim", "dev")
	tok, _ := gatewaySession(t, base, "kim")
	spoken := []string{"", "2025-06-18", "2025-03-26", "2024-11-05"}
	long := strings.Repeat("9", 300)
	passed := map[string]int{http.MethodPost: http.StatusOK, http.MethodGet: http.StatusMethodNotAllowed, http.MethodDelete: http.StatusOK}
	for _, v := range append(slices.Clone(spoken), "2025-11-25", "garbage", long) {
		refused := !slices.Contains(spoken, v)
		header := http.Header{}
		if v != "" {
			header.Set("MCP-Protocol-Version", v)
		}
		for _, p := range []string{"/mcp", "/mcp/nope"} {
			for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
				body := ""
				if method == http.MethodPost {
					body = `{"jsonrpc":"2.0","id":1,"method":"ping"}`
				}
				code, raw := mcpRequest(t, method, base+p, tok, header, body)
				switch {
				case refused && (code != http.StatusBadRequest || apiErrorOf(raw) != fmt.Sprintf(badVersionMsg, cutUnresolved(v))):
					t.Errorf("%s %s with %.20q = %d %s, want 400 and the version sentence", method, p, v, code, raw)
				case !refused && code != passed[method]:
					t.Errorf("%s %s with %q = %d %s, want %d", method, p, v, code, raw, passed[method])
				}
			}
		}
	}

	foreign := http.Header{"Mcp-Protocol-Version": {"2025-11-25"}}
	code, raw := mcpRequest(t, http.MethodPost, base+"/mcp", tok, foreign,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`)
	var res map[string]any
	_ = json.Unmarshal(raw, &res)
	if code != http.StatusOK || resultOf(t, res, raw)["protocolVersion"] != "2025-06-18" {
		t.Errorf("initialize with a foreign header = %d %s, want 200 and 2025-06-18", code, raw)
	}
	if code, raw := mcpRequest(t, http.MethodPost, base+"/mcp", tok, foreign, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != http.StatusBadRequest {
		t.Errorf("a notification with a foreign header = %d %s, want 400", code, raw)
	}
	if code, raw := mcpRequest(t, http.MethodGet, base+"/mcp", "", foreign, ""); code != http.StatusUnauthorized {
		t.Errorf("a foreign header without a token = %d %s, want 401", code, raw)
	}
}
