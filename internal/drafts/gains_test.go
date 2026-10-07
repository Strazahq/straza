package drafts

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// gainSet is a policy set named name matching the roles match, with rules.
func gainSet(name, match, rules string) string {
	return "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata:\n  name: " + name + "\nspec:\n  priority: 10\n  match: " + match + "\n  rules:\n" + rules
}

// devAccess is the live set of gainWorld: push_files is held for
// sec-approvers and delete_repo is denied.
var devAccess = gainSet("dev-access", "{ roles: [dev] }",
	"    - id: hold-push\n      tools: [mcp.call]\n      toolNames: { allow: [push_files] }\n      mode: approve\n      approve: { roles: [sec-approvers], timeoutSeconds: 120 }\n"+
		"    - id: no-delete\n      tools: [mcp.call]\n      toolNames: { deny: [delete_repo] }\n")

// gainWorld is a small tenant: github offers three tools and jira one; dev
// reaches every github tool through dev-access, readers reaches get_me,
// and engineering composes dev. alice holds dev, bob and the agent ci-bot
// hold engineering, and carol holds readers.
func gainWorld() World {
	w := World{
		Apps: map[string]App{
			"github": {ID: "a1", Name: "github", Status: "running", Runtime: "remote", URL: "https://api.github.com/mcp", Credential: CredentialNone,
				Offered: []string{"delete_repo", "get_me", "push_files"}, ReadOnly: []string{"get_me"}, Exposure: []string{"*"}, AdminRole: "mcp-admin-github", RolePrefix: "github-"},
			"jira": {ID: "a2", Name: "jira", Status: "running", Runtime: "remote", URL: "https://jira.example.com/mcp", Credential: CredentialNone,
				Offered: []string{"create_issue"}, Exposure: []string{"*"}, AdminRole: "mcp-admin-jira", RolePrefix: "jira-"},
		},
		Roles: map[string]Role{
			"dev":              {ID: "r1", Name: "dev", Kind: RoleKindApplication, Plane: PlaneAccess},
			"readers":          {ID: "r2", Name: "readers", Kind: RoleKindApplication, Plane: PlaneAccess},
			"engineering":      {ID: "r3", Name: "engineering", Kind: RoleKindBusiness, Plane: PlaneAccess},
			"sec-approvers":    {ID: "r4", Name: "sec-approvers", Kind: RoleKindApprover, Plane: PlaneAccess},
			"mcp-admin-github": {ID: "r5", Name: "mcp-admin-github", Kind: RoleKindBusiness, Plane: PlaneControl},
			"mcp-admin-jira":   {ID: "r6", Name: "mcp-admin-jira", Kind: RoleKindBusiness, Plane: PlaneControl},
			AdminRole:          {ID: "r7", Name: AdminRole, Kind: RoleKindBusiness, Plane: PlaneControl},
			DraftConfigRole:    {ID: "r8", Name: DraftConfigRole, Kind: RoleKindBusiness, Plane: PlaneControl},
		},
		Implies: map[string][]string{"engineering": {"dev"}},
		Access: map[string]Access{
			"dev":     {ID: "b1", Server: "github", Tools: []string{"*"}},
			"readers": {ID: "b2", Server: "github", Tools: []string{"get_me"}},
		},
		Policies: map[string]Policy{"dev-access": {Name: "dev-access", Text: devAccess}},
		Holders: map[string][]Holder{
			"dev":           {{Username: "alice", UserType: "human", AgencyMode: "interactive"}},
			"engineering":   {{Username: "bob", UserType: "human", AgencyMode: "interactive"}, {Username: "ci-bot", Agent: true, UserType: "agent", AgencyMode: "autonomous", Sponsor: "bob"}},
			"readers":       {{Username: "carol", UserType: "human", AgencyMode: "interactive", Devices: 1}},
			"sec-approvers": {{Username: "dana", UserType: "human", Devices: 1}},
		},
		Providers:        map[string]Provider{},
		SnapshotID:       "snap-1",
		LocalToolDefault: policy.EffectAllow,
		Push:             true,
		DockerOnPath:     true,
		Fingerprints:     map[string]Fingerprint{},
	}
	for _, kind := range []Kind{KindApp, KindRole, KindPolicySet} {
		for _, name := range append(append(sortedKeys(w.Apps), sortedKeys(w.Roles)...), sortedKeys(w.Policies)...) {
			w.Fingerprints[string(kind)+"/"+name] = Fingerprint("fp-" + string(kind) + "/" + name)
		}
	}
	return w
}

