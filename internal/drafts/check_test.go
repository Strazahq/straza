package drafts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// checkSet is a set named name matching match whose one rule holds calls
// for the pool.
func checkSet(name, match, pool string) string {
	return "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata:\n  name: " + name + "\nspec:\n  priority: 10\n  match: { roles: [" + match + "] }\n" +
		"  rules:\n    - id: hold\n      tools: [mcp.call]\n      effect: allow\n      mode: approve\n      approve: { roles: [" + pool + "] }\n"
}

// checkWorld is overlayWorld with sets that compile, an approver role that
// decides one of them, a Straza role, a provider and a fingerprint for
// every live object a test drafts.
func checkWorld() World {
	w := overlayWorld()
	w.Roles["sec-approvers"] = Role{ID: "r6", Name: "sec-approvers", Kind: RoleKindApprover, Plane: PlaneAccess}
	w.Roles["auditors"] = Role{ID: "r7", Name: "auditors", Kind: RoleKindBusiness, Plane: PlaneControl}
	w.Policies = map[string]Policy{"dev-access": {Name: "dev-access", Text: checkSet("dev-access", "dev", "sec-approvers")}}
	w.Providers = map[string]Provider{"entra": {Name: "entra"}, "okta": {Name: "okta", ClientCredentials: true}}
	w.LocalToolDefault = "allow"
	w.Fingerprints = map[string]Fingerprint{}
	for _, object := range []string{"App/github", "App/jira", "Role/dev", "Role/engineering", "Role/github-readers", "Role/mcp-admin-github",
		"Role/ops", "Role/sec-approvers", "Role/auditors", "Role/" + AdminRole, "PolicySet/dev-access"} {
		w.Fingerprints[object] = Fingerprint("fp-" + object)
	}
	return w
}

// stamped is a console draft of items, each stamped with its live
// fingerprint.
func stamped(w World, items ...Item) Draft {
	for i := range items {
		items[i].Base = w.Fingerprints[items[i].Object()]
	}
	return Draft{ID: "41", Revision: 2, Door: DoorConsole, Items: items, Authors: []Principal{person}}
}

// appPut is the put of a remote server named name, one document, whose
// facts a test hands in beside it.
func appPut(name string) Item {
	return Item{Kind: KindApp, Name: name, Op: OpPut, Doc: strings.Replace(intakeServer("    kind: remote\n    remote:\n      url: https://x.example.com\n").Doc,
		"name: github", "name: "+name, 1)}
}

// checkNow is the time every test verdict is stamped with.
var checkNow = time.Date(2026, 9, 24, 10, 14, 3, 0, time.UTC)

func TestCheckStale(t *testing.T) {
	t.Parallel()
	w := checkWorld()
	moved := Item{Kind: KindRole, Name: "dev", Op: OpRemove, Base: "fp-before"}
	// created would meet role.create for its prefix if the check read on.
	created := intakeRole("straza-helpers", "    kind: business\n")
	cases := []struct {
		name    string
		d       Draft
		changed map[string]LastChange
		want    []Finding
	}{
		{"a moved object names who published it", Draft{ID: "41", Items: []Item{moved}},
			map[string]LastChange{"Role/dev": {Draft: 40, Publisher: "bob", At: time.Date(2026, 9, 24, 9, 5, 59, 0, time.FixedZone("CEST", 7200))}},
			[]Finding{{Code: "draft.stale", Class: ClassRefused, Object: "Role/dev",
				Sentence: "Role/dev changed after this draft was checked, when bob published draft 40 at 2026-09-24 07:05 UTC.",
				Fix:      "Check the draft again with Check again on the console or strazactl drafts rebase 41. Straza keeps what this draft changed, takes every other field from live state, and asks you to pick where both changed."}}},
		{"a moved object with no publish on record", Draft{ID: "41", Items: []Item{moved}}, nil,
			[]Finding{{Code: "draft.stale", Class: ClassRefused, Object: "Role/dev", Sentence: "Role/dev changed after this draft was checked.",
				Fix: "Check the draft again with Check again on the console or strazactl drafts rebase 41. Straza keeps what this draft changed, takes every other field from live state, and asks you to pick where both changed."}}},
		{"an object created since the draft named it absent, and the check reads no further", Draft{ID: "41", Items: []Item{
			{Kind: KindRole, Name: "ops", Op: OpRemove}, created}}, nil,
			[]Finding{{Code: "draft.stale", Class: ClassRefused, Object: "Role/ops", Sentence: "Role/ops changed after this draft was checked.",
				Fix: "Check the draft again with Check again on the console or strazactl drafts rebase 41. Straza keeps what this draft changed, takes every other field from live state, and asks you to pick where both changed."}}},
		{"a draft not stored yet names no number", Draft{Items: []Item{moved}}, nil,
			[]Finding{{Code: "draft.stale", Class: ClassRefused, Object: "Role/dev", Sentence: "Role/dev changed after this draft was checked.",
				Fix: "Check the draft again with Check again on the console or strazactl drafts rebase <id>. Straza keeps what this draft changed, takes every other field from live state, and asks you to pick where both changed."}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Check(w, tc.d, CheckInput{Changed: tc.changed, Now: checkNow})
			if !reflect.DeepEqual(v.Refused, tc.want) {
				t.Errorf("Refused\n got %+v\nwant %+v", v.Refused, tc.want)
			}
		})
	}
}

