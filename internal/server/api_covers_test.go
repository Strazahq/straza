package server

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/objects"
	"github.com/strazahq/straza/internal/store"
)

// coversManifest is ghapp, a remote server that pins repos: pr_create names
// its repo, get_me touches none, and repo_create reaches every repo.
const coversManifest = `apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: ghapp}
server: {name: straza.test/ghapp, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: "http://127.0.0.1:1/mcp"}
  objects:
    type: repo
    list: {tool: repo_list}
    tools:
      pr_create: {argument: repo}
      get_me: none
      repo_create: all
`

// seedCovers stores ghapp with its object list holding repos, each a value
// and an id, as a listing writes it, and answers the server's id. The
// listing loop is off in the tests that call it, so nothing lists over the
// seeded rows.
func seedCovers(t *testing.T, app *App, repos [][2]string) string {
	t.Helper()
	ctx := context.Background()
	mf, err := manager.Parse([]byte(coversManifest))
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.store.Apps().GetByName(ctx, "ghapp")
	if err != nil {
		row, err = app.store.Apps().Create(ctx, store.App{Name: "ghapp", RuntimeKind: "remote", Manifest: objectsTestJSON(t, mf)})
		if err != nil {
			t.Fatal(err)
		}
	}
	seedList(t, app, row.ID, "repo", objects.DeclHash(*mf.Straza.Objects), repos)
	return row.ID
}

