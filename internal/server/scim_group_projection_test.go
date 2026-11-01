package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/objects"
	"github.com/strazahq/straza/internal/store"
)

// TestSCIMGroupProjection is the server-truth half of spec/scim-profile §4.1
// (revision 12): an exported role's wire-group render carries the role's
// identity and its transitive access projection. Pins: the implication closure pulls
// an implied role's bindings into apps; policies list exactly the ACTIVE
// sets NAMING a projected role (subject-global and draft sets absent);
// tools stay absent while no app is live in the manager (the same
// fail-closed truth /v1/admin/tools reports).
func TestSCIMGroupProjection(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := t.Context()
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminTok, _ := checkinToken(t, app, base)
	scimToken := mintProvisioningToken(t, base, adminTok)

	dev, err := app.store.Roles().Create(ctx, store.Role{Name: "proj-dev", Kind: store.RoleKindBusiness, Description: "Developer access"})
	if err != nil {
		t.Fatal(err)
	}
	baseRole, err := app.store.Roles().Create(ctx, store.Role{Name: "proj-base", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.Roles().AddImplication(ctx, dev.ID, baseRole.ID); err != nil {
		t.Fatal(err)
	}
	demoApp, err := app.store.Apps().Create(ctx, store.App{Name: "demo-tools", RuntimeKind: "remote", Status: "stopped"})
	if err != nil {
		t.Fatal(err)
	}
	// The binding hangs off the IMPLIED role: the projection must follow
	// the closure, not just the direct role.
	if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{
		RoleID: baseRole.ID, AppID: demoApp.ID, ToolMatcher: `["get_*"]`, Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}

	mkSet := func(name, roles, status string) {
		yaml := "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata: { name: " + name + " }\nspec:\n"
		if roles != "" {
			yaml += "  match: { roles: [" + roles + "] }\n"
		}
		yaml += "  rules:\n    - id: allow-read\n      tools: [file.read]\n      effect: allow\n"
		if _, err := app.store.Policies().Create(ctx, store.PolicySet{Name: name, YAMLSource: yaml, Status: status}); err != nil {
			t.Fatalf("policyset %s: %v", name, err)
		}
	}
	mkSet("dev-guard", "proj-dev", "active")
	mkSet("base-guard", "proj-base", "active")
	mkSet("global-guard", "", "active")     // subject-global: must not appear
	mkSet("dev-draft", "proj-dev", "draft") // inactive: must not appear
	mkSet("other-guard", "other", "active") // foreign role: must not appear

	req, _ := http.NewRequest(http.MethodGet, base+"/scim/v2/Groups/"+dev.ID, nil)
	req.Header.Set("Authorization", "Bearer "+scimToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("group read = %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}

	block, _ := out["urn:straza:params:scim:schemas:extension:2.0:Group"].(map[string]any)
	if block == nil {
		t.Fatalf("no extension block in %v", out)
	}
	if block["role"] != "proj-dev" || block["roleKind"] != "business" || block["plane"] != "access" {
		t.Errorf("role identity = %v/%v/%v, want proj-dev/business/access", block["role"], block["roleKind"], block["plane"])
	}
	if block["description"] != "Developer access" {
		t.Errorf("description = %v", block["description"])
	}
	if apps, _ := block["apps"].([]any); !reflect.DeepEqual(apps, []any{"demo-tools"}) {
		t.Errorf("apps = %v, want [demo-tools] via the implied role's binding", block["apps"])
	}
	if pols, _ := block["policies"].([]any); !reflect.DeepEqual(pols, []any{"base-guard", "dev-guard"}) {
		t.Errorf("policies = %v, want [base-guard dev-guard]", block["policies"])
	}
	if _, present := block["tools"]; present {
		t.Errorf("tools = %v, want absent (no live app in the manager)", block["tools"])
	}
	schemas, _ := out["schemas"].([]any)
	if len(schemas) != 2 {
		t.Errorf("schemas = %v, want core + extension", schemas)
	}
}

// TestSCIMGroupProjectionAdministers is the server-truth half of
// spec/scim-profile revision 18: a role's render lists the servers whose
// admin role it is, over the implication closure, so a minted role names
// its one server, a business role implying it names the same server, a
// role no server names carries no list, and straza-global-mcp-admin carries none
// because its meaning is every server rather than a fact on rows.
func TestSCIMGroupProjectionAdministers(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := t.Context()
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminTok, _ := checkinToken(t, app, base)
	scimToken := mintProvisioningToken(t, base, adminTok)

	srv, err := app.store.Apps().Create(ctx, store.App{Name: "finance/jira", RuntimeKind: "remote", Status: "stopped"})
	if err != nil {
		t.Fatal(err)
	}
	team, err := app.store.Roles().Create(ctx, store.Role{Name: "finance-team", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.Roles().AddImplication(ctx, team.ID, srv.AdminRoleID); err != nil {
		t.Fatal(err)
	}
	lone, err := app.store.Roles().Create(ctx, store.Role{Name: "lone-role", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	global, err := app.store.Roles().GetByName(ctx, MCPAdminRole)
	if err != nil {
		t.Fatal(err)
	}

	read := func(roleID string) map[string]any {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, base+"/scim/v2/Groups/"+roleID, nil)
		req.Header.Set("Authorization", "Bearer "+scimToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("group %s read = %d", roleID, resp.StatusCode)
		}
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		block, _ := out["urn:straza:params:scim:schemas:extension:2.0:Group"].(map[string]any)
		if block == nil {
			t.Fatalf("no extension block in %v", out)
		}
		return block
	}

	cases := []struct {
		name   string
		roleID string
		want   []any
	}{
		{"minted role names its server", srv.AdminRoleID, []any{"finance/jira"}},
		{"business role implying it names the same server", team.ID, []any{"finance/jira"}},
		{"a role no server names", lone.ID, nil},
		{"straza-global-mcp-admin lists nothing", global.ID, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block := read(tc.roleID)
			got, present := block["administers"]
			if tc.want == nil {
				if present {
					t.Errorf("administers = %v, want absent", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("administers = %v, want %v", got, tc.want)
			}
		})
	}
	minted := read(srv.AdminRoleID)
	if minted["roleKind"] != "straza" || minted["plane"] != "control" {
		t.Errorf("minted role renders %v/%v, want straza/control", minted["roleKind"], minted["plane"])
	}
}

// TestSCIMGroupProjectionServer is the server-truth half of
// spec/scim-profile revision 19: a server-owned role's render names the
// owning server under server, a global role renders none, and an owner id
// no server row names degrades to no attribute rather than a failed read.
// The block is driven with a role row built here, so the pin holds
// whatever fills the owner on a stored row.
func TestSCIMGroupProjectionServer(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := t.Context()
	srv, err := app.store.Apps().Create(ctx, store.App{Name: "demo-tools", RuntimeKind: "remote", Status: "stopped"})
	if err != nil {
		t.Fatal(err)
	}
	owned := store.Role{ID: "role-owned", Name: "demo-tools-readers", Kind: store.RoleKindApplication, Plane: store.RolePlaneAccess, OwnerAppID: srv.ID}
	global := store.Role{ID: "role-global", Name: "readers", Kind: store.RoleKindApplication, Plane: store.RolePlaneAccess}
	orphan := store.Role{ID: "role-orphan", Name: "gone-readers", Kind: store.RoleKindApplication, Plane: store.RolePlaneAccess, OwnerAppID: "no-such-app"}

	cases := []struct {
		name string
		role store.Role
		want any
	}{
		{"server-owned role names its server", owned, "demo-tools"},
		{"global role renders none", global, nil},
		{"owner no server row names renders none", orphan, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block := app.groupRoleBlock(ctx, tc.role)
			if block["role"] != tc.role.Name || block["roleKind"] != "application" || block["plane"] != "access" {
				t.Errorf("role identity = %v/%v/%v, want %s/application/access", block["role"], block["roleKind"], block["plane"], tc.role.Name)
			}
			got, present := block["server"]
			if tc.want == nil {
				if present {
					t.Errorf("server = %v, want absent", got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("server = %v, want %v", got, tc.want)
			}
		})
	}
}

// coversFaults wraps a store so a test can fail the three reads the Group
// block's apps, tools and covers need: the access rows of one role, the
// server list and the current names of the objects a covers line names.
type coversFaults struct {
	store.Store
	rowsOf atomic.Pointer[string]
	apps   atomic.Bool
	names  atomic.Bool
}

func (s *coversFaults) ObjectLists() store.ObjectListRepo {
	return coversFaultLists{s.Store.ObjectLists(), s}
}

type coversFaultLists struct {
	store.ObjectListRepo
	s *coversFaults
}

func (r coversFaultLists) ObjectsByIDs(ctx context.Context, appID string, ids []string) (map[string]string, error) {
	if r.s.names.Load() {
		return nil, errors.New("database unavailable")
	}
	return r.ObjectListRepo.ObjectsByIDs(ctx, appID, ids)
}

func (s *coversFaults) ToolBindings() store.ToolBindingRepo {
	return coversFaultRows{s.Store.ToolBindings(), s}
}

func (s *coversFaults) Apps() store.AppRepo { return coversFaultApps{s.Store.Apps(), s} }

type coversFaultRows struct {
	store.ToolBindingRepo
	s *coversFaults
}

func (r coversFaultRows) ListByRole(ctx context.Context, roleID string) ([]store.ToolBinding, error) {
	if id := r.s.rowsOf.Load(); id != nil && *id == roleID {
		return nil, errors.New("database unavailable")
	}
	return r.ToolBindingRepo.ListByRole(ctx, roleID)
}

type coversFaultApps struct {
	store.AppRepo
	s *coversFaults
}

func (r coversFaultApps) List(ctx context.Context) ([]store.App, error) {
	if r.s.apps.Load() {
		return nil, errors.New("database unavailable")
	}
	return r.AppRepo.List(ctx)
}

// TestSCIMGroupProjectionCovers is the server-truth half of
// spec/scim-profile revision 21: every access row in the closure on a
// server that pins an object type renders under covers as <server>:<type>
// and every, one value per named object with the name quoted, or none for
// a row with no line or one the store cannot read. A named object renders
// the name the server's object list holds for its id now, and its stored
// name when the list no longer holds it. Values are deduplicated and
// sorted, and every and a name from two roles both show. A failed read of
// the rows, the servers, a bound server's manifest or the current names
// leaves apps, tools and covers out together with one Warn line, and a
// PATCH on covers answers 400 mutability.
func TestSCIMGroupProjectionCovers(t *testing.T) {
	t.Parallel()
	faults := &coversFaults{}
	var logs *syncBuffer
	app, base := testAppPreRun(t, []func(*App){func(a *App) {
		faults.Store, a.store = a.store, faults
		a.log, logs = captureLogger()
	}})
	ctx := t.Context()
	server := func(name, manifest string) store.App {
		t.Helper()
		row, err := app.store.Apps().Create(ctx, store.App{Name: name, RuntimeKind: "remote", Status: "stopped", Manifest: manifest})
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	pinned := func(typ string) string {
		return `{"straza":{"runtime":{"kind":"remote"},"objects":{"type":"` + typ + `","list":{"tool":"list"}}}}`
	}
	ghapp, jira := server("ghapp", pinned("repo")), server("jira", pinned("issue"))
	plain, broken := server("plain", `{"straza":{"runtime":{"kind":"remote"}}}`), server("broken", `{"straza":5}`)
	seedList(t, app, ghapp.ID, "repo", manifestDeclHash(t, ghapp.Manifest), [][2]string{{"org/api", "101"}, {"org/web", "102"},
		{"every", "201"}, {`org/a"b<c>`, "202"}, {"org/new", "301"}, {"org/dup-a", "303"}, {"org/dup", "304"}})
	role := func(name, kind string, implies ...store.Role) store.Role {
		t.Helper()
		ro, err := app.store.Roles().Create(ctx, store.Role{Name: name, Kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range implies {
			if err := app.store.Roles().AddImplication(ctx, ro.ID, imp.ID); err != nil {
				t.Fatal(err)
			}
		}
		return ro
	}
	bound := func(name string, server store.App, scope *store.ObjectScope) store.Role {
		t.Helper()
		ro := role(name, store.RoleKindApplication)
		if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: ro.ID, AppID: server.ID, ToolMatcher: `["*"]`,
			Effect: "allow", Scope: scope}); err != nil {
			t.Fatal(err)
		}
		return ro
	}
	objects := func(pairs ...string) *store.ObjectScope {
		s := &store.ObjectScope{}
		for i := 0; i < len(pairs); i += 2 {
			s.Objects = append(s.Objects, store.CoveredObject{ID: pairs[i], Value: pairs[i+1]})
		}
		return s
	}
	every := &store.ObjectScope{Every: true}
	ghEvery, ghAlso := bound("gh-every", ghapp, every), bound("gh-also-every", ghapp, every)
	ghTwo, ghAPI := bound("gh-two", ghapp, objects("102", "org/web", "101", "org/api")), bound("gh-api", ghapp, objects("101", "org/api"))
	ghOdd := bound("gh-odd", ghapp, objects("201", "every", "202", `org/a"b<c>`))
	ghNone := bound("gh-none", ghapp, nil)
	ghFault := bound("gh-fault", ghapp, every)
	plainRead, jiraOne, brokenRead := bound("plain-read", plain, nil), bound("jira-one", jira, objects("7", "PAY-1")), bound("broken-read", broken, nil)
	ghRenamed, ghGone := bound("gh-renamed", ghapp, objects("301", "org/old")), bound("gh-gone", ghapp, objects("302", "org/gone"))
	ghWas, ghTook := bound("gh-was", ghapp, objects("303", "org/dup")), bound("gh-took", ghapp, objects("304", "org/dup"))
	team := role("team", store.RoleKindBusiness, ghEvery, ghAlso, ghTwo, ghAPI, jiraOne, plainRead)
	pair := role("pair", store.RoleKindBusiness, ghWas, ghTook)
	mixed := role("mixed", store.RoleKindBusiness, ghEvery, brokenRead)
	split := role("split", store.RoleKindBusiness, ghEvery, jiraOne)

	db, err := sql.Open("sqlite", "file:"+url.PathEscape(filepath.ToSlash(app.cfg.Store.DSN))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`UPDATE tool_bindings SET object_scope = '{}' WHERE role_id = ?`, ghFault.ID); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		role   store.Role
		arm    func() func()
		apps   []string
		covers []string
	}{
		{"every", ghEvery, nil, []string{"ghapp"}, []string{"ghapp:repo every"}},
		{"two named objects, one value each, sorted and quoted", ghTwo, nil, []string{"ghapp"}, []string{`ghapp:repo "org/api"`, `ghapp:repo "org/web"`}},
		{"a value named every and a value with a quote", ghOdd, nil, []string{"ghapp"}, []string{`ghapp:repo "every"`, `ghapp:repo "org/a\"b<c>"`}},
		{"no covers line renders none", ghNone, nil, []string{"ghapp"}, []string{"ghapp:repo none"}},
		{"an unreadable covers line renders none", ghFault, nil, []string{"ghapp"}, []string{"ghapp:repo none"}},
		{"a server that pins no type renders no covers", plainRead, nil, []string{"plain"}, nil},
		{"a business role over two pinned servers keeps every line", team, nil, []string{"ghapp", "jira", "plain"},
			[]string{`ghapp:repo "org/api"`, `ghapp:repo "org/web"`, "ghapp:repo every", `jira:issue "PAY-1"`}},
		{"a renamed object renders the name the list holds now", ghRenamed, nil, []string{"ghapp"}, []string{`ghapp:repo "org/new"`}},
		{"an object the list no longer holds renders its stored name", ghGone, nil, []string{"ghapp"}, []string{`ghapp:repo "org/gone"`}},
		{"two rows stored under one value render each current name", pair, nil, []string{"ghapp"},
			[]string{`ghapp:repo "org/dup"`, `ghapp:repo "org/dup-a"`}},
		{"a list read that fails leaves out apps, tools and covers", ghRenamed, func() func() {
			faults.names.Store(true)
			return func() { faults.names.Store(false) }
		}, nil, nil},
		{"a bound server's manifest that does not decode", mixed, nil, nil, nil},
		{"a closure role's access rows cannot be read", split, func() func() {
			faults.rowsOf.Store(&jiraOne.ID)
			return func() { faults.rowsOf.Store(nil) }
		}, nil, nil},
		{"the server list cannot be read", ghEvery, func() func() {
			faults.apps.Store(true)
			return func() { faults.apps.Store(false) }
		}, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.arm != nil {
				defer tc.arm()()
			}
			logs.Reset()
			block := app.groupRoleBlock(ctx, tc.role)
			if block["role"] != tc.role.Name {
				t.Errorf("role = %v, want %s: the rest of the block renders whatever the access reads do", block["role"], tc.role.Name)
			}
			for key, want := range map[string][]string{"apps": tc.apps, "covers": tc.covers} {
				got, present := block[key]
				switch {
				case want == nil && present:
					t.Errorf("%s = %v, want absent", key, got)
				case want != nil && !reflect.DeepEqual(got, want):
					t.Errorf("%s = %#v, want %v", key, got, want)
				}
			}
			if _, present := block["tools"]; present && tc.apps == nil {
				t.Errorf("tools = %v, want absent with apps and covers", block["tools"])
			}
			warns := strings.Count(logs.String(), "scim: the group projection leaves out apps, tools and covers")
			if want := map[bool]int{true: 1, false: 0}[tc.apps == nil]; warns != want {
				t.Errorf("Warn lines = %d, want %d:\n%s", warns, want, logs.String())
			}
		})
	}

	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminTok, _ := checkinToken(t, app, base)
	scimToken := mintProvisioningToken(t, base, adminTok)
	body := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"covers","value":["ghapp:repo every"]}]}`
	req, _ := http.NewRequest(http.MethodPatch, base+"/scim/v2/Groups/"+ghTwo.ID, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+scimToken)
	req.Header.Set("Content-Type", "application/scim+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var scimErr struct {
		ScimType string `json:"scimType"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&scimErr); err != nil || resp.StatusCode != http.StatusBadRequest || scimErr.ScimType != "mutability" {
		t.Errorf("PATCH covers = %d %q (%v), want 400 mutability", resp.StatusCode, scimErr.ScimType, err)
	}
}

// manifestDeclHash is the declaration hash of the objects block that the
// stored manifest JSON pins.
func manifestDeclHash(t *testing.T, manifest string) string {
	t.Helper()
	mf, err := manager.FromJSON(manifest)
	if err != nil || mf.Straza.Objects == nil {
		t.Fatalf("the manifest pins no objects block: %v", err)
	}
	return objects.DeclHash(*mf.Straza.Objects)
}
