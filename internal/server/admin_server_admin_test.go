package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestMCPAdminRoleBoot pins the product-defined global MCP admin: the
// role exists from boot with its fixed description, its standing is the
// two apps grants and nothing else, and it cannot be deleted.
func TestMCPAdminRoleBoot(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	role, err := app.store.Roles().GetByName(ctx, MCPAdminRole)
	if err != nil {
		t.Fatalf("%s missing after boot: %v", MCPAdminRole, err)
	}
	if role.Plane != store.RolePlaneControl || !strings.HasPrefix(role.Description, "Administers every MCP server") {
		t.Errorf("boot role = %+v, want control plane with the product description", role)
	}
	if got := app.adminGrantsForRoles([]store.Role{role}); got != "apps:read,apps:write" {
		t.Errorf("standing = %q, want apps:read,apps:write", got)
	}
	if got := app.roleAreas(role); strings.Join(got, ",") != "apps:read,apps:write" {
		t.Errorf("roleAreas = %v, want the two apps grants", got)
	}

	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	mkHuman(t, app, "mia", MCPAdminRole)
	miaTok := loginDeviceFlow(t, base, "mia", "hunter2!")

	if code := adminReq(t, "GET", base+"/v1/admin/apps", miaTok, nil, nil); code != http.StatusOK {
		t.Errorf("mia lists apps = %d, want 200", code)
	}
	var refused map[string]string
	if code := adminReq(t, "GET", base+"/v1/admin/users", miaTok, nil, &refused); code != http.StatusForbidden || refused["error"] != "session lacks scope identity:read" {
		t.Errorf("mia lists users = %d %v, want 403 lacking identity:read", code, refused)
	}
	if code := adminReq(t, "DELETE", base+"/v1/admin/roles/"+role.ID, root, nil, &refused); code != http.StatusConflict || !strings.Contains(refused["error"], "comes with the product") {
		t.Errorf("delete %s = %d %v, want 409 comes with the product", MCPAdminRole, code, refused)
	}
}

