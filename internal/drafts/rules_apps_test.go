package drafts

import "testing"

func TestSecretRefusal(t *testing.T) {
	t.Parallel()
	w := World{Roles: map[string]Role{
		"dev":           {Name: "dev", Kind: RoleKindApplication, Plane: "access"},
		"engineering":   {Name: "engineering", Kind: RoleKindBusiness, Plane: "access"},
		"auditor":       {Name: "auditor", Kind: RoleKindBusiness, Plane: PlaneControl},
		"sec-approvers": {Name: "sec-approvers", Kind: RoleKindApprover, Plane: "access"},
	}}
	static := App{Name: "github", Credential: "static"}
	cases := []struct {
		name string
		app  App
		role string
		kind RefusalKind
		want string
	}{
		{"a manifest nobody read", App{Name: "github"}, "", RefusalUnread,
			"Straza did not read the manifest of the MCP server github, so it cannot tell whether a secret has a use there. Try again, and read the strazad log if it keeps failing."},
		{"a server with no credential", App{Name: "github", Credential: CredentialNone}, "", RefusalInvalid,
			"the MCP server github declares no credential (credential.kind none), so a secret would never be used. Change the manifest's credential block first."},
		{"a static server", static, "", 0, ""},
		{"each caller's own sign-in", App{Name: "github", Credential: CredentialOAuth, Agents: "own"}, "", RefusalInvalid,
			"the MCP server github uses each caller's own sign-in (credential.kind oauth) and lets no agent use a shared account (credential.agents own), so a static secret would never be used. Set credential.agents: shared in the manifest first."},
		{"each caller's own token", App{Name: "github", Credential: CredentialToken, Agents: "sponsor"}, "", RefusalInvalid,
			"the MCP server github uses each caller's own token (credential.kind token) and lets no agent use a shared account (credential.agents sponsor), so a static secret would never be used. Set credential.agents: shared in the manifest first."},
		{"a sign-in server that shares an account with agents", App{Name: "github", Credential: CredentialOAuth, Agents: AgentsShared}, "", 0, ""},
		{"a token server that shares an account with agents", App{Name: "github", Credential: CredentialToken, Agents: AgentsShared}, "dev", 0, ""},
		{"a role that does not exist", static, "ghost", RefusalMissing,
			`role "ghost" does not exist. Pick one from strazactl roles list, or create one on its MCP server with strazactl roles create <server>-<word> --app <server> --tools <tool,...>.`},
		{"a Straza role", static, "auditor", RefusalInvalid, "Straza role: it governs Straza itself and cannot hold a secret"},
		{"an approver role", static, "sec-approvers", RefusalInvalid, "approver role: it decides approval requests and cannot hold a secret"},
		{"a business role", static, "engineering", RefusalInvalid,
			"business role: it composes application roles and reaches tools through them. Set the secret for an application role instead, or for the server itself."},
		{"an application role", static, "dev", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// The server's own secret reads no role, so it runs over no roles.
			world := w
			if tc.role == "" {
				world = World{}
			}
			wantRefusal(t, SecretRefusal(world, tc.app, tc.role), tc.kind, tc.want)
		})
	}
}

func TestRegisterRefusal(t *testing.T) {
	t.Parallel()
	w := World{Apps: map[string]App{
		"echoapp":  {ID: "app-echo", Name: "echoapp"},
		"otherapp": {ID: "app-other", Name: "otherapp"},
	}}
	one := Standing{Servers: map[string]bool{"app-echo": true}}
	two := Standing{Servers: map[string]bool{"app-echo": true, "app-third": true}}
	cases := []struct {
		name  string
		world World
		st    Standing
		app   string
		kind  RefusalKind
		want  string
	}{
		{"a full admin registers a new server", w, Standing{Full: true}, "newapp", 0, ""},
		{"a server admin changes their server", w, one, "echoapp", 0, ""},
		{"a server admin changes another server", w, one, "otherapp", RefusalForbidden,
			"registering a new server needs the scope apps:write or the role straza-global-mcp-admin. You administer 1 server and may change it."},
		{"a server admin registers a new server", w, two, "newapp", RefusalForbidden,
			"registering a new server needs the scope apps:write or the role straza-global-mcp-admin. You administer 2 servers and may change those."},
		{"a server admin with a World that holds no server", World{}, one, "echoapp", RefusalForbidden,
			"registering a new server needs the scope apps:write or the role straza-global-mcp-admin. You administer 1 server and may change it."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wantRefusal(t, RegisterRefusal(tc.world, tc.st, tc.app), tc.kind, tc.want)
		})
	}
}
