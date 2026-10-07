package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// The refusals the owned-role tests pin, worded as the handlers word them.
const (
	wantEchoAdminRefusal  = "this server's admin role is mcp-admin-echoapp, which you do not hold. Ask your identity manager for mcp-admin-echoapp, or a holder of straza-global-mcp-admin to make the change."
	wantOtherAdminRefusal = "this server's admin role is mcp-admin-otherapp, which you do not hold. Ask your identity manager for mcp-admin-otherapp, or a holder of straza-global-mcp-admin to make the change."
	wantAreaRefusal       = "requires role straza-admin, straza-global-mcp-admin or a role mapped in admin.roleAreas. The admin role of a server opens that server's own routes only."
	wantNamingRefusal     = "a role of the server echoapp is named echoapp-<suffix>. The server's page fills the prefix for you"
	wantPrefixRefusal     = "role names beginning with echoapp- belong to the server echoapp. Create it on that server's page so it becomes server-owned"
	wantNotOwnedRefusal   = "global-readers is not a role of the server echoapp. A server admin gives access only to the roles their server owns"
	wantUnownedRefusal    = "the role finance-team belongs to no server, so a server admin cannot change it. Ask an identity administrator."
)

// ownedRig is the fixture of the server-owned role tests: kim is root, erin
// administers echoapp, owen administers otherapp, and erin has created
// echoapp-readers over the echo tool.
type ownedRig struct {
	app                   *App
	base                  string
	root, erin, owen, mia string
	readers               rolePayload
}

func newOwnedRig(t *testing.T) *ownedRig {
	t.Helper()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	root := loginDeviceFlow(t, base, "kim", "hunter2!")
	up := startEchoUpstream(t)
	for _, name := range []string{"echoapp", "otherapp"} {
		mf := strings.Replace(echoManifest(up.URL), "name: echoapp", "name: "+name, 1)
		if code := rawReq(t, "POST", base+"/v1/admin/apps", root, "application/yaml", []byte(mf), nil); code != http.StatusCreated {
			t.Fatalf("install %s = %d", name, code)
		}
	}
	mkHuman(t, app, "erin", "mcp-admin-echoapp")
	mkHuman(t, app, "owen", "mcp-admin-otherapp")
	mkHuman(t, app, "mia", MCPAdminRole)
	rig := &ownedRig{app: app, base: base, root: root,
		erin: loginDeviceFlow(t, base, "erin", "hunter2!"), owen: loginDeviceFlow(t, base, "owen", "hunter2!"),
		mia: loginDeviceFlow(t, base, "mia", "hunter2!")}
	if code := adminReq(t, "POST", base+"/v1/admin/roles", rig.erin,
		map[string]any{"name": "echoapp-readers", "description": "Reads echo", "server": "echoapp", "tools": []string{"echo"}}, &rig.readers); code != http.StatusCreated {
		t.Fatalf("erin creates echoapp-readers = %d", code)
	}
	return rig
}

// call sends one admin request and answers the status and the error sentence.
func (rig *ownedRig) call(t *testing.T, method, path, tok string, body map[string]any) (int, string) {
	t.Helper()
	var out any
	code := adminReq(t, method, rig.base+path, tok, body, &out)
	msg := ""
	if m, ok := out.(map[string]any); ok {
		msg, _ = m["error"].(string)
	}
	return code, msg
}

// bindingOf answers the id of role's binding on app from root's list.
func (rig *ownedRig) bindingOf(t *testing.T, role, app string) string {
	t.Helper()
	var rows []bindingPayload
	if code := adminReq(t, "GET", rig.base+"/v1/admin/bindings", rig.root, nil, &rows); code != http.StatusOK {
		t.Fatalf("root lists bindings = %d", code)
	}
	for _, b := range rows {
		if b.Role == role && b.App == app {
			return b.ID
		}
	}
	t.Fatalf("no binding of %s on %s in %v", role, app, rows)
	return ""
}

// countRecords counts the straza.audit.admin records with action on role
// and answers the last one.
func countRecords(t *testing.T, app *App, action, role string) (int, map[string]any) {
	t.Helper()
	n := 0
	var last map[string]any
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == action && ev["role"] == role {
			n++
			last = ev
		}
	}
	return n, last
}

