package drafts

import (
	"fmt"
	"sort"
	"strings"
)

// The refusals of the server-owned role rules, worded once.
const (
	OwnedRoleKindErr    = "a server-owned role is always an application role. Leave kind empty or set it to application"
	OwnedRoleToolsErr   = "a server-owned role lists the tools it reaches by name. Only a global admin may give it every tool, and the tools the server adds later, with the * matcher"
	OwnedRoleServerErr  = "a server admin creates roles for their server. Name it in server"
	OwnedRoleImpliesErr = "a server-owned role composes no other role. Compose it into a business role instead"
)

// roleKindEnum refuses kind, which the admin API does not know, naming the
// value sent and the four kinds to set instead.
func roleKindEnum(kind string) string {
	return fmt.Sprintf("kind must be business, application, approver or straza, and it is %q. Set it to one of the four", kind)
}

// RoleSpec is a role as a create request names it. Kind is in the words of
// the admin API: business, application, approver or straza, and empty for
// the default. Server and Tools name the owning server and the tools of the
// role's one access row, and are empty for a global role.
type RoleSpec struct {
	Name   string
	Kind   string
	Server string
	Tools  []string
}

// RoleCreateRefusal refuses the creation of the role spec names, or answers
// nil. It reads w.Roles and w.Apps. The straza- and mcp-admin- prefixes
// belong to the product, the kind must be one the API knows, a server admin
// creates roles only for a server they administer, a global role may not
// wear a server's prefix, and no two roles share a name.
func RoleCreateRefusal(w World, st Standing, spec RoleSpec) *Refusal {
	lower := strings.ToLower(spec.Name)
	// The straza- prefix is the product's namespace, so org vocabulary never
	// collides with a product role's meaning, and names never change after
	// create. The minted prefix is reserved the same way, so a role wearing
	// it is product-minted by definition and goes with its server.
	switch {
	case strings.HasPrefix(lower, "straza-"):
		return refused(RefusalInvalid, "role names beginning with straza- are reserved for product-defined roles; choose a name without the straza- prefix")
	case strings.HasPrefix(lower, AppAdminRolePrefix):
		return refused(RefusalInvalid, "role names beginning with "+AppAdminRolePrefix+" are reserved for the roles Straza creates with a server; choose a name without that prefix")
	case !roleKindValid(spec.Kind):
		return refused(RefusalInvalid, roleKindEnum(spec.Kind))
	}
	if !st.Full && spec.Server == "" {
		if st.AreaRefusal != "" {
			return refused(RefusalForbidden, st.AreaRefusal)
		}
		return refused(RefusalInvalid, OwnedRoleServerErr)
	}
	if spec.Server != "" {
		return ownedRoleRefusal(w, st, spec)
	}
	// A server's prefix is exclusive: a global role wearing it would read as
	// the server's while its owner says otherwise.
	if owner := ownerByPrefix(w.Apps, spec.Name); owner != "" {
		return refused(RefusalInvalid, fmt.Sprintf(
			"role names beginning with %s belong to the server %s. Create it on that server's page so it becomes server-owned",
			w.Apps[owner].RolePrefix, owner))
	}
	if _, ok := w.Roles[spec.Name]; ok {
		return RoleExists(spec.Name)
	}
	return nil
}

// ownedRoleRefusal applies the server-owned role rules to spec, which names
// a server: the server exists, the caller administers it, the role is an
// application role named with the server's prefix, it lists its tools, only
// a full standing gives it the every-tool matcher, and its name is free.
func ownedRoleRefusal(w World, st Standing, spec RoleSpec) *Refusal {
	app, ok := w.Apps[spec.Server]
	if !ok {
		return refused(RefusalMissing, fmt.Sprintf(
			"no server named %s is registered, so no role can be owned by it. Check the name with strazactl apps list", spec.Server))
	}
	if !st.Full && !st.Servers[app.ID] {
		return refused(RefusalForbidden, ServerAdminRefusal(app.AdminRole))
	}
	// A server admin's kind is forced to application, and a global admin who
	// asks for another kind is told.
	if st.Full && spec.Kind != "" && spec.Kind != RoleKindApplication {
		return refused(RefusalInvalid, OwnedRoleKindErr)
	}
	if !strings.HasPrefix(spec.Name, app.RolePrefix) || len(spec.Name) == len(app.RolePrefix) {
		return refused(RefusalInvalid, fmt.Sprintf(
			"a role of the server %s is named %s<suffix>. The server's page fills the prefix for you", app.Name, app.RolePrefix))
	}
	if len(spec.Tools) == 0 || (!st.Full && hasCatalogGlob(spec.Tools)) {
		return refused(RefusalInvalid, OwnedRoleToolsErr)
	}
	if _, ok := w.Roles[spec.Name]; ok {
		return RoleExists(spec.Name)
	}
	return nil
}

// RoleExists refuses the creation of a role whose name another role has.
func RoleExists(name string) *Refusal {
	return refused(RefusalExists, fmt.Sprintf(
		"a role named %s already exists. Pick another name, or open %s under Roles to change it", name, name))
}

