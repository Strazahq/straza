package server

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
	"github.com/strazahq/straza/internal/tokenscopes"
)

// TestRoleAreaScopesConfig pins the startup validation of admin.roleAreas
// for delegated admin: a bad entry must refuse construction, failing closed
// at boot, never a silently inert delegation. The vocabulary is the wat_
// token grammar verbatim, root keeps exactly one spelling (the straza-admin
// role, neither narrowable by a map entry nor re-creatable via "full"),
// and tokens:write delegation is legal but loudly advisory: it mints admin
// credentials, which is root-equivalent.
func TestRoleAreaScopesConfig(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		areas   map[string][]string
		wantErr string // substring of the error; "" = must succeed
	}{
		{"valid multi-role", map[string][]string{
			"policy-editor": {"policy:read", "policy:write"},
			"auditor":       {"audit:read", "transcripts:read"},
		}, ""},
		{"unknown area", map[string][]string{"x": {"banana:read"}}, "invalid grant"},
		{"unknown verb", map[string][]string{"x": {"policy:delete"}}, "invalid grant"},
		{"full rejected with root hint", map[string][]string{"x": {"full"}}, "straza-admin"},
		{"root role not mappable", map[string][]string{"straza-admin": {"policy:read"}}, "root"},
		{"drafting role not mappable", map[string][]string{DraftConfigRole: {"drafts:read"}},
			`admin.roleAreas must not map "straza-draft-config". It lets an agent draft through the built-in straza MCP server and opens no console area, and a map entry would make it an admin delegate`},
		{"empty grants rejected", map[string][]string{"x": {}}, "scope is required"},
		{"tokens:write allowed with advisory", map[string][]string{"minter": {"tokens:write"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			scopes, err := buildRoleAreaScopes(config.Admin{RoleAreas: tc.areas}, log)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(scopes) != len(tc.areas) {
				t.Fatalf("scopes = %d entries, want %d", len(scopes), len(tc.areas))
			}
			if tc.name == "tokens:write allowed with advisory" && !strings.Contains(buf.String(), "root-equivalent") {
				t.Errorf("tokens:write delegation logged no root-equivalent advisory: %s", buf.String())
			}
		})
	}
}

// TestDelegatedAdminRouteSweep pins the fail-closed shape across the WHOLE
// admin surface: a one-area scope must be allowed on exactly its area's
// routes (verb-matched) and denied on all others; humans reuse the same
// adminRouteArea map and allows() as wat_ tokens, so the token parity test
// keeps this sweep exhaustive when routes are added.
func TestDelegatedAdminRouteSweep(t *testing.T) {
	t.Parallel()
	ts, err := tokenscopes.Parse("policy:read,policy:write")
	if err != nil {
		t.Fatal(err)
	}
	for pattern, area := range tokenscopes.AdminRouteArea {
		method, _, ok := strings.Cut(pattern, " ")
		if !ok {
			t.Fatalf("unparseable pattern %q", pattern)
		}
		allowed, _, mapped := ts.Allows(method, pattern)
		if !mapped {
			t.Fatalf("route %q reports unmapped while present in adminRouteArea", pattern)
		}
		if want := area == "policy"; allowed != want {
			t.Errorf("scope policy:* on %q: allowed = %v, want %v", pattern, allowed, want)
		}
	}
	if allowed, _, mapped := ts.Allows("GET", "GET /v1/admin/never-registered"); allowed || mapped {
		t.Errorf("unknown route: allowed=%v mapped=%v, want denied and unmapped", allowed, mapped)
	}
}

// grantNamedRole creates (or reuses) a role by name and assigns it to the
// user, the store-level stand-in for an IdM-delivered membership.
func grantNamedRole(t *testing.T, app *App, userID, name string) {
	t.Helper()
	ctx := context.Background()
	role, err := app.store.Roles().Create(ctx, store.Role{Name: name})
	if err != nil {
		role, err = app.store.Roles().GetByName(ctx, name)
		if err != nil {
			t.Fatalf("role %s: %v", name, err)
		}
	}
	if _, err := app.store.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: userID, RoleID: role.ID,
	}); err != nil {
		t.Fatal(err)
	}
	app.resolver.Bump()
}