// TestOwnedRoleCreate pins POST /v1/admin/roles across the three standings:
// the naming, kind and tools rules of a server-owned role, the exclusive
// prefix for global roles, the one roles.create record, the binding the
// create makes, and the list each standing sees.
func TestOwnedRoleCreate(t *testing.T) {
	t.Parallel()
	rig := newOwnedRig(t)
	if rig.readers.Server != "echoapp" || strings.Join(rig.readers.Tools, ",") != "echo" || rig.readers.Kind != store.RoleKindApplication {
		t.Errorf("create answer = %+v, want server echoapp, tools echo, kind application", rig.readers)
	}
	echo := []string{"echo"}
	cases := []struct {
		name string
		tok  string
		body map[string]any
		want int
		err  string
	}{
		{"server admin, no server", rig.erin, map[string]any{"name": "echoapp-x"}, http.StatusBadRequest, ownedRoleServerErr},
		{"server admin, other server", rig.erin, map[string]any{"name": "otherapp-x", "server": "otherapp", "tools": echo}, http.StatusForbidden, wantOtherAdminRefusal},
		{"server admin, kind forced", rig.erin, map[string]any{"name": "echoapp-forced", "server": "echoapp", "kind": "business", "tools": echo}, http.StatusCreated, ""},
		{"other server's admin", rig.owen, map[string]any{"name": "echoapp-y", "server": "echoapp", "tools": echo}, http.StatusForbidden, wantEchoAdminRefusal},
		{"global, business kind", rig.root, map[string]any{"name": "echoapp-biz", "server": "echoapp", "kind": "business", "tools": echo}, http.StatusBadRequest, ownedRoleKindErr},
		{"global, name off prefix", rig.root, map[string]any{"name": "readers", "server": "echoapp", "tools": echo}, http.StatusBadRequest, wantNamingRefusal},
		{"global, empty suffix", rig.root, map[string]any{"name": "echoapp-", "server": "echoapp", "tools": echo}, http.StatusBadRequest, wantNamingRefusal},
		{"global, no tools", rig.root, map[string]any{"name": "echoapp-notools", "server": "echoapp"}, http.StatusBadRequest, ownedRoleToolsErr},
		{"server admin, catalog glob", rig.erin, map[string]any{"name": "echoapp-star", "server": "echoapp", "tools": []string{"*"}}, http.StatusBadRequest, ownedRoleToolsErr},
		{"global, prefix without server", rig.root, map[string]any{"name": "echoapp-loose"}, http.StatusBadRequest, wantPrefixRefusal},
		{"global, prefix folded", rig.root, map[string]any{"name": "EchoApp-Loose"}, http.StatusBadRequest, wantPrefixRefusal},
		{"global, unknown server", rig.root, map[string]any{"name": "nosuch-x", "server": "nosuch", "tools": echo}, http.StatusNotFound, "no server named nosuch is registered, so no role can be owned by it. Check the name with strazactl apps list"},
		{"global, taken name", rig.root, map[string]any{"name": "echoapp-readers", "server": "echoapp", "tools": echo}, http.StatusConflict, roleExistsMsg("echoapp-readers")},
		{"global, owned role", rig.root, map[string]any{"name": "echoapp-ok", "server": "echoapp", "kind": "application", "tools": echo}, http.StatusCreated, ""},
		{"global, plain role", rig.root, map[string]any{"name": "finance-team", "kind": "business"}, http.StatusCreated, ""},
		{"global MCP admin, owned role", rig.mia, map[string]any{"name": "echoapp-mia", "server": "echoapp", "tools": echo}, http.StatusCreated, ""},
		{"global MCP admin, global role", rig.mia, map[string]any{"name": "mia-team", "kind": "business"}, http.StatusForbidden, "session lacks scope identity:write"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg := rig.call(t, "POST", "/v1/admin/roles", tc.tok, tc.body)
			if code != tc.want || msg != tc.err {
				t.Errorf("create = %d %q, want %d %q", code, msg, tc.want, tc.err)
			}
		})
	}

	n, ev := countRecords(t, rig.app, "roles.create", "echoapp-readers")
	if n != 1 || ev["actor"] != "erin" || ev["target"] != rig.readers.ID || ev["server"] != "echoapp" || ev["reason"] != nil {
		t.Errorf("roles.create records for echoapp-readers = %d, last %v; want one by erin with target, role and server", n, ev)
	}
	if n, ev := countRecords(t, rig.app, "roles.create", "finance-team"); n != 1 || ev["actor"] != "kim" || ev["server"] != nil {
		t.Errorf("roles.create records for finance-team = %d, last %v; want one by kim without server", n, ev)
	}
	if n, ev := countRecords(t, rig.app, "roles.create", "echoapp-mia"); n != 1 || ev["actor"] != "mia" || ev["server"] != "echoapp" {
		t.Errorf("roles.create records for echoapp-mia = %d, last %v; want one by mia naming echoapp", n, ev)
	}
	if id := rig.bindingOf(t, "echoapp-readers", "echoapp"); id == "" {
		t.Error("the create made no binding")
	}

	var erinRows []rolePayload
	if code := adminReq(t, "GET", rig.base+"/v1/admin/roles", rig.erin, nil, &erinRows); code != http.StatusOK {
		t.Fatalf("erin lists roles = %d", code)
	}
	var names []string
	for _, row := range erinRows {
		names = append(names, row.Name)
		if row.Server != "echoapp" || strings.Join(row.Tools, ",") != "echo" || row.Kind != store.RoleKindApplication {
			t.Errorf("erin's row %s = server %q tools %v kind %q, want echoapp, echo, application", row.Name, row.Server, row.Tools, row.Kind)
		}
	}
	if strings.Join(names, ",") != "echoapp-forced,echoapp-mia,echoapp-ok,echoapp-readers" {
		t.Errorf("erin's list = %v, want the four echoapp roles by name and no global role", names)
	}
	var owenRows []rolePayload
	if code := adminReq(t, "GET", rig.base+"/v1/admin/roles", rig.owen, nil, &owenRows); code != http.StatusOK || len(owenRows) != 0 {
		t.Errorf("owen's list = %d %v, want 200 and no rows", code, owenRows)
	}
	var rootRows []rolePayload
	if code := adminReq(t, "GET", rig.base+"/v1/admin/roles", rig.root, nil, &rootRows); code != http.StatusOK {
		t.Fatalf("root lists roles = %d", code)
	}
	seen := map[string]rolePayload{}
	for _, row := range rootRows {
		seen[row.Name] = row
	}
	if seen["finance-team"].Server != "" || seen["finance-team"].Tools != nil || seen["echoapp-readers"].Server != "echoapp" || seen["straza-admin"].Name == "" {
		t.Errorf("root's list = %v, want global rows without server beside the owned ones", seen)
	}
	var doc string
	if code, b, _ := adminBytes(t, "GET", rig.base+"/v1/admin/roles/"+rig.readers.ID+"/export", rig.root, "", nil); code != http.StatusOK || !strings.Contains(string(b), "server: echoapp") {
		t.Errorf("export = %d %s, want the server line", code, b)
	} else {
		doc = string(b)
	}
	if strings.Contains(doc, "server: echoapp") && !strings.Contains(doc, "app: echoapp") {
		t.Errorf("export names the server but not its binding: %s", doc)
	}
}