// seedList stores the object list of the server appID as a listing of the
// type typ under the declaration hash writes it, holding repos.
func seedList(t *testing.T, app *App, appID, typ, hash string, repos [][2]string) {
	t.Helper()
	ctx, now := context.Background(), time.Now()
	if err := app.store.ObjectLists().Ensure(ctx, appID, typ, hash, now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := app.store.ObjectLists().MarkDue(ctx, appID, now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := app.store.ObjectLists().Claim(ctx, appID, "test", now, now.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("claim the object list: %v %v", ok, err)
	}
	objs := make([]store.ListedObject, len(repos))
	for i, r := range repos {
		objs[i] = store.ListedObject{Value: r[0], ID: r[1], Attributes: "{}"}
	}
	if _, err := app.store.ObjectLists().Replace(ctx, appID, "test", hash, "list-"+now.String(), objs,
		store.Listing{Outcome: store.ObjectsOK, At: now, Due: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
}

// exportOf answers the export of the role with the id.
func exportOf(t *testing.T, base, tok, id string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/v1/admin/roles/"+id+"/export", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export = %d %s", resp.StatusCode, body)
	}
	return string(body)
}

// TestCoversThroughTheAdminRoutes pins the covers line on the direct
// routes: the owned role create stores a list of repos by id, refuses a
// repo the list does not hold, a role with no covers line and a covers key
// it does not know, the export writes the current value of each id and the
// stored value of an id the list no longer holds, byte for byte as
// spec/objects/fixtures/role-objects.yaml, and the binding create and list
// carry the line in its object form, the list with the values of now.
func TestCoversThroughTheAdminRoutes(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) { c.Apps.Objects.Enabled = false })
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()
	appID := seedCovers(t, app, [][2]string{{"Strazahq/pay-api", "101"}, {"Strazahq/pay-web", "102"}, {"Strazahq/site", "103"}})

	role := func(name string, covers any) map[string]any {
		body := map[string]any{"name": name, "description": "Write tools of ghapp on the payments repos", "server": "ghapp", "tools": []string{"pr_create", "get_me"}}
		if covers != nil {
			body["covers"] = covers
		}
		return body
	}
	refusals := []struct {
		name   string
		covers any
		want   string
	}{
		{"a repo the list does not hold", map[string]any{"objects": []map[string]string{{"value": "Strazahq/pay-old"}}},
			"Strazahq/pay-old is not in ghapp's repo list. Compare the name with ghapp's Repos tab or strazactl apps objects ghapp. If the repo is new, refresh the list with strazactl apps objects refresh ghapp, then try again."},
		{"no covers line", nil, "ghapp-write-x binds tools that work on repos. Say which repos it covers: every repo, or these repos by name."},
		{"a key covers does not take", map[string]any{"where": map[string]any{"team": []string{"payments"}}},
			`covers carries "where", and it takes only every or objects. Send {"every": true} or {"objects": [{"value": "<name>"}]}.`},
	}
	var global map[string]any
	if code := adminReq(t, "POST", base+"/v1/admin/roles", tok, map[string]any{"name": "pay-team", "kind": "business", "covers": map[string]any{"every": true}}, &global); code != http.StatusBadRequest ||
		global["error"] != "covers needs the server the role is owned by, because a covers line belongs to one server's access row. Add server, or drop covers." {
		t.Errorf("a role with covers and no server = %d %v", code, global)
	}
	for _, tc := range refusals {
		var out map[string]any
		code := adminReq(t, "POST", base+"/v1/admin/roles", tok, role("ghapp-write-x", tc.covers), &out)
		if msg, _ := out["error"].(string); code < 400 || !strings.Contains(msg, tc.want) {
			t.Errorf("%s: create = %d %q, want a refusal containing %q", tc.name, code, msg, tc.want)
		}
	}

	var created rolePayload
	pay := map[string]any{"objects": []map[string]string{{"value": "Strazahq/pay-web"}, {"value": "Strazahq/pay-api", "id": "999"}}}
	if code := adminReq(t, "POST", base+"/v1/admin/roles", tok, role("ghapp-write-payments", pay), &created); code != http.StatusCreated {
		t.Fatalf("create = %d %+v", code, created)
	}
	rows, err := app.store.ToolBindings().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range rows {
		if b.RoleID == created.ID && (b.Scope == nil || b.Scope.Key() != "objects:101\x00102") {
			t.Errorf("stored covers line = %+v, want the ids 101 and 102, never the id the body sent", b.Scope)
		}
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "spec", "objects", "fixtures", "role-objects.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := exportOf(t, base, tok, created.ID); got != string(want) {
		t.Errorf("export drifted from spec/objects/fixtures/role-objects.yaml:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	seedCovers(t, app, [][2]string{{"Strazahq/pay-api-v2", "101"}, {"Strazahq/site", "103"}})
	if got := exportOf(t, base, tok, created.ID); !strings.Contains(got, "- Strazahq/pay-api-v2\n") || !strings.Contains(got, "- Strazahq/pay-web\n") {
		t.Errorf("export after a rename and a removal:\n%s\nwant the new value of 101 and the stored value of 102", got)
	}

	var reader rolePayload
	if code := adminReq(t, "POST", base+"/v1/admin/roles", tok, role("ghapp-read", map[string]any{"every": true}), &reader); code != http.StatusCreated {
		t.Fatalf("create ghapp-read = %d", code)
	}
	var list []bindingPayload
	if code := adminReq(t, "GET", base+"/v1/admin/bindings", tok, nil, &list); code != http.StatusOK {
		t.Fatalf("list bindings = %d", code)
	}
	for _, b := range list {
		if b.Role == "ghapp-read" {
			if code := adminReq(t, "DELETE", base+"/v1/admin/bindings/"+b.ID, tok, nil, nil); code != http.StatusOK {
				t.Fatalf("delete ghapp-read's row = %d", code)
			}
		}
	}
	var made bindingPayload
	if code := adminReq(t, "POST", base+"/v1/admin/apps/"+appID+"/bindings", tok,
		map[string]any{"role": "ghapp-read", "tools": []string{"get_me", "pr_create"}, "covers": map[string]any{"every": true}}, &made); code != http.StatusCreated {
		t.Fatalf("binding create = %d %+v", code, made)
	}
	if made.Covers == nil || !made.Covers.Every {
		t.Errorf("binding create answered covers %+v, want every", made.Covers)
	}
	list = nil
	adminReq(t, "GET", base+"/v1/admin/bindings", tok, nil, &list)
	found := map[string]string{}
	for _, b := range list {
		found[b.Role] = b.Covers.Key()
		if b.Role == "ghapp-write-payments" && !reflect.DeepEqual(b.Covers.Objects, []drafts.ObjectRef{{Value: "Strazahq/pay-api-v2", ID: "101"}, {Value: "Strazahq/pay-web", ID: "102"}}) {
			t.Errorf("binding list objects = %+v, want the new value of 101 and the stored value of 102", b.Covers.Objects)
		}
	}
	if found["ghapp-read"] != "every" || found["ghapp-write-payments"] != "objects:101\x00102" {
		t.Errorf("binding list covers = %q", found)
	}
}

// TestPublishOfAFirstPinWithAKeptRole pins the first pin's publish: a
// draft that pins repos on github and puts back a role of github unchanged,
// with no covers line, publishes, and the role's row then covers every
// repo under its own id, as the publish gives every row of a server it
// pins first, with no binding change.
func TestPublishOfAFirstPinWithAKeptRole(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	role := typedRole("github-pay", "github", "get_me")
	if code, a := f.publishAll(t, f.root, f.create(t, f.root, role).Draft.ID); code != http.StatusOK {
		t.Fatalf("publish of github-pay = %d %q %+v", code, a.Error, a.Verdict)
	}
	rows, err := f.app.store.ToolBindings().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, b := range rows {
		before[b.RoleID] = b.ID
	}
	pinned := draftApp("github", "https://api.github.example/mcp?region=eu", "The GitHub server.") +
		"  objects:\n    type: repo\n    list: {tool: repo_list}\n    tools:\n      get_me: none\n"
	if code, a := f.publishAll(t, f.root, f.create(t, f.root, pinned, role).Draft.ID); code != http.StatusOK {
		t.Fatalf("publish of the first pin = %d %q %+v", code, a.Error, a.Verdict)
	}
	after, err := f.app.store.ToolBindings().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range after {
		if b.AppID != f.github.ID {
			continue
		}
		if b.Scope.Key() != "every" {
			t.Errorf("a row on github covers %q after the first pin, want every", b.Scope.Key())
		}
		if b.ID != before[b.RoleID] {
			t.Errorf("the first pin replaced the row of role %s, %s became %s, and a row it only gives every keeps its id", b.RoleID, before[b.RoleID], b.ID)
		}
	}
}

// TestAppsListSaysWhoMayCoverEvery pins may_cover_every on the server
// list: true for a global admin whatever admin.serverAdminsCoverEvery
// says, and the setting's value for a server's admin.
func TestAppsListSaysWhoMayCoverEvery(t *testing.T) {
	t.Parallel()
	for _, setting := range []bool{false, true} {
		f := newDraftsFixture(t, nil, func(c *config.Config) { c.Admin.ServerAdminsCoverEvery = setting })
		for _, who := range []struct {
			bearer string
			want   bool
		}{{f.root, true}, {f.erin, setting}} {
			var list []appPayload
			if code := adminReq(t, "GET", f.base+"/v1/admin/apps", who.bearer, nil, &list); code != http.StatusOK {
				t.Fatalf("list apps = %d", code)
			}
			i := slices.IndexFunc(list, func(p appPayload) bool { return p.Name == "github" })
			if i < 0 || list[i].MayCoverEvery == nil || *list[i].MayCoverEvery != who.want {
				t.Errorf("setting %v: github in %+v, want may_cover_every %v", setting, list, who.want)
			}
		}
	}
}

// plainGithub is the fixture's github at an address with no query, which
// hides nothing from its server admin.
func plainGithub() string {
	return draftApp("github", "https://api.github.example/mcp", "The GitHub server.")
}

// pinnedGithub is plainGithub with repos pinned: get_me touches none and
// pr_create names its repo.
func pinnedGithub() string {
	return plainGithub() + "  objects:\n    type: repo\n    list: {tool: repo_list}\n    tools:\n      get_me: none\n      pr_create: {argument: repo}\n"
}

// coversRoleDoc is the Role document of the github role name with tools and
// the covers line given as YAML.
func coversRoleDoc(name, covers string, tools ...string) string {
	return typedRole(name, "github", tools...) + "          covers: " + covers + "\n"
}

// pinGithub publishes github with repos pinned as the fixture's global
// admin and seeds its object list with repos, of the type typ.
func pinGithub(t *testing.T, f *draftsFixture, typ string, repos [][2]string) {
	t.Helper()
	if code, a := f.publishAll(t, f.root, f.create(t, f.root, pinnedGithub()).Draft.ID); code != http.StatusOK {
		t.Fatalf("pin github = %d %q %+v", code, a.Error, a.Verdict)
	}
	mf, err := manager.Parse([]byte(pinnedGithub()))
	if err != nil {
		t.Fatal(err)
	}
	seedList(t, f.app, f.github.ID, typ, objects.DeclHash(*mf.Straza.Objects), repos)
}

// accessRowOf answers the access row of the role name.
func accessRowOf(t *testing.T, f *draftsFixture, name string) store.ToolBinding {
	t.Helper()
	ctx := context.Background()
	role, err := f.app.store.Roles().GetByName(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.app.store.ToolBindings().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range rows {
		if b.RoleID == role.ID {
			return b
		}
	}
	t.Fatalf("%s has no access row", name)
	return store.ToolBinding{}
}

// TestCoversReadTheListByCurrentValue pins how a role's named repos meet a
// list that moved: after 101 is renamed to Strazahq/pay-legacy and a new
// Strazahq/pay-api is listed as 201, a description edit keeps the row as
// it is, a draft that names Strazahq/pay-api moves the role to 201, and a
// list of another type than the pinned one gives no ids, so a value it
// holds is not in the repo list.
func TestCoversReadTheListByCurrentValue(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	pinGithub(t, f, "repo", [][2]string{{"Strazahq/pay-api", "101"}, {"Strazahq/site", "103"}})
	role := coversRoleDoc("github-pay", "{objects: [Strazahq/pay-api]}", "pr_create")
	if code, a := f.publishAll(t, f.root, f.create(t, f.root, role).Draft.ID); code != http.StatusOK {
		t.Fatalf("publish github-pay = %d %q %+v", code, a.Error, a.Verdict)
	}
	seedList(t, f.app, f.github.ID, "repo", rowListHash(t), [][2]string{{"Strazahq/pay-legacy", "101"}, {"Strazahq/pay-api", "201"}, {"Strazahq/site", "103"}})
	before := accessRowOf(t, f, "github-pay")
	ro, err := f.app.store.Roles().GetByName(context.Background(), "github-pay")
	if err != nil {
		t.Fatal(err)
	}
	if code := adminReq(t, "PATCH", f.base+"/v1/admin/roles/"+ro.ID, f.root, map[string]any{"description": "Payments"}, nil); code != http.StatusOK {
		t.Fatalf("describe github-pay = %d", code)
	}
	if after := accessRowOf(t, f, "github-pay"); after.ID != before.ID || after.Scope.Key() != "objects:101" {
		t.Errorf("a description edit changed the row: %s %q, want %s objects:101", after.ID, after.Scope.Key(), before.ID)
	}
	if code, a := f.publishAll(t, f.root, f.create(t, f.root, role).Draft.ID); code != http.StatusOK {
		t.Fatalf("publish github-pay on the new pay-api = %d %q %+v", code, a.Error, a.Verdict)
	}
	if got := accessRowOf(t, f, "github-pay").Scope.Key(); got != "objects:201" {
		t.Errorf("github-pay covers %q after naming the new Strazahq/pay-api, want objects:201", got)
	}
	if err := f.app.store.ObjectLists().Reset(context.Background(), f.github.ID, "project", "another-block", time.Now()); err != nil {
		t.Fatal(err)
	}
	seedList(t, f.app, f.github.ID, "project", "another-block", [][2]string{{"Strazahq/site", "103"}})
	code, a := f.call(t, http.MethodPost, "/v1/admin/drafts", f.root, map[string]any{"documents": []string{coversRoleDoc("github-site", "{objects: [Strazahq/site]}", "pr_create")}})
	if code != http.StatusCreated {
		t.Fatalf("create github-site = %d %q", code, a.Error)
	}
	_, v := f.read(t, f.root, a.Draft.ID)
	if len(v.Refused) == 0 || !strings.Contains(v.Refused[0].Sentence, "Strazahq/site is not in github's repo list.") {
		t.Errorf("a list of another type gave ids: refused %+v", v.Refused)
	}
}

// rowListHash is the declaration hash of pinnedGithub's objects block.
func rowListHash(t *testing.T) string {
	t.Helper()
	mf, err := manager.Parse([]byte(pinnedGithub()))
	if err != nil {
		t.Fatal(err)
	}
	return objects.DeclHash(*mf.Straza.Objects)
}

// TestServerAdminPinsAndCovers pins the standing rules a server admin's
// draft meets on a pinned type while admin.serverAdminsCoverEvery is off:
// a role made to cover every repo in the draft that pins the type first is
// refused, a role that reached every repo before restates it, and removing
// the pinned repos, or turning a tool a role on some repos binds to reach
// none, while a role names repos is left to a global admin.
func TestServerAdminPinsAndCovers(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil, func(c *config.Config) { c.Admin.ServerAdminsCoverEvery = false })
	if code, a := f.publishAll(t, f.root, f.create(t, f.root, plainGithub()).Draft.ID); code != http.StatusOK {
		t.Fatalf("move github to a plain address = %d %q", code, a.Error)
	}
	if code, a := f.publishAll(t, f.root, f.create(t, f.root, typedRole("github-old", "github", "get_me")).Draft.ID); code != http.StatusOK {
		t.Fatalf("publish github-old = %d %q", code, a.Error)
	}
	first := f.create(t, f.erin, pinnedGithub(), coversRoleDoc("github-new", "every", "get_me")).Draft.ID
	code, a := f.publishAll(t, f.erin, first)
	if want := "On this deployment only a global admin may make github-new cover every repo, including the repos github lists later."; code < 400 || !strings.Contains(a.Error, want) {
		t.Errorf("erin's first pin with a role on every repo = %d %q, want a refusal containing %q", code, a.Error, want)
	}
	// A row on a server that pins nothing reaches every repo already, so
	// saying so in the first pin widens nothing.
	restated := f.create(t, f.erin, pinnedGithub(), coversRoleDoc("github-old", "every", "get_me")).Draft.ID
	if code, a := f.publishAll(t, f.erin, restated); code != http.StatusOK {
		t.Errorf("erin's first pin restating github-old on every repo = %d %q", code, a.Error)
	}

	pinGithub(t, f, "repo", [][2]string{{"Strazahq/pay-api", "101"}})
	if code, a := f.publishAll(t, f.root, f.create(t, f.root, coversRoleDoc("github-pay", "{objects: [Strazahq/pay-api]}", "pr_create")).Draft.ID); code != http.StatusOK {
		t.Fatalf("publish github-pay = %d %q %+v", code, a.Error, a.Verdict)
	}
	opened := f.create(t, f.erin, strings.Replace(pinnedGithub(), "pr_create: {argument: repo}", "pr_create: none", 1)).Draft.ID
	code, a = f.publishAll(t, f.erin, opened)
	if want := "Only a global admin may make pr_create on github reach no repo while a role names repos on it, because it would then call pr_create on every repo: github-pay. Ask a global admin to publish this draft."; code < 400 || !strings.Contains(a.Error, want) {
		t.Errorf("erin's reach change to none = %d %q, want a refusal containing %q", code, a.Error, want)
	}
	stricter := f.create(t, f.erin, strings.Replace(pinnedGithub(), "pr_create: {argument: repo}", "pr_create: all", 1)).Draft.ID
	if code, a := f.publishAll(t, f.erin, stricter); code != http.StatusOK {
		t.Errorf("erin's stricter reach = %d %q", code, a.Error)
	}
	unpin := f.create(t, f.erin, plainGithub()).Draft.ID
	code, a = f.publishAll(t, f.erin, unpin)
	want := "Only a global admin may remove github's pinned repos while a role names repos on it, because it would then reach every repo: github-pay. Ask a global admin to publish this draft."
	if code < 400 || !strings.Contains(a.Error, want) {
		t.Errorf("erin's unpin = %d %q, want a refusal containing %q", code, a.Error, want)
	}
	if code, a := f.publishAll(t, f.root, unpin); code != http.StatusOK {
		t.Errorf("the global admin's publish of the unpin = %d %q %+v", code, a.Error, a.Verdict)
	}
}

// TestAnUnreadableCoversLine pins a row whose stored covers line cannot be
// read, which grants nothing at the gateway: the binding list, the export,
// a server's reached_by and a user's own servers leave it out as the
// gateway does, a re-save of the role with no covers line is asked for
// one, and a re-save with a covers line replaces the row.
func TestAnUnreadableCoversLine(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil)
	ctx := context.Background()
	pinGithub(t, f, "repo", [][2]string{{"Strazahq/pay-api", "101"}})
	role := coversRoleDoc("github-pay", "{objects: [Strazahq/pay-api]}", "pr_create")
	if code, a := f.publishAll(t, f.root, f.create(t, f.root, role).Draft.ID); code != http.StatusOK {
		t.Fatalf("publish github-pay = %d %q %+v", code, a.Error, a.Verdict)
	}
	ro, err := f.app.store.Roles().GetByName(ctx, "github-pay")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: f.kim.ID, RoleID: ro.ID}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(filepath.ToSlash(f.app.cfg.Store.DSN))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`UPDATE tool_bindings SET object_scope = '{}' WHERE role_id = ?`, ro.ID); err != nil {
		t.Fatal(err)
	}

	var list []bindingPayload
	adminReq(t, "GET", f.base+"/v1/admin/bindings", f.root, nil, &list)
	if slices.ContainsFunc(list, func(b bindingPayload) bool { return b.Role == "github-pay" }) {
		t.Errorf("the binding list shows the unreadable row: %+v", list)
	}
	if got := exportOf(t, f.base, f.root, ro.ID); strings.Contains(got, "bindings:") {
		t.Errorf("the export writes the unreadable row:\n%s", got)
	}
	var apps []appPayload
	adminReq(t, "GET", f.base+"/v1/admin/apps", f.root, nil, &apps)
	for _, p := range apps {
		if p.Name == "github" && slices.Contains(p.ReachedBy, "github-pay") {
			t.Errorf("github is reached by %q, want the unreadable row left out", p.ReachedBy)
		}
	}
	if reached, err := f.app.reachedApps(ctx, f.kim.ID); err != nil || reached[f.github.ID] {
		t.Errorf("kim's own servers = %v %v, want github left out", reached, err)
	}

	code, a := f.call(t, http.MethodPost, "/v1/admin/drafts", f.root, map[string]any{"documents": []string{typedRole("github-pay", "github", "pr_create")}})
	if code != http.StatusCreated {
		t.Fatalf("create the re-save = %d %q", code, a.Error)
	}
	if _, v := f.read(t, f.root, a.Draft.ID); len(v.Refused) == 0 || !strings.Contains(v.Refused[0].Sentence, "github-pay binds tools that work on repos.") {
		t.Errorf("a re-save with no covers line: refused %+v, want the covers line asked for", v.Refused)
	}
	if code, a := f.publishAll(t, f.root, f.create(t, f.root, role).Draft.ID); code != http.StatusOK {
		t.Fatalf("publish the repair = %d %q %+v", code, a.Error, a.Verdict)
	}
	if b := accessRowOf(t, f, "github-pay"); b.ScopeFault != "" || b.Scope.Key() != "objects:101" {
		t.Errorf("after the repair the row reads %q with fault %q, want objects:101", b.Scope.Key(), b.ScopeFault)
	}
}

