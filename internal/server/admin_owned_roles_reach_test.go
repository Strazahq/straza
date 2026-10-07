package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// The refusals of a first access row for an application role no server
// owns, pinned whole.
const (
	deployOnGithub = "deploy belongs to no MCP server, so it cannot be given access to github. " +
		"A role that reaches a server belongs to that server, so the identity manager and the console can name the server it reaches. " +
		"Create a role of github with strazactl roles create github-<word> --app github --tools <tool,...>, and compose it and deploy into a business role."
	legacyOnJira = "legacy belongs to no MCP server, so it cannot be given access to jira. " +
		"A role that reaches a server belongs to that server, so the identity manager and the console can name the server it reaches. " +
		"Create a role of jira with strazactl roles create jira-<word> --app jira --tools <tool,...>, and compose it and legacy into a business role."
	everyToolRefusal = "a server-owned role lists the tools it reaches by name. Only a global admin may give it every tool, and the tools the server adds later, with the * matcher"
)

// rowTools answers the tool matchers of role's one access row in the
// store, and whether it has one.
func rowTools(t *testing.T, app *App, role string) (string, bool) {
	t.Helper()
	rows, err := app.store.ToolBindings().ListByRole(context.Background(), mustRole(t, app, role).ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		return "", false
	}
	return rows[0].ToolMatcher, true
}

// TestApplicationRoleOfNoServerReachesNoServer pins that an application role
// created without a server is stored and reaches no server: its first
// access row is refused on the bindings route and in a draft's Role
// document, in the same words, and nothing is written.
func TestApplicationRoleOfNoServerReachesNoServer(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	var created rolePayload
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/roles", f.root, map[string]any{"name": "deploy", "kind": "application"}, &created); code != http.StatusCreated ||
		created.Kind != store.RoleKindApplication || created.Server != "" {
		t.Fatalf("create deploy = %d %+v, want 201, an application role of no server", code, created)
	}
	var answer struct {
		Error string `json:"error"`
	}
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/apps/github/bindings", f.root, map[string]any{"role": "deploy", "tools": []string{"search"}}, &answer); code != http.StatusConflict ||
		answer.Error != deployOnGithub {
		t.Errorf("bind deploy = %d %q, want 409 %q", code, answer.Error, deployOnGithub)
	}
	id := f.create(t, f.root, roleText(t, "deploy", "", "", "github", "search")).Draft.ID
	code, a := f.publishAll(t, f.root, id)
	if want := "Draft " + id + " cannot be published: " + deployOnGithub; code != http.StatusConflict || a.Error != want ||
		len(a.Verdict.Refused) != 1 || a.Verdict.Refused[0].Code != "access.owned" {
		t.Errorf("publish deploy's row = %d %q %+v, want 409 %q with access.owned", code, a.Error, a.Verdict.Refused, want)
	}
	if _, ok := rowTools(t, f.app, "deploy"); ok {
		t.Error("deploy holds an access row after both refusals")
	}
	if n, _ := countRecords(t, f.app, "apps.binding.create", "deploy"); n != 0 {
		t.Errorf("apps.binding.create records for deploy = %d, want none", n)
	}
}

