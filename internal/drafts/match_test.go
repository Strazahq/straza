package drafts

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
)

// TestAppliesToAgreesWithTheEngine pins appliesTo against the policy
// engine over the same cases: a set that records conversations applies to
// a subject exactly when the engine turns recording on for it.
func TestAppliesToAgreesWithTheEngine(t *testing.T) {
	t.Parallel()
	dev := policy.Subject{User: "alice", Roles: []string{"dev", "engineering"}, UserType: "human", AgencyMode: "interactive", SwarmID: "blue"}
	bot := policy.Subject{User: "ci-bot", Roles: []string{"dev"}, UserType: "agent", AgencyMode: "autonomous"}
	cases := []struct {
		name  string
		match policy.Match
		sub   policy.Subject
		want  bool
	}{
		{"no selector matches everyone", policy.Match{}, bot, true},
		{"a role the subject holds", policy.Match{Roles: []string{"ops", "dev"}}, dev, true},
		{"no role the subject holds", policy.Match{Roles: []string{"ops"}}, dev, false},
		{"a subject with no roles", policy.Match{Roles: []string{"ops"}}, policy.Subject{User: "x"}, false},
		{"a username listed", policy.Match{Users: []string{"alice"}}, dev, true},
		{"a username not listed", policy.Match{Users: []string{"bob"}}, dev, false},
		{"roles and users both hold", policy.Match{Roles: []string{"dev"}, Users: []string{"alice"}}, dev, true},
		{"roles hold and users do not", policy.Match{Roles: []string{"dev"}, Users: []string{"bob"}}, dev, false},
		{"a user type listed", policy.Match{Identity: &policy.IdentityMatch{UserType: []string{"agent"}}}, bot, true},
		{"a user type not listed", policy.Match{Identity: &policy.IdentityMatch{UserType: []string{"agent"}}}, dev, false},
		{"an unset user type fails a listed one", policy.Match{Identity: &policy.IdentityMatch{UserType: []string{"human"}}}, policy.Subject{Roles: []string{"dev"}}, false},
		{"an agency mode listed", policy.Match{Identity: &policy.IdentityMatch{AgencyMode: []string{"autonomous"}}}, bot, true},
		{"a swarm listed", policy.Match{Identity: &policy.IdentityMatch{SwarmID: []string{"blue"}}}, dev, true},
		{"an unset swarm fails a listed one", policy.Match{Identity: &policy.IdentityMatch{SwarmID: []string{"blue"}}}, bot, false},
		{"roles and identity both hold", policy.Match{Roles: []string{"dev"}, Identity: &policy.IdentityMatch{UserType: []string{"agent"}}}, bot, true},
		{"roles hold and identity does not", policy.Match{Roles: []string{"dev"}, Identity: &policy.IdentityMatch{UserType: []string{"human"}}}, bot, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := policy.Document{APIVersion: policy.APIVersion, Kind: "PolicySet", Metadata: policy.Metadata{Name: "s"},
				Spec: policy.Spec{Match: tc.match, Capture: &policy.Capture{Conversations: true},
					Rules: []policy.Rule{{ID: "r", Events: []string{policy.EventToolPre}, Effect: policy.EffectDeny}}}}
			eng, err := policy.NewEngine([]policy.Document{doc}, policy.EffectAllow)
			if err != nil {
				t.Fatal(err)
			}
			engine := eng.Capture(tc.sub).Conversations
			if got := appliesTo(tc.match, tc.sub); got != tc.want || engine != tc.want {
				t.Errorf("appliesTo = %v and the engine = %v, want %v", got, engine, tc.want)
			}
		})
	}
}

// TestMatchGlobAgreesWithTheManager pins the ported tool glob against the
// manager's, which cuts a server's tools and reads an access row.
func TestMatchGlobAgreesWithTheManager(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"*", "anything", true},
		{"*", "", true},
		{"get_me", "get_me", true},
		{"get_me", "get_me2", false},
		{"get_*", "get_repo", true},
		{"get_*", "list_repo", false},
		{"*_repo", "delete_repo", true},
		{"a*b*c", "a1b2c", true},
		{"a*b*c", "a1c2b", false},
		{"a.b", "axb", false},
		{"a.b*", "a.bc", true},
		{"(x)*", "(x)y", true},
		{"re:.*", "re:.*", true},
	}
	for _, tc := range cases {
		t.Run(tc.pattern+" "+tc.name, func(t *testing.T) {
			got, manager := matchAnyGlob([]string{tc.pattern}, tc.name), manager.MatchAnyGlob([]string{tc.pattern}, tc.name)
			if got != tc.want || manager != tc.want {
				t.Errorf("matchAnyGlob = %v and the manager's = %v, want %v", got, manager, tc.want)
			}
		})
	}
}