func TestCheckRefusals(t *testing.T) {
	t.Parallel()
	w := checkWorld()
	cycle := func(role, implies string) string {
		return fmt.Sprintf("%s would imply %s, and %s implies %s, so the two would compose each other in a circle. Drop this implication, or the path that leads back from %s to %s", role, implies, implies, role, implies, role)
	}
	remote := func(provider, agents string) App {
		return App{Runtime: "remote", URL: "https://x.example.com", Credential: CredentialOAuth, Provider: provider, Agents: agents}
	}
	allowRego := "  escape:\n    rego: |\n      package straza.ext\n      allow if { true }\n"
	fetchRego := "  escape:\n    rego: |\n      package straza.ext\n      deny contains msg if { msg := http.send({\"method\": \"get\", \"url\": \"http://a/\"}).raw_body }\n"
	// brokenTail is a deny set after the first, cut off so that it never
	// parses, which a first-document parser would never read.
	brokenTail := "---\napiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata:\n  name: p\nspec:\n  priority: 1000\n  match: { roles: [dev] }\n" +
		"  rules:\n    - id: deny-writes\n      tools: [mcp.call]\n      effect: deny\n      reason: writes are denied\n  note: [\n"
	cases := []struct {
		name  string
		items []Item
		apps  map[string]App
		want  []string
	}{
		{"a provider the config does not name", []Item{appPut("github")}, map[string]App{"github": remote("auth0", "own")},
			[]string{`app.provider App/github credential.oauth.provider "auth0" is not configured on this server. Add oauth.providers.auth0 to the strazad config, or pick one of: entra, okta.`}},
		{"client credentials on a provider without them", []Item{appPut("github")}, map[string]App{"github": remote("entra", "client_credentials")},
			[]string{"app.provider App/github server github sets credential.agents to client_credentials, and the provider entra has no clientCredentials settings. " +
				"Add oauth.providers.entra.clientCredentials.assertionAudience to strazad's config, or set credential.agents to own, sponsor or shared."}},
		{"client credentials on a provider with them", []Item{appPut("github")}, map[string]App{"github": remote("okta", "client_credentials")}, nil},
		{"an App whose facts were not read fails closed", []Item{appPut("github")}, nil,
			[]string{"app.parse App/github Straza did not read the manifest of github, so it cannot check the draft."}},
		{"a removal of a server that does not exist", []Item{{Kind: KindApp, Name: "nosuch", Op: OpRemove}}, nil, []string{"app.remove-missing App/nosuch unknown server"}},
		{"a new role in the product's namespace", []Item{intakeRole("straza-ops", "    kind: business\n")}, nil,
			[]string{"role.create Role/straza-ops role names beginning with straza- are reserved for product-defined roles; choose a name without the straza- prefix"}},
		{"a new role owned by a server the same draft adds", []Item{
			appPut("newsrv"), intakeRole("newsrv-readers", "    kind: application\n    server: newsrv\n    bindings:\n        - app: newsrv\n          tools: [read]\n")},
			map[string]App{"newsrv": {Runtime: "remote", URL: "https://n.example.com", RolePrefix: "newsrv-", AdminRole: "mcp-admin-newsrv"}}, nil},
		{"a role's kind moved", []Item{intakeRole("dev", "    kind: business\n")}, nil,
			[]string{"role.kind-change Role/dev dev is an application role, and a role's kind is fixed at create. Create a new role of the kind you need, move its holders there in the identity manager, then delete this one"}},
		{"a global role given an owner", []Item{intakeRole("dev", "    kind: application\n    server: github\n    bindings:\n        - app: github\n          tools: [x]\n")}, nil,
			[]string{"role.owner-change Role/dev dev is a global role, and a role's owner is fixed at create."}},
		{"an owned role made global", []Item{intakeRole("github-readers", "    kind: application\n    bindings:\n        - app: github\n          tools: [x]\n")}, nil,
			[]string{"role.owner-change Role/github-readers github-readers is owned by the server github, and a role's owner is fixed at create."}},
		{"a product role removed", []Item{{Kind: KindRole, Name: AdminRole, Op: OpRemove}}, nil,
			[]string{`role.delete-product Role/straza-admin role "straza-admin" comes with the product and cannot be deleted. To take someone's access away, remove their assignment instead`}},
		{"a decider removed while a live set names it", []Item{{Kind: KindRole, Name: "sec-approvers", Op: OpRemove}}, nil,
			[]string{`role.delete-decider Role/sec-approvers role "sec-approvers" is named among the deciders of a live policy (set "dev-access" rule "hold"): deleting it would leave those approvals to expire unanswered. Remove it from approve.roles and publish the set again, or turn the set off, then delete the role`}},
		{"a decider removed with the set that names it turned off", []Item{
			{Kind: KindRole, Name: "sec-approvers", Op: OpRemove}, {Kind: KindPolicySet, Name: "dev-access", Op: OpOff, Doc: checkSet("dev-access", "dev", "sec-approvers")}}, nil, nil},
		{"a server's admin role removed", []Item{{Kind: KindRole, Name: "mcp-admin-github", Op: OpRemove}}, nil,
			[]string{`role.delete-admin-role Role/mcp-admin-github role "mcp-admin-github" is the admin role of server github and lives as long as the server does. Remove the server instead.`}},
		{"a business role given access", []Item{intakeRole("engineering", "    kind: business\n    bindings:\n        - app: github\n          tools: [x]\n")}, nil,
			[]string{"access.role Role/engineering business role: it composes application roles and reaches tools through them. Give an application role access instead."}},
		{"an owned role reaching another server", []Item{intakeRole("github-readers", "    kind: application\n    server: github\n    bindings:\n        - app: jira\n          tools: [x]\n")}, nil,
			[]string{"access.owned Role/github-readers github-readers belongs to the server github and reaches no other server"}},
		{"a new owned role on every tool, which the check reads as a global admin's", []Item{intakeRole("github-writers", "    kind: application\n    server: github\n    bindings:\n        - app: github\n          tools: ['*']\n")}, nil, nil},
		{"a live owned role widened to every tool", []Item{intakeRole("github-readers", "    kind: application\n    server: github\n    bindings:\n        - app: github\n          tools: ['*']\n")}, nil, nil},
		{"a new global application role with no access", []Item{intakeRole("deploy", "    kind: application\n")}, nil, nil},
		{"a new global application role given access", []Item{intakeRole("deploy", "    kind: application\n    bindings:\n        - app: github\n          tools: [x]\n")}, nil,
			[]string{"access.owned Role/deploy " + unownedRow("deploy", "github")}},
		{"a live global role's tools edited on its server", []Item{intakeRole("dev", "    kind: application\n    bindings:\n        - app: github\n          tools: [get_me]\n")}, nil, nil},
		{"a live global role moved to another server", []Item{intakeRole("dev", "    kind: application\n    bindings:\n        - app: jira\n          tools: [x]\n")}, nil,
			[]string{"access.owned Role/dev " + unownedRow("dev", "jira")}},
		{"access on a server that does not exist", []Item{intakeRole("dev", "    kind: application\n    bindings:\n        - app: nosuch\n          tools: [x]\n")}, nil,
			[]string{"access.server-missing Role/dev dev gives access on nosuch, which is not a registered MCP server and is not added by this draft."}},
		{"access on a server the same draft removes", []Item{{Kind: KindApp, Name: "jira", Op: OpRemove}, intakeRole("dev", "    kind: application\n    bindings:\n        - app: jira\n          tools: [x]\n")}, nil,
			[]string{"access.server-missing Role/dev dev gives access on jira, which this draft removes."}},
		{"an implication of a role that does not exist", []Item{intakeRole("engineering", "    kind: business\n    implies: [nosuch]\n")}, nil,
			[]string{"imply.missing Role/engineering engineering implies nosuch, which is not a role in Straza and is not created by this draft."}},
		{"an implication of a role the same draft removes", []Item{{Kind: KindRole, Name: "ops", Op: OpRemove}, intakeRole("engineering", "    kind: business\n    implies: [ops]\n")}, nil,
			[]string{"imply.missing Role/engineering engineering implies ops, which this draft removes."}},
		{"an implication of a role the draft removes, kept beside a live one", []Item{{Kind: KindRole, Name: "dev", Op: OpRemove},
			intakeRole("engineering", "    kind: business\n    implies: [dev, github-readers]\n")}, nil,
			[]string{"imply.missing Role/engineering engineering implies dev, which this draft removes."}},
		{"a live owned role kept while its server is removed", []Item{{Kind: KindApp, Name: "github", Op: OpRemove},
			intakeRole("github-readers", "    kind: application\n    server: github\n    description: kept\n")}, nil,
			[]string{"role.owner-removed Role/github-readers github-readers belongs to the server github, which this draft removes, so the role goes with it."}},
		{"a new owned role on a server the draft removes", []Item{{Kind: KindApp, Name: "github", Op: OpRemove},
			intakeRole("github-writers", "    kind: application\n    server: github\n    bindings:\n        - app: github\n          tools: [x]\n")}, nil,
			[]string{"role.owner-removed Role/github-writers github-writers belongs to the server github, which this draft removes, so the role goes with it."}},
		{"a set followed by a broken document", []Item{{Kind: KindPolicySet, Name: "p", Op: OpPut, Doc: checkSet("p", "dev", "sec-approvers") + brokenTail}}, nil,
			[]string{"bundle.yaml PolicySet/p Document 1 is not valid YAML: yaml: line 27: did not find expected node content."}},
		{"two edges the draft adds close a cycle", []Item{intakeRole("engineering", "    kind: business\n    implies: [ops]\n"), intakeRole("ops", "    kind: business\n    implies: [engineering]\n")}, nil,
			[]string{"imply.rule Role/engineering " + cycle("engineering", "ops"), "imply.rule Role/ops " + cycle("ops", "engineering")}},
		{"three edges the draft adds close a cycle through a new role", []Item{
			intakeRole("engineering", "    kind: business\n    implies: [dev, github-readers, ops]\n"),
			intakeRole("ops", "    kind: business\n    implies: [mcp-admin-github, x]\n"),
			intakeRole("x", "    kind: business\n    implies: [engineering]\n")}, nil,
			[]string{"imply.rule Role/engineering " + cycle("engineering", "ops"), "imply.rule Role/ops " + cycle("ops", "x"),
				"imply.rule Role/x " + cycle("x", "engineering")}},
		{"a role that implies itself", []Item{intakeRole("ops", "    kind: business\n    implies: [ops]\n")}, nil,
			[]string{"imply.rule Role/ops ops would imply itself, and a role never composes itself. Drop the implication"}},
		{"an approver composed into a role", []Item{intakeRole("ops", "    kind: business\n    implies: [sec-approvers]\n")}, nil,
			[]string{"imply.rule Role/ops sec-approvers is an approver role, which stands alone: no role composes it and it composes no role, because who may decide is its direct member list, certified as it stands. Drop the implication, and assign sec-approvers directly to each person who decides instead"}},
		{"a live edge the draft keeps is not judged again", []Item{intakeRole("ops", "    kind: business\n    implies: [mcp-admin-github]\n")}, nil, nil},
		{"a pool that names an access role", []Item{{Kind: KindPolicySet, Name: "ops-access", Op: OpPut, Doc: checkSet("ops-access", "dev", "dev")}}, nil,
			[]string{`policy.pool PolicySet/ops-access rule "hold": approve.roles names "dev" (application role): only approver roles or straza-admin may decide. Create one (strazactl roles create <name> --kind approver), assign it in your identity manager, then name it here`}},
		{"a pool that names no role", []Item{{Kind: KindPolicySet, Name: "ops-access", Op: OpPut, Doc: checkSet("ops-access", "dev", "release-approvers")}}, nil,
			[]string{`policy.pool PolicySet/ops-access rule "hold": approve.roles names "release-approvers", which is not a role in Straza: ` +
				`create it first (strazactl roles create release-approvers --kind approver), add a Role named release-approvers of kind approver to this draft, or fix the name`}},
		{"a pool that names an approver role the same draft creates", []Item{
			intakeRole("release-approvers", "    kind: approver\n"), {Kind: KindPolicySet, Name: "ops-access", Op: OpPut, Doc: checkSet("ops-access", "dev", "release-approvers")}}, nil, nil},
		{"a set that matches a business role", []Item{{Kind: KindPolicySet, Name: "ops-access", Op: OpPut, Doc: checkSet("ops-access", "engineering", "sec-approvers")}}, nil,
			[]string{`policy.match PolicySet/ops-access match.roles names "engineering" (business role): policies match the application roles that carry the tools (a session holds every role its roles compose, so those cover every holder). Name the application roles it composes instead`}},
		{"a rule with obligations", []Item{{Kind: KindPolicySet, Name: "ops-access", Op: OpPut, Doc: checkSet("ops-access", "dev", "sec-approvers") + "      obligations: [notify]\n"}}, nil,
			[]string{"policy.obligations PolicySet/ops-access rules[0] (hold): obligations are not supported and never ran. Remove the obligations list. To keep recorded content out of the transcript, set capture.mode: redact on the set. To tell people about a call waiting on a hold, set approve.notify on the rule."}},
		{"a set that parses and does not compile", []Item{{Kind: KindPolicySet, Name: "ops-access", Op: OpPut, Doc: checkSet("ops-access", "dev", "sec-approvers") + allowRego}}, nil,
			[]string{`policy.compile  The policy does not compile with this draft: policy: set ops-access: escape modules may only tighten; rule "allow" at escape.rego:2 is not permitted.`}},
		{"a set whose module calls a refused built-in", []Item{{Kind: KindPolicySet, Name: "ops-access", Op: OpPut, Doc: checkSet("ops-access", "dev", "sec-approvers") + fetchRego}}, nil,
			[]string{"policy.compile  The policy does not compile with this draft: policy: set ops-access: its Rego module calls http.send on line 2, " +
				"and Straza refuses that built-in because a policy module must not reach the network, the file system or the process environment. " +
				"Remove the call from the module."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Check(w, stamped(w, tc.items...), CheckInput{Apps: tc.apps, Now: checkNow})
			if got := findingLines(v.Refused); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Refused\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestCheckRefusalFixes pins the fix of each new check refusal.
func TestCheckRefusalFixes(t *testing.T) {
	t.Parallel()
	w := checkWorld()
	cases := []struct {
		item Item
		code string
		fix  string
	}{
		{intakeRole("dev", "    kind: application\n    server: github\n    bindings:\n        - app: github\n          tools: [x]\n"), codeRoleOwnerChange,
			"Create a new role for the server you mean, move its holders, then remove this one."},
		{intakeRole("dev", "    kind: application\n    bindings:\n        - app: nosuch\n          tools: [x]\n"), codeAccessServer, "Check the name with strazactl apps list."},
		{intakeRole("engineering", "    kind: business\n    implies: [nosuch]\n"), codeImplyMissing, "Create it first, or fix the name."},
		{Item{Kind: KindApp, Name: "nosuch", Op: OpRemove}, codeAppRemoveMissing,
			"Nothing is removed. List the servers with strazactl apps list, and leave this removal out of the draft."},
		{Item{Kind: KindPolicySet, Name: "p", Op: OpPut, Doc: checkSet("p", "dev", "sec-approvers") + "  escape:\n    rego: |\n      package straza.ext\n      allow if { true }\n"},
			codePolicyCompile, "Fix the set the error names."},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			v := Check(w, stamped(w, tc.item), CheckInput{Now: checkNow})
			if len(v.Refused) != 1 || v.Refused[0].Code != tc.code || v.Refused[0].Fix != tc.fix {
				t.Errorf("Refused = %+v, want %s with fix %q", v.Refused, tc.code, tc.fix)
			}
		})
	}
}

// TestCheckNamesTheDraftsOwnRemoval pins the fix of each refusal whose
// target the draft itself removes.
func TestCheckNamesTheDraftsOwnRemoval(t *testing.T) {
	t.Parallel()
	w := checkWorld()
	cases := []struct {
		items []Item
		code  string
		fix   string
	}{
		{[]Item{{Kind: KindRole, Name: "ops", Op: OpRemove}, intakeRole("engineering", "    kind: business\n    implies: [ops]\n")},
			codeImplyMissing, "Drop the implication, or keep ops."},
		{[]Item{{Kind: KindApp, Name: "jira", Op: OpRemove}, intakeRole("dev", "    kind: application\n    bindings:\n        - app: jira\n          tools: [x]\n")},
			codeAccessServer, "Drop the binding, or keep jira."},
		{[]Item{{Kind: KindApp, Name: "github", Op: OpRemove}, intakeRole("github-readers", "    kind: application\n    server: github\n")},
			codeRoleOwnerRemoved, "Remove the role's item, or keep the server."},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			v := Check(w, stamped(w, tc.items...), CheckInput{Now: checkNow})
			if len(v.Refused) != 1 || v.Refused[0].Code != tc.code || v.Refused[0].Fix != tc.fix {
				t.Errorf("Refused = %+v, want %s with fix %q", v.Refused, tc.code, tc.fix)
			}
		})
	}
}

