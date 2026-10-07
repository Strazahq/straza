package drafts

import (
	"fmt"

	"github.com/strazahq/straza/internal/policy"
)

// ApprovePoolViolations lists every approve.roles entry of doc that the
// approve-pool rule refuses, one line per rule and role, each naming the
// fix. It reads w.Roles. A pool may name approver roles, and straza-admin by
// name as the small shop's escape hatch. An access role must never double as
// decide authority by a coincidence of names, a Straza role other than the
// root has no business deciding agent actions, and a role that does not
// exist would be a doomed pool: nobody holds it, so every record expires.
func ApprovePoolViolations(w World, doc policy.Document) []string {
	return approvePoolViolations(w, doc, false)
}

// approvePoolViolations is ApprovePoolViolations, and for a set in a draft
// it names a third way for a pool that names no role: the draft may create
// the approver role beside the set.
func approvePoolViolations(w World, doc policy.Document, draft bool) []string {
	var out []string
	for _, r := range doc.Spec.Rules {
		if r.Mode != policy.ModeApprove || r.Approve == nil {
			continue
		}
		for _, name := range r.Approve.Roles {
			ro, ok := w.Roles[name]
			switch {
			case !ok && draft:
				out = append(out, fmt.Sprintf(
					"rule %q: approve.roles names %q, which is not a role in Straza: create it first (strazactl roles create %s --kind approver), "+
						"add a Role named %s of kind approver to this draft, or fix the name", r.ID, name, name, name))
			case !ok:
				out = append(out, fmt.Sprintf(
					"rule %q: approve.roles names %q, which is not a role in Straza: create it first (strazactl roles create %s --kind approver) or fix the name",
					r.ID, name, name))
			case name == AdminRole:
				// The root role may always be named as a decider.
			case ro.Plane == PlaneControl:
				out = append(out, fmt.Sprintf(
					"rule %q: approve.roles names %q (straza role): only approver roles or straza-admin may decide. Create one (strazactl roles create <name> --kind approver), assign it in your identity manager, then name it here",
					r.ID, name))
			case ro.Kind != RoleKindApprover:
				out = append(out, fmt.Sprintf(
					"rule %q: approve.roles names %q (%s role): only approver roles or straza-admin may decide. Create one (strazactl roles create <name> --kind approver), assign it in your identity manager, then name it here",
					r.ID, name, ro.Kind))
			}
		}
	}
	return out
}

// MatchRoleViolations lists every match.roles entry of doc that the
// match.roles rule refuses, one line per role, each naming the fix. It reads
// w.Roles. A selector may name only application roles, because they carry
// the tools and a session holds the closure of its roles, so naming the
// application role covers every path to its tools. A set naming a business
// role governs one grant path, approver roles grant no tools, and Straza
// roles never match sessions. A name no role has stays legal, unlike in
// approve.roles: a selector matches nobody until the role exists.
func MatchRoleViolations(w World, doc policy.Document) []string {
	var out []string
	for _, name := range doc.Spec.Match.Roles {
		ro, ok := w.Roles[name]
		if !ok {
			continue
		}
		switch {
		case ro.Plane == PlaneControl:
			out = append(out, fmt.Sprintf(
				"match.roles names %q (straza role): a Straza role governs Straza itself and never matches sessions",
				name))
		case ro.Kind == RoleKindApprover:
			out = append(out, fmt.Sprintf(
				"match.roles names %q (approver role): approver roles carry no tools and never match sessions; name them in approve.roles",
				name))
		case ro.Kind == RoleKindBusiness:
			out = append(out, fmt.Sprintf(
				"match.roles names %q (business role): policies match the application roles that carry the tools (a session holds every role its roles compose, so those cover every holder). Name the application roles it composes instead",
				name))
		}
	}
	return out
}

// NamesRoles reports whether doc names a role that ApprovePoolViolations or
// MatchRoleViolations looks up: an approve.roles entry of an approve rule,
// or a match.roles entry. When it does not, neither rule reads w.Roles.
func NamesRoles(doc policy.Document) bool {
	if len(doc.Spec.Match.Roles) > 0 {
		return true
	}
	for _, r := range doc.Spec.Rules {
		if r.Mode == policy.ModeApprove && r.Approve != nil && len(r.Approve.Roles) > 0 {
			return true
		}
	}
	return false
}

// ObligationViolations lists every rule of doc that carries an obligations
// list, one line per rule, each naming the fix. The redact and notify
// obligations never ran in any lane, so a set that carries them is refused
// with the two replacements: capture.mode redact on the set, and
// approve.notify on the rule.
func ObligationViolations(doc policy.Document) []string {
	var out []string
	for i, r := range doc.Spec.Rules {
		if len(r.Obligations) == 0 {
			continue
		}
		out = append(out, fmt.Sprintf(
			"rules[%d] (%s): obligations are not supported and never ran. Remove the obligations list. To keep recorded content out of the transcript, set capture.mode: redact on the set. To tell people about a call waiting on a hold, set approve.notify on the rule.",
			i, r.ID))
	}
	return out
}
