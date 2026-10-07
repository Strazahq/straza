package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/store"
)

// TestStandingMatchesTheDirectRoutes runs each standing case of the
// direct config routes two ways from the same state: through the standing
// check, with the one-item draft that route makes of the change, and
// through the route itself. Both must give the same answer: a pass, or the
// status and the words. The routes are fired live, so the table cannot
// drift from them.
func TestStandingMatchesTheDirectRoutes(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) {
		c.Admin.RoleAreas = map[string][]string{"id-admins": {"identity:read", "identity:write"}}
	})
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	kimLogin := loginDeviceFlow(t, base, "kim", "hunter2!")
	up := startEchoUpstream(t)
	manifest := func(name, addr, description string) string {
		meta := "metadata: {name: " + name + "}"
		if description != "" {
			meta = "metadata: {name: " + name + ", description: " + description + "}"
		}
		return strings.Replace(echoManifest(addr), "metadata: {name: echoapp}", meta, 1)
	}
	install := func(name string) store.App {
		t.Helper()
		if code := rawReq(t, http.MethodPost, base+"/v1/admin/apps", kimLogin, "application/yaml", []byte(manifest(name, up.URL, "")), nil); code != http.StatusCreated {
			t.Fatalf("install %s = %d", name, code)
		}
		row, err := app.store.Apps().GetByName(ctx, name)
		must(err)
		return row
	}
	echo := install("echoapp")
	install("delapp")
	other, err := app.store.Apps().Create(ctx, store.App{Name: "otherapp", RuntimeKind: "remote",
		Manifest: appFactsOf(t, appDoc("otherapp", "https://other.example.com/mcp", "", "")).Manifest})
	must(err)
	adminRoleOf := func(row store.App) string {
		ro, err := app.store.Roles().GetByID(ctx, row.AdminRoleID)
		must(err)
		return ro.Name
	}
	echoAdmin := adminRoleOf(echo)
	_, err = app.store.Roles().Create(ctx, store.Role{Name: "id-admins"})
	must(err)
	mkHuman(t, app, "max", MCPAdminRole)
	mkHuman(t, app, "ida", "id-admins")
	mkHuman(t, app, "erin", echoAdmin)
	mkHuman(t, app, "jo", adminRoleOf(other))
	mkHuman(t, app, "nell")
	hana := mkHuman(t, app, "hana")
	hold := func(ro store.Role) {
		_, err := app.store.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: hana.ID, RoleID: ro.ID})
		must(err)
	}
	owned := func(name string, held, row bool) {
		ro, b, err := app.store.Roles().CreateOwned(ctx, store.Role{Name: name, OwnerAppID: echo.ID}, `["echo"]`)
		must(err)
		if !row {
			must(app.store.ToolBindings().Delete(ctx, b.ID))
		}
		if held {
			hold(ro)
		}
	}
	global := func(name, kind string, row bool) store.Role {
		ro, err := app.store.Roles().Create(ctx, store.Role{Name: name, Kind: kind})
		must(err)
		if row {
			_, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: ro.ID, AppID: echo.ID, ToolMatcher: `["echo"]`})
			must(err)
		}
		return ro
	}
	owned("echoapp-held", true, true)
	owned("echoapp-held2", true, true)
	owned("echoapp-free", false, true)
	owned("echoapp-free2", false, true)
	owned("echoapp-unbind", false, true)
	owned("echoapp-norow", false, false)
	owned("echoapp-norow2", false, false)
	owned("echoapp-heldnorow", true, false)
	for _, name := range []string{"g-dev", "g-unbind"} {
		global(name, store.RoleKindApplication, true)
	}
	for _, name := range []string{"g-ops", "g-ops2", "g-ops3", "g-ops4", "g-del"} {
		global(name, store.RoleKindApplication, false)
	}
	global("g-team", store.RoleKindBusiness, false)
	team2 := global("g-team2", store.RoleKindBusiness, false)
	id := func(name string) string {
		t.Helper()
		ro, err := app.store.Roles().GetByName(ctx, name)
		must(err)
		return ro.ID
	}
	must(app.store.Roles().AddImplication(ctx, team2.ID, id("g-ops4")))
	app.resolver.Bump()
	rowOf := func(name string) string {
		t.Helper()
		rows, err := app.store.ToolBindings().ListByRole(ctx, id(name))
		must(err)
		if len(rows) != 1 {
			t.Fatalf("%s holds %d access rows, want 1", name, len(rows))
		}
		return rows[0].ID
	}
	mint := func(name, scope string) string {
		var minted struct {
			Token string `json:"token"`
		}
		if code := adminReq(t, http.MethodPost, base+"/v1/admin/api-tokens", kimLogin, map[string]any{"name": name, "scope": scope}, &minted); code != http.StatusCreated && code != http.StatusOK {
			t.Fatalf("mint %s = %d", name, code)
		}
		return minted.Token
	}
	bearers := map[string]string{"kim": kimLogin, "apps-token": mint("apps-token", "apps:write"), "identity-token": mint("identity-token", "identity:write")}
	for _, name := range []string{"max", "ida", "erin", "jo", "nell"} {
		bearers[name] = loginDeviceFlow(t, base, name, "hunter2!")
	}

	liveDoc := func(w drafts.World, name string) drafts.RoleDoc {
		t.Helper()
		d, ok := drafts.RoleDocOf(w, name)
		if !ok {
			t.Fatalf("no live role %s", name)
		}
		return d
	}
	text := func(d drafts.RoleDoc) string {
		t.Helper()
		b, err := d.Marshal()
		must(err)
		return string(b)
	}
	rolePut := func(name string, change func(*drafts.RoleDoc)) func(drafts.World) drafts.Item {
		return func(w drafts.World) drafts.Item {
			d := liveDoc(w, name)
			change(&d)
			return drafts.Item{Kind: drafts.KindRole, Name: name, Op: drafts.OpPut, Doc: text(d)}
		}
	}
	describe := func(name, description string) func(drafts.World) drafts.Item {
		return rolePut(name, func(d *drafts.RoleDoc) { d.Spec.Description = description })
	}
	bind := func(name string) func(drafts.World) drafts.Item {
		return rolePut(name, func(d *drafts.RoleDoc) {
			d.Spec.Bindings = []drafts.RoleBinding{{App: "echoapp", Tools: []string{"echo"}}}
		})
	}
	unbind := func(name string) func(drafts.World) drafts.Item {
		return rolePut(name, func(d *drafts.RoleDoc) { d.Spec.Bindings = nil })
	}
	imply := func(name, implied string) func(drafts.World) drafts.Item {
		return rolePut(name, func(d *drafts.RoleDoc) { d.Spec.Implies = append(d.Spec.Implies, implied) })
	}
	unimply := func(name string) func(drafts.World) drafts.Item {
		return rolePut(name, func(d *drafts.RoleDoc) { d.Spec.Implies = nil })
	}
	create := func(name, server string) func(drafts.World) drafts.Item {
		return func(drafts.World) drafts.Item {
			d := drafts.RoleDoc{APIVersion: drafts.RoleAPIVersion, Kind: string(drafts.KindRole), Metadata: drafts.RoleDocMeta{Name: name},
				Spec: drafts.RoleDocSpec{Kind: drafts.RoleKindApplication, Server: server}}
			if server != "" {
				d.Spec.Bindings = []drafts.RoleBinding{{App: server, Tools: []string{"echo"}}}
			}
			return drafts.Item{Kind: drafts.KindRole, Name: name, Op: drafts.OpPut, Doc: text(d)}
		}
	}
	remove := func(kind drafts.Kind, name string) func(drafts.World) drafts.Item {
		return func(drafts.World) drafts.Item { return drafts.Item{Kind: kind, Name: name, Op: drafts.OpRemove} }
	}
	appPut := func(name, doc string) func(drafts.World) drafts.Item {
		return func(drafts.World) drafts.Item {
			return drafts.Item{Kind: drafts.KindApp, Name: name, Op: drafts.OpPut, Doc: doc}
		}
	}
	const set = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: route-set}