// TestRulePredicates pins which rules are gates, which can allow,
// and which only tighten a set an agent adds them to.
func TestRulePredicates(t *testing.T) {
	t.Parallel()
	pre, shell := []string{policy.EventToolPre}, []string{policy.ToolShellExec}
	cases := []struct {
		name                   string
		rule                   policy.Rule
		gate, allows, tightens bool
	}{
		{"a plain allow", policy.Rule{Events: pre, Effect: policy.EffectAllow}, false, true, false},
		{"a plain deny", policy.Rule{Events: pre, Effect: policy.EffectDeny}, true, false, true},
		{"deny-side patterns only", policy.Rule{Events: pre, ToolNames: &policy.AllowDeny{Deny: []string{"x"}}}, true, false, true},
		{"patterns on both sides", policy.Rule{Events: pre, ToolNames: &policy.AllowDeny{Allow: []string{"x"}, Deny: []string{"y"}}}, true, true, false},
		{"effect deny with an allow side still allows", policy.Rule{Events: pre, Effect: policy.EffectDeny,
			ToolNames: &policy.AllowDeny{Allow: []string{"x"}, Deny: []string{"y"}}}, true, true, false},
		{"an approval", policy.Rule{Events: pre, Tools: shell, Effect: policy.EffectAllow, Mode: policy.ModeApprove}, true, true, true},
		{"a confirmation", policy.Rule{Events: pre, Tools: shell, Effect: policy.EffectAllow, Mode: policy.ModeConfirm}, true, true, true},
		{"a classifier", policy.Rule{Events: pre, Tools: shell, Effect: policy.EffectAllow, Mode: policy.ModeClassify}, true, true, true},
		{"an approval of every tool where local tools are allowed", policy.Rule{Events: pre, Effect: policy.EffectAllow, Mode: policy.ModeApprove}, true, true, true},
		{"a require alone", policy.Rule{Events: pre, Require: &policy.Require{Attestation: "managed"}}, true, false, true},
		{"a require with an allow", policy.Rule{Events: pre, Effect: policy.EffectAllow, Require: &policy.Require{DeviceCert: true}}, true, true, false},
		{"a rule for the permission prompt only", policy.Rule{Events: []string{policy.EventPermissionRequest}, Effect: policy.EffectDeny}, true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := [3]bool{isGate(tc.rule), canAllow(tc.rule), tightens(tc.rule, policy.EffectAllow)}; got != [3]bool{tc.gate, tc.allows, tc.tightens} {
				t.Errorf("gate, allows, tightens = %v, want %v", got, [3]bool{tc.gate, tc.allows, tc.tightens})
			}
		})
	}
}

// TestOpensDefault pins which gates turn a call the deployment denies by
// default into a held or checked one: a local tool while localToolDefault
// is deny, on an event that waits for the decision. A gate on mcp.call
// never does, because the gateway enforces MCP calls where the access row
// grants the tool.
func TestOpensDefault(t *testing.T) {
	t.Parallel()
	rule := func(mode, effect string, events, tools []string) policy.Rule {
		return policy.Rule{ID: "r", Events: events, Tools: tools, Mode: mode, Effect: effect}
	}
	pre, post, ask := []string{policy.EventToolPre}, []string{policy.EventToolPost}, []string{policy.EventPermissionRequest}
	mcp, shell := []string{policy.ToolMCPCall}, []string{policy.ToolShellExec}
	cases := []struct {
		name  string
		rule  policy.Rule
		local string
		want  bool
		kinds []string
	}{
		{"an approval of an MCP tool", rule(policy.ModeApprove, policy.EffectAllow, pre, mcp), policy.EffectAllow, false, nil},
		{"an approval of an MCP tool where local tools are denied", rule(policy.ModeApprove, policy.EffectAllow, pre, mcp), policy.EffectDeny, false, nil},
		{"an approval of a local tool the deployment allows", rule(policy.ModeApprove, policy.EffectAllow, pre, shell), policy.EffectAllow, false, nil},
		{"an approval of a local tool the deployment denies", rule(policy.ModeApprove, policy.EffectAllow, pre, shell), policy.EffectDeny, true, shell},
		{"a permission prompt of a local tool the deployment denies", rule(policy.ModeConfirm, policy.EffectAllow, ask, shell), policy.EffectDeny, true, shell},
		{"an approval after the call", rule(policy.ModeApprove, policy.EffectAllow, post, shell), policy.EffectDeny, false, shell},
		{"a server check of every tool where local tools are allowed", rule(policy.ModeServerCheck, policy.EffectAllow, pre, nil), policy.EffectAllow, false, nil},
		{"a classifier of every tool where local tools are denied", rule(policy.ModeClassify, policy.EffectAllow, pre, nil), policy.EffectDeny, true, localTools},
		{"a deny of an MCP tool", rule("", policy.EffectDeny, pre, mcp), policy.EffectAllow, false, nil},
		{"an approval that denies", rule(policy.ModeApprove, policy.EffectDeny, pre, shell), policy.EffectDeny, false, shell},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := opensDefault(tc.rule, tc.local); got != tc.want {
				t.Errorf("opensDefault = %v, want %v", got, tc.want)
			}
			if got := deniedKinds(tc.rule, tc.local); !reflect.DeepEqual(got, tc.kinds) && len(got)+len(tc.kinds) > 0 {
				t.Errorf("deniedKinds = %v, want %v", got, tc.kinds)
			}
		})
	}
}