// TestOwnedRoleChangeAndDelete pins PATCH and DELETE on /v1/admin/roles/{id}
// and the implication verbs across the three standings: the fixed kind,
// the description a server admin may change, the holder freeze counted
// through a business role, and the one roles.delete record per delete.
func TestOwnedRoleChangeAndDelete(t *testing.T) {
	t.Parallel()
	rig := newOwnedRig(t)
	ctx := context.Background()
	var otherReaders, team, writers rolePayload
	if code := adminReq(t, "POST", rig.base+"/v1/admin/roles", rig.root, map[string]any{"name": "otherapp-readers", "server": "otherapp", "tools": []string{"echo"}}, &otherReaders); code != http.StatusCreated {
		t.Fatalf("otherapp-readers = %d", code)
	}
	if code := adminReq(t, "POST", rig.base+"/v1/admin/roles", rig.root, map[string]any{"name": "finance-team", "kind": "business"}, &team); code != http.StatusCreated {
		t.Fatalf("finance-team = %d", code)
	}
	if code := adminReq(t, "POST", rig.base+"/v1/admin/roles", rig.erin, map[string]any{"name": "echoapp-writers", "server": "echoapp", "tools": []string{"echo"}}, &writers); code != http.StatusCreated {
		t.Fatalf("echoapp-writers = %d", code)
	}
	if code, msg := rig.call(t, "POST", "/v1/admin/roles/"+team.ID+"/implications", rig.root, map[string]any{"implies_role_id": rig.readers.ID}); code != http.StatusCreated {
		t.Fatalf("team implies readers = %d %q", code, msg)
	}
	mkHuman(t, rig.app, "tess", "finance-team")
	if code, msg := rig.call(t, "DELETE", "/v1/admin/roles/"+rig.readers.ID, rig.erin, nil); code != http.StatusConflict ||
		msg != "the role echoapp-readers has 1 holder. The identity manager removes them first, then delete it" {
		t.Errorf("erin deletes with one holder through the team = %d %q", code, msg)
	}
	mkHuman(t, rig.app, "uma", "echoapp-readers")
	if code, out, _ := adminBytes(t, "PUT", rig.base+"/v1/admin/policies", rig.root, "application/yaml", []byte(roleSet("echoapp-writers-access", "{roles: [echoapp-writers]}"))); code != http.StatusCreated {
		t.Fatalf("store echoapp-writers-access = %d %s", code, out)
	}
	if code := adminReq(t, "POST", rig.base+"/v1/admin/policies/echoapp-writers-access/activate", rig.root, map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate echoapp-writers-access = %d", code)
	}

	cases := []struct {
		name, method, path, tok string
		body                    map[string]any
		want                    int
		err                     string
	}{
		{"server admin describes own role", "PATCH", "/v1/admin/roles/" + rig.readers.ID, rig.erin, map[string]any{"description": "Reads echo, updated"}, http.StatusOK, ""},
		{"server admin sends a kind", "PATCH", "/v1/admin/roles/" + rig.readers.ID, rig.erin, map[string]any{"kind": "application"}, http.StatusBadRequest, ownedRoleKindErr},
		{"server admin on a global role", "PATCH", "/v1/admin/roles/" + team.ID, rig.erin, map[string]any{"description": "x"}, http.StatusForbidden, wantUnownedRefusal},
		{"server admin on another server's role", "PATCH", "/v1/admin/roles/" + otherReaders.ID, rig.erin, map[string]any{"description": "x"}, http.StatusForbidden, wantOtherAdminRefusal},
		{"server admin on no role", "PATCH", "/v1/admin/roles/nosuch", rig.erin, map[string]any{"description": "x"}, http.StatusNotFound, "no such role"},
		{"global flips the kind", "PATCH", "/v1/admin/roles/" + rig.readers.ID, rig.root, map[string]any{"kind": "business"}, http.StatusBadRequest, ownedRoleKindErr},
		{"global restates the kind", "PATCH", "/v1/admin/roles/" + rig.readers.ID, rig.root, map[string]any{"kind": "application", "description": "restated"}, http.StatusOK, ""},
		{"server admin adds an edge", "POST", "/v1/admin/roles/" + rig.readers.ID + "/implications", rig.erin, map[string]any{"implies_role_id": team.ID}, http.StatusForbidden, wantAreaRefusal},
		{"server admin removes an edge", "DELETE", "/v1/admin/roles/" + team.ID + "/implications/" + rig.readers.ID, rig.erin, nil, http.StatusForbidden, wantAreaRefusal},
		{"global adds an edge from an owned role", "POST", "/v1/admin/roles/" + rig.readers.ID + "/implications", rig.root, map[string]any{"implies_role_id": writers.ID}, http.StatusBadRequest, ownedRoleImpliesErr},
		{"server admin deletes another server's role", "DELETE", "/v1/admin/roles/" + otherReaders.ID, rig.erin, nil, http.StatusForbidden, wantOtherAdminRefusal},
		{"server admin deletes a held role", "DELETE", "/v1/admin/roles/" + rig.readers.ID, rig.erin, nil, http.StatusConflict, "the role echoapp-readers has 2 holders. The identity manager removes them first, then delete it"},
		{"other server's admin deletes it", "DELETE", "/v1/admin/roles/" + rig.readers.ID, rig.owen, nil, http.StatusForbidden, wantEchoAdminRefusal},
		{"server admin deletes an unheld role with its own set", "DELETE", "/v1/admin/roles/" + writers.ID, rig.erin, nil, http.StatusOK, ""},
		{"global deletes the held role", "DELETE", "/v1/admin/roles/" + rig.readers.ID, rig.root, nil, http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg := rig.call(t, tc.method, tc.path, tc.tok, tc.body)
			if code != tc.want || msg != tc.err {
				t.Errorf("%s %s = %d %q, want %d %q", tc.method, tc.path, code, msg, tc.want, tc.err)
			}
		})
	}

	var patched rolePayload
	if code := adminReq(t, "PATCH", rig.base+"/v1/admin/roles/"+otherReaders.ID, rig.root, map[string]any{"description": "y"}, &patched); code != http.StatusOK ||
		patched.Server != "otherapp" || strings.Join(patched.Tools, ",") != "echo" {
		t.Errorf("patch answer = %d %+v, want server otherapp and tools echo", code, patched)
	}
	if n, ev := countRecords(t, rig.app, "roles.delete", "echoapp-writers"); n != 1 || ev["actor"] != "erin" || ev["target"] != writers.ID || ev["server"] != "echoapp" || ev["reason"] != nil {
		t.Errorf("roles.delete records for echoapp-writers = %d, last %v; want one by erin with target, role and server", n, ev)
	}
	if row := rowOf(t, rig.app, "echoapp-writers-access"); row.Status != "draft" {
		t.Errorf("echoapp-writers-access reads %s after erin's delete, want draft, since the set matched only the role", row.Status)
	}
	if n, ev := policyRecords(t, rig.app, "policy.deactivate", "echoapp-writers-access"); n != 1 || ev["actor"] != "erin" {
		t.Errorf("policy.deactivate records for echoapp-writers-access = %d, last %v; want one by erin", n, ev)
	}
	if n, ev := countRecords(t, rig.app, "roles.delete", "echoapp-readers"); n != 1 || ev["actor"] != "kim" || ev["server"] != "echoapp" {
		t.Errorf("roles.delete records for echoapp-readers = %d, last %v; want one by kim", n, ev)
	}
	if _, err := rig.app.store.Roles().GetByName(ctx, "echoapp-readers"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("echoapp-readers after the global delete: %v", err)
	}
	if _, err := rig.app.store.Roles().GetByName(ctx, "finance-team"); err != nil {
		t.Errorf("the team role went with the owned role: %v", err)
	}
}

