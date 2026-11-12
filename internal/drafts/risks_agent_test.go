package drafts

import (
	"reflect"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// agentGuardrails is a live set that binds dev, and so ci-bot, and gates
// only tools of a managed machine, which agent.own-reach does not read: a
// hold on shell.exec and a deny on file.write.
var agentGuardrails = gainSet("agent-guardrails", "{ roles: [dev] }", holdShell+"    - id: no-write\n      tools: [file.write]\n      effect: deny\n")

// guardWorld is gainWorld with agentGuardrails.
func guardWorld() World {
	return withSet(gainWorld(), "agent-guardrails", agentGuardrails)
}

// guardRefusal is the agent.guardrail refusal of set for the clause, as
// findingLines spells it.
func guardRefusal(set, clause string) string {
	return "agent.guardrail PolicySet/" + set + " " + set + " applies to you, and this draft " + clause + ". An agent's draft may not change what binds the agent."
}

// guardLoosened is the agent.guardrail risk of set for the clause, as
// riskLines spells it.
func guardLoosened(set, clause string) string {
	return "agent.guardrail PolicySet/" + set + " (typed " + set + ") " + set + " binds the agent that wrote this draft, and this draft " + clause + ". | - | -"
}

// sameLines reports whether two line lists hold the same lines, nil and
// empty alike.
func sameLines(got, want []string) bool {
	return (len(got) == 0 && len(want) == 0) || reflect.DeepEqual(got, want)
}

// TestAgentGuardrail pins every case of the guardrail for an agent. A
// draft that loosens a set that binds the agent, by removing it, turning
// it off, narrowing its match away from the agent, changing its priority,
// taking a role out of the agent's roles, or changing or removing a gate,
// raises agent.guardrail as a risk typed by the set's name. A direct
// route, which publishes with no review, refuses each of them as before.
// A rule added or widened in such a set, a set the draft creates included,
// is refused either way. So is a rule kept by its id that comes to allow
// more, and an approval of another set that the draft lets take over a
// stricter one, whichever of the two sets the draft changes. Added gates,
// added denies and reason text raise nothing, and a loosening that widens
// what the agent reaches on an MCP server is refused by agent.own-reach.
func TestAgentGuardrail(t *testing.T) {
	t.Parallel()
	guard := func(old, repl string) []Item {
		return []Item{setPut("agent-guardrails", strings.Replace(agentGuardrails, old, repl, 1))}
	}
	outOfRoles := "takes dev out of your roles so that the set no longer applies to you"
	outOfAgent := "takes dev out of the agent's roles, so the set no longer applies to it"
	cases := []struct {
		name    string
		items   []Item
		refused []string
		risks   []string
		direct  []string
	}{
		{"the set removed", []Item{{Kind: KindPolicySet, Name: "agent-guardrails", Op: OpRemove}}, nil,
			[]string{guardLoosened("agent-guardrails", "removes it")}, []string{guardRefusal("agent-guardrails", "removes it")}},
		{"the set turned off", []Item{{Kind: KindPolicySet, Name: "agent-guardrails", Op: OpOff, Doc: agentGuardrails}}, nil,
			[]string{guardLoosened("agent-guardrails", "turns it off")}, []string{guardRefusal("agent-guardrails", "turns it off")}},
		{"its match narrowed away from the agent", guard("roles: [dev]", "roles: [readers]"), nil,
			[]string{guardLoosened("agent-guardrails", "changes its match or priority")}, []string{guardRefusal("agent-guardrails", "changes its match or priority")}},
		{"its priority changed", guard("priority: 10", "priority: 20"), nil,
			[]string{guardLoosened("agent-guardrails", "changes its match or priority")}, []string{guardRefusal("agent-guardrails", "changes its match or priority")}},
		{"a role taken out of the agent's roles", []Item{gainRole("engineering", "    kind: business\n")}, nil,
			[]string{guardLoosened("agent-guardrails", outOfAgent), guardLoosened("dev-access", outOfAgent)},
			[]string{guardRefusal("agent-guardrails", outOfRoles), guardRefusal("dev-access", outOfRoles)}},
		{"a gate changed", guard("approve: { roles: [sec-approvers] }", "approve: { roles: [sec-approvers], timeoutSeconds: 600 }"), nil,
			[]string{guardLoosened("agent-guardrails", "changes rule hold-shell")}, []string{guardRefusal("agent-guardrails", "changes rule hold-shell")}},
		{"a gate removed", guard("    - id: no-write\n      tools: [file.write]\n      effect: deny\n", ""), nil,
			[]string{guardLoosened("agent-guardrails", "changes rule no-write")}, []string{guardRefusal("agent-guardrails", "changes rule no-write")}},
		{"an allow rule added", []Item{setPut("agent-guardrails", agentGuardrails+"    - id: allow-fetch\n      tools: [net.fetch]\n      effect: allow\n")},
			[]string{guardRefusal("agent-guardrails", "adds or widens rule allow-fetch")}, nil, []string{guardRefusal("agent-guardrails", "adds or widens rule allow-fetch")}},
		{"a new set with an allow rule that binds the agent", []Item{setPut("extra", gainSet("extra", "{ roles: [dev] }", "    - id: allow-shell\n      tools: [shell.exec]\n      effect: allow\n"))},
			[]string{guardRefusal("extra", "adds or widens rule allow-shell")}, nil, []string{guardRefusal("extra", "adds or widens rule allow-shell")}},
		{"an MCP gate of such a set removed", []Item{devAccessWith("    - id: no-delete\n      tools: [mcp.call]\n      toolNames: { deny: [delete_repo] }\n", "")},
			[]string{"agent.own-reach App/github " + ownReach("run delete_repo on github with no approval")}, nil, []string{guardRefusal("dev-access", "changes rule no-delete")}},
		{"a set with MCP gates removed", []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpRemove}},
			[]string{"agent.own-reach App/github " + ownReach("run delete_repo and push_files on github with no approval")}, nil, []string{guardRefusal("dev-access", "removes it")}},
		{"a gate added for a tool the deployment allows by default", []Item{setPut("dev-access", devAccess+
			"    - id: hold-shell\n      tools: [shell.exec]\n      mode: approve\n      effect: allow\n      approve: { roles: [sec-approvers] }\n")}, nil, nil, nil},
		{"a deny added", []Item{setPut("dev-access", devAccess+"    - id: no-shell\n      tools: [shell.exec]\n      effect: deny\n")}, nil, nil, nil},
		{"reason text changed", []Item{devAccessWith("toolNames: { deny: [delete_repo] }\n", "toolNames: { deny: [delete_repo] }\n      reason: never delete\n")}, nil, nil, nil},
		{"a new set with gates only", []Item{setPut("extra", gainSet("extra", "{ roles: [dev] }", "    - id: no-shell\n      tools: [shell.exec]\n      effect: deny\n"))}, nil, nil, nil},
		{"a set that does not bind the agent", []Item{setPut("readers-access", gainSet("readers-access", "{ roles: [readers] }", "    - id: allow-shell\n      tools: [shell.exec]\n      effect: allow\n"))},
			nil, nil, nil},
	}
	judge := func(t *testing.T, w World, items []Item, refused, risks, direct []string) {
		t.Helper()
		d := agentDraftOf(w, items...)
		in := CheckInput{Proposer: ciBot, Now: checkNow, HarnessTools: func() (map[string]string, error) { return map[string]string{"Bash": policy.ToolShellExec}, nil }}
		v := Check(w, d, in)
		if got := findingLines(v.Refused); !sameLines(got, refused) {
			t.Errorf("Refused\n got %q\nwant %q", got, refused)
		}
		if got := only(riskLines(v.Risks), codeAgentGuardrail); !sameLines(got, risks) {
			t.Errorf("Risks\n got %q\nwant %q", got, risks)
		}
		in.Direct = true
		dv := Check(w, d, in)
		if got := findingLines(dv.Refused); !sameLines(got, direct) {
			t.Errorf("Refused on a direct route\n got %q\nwant %q", got, direct)
		}
		for _, f := range append(v.Refused, dv.Refused...) {
			set := strings.TrimPrefix(f.Object, "PolicySet/")
			if fix := "Leave " + set + " and your roles as they are. A person can change them."; f.Code == codeAgentGuardrail && f.Fix != fix {
				t.Errorf("Fix = %q, want %q", f.Fix, fix)
			}
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { judge(t, guardWorld(), tc.items, tc.refused, tc.risks, tc.direct) })
	}
	// Each widening below is refused whatever the route. A direct route
	// also refuses the loosening the same rewrite makes, where it makes one.
	holdShellPre := "    - id: hold-shell\n      events: [tool.pre]\n      tools: [shell.exec]\n      mode: approve\n      effect: allow\n      approve: { roles: [sec-approvers] }\n"
	denyLocal := func(rules string) World {
		w := withSet(gainWorld(), "agent-guardrails", gainSet("agent-guardrails", "{ roles: [dev] }", rules))
		w.LocalToolDefault = policy.EffectDeny
		return w
	}
	rewrite := func(rules string) []Item {
		return []Item{setPut("agent-guardrails", gainSet("agent-guardrails", "{ roles: [dev] }", rules))}
	}
	widens := func(id string) []string { return []string{guardRefusal("agent-guardrails", "adds or widens rule "+id)} }
	alsoChanges := func(id string) []string {
		return append(widens(id), guardRefusal("agent-guardrails", "changes rule "+id))
	}
	loose := strings.Replace(gainSet("a-loose", "{ roles: [dev] }", fastShell), "priority: 10", "priority: 5", 1)
	priority := guardRefusal("a-loose", "changes its match or priority")
	takeover := withSet(guardWorld(), "a-loose", loose)
	for _, tc := range []struct {
		name            string
		w               World
		items           []Item
		refused, direct []string
	}{
		{"a gate kept by id that becomes an allow of more tool kinds", denyLocal(holdShellPre),
			rewrite("    - id: hold-shell\n      events: [tool.pre]\n      tools: [shell.exec, net.fetch, task.spawn, other]\n      effect: allow\n"), widens("hold-shell"), alsoChanges("hold-shell")},
		{"a command rule kept by id whose allow side widens", denyLocal("    - id: git-only\n      events: [tool.pre]\n      tools: [shell.exec]\n      command: { allowPatterns: [\"git status\"], denyPatterns: [\"git push*\"] }\n"),
			rewrite("    - id: git-only\n      events: [tool.pre]\n      tools: [shell.exec]\n      command: { allowPatterns: [\"*\"], denyPatterns: [\"git push*\"] }\n"), widens("git-only"), alsoChanges("git-only")},
		{"a by-name deny kept by id that becomes a by-name allow", denyLocal("    - id: no-some\n      events: [tool.pre]\n      tools: [other]\n      toolNames: { deny: [SomeTool] }\n"),
			rewrite("    - id: no-some\n      events: [tool.pre]\n      tools: [other]\n      toolNames: { allow: [SomeTool] }\n"), widens("no-some"), alsoChanges("no-some")},
		{"a deny kept by id that becomes an allow", denyLocal("    - id: no-write\n      events: [tool.pre]\n      tools: [file.write]\n      effect: deny\n"),
			rewrite("    - id: no-write\n      events: [tool.pre]\n      tools: [file.write, net.fetch]\n      effect: allow\n"), widens("no-write"), alsoChanges("no-write")},
		{"an approval added for the tools of a server that is not registered", guardWorld(),
			[]Item{setPut("agent-guardrails", agentGuardrails+"    - id: hold-chrome\n      events: [tool.pre]\n      tools: [mcp.call]\n      apps: [claude-in-chrome]\n"+
				"      mode: approve\n      effect: allow\n      approve: { roles: [sec-approvers] }\n")}, widens("hold-chrome"), widens("hold-chrome")},
		{"an approval kept by id that opens another tool kind", denyLocal(holdShellPre),
			rewrite(strings.Replace(holdShellPre, "tools: [shell.exec]", "tools: [shell.exec, net.fetch]", 1)), widens("hold-shell"), widens("hold-shell")},
		{"a gate kept by id that becomes a plain allow where the deployment allows by default", guardWorld(),
			[]Item{setPut("agent-guardrails", strings.Replace(agentGuardrails, "      mode: approve\n      effect: allow\n      approve: { roles: [sec-approvers] }\n", "      effect: allow\n", 1))},
			widens("hold-shell"), alsoChanges("hold-shell")},
		{"the stricter set lowered so that a looser approval takes over", takeover, []Item{setPut("agent-guardrails", strings.Replace(agentGuardrails, "priority: 10", "priority: 1", 1))},
			[]string{guardRefusal("agent-guardrails", "changes its match or priority")}, []string{guardRefusal("agent-guardrails", "changes its match or priority")}},
		{"the looser set raised so that its approval takes over", takeover, []Item{setPut("a-loose", strings.Replace(loose, "priority: 5", "priority: 20", 1))},
			[]string{guardRefusal("a-loose", "adds or widens rule fast-shell")}, []string{priority, guardRefusal("a-loose", "adds or widens rule fast-shell")}},
	} {
		t.Run(tc.name, func(t *testing.T) { judge(t, tc.w, tc.items, tc.refused, nil, tc.direct) })
	}
}

