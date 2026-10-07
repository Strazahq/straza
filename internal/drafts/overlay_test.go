package drafts

import (
	"reflect"
	"testing"
)

// overlayWorld is live state with two servers, github with an admin role,
// a role it owns, a global role reaching it, a business role composing
// both and the admin role, and one live set.
func overlayWorld() World {
	return World{
		Apps: map[string]App{
			"github": {ID: "app-gh", Name: "github", Status: "running", Detail: "ok", Paused: true, File: "/apps/github.yaml",
				AdminRole: "mcp-admin-github", RolePrefix: "github-", Runtime: "remote", URL: "https://api.github.com/mcp",
				Offered: []string{"get_me", "delete_repo"}, ReadOnly: []string{"get_me"}, SharedSecret: true, UserCredentials: 3},
			"jira": {ID: "app-jira", Name: "jira", AdminRole: "mcp-admin-jira", RolePrefix: "jira-", Runtime: "remote", URL: "https://jira.example.com/mcp"},
		},
		Roles: map[string]Role{
			"github-readers":   {ID: "r1", Name: "github-readers", Kind: RoleKindApplication, Plane: PlaneAccess, Owner: "github", Owned: true},
			"dev":              {ID: "r2", Name: "dev", Kind: RoleKindApplication, Plane: PlaneAccess, Description: "Developers", Packs: []string{"go-style"}},
			"engineering":      {ID: "r3", Name: "engineering", Kind: RoleKindBusiness, Plane: PlaneAccess},
			"mcp-admin-github": {ID: "r4", Name: "mcp-admin-github", Kind: RoleKindBusiness, Plane: PlaneControl},
			"ops":              {ID: "r5", Name: "ops", Kind: RoleKindBusiness, Plane: PlaneAccess},
		},
		Implies: map[string][]string{"engineering": {"dev", "github-readers"}, "ops": {"mcp-admin-github"}},
		Access: map[string]Access{
			"github-readers": {ID: "b1", Server: "github", Tools: []string{"get_me"}},
			"dev":            {ID: "b2", Server: "github", Tools: []string{"*"}},
		},
		Policies:     map[string]Policy{"dev-access": {Name: "dev-access", Text: "old"}},
		Holders:      map[string][]Holder{"dev": {{Username: "alice"}}, "github-readers": {{Username: "bob"}}},
		HolderCounts: map[string]int{"dev": 1, "github-readers": 2},
		SnapshotID:   "snap-1",
		Generation:   7,
	}
}

