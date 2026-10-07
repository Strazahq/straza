package drafts

import (
	"cmp"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// riskLines spells each risk on one line: its code, object, how it is
// acknowledged, and its sentence, before and after words.
func riskLines(fs []Finding) []string {
	out := []string{}
	for _, f := range fs {
		ack := string(f.Ack)
		if f.Ack == AckTyped {
			ack += " " + f.Typed
		}
		out = append(out, f.Code+" "+f.Object+" ("+ack+") "+f.Sentence+" | "+cmp.Or(f.Before, "-")+" | "+cmp.Or(f.After, "-"))
	}
	return out
}

// codesOf answers the code of each finding.
func codesOf(fs []Finding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Code)
	}
	return out
}

// devAccessWith is devAccess with old replaced by repl, once.
func devAccessWith(old, repl string) Item {
	return Item{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: strings.Replace(devAccess, old, repl, 1)}
}

// refOf is the reference of the rule id of the set text.
func refOf(t *testing.T, text, id string) string {
	t.Helper()
	doc, err := policy.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return ruleRef(doc.Spec.Rules[ruleIndex(doc, id)])
}

func TestAccessRisks(t *testing.T) {
	t.Parallel()
	hold := "a hold, up to 2 minutes, decided by sec-approvers"
	ticket := strings.Replace(devAccess, "timeoutSeconds: 120", "class: ticket", 1)
	cases := []struct {
		name   string
		prefix string
		items  []Item
		want   []string
	}{
		{"a held role reaches tools that run with no gate", "", []Item{gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: ['*']\n")}, []string{
			"access.tools-later Role/readers (tick) readers will reach every tool on github, including tools the server adds later. | get_me | *",
			"access.ungated Role/readers (typed readers) delete_repo and push_files on github will run for holders of readers with no rule gating it. | github/delete_repo, github/push_files | github/delete_repo, github/push_files"}},
		{"a role nobody holds raises nothing", "", []Item{gainRole("github-writers", "    kind: application\n    server: github\n    bindings:\n        - app: github\n          tools: ['*']\n")}, []string{}},
		{"a held role reaches a gated tool", "", []Item{gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: [get_me, push_files]\n"),
			{Kind: KindPolicySet, Name: "readers-access", Op: OpPut, Doc: gainSet("readers-access", "{ roles: [readers] }",
				"    - id: hold-push\n      tools: [mcp.call]\n      toolNames: { allow: [push_files] }\n      mode: approve\n      approve: { roles: [sec-approvers] }\n")}}, []string{
			"access.gated Role/readers (tick) readers will reach push_files on github, each call a hold, up to 90 seconds, decided by sec-approvers. | - | github/push_files=a hold, up to 90 seconds, decided by sec-approvers"}},
		{"a gate grows looser for every holder", "", []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: ticket}}, []string{
			"policy.guardrail-changed PolicySet/dev-access (tick) Rule hold-push of dev-access will gate mcp.call with a ticket good for 1 day, decided by sec-approvers where today it is " + hold + ". | " +
				refOf(t, devAccess, "hold-push") + " | " + refOf(t, ticket, "hold-push"),
			"access.gate-looser Role/dev (tick) push_files on github will be gated by a ticket good for 1 day, decided by sec-approvers instead of " + hold + ". | github/push_files=" + hold + " | github/push_files=a ticket good for 1 day, decided by sec-approvers",
			"access.gate-looser Role/engineering (tick) push_files on github will be gated by a ticket good for 1 day, decided by sec-approvers instead of " + hold + ". | github/push_files=" + hold + " | github/push_files=a ticket good for 1 day, decided by sec-approvers"}},
		{"a deny that goes away", "", []Item{devAccessWith("    - id: no-delete\n      tools: [mcp.call]\n      toolNames: { deny: [delete_repo] }\n", "")}, []string{
			"policy.guardrail-changed PolicySet/dev-access (typed dev-access) dev-access will stop denying mcp.call as rule no-delete does today, because this draft changes the rule. | " +
				refOf(t, devAccess, "no-delete") + " | no-delete@removed",
			"access.deny-removed Role/dev (tick) No rule will refuse delete_repo on github for dev any more. It will run. | github/delete_repo=denied by rule no-delete of dev-access | github/delete_repo",
			"access.ungated Role/dev (typed dev) delete_repo on github will run for holders of dev with no rule gating it, where today denied by rule no-delete of dev-access. | github/delete_repo=denied by rule no-delete of dev-access | github/delete_repo",
			"access.deny-removed Role/engineering (tick) No rule will refuse delete_repo on github for engineering any more. It will run. | github/delete_repo=denied by rule no-delete of dev-access | github/delete_repo",
			"access.ungated Role/engineering (typed engineering) delete_repo on github will run for holders of engineering with no rule gating it, where today denied by rule no-delete of dev-access. | github/delete_repo=denied by rule no-delete of dev-access | github/delete_repo"}},
		{"a requester who may approve their own call", "", []Item{devAccessWith("timeoutSeconds: 120", "timeoutSeconds: 120, selfApproval: true")}, []string{
			"policy.guardrail-changed PolicySet/dev-access (typed dev-access) Rule hold-push of dev-access will gate mcp.call with " + hold + " or the requester where today it is " + hold + ". | " +
				refOf(t, devAccess, "hold-push") + " | " + refOf(t, strings.Replace(devAccess, "timeoutSeconds: 120", "timeoutSeconds: 120, selfApproval: true", 1), "hold-push"),
			"access.self-approval Role/dev (typed dev) A person who asks to run push_files on github may approve it themself. | - | github/push_files",
			"access.self-approval Role/engineering (typed engineering) A person who asks to run push_files on github may approve it themself. | - | github/push_files"}},
		{"a built-in tool opened to a held role", "", []Item{{Kind: KindPolicySet, Name: "readers-access", Op: OpPut, Doc: gainSet("readers-access", "{ roles: [readers] }",
			"    - id: tickets\n      tools: [mcp.call]\n      apps: [straza]\n      toolNames: { allow: [approval_request] }\n")}}, []string{
			"policy.native-opened Role/readers (tick) Holders of readers will see approval_request of the built-in straza MCP server. | - | straza/approval_request"}},
		{"a role nobody holds that reaches a Straza role", "", []Item{gainRole("ops", "    kind: business\n    implies: ["+AdminRole+"]\n")}, []string{
			"role.straza-reach Role/ops (typed ops) Holders of ops will also hold straza-admin, which is full control of Straza. | - | straza-admin"}},
		{"a new implication and a Straza role reached", "", []Item{gainRole("engineering", "    kind: business\n    implies: [dev, readers, "+AdminRole+"]\n")}, []string{
			"access.implication Role/engineering (tick) Holders of engineering will also hold readers and reach what it reaches. | - | readers",
			"role.straza-reach Role/engineering (typed engineering) Holders of engineering will also hold straza-admin, which is full control of Straza. | - | straza-admin"}},
		{"a gate that lists today's tools beside a row for every tool", codeToolsLater, []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: gainSet("dev-access", "{ roles: [dev] }",
			"    - id: hold-all\n      tools: [mcp.call]\n      apps: [github]\n      toolNames: { allow: [delete_repo, get_me, push_files] }\n      mode: approve\n      approve: { roles: [sec-approvers] }\n")}},
			[]string{"access.tools-later Role/dev (tick) dev will reach every tool on github, including tools the server adds later. | * | *"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := gainWorld()
			v := Check(w, stamped(w, tc.items...), CheckInput{Now: checkNow})
			if len(v.Refused) > 0 {
				t.Fatalf("refused: %q", findingLines(v.Refused))
			}
			if got := only(riskLines(v.Risks), tc.prefix); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Risks\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestServerRisks(t *testing.T) {
	t.Parallel()
	remote := func(url string) App {
		return App{Runtime: "remote", URL: url, Credential: CredentialNone, Exposure: []string{"*"}, AdminRole: "mcp-admin-x", RolePrefix: "x-"}
	}
	with := func(a App, change func(*App)) App {
		change(&a)
		return a
	}
	github := func(change func(*App)) App { return with(remote("https://api.github.com/mcp"), change) }
	cases := []struct {
		name string
		item Item
		app  App
		want []string
	}{
		{"a new command server", appItem("local"),
			App{Runtime: "command", Exec: "npx", Args: []string{"-y", "@acme/mcp"}, EnvNames: []string{"B", "A"}, Credential: CredentialNone}, []string{
				"server.runs-code App/local (typed local) Publishing starts npx on the Straza host as Straza's own user, now and after every restart. | - | command npx 2 args env A,B"}},
		{"a new container with no sandbox", appItem("box"),
			App{Runtime: "oci", Image: "ghcr.io/acme/mcp:1", Sandbox: "none", Credential: CredentialNone}, []string{
				"server.runs-code App/box (typed box) Publishing runs the container image ghcr.io/acme/mcp:1 on the Straza host, with no sandbox, now and after every restart. | - | oci ghcr.io/acme/mcp:1 sandbox none"}},
		{"a credential sent to a new host", appItem("github"),
			github(func(a *App) {
				a.URL, a.Credential, a.Provider, a.Agents = "https://api.githubcopilot.com/mcp?tenant=acme", CredentialOAuth, "entra", "own"
			}), []string{
				"server.credential-host App/github (typed api.githubcopilot.com) Straza will send every caller's sign-in to api.githubcopilot.com, a host github has not used before. | api.github.com | oauth api.githubcopilot.com",
				"server.new-host App/github (tick) github will be reached at api.githubcopilot.com, a host no server uses today. | api.github.com/mcp | api.githubcopilot.com/mcp"}},
		{"a new server at a metadata address", appItem("meta"), remote("http://169.254.169.254/latest"), []string{
			"server.new-host App/meta (tick) meta will be reached at 169.254.169.254, a host no server uses today. | - | 169.254.169.254/latest",
			"server.unusual-host App/meta (typed 169.254.169.254) meta is reached at 169.254.169.254, a cloud metadata address, where Straza's own requests reach what they should not. Straza does not dial such an address, so the server would stay degraded until its address changes. | - | 169.254.169.254 cloud metadata"}},
		{"a new server at a punycode name", appItem("puny"), remote("https://xn--mnchen-3ya.de/mcp"), []string{
			"server.new-host App/puny (tick) puny will be reached at xn--mnchen-3ya.de, a host no server uses today. | - | xn--mnchen-3ya.de/mcp",
			"server.unusual-host App/puny (typed xn--mnchen-3ya.de) puny is reached at xn--mnchen-3ya.de, a name with a punycode label that reads m\u00fcnchen.de, where Straza's own requests reach what they should not. | - | xn--mnchen-3ya.de punycode"}},
		{"a path moved on the same host", appItem("github"), github(func(a *App) { a.URL = "https://api.github.com:8443/v2/mcp" }), []string{
			"server.new-host App/github (tick) The address of github moves to :8443/v2/mcp on the same host. | api.github.com/mcp | api.github.com:8443/v2/mcp"}},
		{"agents use the shared account", appItem("github"), github(func(a *App) { a.Credential, a.Agents = CredentialToken, AgentsShared }), []string{
			"server.agents-credential App/github (typed github) Agents with no sign-in of their own will use the shared account of github. | - | shared"}},
		{"agents use their own client at a provider", appItem("github"),
			github(func(a *App) {
				a.Credential, a.Agents, a.Provider = CredentialOAuth, credentialClientCredentials, "okta"
			}), []string{
				"server.agents-credential App/github (typed okta) Agents with no sign-in of their own will use a token of their own client at the provider okta for github. | - | client_credentials okta"}},
		{"the exposure offers more tools", appItem("jira"),
			with(remote("https://jira.example.com/mcp"), func(a *App) { a.Exposure = []string{"*"} }), []string{
				"server.exposure-wider App/jira (tick) jira will offer more of its tools, among them delete_issue. | create_issue | *"}},
		{"a caller's own token becomes a shared account", appItem("jira"),
			with(remote("https://jira.example.com/mcp"), func(a *App) { a.Credential, a.Exposure = credentialStatic, []string{"create_issue"} }), []string{
				"server.shared-account App/jira (tick) Calls to jira will use one shared account instead of each person's own sign-in, so jira sees one identity. | token | shared"}},
		{"a server removed", Item{Kind: KindApp, Name: "github", Op: OpRemove}, App{}, []string{
			"server.removal App/github (typed github) Publishing removes github: its access rows, its stored secrets and every person's connection go, with the roles it owns and its admin role. | fp-App/github | removed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := gainWorld()
			w.Providers = map[string]Provider{"entra": {Name: "entra"}, "okta": {Name: "okta", ClientCredentials: true}}
			jira := w.Apps["jira"]
			jira.Credential, jira.Exposure, jira.Offered = CredentialToken, []string{"create_issue"}, []string{"create_issue", "delete_issue"}
			w.Apps["jira"] = jira
			var apps map[string]App
			if tc.item.Op == OpPut {
				apps = map[string]App{tc.item.Name: tc.app}
			}
			v := Check(w, stamped(w, tc.item), CheckInput{Apps: apps, Now: checkNow})
			if len(v.Refused) > 0 {
				t.Fatalf("refused: %q", findingLines(v.Refused))
			}
			got := slices.DeleteFunc(riskLines(v.Risks), func(l string) bool { return !strings.HasPrefix(l, "server.") })
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Risks\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestPolicyRisks(t *testing.T) {
	t.Parallel()
	recording := devAccess + "  capture: { conversations: true }\n"
	requireSet := func(require string) string {
		return gainSet("posture", "{ roles: [readers] }", "    - id: managed\n      tools: [shell.exec]\n      effect: allow\n      require: "+require+"\n")
	}
	shell := devAccess + "    - id: allow-shell\n      tools: [shell.exec]\n      effect: allow\n"
	cases := []struct {
		name  string
		live  map[string]string
		deny  bool
		items []Item
		want  []string
	}{
		{"recording turned on", nil, false, []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: recording}}, []string{
			"policy.recording-on PolicySet/dev-access (typed dev-access) Conversations of the sessions dev-access matches will be recorded verbatim. | off | verbatim"}},
		{"recording moved to masked", map[string]string{"dev-access": strings.Replace(recording, "true }", "true, mode: redact }", 1)}, false,
			[]Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: recording}}, []string{
				"policy.recording-on PolicySet/dev-access (typed dev-access) Conversations of the sessions dev-access matches will be recorded verbatim. | redact | verbatim"}},
		{"recording turned off", map[string]string{"dev-access": recording}, false, []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: devAccess}}, []string{
			"policy.recording-off PolicySet/dev-access (tick) Conversations of the sessions dev-access matches will stop being recorded. | verbatim | off"}},
		{"a require predicate loosened", map[string]string{"posture": requireSet("{ attestation: managed, deviceCert: true }")}, false,
			[]Item{{Kind: KindPolicySet, Name: "posture", Op: OpPut, Doc: requireSet("{ attestation: advisory }")}}, []string{
				"policy.require-looser PolicySet/posture (tick) Rule managed of posture will no longer require attestation managed. Rule managed of posture will no longer require a device certificate." +
					" | managed: a device certificate; managed: attestation managed | managed: attestation advisory; managed: none"}},
		{"an allow of a local tool where the deployment denies by default", nil, true, []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: shell}}, []string{
			"policy.local-allow PolicySet/dev-access (tick) Rule allow-shell of dev-access allows shell.exec on managed machines, where this deployment denies it by default. | - | allow-shell: shell.exec"}},
		{"the same allow where the deployment allows by default", nil, false, []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: shell}}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := gainWorld()
			for name, text := range tc.live {
				w.Policies[name] = Policy{Name: name, Text: text}
				w.Fingerprints["PolicySet/"+name] = "fp-" + Fingerprint(name)
			}
			if tc.deny {
				w.LocalToolDefault = policy.EffectDeny
			}
			v := Check(w, stamped(w, tc.items...), CheckInput{Now: checkNow})
			if len(v.Refused) > 0 {
				t.Fatalf("refused: %q", findingLines(v.Refused))
			}
			got := slices.DeleteFunc(riskLines(v.Risks), func(l string) bool { return !strings.HasPrefix(l, "policy.") || strings.HasPrefix(l, codeGuardrail) })
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Risks\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// ciBot is the agent of gainWorld, which holds engineering and so dev, and
// which dev-access binds.
var ciBot = Holder{Username: "ci-bot", Agent: true, UserType: "agent", AgencyMode: "autonomous", Sponsor: "bob"}

