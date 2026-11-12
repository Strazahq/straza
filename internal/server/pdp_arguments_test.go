package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// TestDecideRecordKeepsToolInput pins what the record of a decision on
// /v1/decide says about a call beyond its kind, the same way the client's
// own record does. It keeps the arguments an mcp.call or an unclassified
// tool carried on its event, at most auditArgsMax bytes of them, shortened
// so the text stays JSON, with the hash of the whole text. It keeps the
// input a client sent beside a classified tool's event, with the client's
// hash when that is a SHA-256, and the harness's own name of that tool. An
// mcp.call that names the built-in straza server keeps none, and neither
// does a call of the gateway's drafting tools under another server name,
// because only the gateway serves them and its own record masks a draft,
// which this record would not.
func TestDecideRecordKeepsToolInput(t *testing.T) {
	t.Parallel()
	long := `{"q":"` + strings.Repeat("é", auditArgsMax) + `"}`
	sum := sha256.Sum256([]byte(long))
	longHash := "sha256:" + hex.EncodeToString(sum[:])
	sentHash := "sha256:" + strings.Repeat("ab", 32)
	rows := []struct {
		name   string
		ev     policy.Event
		input  *ToolInput
		args   string // "" means the record carries no arguments
		hash   string // "" means the record says nothing was cut
		native string
	}{
		{"an unclassified tool keeps its arguments",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolOther, ToolName: "LSP", Args: []byte(`{"operation":"hover"}`)},
			nil, `{"operation":"hover"}`, "", ""},
		{"an observed call with no arguments says so",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolOther, ToolName: "LSP", Args: []byte(`{}`)},
			nil, `{}`, "", ""},
		{"arguments past the bound are shortened and stay JSON",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolOther, ToolName: "LSP", Args: []byte(long)},
			nil, long, longHash, ""},
		{"arguments that were not observed write no field",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolOther, ToolName: "LSP"},
			nil, "", "", ""},
		{"a call to a server the harness runs keeps the text it typed",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "claude-in-chrome", ToolName: "form_input", Args: []byte(`{"ref":"e12","value":"hunter2"}`)},
			nil, `{"ref":"e12","value":"hunter2"}`, "", ""},
		{"a call that names the built-in straza server keeps none",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: nativeAppName, ToolName: nativeToolDraftSubmit, Args: []byte(`{"documents":["token: x"]}`)},
			nil, "", "", ""},
		{"a drafting tool under another server name keeps none",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "gw", ToolName: "straza__draft_submit", Args: []byte(`{"documents":["token: x"]}`)},
			nil, "", "", ""},
		{"a drafting tool a dialect could not split keeps none",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolOther, ToolName: "mcp_gw_straza__approval_request", Args: []byte(`{"action":{"args":{"documents":["token: x"]}}}`)},
			nil, "", "", ""},
		{"a classified tool keeps the input the client sent",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolFileRead, NativeTool: "Skill"},
			&ToolInput{Text: `{"skill":"deploy"}`}, `{"skill":"deploy"}`, "", "Skill"},
		{"an input the client cut keeps the client's hash",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolFileWrite, NativeTool: "Write"},
			&ToolInput{Text: `{"content":"abc`, Hash: sentHash}, `{"content":"abc`, sentHash, "Write"},
		{"a hash that is not a SHA-256 is dropped",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolFileWrite, NativeTool: "Write"},
			&ToolInput{Text: `{"content":"abc"}`, Hash: "sha256:" + strings.Repeat("zz", 32)}, `{"content":"abc"}`, "", "Write"},
		{"an input longer than a client may send is cut again",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolFileWrite, NativeTool: "Write"},
			&ToolInput{Text: long, Hash: sentHash}, long, longHash, "Write"},
		{"the event's arguments win over an input beside it",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolOther, ToolName: "LSP", Args: []byte(`{"operation":"hover"}`)},
			&ToolInput{Text: `{"other":1}`}, `{"operation":"hover"}`, "", ""},
		{"a classified tool whose input was not sent names the harness's tool",
			policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolFileRead, NativeTool: "Skill"},
			nil, "", "", "Skill"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			a := &App{audit: &auditSpool{ch: make(chan store.OutboxEvent, 1)}}
			claims := authn.Claims{Subject: "u1", Session: "s1"}
			if !a.auditDecision(context.Background(), claims, DecideRequest{Event: row.ev, ToolInput: row.input}, policy.Decision{Effect: policy.EffectAllow}, "snap") {
				t.Fatal("the record was not queued")
			}
			var ce struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal([]byte((<-a.audit.ch).CE), &ce); err != nil {
				t.Fatal(err)
			}
			d := ce.Data
			got, has := d["arguments"].(string)
			if has != (row.args != "") || len(got) > auditArgsMax {
				t.Fatalf("arguments kept %d bytes (present %v), want at most %d bytes of the %d sent", len(got), has, auditArgsMax, len(row.args))
			}
			_, cut := d["argumentsTruncated"]
			if cut != (row.hash != "") || (cut && (d["argumentsTruncated"] != true || d["argumentsHash"] != row.hash)) || (!cut && d["argumentsHash"] != nil) {
				t.Fatalf("arguments fields = %v / %v with %d bytes kept, want hash %q", d["argumentsTruncated"], d["argumentsHash"], len(got), row.hash)
			}
			if short := len(row.args) <= auditArgsMax; (short && got != row.args) || (!short && (!json.Valid([]byte(got)) || len(got) < auditArgsMax-3)) {
				t.Fatalf("arguments kept %d of %d bytes, want them whole within the bound and shortened JSON at the bound past it", len(got), len(row.args))
			}
			if native, _ := d["nativeTool"].(string); native != row.native {
				t.Fatalf("nativeTool = %q, want %q", native, row.native)
			}
			if _, has := d["nativeTool"]; has != (row.native != "") {
				t.Fatalf("nativeTool present = %v, want it only for a classified tool with a name", has)
			}
		})
	}
}

