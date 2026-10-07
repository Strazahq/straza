package drafts

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// Each test here is one probe of the verdict signals, asserting what the
// verdict must do.

// postureSet is a live set with no match whose one rule lets a call to
// github or jira through only from a managed session.
const postureSet = "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata:\n  name: posture\nspec:\n  rules:\n" +
	"    - id: managed-github\n      tools: [mcp.call]\n      apps: [github, jira]\n      require: { attestation: managed }\n"

// withSet is w with the live set name holding text.
func withSet(w World, name, text string) World {
	w.Policies[name] = Policy{Name: name, Text: text}
	w.Fingerprints["PolicySet/"+name] = Fingerprint("fp-PolicySet/" + name)
	return w
}

// withShell is w with local tools denied by default, a live application
// role shell with no row, and a live set shell-users that allows shell.exec
// to its holders.
func withShell(w World) World {
	w.LocalToolDefault = policy.EffectDeny
	w.Roles["shell"] = Role{ID: "r9", Name: "shell", Kind: RoleKindApplication, Plane: PlaneAccess}
	w.Fingerprints["Role/shell"] = "fp-Role/shell"
	return withSet(w, "shell-users", shellUsers)
}

// shellUsers is the set of withShell.
var shellUsers = gainSet("shell-users", "{ roles: [shell] }", "    - id: allow-shell\n      tools: [shell.exec]\n      effect: allow\n")

// ownReach is the agent.own-reach refusal of words.
func ownReach(words string) string {
	return "This draft would let you " + words + ", which widens your own reach. An agent's draft may not."
}

// sentences answers the sentences of fs whose code is code.
func sentences(fs []Finding, code string) []string {
	out := []string{}
	for _, f := range fs {
		if f.Code == code {
			out = append(out, f.Sentence)
		}
	}
	return out
}

// TestRequireRulesDoNotHideWidening pins that who gains reads a session
// that meets every require predicate, so a live require rule hides no
// widening, and the verdict says so in one unchecked line.
func TestRequireRulesDoNotHideWidening(t *testing.T) {
	t.Parallel()
	w := withSet(gainWorld(), "posture", postureSet)
	person := Check(w, widenReaders(w), CheckInput{Now: checkNow})
	if !slices.Contains(codesOf(person.Risks), codeUngated) {
		t.Errorf("a person's widening raised %q, want access.ungated", codesOf(person.Risks))
	}
	want := []string{"Straza read who gains what for a session that meets every require predicate of posture, the widest reach a holder can have, so a session that does not meet them reaches less."}
	if got := sentences(person.Unchecked, codeUncheckedRequire); !reflect.DeepEqual(got, want) {
		t.Errorf("unchecked.require = %q, want %q", got, want)
	}
	items := []Item{gainRole("engineering", "    kind: business\n    implies: [dev, jira-tickets]\n"),
		gainRole("jira-tickets", "    kind: application\n    server: jira\n    bindings:\n        - app: jira\n          tools: [create_issue]\n")}
	for _, refusalsOnly := range []bool{false, true} {
		v := Check(w, agentDraftOf(w, items...), CheckInput{Proposer: ciBot, Now: checkNow, RefusalsOnly: refusalsOnly})
		if got := sentences(v.Refused, codeAgentOwnReach); !reflect.DeepEqual(got, []string{ownReach("run create_issue on jira with no approval")}) {
			t.Errorf("RefusalsOnly %v: agent.own-reach = %q", refusalsOnly, got)
		}
	}
}