// TestRoleAreasRefusesMCPAdminRole pins the config validator: the product
// role's meaning is fixed, so a map entry for it refuses boot like one for
// straza-admin does.
func TestRoleAreasRefusesMCPAdminRole(t *testing.T) {
	t.Parallel()
	_, err := buildRoleAreaScopes(config.Admin{RoleAreas: map[string][]string{MCPAdminRole: {"apps:read"}}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "fixed by the product") {
		t.Fatalf("err = %v, want a refusal naming the fixed meaning", err)
	}
}

// TestRoleCreateRefusesMintedPrefix pins the reserved mcp-admin- prefix at
// role create, so a role wearing it is product-minted by definition.
func TestRoleCreateRefusesMintedPrefix(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	var refused map[string]string
	code := adminReq(t, "POST", base+"/v1/admin/roles", root, map[string]string{"name": "mcp-admin-x", "kind": "straza"}, &refused)
	want := "role names beginning with mcp-admin- are reserved for the roles Straza creates with a server; choose a name without that prefix"
	if code != http.StatusBadRequest || refused["error"] != want {
		t.Fatalf("create = %d %v, want 400 %q", code, refused, want)
	}
}

// TestServerAdminScope walks the delegated server admin end to end: a
// server's minted role opens that server's own verbs and nothing else, the
// list is filtered and flagged, registration of a new name is refused, a
// business role that implies the minted role counts through the closure,
// and the role goes with its server and never alone.
func TestServerAdminScope(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	up := startEchoUpstream(t)
	other := strings.Replace(echoManifest(up.URL), "name: echoapp", "name: otherapp", 1)

	var installed appPayload
	if code := rawReq(t, "POST", base+"/v1/admin/apps", root, "application/yaml", []byte(echoManifest(up.URL)), &installed); code != http.StatusCreated {
		t.Fatalf("install = %d", code)
	}
	if installed.AdminRole != "mcp-admin-echoapp" || installed.AdminRoleID == "" {
		t.Fatalf("install answer names admin role %q (%q), want mcp-admin-echoapp", installed.AdminRole, installed.AdminRoleID)
	}
	if code := rawReq(t, "POST", base+"/v1/admin/apps", root, "application/yaml", []byte(other), nil); code != http.StatusCreated {
		t.Fatalf("install otherapp = %d", code)
	}
	waitAppStatus(t, base, root, "echoapp", "running")
	if ev := lastAdminEvent(t, app, "apps.install"); ev["adminRole"] != "mcp-admin-otherapp" || ev["actor"] != "kim" {
		t.Errorf("apps.install record = %v, want adminRole mcp-admin-otherapp by kim", ev)
	}

	erin := mkHuman(t, app, "erin", "mcp-admin-echoapp")
	erinTok := loginDeviceFlow(t, base, "erin", "hunter2!")

	var rows []appPayload
	if code := adminReq(t, "GET", base+"/v1/admin/apps", erinTok, nil, &rows); code != http.StatusOK {
		t.Fatalf("erin lists = %d", code)
	}
	if len(rows) != 1 || rows[0].Name != "echoapp" || rows[0].MayChange == nil || !*rows[0].MayChange {
		t.Errorf("erin's list = %+v, want echoapp alone with may_change true", rows)
	}
	rows = nil
	if code := adminReq(t, "GET", base+"/v1/admin/apps", root, nil, &rows); code != http.StatusOK || len(rows) != 2 {
		t.Fatalf("root lists = %d %d rows, want 200 and 2", code, len(rows))
	}
	for _, row := range rows {
		if row.MayChange == nil || !*row.MayChange || row.AdminRole == "" {
			t.Errorf("root's row %s = may_change %v admin_role %q, want true and a name", row.Name, row.MayChange, row.AdminRole)
		}
	}

	_, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token": erinTok, "harness": map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:x"}},
	})
	if checkin["admin_servers"] != float64(1) || checkin["admin_grants"] != nil {
		t.Errorf("erin's checkin = admin_servers %v admin_grants %v, want 1 and no grants", checkin["admin_servers"], checkin["admin_grants"])
	}

	changed := strings.Replace(echoManifest(up.URL), `version: "1.0.0"`, `version: "2.0.0"`, 1)
	if code := rawReq(t, "POST", base+"/v1/admin/apps", erinTok, "application/yaml", []byte(changed), nil); code != http.StatusCreated {
		t.Errorf("erin changes echoapp = %d, want 201", code)
	}
	var refused map[string]string
	third := strings.Replace(echoManifest(up.URL), "name: echoapp", "name: thirdapp", 1)
	wantNew := "registering a new server needs the scope apps:write or the role straza-global-mcp-admin. You administer 1 server and may change it."
	if code := rawReq(t, "POST", base+"/v1/admin/apps", erinTok, "application/yaml", []byte(third), &refused); code != http.StatusForbidden || refused["error"] != wantNew {
		t.Errorf("erin registers a new name = %d %v, want 403 %q", code, refused, wantNew)
	}
	wantOther := "this server's admin role is mcp-admin-otherapp, which you do not hold. Ask your identity manager for mcp-admin-otherapp, or a holder of straza-global-mcp-admin to make the change."
	wantArea := "requires role straza-admin, straza-global-mcp-admin or a role mapped in admin.roleAreas. The admin role of a server opens that server's own routes only."
	cases := []struct {
		name, method, path string
		want               int
		errText            string
	}{
		{"own health", "POST", "/v1/admin/apps/echoapp/health", http.StatusOK, ""},
		{"own disable", "POST", "/v1/admin/apps/echoapp/disable", http.StatusOK, ""},
		{"own enable", "POST", "/v1/admin/apps/echoapp/enable", http.StatusOK, ""},
		{"own secrets list", "GET", "/v1/admin/apps/echoapp/secrets", http.StatusOK, ""},
		{"own remove stays global", "DELETE", "/v1/admin/apps/echoapp", http.StatusForbidden, wantArea},
		{"own secret delete, none stored", "DELETE", "/v1/admin/apps/echoapp/secrets", http.StatusNotFound, ""},
		{"other health", "POST", "/v1/admin/apps/otherapp/health", http.StatusForbidden, wantOther},
		{"other by id", "POST", "/v1/admin/apps/" + appID(t, app, "otherapp") + "/disable", http.StatusForbidden, wantOther},
		{"other secrets", "GET", "/v1/admin/apps/otherapp/secrets", http.StatusForbidden, wantOther},
		{"unknown server", "POST", "/v1/admin/apps/nosuch/health", http.StatusNotFound, ""},
		{"bindings, own servers only", "GET", "/v1/admin/bindings", http.StatusOK, ""},
		{"tools stay global", "GET", "/v1/admin/tools", http.StatusForbidden, wantArea},
		{"users stay closed", "GET", "/v1/admin/users", http.StatusForbidden, wantArea},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]string
			var out any
			if tc.errText != "" {
				out = &body
			}
			code := adminReq(t, tc.method, base+tc.path, erinTok, nil, out)
			if code != tc.want || (tc.errText != "" && body["error"] != tc.errText) {
				t.Errorf("%s %s = %d %v, want %d %q", tc.method, tc.path, code, body, tc.want, tc.errText)
			}
		})
	}
	for _, action := range []string{"apps.disable", "apps.enable"} {
		var n int
		for _, ev := range adminAuditEvents(t, app) {
			if ev["action"] != action || ev["app"] != "echoapp" {
				continue
			}
			n++
			if ev["actor"] != "erin" || ev["adminRole"] != "mcp-admin-echoapp" {
				t.Errorf("%s record = %v, want actor erin naming mcp-admin-echoapp", action, ev)
			}
		}
		if n != 1 {
			t.Errorf("%s records on echoapp = %d, want exactly one", action, n)
		}
	}

	// A team is a business role that implies the minted role: the resolver
	// grants the closure, so the holder administers the server without a
	// direct assignment, and the check-in count agrees.
	if code := adminReq(t, "POST", base+"/v1/admin/roles", root, map[string]string{"name": "finance-team", "kind": "business"}, nil); code != http.StatusCreated {
		t.Fatalf("create team role = %d", code)
	}
	teamRole, _ := app.store.Roles().GetByName(ctx, "finance-team")
	otherRole, _ := app.store.Roles().GetByName(ctx, "mcp-admin-otherapp")
	if err := app.store.Roles().AddImplication(ctx, teamRole.ID, otherRole.ID); err != nil {
		t.Fatal(err)
	}
	app.resolver.Bump()
	frank := mkHuman(t, app, "frank", "finance-team")
	frankTok := loginDeviceFlow(t, base, "frank", "hunter2!")
	holders := []struct {
		name, tok string
		want      int
		servers   []string
	}{
		{"direct holder", erinTok, 1, []string{"echoapp"}},
		{"holder through a business role", frankTok, 1, []string{"otherapp"}},
		{"no admin role", loginDeviceFlow(t, base, mkHuman(t, app, "gil").Username, "hunter2!"), 0, nil},
	}
	for _, h := range holders {
		t.Run(h.name, func(t *testing.T) {
			var rows []appPayload
			var out any
			if h.want > 0 {
				out = &rows
			}
			code := adminReq(t, "GET", base+"/v1/admin/apps", h.tok, nil, out)
			var names []string
			for _, row := range rows {
				names = append(names, row.Name)
			}
			if h.want == 0 {
				if code != http.StatusForbidden {
					t.Errorf("list = %d, want 403", code)
				}
			} else if code != http.StatusOK || strings.Join(names, ",") != strings.Join(h.servers, ",") {
				t.Errorf("list = %d %v, want 200 %v", code, names, h.servers)
			}
			for _, name := range h.servers {
				if code := adminReq(t, "POST", base+"/v1/admin/apps/"+name+"/health", h.tok, nil, nil); code != http.StatusOK {
					t.Errorf("health on %s = %d, want 200", name, code)
				}
			}
			_, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
				"id_token": h.tok, "harness": map[string]string{"name": "claude-code", "version": "2.1.0"},
				"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:x"}},
			})
			got, _ := checkin["admin_servers"].(float64)
			if int(got) != h.want {
				t.Errorf("admin_servers = %v, want %d", checkin["admin_servers"], h.want)
			}
		})
	}
	_ = frank

	// Deletes: the role never goes alone, and it goes with its server
	// together with its memberships, one unassign record per holder.
	wantRoleDel := `role "mcp-admin-echoapp" is the admin role of server echoapp and lives as long as the server does. Remove the server instead.`
	echoRole, _ := app.store.Roles().GetByName(ctx, "mcp-admin-echoapp")
	if code := adminReq(t, "DELETE", base+"/v1/admin/roles/"+echoRole.ID, root, nil, &refused); code != http.StatusConflict || refused["error"] != wantRoleDel {
		t.Errorf("delete a server's role = %d %v, want 409 %q", code, refused, wantRoleDel)
	}
	hana := mkHuman(t, app, "hana", "mcp-admin-echoapp")
	if code := adminReq(t, "DELETE", base+"/v1/admin/apps/echoapp", root, nil, nil); code != http.StatusOK {
		t.Errorf("delete echoapp = %d, want 200", code)
	}
	if _, err := app.store.Roles().GetByName(ctx, "mcp-admin-echoapp"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("mcp-admin-echoapp survived its server: %v", err)
	}
	var listed []rolePayload
	if code := adminReq(t, "GET", base+"/v1/admin/roles", root, nil, &listed); code != http.StatusOK {
		t.Fatalf("roles list = %d", code)
	}
	for _, ro := range listed {
		if ro.Name == "mcp-admin-echoapp" {
			t.Errorf("the roles list still shows %s after its server went", ro.Name)
		}
	}
	unassigned := map[string]int{}
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == "roles.unassign" && ev["role"] == "mcp-admin-echoapp" {
			user, _ := ev["user"].(string)
			unassigned[user]++
			if ev["reason"] != "server removed" || ev["origin"] != "admin" || ev["actor"] != "kim" || ev["roleId"] != echoRole.ID {
				t.Errorf("unassign record = %v, want reason server removed, origin admin, by kim", ev)
			}
		}
	}
	if len(unassigned) != 2 || unassigned[erin.ID] != 1 || unassigned[hana.ID] != 1 {
		t.Errorf("unassign records per holder = %v, want exactly one for erin and one for hana", unassigned)
	}
	if ev := lastAdminEvent(t, app, "apps.remove"); ev["app"] != "echoapp" || ev["actor"] != "kim" || ev["adminRole"] != "mcp-admin-echoapp" {
		t.Errorf("remove record = %v, want echoapp by kim naming mcp-admin-echoapp", ev)
	}
	// Removal stays with an apps-area grant: a server admin pauses their
	// server and never removes it, because removal also deletes the office.
	// A business role that implied the minted role keeps its other meaning
	// and ends no membership, since frank holds the team role and not the
	// minted one.
	if code := adminReq(t, "DELETE", base+"/v1/admin/apps/otherapp", frankTok, nil, &refused); code != http.StatusForbidden || refused["error"] != wantArea {
		t.Errorf("frank removes otherapp = %d %v, want 403 %q", code, refused, wantArea)
	}
	if code := adminReq(t, "DELETE", base+"/v1/admin/apps/otherapp", root, nil, nil); code != http.StatusOK {
		t.Errorf("root removes otherapp = %d, want 200", code)
	}
	if _, err := app.store.Roles().GetByName(ctx, "mcp-admin-otherapp"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("mcp-admin-otherapp survived its server: %v", err)
	}
	if _, err := app.store.Roles().GetByName(ctx, "finance-team"); err != nil {
		t.Errorf("the team role went with the server: %v", err)
	}
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == "roles.unassign" && ev["role"] == "mcp-admin-otherapp" {
			t.Errorf("an implied holder got an unassign record: %v", ev)
		}
	}
}

