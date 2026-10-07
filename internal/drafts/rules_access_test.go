package drafts

import "testing"

// unownedRow is the refusal of a new access row on server for role, an
// application role no server owns.
func unownedRow(role, server string) string {
	return role + " belongs to no MCP server, so it cannot be given access to " + server + ". " +
		"A role that reaches a server belongs to that server, so the identity manager and the console can name the server it reaches. " +
		"Create a role of " + server + " with strazactl roles create " + server + "-<word> --app " + server + " --tools <tool,...>, and compose it and " + role + " into a business role."
}

// accessWorld holds a role of every kind, an owned role with one holder and
// its row, an owned role whose server is gone, and global roles with a row
// on a live server and on a server that is gone.
func accessWorld() World {
	return World{
		Roles: map[string]Role{
			"dev":             {Name: "dev", Kind: RoleKindApplication, Plane: "access"},
			"stale":           {Name: "stale", Kind: RoleKindApplication, Plane: "access"},
			"global-readers":  {Name: "global-readers", Kind: RoleKindApplication, Plane: "access"},
			"engineering":     {Name: "engineering", Kind: RoleKindBusiness, Plane: "access"},
			"auditor":         {Name: "auditor", Kind: RoleKindBusiness, Plane: PlaneControl},
			"sec-approvers":   {Name: "sec-approvers", Kind: RoleKindApprover, Plane: "access"},
			"echoapp-readers": {Name: "echoapp-readers", Kind: RoleKindApplication, Plane: "access", Owner: "echoapp", Owned: true},
			"echoapp-writers": {Name: "echoapp-writers", Kind: RoleKindApplication, Plane: "access", Owner: "echoapp", Owned: true},
			"echoapp-crew":    {Name: "echoapp-crew", Kind: RoleKindApplication, Plane: "access", Owner: "echoapp", Owned: true},
			"echoapp-loose":   {Name: "echoapp-loose", Kind: RoleKindApplication, Plane: "access", Owner: "echoapp", Owned: true},
			"orphan-readers":  {Name: "orphan-readers", Kind: RoleKindApplication, Plane: "access", Owned: true},
		},
		Access: map[string]Access{
			"dev":             {ID: "row-dev", Server: "echoapp", Tools: []string{"echo"}},
			"stale":           {ID: "row-stale", Tools: []string{"echo"}},
			"echoapp-readers": {ID: "row-readers", Server: "echoapp", Tools: []string{"echo"}},
		},
		HolderCounts: map[string]int{"echoapp-readers": 1, "echoapp-writers": 0, "echoapp-crew": 2},
	}
}