// TestOwnedRoleBindings pins the binding verbs across the three standings:
// an owned role reaches only its owner, a server admin binds and unbinds
// only the roles their server owns and never while anyone holds them, a
// server admin never sends the every-tool matcher, and each server admin
// lists the bindings of their own servers alone.
func TestOwnedRoleBindings(t *testing.T) {
	t.Parallel()
	rig := newOwnedRig(t)
	ctx := context.Background()
	if code, msg := rig.call(t, "POST", "/v1/admin/roles", rig.root, map[string]any{"name": "global-readers", "kind": "application"}); code != http.StatusCreated {
		t.Fatalf("global-readers = %d %q", code, msg)
	}
	// The global role's row predates server-owned roles, so it is a store
	// fixture: only a role a server owns gains a new row through the API.
	if _, err := rig.app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: mustRole(t, rig.app, "global-readers").ID,
		AppID: mustApp(t, rig.app, "echoapp").ID, ToolMatcher: `["echo"]`}); err != nil {
		t.Fatal(err)
	}
	globalBinding := rig.bindingOf(t, "global-readers", "echoapp")
	readersBinding := rig.bindingOf(t, "echoapp-readers", "echoapp")
	uma := mkHuman(t, rig.app, "uma", "echoapp-readers")
	frozen := "the role echoapp-readers has 1 holder. Its tools change only by the global admin or by a new role"

	cases := []struct {
		name, method, path, tok string
		body                    map[string]any
		want                    int
		err                     string
	}{
		{"server admin binds a global role", "POST", "/v1/admin/apps/echoapp/bindings", rig.erin, map[string]any{"role": "global-readers", "tools": []string{"echo"}}, http.StatusConflict, wantNotOwnedRefusal},
		{"server admin binds on another server", "POST", "/v1/admin/apps/otherapp/bindings", rig.erin, map[string]any{"role": "echoapp-readers", "tools": []string{"echo"}}, http.StatusForbidden, wantOtherAdminRefusal},
		{"global binds an owned role elsewhere", "POST", "/v1/admin/apps/otherapp/bindings", rig.root, map[string]any{"role": "echoapp-readers", "tools": []string{"echo"}}, http.StatusConflict, "echoapp-readers belongs to the server echoapp and reaches no other server"},
		{"server admin binds an owned role with the glob", "POST", "/v1/admin/apps/echoapp/bindings", rig.erin, map[string]any{"role": "echoapp-readers", "tools": []string{"*"}}, http.StatusBadRequest, ownedRoleToolsErr},
		{"server admin binds an owned role with no tools", "POST", "/v1/admin/apps/echoapp/bindings", rig.erin, map[string]any{"role": "echoapp-readers"}, http.StatusBadRequest, ownedRoleToolsErr},
		{"server admin rebinds a held role", "POST", "/v1/admin/apps/echoapp/bindings", rig.erin, map[string]any{"role": "echoapp-readers", "tools": []string{"echo"}}, http.StatusConflict, frozen},
		{"server admin unbinds a held role", "DELETE", "/v1/admin/bindings/" + readersBinding, rig.erin, nil, http.StatusConflict, frozen},
		{"server admin unbinds a global role", "DELETE", "/v1/admin/bindings/" + globalBinding, rig.erin, nil, http.StatusConflict, wantNotOwnedRefusal},
		{"other server's admin unbinds", "DELETE", "/v1/admin/bindings/" + readersBinding, rig.owen, nil, http.StatusForbidden, wantEchoAdminRefusal},
		{"server admin unbinds nothing", "DELETE", "/v1/admin/bindings/nosuch", rig.erin, nil, http.StatusNotFound, "no access row with that id"},
		{"global rebinds a held role", "POST", "/v1/admin/apps/echoapp/bindings", rig.root, map[string]any{"role": "echoapp-readers", "tools": []string{"echo"}}, http.StatusConflict, "this role already has an access row on this server: edit its tools instead of creating a second one"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg := rig.call(t, tc.method, tc.path, tc.tok, tc.body)
			if code != tc.want || msg != tc.err {
				t.Errorf("%s %s = %d %q, want %d %q", tc.method, tc.path, code, msg, tc.want, tc.err)
			}
		})
	}

	lists := []struct {
		name, tok string
		want      []string
	}{
		{"erin", rig.erin, []string{"echoapp/echoapp-readers", "echoapp/global-readers"}},
		{"owen", rig.owen, nil},
	}
	for _, l := range lists {
		var rows []bindingPayload
		if code := adminReq(t, "GET", rig.base+"/v1/admin/bindings", l.tok, nil, &rows); code != http.StatusOK {
			t.Fatalf("%s lists bindings = %d", l.name, code)
		}
		var got []string
		for _, b := range rows {
			got = append(got, b.App+"/"+b.Role)
		}
		if strings.Join(got, ",") != strings.Join(l.want, ",") {
			t.Errorf("%s's bindings = %v, want %v", l.name, got, l.want)
		}
	}

	// Once nobody holds the role its server admin changes the tools by
	// unbinding and binding again.
	as, err := rig.app.store.Roles().ListAssignments(ctx, store.SubjectUser, uma.ID)
	if err != nil || len(as) != 1 {
		t.Fatalf("uma's assignments = %v, %v", as, err)
	}
	if err := rig.app.store.Roles().Unassign(ctx, as[0].ID); err != nil {
		t.Fatal(err)
	}
	if code, msg := rig.call(t, "DELETE", "/v1/admin/bindings/"+readersBinding, rig.erin, nil); code != http.StatusOK {
		t.Errorf("erin unbinds the unheld role = %d %q, want 200", code, msg)
	}
	if code, msg := rig.call(t, "POST", "/v1/admin/apps/echoapp/bindings", rig.erin, map[string]any{"role": "echoapp-readers", "tools": []string{"echo"}}); code != http.StatusCreated {
		t.Errorf("erin binds the unheld role = %d %q, want 201", code, msg)
	}
}