// TestAgentLoosensWithoutReadingRisks pins that a check that stops after
// its refusals keeps an agent's loosening of a set that binds it refused,
// since nothing would read the risk it raises otherwise.
func TestAgentLoosensWithoutReadingRisks(t *testing.T) {
	t.Parallel()
	w := guardWorld()
	v := Check(w, agentDraftOf(w, Item{Kind: KindPolicySet, Name: "agent-guardrails", Op: OpRemove}), CheckInput{Proposer: ciBot, Now: checkNow, RefusalsOnly: true})
	if want := []string{guardRefusal("agent-guardrails", "removes it")}; !reflect.DeepEqual(findingLines(v.Refused), want) {
		t.Errorf("Refused\n got %q\nwant %q", findingLines(v.Refused), want)
	}
}

// TestAgentGuardrailReadsByNameRules pins that a by-name rule needs no rule
// of its own: an allow of a harness tool by name added to a set that binds
// the agent widens its reach and is refused, and a by-name deny dropped
// from such a set loosens it, a risk typed by the set's name.
func TestAgentGuardrailReadsByNameRules(t *testing.T) {
	t.Parallel()
	noSome := "    - id: no-some\n      tools: [other]\n      toolNames: { deny: [SomeTool] }\n"
	in := CheckInput{Proposer: ciBot, Now: checkNow, HarnessTools: func() (map[string]string, error) { return map[string]string{"Bash": policy.ToolShellExec}, nil }}
	w := guardWorld()
	added := Check(w, agentDraftOf(w, setPut("agent-guardrails", agentGuardrails+"    - id: allow-some\n      tools: [other]\n      toolNames: { allow: [SomeTool] }\n")), in)
	if want := []string{guardRefusal("agent-guardrails", "adds or widens rule allow-some")}; !reflect.DeepEqual(findingLines(added.Refused), want) {
		t.Errorf("a by-name allow added: refused\n got %q\nwant %q", findingLines(added.Refused), want)
	}
	w = withSet(gainWorld(), "agent-guardrails", agentGuardrails+noSome)
	dropped := Check(w, agentDraftOf(w, setPut("agent-guardrails", agentGuardrails)), in)
	if len(dropped.Refused) > 0 {
		t.Fatalf("a by-name deny dropped: refused %q", findingLines(dropped.Refused))
	}
	if want := []string{guardLoosened("agent-guardrails", "changes rule no-some")}; !reflect.DeepEqual(only(riskLines(dropped.Risks), codeAgentGuardrail), want) {
		t.Errorf("a by-name deny dropped: risks\n got %q\nwant %q", only(riskLines(dropped.Risks), codeAgentGuardrail), want)
	}
}