func TestAccessCreateRefusal(t *testing.T) {
	t.Parallel()
	full := Standing{Full: true}
	erin := Standing{Servers: map[string]bool{"app-echo": true}}
	echo := []string{"echo"}
	uncounted := "Straza did not count who holds echoapp-loose, so it cannot tell whether its tools may change. Try again, and read the strazad log if it keeps failing."
	cases := []struct {
		name         string
		st           Standing
		role, server string
		tools        []string
		kind         RefusalKind
		want         string
	}{
		{"a role that does not exist", full, "ghost", "echoapp", echo, RefusalMissing,
			`role "ghost" does not exist. Pick one from strazactl roles list, or create one on its MCP server with strazactl roles create <server>-<word> --app <server> --tools <tool,...>.`},
		{"a Straza role", full, "auditor", "echoapp", echo, RefusalInvalid, "Straza role: it governs Straza itself and cannot be given tool access"},
		{"an approver role", full, "sec-approvers", "echoapp", echo, RefusalInvalid, "approver role: it decides approval requests and cannot be given tool access"},
		{"a business role", full, "engineering", "echoapp", echo, RefusalInvalid,
			"business role: it composes application roles and reaches tools through them. Give an application role access instead."},
		{"an owned role on another server", full, "echoapp-writers", "otherapp", echo, RefusalConflict,
			"echoapp-writers belongs to the server echoapp and reaches no other server"},
		{"an owned role whose server is gone", full, "orphan-readers", "otherapp", echo, RefusalConflict,
			"orphan-readers belongs to another server and reaches no other server"},
		{"a global admin gives an owned role the every-tool matcher", full, "echoapp-writers", "echoapp", []string{"*"}, 0, ""},
		{"a server admin gives an owned role the every-tool matcher", erin, "echoapp-writers", "echoapp", []string{"*"}, RefusalInvalid,
			"a server-owned role lists the tools it reaches by name. Only a global admin may give it every tool, and the tools the server adds later, with the * matcher"},
		{"a server admin on a global role", erin, "global-readers", "echoapp", echo, RefusalConflict,
			"global-readers is not a role of the server echoapp. A server admin gives access only to the roles their server owns"},
		{"a server admin on a role one person holds", erin, "echoapp-readers", "echoapp", echo, RefusalConflict,
			"the role echoapp-readers has 1 holder. Its tools change only by the global admin or by a new role"},
		{"a server admin on a role two people hold", erin, "echoapp-crew", "echoapp", echo, RefusalConflict,
			"the role echoapp-crew has 2 holders. Its tools change only by the global admin or by a new role"},
		{"a server admin on a role nobody counted", erin, "echoapp-loose", "echoapp", echo, RefusalUnread, uncounted},
		{"a server admin's every-tool matcher refuses before the count", erin, "echoapp-loose", "echoapp", []string{"*"}, RefusalInvalid, OwnedRoleToolsErr},
		{"a server admin on a global role refuses before the count", erin, "stale", "echoapp", echo, RefusalConflict,
			"stale is not a role of the server echoapp. A server admin gives access only to the roles their server owns"},
		{"a server admin on an owned role nobody holds", erin, "echoapp-writers", "echoapp", echo, 0, ""},
		{"a global admin on a held role", full, "echoapp-crew", "echoapp", echo, 0, ""},
		{"a second row on the same server", full, "echoapp-readers", "echoapp", echo, RefusalExists,
			"this role already has an access row on this server: edit its tools instead of creating a second one"},
		{"a global role's second row on its own server", full, "dev", "echoapp", echo, RefusalExists,
			"this role already has an access row on this server: edit its tools instead of creating a second one"},
		{"a second server", full, "dev", "otherapp", echo, RefusalConflict,
			"dev already reaches echoapp. An application role reaches one server: make a role for otherapp and compose both from a business role."},
		{"a second server when the first is gone", full, "stale", "otherapp", echo, RefusalConflict,
			"stale already reaches another server. An application role reaches one server: make a role for otherapp and compose both from a business role."},
		{"a first row of an owned role", full, "echoapp-writers", "echoapp", echo, 0, ""},
		{"a first row of a global role", full, "global-readers", "echoapp", []string{"*"}, RefusalConflict,
			"global-readers belongs to no MCP server, so it cannot be given access to echoapp. A role that reaches a server belongs to that server, so the identity manager and the console can name the server it reaches. " +
				"Create a role of echoapp with strazactl roles create echoapp-<word> --app echoapp --tools <tool,...>, and compose it and global-readers into a business role."},
		{"a first row of a global role names the server's folded prefix", full, "global-readers", "EchoHub", echo, RefusalConflict,
			"global-readers belongs to no MCP server, so it cannot be given access to EchoHub. A role that reaches a server belongs to that server, so the identity manager and the console can name the server it reaches. " +
				"Create a role of EchoHub with strazactl roles create echohub-<word> --app EchoHub --tools <tool,...>, and compose it and global-readers into a business role."},
		{"a global role the World holds and the store does not", full, "draft-role", "echoapp", echo, RefusalConflict, unownedRow("draft-role", "echoapp")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := accessWorld()
			w.Roles["draft-role"] = Role{Name: "draft-role", Kind: RoleKindApplication}
			w.Apps = map[string]App{"EchoHub": {Name: "EchoHub", RolePrefix: "echohub-"}}
			wantRefusal(t, AccessCreateRefusal(w, tc.st, tc.role, tc.server, tc.tools), tc.kind, tc.want)
		})
	}
}

func TestAccessRemoveRefusal(t *testing.T) {
	t.Parallel()
	full := Standing{Full: true}
	erin := Standing{Servers: map[string]bool{"app-echo": true}}
	cases := []struct {
		name         string
		st           Standing
		role, server string
		kind         RefusalKind
		want         string
	}{
		{"a row whose role is gone", erin, "ghost", "echoapp", 0, ""},
		{"a global admin on a held role", full, "echoapp-readers", "echoapp", 0, ""},
		{"a server admin on a global role", erin, "dev", "echoapp", RefusalConflict,
			"dev is not a role of the server echoapp. A server admin gives access only to the roles their server owns"},
		{"a server admin on a held role", erin, "echoapp-readers", "echoapp", RefusalConflict,
			"the role echoapp-readers has 1 holder. Its tools change only by the global admin or by a new role"},
		{"a server admin on a role nobody holds", erin, "echoapp-writers", "echoapp", 0, ""},
		{"a server admin on a role nobody counted", erin, "echoapp-loose", "echoapp", RefusalUnread,
			"Straza did not count who holds echoapp-loose, so it cannot tell whether its tools may change. Try again, and read the strazad log if it keeps failing."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wantRefusal(t, AccessRemoveRefusal(accessWorld(), tc.st, tc.role, tc.server), tc.kind, tc.want)
		})
	}
}