// TestAgentOwnReachReadsTheAgentsWholeSubject pins that an agent's own
// reach is read over every role it holds, so a looser gate in a set that
// matches one of its roles is seen on a tool another of its roles reaches.
// Here no set changes: the agent gains readers, whose live set outranks
// dev-access's hold on push_files, which dev's row reaches.
func TestAgentOwnReachReadsTheAgentsWholeSubject(t *testing.T) {
	t.Parallel()
	w := withSet(gainWorld(), "a-fast", gainSet("a-fast", "{ roles: [readers] }", "    - id: fast-push\n      tools: [mcp.call]\n      apps: [github]\n      toolNames: { allow: [push_files] }\n"+
		"      mode: approve\n      approve: { deciders: [sponsor], class: ticket }\n"))
	for _, refusalsOnly := range []bool{false, true} {
		v := Check(w, agentDraftOf(w, gainRole("engineering", "    kind: business\n    implies: [dev, readers]\n")), CheckInput{Proposer: ciBot, Now: checkNow, RefusalsOnly: refusalsOnly})
		if got := findingLines(v.Refused); !reflect.DeepEqual(got, []string{"agent.own-reach App/github " + ownReach("reach push_files on github under a looser approval")}) {
			t.Errorf("RefusalsOnly %v: refused %q", refusalsOnly, got)
		}
	}
}

// fastShell is an added gate that the engine picks before dev-access's
// hold on shell.exec, and that is looser: a ticket the person behind the
// agent decides.
const fastShell = "    - id: fast-shell\n      tools: [shell.exec]\n      mode: approve\n      effect: allow\n      approve: { deciders: [sponsor], class: ticket }\n"

// TestAddedGateThatTakesOverALooserOneWidens pins that an added approve
// rule that the engine picks before a stricter live gate on the same calls
// loosens it, so an agent is refused and a person reads a gate change.
func TestAddedGateThatTakesOverALooserOneWidens(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	live := devAccess + "    - id: hold-shell\n      tools: [shell.exec]\n      mode: approve\n      effect: allow\n      approve: { roles: [sec-approvers] }\n"
	w.Policies["dev-access"] = Policy{Name: "dev-access", Text: live}
	newSet := Item{Kind: KindPolicySet, Name: "a-fast", Op: OpPut, Doc: gainSet("a-fast", "{ roles: [dev] }", fastShell)}
	inserted := Item{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: strings.Replace(live, "    - id: hold-push\n", fastShell+"    - id: hold-push\n", 1)}
	for _, tc := range []struct {
		item Item
		want string
	}{
		{newSet, "a-fast applies to you, and this draft adds or widens rule fast-shell. An agent's draft may not change what binds the agent."},
		{inserted, "dev-access applies to you, and this draft adds or widens rule fast-shell. An agent's draft may not change what binds the agent."},
	} {
		v := Check(w, agentDraftOf(w, tc.item), CheckInput{Proposer: ciBot, Now: checkNow})
		if got := sentences(v.Refused, codeAgentGuardrail); !reflect.DeepEqual(got, []string{tc.want}) {
			t.Errorf("%s: agent.guardrail = %q", tc.item.Name, got)
		}
	}
	p := Check(w, stamped(w, newSet), CheckInput{Proposer: Holder{Username: "carol", UserType: "human"}, Now: checkNow})
	want := []string{"Rule fast-shell of a-fast will gate shell.exec in place of rule hold-shell of dev-access, with a ticket good for 1 day, " +
		"decided by the person behind the agent where today it is a hold, up to 90 seconds, decided by sec-approvers."}
	if got := sentences(p.Risks, codeGuardrail); !reflect.DeepEqual(got, want) {
		t.Errorf("a person's policy.guardrail-changed = %q, want %q", got, want)
	}
}

// TestSetThatStartsToBindTheAgentIsJudged pins that a set that starts to
// apply to the agent through its own roles is judged as a set the draft
// creates, so an allow rule in it is refused.
func TestSetThatStartsToBindTheAgentIsJudged(t *testing.T) {
	t.Parallel()
	w := withShell(gainWorld())
	v := Check(w, agentDraftOf(w, gainRole("engineering", "    kind: business\n    implies: [dev, shell]\n")), CheckInput{Proposer: ciBot, Now: checkNow})
	want := []string{"shell-users applies to you, and this draft adds or widens rule allow-shell. An agent's draft may not change what binds the agent."}
	if got := sentences(v.Refused, codeAgentGuardrail); !reflect.DeepEqual(got, want) {
		t.Errorf("agent.guardrail = %q, want %q", got, want)
	}
}