// TestDelegatedAdminEnforcement drives the delegated-admin human lanes
// end-to-end: a
// session whose roles map through admin.roleAreas gets exactly its areas
// (verb-enforced, union across roles), everything else answers 403 naming
// the missing grant, unmapped roles keep today's full denial, straza-admin
// stays root, and the admin audit trail names the delegated actor.
func TestDelegatedAdminEnforcement(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(cfg *config.Config) {
		cfg.Admin.RoleAreas = map[string][]string{
			"policy-editor": {"policy:read", "policy:write"},
			"auditor":       {"audit:read", "transcripts:read"},
		}
	})
	user := seedIdentity(t, app) // kim holds only unmapped roles (dev→reader)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	// Unmapped roles only: denied, and the message names the delegated lane.
	code, body, _ := adminBytes(t, "GET", base+"/v1/admin/policies", idToken, "", nil)
	if code != http.StatusForbidden {
		t.Fatalf("unmapped roles = %d, want 403 (%s)", code, body)
	}
	if !strings.Contains(string(body), "admin.roleAreas") {
		t.Errorf("403 does not name the delegated option: %s", body)
	}

	grantNamedRole(t, app, user.ID, "policy-editor")

	// In-area write and read pass; the audit event names the delegated actor.
	if code, body, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", idToken, "application/yaml", []byte(rmPolicy)); code != http.StatusCreated {
		t.Fatalf("delegated policy apply = %d: %s", code, body)
	}
	if code, _, _ := adminBytes(t, "GET", base+"/v1/admin/policies", idToken, "", nil); code != http.StatusOK {
		t.Fatalf("delegated policy list = %d", code)
	}
	data := lastAdminAction(t, app, "policy.create")
	if data["actor"] != "kim" || data["actorId"] != user.ID || data["actorVia"] != "login" {
		t.Errorf("delegated mutation attributed %v/%v/%v, want kim/%s/login",
			data["actor"], data["actorId"], data["actorVia"], user.ID)
	}

	// Out-of-area: 403 naming the missing grant; tokens stays isolated.
	code, body, _ = adminBytes(t, "GET", base+"/v1/admin/users", idToken, "", nil)
	if code != http.StatusForbidden || !strings.Contains(string(body), "identity:read") {
		t.Errorf("cross-area read = %d %q, want 403 naming identity:read", code, body)
	}
	code, body, _ = adminBytes(t, "POST", base+"/v1/admin/api-tokens", idToken, "application/json", []byte(`{"name":"x","scope":"full"}`))
	if code != http.StatusForbidden || !strings.Contains(string(body), "tokens:write") {
		t.Errorf("token mint = %d %q, want 403 naming tokens:write", code, body)
	}
	code, body, _ = adminBytes(t, "GET", base+"/v1/admin/audit", idToken, "", nil)
	if code != http.StatusForbidden || !strings.Contains(string(body), "audit:read") {
		t.Errorf("audit read = %d %q, want 403 naming audit:read", code, body)
	}

	// Union across roles: auditor adds its areas, policy keeps working.
	grantNamedRole(t, app, user.ID, "auditor")
	if code, body, _ := adminBytes(t, "GET", base+"/v1/admin/audit", idToken, "", nil); code != http.StatusOK {
		t.Fatalf("union audit read = %d: %s", code, body)
	}
	if code, _, _ := adminBytes(t, "GET", base+"/v1/admin/policies", idToken, "", nil); code != http.StatusOK {
		t.Fatalf("union policy list = %d", code)
	}

	// Session-token lane (the console's steady state after checkin).
	_, checkin := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token": idToken,
		"harness":  map[string]string{"name": "strazactl", "version": "dev"},
		"attestation": map[string]any{
			"managed": false, "hashes": map[string]string{},
		},
	})
	sessionToken, _ := checkin["session_token"].(string)
	if sessionToken == "" {
		t.Fatalf("checkin minted no session token: %v", checkin)
	}
	if code, body, _ := adminBytes(t, "GET", base+"/v1/admin/policies", sessionToken, "", nil); code != http.StatusOK {
		t.Fatalf("session-lane policy list = %d: %s", code, body)
	}
	if code, _, _ := adminBytes(t, "GET", base+"/v1/admin/users", sessionToken, "", nil); code != http.StatusForbidden {
		t.Errorf("session-lane cross-area read passed; want 403")
	}

	// Root regression: straza-admin keeps the whole surface.
	grantAdmin(t, app, user.ID)
	if code, _, _ := adminBytes(t, "GET", base+"/v1/admin/users", idToken, "", nil); code != http.StatusOK {
		t.Fatalf("root lost cross-area access")
	}
}

// TestCheckinAdminGrants pins the console's area-discovery field: the
// checkin response carries the session's effective admin standing in the
// canonical token-scope rendering: "full" for root, the sorted grant union
// for delegated roles, and NO field at all for everyone else (an agent's
// checkin never grows an admin-shaped key; openapi marks it optional).
func TestCheckinAdminGrants(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(cfg *config.Config) {
		cfg.Admin.RoleAreas = map[string][]string{
			"policy-editor": {"policy:write", "policy:read"},
		}
	})
	user := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")

	checkin := func() map[string]any {
		t.Helper()
		_, resp := postJSON(t, base+"/v1/checkin", map[string]any{
			"id_token": idToken,
			"harness":  map[string]string{"name": "strazactl", "version": "dev"},
			"attestation": map[string]any{
				"managed": false, "hashes": map[string]string{},
			},
		})
		return resp
	}

	if resp := checkin(); resp["admin_grants"] != nil {
		t.Errorf("plain user checkin carries admin_grants = %v, want absent", resp["admin_grants"])
	}

	grantNamedRole(t, app, user.ID, "policy-editor")
	if got := checkin()["admin_grants"]; got != "policy:read,policy:write" {
		t.Errorf("delegated admin_grants = %v, want canonical %q", got, "policy:read,policy:write")
	}

	grantAdmin(t, app, user.ID)
	if got := checkin()["admin_grants"]; got != "full" {
		t.Errorf("root admin_grants = %v, want %q", got, "full")
	}
}