// TestCheckReadsEachSetOnceOverManyRemovals pins that a draft of 2,000 role
// removals checked against 101 sets reads each set once, not once for each
// removal. The bound is loose enough for the race detector on a loaded machine.
func TestCheckReadsEachSetOnceOverManyRemovals(t *testing.T) {
	t.Parallel()
	w := checkWorld()
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("set-%03d", i)
		w.Policies[name] = Policy{Name: name, Text: checkSet(name, "dev", "sec-approvers")}
	}
	d := Draft{ID: "41", Door: DoorConsole, Items: manyRemovals(2000), Authors: []Principal{person}}
	start := time.Now()
	v := Check(w, d, CheckInput{Now: checkNow})
	if took := time.Since(start); took > 5*time.Second || len(v.Refused) > 0 {
		t.Errorf("Check of 2,000 removals over 101 sets took %v and refused %v", took, findingLines(v.Refused))
	}
}

// wideGraph is n new business roles, each implying every role after it, as
// the items of one bundle ParseBundle read, or as items the console sends
// in flow style.
func wideGraph(n int, bundle bool) []Item {
	var items []Item
	var texts []string
	for i := 0; i < n; i++ {
		var later []string
		for j := i + 1; j < n; j++ {
			later = append(later, fmt.Sprintf("q%03d", j))
		}
		doc := fmt.Sprintf("apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata: {name: q%03d}\nspec: {kind: business, implies: [%s]}\n", i, strings.Join(later, ","))
		texts = append(texts, doc)
		items = append(items, Item{Kind: KindRole, Name: fmt.Sprintf("q%03d", i), Op: OpPut, Doc: doc})
	}
	if bundle {
		items, _ = ParseBundle([]string{strings.Join(texts, "---\n")})
	}
	return items
}

