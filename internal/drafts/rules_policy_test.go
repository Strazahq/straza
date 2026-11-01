package drafts

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// probeDoc parses a set that matches match and holds one approve rule whose
// pool is pool, both comma-separated role lists.
func probeDoc(t *testing.T, match, pool string) policy.Document {
	t.Helper()
	doc, err := policy.Parse([]byte(`apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: rules-probe }
spec:
  priority: 100
  match: { roles: [` + match + `] }
  rules:
    - id: held
      tools: [shell.exec]
      command: { allowPatterns: ["echo *"] }
      effect: allow
      mode: approve
      approve:
        roles: [` + pool + `]
      reason: "Straza: held"
`))
	if err != nil {
		t.Fatalf("parse the probe set: %v", err)
	}
	return doc
}

// policyWorld holds one role of every kind the policy rules tell apart.
func policyWorld() World {
	return World{Roles: map[string]Role{
		"sec-approvers": {Name: "sec-approvers", Kind: RoleKindApprover, Plane: "access"},
		"dev":           {Name: "dev", Kind: RoleKindApplication, Plane: "access"},
		"engineering":   {Name: "engineering", Kind: RoleKindBusiness, Plane: "access"},
		"auditor":       {Name: "auditor", Kind: RoleKindBusiness, Plane: PlaneControl},
		AdminRole:       {Name: AdminRole, Kind: RoleKindBusiness, Plane: PlaneControl},
	}}
}