// TestServerAdminRouteParity pins the routes a server admin may reach: each
// one is mounted under requireServerAdmin, so a session holding a minted
// role gets past the area check on it, and every other admin route refuses
// that session with the area sentence, so no handler is reached with the
// Full default of standingFrom through a forgotten mount.
func TestServerAdminRouteParity(t *testing.T) {
	t.Parallel()
	rig := newOwnedRig(t)
	mkHuman(t, rig.app, "uma", "echoapp-readers")
	readersBinding := rig.bindingOf(t, "echoapp-readers", "echoapp")
	reach := map[string]bool{
		"GET /v1/admin/apps": true, "POST /v1/admin/apps": true,
		"GET /v1/admin/apps/{id}/logs": true, "POST /v1/admin/apps/{id}/health": true,
		"POST /v1/admin/apps/{id}/enable": true, "POST /v1/admin/apps/{id}/disable": true,
		"POST /v1/admin/apps/{id}/secrets": true, "GET /v1/admin/apps/{id}/secrets": true,
		"DELETE /v1/admin/apps/{id}/secrets": true, "DELETE /v1/admin/apps/{id}/secrets/{role}": true,
		"GET /v1/admin/roles": true, "POST /v1/admin/roles": true,
		"GET /v1/admin/roles/{id}/export": true,
		"PATCH /v1/admin/roles/{id}":      true, "DELETE /v1/admin/roles/{id}": true,
		"POST /v1/admin/apps/{id}/bindings": true, "GET /v1/admin/bindings": true, "DELETE /v1/admin/bindings/{id}": true,
		// The drafts routes admit a server's admin as well. Their
		// wrapper, requireDrafts, puts no standing in the request, so no
		// handler there meets the Full default of standingFrom either.
		"GET /v1/admin/drafts": true, "POST /v1/admin/drafts": true, "POST /v1/admin/drafts/check": true,
		"GET /v1/admin/drafts/{id}": true, "PUT /v1/admin/drafts/{id}": true,
		"POST /v1/admin/drafts/{id}/discard": true, "POST /v1/admin/drafts/{id}/revert": true,
		"POST /v1/admin/drafts/{id}/publish": true, "POST /v1/admin/drafts/{id}/rebase": true,
	}
	// The approve and deny verbs take any signed-in human and sit outside
	// both admin wrappers.
	identified := map[string]bool{"POST /v1/admin/approvals/{id}/approve": true, "POST /v1/admin/approvals/{id}/deny": true}
	// The client assertion key routes pass a full administrator only and
	// refuse everyone else with their own sentence.
	fullOnly := map[string]bool{
		"POST /v1/admin/signing-keys/client-assertion/rotate":       true,
		"POST /v1/admin/signing-keys/client-assertion/{kid}/retire": true,
	}
	// Contact dials the address a draft names and passes only the scope
	// apps:write or straza-global-mcp-admin, with its own sentence.
	const contact = "POST /v1/admin/drafts/{id}/contact"
	fill := strings.NewReplacer("{role}", "x", "{name}", "x", "{deviceId}", "x", "{implicationId}", "x", "{bindingId}", "x", "{kid}", "x")
	seen := map[string]bool{}
	for _, rt := range rig.app.routeTable() {
		if !strings.HasPrefix(rt.pattern, "/v1/admin/") {
			continue
		}
		key := rt.method + " " + rt.pattern
		if identified[key] {
			continue
		}
		seen[key] = true
		path := rt.pattern
		switch {
		case strings.HasPrefix(path, "/v1/admin/roles/"):
			path = strings.Replace(path, "{id}", rig.readers.ID, 1)
		case strings.HasPrefix(path, "/v1/admin/bindings/"):
			path = strings.Replace(path, "{id}", readersBinding, 1)
		default:
			path = strings.Replace(path, "{id}", "echoapp", 1)
		}
		var code int
		var msg string
		if key == "GET /v1/admin/roles/{id}/export" {
			// The export answers a YAML document, not JSON, so it is read raw.
			code, _, _ = adminBytes(t, rt.method, rig.base+fill.Replace(path), rig.erin, "", nil)
		} else {
			code, msg = rig.call(t, rt.method, fill.Replace(path), rig.erin, nil)
		}
		refused := code == http.StatusForbidden && (msg == wantAreaRefusal || (fullOnly[key] && msg == fullAdminRefusal) ||
			(key == contact && msg == contactStandingRefusal))
		if reach[key] && refused {
			t.Errorf("%s is a server admin's route but refuses the standing: %d %q", key, code, msg)
		}
		if !reach[key] && !refused {
			t.Errorf("%s is not a server admin's route but answered %d %q", key, code, msg)
		}
	}
	for key := range reach {
		if !seen[key] {
			t.Errorf("%s is listed as a server admin's route but is not registered", key)
		}
	}
}

