package drafts

import (
	"reflect"
	"slices"
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

// guardLoosened is the agent.guardrail risk of set for the clauses, one
// sentence each and the clauses sorted as its after words, as riskLines
// spells it.
func guardLoosened(set string, clauses ...string) string {
	said := make([]string, len(clauses))
	for i, clause := range clauses {
		said[i] = set + " binds the agent that wrote this draft, and this draft " + clause + "."
	}
	return "agent.guardrail PolicySet/" + set + " (typed " + set + ") " + strings.Join(said, " ") + " | - | " + strings.Join(slices.Sorted(slices.Values(clauses)), "; ")
}

// sameLines reports whether two line lists hold the same lines, nil and
// empty alike.
func sameLines(got, want []string) bool {
	return (len(got) == 0 && len(want) == 0) || reflect.DeepEqual(got, want)
}

// TestAgentGuardrail pins every case of the guardrail for an agent. A
// draft that changes a set that binds the agent raises agent.guardrail as
// a risk typed by the set's name, and is not refused: it removes the set,
// turns it off, narrows its match away from the agent, changes its
// priority, takes a role out of the agent's roles, changes or removes a
// gate, adds or widens a rule, a set the draft creates included, keeps a
// rule by its id that comes to allow more, or lets an approval of another
// set take over a stricter one, whichever of the two sets it changes. A
// direct route, which publishes with no review, refuses each of them.
// Added gates, added denies and reason text raise nothing. A loosening
// that widens what the agent reaches on an MCP server raises
// agent.own-reach beside it, which a direct route never reaches.
func TestAgentGuardrail(t *testing.T) {
	t.Parallel()
	guard := func(old, repl string) []Item {
		return []Item{setPut("agent-guardrails", strings.Replace(agentGuardrails, old, repl, 1))}
	}
	outOfRoles := "takes dev out of your roles so that the set no longer applies to you"
	outOfAgent := "takes dev out of the agent's roles, so the set no longer applies to it"
	reaches := func(words string) string {
		return ownReachRisks([]string{"agent.own-reach App/github " + ownReach(words)})[0]
	}
	cases := []struct {
		name   string
		items  []Item
		risks  []string
		direct []string
	}{
		{"the set removed", []Item{{Kind: KindPolicySet, Name: "agent-guardrails", Op: OpRemove}},
			[]string{guardLoosened("agent-guardrails", "removes it")}, []string{guardRefusal("agent-guardrails", "removes it")}},
		{"the set turned off", []Item{{Kind: KindPolicySet, Name: "agent-guardrails", Op: OpOff, Doc: agentGuardrails}},
			[]string{guardLoosened("agent-guardrails", "turns it off")}, []string{guardRefusal("agent-guardrails", "turns it off")}},
		{"its match narrowed away from the agent", guard("roles: [dev]", "roles: [readers]"),
			[]string{guardLoosened("agent-guardrails", "changes its match or priority")}, []string{guardRefusal("agent-guardrails", "changes its match or priority")}},
		{"its priority changed", guard("priority: 10", "priority: 20"),
			[]string{guardLoosened("agent-guardrails", "changes its match or priority")}, []string{guardRefusal("agent-guardrails", "changes its match or priority")}},
		{"a role taken out of the agent's roles", []Item{gainRole("engineering", "    kind: business\n")},
			[]string{guardLoosened("agent-guardrails", outOfAgent), guardLoosened("dev-access", outOfAgent)},
			[]string{guardRefusal("agent-guardrails", outOfRoles), guardRefusal("dev-access", outOfRoles)}},
		{"a gate changed", guard("approve: { roles: [sec-approvers] }", "approve: { roles: [sec-approvers], timeoutSeconds: 600 }"),
			[]string{guardLoosened("agent-guardrails", "changes rule hold-shell")}, []string{guardRefusal("agent-guardrails", "changes rule hold-shell")}},
		{"a gate removed", guard("    - id: no-write\n      tools: [file.write]\n      effect: deny\n", ""),
			[]string{guardLoosened("agent-guardrails", "changes rule no-write")}, []string{guardRefusal("agent-guardrails", "changes rule no-write")}},
		{"an allow rule added", []Item{setPut("agent-guardrails", agentGuardrails+"    - id: allow-fetch\n      tools: [net.fetch]\n      effect: allow\n")},
			[]string{guardLoosened("agent-guardrails", "adds or widens rule allow-fetch")}, []string{guardRefusal("agent-guardrails", "adds or widens rule allow-fetch")}},
		{"a new set with an allow rule that binds the agent", []Item{setPut("extra", gainSet("extra", "{ roles: [dev] }", "    - id: allow-shell\n      tools: [shell.exec]\n      effect: allow\n"))},
			[]string{guardLoosened("extra", "adds or widens rule allow-shell")}, []string{guardRefusal("extra", "adds or widens rule allow-shell")}},
		{"an MCP gate of such a set removed", []Item{devAccessWith("    - id: no-delete\n      tools: [mcp.call]\n      toolNames: { deny: [delete_repo] }\n", "")},
			[]string{reaches("run delete_repo on github with no approval"), guardLoosened("dev-access", "changes rule no-delete")}, []string{guardRefusal("dev-access", "changes rule no-delete")}},
		{"a set with MCP gates removed", []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpRemove}},
			[]string{reaches("run delete_repo and push_files on github with no approval"), guardLoosened("dev-access", "removes it")}, []string{guardRefusal("dev-access", "removes it")}},
		{"a gate added for a tool the deployment allows by default", []Item{setPut("dev-access", devAccess+
			"    - id: hold-shell\n      tools: [shell.exec]\n      mode: approve\n      effect: allow\n      approve: { roles: [sec-approvers] }\n")}, nil, nil},
		{"a deny added", []Item{setPut("dev-access", devAccess+"    - id: no-shell\n      tools: [shell.exec]\n      effect: deny\n")}, nil, nil},
		{"reason text changed", []Item{devAccessWith("toolNames: { deny: [delete_repo] }\n", "toolNames: { deny: [delete_repo] }\n      reason: never delete\n")}, nil, nil},
		{"a new set with gates only", []Item{setPut("extra", gainSet("extra", "{ roles: [dev] }", "    - id: no-shell\n      tools: [shell.exec]\n      effect: deny\n"))}, nil, nil},
		{"a set that does not bind the agent", []Item{setPut("readers-access", gainSet("readers-access", "{ roles: [readers] }", "    - id: allow-shell\n      tools: [shell.exec]\n      effect: allow\n"))},
			nil, nil},
	}
	judge := func(t *testing.T, w World, items []Item, risks, direct []string) {
		t.Helper()
		d := agentDraftOf(w, items...)
		in := CheckInput{Proposer: ciBot, Now: checkNow, HarnessTools: func() (map[string]string, error) { return map[string]string{"Bash": policy.ToolShellExec}, nil }}
		acceptedWhereRefused(t, w, d, in)
		v := Check(w, d, in)
		if len(v.Refused) > 0 {
			t.Errorf("Refused %q, want a reviewed draft not refused", findingLines(v.Refused))
		}
		if got := only(riskLines(v.Risks), "agent."); !sameLines(got, risks) {
			t.Errorf("Risks\n got %q\nwant %q", got, risks)
		}
		for _, f := range v.Risks {
			if strings.HasPrefix(f.Code, "agent.") && (f.Ack != AckTyped || f.Typed == "" || f.Group != "") {
				t.Errorf("%s of %s: ack %q, typed %q, group %q, want typed by a name of its own", f.Code, f.Object, f.Ack, f.Typed, f.Group)
			}
		}
		in.Direct = true
		dv := Check(w, d, in)
		if got := findingLines(dv.Refused); !sameLines(got, direct) {
			t.Errorf("Refused on a direct route\n got %q\nwant %q", got, direct)
		}
		for _, f := range dv.Refused {
			set := strings.TrimPrefix(f.Object, "PolicySet/")
			if fix := "Leave " + set + " and your roles as they are. A person can change them."; f.Code == codeAgentGuardrail && f.Fix != fix {
				t.Errorf("Fix = %q, want %q", f.Fix, fix)
			}
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { judge(t, guardWorld(), tc.items, tc.risks, tc.direct) })
	}
	// Each widening below is a risk on a reviewed draft and a refusal on a
	// direct route, beside the loosening the same rewrite makes, where it
	// makes one.
	holdShellPre := "    - id: hold-shell\n      events: [tool.pre]\n      tools: [shell.exec]\n      mode: approve\n      effect: allow\n      approve: { roles: [sec-approvers] }\n"
	denyLocal := func(rules string) World {
		w := withSet(gainWorld(), "agent-guardrails", gainSet("agent-guardrails", "{ roles: [dev] }", rules))
		w.LocalToolDefault = policy.EffectDeny
		return w
	}
	rewrite := func(rules string) []Item {
		return []Item{setPut("agent-guardrails", gainSet("agent-guardrails", "{ roles: [dev] }", rules))}
	}
	loose := strings.Replace(gainSet("a-loose", "{ roles: [dev] }", fastShell), "priority: 10", "priority: 5", 1)
	takeover := withSet(guardWorld(), "a-loose", loose)
	for _, tc := range []struct {
		name    string
		w       World
		items   []Item
		set     string
		clauses []string
	}{
		{"a gate kept by id that becomes an allow of more tool kinds", denyLocal(holdShellPre),
			rewrite("    - id: hold-shell\n      events: [tool.pre]\n      tools: [shell.exec, net.fetch, task.spawn, other]\n      effect: allow\n"),
			"agent-guardrails", []string{"adds or widens rule hold-shell", "changes rule hold-shell"}},
		{"a command rule kept by id whose allow side widens", denyLocal("    - id: git-only\n      events: [tool.pre]\n      tools: [shell.exec]\n      command: { allowPatterns: [\"git status\"], denyPatterns: [\"git push*\"] }\n"),
			rewrite("    - id: git-only\n      events: [tool.pre]\n      tools: [shell.exec]\n      command: { allowPatterns: [\"*\"], denyPatterns: [\"git push*\"] }\n"),
			"agent-guardrails", []string{"adds or widens rule git-only", "changes rule git-only"}},
		{"a by-name deny kept by id that becomes a by-name allow", denyLocal("    - id: no-some\n      events: [tool.pre]\n      tools: [other]\n      toolNames: { deny: [SomeTool] }\n"),
			rewrite("    - id: no-some\n      events: [tool.pre]\n      tools: [other]\n      toolNames: { allow: [SomeTool] }\n"),
			"agent-guardrails", []string{"adds or widens rule no-some", "changes rule no-some"}},
		{"a deny kept by id that becomes an allow", denyLocal("    - id: no-write\n      events: [tool.pre]\n      tools: [file.write]\n      effect: deny\n"),
			rewrite("    - id: no-write\n      events: [tool.pre]\n      tools: [file.write, net.fetch]\n      effect: allow\n"),
			"agent-guardrails", []string{"adds or widens rule no-write", "changes rule no-write"}},
		{"an approval added for the tools of a server that is not registered", guardWorld(),
			[]Item{setPut("agent-guardrails", agentGuardrails+"    - id: hold-chrome\n      events: [tool.pre]\n      tools: [mcp.call]\n      apps: [claude-in-chrome]\n"+
				"      mode: approve\n      effect: allow\n      approve: { roles: [sec-approvers] }\n")}, "agent-guardrails", []string{"adds or widens rule hold-chrome"}},
		{"an approval kept by id that opens another tool kind", denyLocal(holdShellPre),
			rewrite(strings.Replace(holdShellPre, "tools: [shell.exec]", "tools: [shell.exec, net.fetch]", 1)), "agent-guardrails", []string{"adds or widens rule hold-shell"}},
		{"a gate kept by id that becomes a plain allow where the deployment allows by default", guardWorld(),
			[]Item{setPut("agent-guardrails", strings.Replace(agentGuardrails, "      mode: approve\n      effect: allow\n      approve: { roles: [sec-approvers] }\n", "      effect: allow\n", 1))},
			"agent-guardrails", []string{"adds or widens rule hold-shell", "changes rule hold-shell"}},
		{"the stricter set lowered so that a looser approval takes over", takeover, []Item{setPut("agent-guardrails", strings.Replace(agentGuardrails, "priority: 10", "priority: 1", 1))},
			"agent-guardrails", []string{"changes its match or priority"}},
		{"the looser set raised so that its approval takes over", takeover, []Item{setPut("a-loose", strings.Replace(loose, "priority: 5", "priority: 20", 1))},
			"a-loose", []string{"changes its match or priority", "adds or widens rule fast-shell"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var direct []string
			for _, clause := range tc.clauses {
				direct = append(direct, guardRefusal(tc.set, clause))
			}
			judge(t, tc.w, tc.items, []string{guardLoosened(tc.set, tc.clauses...)}, direct)
		})
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
// the agent widens its reach, and a by-name deny dropped from such a set
// loosens it, each a risk typed by the set's name.
func TestAgentGuardrailReadsByNameRules(t *testing.T) {
	t.Parallel()
	noSome := "    - id: no-some\n      tools: [other]\n      toolNames: { deny: [SomeTool] }\n"
	in := CheckInput{Proposer: ciBot, Now: checkNow, HarnessTools: func() (map[string]string, error) { return map[string]string{"Bash": policy.ToolShellExec}, nil }}
	w := guardWorld()
	added := Check(w, agentDraftOf(w, setPut("agent-guardrails", agentGuardrails+"    - id: allow-some\n      tools: [other]\n      toolNames: { allow: [SomeTool] }\n")), in)
	if len(added.Refused) > 0 {
		t.Fatalf("a by-name allow added: refused %q", findingLines(added.Refused))
	}
	if want := []string{guardLoosened("agent-guardrails", "adds or widens rule allow-some")}; !reflect.DeepEqual(only(riskLines(added.Risks), codeAgentGuardrail), want) {
		t.Errorf("a by-name allow added: risks\n got %q\nwant %q", only(riskLines(added.Risks), codeAgentGuardrail), want)
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

// TestAgentGuardrailKeyFollowsTheClause holds a publisher's typed name to
// what the draft does to the set: when the agent's roles change between the
// review and the publish so that the same draft comes to widen a rule, the
// risk's key and the risk digest change, and the publish asks again.
func TestAgentGuardrailKeyFollowsTheClause(t *testing.T) {
	item := setPut("shell-users", gainSet("shell-users", "{ roles: [shell, dev] }", "    - id: allow-shell\n      tools: [shell.exec]\n      effect: allow\n"))
	at := func(roles ...RoleWindow) (Finding, string) {
		t.Helper()
		w := withShell(gainWorld())
		ciBotHolds(roles...)(&w)
		v := Check(w, agentDraftOf(w, item), CheckInput{Proposer: ciBot, Now: checkNow})
		for _, f := range v.Risks {
			if f.Code == codeAgentGuardrail && f.Object == "PolicySet/shell-users" {
				return f, v.RiskDigest
			}
		}
		t.Fatalf("no agent.guardrail risk on shell-users: refused %q", findingLines(v.Refused))
		return Finding{}, ""
	}
	review, reviewDigest := at(RoleWindow{Role: "engineering"}, RoleWindow{Role: "shell"})
	publish, publishDigest := at(RoleWindow{Role: "engineering"})
	if review.Sentence == publish.Sentence {
		t.Fatalf("the case no longer changes the clause: %q", review.Sentence)
	}
	if review.Key() == publish.Key() {
		t.Errorf("the key stayed %s while the sentence went from %q to %q", review.Key(), review.Sentence, publish.Sentence)
	}
	if reviewDigest == publishDigest {
		t.Errorf("the risk digest stayed %s across the two sentences", reviewDigest)
	}
}