// TestLocalAllowReadsAWiderMatch pins that a set that allows a local tool
// the deployment denies by default raises policy.local-allow when its match
// widens, though its rule stays the same.
func TestLocalAllowReadsAWiderMatch(t *testing.T) {
	t.Parallel()
	w := withShell(gainWorld())
	want := []string{"Rule allow-shell of shell-users allows shell.exec on managed machines, where this deployment denies it by default."}
	for _, match := range []string{"  match: { roles: [shell, dev] }\n", "  match: {}\n"} {
		doc := strings.Replace(shellUsers, "  match: { roles: [shell] }\n", match, 1)
		v := Check(w, stamped(w, Item{Kind: KindPolicySet, Name: "shell-users", Op: OpPut, Doc: doc}), CheckInput{Proposer: Holder{Username: "carol", UserType: "human"}, Now: checkNow})
		if got := sentences(v.Risks, codeLocalAllow); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: policy.local-allow = %q, want %q", match, got, want)
		}
	}
}

// TestAgentViewAnswersNothingAboutANamedPerson pins that a set that selects
// one person by username tells the agent nothing about the roles that
// person holds. The views differ only in the words of
// policy.identity-scoped-change, the hash of the agent's own document.
func TestAgentViewAnswersNothingAboutANamedPerson(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	var views []AgentVerdict
	for _, who := range []string{"alice", "carol", "dana", "nobody"} {
		probe := gainSet("probe", "{ users: ["+who+"] }", "    - id: x\n      tools: [mcp.call]\n      effect: deny\n")
		view := Check(w, agentDraftOf(w, Item{Kind: KindPolicySet, Name: "probe", Op: OpPut, Doc: probe}), CheckInput{Proposer: ciBot, Now: checkNow}).ForAgent()
		for i, f := range view.Risks {
			if f.Code == codeIdentityScoped && f.After == sha256Hex(probe)[:12] {
				view.Risks[i].After = ""
			}
		}
		views = append(views, view)
	}
	for i := range views[1:] {
		if !reflect.DeepEqual(views[i+1], views[0]) {
			t.Errorf("the agent's view differs by the person the set names:\n%+v\n%+v", views[0], views[i+1])
		}
	}
}

// TestAgentOwnReachSeesTheBuiltInApp pins that a set that opens the
// built-in straza app's tools to the agent by username widens the agent's
// own reach.
func TestAgentOwnReachSeesTheBuiltInApp(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	bot := Holder{Username: "helper-bot", Agent: true, UserType: "agent", AgencyMode: "supervised", Sponsor: "bob"}
	w.Holders[DraftConfigRole] = append(w.Holders[DraftConfigRole], bot)
	set := gainSet("helper", "{ users: [helper-bot] }", "    - id: ask\n      tools: [mcp.call]\n      apps: [straza]\n      toolNames: { allow: [approval_request, approval_await] }\n"+
		"      mode: approve\n      approve: { roles: [sec-approvers] }\n")
	d := agentDraftOf(w, Item{Kind: KindPolicySet, Name: "helper", Op: OpPut, Doc: set})
	d.Authors[0].Username = bot.Username
	v := Check(w, d, CheckInput{Proposer: bot, Now: checkNow})
	want := []string{ownReach("reach approval_await and approval_request of the built-in straza MCP server with an approval")}
	if got := sentences(v.Refused, codeAgentOwnReach); !reflect.DeepEqual(got, want) {
		t.Errorf("agent.own-reach = %q, want %q", got, want)
	}
}

