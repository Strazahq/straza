package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestM4Demo drives the SCIM kill-switch demo end to end: an IdM
// provisions a user over SCIM into a role-mapped group → the user enrolls and
// works through the gateway → the IdM deactivates the user over SCIM → the
// live session loses everything in well under 5 s (the kill switch). The IdM
// and the user's IdP are stood in hermetically (SCIM against the real
// endpoint; the built-in issuer as the login factor).
func TestM4Demo(t *testing.T) {
	// Serial: its 5 second budget flakes under the load of parallel servers.
	gh := startGithubMock(t)
	const ghToken = "ghp_M4-DEMO-never-client-side"

	app, base := testApp(t, func(cfg *config.Config) {
		cfg.Apps.HealthInterval = time.Hour
	})

	// Admin + SCIM token (the operator wiring the IdM connector).
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	adminBearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	scimToken := mintProvisioningToken(t, base, adminBearer)

	// --- 1. The operator births the dev role in Straza; the IdM imports
	// it as a wire-group and provisions a user into it (membership IS
	// assignment, spec/scim-profile rev 12: the IdM never creates access,
	// it only grants it). ---
	if _, err := app.store.Roles().GetByName(context.Background(), "dev"); err != nil {
		if _, err := app.store.Roles().Create(context.Background(), store.Role{Name: "dev", Kind: store.RoleKindBusiness}); err != nil {
			t.Fatal(err)
		}
	}
	uid := scimCreateUser(t, base, scimToken, `{
		"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],
		"userName":"grace","externalId":"idm-42","active":true}`)
	gid := scimGroupIDByName(t, base, scimToken, "dev")
	scimGroupPatch(t, base, scimToken, gid, `{
		"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations":[{"op":"add","path":"members","value":[{"value":"`+uid+`"}]}]}`)

	// Give the user a password so the built-in issuer can authenticate
	// them (stand-in for the enterprise IdP). sessionToken logs in with
	// this password.
	setUserPassword(t, app, uid, "hunter2!")
	app.resolver.Bump()

	// --- 2. Operator deploys the GitHub app, binds it to dev, sets policy. ---
	deployGithubApp(t, app, gh.URL, ghToken)

	// --- 3. The user enrolls and works through the gateway. ---
	tok := sessionToken(t, base, "grace")
	_, list, _ := mcpCall(t, base, tok, "tools/list", nil)
	if names := toolNamesOf(t, list); len(names) == 0 {
		t.Fatalf("provisioned user sees no tools: %v", names)
	}
	_, _, raw := mcpCall(t, base, tok, "tools/call", map[string]any{
		"name": "github__get_issue", "arguments": map[string]int{"number": 7},
	})
	if !strings.Contains(string(raw), "issue #7") {
		t.Fatalf("call failed: %s", raw)
	}

	// --- 4. The IdM deactivates the user (active:false). ---
	deactivated := time.Now()
	scimPatch(t, base, scimToken, uid, `{
		"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations":[{"op":"replace","path":"active","value":false}]}`)

	// --- 5. The session loses everything, fast. ---
	var lostAt time.Duration
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, _, _ := mcpCall(t, base, tok, "tools/list", nil)
		if code == http.StatusForbidden {
			lostAt = time.Since(deactivated)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session still valid 5s after SCIM deactivation: kill switch failed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("kill switch: session denied %s after SCIM deactivate", lostAt)
	if lostAt > 5*time.Second {
		t.Errorf("kill-switch propagation %s exceeds the 5s budget", lostAt)
	}
}

// --- demo helpers (thin wrappers over the SCIM + admin surfaces) ---

func scimReq(t *testing.T, method, base, token, path, body string) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, base+path, r)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/scim+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func scimCreateUser(t *testing.T, base, token, body string) string {
	t.Helper()
	code, out := scimReq(t, "POST", base, token, "/scim/v2/Users", body)
	if code != http.StatusCreated {
		t.Fatalf("scim create user = %d %v", code, out)
	}
	return out["id"].(string)
}

// scimGroupIDByName resolves a wire-group (an exported role) by name the
// way an IdM does: the displayName filter.
func scimGroupIDByName(t *testing.T, base, token, name string) string {
	t.Helper()
	code, out := scimReq(t, "GET", base, token, "/scim/v2/Groups?filter=displayName+eq+%22"+name+"%22", "")
	if code != http.StatusOK {
		t.Fatalf("scim group filter = %d %v", code, out)
	}
	res, _ := out["Resources"].([]any)
	if len(res) != 1 {
		t.Fatalf("scim group filter %q = %v, want one row", name, out)
	}
	return res[0].(map[string]any)["id"].(string)
}

// scimGroupPatch PATCHes a wire-group (membership ops).
func scimGroupPatch(t *testing.T, base, token, gid, body string) {
	t.Helper()
	if code, out := scimReq(t, "PATCH", base, token, "/scim/v2/Groups/"+gid, body); code != http.StatusOK {
		t.Fatalf("scim group patch = %d %v", code, out)
	}
}

func scimPatch(t *testing.T, base, token, uid, body string) {
	t.Helper()
	code, out := scimReq(t, "PATCH", base, token, "/scim/v2/Users/"+uid, body)
	if code != http.StatusOK {
		t.Fatalf("scim patch = %d %v", code, out)
	}
}

func setUserPassword(t *testing.T, app *App, uid, password string) {
	t.Helper()
	ctx := context.Background()
	u, err := app.store.Users().GetByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := testPasswordHash(password)
	if err != nil {
		t.Fatal(err)
	}
	u.PasswordHash = hash
	if _, err := app.store.Users().Update(ctx, u); err != nil {
		t.Fatal(err)
	}
}

// deployGithubApp installs the GitHub mock app, binds read-mostly tools to
// dev, sets the allow policy, and waits for running.
func deployGithubApp(t *testing.T, app *App, url, token string) {
	t.Helper()
	ctx := context.Background()
	mf, err := managerParse(t, `
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: github}
server: {name: straza.test/github, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: `+url+`}
  credential:
    kind: static
    inject: {as: header, name: Authorization, template: "Bearer {{secret}}"}
`)
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.manager.Install(ctx, mf, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := app.store.Roles().GetByName(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.broker.Set(ctx, row.ID, dev.ID, token); err != nil {
		t.Fatal(err)
	}
	app.manager.SecretUpdated(ctx, row.ID)
	if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{
		RoleID: dev.ID, AppID: row.ID, ToolMatcher: `["get_*","list_*","create_issue"]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: "github-dev", Status: "active", YAMLSource: `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: github-dev}
spec:
  match: {roles: [dev]}
  rules:
    - id: allow-readmostly
      tools: [mcp.call]
      apps: [github]
      toolNames: {allow: ["get_*", "list_*", "create_issue"]}
      effect: allow
`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if v, ok := app.manager.View("github"); ok && v.Status == "running" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("github app not running")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