// TestCheckWalksTheRoleGraphOnce pins that a draft whose new roles each
// imply every later role is checked with one walk of the role graph, not
// one walk for each new edge, which costs minutes at a few hundred roles. An
// agent's draft reads which Straza roles each role reaches in the same one
// walk. The race detector slows one read of these documents from 0.3 seconds
// to 3, so a race build gets a bound that still catches a walk per edge.
func TestCheckWalksTheRoleGraphOnce(t *testing.T) {
	t.Parallel()
	bound := time.Second
	if raceOn {
		bound = 30 * time.Second
	}
	w, bundle := checkWorld(), wideGraph(360, true)
	for _, tc := range []struct {
		name     string
		items    []Item
		door     Door
		proposer Holder
	}{
		{"360 roles in a bundle", bundle, DoorConsole, Holder{Username: "alice"}},
		{"500 roles as items", wideGraph(500, false), DoorConsole, Holder{Username: "alice"}},
		{"360 roles from an agent", bundle, DoorAgent, Holder{Username: "joe-agent", Agent: true, Sponsor: "alice"}},
	} {
		d := Draft{ID: "41", Door: tc.door, Items: tc.items, Authors: []Principal{person}}
		start := time.Now()
		v := Check(w, d, CheckInput{Now: checkNow, Proposer: tc.proposer, RefusalsOnly: true})
		took := time.Since(start)
		t.Logf("%s: Check took %v", tc.name, took)
		if took > bound || len(v.Refused) > 0 || len(tc.items) < 360 {
			t.Errorf("%s: Check took %v over %d items and refused %v", tc.name, took, len(tc.items), findingLines(v.Refused))
		}
	}
}