func TestOverlayApp(t *testing.T) {
	t.Parallel()
	w := overlayWorld()
	facts := func(url string) App {
		return App{Runtime: "remote", URL: url, Manifest: "{}", Credential: CredentialNone, Offered: []string{"contacted"}}
	}
	cases := []struct {
		name  string
		facts App
		apps  string
		want  App
	}{
		{"a changed manifest on the same address keeps the live tools", facts("https://api.github.com/mcp"), "github", App{
			ID: "app-gh", Name: "github", Status: "running", Detail: "ok", Paused: true, File: "/apps/github.yaml", AdminRole: "mcp-admin-github",
			RolePrefix: "github-", Runtime: "remote", URL: "https://api.github.com/mcp", Manifest: "{}", Credential: CredentialNone,
			Offered: []string{"get_me", "delete_repo"}, ReadOnly: []string{"get_me"}, SharedSecret: true, UserCredentials: 3}},
		{"a moved address takes the tools a Contact read", facts("https://copilot.github.com/mcp"), "github", App{
			ID: "app-gh", Name: "github", Status: "running", Detail: "ok", Paused: true, File: "/apps/github.yaml", AdminRole: "mcp-admin-github",
			RolePrefix: "github-", Runtime: "remote", URL: "https://copilot.github.com/mcp", Manifest: "{}", Credential: CredentialNone,
			Offered: []string{"contacted"}, SharedSecret: true, UserCredentials: 3}},
		{"a new server is pending with its own admin role", App{Runtime: "remote", URL: "https://new.example.com", AdminRole: "mcp-admin-new", RolePrefix: "new-"}, "new",
			App{Name: "new", Status: statusPending, Runtime: "remote", URL: "https://new.example.com", AdminRole: "mcp-admin-new", RolePrefix: "new-"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			after := w.Overlay(Draft{Items: []Item{{Kind: KindApp, Name: tc.apps, Op: OpPut}}}, map[string]App{tc.apps: tc.facts})
			if got := after.Apps[tc.apps]; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Overlay App\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestOverlayRemovesAServerWithWhatItOwns(t *testing.T) {
	t.Parallel()
	w := overlayWorld()
	after := w.Overlay(Draft{Items: []Item{{Kind: KindApp, Name: "github", Op: OpRemove}}}, nil)
	if _, ok := after.Apps["github"]; ok {
		t.Error("the removed server stays")
	}
	for _, role := range []string{"github-readers", "mcp-admin-github"} {
		if _, ok := after.Roles[role]; ok {
			t.Errorf("%s stays after its server went", role)
		}
	}
	if _, ok := after.Access["dev"]; ok {
		t.Error("dev keeps its access row on the removed server")
	}
	if _, ok := after.Holders["github-readers"]; ok {
		t.Error("the owned role keeps its holders")
	}
	want := map[string][]string{"engineering": {"dev"}, "ops": {}}
	if !reflect.DeepEqual(after.Implies, want) {
		t.Errorf("Implies = %v, want %v", after.Implies, want)
	}
	if !reflect.DeepEqual(w, overlayWorld()) {
		t.Error("Overlay changed the live World")
	}
}

func TestOverlayRole(t *testing.T) {
	t.Parallel()
	w := overlayWorld()
	cases := []struct {
		name    string
		item    Item
		role    Role
		access  *Access
		implies []string
	}{
		{"a changed role keeps its id, its packs and its row's id on the same server",
			intakeRole("dev", "    kind: application\n    description: Devs\n    bindings:\n        - app: github\n          tools: [search, get_me]\n"),
			Role{ID: "r2", Name: "dev", Kind: RoleKindApplication, Plane: PlaneAccess, Description: "Devs", Packs: []string{"go-style"}},
			&Access{ID: "b2", Server: "github", Tools: []string{"search", "get_me"}}, nil},
		{"a row moved to another server is a new row",
			intakeRole("dev", "    kind: application\n    bindings:\n        - app: jira\n          tools: [search]\n"),
			Role{ID: "r2", Name: "dev", Kind: RoleKindApplication, Plane: PlaneAccess, Packs: []string{"go-style"}},
			&Access{Server: "jira", Tools: []string{"search"}}, nil},
		{"a document with no binding leaves no row, and its implications sorted",
			intakeRole("engineering", "    kind: business\n    implies: [github-readers, dev]\n"),
			Role{ID: "r3", Name: "engineering", Kind: RoleKindBusiness, Plane: PlaneAccess}, nil, []string{"dev", "github-readers"}},
		{"a new Straza role is a business role on the control plane",
			intakeRole("auditors", "    kind: straza\n"),
			Role{Name: "auditors", Kind: RoleKindBusiness, Plane: PlaneControl}, nil, nil},
		{"a new owned role names its owner",
			intakeRole("github-writers", "    kind: application\n    server: github\n    bindings:\n        - app: github\n          tools: [create_issue]\n"),
			Role{Name: "github-writers", Kind: RoleKindApplication, Plane: PlaneAccess, Owner: "github", Owned: true},
			&Access{Server: "github", Tools: []string{"create_issue"}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			after := w.Overlay(Draft{Items: []Item{tc.item}}, nil)
			name := tc.item.Name
			if got := after.Roles[name]; !reflect.DeepEqual(got, tc.role) {
				t.Errorf("Role\n got %+v\nwant %+v", got, tc.role)
			}
			got, ok := after.Access[name]
			if (tc.access == nil) == ok || ok && !reflect.DeepEqual(got, *tc.access) {
				t.Errorf("Access = %+v (%v), want %+v", got, ok, tc.access)
			}
			if got := after.Implies[name]; !reflect.DeepEqual(got, tc.implies) {
				t.Errorf("Implies = %v, want %v", got, tc.implies)
			}
			if after.HolderCounts[name] != w.HolderCounts[name] {
				t.Errorf("HolderCounts moved to %d", after.HolderCounts[name])
			}
		})
	}
}

func TestOverlayRemovesARole(t *testing.T) {
	t.Parallel()
	after := overlayWorld().Overlay(Draft{Items: []Item{{Kind: KindRole, Name: "dev", Op: OpRemove}}}, nil)
	_, role := after.Roles["dev"]
	_, row := after.Access["dev"]
	_, held := after.Holders["dev"]
	if role || row || held || !reflect.DeepEqual(after.Implies["engineering"], []string{"github-readers"}) {
		t.Errorf("after removing dev: role %v, row %v, holders %v, engineering implies %v", role, row, held, after.Implies["engineering"])
	}
}

func TestOverlayPolicySet(t *testing.T) {
	t.Parallel()
	w := overlayWorld()
	after := w.Overlay(Draft{Items: []Item{
		{Kind: KindPolicySet, Name: "new-access", Op: OpPut, Doc: "new text"},
		{Kind: KindPolicySet, Name: "dev-access", Op: OpOff, Doc: "old"},
	}}, nil)
	want := map[string]Policy{"new-access": {Name: "new-access", Text: "new text"}}
	if !reflect.DeepEqual(after.Policies, want) || after.SnapshotID != "snap-1" || after.Generation != 7 {
		t.Errorf("Policies = %v, snapshot %s, generation %d", after.Policies, after.SnapshotID, after.Generation)
	}
	if removed := w.Overlay(Draft{Items: []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpRemove}}}, nil); len(removed.Policies) != 0 {
		t.Errorf("a removed set stays: %v", removed.Policies)
	}
}

// TestOverlayAppliesRemovalsLast pins that a removal takes what it owns in
// the state the draft leaves, whatever the item order.
func TestOverlayAppliesRemovalsLast(t *testing.T) {
	t.Parallel()
	owned := intakeRole("github-writers", "    kind: application\n    server: github\n    bindings:\n        - app: github\n          tools: [x]\n")
	for _, items := range [][]Item{
		{{Kind: KindApp, Name: "github", Op: OpRemove}, owned},
		{owned, {Kind: KindApp, Name: "github", Op: OpRemove}},
	} {
		if _, ok := overlayWorld().Overlay(Draft{Items: items}, nil).Roles["github-writers"]; ok {
			t.Errorf("items %v: a role owned by the removed server stays", items)
		}
	}
}

func TestImplied(t *testing.T) {
	t.Parallel()
	w := overlayWorld()
	cases := []struct {
		name  string
		items []Item
		want  []Item
	}{
		{"removing a server takes its owned role, strips rows and edges, and names no admin role", []Item{{Kind: KindApp, Name: "github", Op: OpRemove}}, []Item{
			{Kind: KindRole, Name: "dev", Op: OpPut, Doc: "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: dev\nspec:\n    kind: application\n    description: Developers\n    packs:\n        - go-style\n"},
			{Kind: KindRole, Name: "engineering", Op: OpPut, Doc: "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: engineering\nspec:\n    kind: business\n    implies:\n        - dev\n"},
			{Kind: KindRole, Name: "github-readers", Op: OpRemove},
			{Kind: KindRole, Name: "ops", Op: OpPut, Doc: "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: ops\nspec:\n    kind: business\n"},
		}},
		{"removing a role strips the edges to it", []Item{{Kind: KindRole, Name: "dev", Op: OpRemove}}, []Item{
			{Kind: KindRole, Name: "engineering", Op: OpPut, Doc: "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: engineering\nspec:\n    kind: business\n    implies:\n        - github-readers\n"},
		}},
		{"a role the draft names is not implied", []Item{{Kind: KindRole, Name: "dev", Op: OpRemove}, intakeRole("engineering", "    kind: business\n")}, nil},
		{"removing what does not exist implies nothing", []Item{{Kind: KindApp, Name: "nosuch", Op: OpRemove}, {Kind: KindRole, Name: "nosuch", Op: OpRemove}}, nil},
		{"a put implies nothing", []Item{intakeRole("dev", "    kind: application\n")}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Implied(w, Draft{Items: tc.items}); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Implied\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}