// TestRuleRefIgnoresReasonText pins that a rule's reference covers every
// field and sameRule ignores only the reason.
func TestRuleRefIgnoresReasonText(t *testing.T) {
	t.Parallel()
	a := policy.Rule{ID: "hold", Events: []string{policy.EventToolPre}, Effect: policy.EffectAllow, Mode: policy.ModeApprove, Reason: "old"}
	b, c := a, a
	b.Reason, c.Mode = "new", policy.ModeConfirm
	if !sameRule(a, b) || sameRule(a, c) {
		t.Errorf("sameRule(reason changed) = %v, sameRule(mode changed) = %v", sameRule(a, b), sameRule(a, c))
	}
	if ref := ruleRef(a); len(ref) != len("hold@")+12 || ref == ruleRef(c) || ref == ruleRef(b) {
		t.Errorf("ruleRef = %s, %s, %s", ref, ruleRef(b), ruleRef(c))
	}
}

// TestHoldingFoldsHoldersThroughClosures pins who holds what: each user's
// roles through any path, and each role's users, each once and sorted.
func TestHoldingFoldsHoldersThroughClosures(t *testing.T) {
	t.Parallel()
	w := World{
		Implies: map[string][]string{"engineering": {"dev"}, "dev": {"readers"}},
		Holders: map[string][]Holder{
			"engineering": {{Username: "bob"}, {Username: "alice"}},
			"dev":         {{Username: "alice"}},
			"readers":     {{Username: "carol", Agent: true}},
		},
	}
	h := holdingOf(w, newClosures(w.Implies))
	wantRoles := map[string][]string{"alice": {"dev", "engineering", "readers"}, "bob": {"dev", "engineering", "readers"}, "carol": {"readers"}}
	wantByRole := map[string][]string{"engineering": {"alice", "bob"}, "dev": {"alice", "bob"}, "readers": {"alice", "bob", "carol"}}
	if !reflect.DeepEqual(h.roles, wantRoles) || !reflect.DeepEqual(h.byRole, wantByRole) || !h.users["carol"].Agent {
		t.Errorf("roles %v\nbyRole %v\nusers %v", h.roles, h.byRole, h.users)
	}
}

func TestListWords(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		words []string
		join  string
		want  string
	}{
		{nil, "and", ""},
		{[]string{"a"}, "and", "a"},
		{[]string{"a", "b"}, "or", "a or b"},
		{[]string{"a", "b", "c"}, "and", "a, b and c"},
	} {
		if got := listWords(tc.words, tc.join); got != tc.want {
			t.Errorf("listWords(%v, %s) = %q, want %q", tc.words, tc.join, got, tc.want)
		}
	}
}

// TestClosuresAgreeWithTheWalk pins the one-walk closures against a plain
// walk from each role: over a chain, a diamond, a cycle, a role on no edge,
// and a graph wider than one bitset word where every role implies every
// later one.
func TestClosuresAgreeWithTheWalk(t *testing.T) {
	t.Parallel()
	wide := map[string][]string{}
	for i := range 70 {
		for j := i + 1; j < 70; j++ {
			wide[fmt.Sprintf("r%02d", i)] = append(wide[fmt.Sprintf("r%02d", i)], fmt.Sprintf("r%02d", j))
		}
	}
	graphs := map[string]map[string][]string{
		"a chain":       {"a": {"b"}, "b": {"c"}},
		"a diamond":     {"top": {"left", "right"}, "left": {"bottom"}, "right": {"bottom"}},
		"a cycle":       {"a": {"b"}, "b": {"c"}, "c": {"a", "d"}},
		"no edge":       {},
		"a wide graph":  wide,
		"a self-loop":   {"a": {"a", "b"}},
		"two in a ring": {"x": {"y"}, "y": {"x"}, "z": {"x"}},
	}
	for name, implies := range graphs {
		t.Run(name, func(t *testing.T) {
			cl := newClosures(implies)
			for _, role := range append(sortedKeys(implies), "lonely", "d", "bottom", "r69") {
				if got, want := cl.of(role), closure(implies, role); !reflect.DeepEqual(got, want) {
					t.Errorf("of(%s) = %v, want %v", role, got, want)
				}
			}
			if got, want := cl.union([]string{"a", "lonely"}), sortedCopy(append(closure(implies, "a"), "lonely")); !reflect.DeepEqual(got, slices.Compact(want)) {
				t.Errorf("union = %v, want %v", got, want)
			}
		})
	}
}
