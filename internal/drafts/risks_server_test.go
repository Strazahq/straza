package drafts

import (
	"reflect"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// TestUnregisteredServerRisks pins the risks of a rule that opens an MCP
// server Straza has not registered, which a harness runs itself and every
// profile denies until a rule allows it: an allow by the server's name or
// by naming no server, and a gate that names the server. It also pins
// that the risk of a rule which names harness tools reads as those tools.
func TestUnregisteredServerRisks(t *testing.T) {
	t.Parallel()
	const tail = ", whose tools are denied until a rule allows them."
	const managed = " on managed machines, where this deployment denies it by default."
	rule := func(id, body string) string { return "    - id: " + id + "\n      events: [tool.pre]\n" + body }
	hold := "      mode: approve\n      effect: allow\n      approve: { roles: [sec-approvers] }\n"
	cases := []struct {
		name string
		deny bool
		rule string
		code string
		// sentence is what the risk says and after ends its after text. An
		// empty sentence means the rule raises neither code.
		sentence, after string
	}{
		{"an allow of named tools on a server that is not registered", false,
			rule("allow-chrome", "      tools: [mcp.call]\n      apps: [claude-in-chrome]\n      toolNames: { allow: [navigate, form_input] }\n      effect: allow\n"), codeLocalAllow,
			"Rule allow-chrome of dev-access allows navigate and form_input of claude-in-chrome, an MCP server Straza has not registered" + tail, "allow-chrome: claude-in-chrome"},
		{"an allow of every tool of such a server", false, rule("allow-chrome", "      tools: [mcp.call]\n      apps: [claude-in-chrome]\n      effect: allow\n"), codeLocalAllow,
			"Rule allow-chrome of dev-access allows every tool of claude-in-chrome, an MCP server Straza has not registered" + tail, "allow-chrome: claude-in-chrome"},
		{"an allow of MCP tools that names no server", false, rule("allow-mcp", "      tools: [mcp.call]\n      effect: allow\n"), codeLocalAllow,
			"Rule allow-mcp of dev-access allows every tool of every MCP server, also of the servers Straza has not registered" + tail, "allow-mcp: every MCP server"},
		{"an allow on a registered server", false, rule("allow-github", "      tools: [mcp.call]\n      apps: [github]\n      effect: allow\n"), codeLocalAllow, "", ""},
		{"an allow on the built-in server", false, rule("allow-straza", "      tools: [mcp.call]\n      apps: [straza]\n      effect: allow\n"), codeLocalAllow, "", ""},
		{"an approval that names such a server", false, rule("hold-chrome", "      tools: [mcp.call]\n      apps: [claude-in-chrome]\n      toolNames: { allow: [navigate] }\n"+hold), codeGuardrail,
			"Rule hold-chrome of dev-access lets navigate of claude-in-chrome run once a person approves it, although claude-in-chrome is an MCP server Straza has not registered" + tail,
			" on claude-in-chrome"},
		{"an approval of an MCP tool that names no server", false, rule("hold-any", "      tools: [mcp.call]\n      toolNames: { allow: [push_files] }\n"+hold), codeGuardrail, "", ""},
		{"a server check of an MCP tool that names no server", false,
			rule("check-any", "      tools: [mcp.call]\n      toolNames: { allow: [navigate] }\n      mode: serverCheck\n      effect: allow\n"), codeGuardrail,
			"Rule check-any of dev-access lets navigate of every MCP server run once the server allows it, also of the servers Straza has not registered" + tail, " on every MCP server"},
		{"a classifier of an MCP tool that names no server", false,
			rule("scan-any", "      tools: [mcp.call]\n      toolNames: { allow: [navigate] }\n      mode: classify\n      effect: allow\n"), codeGuardrail,
			"Rule scan-any of dev-access lets navigate of every MCP server run once a classifier allows it, also of the servers Straza has not registered" + tail, " on every MCP server"},
		{"an allow of one harness tool", true, rule("allow-lsp", "      tools: [other]\n      toolNames: { allow: [LSP] }\n      effect: allow\n"), codeLocalAllow,
			"Rule allow-lsp of dev-access allows the harness tool LSP" + managed, "allow-lsp: other"},
		{"an allow of two harness tools", true, rule("allow-two", "      tools: [other]\n      toolNames: { allow: [LSP, FutureTool] }\n      effect: allow\n"), codeLocalAllow,
			"Rule allow-two of dev-access allows the harness tools LSP and FutureTool" + managed, "allow-two: other"},
		{"an allow of every harness tool", true, rule("allow-other", "      tools: [other]\n      effect: allow\n"), codeLocalAllow,
			"Rule allow-other of dev-access allows every unclassified harness tool" + managed, "allow-other: other"},
		{"an approval of one harness tool", true, rule("hold-lsp", "      tools: [other]\n      toolNames: { allow: [LSP] }\n"+hold), codeGuardrail,
			"Rule hold-lsp of dev-access lets the harness tool LSP run on managed machines once a person approves it, where this deployment denies it by default.", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := gainWorld()
			if tc.deny {
				w.LocalToolDefault = policy.EffectDeny
			}
			d := stamped(w, Item{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: devAccess + tc.rule})
			v := Check(w, d, CheckInput{Proposer: Holder{Username: "alice", UserType: "human"}, Now: checkNow,
				HarnessTools: func() (map[string]string, error) { return map[string]string{"Bash": policy.ToolShellExec}, nil }})
			if len(v.Refused) > 0 {
				t.Fatalf("refused: %q", findingLines(v.Refused))
			}
			got := append(only(riskLines(v.Risks), codeLocalAllow), only(riskLines(v.Risks), codeGuardrail)...)
			if tc.sentence == "" {
				if len(got) > 0 {
					t.Errorf("Risks\n got %q\nwant none", got)
				}
				return
			}
			prefix := tc.code + " PolicySet/dev-access (tick) " + tc.sentence + " | - | "
			if len(got) != 1 || !strings.HasPrefix(got[0], prefix) || !strings.HasSuffix(got[0], tc.after) {
				t.Errorf("Risks\n got %q\nwant one line that starts %q and ends %q", got, prefix, tc.after)
			}
		})
	}
}