// TestCheckFailsClosedOnALivePolicyThatDoesNotCompile pins that a World
// whose own policy does not compile, such as one read without its local
// tool default, refuses every draft.
func TestCheckFailsClosedOnALivePolicyThatDoesNotCompile(t *testing.T) {
	t.Parallel()
	w := checkWorld()
	w.LocalToolDefault = ""
	v := Check(w, stamped(w, intakeRole("ops", "    kind: business\n")), CheckInput{Now: checkNow})
	want := []string{`policy.compile  Straza could not compile the live policy to check the draft: policy: localDefault must be allow or deny, got "".`}
	if got := findingLines(v.Refused); !reflect.DeepEqual(got, want) {
		t.Errorf("Refused = %q, want %q", got, want)
	}
}

func TestCheckAgentRefusals(t *testing.T) {
	t.Parallel()
	w := checkWorld()
	sponsored := Holder{Username: "joe-agent", Agent: true, Sponsor: "alice"}
	recording := checkSet("dev-access", "dev", "sec-approvers") + "  capture: { conversations: true }\n"
	wr := checkWorld()
	wr.Policies["dev-access"] = Policy{Name: "dev-access", Text: recording}
	cases := []struct {
		name     string
		w        World
		door     Door
		authors  []Principal
		proposer Holder
		items    []Item
		want     []string
	}{
		{"a role that would reach a Straza role", w, DoorAgent, nil, sponsored, []Item{intakeRole("ops", "    kind: business\n    implies: [auditors]\n")},
			[]string{"agent.straza-reach Role/ops ops would imply auditors, a Straza role, and an agent's draft may not reach one."}},
		{"a role that reaches a Straza role through another role of the draft", w, DoorAgent, nil, sponsored, []Item{
			intakeRole("engineering", "    kind: business\n    implies: [dev, github-readers, auditors]\n"),
			intakeRole("ops", "    kind: business\n    implies: [engineering]\n")},
			[]string{"agent.straza-reach Role/engineering engineering would imply auditors, a Straza role, and an agent's draft may not reach one.",
				"agent.straza-reach Role/ops ops would imply auditors, a Straza role, and an agent's draft may not reach one."}},
		{"a live Straza role removed", w, DoorAgent, nil, sponsored, []Item{{Kind: KindRole, Name: "auditors", Op: OpRemove}},
			[]string{"agent.straza-role Role/auditors auditors is a Straza role, which governs Straza itself, and an agent cannot propose one."}},
		{"recording turned on", w, DoorAgent, nil, sponsored, []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: recording}},
			[]string{"agent.capture PolicySet/dev-access The policy set dev-access changes conversation recording, and an agent cannot propose that, because recording decides what Straza keeps of every session the set matches."}},
		{"a recording set's match narrowed", wr, DoorAgent, nil, sponsored, []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpPut,
			Doc: strings.Replace(recording, "roles: [dev]", "roles: [github-readers]", 1)}},
			[]string{"agent.capture PolicySet/dev-access The policy set dev-access changes conversation recording, and an agent cannot propose that, because recording decides what Straza keeps of every session the set matches."}},
		{"a recording set turned off", wr, DoorAgent, nil, sponsored, []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpOff, Doc: recording}},
			[]string{"agent.capture PolicySet/dev-access The policy set dev-access changes conversation recording, and an agent cannot propose that, because recording decides what Straza keeps of every session the set matches."}},
		{"a recording set's rule changed and its recording kept", wr, DoorAgent, nil, sponsored, []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpPut,
			Doc: strings.Replace(recording, "id: hold", "id: hold-all", 1)}}, nil},
		{"an agent whose sponsor went", w, DoorAgent, nil, Holder{Username: "joe-agent", Agent: true}, []Item{intakeRole("ops", "    kind: business\n")},
			[]string{"agent.sponsor  joe-agent has no active sponsor, and an agent's draft needs a person who answers for it."}},
		{"the pure agent rules hold for an agent's revision on the console", w, DoorConsole, []Principal{person, agent}, Holder{Username: "alice"},
			[]Item{{Kind: KindApp, Name: "github", Op: OpPut, Doc: intakeServer("    kind: command\n    command:\n      exec: npx\n").Doc}},
			[]string{"agent.runtime App/github An agent cannot propose a command or container server, because such a server runs code on the Straza host as Straza's own user, with its data directory in reach."}},
		{"a person's draft meets none of them", w, DoorConsole, []Principal{person}, Holder{Username: "alice"}, []Item{intakeRole("ops", "    kind: business\n    implies: [auditors]\n")}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := stamped(tc.w, tc.items...)
			d.Door, d.Authors = tc.door, tc.authors
			apps := map[string]App{"github": {Runtime: "command", Exec: "npx", Credential: CredentialNone}}
			v := Check(tc.w, d, CheckInput{Apps: apps, Proposer: tc.proposer, Now: checkNow})
			if got := findingLines(v.Refused); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Refused\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestCheckVerdictShape(t *testing.T) {
	t.Parallel()
	w := checkWorld()
	d := stamped(w, intakeRole("ops", "    kind: business\n    description: Operators\n"))
	v := Check(w, d, CheckInput{Now: checkNow.In(time.FixedZone("CEST", 7200))})
	if v.Draft != "41" || v.Revision != 2 || v.Snapshot != w.SnapshotID || v.CheckedAt != "2026-09-24T10:14:03Z" {
		t.Errorf("verdict head = %q %d %q %q", v.Draft, v.Revision, v.Snapshot, v.CheckedAt)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, list := range []string{"refused", "risks", "warnings", "unchecked", "passed", "info", "gains"} {
		if !strings.Contains(string(b), `"`+list+`":[`) {
			t.Errorf("%s is not a list in %s", list, b)
		}
	}
	empty := sha256.Sum256(nil)
	if v.RiskDigest != hex.EncodeToString(empty[:]) {
		t.Errorf("RiskDigest of no risk = %s", v.RiskDigest)
	}
	gain, err := json.Marshal(Gain{Role: "dev", Server: "github", Tool: "get_me", HolderCount: 0, Before: OutcomeNotReachable, After: OutcomeRuns})
	if err != nil || strings.Contains(string(gain), "holders\"") || !strings.Contains(string(gain), `"holders_count":0`) {
		t.Errorf("a gain with its holders cut reads %s", gain)
	}
}

// TestCheckSortsFindings pins that each list sorts by object, then code,
// and keeps the order of findings that share both.
func TestCheckSortsFindings(t *testing.T) {
	t.Parallel()
	w := checkWorld()
	d := stamped(w,
		intakeRole("ops", "    kind: business\n    implies: [nosuch, zzz]\n"),
		Item{Kind: KindApp, Name: "nosuch", Op: OpRemove},
		intakeRole("engineering", "    kind: application\n    implies: [yyy]\n"),
	)
	want := []string{
		"app.remove-missing App/nosuch unknown server",
		"imply.missing Role/engineering engineering implies yyy, which is not a role in Straza and is not created by this draft.",
		"role.kind-change Role/engineering engineering is a business role, and a role's kind is fixed at create. Create a new role of the kind you need, move its holders there in the identity manager, then delete this one",
		"imply.missing Role/ops ops implies nosuch, which is not a role in Straza and is not created by this draft.",
		"imply.missing Role/ops ops implies zzz, which is not a role in Straza and is not created by this draft.",
	}
	if got := findingLines(Check(w, d, CheckInput{Now: checkNow}).Refused); !reflect.DeepEqual(got, want) {
		t.Errorf("Refused\n got %q\nwant %q", got, want)
	}
}

func TestRiskDigestIgnoresOrder(t *testing.T) {
	t.Parallel()
	a := Finding{Code: "access.ungated", Object: "Role/dev", Before: "", After: "github/get_me"}
	b := Finding{Code: "server.removal", Object: "App/jira", Before: "fp", After: "removed"}
	if riskDigest([]Finding{a, b}) != riskDigest([]Finding{b, a}) || riskDigest([]Finding{a}) == riskDigest([]Finding{b}) {
		t.Error("the risk digest depends on the order of the risks, or not on their keys")
	}
}

func TestCheckNeeds(t *testing.T) {
	t.Parallel()
	w := checkWorld()
	liveGithub := `{"straza":{"runtime":{"kind":"remote","remote":{"url":"https://api.github.com/mcp"}}}}`
	w.Apps["github"] = App{ID: "app-gh", Name: "github", AdminRole: "mcp-admin-github", Manifest: liveGithub, Agents: "own", Provider: "okta"}
	owned := intakeRole("github-writers", "    kind: application\n    server: github\n    bindings:\n        - app: github\n          tools: [x]\n")
	cases := []struct {
		name string
		item Item
		next App
		want string
	}{
		{"a new server", Item{Kind: KindApp, Name: "newsrv", Op: OpPut}, App{}, needApps},
		{"a changed address", Item{Kind: KindApp, Name: "github", Op: OpPut},
			App{Manifest: `{"straza":{"runtime":{"kind":"remote","remote":{"url":"https://evil.example.com/mcp"}}}}`}, needApps},
		{"a switch to client credentials", Item{Kind: KindApp, Name: "github", Op: OpPut}, App{Manifest: liveGithub, Agents: "client_credentials", Provider: "okta"}, needApps},
		{"views turned on", Item{Kind: KindApp, Name: "github", Op: OpPut}, App{Manifest: `{"straza":{"runtime":{"kind":"remote","remote":{"url":"https://api.github.com/mcp"}},"exposure":{"tools":["*"],"views":true}}}`,
			Agents: "own", Provider: "okta"}, needApps},
		{"any other server change", Item{Kind: KindApp, Name: "github", Op: OpPut}, App{Manifest: liveGithub, Agents: "own", Provider: "okta"},
			"the scope apps:write, the role straza-global-mcp-admin, or the server's admin role mcp-admin-github"},
		{"a server removal", Item{Kind: KindApp, Name: "github", Op: OpRemove}, App{}, needApps},
		{"a global role", intakeRole("ops", "    kind: business\n"), App{}, needIdentity},
		{"a Straza role", Item{Kind: KindRole, Name: "auditors", Op: OpRemove}, App{}, needIdentity},
		{"a live owned role", Item{Kind: KindRole, Name: "github-readers", Op: OpRemove}, App{},
			"the scope identity:write, or while nobody holds the role the scope apps:write, the role straza-global-mcp-admin or mcp-admin-github"},
		{"a new owned role", owned, App{}, "the scope identity:write or apps:write, the role straza-global-mcp-admin, or mcp-admin-github"},
		{"a set turned off", Item{Kind: KindPolicySet, Name: "dev-access", Op: OpOff}, App{}, needPolicy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apps := map[string]App{}
			if tc.item.Kind == KindApp && tc.item.Op == OpPut {
				apps[tc.item.Name] = tc.next
			}
			got := needs(w, World{}, Draft{Items: []Item{tc.item}}, apps)
			want := []Need{{Object: tc.item.Object(), Standing: tc.want}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("needs = %+v, want %+v", got, want)
			}
		})
	}
}

// TestCheckAnswersNeedsForARefusedDraft pins that a refusal ends the steps
// but the needs are still answered, one per item.
func TestCheckAnswersNeedsForARefusedDraft(t *testing.T) {
	t.Parallel()
	w := checkWorld()
	d := Draft{ID: "41", Items: []Item{{Kind: KindRole, Name: "dev", Op: OpRemove, Base: "moved"}, {Kind: KindPolicySet, Name: "p", Op: OpRemove}}}
	v := Check(w, d, CheckInput{Now: checkNow})
	want := []Need{{Object: "Role/dev", Standing: needIdentity}, {Object: "PolicySet/p", Standing: needPolicy}}
	if len(v.Refused) != 1 || !reflect.DeepEqual(v.Needs, want) {
		t.Errorf("Refused %v, Needs %+v, want one refusal and %+v", findingLines(v.Refused), v.Needs, want)
	}
}

// TestNeedsFollowTheDirectRoutes pins the Needs for roles against the route
// guards: an item needs what the routes that make its change need. A global
// role needs identity:write, a change of its access row alone needs what the
// access row routes need, and both together need both. A role a server owns
// follows its server's routes.
func TestNeedsFollowTheDirectRoutes(t *testing.T) {
	t.Parallel()
	global, owned := gainWorld(), checkWorld()
	owned.Apps["github"] = App{ID: "app-gh", Name: "github", AdminRole: "mcp-admin-github"}
	ownedRole := func(name, spec string) Item {
		return intakeRole(name, "    kind: application\n    server: github\n"+spec+"    bindings:\n        - app: github\n          tools: [get_me]\n")
	}
	row := "the scope identity:write, and the scope apps:write or the role straza-global-mcp-admin for its access row"
	create := "the scope identity:write or apps:write, the role straza-global-mcp-admin, or mcp-admin-github"
	cases := []struct {
		name string
		w    World
		item Item
		want string
	}{
		{"a global row changed and nothing else", global, gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: ['*']\n"), needApps},
		{"a global row and its description", global, gainRole("readers", "    kind: application\n    description: Readers\n    bindings:\n        - app: github\n          tools: ['*']\n"), row},
		{"the same global row", global, gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: [get_me]\n"), needIdentity},
		{"a new global role with a row", global, gainRole("writers", "    kind: application\n    bindings:\n        - app: github\n          tools: [push_files]\n"), row},
		{"a global role removed with its row", global, Item{Kind: KindRole, Name: "readers", Op: OpRemove}, needIdentity},
		{"a global role with no row", global, gainRole("engineering", "    kind: business\n    implies: [dev, readers]\n"), needIdentity},
		{"an owned role described", owned, ownedRole("github-readers", "    description: Readers\n"), create},
		{"an owned role's row changed", owned, intakeRole("github-readers", "    kind: application\n    server: github\n    bindings:\n        - app: github\n          tools: [get_me, delete_repo]\n"),
			"the scope apps:write, the role straza-global-mcp-admin, or mcp-admin-github while nobody holds the role"},
		{"an owned role created", owned, ownedRole("github-writers", ""), create},
		{"an owned role removed", owned, Item{Kind: KindRole, Name: "github-readers", Op: OpRemove},
			"the scope identity:write, or while nobody holds the role the scope apps:write, the role straza-global-mcp-admin or mcp-admin-github"},
	}
	for _, tc := range cases {
		v := Check(tc.w, stamped(tc.w, tc.item), CheckInput{Now: checkNow})
		if want := []Need{{Object: tc.item.Object(), Standing: tc.want}}; !reflect.DeepEqual(v.Needs, want) {
			t.Errorf("%s: needs %+v, want %+v", tc.name, v.Needs, want)
		}
	}
}
