package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/store"
)

// TestSchemaMismatch pins the argument check an approval-gated call passes
// before anyone is asked: a definite violation of the schema the server
// published is reported, and a schema the check cannot use, or that points
// outside itself, lets the call through as before.
func TestSchemaMismatch(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"type":"string"}`))
	}))
	t.Cleanup(remote.Close)

	strict := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"roleOid": map[string]any{"type": "string"}, "roleName": map[string]any{"type": "string"}},
		"required":             []any{"roleOid", "roleName"},
		"additionalProperties": false,
	}
	rows := []struct {
		name   string
		schema any
		args   string
		want   string // a fragment of the reported mismatch; empty means the call passes
	}{
		{"valid arguments pass", strict, `{"roleOid":"r1","roleName":"Finance"}`, ""},
		{"missing required property", strict, `{"roleOid":"r1"}`, "roleName"},
		{"wrong type", strict, `{"roleOid":"r1","roleName":7}`, "roleName"},
		{"property the schema forbids", strict, `{"roleOid":"r1","roleName":"F","extra":1}`, "extra"},
		{"no arguments against required properties", strict, ``, "roleOid"},
		{"array where an object is expected", strict, `[1,2]`, "object"},
		{"no arguments and nothing required", map[string]any{"type": "object"}, ``, ""},
		{"draft-07 schema is checked", map[string]any{
			"$schema": "http://json-schema.org/draft-07/schema#", "type": "object", "required": []any{"path"},
		}, `{}`, "path"},
		{"nil schema passes", nil, `{"anything":1}`, ""},
		{"remote $ref passes without a fetch", map[string]any{
			"type": "object", "properties": map[string]any{"x": map[string]any{"$ref": remote.URL + "/x.json"}}, "required": []any{"x"},
		}, `{}`, ""},
		{"file $ref passes without a read", map[string]any{
			"type": "object", "properties": map[string]any{"x": map[string]any{"$ref": "file:///etc/hostname"}}, "required": []any{"x"},
		}, `{}`, ""},
		{"draft-04 boolean exclusiveMaximum passes", map[string]any{
			"type": "object", "properties": map[string]any{"n": map[string]any{"type": "number", "maximum": 5, "exclusiveMaximum": true}}, "required": []any{"n"},
		}, `{}`, ""},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got := schemaMismatch(row.schema, json.RawMessage(row.args))
			if row.want == "" && got != "" {
				t.Fatalf("schemaMismatch = %q, want the call to pass", got)
			}
			if row.want != "" && !strings.Contains(got, row.want) {
				t.Fatalf("schemaMismatch = %q, want a mismatch naming %q", got, row.want)
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the check fetched a remote $ref %d times, want none", n)
	}
}

// argsGatePolicy gates grant and loose behind an approval and lets plain run.
const argsGatePolicy = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: arg-gates}
spec:
  match: {roles: [dev]}
  rules:
    - id: approve-grant
      tools: [mcp.call]
      apps: [argapp]
      toolNames: {allow: ["grant", "loose"]}
      effect: allow
      mode: approve
      approve: {roles: [sec-approvers], timeoutSeconds: 60}
    - id: allow-plain
      tools: [mcp.call]
      apps: [argapp]
      toolNames: {allow: ["plain"]}
      effect: allow
`

// startArgsUpstream serves three tools that never check their own arguments:
// grant and plain publish a strict schema, loose a draft-04 one the check
// cannot read. Each answers "<name> ran", so a test sees which calls arrived.
func startArgsUpstream(t *testing.T) *gatewayUpstream {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "args-upstream", Version: "1.0.0"}, nil)
	strict := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"roleOid": map[string]any{"type": "string"}, "roleName": map[string]any{"type": "string"}},
		"required":             []any{"roleOid", "roleName"},
		"additionalProperties": false,
	}
	draft04 := map[string]any{
		"type":       "object",
		"properties": map[string]any{"n": map[string]any{"type": "number", "maximum": 5, "exclusiveMaximum": true}},
		"required":   []any{"n"},
	}
	ran := func(name string) mcp.ToolHandler {
		return func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: name + " ran"}}}, nil
		}
	}
	srv.AddTool(&mcp.Tool{Name: "grant", InputSchema: strict}, ran("grant"))
	srv.AddTool(&mcp.Tool{Name: "plain", InputSchema: strict}, ran("plain"))
	srv.AddTool(&mcp.Tool{Name: "loose", InputSchema: draft04}, ran("loose"))
	u := &gatewayUpstream{Server: httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))}
	t.Cleanup(u.Close)
	return u
}

