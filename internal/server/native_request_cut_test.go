package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
)

// TestApprovalRequestCutsLongNames pins that approval_request quotes a long
// name it was asked about cut, under the deny local default: the server of
// a described mcp.call, the tool of a described call that is not one, and
// the tool name of a described other tool, which the engine's sentence
// names twice. The answer stays small, and the one record carries the same
// sentence as its reason (boundedRecord). The sentence of the other tool
// quotes two names, so its bound is 8 KiB, not 4.
func TestApprovalRequestCutsLongNames(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) { c.Governance.LocalToolDefault = config.EffectDeny })
	seedGatewayUser(t, app, "kim", "dev")
	catActivate(t, app, "ask", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: ask}
spec:
  rules:
    - id: ask
      tools: [mcp.call]
      apps: [straza]
      toolNames: {allow: [approval_request]}
      effect: allow
`)
	tok, sid := gatewaySession(t, base, "kim")
	long := strings.Repeat("<", 64<<10)
	cut := strings.Repeat("<", 256) + "... (65536 bytes)"

	rows := []struct {
		name   string
		action map[string]any
		want   []string // parts of the sentence, each holding the cut name
		max    int      // the bound on the answer and on the record apart from arguments
	}{
		{"a long server of an mcp.call", map[string]any{"tool": "mcp.call", "app": long, "tool_name": "echo", "args": map[string]any{}},
			[]string{unknownTool(cut, "echo")}, 4 << 10},
		{"a long tool that is not an mcp.call", map[string]any{"tool": long, "app": "a", "tool_name": "b"},
			[]string{`Straza: denied by policy. The described call would be denied (Straza: denied, because the tool "` + cut + `" is not one Straza knows`}, 4 << 10},
		{"a long tool name of an other tool", map[string]any{"tool": "other", "app": "a", "tool_name": long},
			[]string{"would be denied (Straza: " + cut + " is a harness tool Straza does not classify", "toolNames: {allow: [" + cut + "]}"}, 8 << 10},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			text := refusedText(t, base, tok, "straza__approval_request", map[string]any{"action": row.action, "reason": "cut"}, row.max)
			for _, part := range row.want {
				if !strings.Contains(text, part) {
					t.Errorf("answer %q lacks %q", text, part)
				}
			}
			if strings.Contains(text, strings.Repeat("<", 257)) {
				t.Errorf("answer quotes the name whole: %d bytes", len(text))
			}
			sel := func(d map[string]any) bool { return d["toolName"] == "approval_request" && d["reason"] == text }
			rec := awaitMCPRecords(t, app, 1, sel)[0]
			if rec["effect"] != "deny" || rec["ruleId"] != "ask" || rec["session"] != sid {
				t.Errorf("record = effect %v, ruleId %v, session %v, want deny by ask in %s", rec["effect"], rec["ruleId"], rec["session"], sid)
			}
			boundedRecord(t, app, sel, row.max)
		})
	}
}

// refusedText makes one tools/call of name with args and returns the text
// of its tool error, failing unless the answer is one under max bytes.
func refusedText(t *testing.T, base, tok, name string, args map[string]any, max int) string {
	t.Helper()
	_, res, raw := mcpCall(t, base, tok, "tools/call", map[string]any{"name": name, "arguments": args})
	result, _ := res["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if result["isError"] != true || len(content) != 1 {
		t.Fatalf("answer is %d bytes and not one tool error", len(raw))
	}
	if len(raw) >= max {
		t.Errorf("answer is %d bytes, want under %d", len(raw), max)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	return text
}

// boundedRecord fails unless the one straza.audit.mcp record sel matches is
// under max bytes apart from arguments, carries arguments cut at their own
// 8 KiB cap, and is under 64 KiB whole.
func boundedRecord(t *testing.T, app *App, sel func(map[string]any) bool, max int) {
	t.Helper()
	stored := rawMCPRecord(t, app, sel)
	if len(stored) >= 64<<10 {
		t.Errorf("the record is %d bytes, want under 64 KiB", len(stored))
	}
	var ce map[string]any
	if err := json.Unmarshal([]byte(stored), &ce); err != nil {
		t.Fatal(err)
	}
	data := ce["data"].(map[string]any)
	if args, _ := data["arguments"].(string); len(args) > auditArgsMax || data["argumentsTruncated"] != true {
		t.Errorf("record arguments are %d bytes with argumentsTruncated %v, want at most %d and true", len(args), data["argumentsTruncated"], auditArgsMax)
	}
	delete(data, "arguments")
	if rest, _ := json.Marshal(ce); len(rest) >= max {
		t.Errorf("the record apart from arguments is %d bytes, want under %d", len(rest), max)
	}
}

// TestArgumentsMismatchCut pins the cut of the schema check's detail, which
// quotes the instance that broke the schema: a gated call whose argument is
// an array of 65536 < is refused, through a direct tools/call and through
// approval_request, with the sentence and at most schemaDetailMax bytes of
// the detail. Each leaves one small record.
func TestArgumentsMismatchCut(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	seedGatewayUser(t, app, "kim", "dev")
	installEchoApp(t, app, startGatewayUpstream(t), "schapp", 0)
	catActivate(t, app, "sch-policy", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: sch-policy}
spec:
  rules:
    - id: ask
      tools: [mcp.call]
      apps: [straza]
      toolNames: {allow: [approval_request]}
      effect: allow
    - id: echo-ticket
      tools: [mcp.call]
      apps: [schapp]
      toolNames: {allow: ["echo"]}
      effect: allow
      mode: approve
      approve: {class: ticket, roles: [sec-approvers], ticketTTLSeconds: 86400, grantTTLSeconds: 3600}
`)
	tok, sid := gatewaySession(t, base, "kim")
	args := map[string]any{"text": []string{strings.Repeat("<", 64<<10)}}
	const head = "Straza checked the arguments against schapp__echo's input schema before asking for approval, and they do not match: "

	rows := []struct {
		name, tool string
		args       map[string]any
		sel        func(text string) func(map[string]any) bool
	}{
		{"a direct call", "schapp__echo", args, func(string) func(map[string]any) bool {
			return func(d map[string]any) bool {
				return d["app"] == "schapp" && d["toolName"] == "echo" && d["ruleId"] == "echo-ticket"
			}
		}},
		{"a call approval_request describes", "straza__approval_request",
			map[string]any{"action": map[string]any{"tool": "mcp.call", "app": "schapp", "tool_name": "echo", "args": args}},
			func(text string) func(map[string]any) bool {
				return func(d map[string]any) bool { return d["toolName"] == "approval_request" && d["reason"] == text }
			}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			text := refusedText(t, base, tok, row.tool, row.args, 8<<10)
			if !strings.HasPrefix(text, head) || !strings.Contains(text, " bytes). Fix the arguments and call the tool again.") || strings.Contains(text, strings.Repeat("<", schemaDetailMax)) {
				t.Errorf("answer = %.300q..., want the sentence with the detail cut at %d bytes", text, schemaDetailMax)
			}
			sel := row.sel(text)
			rec := awaitMCPRecords(t, app, 1, sel)[0]
			if rec["effect"] != "deny" || rec["reason"] != text || rec["session"] != sid {
				t.Errorf("record = effect %v, session %v, want a deny in %s whose reason is the answer", rec["effect"], rec["session"], sid)
			}
			boundedRecord(t, app, sel, 8<<10)
		})
	}
}
