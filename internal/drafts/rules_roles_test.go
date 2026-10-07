package drafts

import (
	"fmt"
	"math/rand"
	"testing"
)

// wantRefusal compares a rule's answer with the kind and sentence a case wants,
// where kind 0 wants no refusal.
func wantRefusal(t *testing.T, got *Refusal, kind RefusalKind, sentence string) {
	t.Helper()
	switch {
	case got == nil && kind != 0:
		t.Errorf("no refusal, want kind %d %q", kind, sentence)
	case got != nil && kind == 0:
		t.Errorf("refusal kind %d %q, want none", got.Kind, got.Sentence)
	case got != nil && (got.Kind != kind || got.Sentence != sentence):
		t.Errorf("refusal kind %d %q, want kind %d %q", got.Kind, got.Sentence, kind, sentence)
	}
}

// roleWorld holds two servers with their admin roles, one server whose
// admin role is gone, two servers whose prefixes nest, a server-owned role
// and a global role.
func roleWorld() World {
	return World{
		Apps: map[string]App{
			"echoapp":  {ID: "app-echo", Name: "echoapp", RolePrefix: "echoapp-", AdminRole: "mcp-admin-echoapp"},
			"otherapp": {ID: "app-other", Name: "otherapp", RolePrefix: "otherapp-", AdminRole: "mcp-admin-otherapp"},
			"lostapp":  {ID: "app-lost", Name: "lostapp", RolePrefix: "lostapp-"},
			"echo":     {ID: "app-e", Name: "echo", RolePrefix: "echo-", AdminRole: "mcp-admin-echo"},
			"echo-hub": {ID: "app-hub", Name: "echo-hub", RolePrefix: "echo-hub-", AdminRole: "mcp-admin-echo-hub"},
		},
		Roles: map[string]Role{
			"echoapp-readers": {ID: "r1", Name: "echoapp-readers", Kind: RoleKindApplication, Plane: "access", Owner: "echoapp", Owned: true},
			"dev":             {ID: "r2", Name: "dev", Kind: RoleKindApplication, Plane: "access"},
		},
	}
}

func TestRoleCreateRefusal(t *testing.T) {
	t.Parallel()
	full := Standing{Full: true}
	erin := Standing{Servers: map[string]bool{"app-echo": true}}
	mia := Standing{Servers: map[string]bool{"app-echo": true, "app-other": true}, AreaRefusal: "session lacks scope identity:write"}
	echo := []string{"echo"}
	naming := "a role of the server echoapp is named echoapp-<suffix>. The server's page fills the prefix for you"
	cases := []struct {
		name string
		st   Standing
		spec RoleSpec
		kind RefusalKind
		want string
	}{
		{"the straza- prefix is the product's", full, RoleSpec{Name: "Straza-team"}, RefusalInvalid,
			"role names beginning with straza- are reserved for product-defined roles; choose a name without the straza- prefix"},
		{"the minted prefix is the product's", full, RoleSpec{Name: "mcp-admin-x"}, RefusalInvalid,
			"role names beginning with mcp-admin- are reserved for the roles Straza creates with a server; choose a name without that prefix"},
		{"a kind the API does not know", full, RoleSpec{Name: "x", Kind: "console"}, RefusalInvalid,
			`kind must be business, application, approver or straza, and it is "console". Set it to one of the four`},
		{"a server admin names no server", erin, RoleSpec{Name: "echoapp-x"}, RefusalInvalid, OwnedRoleServerErr},
		{"the apps:write grant names no server", mia, RoleSpec{Name: "mia-team"}, RefusalForbidden, "session lacks scope identity:write"},
		{"a global role wears a server's prefix", full, RoleSpec{Name: "EchoApp-Loose"}, RefusalInvalid,
			"role names beginning with echoapp- belong to the server echoapp. Create it on that server's page so it becomes server-owned"},
		{"the longest of two nesting prefixes names the owner", full, RoleSpec{Name: "echo-hub-readers"}, RefusalInvalid,
			"role names beginning with echo-hub- belong to the server echo-hub. Create it on that server's page so it becomes server-owned"},
		{"a global role takes a name another role has", full, RoleSpec{Name: "dev"}, RefusalExists,
			"a role named dev already exists. Pick another name, or open dev under Roles to change it"},
		{"a global role with a free name", full, RoleSpec{Name: "finance-team", Kind: RoleKindBusiness}, 0, ""},
		{"a Straza role with a free name", full, RoleSpec{Name: "auditors", Kind: RoleKindStraza}, 0, ""},
		{"an owned role on no server", full, RoleSpec{Name: "nosuch-x", Server: "nosuch", Tools: echo}, RefusalMissing,
			"no server named nosuch is registered, so no role can be owned by it. Check the name with strazactl apps list"},
		{"another server's admin", erin, RoleSpec{Name: "otherapp-x", Server: "otherapp", Tools: echo}, RefusalForbidden,
			"this server's admin role is mcp-admin-otherapp, which you do not hold. Ask your identity manager for mcp-admin-otherapp, or a holder of straza-global-mcp-admin to make the change."},
		{"a server whose admin role is gone", erin, RoleSpec{Name: "lostapp-x", Server: "lostapp", Tools: echo}, RefusalForbidden,
			"this server's admin role is unset, which you do not hold. Ask your identity manager for unset, or a holder of straza-global-mcp-admin to make the change."},
		{"a global admin asks for another kind", full, RoleSpec{Name: "echoapp-biz", Kind: RoleKindBusiness, Server: "echoapp", Tools: echo}, RefusalInvalid, OwnedRoleKindErr},
		{"a server admin's kind is forced", erin, RoleSpec{Name: "echoapp-forced", Kind: RoleKindBusiness, Server: "echoapp", Tools: echo}, 0, ""},
		{"a name off the prefix", full, RoleSpec{Name: "readers", Server: "echoapp", Tools: echo}, RefusalInvalid, naming},
		{"a name that is the prefix alone", full, RoleSpec{Name: "echoapp-", Server: "echoapp", Tools: echo}, RefusalInvalid, naming},
		{"an owned role with no tools", full, RoleSpec{Name: "echoapp-x", Server: "echoapp"}, RefusalInvalid, OwnedRoleToolsErr},
		{"a global admin gives an owned role the every-tool matcher", full, RoleSpec{Name: "echoapp-x", Server: "echoapp", Tools: []string{"*"}}, 0, ""},
		{"a server admin gives an owned role the every-tool matcher", erin, RoleSpec{Name: "echoapp-x", Server: "echoapp", Tools: []string{"echo", "*"}}, RefusalInvalid,
			"a server-owned role lists the tools it reaches by name. Only a global admin may give it every tool, and the tools the server adds later, with the * matcher"},
		{"a global application role with no server", full, RoleSpec{Name: "deploy", Kind: RoleKindApplication}, 0, ""},
		{"an owned role takes a name another role has", full, RoleSpec{Name: "echoapp-readers", Server: "echoapp", Tools: echo}, RefusalExists,
			"a role named echoapp-readers already exists. Pick another name, or open echoapp-readers under Roles to change it"},
		{"an owned role on a server the store does not hold yet", full, RoleSpec{Name: "draftapp-x", Server: "draftapp", Tools: echo}, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := roleWorld()
			w.Apps["draftapp"] = App{Name: "draftapp", RolePrefix: "draftapp-"}
			wantRefusal(t, RoleCreateRefusal(w, tc.st, tc.spec), tc.kind, tc.want)
		})
	}
}