// agentDraftOf is a draft of items through the straza-app door, stamped.
func agentDraftOf(w World, items ...Item) Draft {
	d := stamped(w, items...)
	d.Door, d.Authors = DoorAgent, []Principal{{Username: ciBot.Username, Agent: true, SponsorName: "bob", SponsorID: "u-bob"}}
	return d
}

// TestAgentGuardrail pins every case of the guardrail for an agent: a set
// with a gate that binds it may not be removed, turned off, narrowed away
// from it or changed but for added gates, added denies and reason text, and
// a set the draft creates counts as changed.
func TestAgentGuardrail(t *testing.T) {
	t.Parallel()
	fix := "Leave dev-access and your roles as they are. A person can change them."
	refused := func(set, clause string) string {
		return "agent.guardrail PolicySet/" + set + " " + set + " applies to you, and this draft " + clause + ". An agent's draft may not change what binds the agent."
	}
	cases := []struct {
		name  string
		items []Item
		want  []string
	}{
		{"the set removed", []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpRemove}}, []string{refused("dev-access", "removes it")}},
		{"the set turned off", []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpOff, Doc: devAccess}}, []string{refused("dev-access", "turns it off")}},
		{"its match narrowed away from the agent", []Item{devAccessWith("roles: [dev]", "roles: [readers]")}, []string{refused("dev-access", "changes its match or priority")}},
		{"its priority changed", []Item{devAccessWith("priority: 10", "priority: 20")}, []string{refused("dev-access", "changes its match or priority")}},
		{"a role taken out of the agent's roles", []Item{gainRole("engineering", "    kind: business\n")},
			[]string{refused("dev-access", "takes dev out of your roles so that the set no longer applies to you")}},
		{"a gate changed", []Item{devAccessWith("timeoutSeconds: 120", "timeoutSeconds: 600")}, []string{refused("dev-access", "changes rule hold-push")}},
		{"a gate removed", []Item{devAccessWith("    - id: no-delete\n      tools: [mcp.call]\n      toolNames: { deny: [delete_repo] }\n", "")},
			[]string{refused("dev-access", "changes rule no-delete")}},
		{"an allow rule added", []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: devAccess + "    - id: allow-shell\n      tools: [shell.exec]\n      effect: allow\n"}},
			[]string{refused("dev-access", "adds or widens rule allow-shell")}},
		{"a new set with an allow rule that binds the agent", []Item{{Kind: KindPolicySet, Name: "extra", Op: OpPut,
			Doc: gainSet("extra", "{ roles: [dev] }", "    - id: allow-shell\n      tools: [shell.exec]\n      effect: allow\n")}},
			[]string{refused("extra", "adds or widens rule allow-shell")}},
		{"a gate added for a tool the deployment allows by default", []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: devAccess +
			"    - id: hold-shell\n      tools: [shell.exec]\n      mode: approve\n      effect: allow\n      approve: { roles: [sec-approvers] }\n"}}, []string{}},
		{"a deny added", []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: devAccess + "    - id: no-shell\n      tools: [shell.exec]\n      effect: deny\n"}}, []string{}},
		{"reason text changed", []Item{devAccessWith("toolNames: { deny: [delete_repo] }\n", "toolNames: { deny: [delete_repo] }\n      reason: never delete\n")}, []string{}},
		{"a new set with gates only", []Item{{Kind: KindPolicySet, Name: "extra", Op: OpPut,
			Doc: gainSet("extra", "{ roles: [dev] }", "    - id: no-shell\n      tools: [shell.exec]\n      effect: deny\n")}}, []string{}},
		{"a set that does not bind the agent", []Item{{Kind: KindPolicySet, Name: "readers-access", Op: OpPut,
			Doc: gainSet("readers-access", "{ roles: [readers] }", "    - id: allow-shell\n      tools: [shell.exec]\n      effect: allow\n")}}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := gainWorld()
			v := Check(w, agentDraftOf(w, tc.items...), CheckInput{Proposer: ciBot, Now: checkNow})
			got := findingLines(v.Refused)
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Refused\n got %q\nwant %q", got, tc.want)
			}
			for _, f := range v.Refused {
				if f.Code == codeAgentGuardrail && strings.HasPrefix(f.Sentence, "dev-access") && f.Fix != fix {
					t.Errorf("Fix = %q, want %q", f.Fix, fix)
				}
			}
		})
	}
}

