package drafts

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// warningLines spells each warning as its code, object, sentence and fix.
func warningLines(fs []Finding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Code+" "+f.Object+" "+f.Sentence+" | "+f.Fix)
	}
	return out
}

// only keeps the lines that begin with prefix.
func only(lines []string, prefix string) []string {
	return slices.DeleteFunc(lines, func(l string) bool { return !strings.HasPrefix(l, prefix) })
}

func TestSetWarnings(t *testing.T) {
	t.Parallel()
	set := func(match, rules string) []Item {
		return []Item{{Kind: KindPolicySet, Name: "readers-access", Op: OpPut, Doc: gainSet("readers-access", match, rules)}}
	}
	hold := func(approve string) string {
		return "    - id: hold\n      tools: [mcp.call]\n      toolNames: { allow: [get_me] }\n      mode: approve\n      approve: " + approve + "\n"
	}
	cases := []struct {
		name   string
		push   bool
		items  []Item
		prefix string
		want   []string
	}{
		{"a match that names no role", true, set("{ roles: [raeders] }", "    - id: d\n      tools: [shell.exec]\n      effect: deny\n"), "ready.match-unknown", []string{
			"ready.match-unknown PolicySet/readers-access match.roles of readers-access names raeders, which is not a role in Straza, so the set matches nobody. | Fix the name, or create the role in this draft."}},
		{"a rule that names a server that does not exist", true, set("{ roles: [readers] }", "    - id: d\n      tools: [mcp.call]\n      apps: [gitlab]\n      effect: deny\n"),
			"ready.rule-names-nothing", []string{
				"ready.rule-names-nothing PolicySet/readers-access Rule d of readers-access names gitlab, which is not a registered server. | Fix the name."}},
		{"a rule that names a tool the server does not offer", true, set("{ roles: [readers] }", "    - id: d\n      tools: [mcp.call]\n      apps: [github]\n      toolNames: { deny: [get_you, 'get_*'] }\n"),
			"ready.rule-names-nothing", []string{
				"ready.rule-names-nothing PolicySet/readers-access Rule d of readers-access names get_you on github, which the server does not offer. | Fix the name."}},
		{"a pool no person holds", true, []Item{gainRole("bot-approvers", "    kind: approver\n"), set("{ roles: [readers] }", hold("{ roles: [bot-approvers] }"))[0]},
			"ready.pool-empty", []string{
				"ready.pool-empty PolicySet/readers-access No person who could decide rule hold of readers-access holds bot-approvers yet, so every request of that rule waits until it expires. | Assign bot-approvers to a person in your identity manager, or with strazactl assign bot-approvers --user <name>."}},
		{"a pool a person holds", true, set("{ roles: [readers] }", hold("{ roles: [sec-approvers] }")), "ready.pool-empty", []string{}},
		{"people who decide their own requests with no device", true, set("{ roles: [dev] }", hold("{ deciders: [sponsor] }")), "ready.decider-device", []string{
			"ready.decider-device PolicySet/readers-access The requests of rule hold of readers-access go to people who have no enrolled phone or browser, and the console and strazactl cannot decide a person's own request. | Assign them straza-enroll-browser or straza-enroll-mobile, which let them enroll on the self-service page, or run strazactl approvals enroll-token <username> for each of them."}},
		{"people with a device", true, set("{ roles: [readers] }", hold("{ deciders: [sponsor] }")), "ready.decider-device", []string{}},
		{"no push lane", false, set("{ roles: [readers] }", hold("{ roles: [sec-approvers], timeoutSeconds: 600 }")), "ready.", []string{
			"ready.no-push PolicySet/readers-access Rule hold of readers-access holds calls, and neither a push lane nor Slack is configured, so a person learns of a request only on the console. | Configure approval.push or approval.channels.slack, or keep someone watching Approvals."}},
		{"a short hold with no push lane", false, set("{ roles: [readers] }", hold("{ roles: [sec-approvers] }")), "ready.hold-short", []string{
			"ready.hold-short PolicySet/readers-access Rule hold of readers-access holds a call for 90 seconds, which is short for a person who is not watching the console. | Lengthen approve.timeoutSeconds, or configure a push lane or Slack."}},
		{"an approval that denies", true, set("{ roles: [readers] }", "    - id: hold\n      tools: [mcp.call]\n      effect: deny\n      mode: approve\n      approve: { roles: [sec-approvers] }\n"),
			"ready.approve-deny", []string{
				"ready.approve-deny PolicySet/readers-access Rule hold of readers-access sets mode approve with effect deny, so its calls are denied and never held. | Drop effect: deny, or drop the approve mode."}},
		{"a ticket bound to the arguments of a tool that posts", true, set("{ roles: [readers] }",
			"    - id: hold\n      tools: [mcp.call]\n      toolNames: { allow: [get_me, postMessage] }\n      mode: approve\n      approve: { roles: [sec-approvers], class: ticket }\n"),
			"ready.ticket-args", []string{
				"ready.ticket-args PolicySet/readers-access Rule hold of readers-access binds a ticket to the exact arguments of postMessage, so an approval is used only by a later call with the same arguments." +
					" | If the arguments of postMessage change on every call, set approve.binding: tool, which lets one approval cover any arguments of postMessage."}},
		{"a ticket bound by the reserved predicate still binds the arguments", true, set("{ roles: [readers] }",
			"    - id: hold\n      tools: [mcp.call]\n      toolNames: { allow: [postMessage] }\n      mode: approve\n      approve: { roles: [sec-approvers], class: ticket, bind: predicate }\n"),
			"ready.ticket-args", []string{
				"ready.ticket-args PolicySet/readers-access Rule hold of readers-access binds a ticket to the exact arguments of postMessage, so an approval is used only by a later call with the same arguments." +
					" | If the arguments of postMessage change on every call, set approve.binding: tool, which lets one approval cover any arguments of postMessage."}},
		{"a ticket bound by the reserved predicate hears the advisory", true, set("{ roles: [readers] }",
			"    - id: hold\n      tools: [mcp.call]\n      toolNames: { allow: [postMessage] }\n      mode: approve\n      approve: { roles: [sec-approvers], class: ticket, bind: predicate }\n"),
			"advisory.", []string{
				`advisory.bind-reserved PolicySet/readers-access rule "hold": approve.bind: predicate is reserved and works exactly like bind: fingerprint, so it does not widen which later call may use an approval. ` +
					`Remove approve.bind so the rule says what it does. For an MCP tool whose arguments change on every call, approve.binding: tool lets one approval cover any arguments | `}},
		{"windows at the low end of their range", true, set("{ roles: [readers] }", hold("{ roles: [sec-approvers], timeoutSeconds: 20, retryTTLSeconds: 5 }")), "ready.window-low", []string{
			"ready.window-low PolicySet/readers-access Rule hold of readers-access sets approve.timeoutSeconds to 20, the low end of its range, which a person rarely meets. | Raise it.",
			"ready.window-low PolicySet/readers-access Rule hold of readers-access sets approve.retryTTLSeconds to 5, the low end of its range, which a person rarely meets. | Raise it."}},
		{"a set of one role under another name", true, []Item{{Kind: KindPolicySet, Name: "gate-readers", Op: OpPut,
			Doc: gainSet("gate-readers", "{ roles: [readers] }", "    - id: d\n      tools: [shell.exec]\n      effect: deny\n")}}, "ready.set-name", []string{
			"ready.set-name PolicySet/gate-readers gate-readers gates readers and is not named readers-access, so the console's role editor will not read it back. | Name it readers-access."}},
		{"the server's advisories", true, set("{ roles: [readers] }", "    - id: hold\n      tools: [mcp.call]\n      mode: approve\n      effect: allow\n"), "advisory.", []string{
			`advisory.approve-unrouted PolicySet/readers-access rule "hold": no approver named, so each request routes to the person behind the agent: an agent's sponsor, or the person themself when they run their own agent. An agent with no usable sponsor is denied immediately. Give every agent this rule can match a sponsor, or name approve.roles / approve.deciders | `}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := gainWorld()
			w.Push = tc.push
			v := Check(w, stamped(w, tc.items...), CheckInput{Now: checkNow})
			if len(v.Refused) > 0 {
				t.Fatalf("refused: %q", findingLines(v.Refused))
			}
			if got := only(warningLines(v.Warnings), tc.prefix); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Warnings\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestItemWarnings(t *testing.T) {
	t.Parallel()
	remote := App{Runtime: "remote", URL: "https://mcp.example.net/mcp", Credential: CredentialNone, Exposure: []string{"*"}, AdminRole: "mcp-admin-x", RolePrefix: "x-"}
	with := func(change func(*App)) App {
		a := remote
		change(&a)
		return a
	}
	cases := []struct {
		name   string
		items  []Item
		app    App
		prefix string
		want   []string
	}{
		{"a static secret not stored", []Item{appItem("x")}, with(func(a *App) { a.Credential = credentialStatic }), "ready.secret-missing", []string{
			"ready.secret-missing App/x x needs a static secret and none is stored yet, so every call fails until one is. | After publishing, run strazactl apps secret set x."}},
		{"a caller's own sign-in nobody connected", []Item{appItem("x")}, with(func(a *App) { a.Credential, a.Provider, a.Agents = CredentialOAuth, "entra", "own" }),
			"ready.nobody-connected", []string{
				"ready.nobody-connected App/x x runs on each caller's own sign-in, and nobody who holds its roles has connected yet. | Each person runs straza connect x after publishing."}},
		{"a container with no docker", []Item{appItem("x")}, App{Runtime: "oci", Image: "ghcr.io/acme/x:1", Credential: CredentialNone}, "ready.no-docker", []string{
			"ready.no-docker App/x x runs as a container and this host has no docker, so it will not start. | Install docker on the Straza host, or run the server remotely."}},
		{"an address the registry record does not give", []Item{appItem("x")}, with(func(a *App) { a.RegistryURL = "https://mcp.example.org/mcp" }),
			"heuristic.registry-address", []string{
				"heuristic.registry-address App/x The address of x differs from the one in the registry record its server block copies. | Check which address the server's publisher gives."}},
		{"an exposure that names a tool the server does not offer", []Item{appItem("github")},
			App{Runtime: "remote", URL: "https://api.github.com/mcp", Credential: CredentialNone, Exposure: []string{"get_me", "get_you", "push_*"}}, "ready.tool-unknown", []string{
				"ready.tool-unknown App/github The exposure of github names get_you, which github does not offer. | Fix the name, or check the list with strazactl apps tools github."}},
		{"a row that names a tool the server does not offer", []Item{gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: [get_me, get_you]\n")}, App{},
			"ready.tool-unknown", []string{
				"ready.tool-unknown Role/readers readers names get_you, which github does not offer. | Fix the name, or check the list with strazactl apps tools github."}},
		{"a server that came from a file", []Item{{Kind: KindApp, Name: "github", Op: OpRemove}}, App{}, "server.file-linked", []string{
			"server.file-linked App/github github came from the file /apps/github.yaml. Removing it here leaves the file in place, and Straza proposes the server again only when the file changes. | "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := gainWorld()
			w.DockerOnPath = false
			w.Providers = map[string]Provider{"entra": {Name: "entra"}}
			github := w.Apps["github"]
			github.File, github.SharedSecret, github.UserCredentials, github.Paused = "/apps/github.yaml", true, 2, true
			w.Apps["github"] = github
			w.Holders["mcp-admin-github"] = []Holder{{Username: "gina", UserType: "human"}}
			var apps map[string]App
			if it := tc.items[0]; it.Kind == KindApp && it.Op == OpPut {
				apps = map[string]App{it.Name: tc.app}
			}
			v := Check(w, stamped(w, tc.items...), CheckInput{Apps: apps, Now: checkNow})
			if len(v.Refused) > 0 {
				t.Fatalf("refused: %q", findingLines(v.Refused))
			}
			if got := only(warningLines(v.Warnings), tc.prefix); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Warnings\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestNarrowingWarnings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		items  []Item
		prefix string
		want   []string
	}{
		{"holders lose tools", []Item{gainRole("dev", "    kind: application\n    bindings:\n        - app: github\n          tools: [get_me]\n")}, "narrow.", []string{
			"narrow.role-loses Role/dev Holders of dev lose push_files on github. | ",
			"narrow.role-loses Role/engineering Holders of engineering lose push_files on github. | "}},
		{"a removed role leaves its set matching nothing", []Item{{Kind: KindRole, Name: "dev", Op: OpRemove}}, "narrow.set-orphaned", []string{
			"narrow.set-orphaned Role/dev Removing dev leaves dev-access matching nothing. | Add a Removal of dev-access to this draft, or turn it off with strazactl policy deactivate dev-access after publishing."}},
		{"a tool newly reached that says it deletes", []Item{gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: [get_me, delete_repo]\n")},
			"heuristic.destructive", []string{
				"heuristic.destructive App/github delete_repo on github does not say it is read-only, and its name says it deletes. | Gate it with an approval rule, or leave it out of the role."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := gainWorld()
			v := Check(w, stamped(w, tc.items...), CheckInput{Now: checkNow})
			if len(v.Refused) > 0 {
				t.Fatalf("refused: %q", findingLines(v.Refused))
			}
			if got := only(warningLines(v.Warnings), tc.prefix); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Warnings\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestSecretWarningsReachTheVerdict pins that the secret scan's warning, a
// long random-looking value, is a warning of the verdict, and that such a
// draft does not pass the secret check.
func TestSecretWarningsReachTheVerdict(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	item := Item{Kind: KindApp, Name: "local", Op: OpPut, Doc: "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: local\nstraza:\n  runtime:\n    kind: command\n" +
		"    command:\n      exec: mcp\n      args: [--build, Zq8Kf3Lm9Xw2Rt7Yp4Nv6Bc1Hd5Gj0Sa]\n"}
	v := Check(w, stamped(w, item), CheckInput{Apps: map[string]App{"local": {Runtime: "command", Exec: "mcp", Credential: CredentialNone}}, Now: checkNow})
	if got := only(warningLines(v.Warnings), "secret."); len(got) != 1 || !strings.HasPrefix(got[0], "secret.entropy App/local ") {
		t.Errorf("secret warnings = %q", got)
	}
	if slices.ContainsFunc(v.Passed, func(f Finding) bool { return f.Code == codePassedSecrets }) {
		t.Error("passed.secrets holds beside a secret warning")
	}
}

// TestDestructiveVerb pins the words heuristic.destructive reads in a tool
// name.
func TestDestructiveVerb(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{"delete_repo": "deletes", "repoDelete": "deletes", "Drop-Table": "drops", "revoke": "revokes",
		"get_me": "", "deleted_items_list": "", "wipeout": ""} {
		if got := destructiveVerb(name); got != want {
			t.Errorf("destructiveVerb(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestSlackReachesApprovers pins that approval requests posted to Slack
// reach a person outside the console, so a hold raises neither
// ready.no-push nor ready.hold-short while Slack is on.
func TestSlackReachesApprovers(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	w.Push, w.Slack = false, true
	v := Check(w, stamped(w, Item{Kind: KindPolicySet, Name: "readers-access", Op: OpPut, Doc: gainSet("readers-access", "{ roles: [readers] }",
		"    - id: hold\n      tools: [mcp.call]\n      toolNames: { allow: [get_me] }\n      mode: approve\n      approve: { roles: [sec-approvers] }\n")}), CheckInput{Now: checkNow})
	if got := append(only(warningLines(v.Warnings), codeNoPush), only(warningLines(v.Warnings), codeHoldShort)...); len(got) > 0 {
		t.Errorf("Slack is on, and the check warns %q", got)
	}
}