func TestRoleKindChangeRefusal(t *testing.T) {
	t.Parallel()
	full := Standing{Full: true}
	erin := Standing{Servers: map[string]bool{"app-echo": true}}
	owned := Role{Name: "echoapp-readers", Kind: RoleKindApplication, Owner: "echoapp", Owned: true}
	team := Role{Name: "dev-team", Kind: RoleKindBusiness, Plane: "access"}
	auditors := Role{Name: "auditors", Kind: RoleKindBusiness, Plane: PlaneControl}
	cases := []struct {
		name string
		ro   Role
		st   Standing
		kind string
		want RefusalKind
		msg  string
	}{
		{"a server admin sends a kind for an owned role", owned, erin, RoleKindApplication, RefusalInvalid, OwnedRoleKindErr},
		{"a global admin moves an owned role's kind", owned, full, RoleKindBusiness, RefusalInvalid, OwnedRoleKindErr},
		{"a global admin restates an owned role's kind", owned, full, RoleKindApplication, 0, ""},
		{"an empty kind is not a keep", team, full, "", RefusalInvalid, `kind must be business, application, approver or straza, and it is "". Set it to one of the four`},
		{"a kind the API does not know", team, full, "console", RefusalInvalid, `kind must be business, application, approver or straza, and it is "console". Set it to one of the four`},
		{"a business role stays business", team, full, RoleKindApplication, RefusalInvalid,
			"dev-team is a business role, and a role's kind is fixed at create. Create a new role of the kind you need, move its holders there in the identity manager, then delete this one"},
		{"a Straza role stays a Straza role", auditors, full, RoleKindBusiness, RefusalInvalid,
			"auditors is a Straza role, and a role's kind is fixed at create. Create a new role of the kind you need, move its holders there in the identity manager, then delete this one"},
		{"restating a Straza role's kind", auditors, full, RoleKindStraza, 0, ""},
		{"restating a business role's kind", team, full, RoleKindBusiness, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wantRefusal(t, RoleKindChangeRefusal(tc.ro, tc.st, tc.kind), tc.want, tc.msg)
		})
	}
}

