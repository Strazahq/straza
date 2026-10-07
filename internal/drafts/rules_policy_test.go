package drafts

import (
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