// TestServerRemovalDeletesOwnedRoles pins the removal cascade: every role
// the server owned goes with it, one roles.unassign record per direct
// holder with the reason "server removed", a holder through a business
// role gets none and keeps the team, and each role's roles.delete record
// names the server and the reason "removed with the server <name>".
func TestServerRemovalDeletesOwnedRoles(t *testing.T) {
	t.Parallel()
	rig := newOwnedRig(t)
	ctx := context.Background()
	var team rolePayload
	if code := adminReq(t, "POST", rig.base+"/v1/admin/roles", rig.root, map[string]any{"name": "finance-team", "kind": "business"}, &team); code != http.StatusCreated {
		t.Fatalf("finance-team = %d", code)
	}
	if code, msg := rig.call(t, "POST", "/v1/admin/roles/"+team.ID+"/implications", rig.root, map[string]any{"implies_role_id": rig.readers.ID}); code != http.StatusCreated {
		t.Fatalf("team implies readers = %d %q", code, msg)
	}
	tess := mkHuman(t, rig.app, "tess", "finance-team")
	uma := mkHuman(t, rig.app, "uma", "echoapp-readers")
	if code, msg := rig.call(t, "DELETE", "/v1/admin/apps/echoapp", rig.root, nil); code != http.StatusOK {
		t.Fatalf("remove echoapp = %d %q", code, msg)
	}
	if _, err := rig.app.store.Roles().GetByName(ctx, "echoapp-readers"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("echoapp-readers survived its server: %v", err)
	}
	if _, err := rig.app.store.Roles().GetByName(ctx, "finance-team"); err != nil {
		t.Errorf("the team role went with the server: %v", err)
	}
	if rows, err := rig.app.store.Roles().ListAssignments(ctx, store.SubjectUser, tess.ID); err != nil || len(rows) != 1 {
		t.Errorf("tess's assignments after the removal = %v, %v; want the team kept", rows, err)
	}
	unassigned := map[string]int{}
	for _, ev := range adminAuditEvents(t, rig.app) {
		if ev["action"] == "roles.unassign" && ev["role"] == "echoapp-readers" {
			user, _ := ev["user"].(string)
			unassigned[user]++
			if ev["reason"] != "server removed" || ev["actor"] != "kim" || ev["roleId"] != rig.readers.ID {
				t.Errorf("unassign record = %v, want reason server removed by kim", ev)
			}
		}
	}
	if len(unassigned) != 1 || unassigned[uma.ID] != 1 {
		t.Errorf("unassign records per holder = %v, want exactly one for uma and none for tess", unassigned)
	}
	if n, ev := countRecords(t, rig.app, "roles.delete", "echoapp-readers"); n != 1 || ev["actor"] != "kim" || ev["target"] != rig.readers.ID ||
		ev["server"] != "echoapp" || ev["reason"] != "removed with the server echoapp" {
		t.Errorf("roles.delete records for echoapp-readers = %d, last %v; want one by kim naming the server and the reason", n, ev)
	}
	if n, ev := countRecords(t, rig.app, "roles.delete", "mcp-admin-echoapp"); n != 1 || ev["reason"] != "removed with the server echoapp" || ev["server"] != nil {
		t.Errorf("roles.delete records for the minted role = %d, last %v; want one with the reason and no server", n, ev)
	}
}