func TestImplicationRefusal(t *testing.T) {
	t.Parallel()
	w := World{
		Roles: map[string]Role{
			"engineering":     {Name: "engineering", Kind: RoleKindBusiness},
			"platform":        {Name: "platform", Kind: RoleKindBusiness},
			"dev":             {Name: "dev", Kind: RoleKindApplication},
			"sec-approvers":   {Name: "sec-approvers", Kind: RoleKindApprover},
			"echoapp-readers": {Name: "echoapp-readers", Kind: RoleKindApplication, Owner: "echoapp", Owned: true},
			"orphan-readers":  {Name: "orphan-readers", Kind: RoleKindApplication, Owned: true},
		},
		Implies: map[string][]string{"engineering": {"platform"}, "platform": {"dev"}},
	}
	approver := "sec-approvers is an approver role, which stands alone: no role composes it and it composes no role, because who may decide is its direct member list, certified as it stands. Drop the implication, and assign sec-approvers directly to each person who decides instead"
	cycle := func(role, implies string) string {
		return fmt.Sprintf("%s would imply %s, and %s implies %s, so the two would compose each other in a circle. Drop this implication, or the path that leads back from %s to %s", role, implies, implies, role, implies, role)
	}
	itself := func(role string) string {
		return role + " would imply itself, and a role never composes itself. Drop the implication"
	}
	cases := []struct {
		name, role, implies string
		kind                RefusalKind
		want                string
	}{
		{"a business role composes an application role", "engineering", "dev", 0, ""},
		{"a business role composes an owned role", "engineering", "echoapp-readers", 0, ""},
		{"an owned role composes nothing", "echoapp-readers", "dev", RefusalInvalid, OwnedRoleImpliesErr},
		{"an owned role whose server is gone composes nothing", "orphan-readers", "dev", RefusalInvalid, OwnedRoleImpliesErr},
		{"an approver role composes nothing", "sec-approvers", "dev", RefusalInvalid, approver},
		{"no role composes an approver role", "engineering", "sec-approvers", RefusalInvalid, approver},
		{"a role composing itself", "dev", "dev", RefusalConflict, itself("dev")},
		{"an edge that closes a cycle two edges long", "dev", "engineering", RefusalConflict, cycle("dev", "engineering")},
		{"an edge back over one edge", "platform", "engineering", RefusalConflict, cycle("platform", "engineering")},
		{"two names no role has", "ghost-a", "ghost-b", 0, ""},
		{"the same name no role has", "ghost", "ghost", RefusalConflict, itself("ghost")},
		{"an empty end passes the role rules", "", "dev", 0, ""},
		{"an owned role implies an empty end", "echoapp-readers", "", RefusalInvalid, OwnedRoleImpliesErr},
		{"an empty end implies an approver role", "", "sec-approvers", RefusalInvalid, approver},
		{"an empty end implies itself", "", "", RefusalConflict, itself("the role")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wantRefusal(t, ImplicationRefusal(w, tc.role, tc.implies), tc.kind, tc.want)
		})
	}
}

// TestImplicationRefusalKeepsTheGraphAcyclic builds random role graphs edge
// by edge, adding only the edges ImplicationRefusal lets through, and checks
// that every graph still sorts topologically, so the cycle rule never admits
// a cycle.
func TestImplicationRefusalKeepsTheGraphAcyclic(t *testing.T) {
	t.Parallel()
	for seed := int64(0); seed < 20; seed++ {
		rng := rand.New(rand.NewSource(seed))
		n := 4 + rng.Intn(8)
		w := World{Implies: map[string][]string{}}
		for i := 0; i < n*3; i++ {
			from := fmt.Sprintf("r%d", rng.Intn(n))
			to := fmt.Sprintf("r%d", rng.Intn(n))
			if ImplicationRefusal(w, from, to) != nil {
				continue
			}
			w.Implies[from] = append(w.Implies[from], to)
		}
		if !acyclic(w.Implies, n) {
			t.Errorf("seed %d: the cycle rule admitted a cycle: %v", seed, w.Implies)
		}
	}
}

// acyclic reports whether the edges over the roles r0 to r(n-1) sort
// topologically, which holds exactly when they close no cycle.
func acyclic(implies map[string][]string, n int) bool {
	indeg := map[string]int{}
	for i := 0; i < n; i++ {
		indeg[fmt.Sprintf("r%d", i)] = 0
	}
	for _, tos := range implies {
		for _, to := range tos {
			indeg[to]++
		}
	}
	var queue []string
	for role, d := range indeg {
		if d == 0 {
			queue = append(queue, role)
		}
	}
	seen := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		seen++
		for _, next := range implies[cur] {
			indeg[next]--
			if indeg[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	return seen == n
}

func TestRoleWords(t *testing.T) {
	t.Parallel()
	cases := []struct{ got, want string }{
		{HoldersPhrase(1), "1 holder"},
		{HoldersPhrase(3), "3 holders"},
		{ServerAdminRefusal("mcp-admin-echoapp"), "this server's admin role is mcp-admin-echoapp, which you do not hold. Ask your identity manager for mcp-admin-echoapp, or a holder of straza-global-mcp-admin to make the change."},
		{ServerAdminRefusal(""), "this server's admin role is unset, which you do not hold. Ask your identity manager for unset, or a holder of straza-global-mcp-admin to make the change."},
		{UnknownRoleMessage("ops"), `role "ops" does not exist. Pick one from strazactl roles list, or create one on its MCP server with strazactl roles create <server>-<word> --app <server> --tools <tool,...>.`},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
}
