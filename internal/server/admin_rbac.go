package server

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/tokenscopes"
)

// Delegated admin: admin.roleAreas
// maps ordinary role names (IdM-delivered like any other membership) to
// per-area grants, reusing the admin API token vocabulary and the adminRouteArea
// enforcement seam verbatim (internal/tokenscopes). The IdM decides WHO holds a
// role; this map decides WHAT it may administer: a role name carries no
// admin power until an entry here grants it, so an IdM admin can never
// conjure straza privilege by naming a role.

// buildRoleAreaScopes validates admin.roleAreas and precomputes each role's
// scope at construction: a bad entry refuses startup (fail closed, never a
// silently inert delegation), and request-time work is a map lookup.
func buildRoleAreaScopes(cfg config.Admin, log *slog.Logger) (map[string]tokenscopes.Scope, error) {
	scopes := make(map[string]tokenscopes.Scope, len(cfg.RoleAreas))
	for role, grants := range cfg.RoleAreas {
		if role == AdminRole {
			return nil, fmt.Errorf("admin.roleAreas must not map %q. It is the root role by definition and a map entry would silently narrow it", AdminRole)
		}
		if role == MCPAdminRole {
			return nil, fmt.Errorf("admin.roleAreas must not map %q. Its meaning is fixed by the product (apps:read, apps:write) and a map entry would silently narrow it", MCPAdminRole)
		}
		if role == DraftConfigRole {
			return nil, fmt.Errorf("admin.roleAreas must not map %q. It lets an agent draft through the built-in straza MCP server and opens no console area, and a map entry would make it an admin delegate", DraftConfigRole)
		}
		ts, err := tokenscopes.Parse(strings.Join(grants, ","))
		if err != nil {
			return nil, fmt.Errorf("admin.roleAreas[%q]: %w", role, err)
		}
		if ts.Full {
			return nil, fmt.Errorf("admin.roleAreas[%q]: \"full\" is not grantable here. Root has one spelling, the %s role; assign it in your identity manager (it renders as a wire-group like any role)", role, AdminRole)
		}
		if ts.Grants["tokens:write"] {
			log.Warn("admin.roleAreas grants tokens:write (minting admin credentials is root-equivalent)", "role", role)
		}
		scopes[role] = ts
	}
	return scopes, nil
}

// adminGrantsForRoles renders a session's admin standing for the checkin
// response (the console's tab-discovery field): "full" for root, the
// canonical sorted grant union for delegated roles, and "" (field omitted)
// for everyone else. Display only; enforcement never reads it.
func (a *App) adminGrantsForRoles(roles []store.Role) string {
	for _, role := range roles {
		if role.Name == AdminRole {
			return "full"
		}
	}
	return a.scopeForRoles(roles).String()
}

// scopeForRoles unions a session's resolved roles through admin.roleAreas,
// plus the product role's fixed apps grants. Unmapped roles contribute
// nothing; an empty union means the session has no delegated admin
// standing at all.
func (a *App) scopeForRoles(roles []store.Role) tokenscopes.Scope {
	ts := tokenscopes.Scope{Grants: map[string]bool{}}
	for _, role := range roles {
		if role.Name == MCPAdminRole {
			ts.Grants["apps:read"], ts.Grants["apps:write"] = true, true
		}
		for g := range a.roleAreaScopes[role.Name].Grants {
			ts.Grants[g] = true
		}
	}
	return ts
}
