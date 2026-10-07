package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/secrets"
	"github.com/strazahq/straza/internal/store"
)

// withKeycloakProvider configures one oauth provider so oauth-kind manifests
// install in tests.
func withKeycloakProvider(c *config.Config) {
	c.OAuth.Providers = map[string]config.OAuthProvider{"keycloak": {
		ClientID: "straza", ClientSecret: "kc-client-secret-NEVER-LEAK",
		AuthURL: "http://kc/auth", TokenURL: "http://kc/token", Scopes: []string{"openid"},
	}}
}

// seedAccessRoles creates one role of every kind the secret and binding
// handlers refuse or accept: an application role, a business role and an
// approver role (the control-plane role exists from bootstrap).
func seedAccessRoles(t *testing.T, app *App) {
	t.Helper()
	ctx := context.Background()
	for _, r := range []store.Role{
		{Name: "scout-role", Kind: store.RoleKindApplication},
		{Name: "biz", Kind: store.RoleKindBusiness},
		{Name: "sec-approvers", Kind: store.RoleKindApprover},
	} {
		if _, err := app.store.Roles().Create(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
}

// TestAppSecretsAPI pins the server's own secret on the wire: set without a
// role stores the app row, a role stores its override, the kind and role
// refusals answer their pinned sentences, the listing carries fingerprints
// and never a value, the two delete routes remove one row each, and every
// write leaves one admin record with the actor.
func TestAppSecretsAPI(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, withKeycloakProvider)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	seedAccessRoles(t, app)
	up := startEchoUpstream(t)

	install := func(name, credential string) {
		t.Helper()
		mf := fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: %s}
server: {name: straza.test/%s, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
%s`, name, name, up.URL, credential)
		if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(mf), nil); code != http.StatusCreated {
			t.Fatalf("install %s = %d", name, code)
		}
	}
	install("echoapp", `
  credential:
    kind: static
    inject: {as: header, name: X-Demo-Key, template: "{{secret}}"}
`)
	install("noneapp", "")
	install("oauthapp", `
  credential:
    kind: oauth
    oauth: {provider: keycloak}
    inject: {as: header, name: Authorization, template: "Bearer {{secret}}"}
`)

	const value = "sk-demo-NEVER-LEAK-4711"
	set := func(appName string, body map[string]any) (int, map[string]any) {
		t.Helper()
		var out map[string]any
		code := adminReq(t, "POST", base+"/v1/admin/apps/"+appName+"/secrets", bearer, body, &out)
		return code, out
	}
	cases := []struct {
		name     string
		app      string
		body     map[string]any
		wantCode int
		wantErr  string
	}{
		{name: "server's own secret", app: "echoapp", body: map[string]any{"value": value}, wantCode: http.StatusCreated},
		{name: "application role override", app: "echoapp", body: map[string]any{"value": value + "-role", "role": "scout-role"}, wantCode: http.StatusCreated},
		{name: "business role refused", app: "echoapp", body: map[string]any{"value": value, "role": "biz"}, wantCode: http.StatusBadRequest,
			wantErr: "business role: it composes application roles and reaches tools through them. Set the secret for an application role instead, or for the server itself."},
		{name: "approver role refused", app: "echoapp", body: map[string]any{"value": value, "role": "sec-approvers"}, wantCode: http.StatusBadRequest,
			wantErr: "approver role: it decides approval requests and cannot hold a secret"},
		{name: "control-plane role refused", app: "echoapp", body: map[string]any{"value": value, "role": "straza-admin"}, wantCode: http.StatusBadRequest,
			wantErr: "Straza role: it governs Straza itself and cannot hold a secret"},
		{name: "unknown role", app: "echoapp", body: map[string]any{"value": value, "role": "scout-rol"}, wantCode: http.StatusNotFound,
			wantErr: `role "scout-rol" does not exist. Pick one from strazactl roles list, or create one on its MCP server with strazactl roles create <server>-<word> --app <server> --tools <tool,...>.`},
		{name: "kind none refused", app: "noneapp", body: map[string]any{"value": value}, wantCode: http.StatusBadRequest,
			wantErr: "the MCP server noneapp declares no credential (credential.kind none), so a secret would never be used. Change the manifest's credential block first."},
		{name: "kind oauth refused", app: "oauthapp", body: map[string]any{"value": value}, wantCode: http.StatusBadRequest,
			wantErr: "the MCP server oauthapp uses each caller's own sign-in (credential.kind oauth) and lets no agent use a shared account (credential.agents own), so a static secret would never be used. Set credential.agents: shared in the manifest first."},
		{name: "value required", app: "echoapp", body: map[string]any{"role": "scout-role"}, wantCode: http.StatusBadRequest, wantErr: "value is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := set(tc.app, tc.body)
			if code != tc.wantCode {
				t.Fatalf("code = %d %v, want %d", code, out, tc.wantCode)
			}
			if tc.wantErr != "" && out["error"] != tc.wantErr {
				t.Errorf("error = %q\nwant %q", out["error"], tc.wantErr)
			}
			if code == http.StatusCreated {
				wantScope, wantRole := "app", ""
				if r, _ := tc.body["role"].(string); r != "" {
					wantScope, wantRole = "role", r
				}
				if out["scope"] != wantScope || out["role"] != wantRole || out["fingerprint"] != secrets.Fingerprint(tc.body["value"].(string)) {
					t.Errorf("response = %v, want scope %s role %q with the fingerprint", out, wantScope, wantRole)
				}
			}
			if raw, _ := json.Marshal(out); strings.Contains(string(raw), value) {
				t.Fatal("SECRET LEAKED into the response")
			}
		})
	}

	// The apps list carries the manifest as the stored JSON object and the
	// remote URL, and the manifest never carries the secret.
	var apps []appPayload
	if code := adminReq(t, "GET", base+"/v1/admin/apps", bearer, nil, &apps); code != http.StatusOK {
		t.Fatalf("list apps = %d", code)
	}
	for _, p := range apps {
		if p.Name != "echoapp" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(p.Manifest, &obj); err != nil || obj["kind"] != "App" || obj["straza"] == nil {
			t.Errorf("echoapp manifest = %s (%v), want the app.yaml object", p.Manifest, err)
		}
		if p.URL != up.URL || !strings.Contains(string(p.Manifest), "X-Demo-Key") {
			t.Errorf("echoapp row = url %q manifest %s", p.URL, p.Manifest)
		}
		if strings.Contains(string(p.Manifest), "NEVER-LEAK") {
			t.Fatal("SECRET LEAKED into the manifest object")
		}
	}

	var listed []secretPayload
	if code := adminReq(t, "GET", base+"/v1/admin/apps/echoapp/secrets", bearer, nil, &listed); code != http.StatusOK {
		t.Fatalf("list secrets = %d", code)
	}
	if len(listed) != 2 || listed[0].Scope != "app" || listed[0].Fingerprint != secrets.Fingerprint(value) ||
		listed[1].Scope != "role" || listed[1].Role != "scout-role" || listed[1].Kind != "static" || listed[1].SetAt == "" {
		t.Errorf("listing = %+v", listed)
	}
	if raw, _ := json.Marshal(listed); strings.Contains(string(raw), "NEVER-LEAK") {
		t.Fatal("SECRET LEAKED into the listing")
	}

	var out map[string]any
	if code := adminReq(t, "DELETE", base+"/v1/admin/apps/echoapp/secrets/scout-role", bearer, nil, &out); code != http.StatusOK || out["status"] != "deleted" {
		t.Errorf("delete role secret = %d %v", code, out)
	}
	if code := adminReq(t, "DELETE", base+"/v1/admin/apps/echoapp/secrets/scout-role", bearer, nil, &out); code != http.StatusNotFound ||
		out["error"] != "no secret is stored for the MCP server echoapp (role scout-role)" {
		t.Errorf("delete absent role secret = %d %v", code, out)
	}
	if code := adminReq(t, "DELETE", base+"/v1/admin/apps/echoapp/secrets/scout-rol", bearer, nil, &out); code != http.StatusNotFound ||
		!strings.HasPrefix(out["error"].(string), `role "scout-rol" does not exist.`) {
		t.Errorf("delete secret of unknown role = %d %v", code, out)
	}
	if code := adminReq(t, "DELETE", base+"/v1/admin/apps/echoapp/secrets", bearer, nil, &out); code != http.StatusOK {
		t.Errorf("delete app secret = %d %v", code, out)
	}
	if code := adminReq(t, "DELETE", base+"/v1/admin/apps/echoapp/secrets", bearer, nil, &out); code != http.StatusNotFound ||
		out["error"] != "no secret is stored for the MCP server echoapp (the server's own secret)" {
		t.Errorf("delete absent app secret = %d %v", code, out)
	}
	listed = nil
	if code := adminReq(t, "GET", base+"/v1/admin/apps/echoapp/secrets", bearer, nil, &listed); code != http.StatusOK || len(listed) != 0 {
		t.Errorf("listing after deletes = %d %+v", code, listed)
	}

	// One admin record per write, with the actor; the value in none of them.
	sets, removes := 0, 0
	for _, ev := range adminAuditEvents(t, app) {
		if raw, _ := json.Marshal(ev); strings.Contains(string(raw), "NEVER-LEAK") {
			t.Fatal("SECRET LEAKED into an admin record")
		}
		switch ev["action"] {
		case "apps.secret.set":
			sets++
		case "apps.secret.remove":
			removes++
			if ev["actor"] != "kim" || ev["app"] != "echoapp" {
				t.Errorf("secret.remove record = %v", ev)
			}
		}
	}
	if sets != 2 || removes != 2 {
		t.Errorf("secret records: set %d remove %d, want 2 and 2", sets, removes)
	}
}