// TestAServerThatDoesNotRunHidesNoWidening pins that who gains reads the
// last tools a server listed whether it runs or not.
func TestAServerThatDoesNotRunHidesNoWidening(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"running", "stopped", "failed"} {
		w := gainWorld()
		github := w.Apps["github"]
		github.Status = status
		w.Apps["github"] = github
		bot := Holder{Username: "ro-bot", Agent: true, UserType: "agent", AgencyMode: "autonomous", Sponsor: "carol"}
		w.Holders["readers"] = append(w.Holders["readers"], bot)
		d := agentDraftOf(w, widenReaders(w).Items...)
		d.Authors[0].Username = bot.Username
		v := Check(w, d, CheckInput{Proposer: bot, Now: checkNow})
		if got := sentences(v.Refused, codeAgentOwnReach); !reflect.DeepEqual(got, []string{ownReach("run delete_repo and push_files on github with no approval")}) {
			t.Errorf("github %s: agent.own-reach = %q", status, got)
		}
	}
}

// TestCheckRunsNoLiveRegoModule pins that the check runs no Rego module, a
// live one included, and names the sets whose modules it did not run.
func TestCheckRunsNoLiveRegoModule(t *testing.T) {
	t.Parallel()
	rego := "  escape:\n    rego: |\n      package straza.ext\n      deny contains \"rego says no\" if { input.event.toolName == \"get_me\" }\n"
	w := gainWorld()
	w.Policies["dev-access"] = Policy{Name: "dev-access", Text: devAccess + rego}
	v := Check(w, stamped(w, Item{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: strings.Replace(devAccess+rego, "roles: [dev]", "roles: [dev, readers]", 1)}),
		CheckInput{Now: checkNow})
	for _, g := range v.Gains {
		if strings.Contains(g.BeforeWords+g.AfterWords, "rego") {
			t.Errorf("the check ran a Rego module: %q", gainLines([]Gain{g}))
		}
	}
	want := []string{"Straza read who gains what without running the Rego modules of dev-access, which only refuse calls, so a holder may reach less than it shows."}
	if got := sentences(v.Unchecked, codeUncheckedRego); !reflect.DeepEqual(got, want) {
		t.Errorf("unchecked.rego = %q, want %q", got, want)
	}
}

// TestRevertLossySitsOnTheUndo pins that the undo of a published draft says
// what re-creating an object cannot bring back, and a removal says nothing
// of an undo.
func TestRevertLossySitsOnTheUndo(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	undo := stamped(w, appItem("gitlab"), gainRole("ops", "    kind: business\n"))
	undo.Reverts = "40"
	v := Check(w, undo, CheckInput{Apps: map[string]App{"gitlab": {Runtime: "remote", URL: "https://gitlab.example.com/mcp", Credential: CredentialNone}}, Now: checkNow})
	want := []string{
		"revert.lossy App/gitlab Undoing draft 40 re-creates gitlab, and does not bring back its stored secrets, each person's connection or a pause. | After publishing, store its secrets again with strazactl apps secret set gitlab, and each person runs straza connect gitlab.",
		"revert.lossy Role/ops Undoing draft 40 re-creates ops, and does not bring back its memberships. | Assign ops again in your identity manager, or with strazactl assign ops --user <name>.",
	}
	if got := only(warningLines(v.Warnings), codeRevertLossy); !reflect.DeepEqual(got, want) {
		t.Errorf("revert.lossy\n got %q\nwant %q", got, want)
	}
	removal := Check(w, stamped(w, Item{Kind: KindApp, Name: "github", Op: OpRemove}, Item{Kind: KindRole, Name: "readers", Op: OpRemove}), CheckInput{Now: checkNow})
	if got := only(warningLines(removal.Warnings), codeRevertLossy); len(got) > 0 {
		t.Errorf("a removal carries %q", got)
	}
}

