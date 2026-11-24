package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestArgumentsMismatchRecordQuotesNoArguments pins the record of the
// schema check's refusal: a gated straza__draft_submit whose documents are a
// token, called directly or described by approval_request, answers the
// caller with the validator's detail, which quotes the token, and writes
// exactly one deny record whose reason names the tool without the detail, so
// the token appears nowhere in the stored record.
func TestArgumentsMismatchRecordQuotesNoArguments(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t)
	ctx := context.Background()
	src := "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata: {name: b366}\nspec:\n  match: {users: [direct, asker]}\n  rules:\n" +
		"    - id: gate\n      tools: [mcp.call]\n      apps: [straza]\n      toolNames: {allow: [draft_submit]}\n      effect: allow\n      mode: approve\n" +
		"      approve: {class: ticket, roles: [sec], ticketTTLSeconds: 86400, grantTTLSeconds: 3600}\n" +
		"    - id: ask\n      tools: [mcp.call]\n      apps: [straza]\n      toolNames: {allow: [approval_request]}\n      effect: allow\n"
	if _, err := r.app.store.Policies().Create(ctx, store.PolicySet{Name: "b366", Status: "active", YAMLSource: src}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}
	broken := map[string]any{"documents": reviewSecret}
	const head = "Straza checked the arguments against straza__draft_submit's input schema before asking for approval, and they do not match: "
	const reason = "Straza checked the arguments against straza__draft_submit's input schema before asking for approval, and they do not match. Fix the arguments and call the tool again."

	rows := []struct {
		name, user, tool string
		args             map[string]any
		recTool, rule    string
	}{
		{"a gated draft_submit", "direct", "straza__draft_submit", broken, "draft_submit", "gate"},
		{"an approval_request that describes one", "asker", "straza__approval_request",
			map[string]any{"action": map[string]any{"tool": "mcp.call", "app": "straza", "tool_name": "draft_submit", "args": broken}},
			"approval_request", "ask"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			u, tok := r.agent(t, row.user)
			text, isErr, _ := callTool(t, r.base, tok, row.tool, row.args)
			if !isErr || !strings.HasPrefix(text, head) || !strings.Contains(text, "/properties/documents") || !strings.Contains(text, reviewSecret) {
				t.Fatalf("answer = %q, want the schema sentence with the validator's detail", text)
			}
			sel := func(d map[string]any) bool {
				return d["user"] == u.ID && (d["toolName"] == "draft_submit" || d["toolName"] == "approval_request")
			}
			rec := awaitMCPRecords(t, r.app, 1, sel)[0]
			want := map[string]any{"toolName": row.recTool, "effect": "deny", "ruleId": row.rule, "setName": "b366", "reason": reason}
			for k, v := range want {
				if rec[k] != v {
					t.Errorf("record %s = %v, want %v", k, rec[k], v)
				}
			}
			if raw := rawMCPRecord(t, r.app, sel); strings.Contains(raw, reviewSecret) {
				t.Errorf("the stored record holds the token: %s", raw)
			}
		})
	}
}

// TestMismatchRecord pins the reason approval_request's record keeps for its
// refusal: the schema check's refusal of the call it describes keeps the
// sentence without the detail, even when the detail repeats the sentence's
// words, and every other refusal is kept whole.
func TestMismatchRecord(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name, msg, want string
	}{
		{"the schema refusal", fmt.Sprintf(argumentsMismatchMsg, "straza__draft_submit", "type: tok has type \"string\""),
			fmt.Sprintf(argumentsMismatchRecord, "straza__draft_submit")},
		{"a detail that repeats the sentence", fmt.Sprintf(argumentsMismatchMsg, "a__b", fmt.Sprintf(argumentsMismatchMsg, "tok", "tok")),
			fmt.Sprintf(argumentsMismatchRecord, "a__b")},
		{"another refusal", nativeMCPNotConcreteMsg, nativeMCPNotConcreteMsg},
		{"a policy deny", "Straza: denied by policy. The described call would be denied (no rule allows it); an approval ticket cannot override a deny",
			"Straza: denied by policy. The described call would be denied (no rule allows it); an approval ticket cannot override a deny"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if got := mismatchRecord(row.msg); got != row.want {
				t.Errorf("mismatchRecord = %q, want %q", got, row.want)
			}
		})
	}
}