func TestApprovePoolViolations(t *testing.T) {
	t.Parallel()
	fix := "only approver roles or straza-admin may decide. Create one (strazactl roles create <name> --kind approver), assign it in your identity manager, then name it here"
	cases := []struct {
		name  string
		world World
		pool  string
		want  []string
	}{
		{"an approver role is a legal pool", policyWorld(), "sec-approvers", nil},
		{"straza-admin is legal by name", policyWorld(), AdminRole, nil},
		{"an approver role the World holds and the store does not is a legal pool",
			World{Roles: map[string]Role{"release-approvers": {Name: "release-approvers", Kind: RoleKindApprover}}}, "release-approvers", nil},
		{"a name no role has is refused", policyWorld(), "ghost", []string{
			`rule "held": approve.roles names "ghost", which is not a role in Straza: create it first (strazactl roles create ghost --kind approver) or fix the name`}},
		{"a Straza role is refused", policyWorld(), "auditor", []string{`rule "held": approve.roles names "auditor" (straza role): ` + fix}},
		{"an application role is refused", policyWorld(), "dev", []string{`rule "held": approve.roles names "dev" (application role): ` + fix}},
		{"a business role is refused", policyWorld(), "engineering", []string{`rule "held": approve.roles names "engineering" (business role): ` + fix}},
		{"every refused name gets its own line, in order", policyWorld(), "dev, sec-approvers, ghost", []string{
			`rule "held": approve.roles names "dev" (application role): ` + fix,
			`rule "held": approve.roles names "ghost", which is not a role in Straza: create it first (strazactl roles create ghost --kind approver) or fix the name`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ApprovePoolViolations(tc.world, probeDoc(t, "dev", tc.pool)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("violations = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMatchRoleViolations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		match string
		want  []string
	}{
		{"an application role is a legal selector", "dev", nil},
		{"a name no role has is a legal selector", "ghost", nil},
		{"a Straza role is refused", "auditor", []string{
			`match.roles names "auditor" (straza role): a Straza role governs Straza itself and never matches sessions`}},
		{"an approver role is refused", "sec-approvers", []string{
			`match.roles names "sec-approvers" (approver role): approver roles carry no tools and never match sessions; name them in approve.roles`}},
		{"a business role is refused", "engineering", []string{
			`match.roles names "engineering" (business role): policies match the application roles that carry the tools (a session holds every role its roles compose, so those cover every holder). Name the application roles it composes instead`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := MatchRoleViolations(policyWorld(), probeDoc(t, tc.match, "sec-approvers")); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("violations = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNamesRoles(t *testing.T) {
	t.Parallel()
	plain, err := policy.Parse([]byte(`apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: plain-probe }
spec:
  priority: 90
  match: { users: [kim] }
  rules:
    - id: no-rm
      tools: [shell.exec]
      command: { denyPatterns: ["rm -rf *"] }
      effect: deny
      reason: "Straza: blocked"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pooled := probeDoc(t, "dev", "sec-approvers")
	matchOnly := probeDoc(t, "dev", "sec-approvers")
	matchOnly.Spec.Rules = nil
	poolOnly := probeDoc(t, "dev", "sec-approvers")
	poolOnly.Spec.Match.Roles = nil
	cases := []struct {
		name string
		doc  policy.Document
		want bool
	}{
		{"a set that names no role", plain, false},
		{"a set with a pool and a selector", pooled, true},
		{"a set with a selector alone", matchOnly, true},
		{"a set with a pool alone", poolOnly, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := NamesRoles(tc.doc); got != tc.want {
				t.Errorf("NamesRoles = %v, want %v", got, tc.want)
			}
			if !tc.want && (ApprovePoolViolations(World{}, tc.doc) != nil || MatchRoleViolations(World{}, tc.doc) != nil) {
				t.Error("a set that names no role is refused over a World with no roles")
			}
		})
	}
}

func TestObligationViolations(t *testing.T) {
	t.Parallel()
	const set = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: obligations-probe }
spec:
  priority: 90
  match: { roles: [dev] }
  rules:
    - id: quiet
      tools: [shell.exec]
      command: { allowPatterns: ["echo *"] }
      effect: allow
    - id: watch-secrets
      tools: [file.read]
      paths: { allow: ["**/secrets/**"] }
      effect: allow
      OBLIGATIONS
`
	cases := []struct {
		name string
		list string
		want []string
	}{
		{"a set without the list passes", "", nil},
		{"a rule with the list is refused with both replacements named", "obligations: [redact, notify]", []string{
			"rules[1] (watch-secrets): obligations are not supported and never ran. Remove the obligations list. To keep recorded content out of the transcript, set capture.mode: redact on the set. To tell people about a call waiting on a hold, set approve.notify on the rule."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc, err := policy.Parse([]byte(strings.Replace(set, "OBLIGATIONS", tc.list, 1)))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := ObligationViolations(doc); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("violations = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestByNameViolations pins the write gate of a rule that names harness
// tools: it lists only other, carries no other matcher and takes whole
// names, and it allows a classified tool by name only when the published
// set already did, so a set keeps that rule after a release classifies the
// tool.
func TestByNameViolations(t *testing.T) {
	t.Parallel()
	parse := func(t *testing.T, rules string) policy.Document {
		t.Helper()
		doc, err := policy.Parse([]byte("apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata: { name: by-name }\nspec:\n  rules:\n" + rules))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		return doc
	}
	classified := func() (map[string]string, error) {
		return map[string]string{"Bash": policy.ToolShellExec, "SubagentHandback": policy.ToolTaskSpawn}, nil
	}
	const handback = "    - id: handback\n      tools: [other]\n      toolNames: { allow: [SubagentHandback] }\n"
	cases := []struct {
		name, rules, live string
		want              []string
	}{
		{"an unclassified name passes", "    - id: future\n      tools: [other]\n      toolNames: { allow: [FutureTool] }\n", "", nil},
		{"a classified name on the deny side passes", "    - id: no-bash\n      tools: [other]\n      toolNames: { deny: [Bash] }\n", "", nil},
		{"a toolNames rule without other is not a by-name rule", "    - id: mcp\n      toolNames: { allow: [\"read_*\"] }\n", "", nil},
		{"a classified name the live set allowed stays", handback, handback, nil},
		{"a classified name on the allow side is refused", "    - id: bash\n      tools: [other]\n      toolNames: { allow: [Bash] }\n", "", []string{
			`rule "bash": toolNames.allow names Bash, which Straza already classifies as shell.exec. Allow it with a shell.exec rule, so the rules for that kind keep governing it.`}},
		{"a newly added classified name is refused", handback, "    - id: handback\n      tools: [other]\n      toolNames: { deny: [SubagentHandback] }\n", []string{
			`rule "handback": toolNames.allow names SubagentHandback, which Straza already classifies as task.spawn. Allow it with a task.spawn rule, so the rules for that kind keep governing it.`}},
		{"a pattern is refused", "    - id: subs\n      tools: [other]\n      toolNames: { allow: [\"Sub*\"], deny: [\"re:Cron.*\"] }\n", "", []string{
			`rule "subs": toolNames on the other tool takes exact tool names only, and "Sub*", "re:Cron.*" are patterns. Straza compares these names whole, so a pattern matches no tool. Write each name in full, for example SubagentHandback.`}},
		{"another tool kind is refused", "    - id: mixed\n      tools: [other, shell.exec]\n      toolNames: { deny: [FutureTool] }\n", "", []string{
			`rule "mixed": a rule that names harness tools in toolNames lists only other in tools, because the names pick the tools. Move the other tool kinds to a rule of their own.`}},
		{"another matcher is refused", "    - id: extra\n      tools: [other]\n      toolNames: { deny: [FutureTool] }\n      command: { denyPatterns: [\"rm *\"] }\n", "", []string{
			`rule "extra": a rule that names harness tools takes no command block, because it governs exactly the tools it names. Move that block to a rule of its own.`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var live policy.Document
			if tc.live != "" {
				live = parse(t, tc.live)
			}
			if got := ByNameViolations(parse(t, tc.rules), live, classified); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("violations = %q, want %q", got, tc.want)
			}
		})
	}
	// The gate fails closed: a table it cannot read refuses a set with a
	// by-name rule, and a set without one never reads it.
	broken := func() (map[string]string, error) { return nil, errors.New("embedded mappings unreadable") }
	future := parse(t, "    - id: future\n      tools: [other]\n      toolNames: { allow: [FutureTool] }\n")
	for _, tools := range []func() (map[string]string, error){broken, nil} {
		if got := ByNameViolations(future, policy.Document{}, tools); len(got) != 1 || !strings.Contains(got[0], "could not read its built-in tool mappings") {
			t.Errorf("violations with no table = %q, want the one fail-closed refusal", got)
		}
	}
	if got := ByNameViolations(parse(t, "    - id: plain\n      tools: [shell.exec]\n      effect: allow\n"), policy.Document{}, broken); got != nil {
		t.Errorf("a set without a by-name rule read the table: %q", got)
	}
}