// gateRule is a rule named id of the given mode, for tools, on events.
func gateRule(id, mode, tools, events string) string {
	approve := ""
	if mode == policy.ModeApprove {
		approve = "      approve: { roles: [sec-approvers] }\n"
	}
	return "    - id: " + id + "\n      events: [" + events + "]\n      tools: [" + tools + "]\n      mode: " + mode + "\n      effect: allow\n" + approve
}

// editorGate is the gate the console's role editor writes into a role's
// <role>-access set: a hold on one tool of the role's server.
const editorGate = "    - id: hold-get-me\n      tools: [mcp.call]\n      apps: [github]\n      toolNames: { allow: [get_me] }\n      mode: approve\n" +
	"      approve: { roles: [sec-approvers] }\n"

// TestRoleEditorGateRaisesNothing pins that the role editor's usual gate,
// an approval on one MCP tool in <role>-access, narrows access and raises
// nothing: no risk for a person, and no refusal for an agent whose own set
// gains it, even where the deployment denies local tools by default.
func TestRoleEditorGateRaisesNothing(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	w.LocalToolDefault = policy.EffectDeny
	person := Check(w, stamped(w, Item{Kind: KindPolicySet, Name: "readers-access", Op: OpPut, Doc: gainSet("readers-access", "{ roles: [readers] }", editorGate)}),
		CheckInput{Proposer: Holder{Username: "carol", UserType: "human"}, Now: checkNow})
	if len(person.Refused) > 0 || len(person.Risks) > 0 {
		t.Errorf("a person's gate: refused %q, risks %q", findingLines(person.Refused), riskLines(person.Risks))
	}
	agent := Check(w, agentDraftOf(w, Item{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: devAccess + editorGate}), CheckInput{Proposer: ciBot, Now: checkNow})
	if len(agent.Refused) > 0 || len(agent.Risks) > 0 {
		t.Errorf("an agent's gate on its own set: refused %q, risks %q", findingLines(agent.Refused), riskLines(agent.Risks))
	}
}