// TestHookLaneHoldOnUnclassifiedToolBindsItsArguments pins the approval a
// hold opens for an unclassified tool whose arguments the client forwarded:
// the approver's preview shows them, the key binds the exact call, so a
// call with other arguments opens its own approval, and the same call asked
// again joins the pending one.
func TestHookLaneHoldOnUnclassifiedToolBindsItsArguments(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t, func(c *config.Config) { c.Approval.Preview.Enabled = true })
	ctx := context.Background()
	src := "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata: {name: refundheld}\nspec:\n  match: {users: [refunder]}\n  rules:\n" +
		"    - id: gate\n      tools: [other]\n      toolNames: {allow: [refund_order]}\n      effect: allow\n      mode: approve\n      approve: {roles: [sec], timeoutSeconds: 1}\n"
	if _, err := r.app.store.Policies().Create(ctx, store.PolicySet{Name: "refundheld", Status: "active", YAMLSource: src}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}
	_, tok := r.agent(t, "refunder")
	call := func(order string) {
		decide(t, r.base, tok, map[string]any{"kind": "tool.pre", "tool": "other", "toolName": "refund_order",
			"args": map[string]any{"order": order, "cents": 500}})
	}
	call("A-1")
	call("B-2")
	call("A-1")
	recs, err := r.app.approval.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{}
	for _, rec := range recs {
		if rec.Lane != "hook" {
			continue
		}
		if !strings.HasPrefix(rec.ArgvHash, "v2:call:") || rec.Summary != "other: refund_order" {
			t.Errorf("approval key %q with summary %q, want a call-scoped key of other: refund_order", rec.ArgvHash, rec.Summary)
		}
		keys[rec.ArgvHash] = rec.ArgsPreview
	}
	if len(keys) != 2 {
		t.Fatalf("the three calls opened %d approvals, want 2: one for each order", len(keys))
	}
	var seenA, seenB bool
	for _, preview := range keys {
		seenA = seenA || strings.Contains(preview, "A-1")
		seenB = seenB || strings.Contains(preview, "B-2")
	}
	if !seenA || !seenB {
		t.Errorf("the previews are %v, want one that shows order A-1 and one that shows B-2", keys)
	}
}

// TestToolExamplesMatchTheDecideRecord pins the tool record examples of
// revisions 64 and 65 to the record /v1/decide writes, as the client's own
// test pins them to its record, so the two producers cannot drift apart or
// away from the spec. The reference producer always writes toolName, empty
// for a classified tool, which the examples leave out as the client does.
func TestToolExamplesMatchTheDecideRecord(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"valid-audit-tool-other-arguments.json", "valid-audit-tool-native-named.json",
		"valid-audit-tool-classified-arguments.json", "valid-audit-tool-mcp-arguments.json"} {
		want := exampleData(t, name)
		str := func(k string) string { s, _ := want[k].(string); return s }
		req := DecideRequest{Event: policy.Event{Kind: str("event"), Tool: str("tool"), App: str("app"), ToolName: str("toolName"),
			NativeTool: str("nativeTool"), Workspace: str("workspace")}}
		if args, ok := want["arguments"].(string); ok && (req.Event.Tool == policy.ToolOther || req.Event.Tool == policy.ToolMCPCall) {
			req.Event.Args = []byte(args)
		} else if ok {
			req.ToolInput = &ToolInput{Text: args}
		}
		a := &App{audit: &auditSpool{ch: make(chan store.OutboxEvent, 1)}}
		if !a.auditDecision(context.Background(), authn.Claims{Subject: "u1", Session: "s1"}, req, policy.Decision{Effect: policy.EffectAllow}, "snap") {
			t.Fatalf("%s: the record was not queued", name)
		}
		var ce struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte((<-a.audit.ch).CE), &ce); err != nil {
			t.Fatal(err)
		}
		got := ce.Data
		if got["toolName"] == "" {
			delete(got, "toolName")
		}
		if g, w := recordFields(got, ""), recordFields(want, ""); !slices.Equal(g, w) {
			t.Errorf("%s: the record's fields are %v, the example's %v", name, g, w)
		}
		for _, k := range []string{"event", "tool", "toolName", "nativeTool", "arguments", "command", "app", "workspace"} {
			if got[k] != want[k] {
				t.Errorf("%s: %s = %v, the example has %v", name, k, got[k], want[k])
			}
		}
	}
}
