package drafts

import "fmt"

// AccessCreateRefusal refuses a new access row that gives role the tools on
// the server named server, or answers nil. tools is the matcher list as the
// row would store it. It reads w.Roles and w.Access, and w.HolderCounts of
// role only when a server admin changes a role of the server, where a count
// it lacks answers RefusalUnread. The role must exist and be an application
// role, the server-owned rules hold, the role has no row yet, on this
// server or another, and a server owns it.
func AccessCreateRefusal(w World, st Standing, role, server string, tools []string) *Refusal {
	ro, ok := w.Roles[role]
	if !ok {
		return refused(RefusalMissing, UnknownRoleMessage(role))
	}
	if msg := roleRefusal(ro, "cannot be given tool access", "Give an application role access instead."); msg != "" {
		return refused(RefusalInvalid, msg)
	}
	if ref := ownedAccessRefusal(w, st, ro, server, tools, true); ref != nil {
		return ref
	}
	if ref := accessRowTaken(w, role, server); ref != nil {
		return ref
	}
	return unownedRowRefusal(w, ro, server)
}

// unownedRowRefusal refuses an access row on the server named server for
// ro, an application role no server owns, or answers nil. It reads w.Access
// and w.Apps. Such a role keeps the row live state holds for it on that
// server, tools edited or not, and gains no other, because a role that
// reaches a server belongs to that server.
func unownedRowRefusal(w World, ro Role, server string) *Refusal {
	if ro.Owned {
		return nil
	}
	if acc, ok := w.Access[ro.Name]; ok && acc.Server == server {
		return nil
	}
	prefix := server + "-"
	if app, ok := w.Apps[server]; ok && app.RolePrefix != "" {
		prefix = app.RolePrefix
	}
	return refused(RefusalConflict, fmt.Sprintf(
		"%s belongs to no MCP server, so it cannot be given access to %s. A role that reaches a server belongs to that server, so the identity manager and the console can name the server it reaches. "+
			"Create a role of %s with strazactl roles create %s<word> --app %s --tools <tool,...>, and compose it and %s into a business role.",
		ro.Name, server, server, prefix, server, ro.Name))
}

// AccessRemoveRefusal refuses the removal of role's access row on the
// server named server, or answers nil. It reads w.Roles, and w.HolderCounts
// of role as AccessCreateRefusal does. A row whose role the World does not
// hold may go.
func AccessRemoveRefusal(w World, st Standing, role, server string) *Refusal {
	ro, ok := w.Roles[role]
	if !ok {
		return nil
	}
	return ownedAccessRefusal(w, st, ro, server, nil, false)
}

// ownedAccessRefusal applies the server-owned rules to a row of ro on the
// server named server, with tools the requested matchers of a create. An
// owned role reaches only its own server, a server admin gives and takes
// access only for the roles their server owns, only a full standing gives
// an owned role the every-tool matcher, and a server admin cannot change
// the tools of a role anyone holds.
func ownedAccessRefusal(w World, st Standing, ro Role, server string, tools []string, create bool) *Refusal {
	if create && ro.Owned && ro.Owner != server {
		owner := "another server"
		if ro.Owner != "" {
			owner = "the server " + ro.Owner
		}
		return refused(RefusalConflict, fmt.Sprintf("%s belongs to %s and reaches no other server", ro.Name, owner))
	}
	if !st.Full && ro.Owner != server {
		return refused(RefusalConflict, fmt.Sprintf(
			"%s is not a role of the server %s. A server admin gives access only to the roles their server owns", ro.Name, server))
	}
	if ro.Owned && !st.Full && hasCatalogGlob(tools) {
		return refused(RefusalInvalid, OwnedRoleToolsErr)
	}
	if !st.Full && ro.Owned {
		n, counted := w.HolderCounts[ro.Name]
		if !counted {
			return refused(RefusalUnread, fmt.Sprintf(
				"Straza did not count who holds %s, so it cannot tell whether its tools may change. Try again, and read the strazad log if it keeps failing.", ro.Name))
		}
		if n > 0 {
			return refused(RefusalConflict, fmt.Sprintf(
				"the role %s has %s. Its tools change only by the global admin or by a new role", ro.Name, HoldersPhrase(n)))
		}
	}
	return nil
}

// accessRowTaken states the two rules the database keeps as unique indexes
// on access rows: a role has one row on a server, and an application role
// reaches one server. It refuses a new row for role on server when the role
// has a row already, and names the server of a row elsewhere, or another
// server when that server is gone.
func accessRowTaken(w World, role, server string) *Refusal {
	acc, ok := w.Access[role]
	if !ok {
		return nil
	}
	if acc.Server == server {
		return refused(RefusalExists, "this role already has an access row on this server: edit its tools instead of creating a second one")
	}
	other := acc.Server
	if other == "" {
		other = "another server"
	}
	return refused(RefusalConflict, fmt.Sprintf(
		"%s already reaches %s. An application role reaches one server: make a role for %s and compose both from a business role.",
		role, other, server))
}