// TestAgentGuardrailAddedGates pins how an added gate is judged: a gate
// added for a local tool the deployment denies by default turns a denied
// call into a held or checked one, so it widens, and an agent may not add
// one to a set that binds it. A gate on mcp.call narrows, because the
// gateway enforces MCP calls where the access row grants the tool.
func TestAgentGuardrailAddedGates(t *testing.T) {
	t.Parallel()
	refused := func(clause string) []string {
		return []string{"agent.guardrail PolicySet/dev-access dev-access applies to you, and this draft " + clause + ". An agent's draft may not change what binds the agent."}
	}
	cases := []struct {
		name string
		deny bool
		rule string
		want []string
	}{
		{"the role editor's approval of an MCP tool", true, editorGate, []string{}},
		{"an approval of a local tool the deployment denies", true, gateRule("hold-shell", policy.ModeApprove, "shell.exec", "tool.pre"), refused("adds or widens rule hold-shell")},
		{"a confirmation of a local tool the deployment denies", true, gateRule("ask-shell", policy.ModeConfirm, "shell.exec", "permission.request"), refused("adds or widens rule ask-shell")},
		{"a classifier for a local tool the deployment denies", true, gateRule("scan-write", policy.ModeClassify, "file.write", "tool.pre"), refused("adds or widens rule scan-write")},
		{"a server check for a local tool the deployment allows", false, gateRule("check-shell", policy.ModeServerCheck, "shell.exec", "tool.pre"), []string{}},
		{"an approval after the call, which blocks nothing", true, gateRule("hold-after", policy.ModeApprove, "shell.exec", "tool.post"), []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := gainWorld()
			if tc.deny {
				w.LocalToolDefault = policy.EffectDeny
			}
			v := Check(w, agentDraftOf(w, Item{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: devAccess + tc.rule}), CheckInput{Proposer: ciBot, Now: checkNow})
			got := findingLines(v.Refused)
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Refused\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestAddedGateRisks pins that a gate added for an event or tool the
// deployment denies by default raises policy.guardrail-changed with a tick,
// for a person's draft and for an agent's draft of a set that does not bind
// it, and that policy.local-allow leaves such a gate to it.
func TestAddedGateRisks(t *testing.T) {
	t.Parallel()
	say := func(rule, kinds, once string) string {
		return "Rule " + rule + " of dev-access lets " + kinds + " run on managed machines once " + once + ", where this deployment denies it by default."
	}
	cases := []struct {
		name  string
		deny  bool
		rule  string
		agent bool
		want  string
	}{
		{"an approval of an MCP tool raises nothing", true, gateRule("hold-me", policy.ModeApprove, "mcp.call", "tool.pre"), false, ""},
		{"an approval of a local tool", true, gateRule("hold-shell", policy.ModeApprove, "shell.exec", "tool.pre"), false, say("hold-shell", "shell.exec", "a person approves it")},
		{"a confirmation", true, gateRule("ask-shell", policy.ModeConfirm, "shell.exec, file.write", "tool.pre"), false,
			say("ask-shell", "file.write and shell.exec", "the requester confirms it")},
		{"a classifier", true, gateRule("scan", policy.ModeClassify, "shell.exec", "tool.pre"), false, say("scan", "shell.exec", "a classifier allows it")},
		{"a server check", true, gateRule("check", policy.ModeServerCheck, "net.fetch", "tool.pre"), false, say("check", "net.fetch", "the server allows it")},
		{"a classifier of every tool", true, gateRule("scan-all", policy.ModeClassify, "shell.exec, file.read, file.write, file.edit, net.fetch, task.spawn, other, mcp.call", "tool.pre"), false,
			say("scan-all", "every local tool kind", "a classifier allows it")},
		{"an agent's gate in a set that does not bind it", true, gateRule("hold-shell", policy.ModeApprove, "shell.exec", "tool.pre"), true, say("hold-shell", "shell.exec", "a person approves it")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := gainWorld()
			if tc.deny {
				w.LocalToolDefault = policy.EffectDeny
			}
			set := devAccess + tc.rule
			d := stamped(w, Item{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: set})
			in := CheckInput{Proposer: Holder{Username: "alice", UserType: "human"}, Now: checkNow}
			if tc.agent {
				in.Proposer = Holder{Username: "carol-bot", Agent: true, UserType: "agent", Sponsor: "carol"}
				d = agentDraftOf(w, d.Items...)
				d.Authors[0].Username = "carol-bot"
			}
			v := Check(w, d, in)
			if len(v.Refused) > 0 {
				t.Fatalf("refused: %q", findingLines(v.Refused))
			}
			doc, err := policy.Parse([]byte(set))
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"policy.guardrail-changed PolicySet/dev-access (tick) " + tc.want + " | - | " + ruleRef(doc.Spec.Rules[len(doc.Spec.Rules)-1])}
			if tc.want == "" {
				want = []string{}
			}
			if got := only(riskLines(v.Risks), codeGuardrail); !reflect.DeepEqual(got, want) {
				t.Errorf("Risks\n got %q\nwant %q", got, want)
			}
		})
	}
}

// TestPlainHTTPRisk pins server.plain-http: an address that moves from
// https to http on the same host sends every call, and any credential,
// unencrypted, and republishing cannot undo that, so it is typed.
func TestPlainHTTPRisk(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		live, next string
		credential string
		want       []string
	}{
		{"a call over plain http", "https://api.github.com/mcp", "http://api.github.com/mcp", CredentialNone, []string{
			"server.plain-http App/github (typed api.github.com) github will be reached at api.github.com over plain http, where it used https, so every call travels unencrypted." +
				" | https://api.github.com/mcp | http://api.github.com/mcp"}},
		{"a credential over plain http", "https://api.github.com/mcp", "http://api.github.com/mcp", CredentialOAuth, []string{
			"server.plain-http App/github (typed api.github.com) github will be reached at api.github.com over plain http, where it used https, so every caller's sign-in and every call travel unencrypted." +
				" | https://api.github.com/mcp | http://api.github.com/mcp"}},
		{"a move from http to https", "http://api.github.com/mcp", "https://api.github.com/mcp", CredentialOAuth, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := gainWorld()
			w.Providers = map[string]Provider{"entra": {Name: "entra"}}
			github := w.Apps["github"]
			github.URL = tc.live
			w.Apps["github"] = github
			next := App{Runtime: "remote", URL: tc.next, Credential: tc.credential, Exposure: []string{"*"}}
			if tc.credential == CredentialOAuth {
				next.Provider, next.Agents = "entra", "own"
			}
			v := Check(w, stamped(w, appItem("github")), CheckInput{Apps: map[string]App{"github": next}, Now: checkNow})
			if len(v.Refused) > 0 {
				t.Fatalf("refused: %q", findingLines(v.Refused))
			}
			if got := only(riskLines(v.Risks), "server."); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Risks\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestAgentGuardrailLeavesAnUnparsedSetToItsParseRefusal pins that a set
// text the draft holds and that does not parse reads as neither removed
// nor changed: its parse refusal alone answers it.
func TestAgentGuardrailLeavesAnUnparsedSetToItsParseRefusal(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	v := Check(w, agentDraftOf(w, devAccessWith("toolNames: { deny: [delete_repo] }", "toolNames: { deny: [] }")), CheckInput{Proposer: ciBot, Now: checkNow})
	if got := codesOf(v.Refused); !reflect.DeepEqual(got, []string{codePolicyParse}) {
		t.Errorf("Refused codes = %q, want only %s", got, codePolicyParse)
	}
}

// TestGuardrailRisks pins policy.guardrail-changed for a set the draft
// removes, typed whether or not the set applies to the proposer, and for a
// role taken out of a person's own roles.
func TestGuardrailRisks(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	remove := []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpRemove}}
	gates := refOf(t, devAccess, "hold-push") + "; " + refOf(t, devAccess, "no-delete")
	sentence := "dev-access will stop gating mcp.call as rule hold-push does today, because this draft removes the set. " +
		"dev-access will stop denying mcp.call as rule no-delete does today, because this draft removes the set."
	cases := []struct {
		name     string
		proposer Holder
		items    []Item
		want     []string
	}{
		{"a person's own set removed", Holder{Username: "alice", UserType: "human"}, remove,
			[]string{"policy.guardrail-changed PolicySet/dev-access (typed dev-access) " + sentence + " | " + gates + " | removed"}},
		{"another set removed", Holder{Username: "carol", UserType: "human"}, remove,
			[]string{"policy.guardrail-changed PolicySet/dev-access (typed dev-access) " + sentence + " | " + gates + " | removed"}},
		{"a role taken out of a person's roles", Holder{Username: "bob", UserType: "human"}, []Item{gainRole("engineering", "    kind: business\n")},
			[]string{"policy.guardrail-changed PolicySet/dev-access (typed dev-access) dev-access will stop applying to you, because this draft takes dev out of your roles. " +
				"dev-access will stop gating mcp.call as rule hold-push does today for holders of engineering, because this draft takes dev out of what engineering implies. " +
				"dev-access will stop denying mcp.call as rule no-delete does today for holders of engineering, because this draft takes dev out of what engineering implies. | " +
				gates + " | unbound; unbound for engineering"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Check(w, stamped(w, tc.items...), CheckInput{Proposer: tc.proposer, Now: checkNow})
			got := slices.DeleteFunc(riskLines(v.Risks), func(l string) bool { return !strings.HasPrefix(l, codeGuardrail) })
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Risks\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestAgentOwnReach pins agent.own-reach: an agent's draft may narrow what
// its roles reach, and may not widen it.
func TestAgentOwnReach(t *testing.T) {
	t.Parallel()
	tickets := gainRole("jira-tickets", "    kind: application\n    server: jira\n    bindings:\n        - app: jira\n          tools: [create_issue]\n")
	cases := []struct {
		name  string
		items []Item
		want  []string
	}{
		{"a role the agent holds reaches a new tool", []Item{tickets, gainRole("engineering", "    kind: business\n    implies: [dev, jira-tickets]\n")}, []string{
			"agent.own-reach App/jira This draft would let you run create_issue on jira with no approval, which widens your own reach. An agent's draft may not."}},
		{"a role the agent does not hold may widen", []Item{tickets, gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: ['*']\n")}, []string{}},
		{"a role the agent holds may narrow", []Item{gainRole("dev", "    kind: application\n    bindings:\n        - app: github\n          tools: [get_me]\n")}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, refusalsOnly := range []bool{false, true} {
				w := gainWorld()
				v := Check(w, agentDraftOf(w, tc.items...), CheckInput{Proposer: ciBot, Now: checkNow, RefusalsOnly: refusalsOnly})
				got := findingLines(v.Refused)
				if got == nil {
					got = []string{}
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("RefusalsOnly %v: Refused\n got %q\nwant %q", refusalsOnly, got, tc.want)
				}
			}
		})
	}
}

func TestLooser(t *testing.T) {
	t.Parallel()
	hold := func(change func(*policy.ApproveSpec)) probe {
		a := policy.ApproveSpec{Class: policy.ClassHold, TimeoutSeconds: 90, RetryTTLSeconds: 60, Binding: policy.ApproveBindingCall, Roles: []string{"sec"}}
		change(&a)
		return probe{outcome: OutcomeApproval, approve: &a}
	}
	same := func(*policy.ApproveSpec) {}
	cases := []struct {
		name string
		b, a probe
		want bool
	}{
		{"the same gate", hold(same), hold(same), false},
		{"a longer hold", hold(same), hold(func(a *policy.ApproveSpec) { a.TimeoutSeconds = 600 }), true},
		{"a shorter hold", hold(same), hold(func(a *policy.ApproveSpec) { a.TimeoutSeconds = 30 }), false},
		{"a longer retry window", hold(same), hold(func(a *policy.ApproveSpec) { a.RetryTTLSeconds = 600 }), true},
		{"a ticket where there was a hold", hold(same), hold(func(a *policy.ApproveSpec) { a.Class = policy.ClassTicket }), true},
		{"a hold where there was a ticket", hold(func(a *policy.ApproveSpec) { a.Class = policy.ClassTicket }), hold(same), false},
		{"a longer grant", hold(func(a *policy.ApproveSpec) { a.Class, a.GrantTTLSeconds = policy.ClassTicket, 3600 }),
			hold(func(a *policy.ApproveSpec) { a.Class, a.GrantTTLSeconds = policy.ClassTicket, 7200 }), true},
		{"an approval for any arguments", hold(same), hold(func(a *policy.ApproveSpec) { a.Binding = policy.ApproveBindingTool }), true},
		{"a grant bound by the reserved predicate is not looser", hold(same), hold(func(a *policy.ApproveSpec) { a.Bind = policy.BindPredicate }), false},
		{"a pool that gains a role", hold(same), hold(func(a *policy.ApproveSpec) { a.Roles = []string{"leads", "sec"} }), true},
		{"a pool that loses a role", hold(func(a *policy.ApproveSpec) { a.Roles = []string{"leads", "sec"} }), hold(same), false},
		{"a pool that gains the sponsor", hold(same), hold(func(a *policy.ApproveSpec) { a.Deciders = []string{"sponsor"} }), true},
		{"the requester confirms where a pool decided", hold(same), probe{outcome: OutcomeApproval, approve: hold(same).approve, confirm: true}, true},
		{"the requester's own approval is not looser here", hold(same), hold(func(a *policy.ApproveSpec) { a.SelfApproval = true }), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looser(tc.b, tc.a); got != tc.want {
				t.Errorf("looser = %v, want %v", got, tc.want)
			}
		})
	}
	if !wider(hold(same), hold(func(a *policy.ApproveSpec) { a.SelfApproval = true })) {
		t.Error("an approval its requester may give is not wider for agent.own-reach")
	}
}

func TestFoldRisks(t *testing.T) {
	t.Parallel()
	parts := []Finding{
		risk(codeUngated, "Role/dev", "dev", "a on github will run.", "github/a", "github/a"),
		risk(codeGated, "Role/dev", "", "dev will reach b.", "", "github/b=x"),
		risk(codeUngated, "Role/dev", "dev", "c on jira will run.", "jira/c", "jira/c"),
		risk(codeUngated, "Role/dev", "dev", "a on github will run.", "github/a", "github/a"),
	}
	got := riskLines(foldRisks(parts))
	want := []string{
		"access.ungated Role/dev (typed dev) a on github will run. c on jira will run. | github/a; jira/c | github/a; jira/c",
		"access.gated Role/dev (tick) dev will reach b. | - | github/b=x",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("foldRisks\n got %q\nwant %q", got, want)
	}
}

// TestRiskKeysIgnoreHolders pins that a membership change between review
// and publish changes who gains, and never a risk's key or the digest.
func TestRiskKeysIgnoreHolders(t *testing.T) {
	t.Parallel()
	d := func(w World) Draft {
		return stamped(w, gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: ['*']\n"))
	}
	w1, w2 := gainWorld(), gainWorld()
	w2.Holders["readers"] = append(w2.Holders["readers"], Holder{Username: "erin", UserType: "human"}, Holder{Username: "frank", Agent: true, UserType: "agent"})
	v1, v2 := Check(w1, d(w1), CheckInput{Now: checkNow}), Check(w2, d(w2), CheckInput{Now: checkNow})
	if len(v1.Risks) == 0 || v1.RiskDigest != v2.RiskDigest || !reflect.DeepEqual(v1.Risks, v2.Risks) {
		t.Errorf("risks moved with the holders:\n%q\n%q", riskLines(v1.Risks), riskLines(v2.Risks))
	}
	if reflect.DeepEqual(v1.Gains, v2.Gains) {
		t.Error("the gains did not move with the holders")
	}
}

// TestFindingsSpellWhatPrintsNothing pins that a tool name an upstream
// server offers with an invisible character reads in a finding with that
// character spelled as its code point.
func TestFindingsSpellWhatPrintsNothing(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	github := w.Apps["github"]
	github.Offered = append(github.Offered, "get\u200bme")
	w.Apps["github"] = github
	v := Check(w, widenReaders(w), CheckInput{Now: checkNow})
	i := slices.IndexFunc(v.Risks, func(f Finding) bool { return f.Code == codeUngated })
	if i < 0 || !strings.Contains(v.Risks[i].Sentence, "getU+200Bme") || strings.ContainsRune(v.Risks[i].Sentence+v.Risks[i].Before+v.Risks[i].After, '\u200b') {
		t.Errorf("Risks = %q", riskLines(v.Risks))
	}
}

// TestIdentityScopedSetChangeIsARisk pins policy.identity-scoped-change.
// Who gains reads role lanes only, so every set the draft adds, changes,
// turns off or removes whose match selects by username or identity, before
// or after, raises a tick that says so, and neither its sentence nor its
// words name the users the match lists.
func TestIdentityScopedSetChangeIsARisk(t *testing.T) {
	t.Parallel()
	named := gainSet("named-deny", "{ roles: [dev], users: [alice] }", "    - id: no-push\n      tools: [mcp.call]\n      toolNames: { deny: [push_files] }\n")
	autonomous := gainSet("autonomous-hold", "{ identity: { agencyMode: [autonomous] } }",
		"    - id: hold-all\n      tools: [mcp.call]\n      mode: approve\n      effect: allow\n      approve: { roles: [sec-approvers] }\n")
	w := withSet(withSet(gainWorld(), "named-deny", named), "autonomous-hold", autonomous)
	shell := gainSet("named-shell", "{ users: [alice], identity: { userType: [human], swarmId: [s1] } }", "    - id: allow-shell\n      tools: [shell.exec]\n      effect: allow\n")
	longer := strings.Replace(autonomous, "roles: [sec-approvers]", "roles: [sec-approvers], timeoutSeconds: 600", 1)
	rolesOnly := strings.Replace(named, ", users: [alice]", "", 1)
	sha := func(text string) string { return sha256Hex(text)[:12] }
	line := func(set, kinds, before, after string) []string {
		return []string{codeIdentityScoped + " PolicySet/" + set + " (tick) Straza cannot show who gains through " + set +
			", because its match selects by " + kinds + ", so check its match by hand. | " + cmp.Or(before, "-") + " | " + after}
	}
	for _, tc := range []struct {
		name string
		item Item
		want []string
	}{
		{"added", Item{Kind: KindPolicySet, Name: "named-shell", Op: OpPut, Doc: shell}, line("named-shell", "username, user type and swarm", "", sha(shell))},
		{"changed", Item{Kind: KindPolicySet, Name: "autonomous-hold", Op: OpPut, Doc: longer}, line("autonomous-hold", "agency mode", sha(autonomous), sha(longer))},
		{"turned off", Item{Kind: KindPolicySet, Name: "autonomous-hold", Op: OpOff, Doc: autonomous}, line("autonomous-hold", "agency mode", sha(autonomous), "off")},
		{"removed", Item{Kind: KindPolicySet, Name: "named-deny", Op: OpRemove}, line("named-deny", "username", sha(named), "removed")},
		{"its user selector dropped", Item{Kind: KindPolicySet, Name: "named-deny", Op: OpPut, Doc: rolesOnly}, line("named-deny", "username", sha(named), sha(rolesOnly))},
		{"a set that selects by role only", devAccessWith("timeoutSeconds: 120", "timeoutSeconds: 600"), []string{}},
	} {
		v := Check(w, stamped(w, tc.item), CheckInput{Proposer: Holder{Username: "carol", UserType: "human"}, Now: checkNow})
		if len(v.Refused) > 0 {
			t.Fatalf("%s: refused %q", tc.name, findingLines(v.Refused))
		}
		got := slices.DeleteFunc(riskLines(v.Risks), func(l string) bool { return !strings.HasPrefix(l, codeIdentityScoped+" ") })
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
		if strings.Contains(strings.Join(got, "\n"), "alice") {
			t.Errorf("%s: the risk names a user: %q", tc.name, got)
		}
	}
}

// TestAgentIdentityScopedDraftIsRefused pins that an agent's draft stays
// covered where who gains reads roles only. A set that selects the agent by
// username, user type or agency mode and takes over a stricter gate is
// refused by agent.guardrail, read from the rules, and one that opens the
// built-in app to it by agent.own-reach, read over its whole subject. A set
// that names another user is not refused, and raises
// policy.identity-scoped-change for the person who publishes.
func TestAgentIdentityScopedDraftIsRefused(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	fastPush := "    - id: fast-push\n      tools: [mcp.call]\n      apps: [github]\n      toolNames: { allow: [push_files] }\n" +
		"      mode: approve\n      approve: { deciders: [sponsor], class: ticket }\n"
	ask := "    - id: ask\n      tools: [mcp.call]\n      apps: [straza]\n      toolNames: { allow: [approval_request, approval_await] }\n" +
		"      mode: approve\n      approve: { roles: [sec-approvers] }\n"
	takeover := []string{"agent.guardrail PolicySet/a-fast a-fast applies to you, and this draft adds or widens rule fast-push. An agent's draft may not change what binds the agent."}
	for _, tc := range []struct {
		match, rules string
		want         []string
	}{
		{"{ users: [ci-bot] }", fastPush, takeover},
		{"{ identity: { userType: [agent] } }", fastPush, takeover},
		{"{ identity: { agencyMode: [autonomous] } }", fastPush, takeover},
		{"{ identity: { agencyMode: [autonomous] } }", ask, []string{"agent.own-reach  " + ownReach("reach approval_await and approval_request of the built-in straza MCP server with an approval")}},
		{"{ users: [alice] }", fastPush, nil},
	} {
		set := Item{Kind: KindPolicySet, Name: "a-fast", Op: OpPut, Doc: gainSet("a-fast", tc.match, tc.rules)}
		v := Check(w, agentDraftOf(w, set), CheckInput{Proposer: ciBot, Now: checkNow})
		if got := findingLines(v.Refused); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: refused\n got %q\nwant %q", tc.match, got, tc.want)
		}
		if tc.want == nil && !slices.Contains(codesOf(v.Risks), codeIdentityScoped) {
			t.Errorf("%s: risks %q, want policy.identity-scoped-change for the publisher", tc.match, codesOf(v.Risks))
		}
	}
}