// TestBootBackfillsAppAdminRoles pins the upgrade path: the boot step
// mints a role for a row that has none, which is what a row from before the
// column looks like, and announces it once as a role create would.
func TestBootBackfillsAppAdminRoles(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()
	row, err := app.store.Apps().Create(ctx, store.App{Name: "legacy", RuntimeKind: "remote", Manifest: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.Roles().Delete(ctx, row.AdminRoleID); err != nil {
		t.Fatal(err)
	}
	if err := app.ensureAppAdminRoles(ctx); err != nil {
		t.Fatalf("ensureAppAdminRoles: %v", err)
	}
	row, err = app.store.Apps().GetByName(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	role, err := app.store.Roles().GetByID(ctx, row.AdminRoleID)
	if err != nil || role.Name != "mcp-admin-legacy" {
		t.Fatalf("backfilled role = %+v, %v; want mcp-admin-legacy", role, err)
	}
	for _, ev := range adminAuditEvents(t, app) {
		if ev["app"] == "legacy" {
			t.Errorf("the backfill wrote an admin record: %v", ev)
		}
	}
	if n := identityEvents(t, app, role.ID); n != 1 {
		t.Errorf("identity events for the minted role = %d, want exactly one", n)
	}
	if err := app.ensureAppAdminRoles(ctx); err != nil {
		t.Fatal(err)
	}
	if n := identityEvents(t, app, role.ID); n != 1 {
		t.Errorf("a second boot announced the role again: %d events", n)
	}
}

// identityEvents counts the straza.identity.updated events naming id.
func identityEvents(t *testing.T, app *App, id string) int {
	t.Helper()
	rows, err := app.store.Outbox().ListRecent(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, row := range rows {
		if row.Subject == "straza.identity.updated" && strings.Contains(row.CE, `"id":"`+id+`"`) {
			n++
		}
	}
	return n
}

func mkHuman(t *testing.T, app *App, name string, roles ...string) store.User {
	t.Helper()
	hash, err := testPasswordHash("hunter2!")
	if err != nil {
		t.Fatal(err)
	}
	u, err := app.store.Users().Create(context.Background(), store.User{Username: name, Email: name + "@x.io", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range roles {
		grantRole(t, app, u.ID, r)
	}
	return u
}

func appID(t *testing.T, app *App, name string) string {
	t.Helper()
	row, err := app.store.Apps().GetByName(context.Background(), name)
	if err != nil {
		t.Fatalf("app %s: %v", name, err)
	}
	return row.ID
}

func lastAdminEvent(t *testing.T, app *App, action string) map[string]any {
	t.Helper()
	evs := adminAuditEvents(t, app)
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i]["action"] == action {
			return evs[i]
		}
	}
	t.Fatalf("no %s record on the chain", action)
	return nil
}

// TestServerAdminRuntimeChangeRefused pins the host-privilege line on a
// change: a server admin may not turn their remote server into a command
// or oci runtime, because that is a process on the gateway host, while a
// change that leaves the runtime alone still lands.
func TestServerAdminRuntimeChangeRefused(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	up := startEchoUpstream(t)
	if code := rawReq(t, "POST", base+"/v1/admin/apps", root, "application/yaml", []byte(echoManifest(up.URL)), nil); code != http.StatusCreated {
		t.Fatalf("install = %d", code)
	}
	waitAppStatus(t, base, root, "echoapp", "running")
	mkHuman(t, app, "erin", "mcp-admin-echoapp")
	erinTok := loginDeviceFlow(t, base, "erin", "hunter2!")

	want := "changing a server's runtime needs the scope apps:write or the role straza-global-mcp-admin, because a command or oci runtime is a process on the gateway host. Ask a holder of straza-global-mcp-admin to make that change."
	cases := []struct{ name, runtime string }{
		{"command", "    kind: command\n    command: {exec: /bin/true}\n"},
		{"oci", "    kind: oci\n    oci: {image: example.test/echo:1}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(echoManifest(up.URL), "    kind: remote\n    remote: {url: \""+up.URL+"\"}\n", tc.runtime, 1)
			if !strings.Contains(body, tc.runtime) {
				t.Fatalf("manifest replacement did not apply: %s", body)
			}
			var refused map[string]string
			if code := rawReq(t, "POST", base+"/v1/admin/apps", erinTok, "application/yaml", []byte(body), &refused); code != http.StatusForbidden || refused["error"] != want {
				t.Errorf("erin turns echoapp into %s = %d %v, want 403 %q", tc.name, code, refused, want)
			}
			if code := rawReq(t, "POST", base+"/v1/admin/apps?dryRun=1", erinTok, "application/yaml", []byte(body), &refused); code != http.StatusForbidden {
				t.Errorf("dry run of the same change = %d, want 403", code)
			}
		})
	}
	var rows []appPayload
	if code := adminReq(t, "GET", base+"/v1/admin/apps", root, nil, &rows); code != http.StatusOK || len(rows) != 1 || rows[0].Runtime != "remote" {
		t.Fatalf("after the refusals the server is %+v, want one remote server", rows)
	}
	changed := strings.Replace(echoManifest(up.URL), `version: "1.0.0"`, `version: "3.0.0"`, 1)
	if code := rawReq(t, "POST", base+"/v1/admin/apps", erinTok, "application/yaml", []byte(changed), nil); code != http.StatusCreated {
		t.Errorf("erin's version change = %d, want 201", code)
	}
}
