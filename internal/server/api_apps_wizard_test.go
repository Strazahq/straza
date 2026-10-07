package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/wire"
)

// TestAppsWizardAPI pins the server additions the wizard rides: dry-run
// validation, the install refusal for an unconfigured oauth provider, the
// install and remove admin records, reached_by on the list, the business,
// unknown and unowned role refusals on bind, the binding delete record, the stopped
// recheck sentence, the provider list, the runtimes on /version, the
// grant-only report, and removal that frees the name.
func TestAppsWizardAPI(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, withKeycloakProvider)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	seedAccessRoles(t, app)
	up := startEchoUpstream(t)
	ctx := context.Background()

	// Dry run validates and installs nothing.
	var dry map[string]any
	if code := rawReq(t, "POST", base+"/v1/admin/apps?dryRun=1", bearer, "application/yaml", []byte(echoManifest(up.URL)), &dry); code != http.StatusOK {
		t.Fatalf("dry run = %d %v", code, dry)
	}
	if dry["name"] != "echoapp" || dry["runtime"] != "remote" || dry["credential"] != "none" {
		t.Errorf("dry run = %v", dry)
	}
	var bad map[string]any
	if code := rawReq(t, "POST", base+"/v1/admin/apps?dryRun=1", bearer, "application/yaml", []byte("kind: Nope"), &bad); code != http.StatusUnprocessableEntity || bad["error"] == "" {
		t.Errorf("dry run of an invalid manifest = %d %v", code, bad)
	}
	var apps []appPayload
	if code := adminReq(t, "GET", base+"/v1/admin/apps", bearer, nil, &apps); code != http.StatusOK || len(apps) != 0 {
		t.Fatalf("apps after dry run = %d %+v, want none", code, apps)
	}

	// An oauth manifest naming a provider this server lacks never installs.
	oauth := func(provider string) string {
		return strings.Replace(echoManifest(up.URL), "metadata: {name: echoapp}", "metadata: {name: oauth-"+provider+"}", 1) + `
  credential:
    kind: oauth
    oauth: {provider: ` + provider + `}
    inject: {as: header, name: Authorization, template: "Bearer {{secret}}"}
`
	}
	var refused map[string]any
	if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(oauth("github")), &refused); code != http.StatusUnprocessableEntity ||
		refused["error"] != `credential.oauth.provider "github" is not configured on this server. Add oauth.providers.github to the strazad config, or pick one of: keycloak.` {
		t.Errorf("install with an unknown provider = %d %v", code, refused)
	}
	if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(oauth("keycloak")), nil); code != http.StatusCreated {
		t.Errorf("install with the configured provider = %d", code)
	}
	if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(echoManifest(up.URL)), nil); code != http.StatusCreated {
		t.Fatalf("install = %d", code)
	}
	waitAppStatus(t, base, bearer, "echoapp", "running")
	installs := 0
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == "apps.install" && ev["app"] == "echoapp" {
			installs++
			if ev["actor"] != "kim" || ev["runtime"] != "remote" {
				t.Errorf("apps.install record = %v", ev)
			}
		}
	}
	if installs != 1 {
		t.Errorf("apps.install records for echoapp = %d, want exactly one", installs)
	}

	// Bind: roles the server owns only, unknown roles named. The owned role
	// is a store fixture without a row, so the route adds its first.
	if _, err := app.store.Roles().Create(ctx, store.Role{Name: "echoapp-scout", Kind: store.RoleKindApplication,
		OwnerAppID: mustApp(t, app, "echoapp").ID}); err != nil {
		t.Fatal(err)
	}
	bind := func(role string) (int, map[string]any) {
		t.Helper()
		var out map[string]any
		code := adminReq(t, "POST", base+"/v1/admin/apps/echoapp/bindings", bearer, map[string]any{"role": role, "tools": []string{"echo"}}, &out)
		return code, out
	}
	if code, out := bind("biz"); code != http.StatusBadRequest ||
		out["error"] != "business role: it composes application roles and reaches tools through them. Give an application role access instead." {
		t.Errorf("bind business role = %d %v", code, out)
	}
	if code, out := bind("scout-rol"); code != http.StatusNotFound ||
		out["error"] != `role "scout-rol" does not exist. Pick one from strazactl roles list, or create one on its MCP server with strazactl roles create <server>-<word> --app <server> --tools <tool,...>.` {
		t.Errorf("bind unknown role = %d %v", code, out)
	}
	if code, out := bind("scout-role"); code != http.StatusConflict ||
		out["error"] != "scout-role belongs to no MCP server, so it cannot be given access to echoapp. A role that reaches a server belongs to that server, so the identity manager and the console can name the server it reaches. "+
			"Create a role of echoapp with strazactl roles create echoapp-<word> --app echoapp --tools <tool,...>, and compose it and scout-role into a business role." {
		t.Errorf("bind a role of no server = %d %v", code, out)
	}
	code, bound := bind("echoapp-scout")
	if code != http.StatusCreated {
		t.Fatalf("bind = %d %v", code, bound)
	}
	bindingID, _ := bound["id"].(string)

	// reached_by names the roles with access; empty for the others.
	apps = nil
	if code := adminReq(t, "GET", base+"/v1/admin/apps", bearer, nil, &apps); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	for _, p := range apps {
		want := []string{}
		if p.Name == "echoapp" {
			want = []string{"echoapp-scout"}
		}
		if strings.Join(p.ReachedBy, ",") != strings.Join(want, ",") {
			t.Errorf("%s reached_by = %v, want %v", p.Name, p.ReachedBy, want)
		}
	}

	// The grant-only report lists the bound tool no rule names.
	var report struct {
		Rows []grantOnlyRow `json:"rows"`
	}
	if code := adminReq(t, "GET", base+"/v1/admin/access/grant-only", bearer, nil, &report); code != http.StatusOK {
		t.Fatalf("grant-only = %d", code)
	}
	if len(report.Rows) != 1 || report.Rows[0].Role != "echoapp-scout" || report.Rows[0].App != "echoapp" ||
		report.Rows[0].BindingID != bindingID || strings.Join(report.Rows[0].Tools, ",") != "echo" {
		t.Errorf("grant-only rows = %+v", report.Rows)
	}

	// Binding delete leaves an admin record with the actor and the grant.
	if code := adminReq(t, "DELETE", base+"/v1/admin/bindings/"+bindingID, bearer, nil, nil); code != http.StatusOK {
		t.Fatalf("delete binding = %d", code)
	}
	deletes := 0
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == "apps.binding.delete" {
			deletes++
			tools, _ := ev["tools"].([]any)
			if ev["actor"] != "kim" || ev["app"] != "echoapp" || ev["role"] != "echoapp-scout" || ev["bindingId"] != bindingID || len(tools) != 1 {
				t.Errorf("apps.binding.delete record = %v", ev)
			}
		}
	}
	if deletes != 1 {
		t.Errorf("apps.binding.delete records = %d, want exactly one", deletes)
	}

	// A stopped app's recheck names the fix.
	if code := adminReq(t, "POST", base+"/v1/admin/apps/echoapp/disable", bearer, nil, nil); code != http.StatusOK {
		t.Fatalf("disable = %d", code)
	}
	var conflict map[string]any
	if code := adminReq(t, "POST", base+"/v1/admin/apps/echoapp/health", bearer, nil, &conflict); code != http.StatusConflict ||
		conflict["error"] != "the MCP server echoapp is stopped. Enable it with strazactl apps enable echoapp, then recheck." {
		t.Errorf("recheck stopped = %d %v", code, conflict)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/apps/echoapp/enable", bearer, nil, nil); code != http.StatusOK {
		t.Fatalf("enable = %d", code)
	}

	// Providers: names, scopes and the redirect URI, never the client secret.
	var providers []oauthProviderPayload
	if code := adminReq(t, "GET", base+"/v1/admin/oauth/providers", bearer, nil, &providers); code != http.StatusOK {
		t.Fatalf("providers = %d", code)
	}
	if len(providers) != 1 || providers[0].Name != "keycloak" || !strings.HasSuffix(providers[0].RedirectURI, "/v1/connect/callback") ||
		strings.Join(providers[0].Scopes, ",") != "openid" {
		t.Errorf("providers = %+v", providers)
	}
	if raw, _ := json.Marshal(providers); strings.Contains(string(raw), "kc-client-secret") {
		t.Fatal("CLIENT SECRET LEAKED into the provider list")
	}

	// Import converts a registry record server-side and installs nothing.
	var imported map[string]string
	if code := rawReq(t, "POST", base+"/v1/admin/apps/import", bearer, "application/json", []byte(`{
		"name": "io.github.example/demo", "version": "1.0.0",
		"remotes": [{"type": "streamable-http", "url": "http://demo:3001/mcp"}]
	}`), &imported); code != http.StatusOK || imported["runtime"] != "remote" || imported["name"] == "" ||
		!strings.Contains(imported["manifest"], "kind: App") {
		t.Errorf("import = %d %v", code, imported)
	}
	var importErr map[string]any
	if code := rawReq(t, "POST", base+"/v1/admin/apps/import", bearer, "application/json", []byte(`{"name": "x"}`), &importErr); code != http.StatusUnprocessableEntity || importErr["error"] == "" {
		t.Errorf("import of an unusable record = %d %v", code, importErr)
	}

	// /version says which runtimes this host can run.
	var v wire.VersionStatus
	if code := adminReq(t, "GET", base+"/version", "", nil, &v); code != http.StatusOK {
		t.Fatalf("version = %d", code)
	}
	wantRuntimes := "remote,command"
	if manager.DockerOnPath() {
		wantRuntimes += ",oci"
	}
	if strings.Join(v.Runtimes, ",") != wantRuntimes {
		t.Errorf("runtimes = %v, want %s", v.Runtimes, wantRuntimes)
	}

	// Remove frees the name, takes the access rows along and records who.
	if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{
		RoleID: mustRole(t, app, "scout-role").ID, AppID: mustApp(t, app, "echoapp").ID, ToolMatcher: `["*"]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	var removed map[string]any
	if code := adminReq(t, "DELETE", base+"/v1/admin/apps/echoapp", bearer, nil, &removed); code != http.StatusOK || removed["status"] != "removed" {
		t.Fatalf("remove = %d %v", code, removed)
	}
	rec := lastAdminAction(t, app, "apps.remove")
	roles, _ := rec["roles"].([]any)
	if rec["actor"] != "kim" || rec["app"] != "echoapp" || len(roles) != 1 || roles[0] != "scout-role" {
		t.Errorf("apps.remove record = %v", rec)
	}
	if code := adminReq(t, "DELETE", base+"/v1/admin/apps/echoapp", bearer, nil, nil); code != http.StatusNotFound {
		t.Errorf("second remove = %d, want 404", code)
	}
	apps = nil
	if code := adminReq(t, "GET", base+"/v1/admin/apps", bearer, nil, &apps); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	for _, p := range apps {
		if p.Name == "echoapp" {
			t.Errorf("removed app still listed: %+v", p)
		}
	}
	if bs, _ := app.gateway.bindings.Load().([]gwBinding); len(bs) != 0 {
		t.Errorf("gateway bindings after remove = %+v, want none", bs)
	}
	if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(echoManifest(up.URL)), nil); code != http.StatusCreated {
		t.Errorf("reinstall after remove = %d", code)
	}
	waitAppStatus(t, base, bearer, "echoapp", "running")
}

func mustRole(t *testing.T, app *App, name string) store.Role {
	t.Helper()
	role, err := app.store.Roles().GetByName(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return role
}

func mustApp(t *testing.T, app *App, name string) store.App {
	t.Helper()
	row, err := app.store.Apps().GetByName(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return row
}