// TestEveryToolOnAServersRole pins the every-tool matcher on a role a
// server owns: a global admin gives it at create and on edit, a server
// admin is refused it at create, on the bindings route and in a draft, a
// server admin narrows an unheld role that has it to named tools, and a
// role with holders keeps its tools for the server admin.
func TestEveryToolOnAServersRole(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	var all rolePayload
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/roles", f.root, map[string]any{"name": "github-all", "server": "github", "tools": []string{"*"}}, &all); code != http.StatusCreated ||
		all.Server != "github" || !slices.Equal(all.Tools, []string{"*"}) {
		t.Fatalf("root creates github-all = %d %+v, want 201 on github with *", code, all)
	}
	if ro := mustRole(t, f.app, "github-all"); ro.OwnerAppID != f.github.ID || ro.Kind != store.RoleKindApplication {
		t.Errorf("github-all is stored with owner %q and kind %q, want github's id and application", ro.OwnerAppID, ro.Kind)
	}
	if tools, _ := rowTools(t, f.app, "github-all"); tools != `["*"]` {
		t.Errorf("github-all's row = %s, want every tool", tools)
	}
	for _, name := range []string{"github-some", "github-held"} {
		if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/roles", f.root, map[string]any{"name": name, "server": "github", "tools": []string{"search"}}, nil); code != http.StatusCreated {
			t.Fatalf("root creates %s = %d", name, code)
		}
	}
	mkHuman(t, f.app, "hal", "github-held")
	var answer struct {
		Error string `json:"error"`
	}
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/roles", f.erin, map[string]any{"name": "github-mine", "server": "github", "tools": []string{"*"}}, &answer); code != http.StatusBadRequest ||
		answer.Error != everyToolRefusal {
		t.Errorf("erin creates github-mine with * = %d %q, want 400 %q", code, answer.Error, everyToolRefusal)
	}
	draft := func(bearer string, docs ...string) (int, pubAnswer, string) {
		t.Helper()
		id := f.create(t, bearer, docs...).Draft.ID
		code, a := f.publishAll(t, bearer, id)
		return code, a, id
	}
	cases := []struct {
		name, bearer, role string
		tools              []string
		code               int
		says               string
		stored             string
	}{
		{"a server admin writes every tool on an unheld role", f.erin, "github-some", []string{"*"}, http.StatusForbidden, everyToolRefusal + ".", `["search"]`},
		{"a server admin narrows an unheld role that has every tool", f.erin, "github-all", []string{"search"}, http.StatusOK, "", `["search"]`},
		{"a server admin narrows a held role", f.erin, "github-held", []string{"get_me"}, http.StatusForbidden,
			"the role github-held has 1 holder. Its tools change only by the global admin or by a new role.", `["search"]`},
		{"a global admin gives every tool on edit", f.root, "github-all", []string{"*"}, http.StatusOK, "", `["*"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, a, id := draft(tc.bearer, roleText(t, tc.role, "github", "", "github", tc.tools...))
			want := ""
			if tc.says != "" {
				want = "You cannot publish draft " + id + ": " + tc.says
			}
			if code != tc.code || a.Error != want {
				t.Errorf("publish = %d %q, want %d %q", code, a.Error, tc.code, want)
			}
			if tools, _ := rowTools(t, f.app, tc.role); tools != tc.stored {
				t.Errorf("%s's row = %s, want %s", tc.role, tools, tc.stored)
			}
		})
	}
	some, _ := f.app.store.ToolBindings().ListByRole(context.Background(), mustRole(t, f.app, "github-some").ID)
	if code := adminReq(t, http.MethodDelete, f.base+"/v1/admin/bindings/"+some[0].ID, f.erin, nil, nil); code != http.StatusOK {
		t.Fatalf("erin removes github-some's row = %d", code)
	}
	answer.Error = ""
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/apps/github/bindings", f.erin, map[string]any{"role": "github-some", "tools": []string{"*"}}, &answer); code != http.StatusBadRequest ||
		answer.Error != everyToolRefusal {
		t.Errorf("erin binds github-some with * = %d %q, want 400 %q", code, answer.Error, everyToolRefusal)
	}
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/apps/github/bindings", f.root, map[string]any{"role": "github-some", "tools": []string{"*"}}, nil); code != http.StatusCreated {
		t.Errorf("root binds github-some with * = %d, want 201", code)
	}
}

// TestLegacyGlobalRoleKeepsItsRow pins that an application role of no
// server whose row the store holds from before keeps it: the bindings
// route still routes a second row into editing it, a draft changes its
// tools on that server, and a draft that moves it to another server is
// refused.
func TestLegacyGlobalRoleKeepsItsRow(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	legacy := mkRole(t, f, "legacy", store.RoleKindApplication)
	row, err := f.app.store.ToolBindings().Create(context.Background(), store.ToolBinding{RoleID: legacy.ID, AppID: f.github.ID, ToolMatcher: `["search"]`})
	if err != nil {
		t.Fatal(err)
	}
	var dup struct {
		Error     string `json:"error"`
		BindingID string `json:"binding_id"`
	}
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/apps/github/bindings", f.root, map[string]any{"role": "legacy", "tools": []string{"get_me"}}, &dup); code != http.StatusConflict ||
		dup.BindingID != row.ID {
		t.Errorf("a second row = %d %+v, want 409 naming row %s", code, dup, row.ID)
	}
	id := f.create(t, f.root, roleText(t, "legacy", "", "", "github", "get_me", "search")).Draft.ID
	if code, a := f.publishAll(t, f.root, id); code != http.StatusOK {
		t.Errorf("edit legacy's tools = %d %q, want 200", code, a.Error)
	}
	if tools, _ := rowTools(t, f.app, "legacy"); tools != `["get_me","search"]` {
		t.Errorf("legacy's row = %s, want get_me and search", tools)
	}
	id = f.create(t, f.root, roleText(t, "legacy", "", "", "jira", "search")).Draft.ID
	if code, a := f.publishAll(t, f.root, id); code != http.StatusConflict || a.Error != "Draft "+id+" cannot be published: "+legacyOnJira {
		t.Errorf("move legacy to jira = %d %q, want 409 %q", code, a.Error, legacyOnJira)
	}
}

// TestServerRemovalTakesAnEveryToolRole pins that a role that has the
// every-tool matcher goes with its server like any role the server owns,
// with one roles.unassign per direct holder and its roles.delete record.
func TestServerRemovalTakesAnEveryToolRole(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	var all rolePayload
	if code := adminReq(t, http.MethodPost, f.base+"/v1/admin/roles", f.root, map[string]any{"name": "jira-all", "server": "jira", "tools": []string{"*"}}, &all); code != http.StatusCreated {
		t.Fatalf("root creates jira-all = %d", code)
	}
	hal := mkHuman(t, f.app, "hal", "jira-all")
	if code := adminReq(t, http.MethodDelete, f.base+"/v1/admin/apps/jira", f.root, nil, nil); code != http.StatusOK {
		t.Fatalf("remove jira = %d", code)
	}
	if _, err := f.app.store.Roles().GetByName(context.Background(), "jira-all"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("jira-all survived its server: %v", err)
	}
	n, ev := countRecords(t, f.app, "roles.unassign", "jira-all")
	if n != 1 || ev["user"] != hal.ID || ev["reason"] != "server removed" {
		t.Errorf("roles.unassign records for jira-all = %d, last %v; want one for hal, server removed", n, ev)
	}
	n, ev = countRecords(t, f.app, "roles.delete", "jira-all")
	if n != 1 || ev["target"] != all.ID || ev["server"] != "jira" || ev["reason"] != "removed with the server jira" {
		t.Errorf("roles.delete records for jira-all = %d, last %v; want one naming jira and the reason", n, ev)
	}
}