// TestGatewayArgumentsCheckedBeforeApproval drives the gateway with the real
// approval service: an approval-gated call whose arguments break the tool's
// schema is refused at once with one deny record and no approval, a valid one
// is still held, and calls with no approval step reach the server unchecked.
func TestGatewayArgumentsCheckedBeforeApproval(t *testing.T) {
	// Serial: it lowers the package knob gatewayHoldCap for its run.
	old := gatewayHoldCap
	gatewayHoldCap = 50 * time.Millisecond
	t.Cleanup(func() { gatewayHoldCap = old })
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedGatewayUser(t, app, "kim", "dev")
	installEchoApp(t, app, startArgsUpstream(t), "argapp", 0)
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{Name: "arg-gates", Status: "active", YAMLSource: argsGatePolicy}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}
	tok, sid := gatewaySession(t, base, "kim")

	call := func(tool string, args map[string]any) string {
		t.Helper()
		_, body, raw := mcpCall(t, base, tok, "tools/call", map[string]any{"name": tool, "arguments": args})
		res, _ := body["result"].(map[string]any)
		content, _ := res["content"].([]any)
		if len(content) == 0 {
			t.Fatalf("%s answered %s, want a tool result", tool, raw)
		}
		first, _ := content[0].(map[string]any)
		text, _ := first["text"].(string)
		return text
	}
	pending := func() int {
		t.Helper()
		recs, err := app.store.Approvals().List(ctx, string(approval.StatePending))
		if err != nil {
			t.Fatal(err)
		}
		return len(recs)
	}

	text := call("argapp__grant", map[string]any{"roleOid": "r1", justificationField: "why"})
	if !strings.HasPrefix(text, "Straza checked the arguments against argapp__grant's input schema") || !strings.Contains(text, "roleName") {
		t.Fatalf("refusal = %q, want the schema sentence naming roleName", text)
	}
	if n := pending(); n != 0 {
		t.Fatalf("pending approvals after a mismatched call = %d, want 0", n)
	}
	rec := awaitMCPRecords(t, app, 1, func(d map[string]any) bool {
		reason, _ := d["reason"].(string)
		return d["toolName"] == "grant" && strings.HasPrefix(reason, "Straza checked")
	})[0]
	want := map[string]any{
		"user": kim.ID, "session": sid, "app": "argapp", "effect": "deny",
		"ruleId": "approve-grant", "setName": "arg-gates", "reason": text, "arguments": `{"roleOid":"r1"}`,
	}
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("record %s = %v, want %v", k, rec[k], v)
		}
	}

	if text := call("argapp__grant", map[string]any{"roleOid": "r1", "roleName": "Finance", justificationField: "why"}); !strings.Contains(text, "approval pending") {
		t.Errorf("valid gated call = %q, want it held for approval", text)
	}
	if text := call("argapp__loose", map[string]any{justificationField: "why"}); !strings.Contains(text, "approval pending") {
		t.Errorf("call of a tool whose schema the check cannot read = %q, want it held as before", text)
	}
	if n := pending(); n != 2 {
		t.Errorf("pending approvals = %d, want 2 (the valid call and the unreadable schema)", n)
	}
	if text := call("argapp__plain", map[string]any{}); text != "plain ran" {
		t.Errorf("ungated call with mismatched arguments = %q, want it to reach the server", text)
	}
}