// TestAgentCannotOpenAServerItRegisters pins that an agent's draft is
// refused when it opens an MCP server a harness runs itself by a way the
// draft check could miss: a gate that names a server the same draft
// registers, and a gate by tool name that no person decides. A gate by
// tool name that a person decides stays a change the agent may make, and
// a person's draft that registers the server it gates carries no risk.
func TestAgentCannotOpenAServerItRegisters(t *testing.T) {
	t.Parallel()
	gate := func(id, body string) string {
		return "    - id: " + id + "\n      events: [tool.pre]\n      tools: [mcp.call]\n" + body + "      effect: allow\n"
	}
	named := gate("hold-chrome", "      apps: [claude-in-chrome]\n      mode: approve\n      approve: { roles: [sec-approvers] }\n")
	chrome := map[string]App{"claude-in-chrome": {Name: "claude-in-chrome", Status: "running", Runtime: "remote", URL: "https://chrome.example.com", Credential: CredentialNone, Exposure: []string{"*"}}}
	tools := func() (map[string]string, error) { return map[string]string{"Bash": policy.ToolShellExec}, nil }
	widens := func(id string) []string { return []string{guardRefusal("agent-guardrails", "adds or widens rule "+id)} }
	for _, tc := range []struct {
		name    string
		rule    string
		apps    map[string]App
		refused []string
	}{
		{"a gate that names a server the same draft registers", named, chrome, widens("hold-chrome")},
		{"a server check by tool name", gate("check-nav", "      toolNames: { allow: [navigate] }\n      mode: serverCheck\n"), nil, widens("check-nav")},
		{"a classifier by tool name", gate("scan-nav", "      toolNames: { allow: [navigate] }\n      mode: classify\n"), nil, widens("scan-nav")},
		{"an approval by tool name", gate("hold-nav", "      toolNames: { allow: [navigate] }\n      mode: approve\n      approve: { roles: [sec-approvers] }\n"), nil, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := guardWorld()
			items := []Item{setPut("agent-guardrails", agentGuardrails+tc.rule)}
			if tc.apps != nil {
				items = append([]Item{appPut("claude-in-chrome")}, items...)
			}
			v := Check(w, agentDraftOf(w, items...), CheckInput{Proposer: ciBot, Now: checkNow, HarnessTools: tools, Apps: tc.apps})
			if got := only(findingLines(v.Refused), codeAgentGuardrail); len(got)+len(tc.refused) > 0 && !reflect.DeepEqual(got, tc.refused) {
				t.Errorf("Refused\n got %q\nwant %q", got, tc.refused)
			}
		})
	}
	w := gainWorld()
	v := Check(w, stamped(w, appPut("claude-in-chrome"), setPut("dev-access", devAccess+named)),
		CheckInput{Proposer: Holder{Username: "erin", UserType: "human"}, Now: checkNow, HarnessTools: tools, Apps: chrome})
	if got := only(riskLines(v.Risks), codeGuardrail); len(v.Refused) > 0 || len(got) > 0 {
		t.Errorf("a person's draft that registers the server it gates: refused %q, risks %q, want neither", findingLines(v.Refused), got)
	}
}
