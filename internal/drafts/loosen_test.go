package drafts

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// Each test below pins what the structural layer, the rule-level diff of
// every set, must do whoever holds the roles.

// holdShell is a live gate that holds shell.exec for sec-approvers.
const holdShell = "    - id: hold-shell\n      tools: [shell.exec]\n      mode: approve\n      effect: allow\n      approve: { roles: [sec-approvers] }\n"

// erin is a person who holds no role, so no set applies to her and a risk
// is typed only for what the change does.
var erin = CheckInput{Proposer: Holder{Username: "erin", UserType: "human", AgencyMode: "interactive"}, Now: checkNow}

// shellWorld is gainWorld with hold-shell in dev-access.
func shellWorld() World {
	w := gainWorld()
	w.Policies["dev-access"] = Policy{Name: "dev-access", Text: devAccess + holdShell}
	return w
}

// guardLines answers the policy.guardrail-changed lines of v.
func guardLines(v Verdict) []string {
	return only(riskLines(v.Risks), codeGuardrail)
}

// setPut is the put of the set name holding text.
func setPut(name, text string) Item {
	return Item{Kind: KindPolicySet, Name: name, Op: OpPut, Doc: text}
}

// TestTakeoverAcrossRolesLoosens pins that a set that matches one role can
// take over the gate a set of another role puts on the same calls, for
// whoever holds both, so the takeover is a risk whatever the roles.
func TestTakeoverAcrossRolesLoosens(t *testing.T) {
	t.Parallel()
	w := shellWorld()
	w.Holders["readers"] = append(w.Holders["readers"], Holder{Username: "alice", UserType: "human", AgencyMode: "interactive"})
	fast := gainSet("a-fast", "{ roles: [readers] }", fastShell)
	self := gainSet("a-self", "{ roles: [readers] }", "    - id: self-push\n      tools: [mcp.call]\n      apps: [github]\n      toolNames: { allow: [push_files] }\n"+
		"      mode: approve\n      approve: { roles: [sec-approvers], selfApproval: true }\n")
	cases := []struct {
		name string
		item Item
		want []string
	}{
		{"a looser shell gate", setPut("a-fast", fast), []string{"policy.guardrail-changed PolicySet/a-fast (tick) Rule fast-shell of a-fast will gate shell.exec in place of rule hold-shell of dev-access, " +
			"with a ticket good for 1 day, decided by the person behind the agent where today it is a hold, up to 90 seconds, decided by sec-approvers. | " +
			refOf(t, devAccess+holdShell, "hold-shell") + " | " + refOf(t, fast, "fast-shell")}},
		{"a push its requester may approve", setPut("a-self", self), []string{"policy.guardrail-changed PolicySet/a-self (typed a-self) Rule self-push of a-self will gate mcp.call in place of rule hold-push of dev-access, " +
			"with a hold, up to 90 seconds, decided by sec-approvers or the requester where today it is a hold, up to 2 minutes, decided by sec-approvers. | " +
			refOf(t, devAccess, "hold-push") + " | " + refOf(t, self, "self-push")}},
	}
	for _, tc := range cases {
		v := Check(w, stamped(w, tc.item), erin)
		if got := guardLines(v); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// TestTakeoverBehindAPatternGate pins that a live gate that outranks the
// new one on some commands hides nothing, because each new gate is weighed
// against every live gate it outranks.
func TestTakeoverBehindAPatternGate(t *testing.T) {
	t.Parallel()
	w := withSet(shellWorld(), "g1", strings.Replace(gainSet("g1", "{ roles: [dev] }",
		"    - id: ls-by-sponsor\n      tools: [shell.exec]\n      command: { allowPatterns: ['ls*'] }\n      mode: approve\n      approve: { deciders: [sponsor] }\n"), "priority: 10", "priority: 20", 1))
	g3 := strings.Replace(gainSet("g3", "{ roles: [dev] }", "    - id: all-by-sponsor\n      tools: [shell.exec]\n      mode: approve\n      effect: allow\n"+
		"      approve: { deciders: [sponsor], class: ticket }\n"), "priority: 10", "priority: 15", 1)
	agent := Check(w, agentDraftOf(w, setPut("g3", g3)), CheckInput{Proposer: ciBot, Now: checkNow})
	want := []string{"agent.guardrail PolicySet/g3 g3 applies to you, and this draft adds or widens rule all-by-sponsor. An agent's draft may not change what binds the agent."}
	if got := findingLines(agent.Refused); !reflect.DeepEqual(got, want) {
		t.Errorf("agent: refused\n got %q\nwant %q", got, want)
	}
	person := Check(w, stamped(w, setPut("g3", g3)), erin)
	want = []string{"policy.guardrail-changed PolicySet/g3 (tick) Rule all-by-sponsor of g3 will gate shell.exec in place of rule hold-shell of dev-access, " +
		"with a ticket good for 1 day, decided by the person behind the agent where today it is a hold, up to 90 seconds, decided by sec-approvers. | " +
		refOf(t, devAccess+holdShell, "hold-shell") + " | " + refOf(t, g3, "all-by-sponsor")}
	if got := guardLines(person); !reflect.DeepEqual(got, want) {
		t.Errorf("person:\n got %q\nwant %q", got, want)
	}
}

// TestRegoRemovalLoosens pins that a Rego module refuses calls, so a draft
// that strips or changes it, or removes or turns off its set, loosens it:
// a typed risk for a person, and agent.guardrail for an agent it binds.
func TestRegoRemovalLoosens(t *testing.T) {
	t.Parallel()
	rego := "  escape:\n    rego: |\n      package straza.ext\n      deny contains \"no pushing\" if { input.event.toolName == \"push_files\" }\n"
	module := func(text string) string {
		doc, err := policy.Parse([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		return "rego@" + sha256Hex(doc.Spec.Escape.Rego)[:12]
	}
	w := withSet(gainWorld(), "dev-access", devAccess+rego)
	guard := gainSet("rego-guard", "{ roles: [dev] }", "    - id: noop\n      tools: [task.spawn]\n      effect: allow\n") + rego
	w2 := withSet(gainWorld(), "rego-guard", guard)
	refusal := func(set, clause string) string {
		return "agent.guardrail PolicySet/" + set + " " + set + " applies to you, and this draft " + clause + ". An agent's draft may not change what binds the agent."
	}
	stop := func(set, cause, before, after string) []string {
		return []string{"policy.guardrail-changed PolicySet/" + set + " (typed " + set + ") " + set + " will stop refusing calls through its Rego module as it does today, because this draft " +
			cause + ". | " + before + " | " + after}
	}
	for _, tc := range []struct {
		name  string
		w     World
		item  Item
		agent string
		risks []string
	}{
		{"the module stripped", w, setPut("dev-access", devAccess), refusal("dev-access", "removes its Rego module"), stop("dev-access", "removes the module", module(devAccess+rego), "rego@removed")},
		{"a module-only set removed", w2, Item{Kind: KindPolicySet, Name: "rego-guard", Op: OpRemove}, refusal("rego-guard", "removes it"), stop("rego-guard", "removes the set", module(guard), "removed")},
		{"a module-only set turned off", w2, Item{Kind: KindPolicySet, Name: "rego-guard", Op: OpOff, Doc: guard}, refusal("rego-guard", "turns it off"), stop("rego-guard", "turns it off", module(guard), "off")},
	} {
		agent := Check(tc.w, agentDraftOf(tc.w, tc.item), CheckInput{Proposer: ciBot, Now: checkNow})
		if !slices.Contains(findingLines(agent.Refused), tc.agent) {
			t.Errorf("%s: agent refused %q, want %q", tc.name, findingLines(agent.Refused), tc.agent)
		}
		if got := guardLines(Check(tc.w, stamped(tc.w, tc.item), erin)); !reflect.DeepEqual(got, tc.risks) {
			t.Errorf("%s: person\n got %q\nwant %q", tc.name, got, tc.risks)
		}
	}
}

// TestRegoModuleDoesNotHideWidening pins that who gains reads every draft
// with the modules stripped, one it adds included, so a widening bundled
// with a new module still raises its risks, and one unchecked line names
// the module that was not run.
func TestRegoModuleDoesNotHideWidening(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	rego := "  escape:\n    rego: |\n      package straza.ext\n      deny contains \"never\" if { false }\n"
	v := Check(w, stamped(w, gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: ['*']\n"),
		setPut("noop", gainSet("noop", "{ roles: [nosuch] }", "    - id: x\n      tools: [task.spawn]\n      effect: deny\n")+rego)), erin)
	if len(v.Refused) > 0 {
		t.Fatalf("refused %q", findingLines(v.Refused))
	}
	if len(v.Gains) == 0 || !slices.Contains(codesOf(v.Risks), codeUngated) || !slices.Contains(codesOf(v.Risks), codeRego) {
		t.Errorf("gains %d, risks %q, want the gains, access.ungated and policy.rego", len(v.Gains), codesOf(v.Risks))
	}
	want := []string{"Straza read who gains what without running the Rego modules of noop, which only refuse calls, so a holder may reach less than it shows."}
	if got := sentences(v.Unchecked, codeUncheckedRego); !reflect.DeepEqual(got, want) {
		t.Errorf("unchecked.rego\n got %q\nwant %q", got, want)
	}
}

// TestIdentityScopedLooseningIsTyped pins that a loosening through a set
// that selects by identity or username is typed like any other, and a deny
// cannot be laundered by first scoping its set.
func TestIdentityScopedLooseningIsTyped(t *testing.T) {
	t.Parallel()
	bots := gainSet("bots-guard", "{ identity: { userType: [agent] } }", "    - id: no-me\n      tools: [mcp.call]\n      toolNames: { deny: [get_me] }\n")
	self := strings.Replace(gainSet("bob-self", "{ users: [bob] }", "    - id: self-push\n      tools: [mcp.call]\n      toolNames: { allow: [push_files] }\n"+
		"      mode: approve\n      approve: { roles: [sec-approvers], selfApproval: true }\n"), "priority: 10", "priority: 100", 1)
	scoped := strings.Replace(devAccess, "  match: { roles: [dev] }\n", "  match: { roles: [dev], identity: { userType: [human, agent, service] } }\n", 1)
	open := strings.Replace(scoped, "    - id: no-delete\n      tools: [mcp.call]\n      toolNames: { deny: [delete_repo] }\n", "", 1)
	cases := []struct {
		name string
		live map[string]string
		item Item
		want []string
	}{
		{"the deny that binds every agent removed", map[string]string{"bots-guard": bots}, Item{Kind: KindPolicySet, Name: "bots-guard", Op: OpRemove}, []string{
			"policy.guardrail-changed PolicySet/bots-guard (typed bots-guard) bots-guard will stop denying mcp.call as rule no-me does today, because this draft removes the set. | " +
				refOf(t, bots, "no-me") + " | removed"}},
		{"one user may approve their own push", nil, setPut("bob-self", self), []string{
			"policy.guardrail-changed PolicySet/bob-self (typed bob-self) Rule self-push of bob-self will gate mcp.call in place of rule hold-push of dev-access, " +
				"with a hold, up to 90 seconds, decided by sec-approvers or the requester where today it is a hold, up to 2 minutes, decided by sec-approvers. | " +
				refOf(t, devAccess, "hold-push") + " | " + refOf(t, self, "self-push")}},
		{"step 1: the set scoped by identity", nil, setPut("dev-access", scoped), []string{
			"policy.guardrail-changed PolicySet/dev-access (typed dev-access) dev-access will stop gating mcp.call as rule hold-push does today, because this draft changes its match or priority. " +
				"dev-access will stop denying mcp.call as rule no-delete does today, because this draft changes its match or priority. | " +
				refOf(t, devAccess, "hold-push") + "; " + refOf(t, devAccess, "no-delete") + " | " + refOf(t, devAccess, "hold-push") + "; " + refOf(t, devAccess, "no-delete")}},
		{"step 2: the deny removed", map[string]string{"dev-access": scoped}, setPut("dev-access", open), []string{
			"policy.guardrail-changed PolicySet/dev-access (typed dev-access) dev-access will stop denying mcp.call as rule no-delete does today, because this draft changes the rule. | " +
				refOf(t, scoped, "no-delete") + " | no-delete@removed"}},
	}
	for _, tc := range cases {
		w := gainWorld()
		for name, text := range tc.live {
			w = withSet(w, name, text)
		}
		if got := guardLines(Check(w, stamped(w, tc.item), erin)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// TestSelfApprovalTakeoverIsTyped pins that a takeover that lets the
// requester approve their own call is looser, and typed.
func TestSelfApprovalTakeoverIsTyped(t *testing.T) {
	t.Parallel()
	w := shellWorld()
	self := gainSet("a-self", "{ roles: [dev] }", "    - id: self-shell\n      tools: [shell.exec]\n      mode: approve\n      effect: allow\n"+
		"      approve: { roles: [sec-approvers], selfApproval: true }\n")
	want := []string{"policy.guardrail-changed PolicySet/a-self (typed a-self) Rule self-shell of a-self will gate shell.exec in place of rule hold-shell of dev-access, " +
		"with a hold, up to 90 seconds, decided by sec-approvers or the requester where today it is a hold, up to 90 seconds, decided by sec-approvers. | " +
		refOf(t, devAccess+holdShell, "hold-shell") + " | " + refOf(t, self, "self-shell")}
	if got := guardLines(Check(w, stamped(w, setPut("a-self", self)), erin)); !reflect.DeepEqual(got, want) {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
}

// TestAgentViewHidesWhatHoldersDecide pins that the agent's view holds no
// finding whose presence depends on whether a role has holders, so a draft
// on a held role and the same draft on a role nobody holds read alike.
func TestAgentViewHidesWhatHoldersDecide(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	w.Roles["idle"] = Role{ID: "r10", Name: "idle", Kind: RoleKindApplication, Plane: PlaneAccess}
	w.Access["idle"] = Access{ID: "b3", Server: "github", Tools: []string{"get_me"}}
	w.Fingerprints["Role/idle"] = "fp-Role/idle"
	var views [][]string
	for _, role := range []string{"readers", "idle"} {
		v := Check(w, agentDraftOf(w, gainRole(role, "    kind: application\n    bindings:\n        - app: github\n          tools: [push_files]\n")), CheckInput{Proposer: ciBot, Now: checkNow}).ForAgent()
		views = append(views, append(codesOf(v.Risks), codesOf(v.Warnings)...))
	}
	if !reflect.DeepEqual(views[0], views[1]) {
		t.Errorf("the agent's view tells a held role from one nobody holds: %q and %q", views[0], views[1])
	}
}

// TestRuleLoosenings pins the rule-level diff of a live set for a person
// who holds none of its roles: a gate that fires on fewer calls, no longer
// denies or no longer gates is typed, a gate loosened in place is a tick,
// or typed when the requester may approve, and a change that only
// tightens raises nothing.
func TestRuleLoosenings(t *testing.T) {
	t.Parallel()
	hold, deny := refOf(t, devAccess, "hold-push"), refOf(t, devAccess, "no-delete")
	was := "a hold, up to 2 minutes, decided by sec-approvers"
	inPlace := func(now string) string {
		return "Rule hold-push of dev-access will gate mcp.call with " + now + " where today it is " + was + "."
	}
	stop := func(verb, rule string) string {
		return "dev-access will stop " + verb + " mcp.call as rule " + rule + " does today, because this draft changes the rule."
	}
	cases := []struct {
		name, old, repl string
		ack, sentence   string
		after           string
	}{
		{"a longer hold", "timeoutSeconds: 120", "timeoutSeconds: 600", "tick", inPlace("a hold, up to 10 minutes, decided by sec-approvers"), "hold-push"},
		{"a larger pool", "roles: [sec-approvers]", "roles: [sec-approvers, leads]", "tick", inPlace("a hold, up to 2 minutes, decided by leads or sec-approvers"), "hold-push"},
		{"the requester confirms", "mode: approve\n      approve: { roles: [sec-approvers], timeoutSeconds: 120 }", "mode: confirm\n      approve: { timeoutSeconds: 120 }", "tick",
			inPlace("a hold, up to 2 minutes, confirmed by the requester"), "hold-push"},
		{"the requester may approve", "timeoutSeconds: 120 }", "timeoutSeconds: 120, selfApproval: true }", "typed dev-access",
			inPlace("a hold, up to 2 minutes, decided by sec-approvers or the requester"), "hold-push"},
		{"the gate dropped", "      mode: approve\n      approve: { roles: [sec-approvers], timeoutSeconds: 120 }\n", "", "typed dev-access", stop("gating", "hold-push"), "hold-push"},
		{"the deny moved to the allow side", "toolNames: { deny: [delete_repo] }", "toolNames: { allow: [delete_repo] }", "typed dev-access", stop("denying", "no-delete"), "no-delete"},
		{"the deny removed", "    - id: no-delete\n      tools: [mcp.call]\n      toolNames: { deny: [delete_repo] }\n", "", "typed dev-access", stop("denying", "no-delete"), "removed"},
		{"a shorter hold", "timeoutSeconds: 120", "timeoutSeconds: 60", "", "", ""},
		{"a deny pattern added", "deny: [delete_repo]", "deny: [delete_repo, wipe_repo]", "", "", ""},
		{"reason text", "toolNames: { deny: [delete_repo] }\n", "toolNames: { deny: [delete_repo] }\n      reason: never delete\n", "", "", ""},
		{"a priority no other approval competes with", "priority: 10", "priority: 20", "", "", ""},
	}
	for _, tc := range cases {
		w := gainWorld()
		w.Roles["leads"] = Role{ID: "r11", Name: "leads", Kind: RoleKindApprover, Plane: PlaneAccess}
		item := devAccessWith(tc.old, tc.repl)
		want := []string{}
		if tc.sentence != "" {
			before, after := hold, ""
			if strings.HasPrefix(tc.after, "no-delete") || tc.after == "removed" {
				before = deny
			}
			switch tc.after {
			case "removed":
				after = "no-delete@removed"
			default:
				after = refOf(t, item.Doc, tc.after)
			}
			want = []string{"policy.guardrail-changed PolicySet/dev-access (" + tc.ack + ") " + tc.sentence + " | " + before + " | " + after}
		}
		v := Check(w, stamped(w, item), erin)
		if len(v.Refused) > 0 {
			t.Fatalf("%s: refused %q", tc.name, findingLines(v.Refused))
		}
		if got := guardLines(v); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, want)
		}
	}
}

// TestTakeovers pins how an approval is weighed against the live approvals
// it can outrank: by priority, set name and place, only where one call of
// one session can fire both, and whichever of the two sets the draft
// changes.
func TestTakeovers(t *testing.T) {
	t.Parallel()
	gate := func(id, approve, extra string) string {
		return "    - id: " + id + "\n      tools: [mcp.call]\n      toolNames: { allow: [create_issue] }\n" + extra + "      mode: approve\n      approve: " + approve + "\n"
	}
	strictRule := gate("strict", "{ roles: [sec-approvers] }", "")
	fastRule := gate("fast", "{ deciders: [sponsor], class: ticket }", "")
	at := func(text, priority string) string {
		return strings.Replace(text, "priority: 10", "priority: "+priority, 1)
	}
	strict := at(gainSet("strict", "{ roles: [dev] }", strictRule), "20")
	loose := gainSet("loose", "{ roles: [dev] }", fastRule)
	line := func(object, x, xSet, xText, y, ySet, yText string) []string {
		return []string{"policy.guardrail-changed PolicySet/" + object + " (tick) Rule " + x + " of " + xSet + " will gate mcp.call in place of rule " + y + " of " + ySet +
			", with a ticket good for 1 day, decided by the person behind the agent where today it is a hold, up to 90 seconds, decided by sec-approvers. | " +
			refOf(t, yText, y) + " | " + refOf(t, xText, x)}
	}
	both := gainSet("both", "{ roles: [dev] }", strictRule+fastRule)
	swapped := gainSet("both", "{ roles: [dev] }", fastRule+strictRule)
	cases := []struct {
		name string
		live map[string]string
		item Item
		want []string
	}{
		{"a stricter gate's priority lowered", map[string]string{"strict": strict, "loose": loose}, setPut("strict", strings.Replace(strict, "priority: 20", "priority: 5", 1)), line("strict", "fast", "loose", loose, "strict", "strict", strict)},
		{"a looser gate moved before a stricter one", map[string]string{"both": both}, setPut("both", swapped), line("both", "fast", "both", swapped, "strict", "both", both)},
		{"a glob that reaches the gated name", map[string]string{"strict": strict}, setPut("loose", at(strings.Replace(loose, "allow: [create_issue]", "allow: [create_*]", 1), "30")),
			line("loose", "fast", "loose", at(strings.Replace(loose, "allow: [create_issue]", "allow: [create_*]", 1), "30"), "strict", "strict", strict)},
		{"sets no session meets together", map[string]string{"strict": strings.Replace(strict, "{ roles: [dev] }", "{ users: [alice] }", 1)},
			setPut("loose", at(strings.Replace(loose, "{ roles: [dev] }", "{ users: [bob] }", 1), "30")), []string{}},
		{"rules on different events", map[string]string{"strict": strings.Replace(strict, "      mode: approve", "      events: [permission.request]\n      mode: approve", 1)},
			setPut("loose", at(loose, "30")), []string{}},
		{"an approval of another tool name", map[string]string{"strict": strict}, setPut("loose", at(strings.Replace(loose, "allow: [create_issue]", "allow: [search]", 1), "30")), []string{}},
		{"a looser gate the stricter one outranks", map[string]string{"strict": strict}, setPut("loose", loose), []string{}},
	}
	for _, tc := range cases {
		w := gainWorld()
		for name, text := range tc.live {
			w = withSet(w, name, text)
		}
		v := Check(w, stamped(w, tc.item), erin)
		if len(v.Refused) > 0 {
			t.Fatalf("%s: refused %q", tc.name, findingLines(v.Refused))
		}
		if got := guardLines(v); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// fastZ is a live set at priority 20 that matches readers and holds
// shell.exec on a ticket the person behind the agent decides, looser than
// dev-access's hold-shell, which it outranks.
var fastZ = strings.Replace(gainSet("z-fast", "{ roles: [readers] }", fastShell), "priority: 10", "priority: 20", 1)

// TestTakeoverThroughARoleChange pins that a role change that brings a live
// set whose looser approval outranks a stricter live gate takes the gate
// over for the role's holders. No set changes, so the set-level pass sees
// nothing: the closure pass reads it for a person, and the agent's own
// subject refuses it.
func TestTakeoverThroughARoleChange(t *testing.T) {
	t.Parallel()
	w := withSet(shellWorld(), "z-fast", fastZ)
	implies := gainRole("engineering", "    kind: business\n    implies: [dev, readers]\n")
	agent := Check(w, agentDraftOf(w, implies), CheckInput{Proposer: ciBot, Now: checkNow})
	want := []string{"agent.guardrail PolicySet/z-fast z-fast applies to you, and this draft lets its rule fast-shell take over from the stricter rule hold-shell of dev-access. " +
		"An agent's draft may not change what binds the agent."}
	if got := findingLines(agent.Refused); !reflect.DeepEqual(got, want) {
		t.Errorf("agent: refused\n got %q\nwant %q", got, want)
	}
	want = []string{"policy.guardrail-changed PolicySet/z-fast (tick) Rule fast-shell of z-fast will gate shell.exec in place of rule hold-shell of dev-access for holders of engineering, " +
		"who will also hold readers, with a ticket good for 1 day, decided by the person behind the agent where today it is a hold, up to 90 seconds, decided by sec-approvers. | " +
		refOf(t, devAccess+holdShell, "hold-shell") + " | " + refOf(t, fastZ, "fast-shell") + " for engineering"}
	if got := guardLines(Check(w, stamped(w, implies), erin)); !reflect.DeepEqual(got, want) {
		t.Errorf("person:\n got %q\nwant %q", got, want)
	}
}

// TestRoleChangeStopsAGate pins that removing a role, or taking it out of
// what another role implies, stops every set that matches it for the
// holders who reached it that way, whoever they are, so each gate and
// module of such a set is typed. An agent that no such set binds is not
// refused, and one whose own roles lose a module-only set is.
func TestRoleChangeStopsAGate(t *testing.T) {
	t.Parallel()
	gates := gainSet("extra-gates", "{ roles: [gatekeeper] }", "    - id: no-push\n      tools: [mcp.call]\n      apps: [github]\n      toolNames: { deny: [push_files, delete_repo] }\n")
	rego := "  escape:\n    rego: |\n      package straza.ext\n      deny contains \"no pushing\" if { input.event.toolName == \"push_files\" }\n"
	guard := gainSet("rego-guard", "{ roles: [gatekeeper] }", "    - id: noop\n      tools: [task.spawn]\n      effect: allow\n") + rego
	setup := func(sets ...string) World {
		w := gainWorld()
		w.Roles["gatekeeper"] = Role{ID: "r11", Name: "gatekeeper", Kind: RoleKindApplication, Plane: PlaneAccess}
		w.Roles["ops"] = Role{ID: "r12", Name: "ops", Kind: RoleKindBusiness, Plane: PlaneAccess}
		w.Implies["ops"] = []string{"gatekeeper"}
		w.Fingerprints["Role/gatekeeper"], w.Fingerprints["Role/ops"] = "fp-Role/gatekeeper", "fp-Role/ops"
		w.Holders["gatekeeper"] = []Holder{{Username: "alice", UserType: "human", AgencyMode: "interactive"}}
		w.Holders["ops"] = []Holder{{Username: "bob", UserType: "human", AgencyMode: "interactive"}}
		for _, text := range sets {
			doc, err := policy.Parse([]byte(text))
			if err != nil {
				t.Fatal(err)
			}
			w = withSet(w, doc.Metadata.Name, text)
		}
		return w
	}
	module := "rego@" + sha256Hex("package straza.ext\ndeny contains \"no pushing\" if { input.event.toolName == \"push_files\" }\n")[:12]
	stop := func(role, cause string) string {
		return "extra-gates will stop denying mcp.call as rule no-push does today for holders of " + role + ", because this draft " + cause + "."
	}
	cases := []struct {
		name string
		sets []string
		item Item
		want []string
	}{
		{"the role removed", []string{gates}, Item{Kind: KindRole, Name: "gatekeeper", Op: OpRemove}, []string{"policy.guardrail-changed PolicySet/extra-gates (typed extra-gates) " +
			stop("gatekeeper", "removes gatekeeper") + " " + stop("ops", "removes gatekeeper") + " | " + refOf(t, gates, "no-push") + " | unbound for gatekeeper; unbound for ops"}},
		{"the implication removed", []string{gates}, gainRole("ops", "    kind: business\n"), []string{"policy.guardrail-changed PolicySet/extra-gates (typed extra-gates) " +
			stop("ops", "takes gatekeeper out of what ops implies") + " | " + refOf(t, gates, "no-push") + " | unbound for ops"}},
		{"a module-only set", []string{guard}, gainRole("ops", "    kind: business\n"), []string{"policy.guardrail-changed PolicySet/rego-guard (typed rego-guard) " +
			"rego-guard will stop refusing calls through its Rego module as it does today for holders of ops, because this draft takes gatekeeper out of what ops implies. | " +
			module + " | unbound for ops"}},
	}
	for _, tc := range cases {
		w := setup(tc.sets...)
		if got := guardLines(Check(w, stamped(w, tc.item), erin)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
		agent := Check(w, agentDraftOf(w, tc.item), CheckInput{Proposer: ciBot, Now: checkNow})
		if len(agent.Refused) > 0 || !reflect.DeepEqual(guardLines(agent), tc.want) {
			t.Errorf("%s: an agent that no such set binds: refused %q, risks %q", tc.name, findingLines(agent.Refused), guardLines(agent))
		}
	}
	w := setup(strings.Replace(guard, "{ roles: [gatekeeper] }", "{ roles: [dev] }", 1))
	agent := Check(w, agentDraftOf(w, gainRole("engineering", "    kind: business\n")), CheckInput{Proposer: ciBot, Now: checkNow})
	want := "agent.guardrail PolicySet/rego-guard rego-guard applies to you, and this draft takes dev out of your roles so that the set no longer applies to you. " +
		"An agent's draft may not change what binds the agent."
	if !slices.Contains(findingLines(agent.Refused), want) {
		t.Errorf("an agent whose own roles lose a module-only set: refused %q, want %q", findingLines(agent.Refused), want)
	}
}

// TestWiderReadsTheWholeGate pins that own reach weighs the gate a call
// meets, not its outcome alone: a looser approval, the requester's own
// approval, and a classifier or a server check dropped each widen, though
// the outcome stays the same.
func TestWiderReadsTheWholeGate(t *testing.T) {
	t.Parallel()
	hold := &policy.ApproveSpec{Class: policy.ClassHold, TimeoutSeconds: 90, RetryTTLSeconds: 60, Binding: policy.ApproveBindingCall, Roles: []string{"sec"}}
	ticket := &policy.ApproveSpec{Class: policy.ClassTicket, TicketTTLSeconds: 86400, GrantTTLSeconds: 3600, Bind: policy.BindFingerprint, Binding: policy.ApproveBindingCall, Roles: []string{"sec"}}
	self := *hold
	self.SelfApproval = true
	cases := []struct {
		name string
		b, a probe
		want bool
	}{
		{"a hold turned ticket", probe{outcome: OutcomeApproval, approve: hold}, probe{outcome: OutcomeApproval, approve: ticket}, true},
		{"the requester may approve", probe{outcome: OutcomeApproval, approve: hold}, probe{outcome: OutcomeApproval, approve: &self}, true},
		{"a classifier dropped", probe{outcome: OutcomeRuns, classify: true}, probe{outcome: OutcomeRuns}, true},
		{"a server check dropped", probe{outcome: OutcomeApproval, approve: hold, serverCheck: true}, probe{outcome: OutcomeApproval, approve: hold}, true},
		{"a classifier added", probe{outcome: OutcomeRuns}, probe{outcome: OutcomeRuns, classify: true}, false},
		{"the same gate", probe{outcome: OutcomeApproval, approve: hold, classify: true}, probe{outcome: OutcomeApproval, approve: hold, classify: true}, false},
	}
	for _, tc := range cases {
		if got := wider(tc.b, tc.a); got != tc.want {
			t.Errorf("%s: wider = %v, want %v", tc.name, got, tc.want)
		}
	}
}