// gainRole is the Role item of a global role named name whose spec lines
// are spec.
func gainRole(name, spec string) Item {
	return intakeRole(name, spec)
}

// appItem is the App put of the server name, whose one document names it.
// A check reads the server's facts from CheckInput.Apps, never from this
// document.
func appItem(name string) Item {
	return Item{Kind: KindApp, Name: name, Op: OpPut, Doc: "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: " + name + "\n"}
}

// gainLines spells each gain on one line: role, server, tool, the two
// outcomes, the gate words when there are any, and the holders.
func gainLines(gs []Gain) []string {
	out := []string{}
	for _, g := range gs {
		line := fmt.Sprintf("%s %s/%s %s>%s", g.Role, g.Server, g.Tool, g.Before, g.After)
		if g.BeforeWords != "" || g.AfterWords != "" {
			line += " [" + g.BeforeWords + "|" + g.AfterWords + "]"
		}
		if len(g.Holders) != g.HolderCount {
			line += fmt.Sprintf(" count %d", g.HolderCount)
		}
		out = append(out, line+" "+strings.Join(g.Holders, ","))
	}
	return out
}

func TestWhoGains(t *testing.T) {
	t.Parallel()
	hold := "a hold, up to 2 minutes, decided by sec-approvers"
	newsrv := App{Runtime: "remote", URL: "https://mcp.example.net/mcp", Credential: CredentialNone, Exposure: []string{"*"}, AdminRole: "mcp-admin-newsrv", RolePrefix: "newsrv-"}
	cases := []struct {
		name      string
		items     []Item
		apps      map[string]App
		contacted map[string][]string
		want      []string
	}{
		{"a row widened to every tool reaches the rest", []Item{gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: ['*']\n")}, nil, nil,
			[]string{"readers github/delete_repo not-reachable>runs carol", "readers github/push_files not-reachable>runs carol"}},
		{"a row taken away narrows the role", []Item{gainRole("readers", "    kind: application\n")}, nil, nil,
			[]string{"readers github/get_me runs>not-reachable carol"}},
		{"a new role with a row reaches its tools and nobody holds it", []Item{gainRole("github-writers", "    kind: application\n    server: github\n    bindings:\n        - app: github\n          tools: [push_files]\n")}, nil, nil,
			[]string{"github-writers github/push_files not-reachable>runs "}},
		{"a gate changed reaches every holder through the closure", []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpPut,
			Doc: strings.Replace(devAccess, "timeoutSeconds: 120", "class: ticket", 1)}}, nil, nil,
			[]string{"dev github/push_files needs-approval>needs-approval [" + hold + "|a ticket good for 1 day, decided by sec-approvers] alice,bob,ci-bot",
				"engineering github/push_files needs-approval>needs-approval [" + hold + "|a ticket good for 1 day, decided by sec-approvers] bob,ci-bot"}},
		{"a set that selects by identity changes no role's lane", []Item{{Kind: KindPolicySet, Name: "no-agents", Op: OpPut,
			Doc: gainSet("no-agents", "{ identity: { userType: [agent] } }", "    - id: no-push\n      tools: [mcp.call]\n      toolNames: { deny: [push_files] }\n")}}, nil, nil,
			[]string{}},
		{"a new server nobody listed reaches unknown tools", []Item{appItem("newsrv"),
			gainRole("newsrv-callers", "    kind: application\n    server: newsrv\n    bindings:\n        - app: newsrv\n          tools: ['*']\n")},
			map[string]App{"newsrv": newsrv}, nil, []string{"newsrv-callers newsrv/* not-reachable>unknown "}},
		{"a new server a person contacted lists its tools", []Item{appItem("newsrv"),
			gainRole("newsrv-callers", "    kind: application\n    server: newsrv\n    bindings:\n        - app: newsrv\n          tools: ['*']\n")},
			map[string]App{"newsrv": newsrv}, map[string][]string{"App/newsrv": {"list", "read"}},
			[]string{"newsrv-callers newsrv/list not-reachable>runs ", "newsrv-callers newsrv/read not-reachable>runs "}},
		{"a set that allows a built-in tool opens it", []Item{{Kind: KindPolicySet, Name: "readers-access", Op: OpPut,
			Doc: gainSet("readers-access", "{ roles: [readers] }", "    - id: tickets\n      tools: [mcp.call]\n      apps: [straza]\n      toolNames: { allow: [approval_request] }\n")}}, nil, nil,
			[]string{"readers straza/approval_request not-reachable>runs carol"}},
		{"a closure that gains the drafting role reaches the drafting tools", []Item{gainRole("readers", "    kind: application\n    implies: ["+DraftConfigRole+"]\n    bindings:\n        - app: github\n          tools: [get_me]\n")}, nil, nil,
			[]string{"readers straza/draft_status not-reachable>runs carol", "readers straza/draft_submit not-reachable>runs carol"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := gainWorld()
			v := Check(w, stamped(w, tc.items...), CheckInput{Apps: tc.apps, Contacted: tc.contacted, Now: checkNow})
			if len(v.Refused) > 0 {
				t.Fatalf("refused: %q", findingLines(v.Refused))
			}
			if got := gainLines(v.Gains); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Gains\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestWhoGainsDraftingToolsNeedTheRole pins that a closure without
// straza-draft-config reaches no drafting tool, whatever a live set allows
// on the built-in app, because tier one lists the two tools to holders
// only. A draft that gives a role the drafting role then shows the gain.
func TestWhoGainsDraftingToolsNeedTheRole(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	w.Policies["straza-all"] = Policy{Name: "straza-all", Text: gainSet("straza-all", "{ roles: [readers] }",
		"    - id: built-ins\n      tools: [mcp.call]\n      apps: [straza]\n      toolNames: { allow: ['*'] }\n")}
	d := stamped(w, gainRole("readers", "    kind: application\n    implies: ["+DraftConfigRole+"]\n    bindings:\n        - app: github\n          tools: [get_me]\n"))
	v := Check(w, d, CheckInput{Now: checkNow})
	if len(v.Refused) > 0 {
		t.Fatalf("refused: %q", findingLines(v.Refused))
	}
	want := []string{"readers straza/draft_status not-reachable>runs carol", "readers straza/draft_submit not-reachable>runs carol"}
	if got := gainLines(v.Gains); !reflect.DeepEqual(got, want) {
		t.Errorf("Gains\n got %q\nwant %q", got, want)
	}
}

// TestWhoGainsCountsHoldersInTheWorldTheRowDescribes pins that a role's
// row is read on the role's own lane and names every holder of the role,
// counted after the draft through the implications it leaves, each user once.
func TestWhoGainsCountsHoldersInTheWorldTheRowDescribes(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	w.Holders["readers"] = append(w.Holders["readers"], Holder{Username: "alice", UserType: "human", AgencyMode: "interactive"})
	d := stamped(w, gainRole("engineering", "    kind: business\n    implies: [dev, readers]\n"),
		gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: [get_me, push_files]\n"))
	v := Check(w, d, CheckInput{Now: checkNow})
	want := []string{"readers github/push_files not-reachable>runs alice,bob,carol,ci-bot"}
	if got := gainLines(v.Gains); !reflect.DeepEqual(got, want) {
		t.Errorf("Gains\n got %q\nwant %q", got, want)
	}
}

func TestProbeWords(t *testing.T) {
	t.Parallel()
	approve := func(a policy.ApproveSpec) policy.Decision {
		return policy.Decision{Effect: policy.EffectAllow, Approve: &a}
	}
	held := &policy.ApproveSpec{Class: policy.ClassHold, TimeoutSeconds: 90, RetryTTLSeconds: 60, Roles: []string{"sec"}}
	cases := []struct {
		name string
		d    policy.Decision
		want probe
	}{
		{"a default deny reaches nothing", policy.Decision{Effect: policy.EffectDeny, Default: true}, probe{outcome: OutcomeNotReachable}},
		{"a rule's deny", policy.Decision{Effect: policy.EffectDeny, RuleID: "no-delete", SetName: "dev-access"}, probe{outcome: OutcomeDenied, words: "denied by rule no-delete of dev-access"}},
		{"a Rego module's deny", policy.Decision{Effect: policy.EffectDeny, RuleID: "rego", SetName: "guard"}, probe{outcome: OutcomeDenied, words: "denied by rule rego of guard"}},
		{"an allow runs", policy.Decision{Effect: policy.EffectAllow}, probe{outcome: OutcomeRuns}},
		{"a classifier's allow runs after it", policy.Decision{Effect: policy.EffectAllow, Classify: true}, probe{outcome: OutcomeRuns, words: "after a classifier allows it", classify: true}},
		{"a held call the server also checks", policy.Decision{Effect: policy.EffectAllow, ServerCheck: true, Approve: held}, probe{outcome: OutcomeApproval,
			words: "a hold, up to 90 seconds, decided by sec, after the server allows it", approve: held, serverCheck: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := probeOf(tc.d); got != tc.want {
				t.Errorf("probeOf = %+v, want %+v", got, tc.want)
			}
		})
	}
	words := []struct {
		name    string
		d       policy.Decision
		confirm bool
		want    string
	}{
		{"a bare hold goes to the person behind the agent", approve(policy.ApproveSpec{Class: "hold", TimeoutSeconds: 90, RetryTTLSeconds: 60}), false,
			"a hold, up to 90 seconds, decided by the person behind the agent"},
		{"a hold for a pool", approve(policy.ApproveSpec{Class: "hold", TimeoutSeconds: 120, RetryTTLSeconds: 60, Roles: []string{"sec-approvers", "leads"}}), false,
			"a hold, up to 2 minutes, decided by leads or sec-approvers"},
		{"a pool, the person behind the agent and the requester", approve(policy.ApproveSpec{Class: "hold", TimeoutSeconds: 3600, RetryTTLSeconds: 600, Roles: []string{"leads"},
			Deciders: []string{"sponsor"}, SelfApproval: true}), false, "a hold, up to 1 hour, decided by leads, the person behind the agent or the requester, retried within 10 minutes"},
		{"a ticket", approve(policy.ApproveSpec{Class: "ticket", TicketTTLSeconds: 86400, GrantTTLSeconds: 3600, Deciders: []string{"sponsor"}}), false,
			"a ticket good for 1 day, decided by the person behind the agent"},
		{"a ticket with its grant and binding widened, and the reserved bind that widens nothing", approve(policy.ApproveSpec{Class: "ticket", TicketTTLSeconds: 7200, GrantTTLSeconds: 86400,
			Roles: []string{"leads"}, Bind: "predicate", Binding: "tool"}), false,
			"a ticket good for 2 hours, decided by leads, used within 1 day, for any arguments"},
		{"a confirmation", approve(policy.ApproveSpec{Class: "hold", TimeoutSeconds: 90, RetryTTLSeconds: 60}), true, "a hold, up to 90 seconds, confirmed by the requester"},
	}
	for _, tc := range words {
		t.Run(tc.name, func(t *testing.T) {
			tc.d.Confirm = tc.confirm
			if got := probeOf(tc.d); got.outcome != OutcomeApproval || got.words != tc.want {
				t.Errorf("probeOf = %s %q, want %q", got.outcome, got.words, tc.want)
			}
		})
	}
}

func TestDuration(t *testing.T) {
	t.Parallel()
	for seconds, want := range map[int]string{1: "1 second", 90: "90 seconds", 120: "2 minutes", 3600: "1 hour", 5400: "90 minutes", 86400: "1 day", 172800: "2 days"} {
		if got := duration(seconds); got != want {
			t.Errorf("duration(%d) = %q, want %q", seconds, got, want)
		}
	}
}

// TestHolderCountsNeverFollowAUser pins that holders_count cannot tell whom
// a set names: who gains reads role lanes only, so a set that selects by
// username, alone or beside a role, adds no row whose count or holders
// depend on the user it names.
func TestHolderCountsNeverFollowAUser(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	for _, who := range []string{"alice", "carol", "dana", "nobody"} {
		for _, match := range []string{"{ users: [" + who + "] }", "{ roles: [dev], users: [" + who + "] }"} {
			set := gainSet("probe", match, "    - id: x\n      tools: [mcp.call]\n      toolNames: { deny: [get_me] }\n")
			if gains := Check(w, stamped(w, setPut("probe", set)), erin).Gains; len(gains) > 0 {
				t.Errorf("%s: a set that names a user adds rows %q", match, gainLines(gains))
			}
		}
	}
}