// TestServerAdminCannotWidenAnUnreadableRow pins that a row whose covers
// line cannot be read, which grants nothing, is not read as reaching every
// repo: a server admin who pins the type first and gives that role every
// repo meets the rule for a role newly on every repo while
// admin.serverAdminsCoverEvery is off.
func TestServerAdminCannotWidenAnUnreadableRow(t *testing.T) {
	t.Parallel()
	f := newDraftsFixture(t, nil, func(c *config.Config) { c.Admin.ServerAdminsCoverEvery = false })
	ctx := context.Background()
	for _, doc := range []string{plainGithub(), typedRole("github-old", "github", "get_me")} {
		if code, a := f.publishAll(t, f.root, f.create(t, f.root, doc).Draft.ID); code != http.StatusOK {
			t.Fatalf("publish = %d %q", code, a.Error)
		}
	}
	ro, err := f.app.store.Roles().GetByName(ctx, "github-old")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(filepath.ToSlash(f.app.cfg.Store.DSN))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`UPDATE tool_bindings SET object_scope = '{}' WHERE role_id = ?`, ro.ID); err != nil {
		t.Fatal(err)
	}
	code, a := f.publishAll(t, f.erin, f.create(t, f.erin, pinnedGithub(), coversRoleDoc("github-old", "every", "get_me")).Draft.ID)
	if want := "On this deployment only a global admin may make github-old cover every repo"; code < 400 || !strings.Contains(a.Error, want) {
		t.Errorf("erin's first pin giving the unreadable row every repo = %d %q, want a refusal containing %q", code, a.Error, want)
	}
}