// TestOwnedRoleExportStanding pins GET /v1/admin/roles/{id}/export across
// the standings the console's Edit tools save reads through: a server
// admin exports the roles their server owns and no other, and a global
// role stays with the identity administrators.
func TestOwnedRoleExportStanding(t *testing.T) {
	t.Parallel()
	rig := newOwnedRig(t)
	var other, team rolePayload
	if code := adminReq(t, "POST", rig.base+"/v1/admin/roles", rig.root, map[string]any{"name": "otherapp-readers", "server": "otherapp", "tools": []string{"echo"}}, &other); code != http.StatusCreated {
		t.Fatalf("otherapp-readers = %d", code)
	}
	if code := adminReq(t, "POST", rig.base+"/v1/admin/roles", rig.root, map[string]any{"name": "finance-team", "kind": "business"}, &team); code != http.StatusCreated {
		t.Fatalf("finance-team = %d", code)
	}
	cases := []struct {
		name string
		tok  string
		id   string
		code int
		want string
	}{
		{"root exports an owned role", rig.root, rig.readers.ID, http.StatusOK, "server: echoapp"},
		{"the server admin exports their own server's role", rig.erin, rig.readers.ID, http.StatusOK, "server: echoapp"},
		{"the server admin is refused another server's role", rig.erin, other.ID, http.StatusForbidden, "otherapp"},
		{"the server admin is refused a global role", rig.erin, team.ID, http.StatusForbidden, "belongs to no server"},
		{"the other server's admin is refused the role", rig.owen, rig.readers.ID, http.StatusForbidden, "echoapp"},
		{"the global MCP admin exports an owned role", rig.mia, rig.readers.ID, http.StatusOK, "server: echoapp"},
		{"the global MCP admin is refused a global role", rig.mia, team.ID, http.StatusForbidden, "identity:read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, b, _ := adminBytes(t, "GET", rig.base+"/v1/admin/roles/"+tc.id+"/export", tc.tok, "", nil)
			if code != tc.code || !strings.Contains(string(b), tc.want) {
				t.Errorf("export = %d %q, want %d holding %q", code, b, tc.code, tc.want)
			}
		})
	}
}
