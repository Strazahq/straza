package server

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

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