// RoleKindChangeRefusal refuses a role update that sends kind for ro, or
// answers nil. The kind of a server-owned role is fixed at application: a
// server admin sends none, and a global admin may only restate it. Every
// other role keeps the kind it was created with, because every kind rule was
// judged against it: a kind moved afterwards would keep access rows the
// server refuses to create. Restating the current kind passes, so a client
// that sends the whole role back keeps working.
func RoleKindChangeRefusal(ro Role, st Standing, kind string) *Refusal {
	if ro.Owned && (!st.Full || kind != RoleKindApplication) {
		return refused(RefusalInvalid, OwnedRoleKindErr)
	}
	if kind == "" || !roleKindValid(kind) {
		return refused(RefusalInvalid, roleKindEnum(kind))
	}
	if kind != wireKind(ro) {
		noun := map[string]string{
			RoleKindBusiness:    "a business role",
			RoleKindApplication: "an application role",
			RoleKindApprover:    "an approver role",
			RoleKindStraza:      "a Straza role",
		}[wireKind(ro)]
		return refused(RefusalInvalid, fmt.Sprintf(
			"%s is %s, and a role's kind is fixed at create. Create a new role of the kind you need, move its holders there in the identity manager, then delete this one", ro.Name, noun))
	}
	return nil
}

// ImplicationRefusal refuses the edge that makes role imply implies, or
// answers nil. It reads w.Roles and w.Implies. A server-owned role implies
// nothing, so its closure never leaves its server. Approver roles stand
// alone on both ends: who may decide is the direct member list the identity
// manager certifies, so an implied approver would be a hidden decider and an
// approver implying access would smuggle tools. No edge may close a cycle. A
// name no role has, the empty name included, passes the role rules, and it
// closes a cycle only with itself, which the sentence words without a name.
func ImplicationRefusal(w World, role, implies string) *Refusal {
	if ro, ok := w.Roles[role]; ok && ro.Owned {
		return refused(RefusalInvalid, OwnedRoleImpliesErr)
	}
	for _, name := range []string{role, implies} {
		if ro, ok := w.Roles[name]; ok && ro.Kind == RoleKindApprover {
			return refused(RefusalInvalid, fmt.Sprintf(
				"%s is an approver role, which stands alone: no role composes it and it composes no role, because who may decide is its direct member list, certified as it stands. Drop the implication, and assign %s directly to each person who decides instead", name, name))
		}
	}
	switch {
	case role == implies:
		subject := role
		if subject == "" {
			subject = "the role"
		}
		return refused(RefusalConflict, subject+" would imply itself, and a role never composes itself. Drop the implication")
	case wouldCycle(w.Implies, role, implies):
		return refused(RefusalConflict, fmt.Sprintf(
			"%s would imply %s, and %s implies %s, so the two would compose each other in a circle. Drop this implication, or the path that leads back from %s to %s",
			role, implies, implies, role, implies, role))
	}
	return nil
}

// UnknownRoleMessage says that a role does not exist and how to find one or
// create one on its MCP server.
func UnknownRoleMessage(name string) string {
	return fmt.Sprintf("role %q does not exist. Pick one from strazactl roles list, or create one on its MCP server with strazactl roles create <server>-<word> --app <server> --tools <tool,...>.", name)
}

// HoldersPhrase words a holder count for a refusal.
func HoldersPhrase(n int) string {
	if n == 1 {
		return "1 holder"
	}
	return fmt.Sprintf("%d holders", n)
}

// roleRefusal says why ro cannot hold tools or a secret, empty for an
// application role. Straza and approver roles never do, and a business role
// reaches tools through the application roles it composes. cannot completes
// the sentence for the first two, and businessFix follows it for a business
// role.
func roleRefusal(ro Role, cannot, businessFix string) string {
	switch {
	case ro.Plane == PlaneControl:
		return "Straza role: it governs Straza itself and " + cannot
	case ro.Kind == RoleKindApprover:
		return "approver role: it decides approval requests and " + cannot
	case ro.Kind == RoleKindBusiness:
		return "business role: it composes application roles and reaches tools through them. " + businessFix
	}
	return ""
}

// roleKindValid reports whether kind is a kind the admin API knows, or empty
// for the default.
func roleKindValid(kind string) bool {
	return kind == "" || kind == RoleKindBusiness || kind == RoleKindApplication ||
		kind == RoleKindApprover || kind == RoleKindStraza
}

// wireKind is ro's kind in the words of the admin API, straza for a role on
// the control plane.
func wireKind(ro Role) string {
	if ro.Plane == PlaneControl {
		return RoleKindStraza
	}
	return ro.Kind
}

// hasCatalogGlob reports whether a tool list carries the every-tool matcher.
func hasCatalogGlob(tools []string) bool {
	for _, t := range tools {
		if t == "*" {
			return true
		}
	}
	return false
}

// ownerByPrefix names the server whose role prefix opens the role name,
// folded to lower case, the longest such prefix when several do, and the
// first by server name among equals, or nothing.
func ownerByPrefix(apps map[string]App, name string) string {
	lower := strings.ToLower(name)
	names := make([]string, 0, len(apps))
	for n := range apps {
		names = append(names, n)
	}
	sort.Strings(names)
	// The bare hyphen of a server name that folds to nothing owns no role.
	owner, longest := "", 1
	for _, n := range names {
		if p := apps[n].RolePrefix; strings.HasPrefix(lower, p) && len(p) > longest {
			owner, longest = n, len(p)
		}
	}
	return owner
}

// ServerAdminRefusal names the role a server admin would need for a server
// whose admin role is adminRole, unset when the server has none.
func ServerAdminRefusal(adminRole string) string {
	if adminRole == "" {
		adminRole = "unset"
	}
	return "this server's admin role is " + adminRole + ", which you do not hold. Ask your identity manager for " + adminRole +
		", or a holder of " + MCPAdminRole + " to make the change."
}

// wouldCycle reports whether an edge from from to to would close a cycle
// over the edges in implies: whether from is to itself or reachable from it.
func wouldCycle(implies map[string][]string, from, to string) bool {
	if from == to {
		return true
	}
	seen := map[string]bool{to: true}
	queue := []string{to}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range implies[cur] {
			if next == from {
				return true
			}
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return false
}