spec:
  priority: 100
  rules:
    - id: no-rm
      tools: [shell.exec]
      command: {denyPatterns: ["rm -rf *"]}
      effect: deny
      reason: "Straza: blocked"
`
	jsonBody := func(v any) (string, []byte) { return "application/json", mustJSON(v) }
	yamlBody := func(s string) (string, []byte) { return "application/yaml", []byte(s) }
	serverAdmin := "this server's admin role is " + echoAdmin + ", which you do not hold. Ask your identity manager for " + echoAdmin +
		", or a holder of straza-global-mcp-admin to make the change."
	const (
		register     = "registering a new server needs the scope apps:write or the role straza-global-mcp-admin. You administer 1 server and may change it."
		address      = "changing a server's address needs the scope apps:write or the role straza-global-mcp-admin, because the server's credentials and every caller's token are sent to that address. Ask a holder of straza-global-mcp-admin to make that change."
		noGrants     = areaRefusalNoGrants
		noIdentity   = "session lacks scope identity:write"
		noApps       = "session lacks scope apps:write"
		heldDelete   = "the role echoapp-held has 1 holder. The identity manager removes them first, then delete it"
		heldTools    = "the role echoapp-held has 1 holder. Its tools change only by the global admin or by a new role"
		heldAddTools = "the role echoapp-heldnorow has 1 holder. Its tools change only by the global admin or by a new role"
	)
	cases := []struct {
		name, who, method, path string
		body                    func() (string, []byte)
		item                    func(drafts.World) drafts.Item
		status                  int
		says                    string
	}{
		{"a server admin changes its server's description", "erin", "POST", "/v1/admin/apps",
			func() (string, []byte) { return yamlBody(manifest("echoapp", up.URL, "Echo tools")) },
			appPut("echoapp", manifest("echoapp", up.URL, "Echo tools")), http.StatusCreated, ""},
		{"a server admin changes its server's address", "erin", "POST", "/v1/admin/apps",
			func() (string, []byte) { return yamlBody(manifest("echoapp", up.URL+"/moved", "")) },
			appPut("echoapp", manifest("echoapp", up.URL+"/moved", "")), http.StatusForbidden, address},
		{"a server admin registers a server", "erin", "POST", "/v1/admin/apps",
			func() (string, []byte) { return yamlBody(manifest("newapp", up.URL, "")) },
			appPut("newapp", manifest("newapp", up.URL, "")), http.StatusForbidden, register},
		{"a server admin changes another server", "jo", "POST", "/v1/admin/apps",
			func() (string, []byte) { return yamlBody(manifest("echoapp", up.URL, "")) },
			appPut("echoapp", manifest("echoapp", up.URL, "")), http.StatusForbidden, register},
		{"an identity admin installs a server", "ida", "POST", "/v1/admin/apps",
			func() (string, []byte) { return yamlBody(manifest("echoapp", up.URL, "")) },
			appPut("echoapp", manifest("echoapp", up.URL, "")), http.StatusForbidden, noApps},
		{"a person with no role installs a server", "nell", "POST", "/v1/admin/apps",
			func() (string, []byte) { return yamlBody(manifest("echoapp", up.URL, "")) },
			appPut("echoapp", manifest("echoapp", up.URL, "")), http.StatusForbidden, noGrants},
		{"an admin API token with identity:write installs a server", "identity-token", "POST", "/v1/admin/apps",
			func() (string, []byte) { return yamlBody(manifest("echoapp", up.URL, "")) },
			appPut("echoapp", manifest("echoapp", up.URL, "")), http.StatusForbidden, "token lacks scope apps:write"},
		{"the global MCP admin changes a server", "max", "POST", "/v1/admin/apps",
			func() (string, []byte) { return yamlBody(manifest("echoapp", up.URL, "")) },
			appPut("echoapp", manifest("echoapp", up.URL, "")), http.StatusCreated, ""},
		{"a server admin removes its server", "erin", "DELETE", "/v1/admin/apps/echoapp", nil,
			remove(drafts.KindApp, "echoapp"), http.StatusForbidden, noGrants},
		{"the global MCP admin removes a server", "max", "DELETE", "/v1/admin/apps/delapp", nil,
			remove(drafts.KindApp, "delapp"), http.StatusOK, ""},

		{"an identity admin creates a global role", "ida", "POST", "/v1/admin/roles",
			func() (string, []byte) { return jsonBody(map[string]any{"name": "g-new-ida", "kind": "application"}) },
			create("g-new-ida", ""), http.StatusCreated, ""},
		{"the global MCP admin creates a global role", "max", "POST", "/v1/admin/roles",
			func() (string, []byte) { return jsonBody(map[string]any{"name": "g-new-max", "kind": "application"}) },
			create("g-new-max", ""), http.StatusForbidden, noIdentity},
		{"a server admin creates a global role", "erin", "POST", "/v1/admin/roles",
			func() (string, []byte) { return jsonBody(map[string]any{"name": "g-new-erin", "kind": "application"}) },
			create("g-new-erin", ""), http.StatusBadRequest, drafts.OwnedRoleServerErr},
		{"a server admin creates a role of its server", "erin", "POST", "/v1/admin/roles",
			func() (string, []byte) {
				return jsonBody(map[string]any{"name": "echoapp-new-erin", "server": "echoapp", "tools": []string{"echo"}})
			},
			create("echoapp-new-erin", "echoapp"), http.StatusCreated, ""},
		{"the global MCP admin creates a role of a server", "max", "POST", "/v1/admin/roles",
			func() (string, []byte) {
				return jsonBody(map[string]any{"name": "echoapp-new-max", "server": "echoapp", "tools": []string{"echo"}})
			},
			create("echoapp-new-max", "echoapp"), http.StatusCreated, ""},
		{"an identity admin creates a role of a server", "ida", "POST", "/v1/admin/roles",
			func() (string, []byte) {
				return jsonBody(map[string]any{"name": "echoapp-new-ida", "server": "echoapp", "tools": []string{"echo"}})
			},
			create("echoapp-new-ida", "echoapp"), http.StatusCreated, ""},
		{"a server admin creates a role of another server", "jo", "POST", "/v1/admin/roles",
			func() (string, []byte) {
				return jsonBody(map[string]any{"name": "echoapp-new-jo", "server": "echoapp", "tools": []string{"echo"}})
			},
			create("echoapp-new-jo", "echoapp"), http.StatusForbidden, serverAdmin},
		{"a person with no role creates a role", "nell", "POST", "/v1/admin/roles",
			func() (string, []byte) {
				return jsonBody(map[string]any{"name": "echoapp-new-nell", "server": "echoapp", "tools": []string{"echo"}})
			},
			create("echoapp-new-nell", "echoapp"), http.StatusForbidden, noGrants},
		{"an admin API token with apps:write creates a role of a server", "apps-token", "POST", "/v1/admin/roles",
			func() (string, []byte) {
				return jsonBody(map[string]any{"name": "echoapp-new-tok", "server": "echoapp", "tools": []string{"echo"}})
			},
			create("echoapp-new-tok", "echoapp"), http.StatusForbidden, "token lacks scope identity:write"},
		{"an admin API token with identity:write creates a role of a server", "identity-token", "POST", "/v1/admin/roles",
			func() (string, []byte) {
				return jsonBody(map[string]any{"name": "echoapp-new-tok2", "server": "echoapp", "tools": []string{"echo"}})
			},
			create("echoapp-new-tok2", "echoapp"), http.StatusCreated, ""},

		{"a server admin describes a held role of its server", "erin", "PATCH", "/v1/admin/roles/" + id("echoapp-held"),
			func() (string, []byte) { return jsonBody(map[string]any{"description": "Described by erin"}) },
			describe("echoapp-held", "Described by erin"), http.StatusOK, ""},
		{"a server admin describes a role of another server", "jo", "PATCH", "/v1/admin/roles/" + id("echoapp-held"),
			func() (string, []byte) { return jsonBody(map[string]any{"description": "Described by jo"}) },
			describe("echoapp-held", "Described by jo"), http.StatusForbidden, serverAdmin},
		{"the global MCP admin describes a held role of a server", "max", "PATCH", "/v1/admin/roles/" + id("echoapp-held"),
			func() (string, []byte) { return jsonBody(map[string]any{"description": "Changed again"}) },
			rolePut("echoapp-held", func(d *drafts.RoleDoc) { d.Spec.Description = "Changed again" }), http.StatusOK, ""},
		{"an identity admin describes a held role of a server", "ida", "PATCH", "/v1/admin/roles/" + id("echoapp-held"),
			func() (string, []byte) { return jsonBody(map[string]any{"description": "Changed once more"}) },
			rolePut("echoapp-held", func(d *drafts.RoleDoc) { d.Spec.Description = "Changed once more" }), http.StatusOK, ""},
		{"a server admin describes a global role", "erin", "PATCH", "/v1/admin/roles/" + id("g-dev"),
			func() (string, []byte) { return jsonBody(map[string]any{"description": "Described by erin"}) },
			describe("g-dev", "Described by erin"), http.StatusForbidden, "the role g-dev belongs to no server, so a server admin cannot change it. Ask an identity administrator."},
		{"the global MCP admin describes a global role", "max", "PATCH", "/v1/admin/roles/" + id("g-dev"),
			func() (string, []byte) { return jsonBody(map[string]any{"description": "Described by max"}) },
			describe("g-dev", "Described by max"), http.StatusForbidden, noIdentity},
		{"a person with no role describes a global role", "nell", "PATCH", "/v1/admin/roles/" + id("g-dev"),
			func() (string, []byte) { return jsonBody(map[string]any{"description": "Described by nell"}) },
			describe("g-dev", "Described by nell"), http.StatusForbidden, noGrants},
		{"an identity admin describes a global role", "ida", "PATCH", "/v1/admin/roles/" + id("g-dev"),
			func() (string, []byte) { return jsonBody(map[string]any{"description": "Described by ida"}) },
			describe("g-dev", "Described by ida"), http.StatusOK, ""},

		{"a server admin removes a held role of its server", "erin", "DELETE", "/v1/admin/roles/" + id("echoapp-held"), nil,
			remove(drafts.KindRole, "echoapp-held"), http.StatusConflict, heldDelete},
		{"the global MCP admin removes a held role of a server", "max", "DELETE", "/v1/admin/roles/" + id("echoapp-held"), nil,
			remove(drafts.KindRole, "echoapp-held"), http.StatusConflict, heldDelete},
		{"a server admin removes a role of another server", "jo", "DELETE", "/v1/admin/roles/" + id("echoapp-free"), nil,
			remove(drafts.KindRole, "echoapp-free"), http.StatusForbidden, serverAdmin},
		{"the global MCP admin removes a global role", "max", "DELETE", "/v1/admin/roles/" + id("g-dev"), nil,
			remove(drafts.KindRole, "g-dev"), http.StatusForbidden, noIdentity},
		{"an identity admin removes a held role of a server", "ida", "DELETE", "/v1/admin/roles/" + id("echoapp-held2"), nil,
			remove(drafts.KindRole, "echoapp-held2"), http.StatusOK, ""},
		{"a server admin removes a role of its server nobody holds", "erin", "DELETE", "/v1/admin/roles/" + id("echoapp-free2"), nil,
			remove(drafts.KindRole, "echoapp-free2"), http.StatusOK, ""},
		{"an identity admin removes a global role", "ida", "DELETE", "/v1/admin/roles/" + id("g-del"), nil,
			remove(drafts.KindRole, "g-del"), http.StatusOK, ""},

		{"the global MCP admin adds an implication", "max", "POST", "/v1/admin/roles/" + id("g-team") + "/implications",
			func() (string, []byte) { return jsonBody(map[string]any{"implies_role_id": id("g-ops2")}) },
			imply("g-team", "g-ops2"), http.StatusForbidden, noIdentity},
		{"a server admin adds an implication", "erin", "POST", "/v1/admin/roles/" + id("g-team") + "/implications",
			func() (string, []byte) { return jsonBody(map[string]any{"implies_role_id": id("g-ops2")}) },
			imply("g-team", "g-ops2"), http.StatusForbidden, noGrants},
		{"an identity admin adds an implication", "ida", "POST", "/v1/admin/roles/" + id("g-team") + "/implications",
			func() (string, []byte) { return jsonBody(map[string]any{"implies_role_id": id("g-ops")}) },
			imply("g-team", "g-ops"), http.StatusCreated, ""},
		{"the global MCP admin removes an implication", "max", "DELETE", "/v1/admin/roles/" + id("g-team2") + "/implications/" + id("g-ops4"), nil,
			unimply("g-team2"), http.StatusForbidden, noIdentity},
		{"an identity admin removes an implication", "ida", "DELETE", "/v1/admin/roles/" + id("g-team2") + "/implications/" + id("g-ops4"), nil,
			unimply("g-team2"), http.StatusOK, ""},

		{"an identity admin gives a global role an access row", "ida", "POST", "/v1/admin/apps/echoapp/bindings",
			func() (string, []byte) { return jsonBody(map[string]any{"role": "g-ops2", "tools": []string{"echo"}}) },
			bind("g-ops2"), http.StatusForbidden, noApps},
		{"a server admin gives a global role an access row", "erin", "POST", "/v1/admin/apps/echoapp/bindings",
			func() (string, []byte) { return jsonBody(map[string]any{"role": "g-ops3", "tools": []string{"echo"}}) },
			bind("g-ops3"), http.StatusConflict, "g-ops3 is not a role of the server echoapp. A server admin gives access only to the roles their server owns"},
		{"a server admin of another server gives an access row on the server", "jo", "POST", "/v1/admin/apps/echoapp/bindings",
			func() (string, []byte) { return jsonBody(map[string]any{"role": "g-ops3", "tools": []string{"echo"}}) },
			bind("g-ops3"), http.StatusForbidden, serverAdmin},
		{"a server admin gives a held role of its server an access row", "erin", "POST", "/v1/admin/apps/echoapp/bindings",
			func() (string, []byte) {
				return jsonBody(map[string]any{"role": "echoapp-heldnorow", "tools": []string{"echo"}})
			},
			bind("echoapp-heldnorow"), http.StatusConflict, heldAddTools},
		{"a server admin gives a role of its server nobody holds an access row", "erin", "POST", "/v1/admin/apps/echoapp/bindings",
			func() (string, []byte) {
				return jsonBody(map[string]any{"role": "echoapp-norow", "tools": []string{"echo"}})
			},
			bind("echoapp-norow"), http.StatusCreated, ""},
		{"the global MCP admin gives a role of a server nobody holds an access row", "max", "POST", "/v1/admin/apps/echoapp/bindings",
			func() (string, []byte) {
				return jsonBody(map[string]any{"role": "echoapp-norow2", "tools": []string{"echo"}})
			},
			bind("echoapp-norow2"), http.StatusCreated, ""},

		{"a server admin takes the access row of a held role of its server", "erin", "DELETE", "/v1/admin/bindings/" + rowOf("echoapp-held"), nil,
			unbind("echoapp-held"), http.StatusConflict, heldTools},
		{"a server admin takes the access row of a global role", "erin", "DELETE", "/v1/admin/bindings/" + rowOf("g-dev"), nil,
			unbind("g-dev"), http.StatusConflict, "g-dev is not a role of the server echoapp. A server admin gives access only to the roles their server owns"},
		{"an identity admin takes an access row", "ida", "DELETE", "/v1/admin/bindings/" + rowOf("g-dev"), nil,
			unbind("g-dev"), http.StatusForbidden, noApps},
		{"a server admin takes the access row of a role of its server nobody holds", "erin", "DELETE", "/v1/admin/bindings/" + rowOf("echoapp-unbind"), nil,
			unbind("echoapp-unbind"), http.StatusOK, ""},
		{"the global MCP admin takes the access row of a global role", "max", "DELETE", "/v1/admin/bindings/" + rowOf("g-unbind"), nil,
			unbind("g-unbind"), http.StatusOK, ""},

		{"the global MCP admin applies a policy set", "max", "PUT", "/v1/admin/policies",
			func() (string, []byte) { return yamlBody(set) },
			func(drafts.World) drafts.Item {
				return drafts.Item{Kind: drafts.KindPolicySet, Name: "route-set", Op: drafts.OpOff, Doc: set}
			}, http.StatusForbidden, "session lacks scope policy:write"},
		{"root applies a policy set", "kim", "PUT", "/v1/admin/policies",
			func() (string, []byte) { return yamlBody(set) },
			func(drafts.World) drafts.Item {
				return drafts.Item{Kind: drafts.KindPolicySet, Name: "route-set", Op: drafts.OpOff, Doc: set}
			}, http.StatusCreated, ""},
	}
	callerOf := func(t *testing.T, bearer string) draftCaller {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, base+"/v1/admin/drafts", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		p, ok := app.authenticateAdmin(rec, req)
		if !ok {
			t.Fatalf("authenticate: %d %s", rec.Code, rec.Body.String())
		}
		c, ok := app.draftCallerOf(rec, req, p)
		if !ok {
			t.Fatalf("caller: %d %s", rec.Code, rec.Body.String())
		}
		return c
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w0, err := app.readWorld(ctx)
			must(err)
			d := drafts.Draft{ID: "1", Items: []drafts.Item{tc.item(w0)}}
			w, in, err := app.draftWorld(ctx, d)
			must(err)
			got := callerOf(t, bearers[tc.who]).directStandingRefusal(d, w, in)
			var ct string
			var raw []byte
			if tc.body != nil {
				ct, raw = tc.body()
			}
			code, body, _ := adminBytes(t, tc.method, base+tc.path, bearers[tc.who], ct, raw)
			var out struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(body, &out)
			if tc.says == "" {
				if got != nil {
					t.Errorf("the check refuses %s %q, and today's route passes", tc.who, got.Sentence)
				}
				if code != tc.status {
					t.Errorf("the route answered %d %s, want %d", code, body, tc.status)
				}
				return
			}
			if got == nil || refusalStatus(got.Kind) != tc.status || got.Sentence != tc.says {
				t.Errorf("the check answers %+v, want %d %q", got, tc.status, tc.says)
			}
			if code != tc.status || out.Error != tc.says {
				t.Errorf("the route answered %d %q, want %d %q", code, out.Error, tc.status, tc.says)
			}
		})
	}
}