// TestEveryStarRowReachesToolsAddedLater pins that a row made only of *
// reaches every tool, and tools the server adds later.
func TestEveryStarRowReachesToolsAddedLater(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	v := Check(w, stamped(w, gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: ['**']\n")), CheckInput{Now: checkNow})
	if !slices.Contains(codesOf(v.Risks), codeToolsLater) {
		t.Errorf("a row of ** raised %q, want access.tools-later", codesOf(v.Risks))
	}
}

// TestRecordingThatWidensItsMatchIsARisk pins that a set that records
// conversations and widens its match records more sessions.
func TestRecordingThatWidensItsMatchIsARisk(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	rec := strings.Replace(devAccess, "  match: { roles: [dev] }\n", "  match: { roles: [dev] }\n  capture: { conversations: true }\n", 1)
	w.Policies["dev-access"] = Policy{Name: "dev-access", Text: rec}
	v := Check(w, stamped(w, Item{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: strings.Replace(rec, "roles: [dev]", "roles: [dev, readers]", 1)}),
		CheckInput{Proposer: Holder{Username: "carol", UserType: "human"}, Now: checkNow})
	want := []string{`policy.recording-on PolicySet/dev-access (typed dev-access) Conversations of the sessions dev-access matches will be recorded verbatim. | verbatim {"roles":["dev"]} | verbatim {"roles":["dev","readers"]}`}
	if got := only(riskLines(v.Risks), codeRecordingOn); !reflect.DeepEqual(got, want) {
		t.Errorf("policy.recording-on\n got %q\nwant %q", got, want)
	}
}

// TestGateSetThatOnlyWidensItsMatchStopsNothing pins that a gate set whose
// match only widens stops gating nobody, and one whose match narrows does.
func TestGateSetThatOnlyWidensItsMatchStopsNothing(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	carol := CheckInput{Proposer: Holder{Username: "carol", UserType: "human"}, Now: checkNow}
	wider := Check(w, stamped(w, devAccessWith("roles: [dev]", "roles: [dev, readers]")), carol)
	if got := only(riskLines(wider.Risks), codeGuardrail); len(got) > 0 {
		t.Errorf("a wider match raised %q", got)
	}
	narrower := Check(w, stamped(w, devAccessWith("  match: { roles: [dev] }\n", "  match: { roles: [dev], users: [alice] }\n")), carol)
	if got := only(riskLines(narrower.Risks), codeGuardrail); len(got) == 0 {
		t.Error("a narrower match raised no policy.guardrail-changed")
	}
}

// TestTicketArgsSaysOnlyWhatItKnows pins that ready.ticket-args says what
// the binding does, and leaves whether the arguments change to the reader.
func TestTicketArgsSaysOnlyWhatItKnows(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	set := gainSet("readers-access", "{ roles: [readers] }", "    - id: hold\n      tools: [mcp.call]\n      toolNames: { allow: [postMessage] }\n      mode: approve\n"+
		"      approve: { roles: [sec-approvers], class: ticket }\n")
	v := Check(w, stamped(w, Item{Kind: KindPolicySet, Name: "readers-access", Op: OpPut, Doc: set}), CheckInput{Now: checkNow})
	want := []string{"ready.ticket-args PolicySet/readers-access Rule hold of readers-access binds a ticket to the exact arguments of postMessage, " +
		"so an approval is used only by a later call with the same arguments. | If the arguments of postMessage change on every call, set approve.binding: tool, which lets one approval cover any arguments of postMessage."}
	if got := only(warningLines(v.Warnings), codeTicketArgs); !reflect.DeepEqual(got, want) {
		t.Errorf("ready.ticket-args\n got %q\nwant %q", got, want)
	}
}

// commandManifest is the canonical JSON of a command server's manifest
// whose one env entry is NODE_OPTIONS=value.
func commandManifest(value string) string {
	b, _ := json.Marshal(map[string]any{"straza": map[string]any{"runtime": map[string]any{"kind": "command",
		"command": map[string]any{"exec": "npx", "args": []string{"-y", "@acme/mcp"}, "env": []map[string]string{{"name": "NODE_OPTIONS", "value": value}}}}}})
	return string(b)
}

// TestEnvValueChangeRunsNewCode pins that a changed env value changes the
// code that runs, and that the words name the entry, never its value or a
// digest of it.
func TestEnvValueChangeRunsNewCode(t *testing.T) {
	t.Parallel()
	live := App{Runtime: runtimeCommand, Exec: "npx", Args: []string{"-y", "@acme/mcp"}, EnvNames: []string{"NODE_OPTIONS"}, Manifest: commandManifest("--max-old-space-size=512")}
	next := live
	next.Manifest = commandManifest("--require=/tmp/evil.js")
	got := runsCode("x", live, true, next)
	if len(got) != 1 || got[0].Before != "command npx 2 args env NODE_OPTIONS" || got[0].After != "command npx 2 args env NODE_OPTIONS (value changed)" {
		t.Errorf("runsCode = %q", riskLines(got))
	}
}

// TestRunsCodeWordsCarryNoEnvValue pins that server.runs-code's words, and
// so its key, carry no digest of an env value: two drafts that differ only
// in an env value that did not change read the same words and key, and a
// changed value reads as its name marked "(value changed)" whatever the new
// value is, so a guessed LOG_LEVEL value never matches a key.
func TestRunsCodeWordsCarryNoEnvValue(t *testing.T) {
	t.Parallel()
	runner := func(exec, level string) App {
		b, _ := json.Marshal(map[string]any{"straza": map[string]any{"runtime": map[string]any{"kind": "command",
			"command": map[string]any{"exec": exec, "env": []map[string]string{{"name": "LOG_LEVEL", "value": level}}}}}})
		return App{Runtime: runtimeCommand, Exec: exec, EnvNames: []string{"LOG_LEVEL"}, Manifest: string(b)}
	}
	one := func(fs []Finding) Finding {
		t.Helper()
		if len(fs) != 1 {
			t.Fatalf("runsCode = %q, want one risk", riskLines(fs))
		}
		return fs[0]
	}
	same := func(name string, a, b Finding) {
		t.Helper()
		if a.Before != b.Before || a.After != b.After || a.Sentence != b.Sentence || a.Key() != b.Key() {
			t.Errorf("%s: %q and %q differ", name, riskLines([]Finding{a}), riskLines([]Finding{b}))
		}
	}
	same("a moved exec beside a value that stays debug or stays info",
		one(runsCode("runner", runner("/usr/bin/runner", "debug"), true, runner("/usr/bin/runner2", "debug"))),
		one(runsCode("runner", runner("/usr/bin/runner", "info"), true, runner("/usr/bin/runner2", "info"))))
	fresh := one(runsCode("runner", App{}, false, runner("/usr/bin/runner", "debug")))
	same("a new server", fresh, one(runsCode("runner", App{}, false, runner("/usr/bin/runner", "info"))))
	changed := one(runsCode("runner", runner("/usr/bin/runner", "debug"), true, runner("/usr/bin/runner", "info")))
	same("a value changed to info or to warn", changed, one(runsCode("runner", runner("/usr/bin/runner", "debug"), true, runner("/usr/bin/runner", "warn"))))
	if changed.Before != "command /usr/bin/runner 0 args env LOG_LEVEL" || changed.After != "command /usr/bin/runner 0 args env LOG_LEVEL (value changed)" {
		t.Errorf("a changed value reads %q, want its name marked", riskLines([]Finding{changed}))
	}
	if got := runsCode("runner", runner("/usr/bin/runner", "debug"), true, runner("/usr/bin/runner", "debug")); len(got) != 0 {
		t.Errorf("an unchanged runtime = %q, want no risk", riskLines(got))
	}
	for _, guess := range []string{"info", "warn", "debug"} {
		if (Finding{Code: codeRunsCode, Object: "App/runner", After: "command /usr/bin/runner env LOG_LEVEL@" + sha256Hex(guess)[:12]}).Key() == fresh.Key() {
			t.Errorf("the guess LOG_LEVEL=%s matches the key of a new runner", guess)
		}
	}
}
