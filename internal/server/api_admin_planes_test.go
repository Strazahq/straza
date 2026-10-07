package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestStrazaKindWireAndGuards pins the control plane's wire contract:
// kind "straza" round-trips, the plane is fixed at create (no
// straza↔access PATCH flips), control-plane roles can bind neither tools nor
// packs, and the straza- name prefix is refused at create (reserved product
// namespace). The product roles themselves are refused at delete. The
// bootstrap straza-admin role lands control-plane on first boot.
func TestStrazaKindWireAndGuards(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	admin, err := app.store.Roles().GetByName(ctx, AdminRole)
	if err != nil {
		t.Fatal(err)
	}
	if admin.Plane != store.RolePlaneControl {
		t.Fatalf("bootstrap straza-admin plane = %q, want control", admin.Plane)
	}

	var created struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/roles", tok,
		map[string]string{"name": "auditor", "kind": "straza", "description": "read-only oversight"}, &created); code != http.StatusCreated {
		t.Fatalf("create straza-kind role = %d", code)
	}
	if created.Kind != "straza" {
		t.Fatalf("created kind = %q, want straza", created.Kind)
	}

	// The reserved product namespace is refused at create, byte-exact.
	var refusal struct {
		Error string `json:"error"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/roles", tok,
		map[string]string{"name": "straza-ops"}, &refusal); code != http.StatusBadRequest {
		t.Fatalf("straza-* create = %d, want 400", code)
	}
	if want := "role names beginning with straza- are reserved for product-defined roles; choose a name without the straza- prefix"; refusal.Error != want {
		t.Fatalf("straza-* refusal = %q, want %q", refusal.Error, want)
	}

	// The reserved product roles are refused at delete too, byte-exact: a
	// DELETE would cascade their assignments and take console access or
	// self-enrollment away from everyone holding them.
	for _, name := range []string{AdminRole, EnrollMobileRole} {
		ro, err := app.store.Roles().GetByName(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if code := adminReq(t, "DELETE", base+"/v1/admin/roles/"+ro.ID, tok, nil, &refusal); code != http.StatusConflict {
			t.Fatalf("delete %s = %d, want 409", name, code)
		}
		want := fmt.Sprintf("role %q comes with the product and cannot be deleted. To take someone's access away, remove their assignment instead", name)
		if refusal.Error != want {
			t.Fatalf("delete refusal for %s = %q, want %q", name, refusal.Error, want)
		}
		var roles []struct {
			Name string `json:"name"`
		}
		if code := adminReq(t, "GET", base+"/v1/admin/roles", tok, nil, &roles); code != http.StatusOK {
			t.Fatalf("roles list after refused delete = %d", code)
		}
		found := false
		for _, row := range roles {
			if row.Name == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("role %q is gone from the roles list after the refused delete", name)
		}
	}

	var out map[string]any
	if code := adminReq(t, "PATCH", base+"/v1/admin/roles/"+created.ID, tok,
		map[string]string{"kind": "business"}, &out); code != http.StatusBadRequest {
		t.Errorf("straza→business PATCH = %d (%v), want 400", code, out)
	}
	dev, err := app.store.Roles().GetByName(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if code := adminReq(t, "PATCH", base+"/v1/admin/roles/"+dev.ID, tok,
		map[string]string{"kind": "straza"}, &out); code != http.StatusBadRequest {
		t.Errorf("business→straza PATCH = %d (%v), want 400", code, out)
	}
	var patched struct {
		Kind        string `json:"kind"`
		Description string `json:"description"`
	}
	if code := adminReq(t, "PATCH", base+"/v1/admin/roles/"+created.ID, tok,
		map[string]string{"description": "the SOC2 seat"}, &patched); code != http.StatusOK {
		t.Fatalf("straza-kind description PATCH = %d", code)
	}
	if patched.Kind != "straza" || patched.Description != "the SOC2 seat" {
		t.Errorf("patched = %+v, want kind straza kept", patched)
	}

	demoApp, err := app.store.Apps().Create(ctx, store.App{Name: "demo-tools", RuntimeKind: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/apps/"+demoApp.ID+"/bindings", tok,
		map[string]any{"role": "auditor", "tools": []string{"echo"}}, &out); code != http.StatusBadRequest {
		t.Errorf("tool binding onto straza-kind role = %d (%v), want 400", code, out)
	}
	pack, err := app.store.Packs().Create(ctx, store.KnowledgePack{Name: "p", Version: "1", Content: "c", Checksum: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/packs/"+pack.ID+"/bindings", tok,
		map[string]string{"role_id": created.ID}, &out); code != http.StatusBadRequest {
		t.Errorf("pack binding onto straza-kind role = %d (%v), want 400", code, out)
	}
}

// TestBindingDuplicateRoutesToEdit pins the (role, app) uniqueness answer:
// the 409 names the existing binding so the console can
// route into editing its scope.
func TestBindingDuplicateRoutesToEdit(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	demoApp, err := app.store.Apps().Create(ctx, store.App{Name: "demo-tools", RuntimeKind: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := app.store.Roles().GetByName(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	// The global role's row is a store fixture: only a role a server owns
	// gains a new row through the API.
	first, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: dev.ID, AppID: demoApp.ID, ToolMatcher: `["echo"]`})
	if err != nil {
		t.Fatal(err)
	}
	var dup struct {
		Error     string `json:"error"`
		BindingID string `json:"binding_id"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/apps/"+demoApp.ID+"/bindings", tok,
		map[string]any{"role": "dev", "tools": []string{"add"}}, &dup); code != http.StatusConflict {
		t.Fatalf("duplicate binding = %d, want 409", code)
	}
	if dup.BindingID != first.ID {
		t.Errorf("409 binding_id = %q, want the existing row %q", dup.BindingID, first.ID)
	}
}

// TestRoleExportGolden pins the canonical Role document (spec/objects)
// byte-for-byte against the checked-in fixture: name-keyed, id-free, every
// list sorted, so re-exports are VCS-stable. The fixture is round-verified
// by this test on every run (its source is the running serializer, not
// hand-derivation).
func TestRoleExportGolden(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	dev, err := app.store.Roles().GetByName(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	demoApp, err := app.store.Apps().Create(ctx, store.App{Name: "demo-tools", RuntimeKind: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: dev.ID, AppID: demoApp.ID, ToolMatcher: `["add","echo"]`}); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	// seedIdentity already ships the golang-style pack (bound to reader);
	// bind it to dev too so the export carries a packs section.
	packs, err := app.store.Packs().List(ctx)
	if err != nil || len(packs) == 0 {
		t.Fatalf("seed pack missing: %v", err)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/packs/"+packs[0].ID+"/bindings", tok,
		map[string]string{"role_id": dev.ID}, &out); code != http.StatusCreated {
		t.Fatalf("pack bind = %d (%v)", code, out)
	}
	if code := adminReq(t, "PATCH", base+"/v1/admin/roles/"+dev.ID, tok,
		map[string]string{"description": "Developer seat: governed demo-tools access"}, &out); code != http.StatusOK {
		t.Fatalf("describe = %d (%v)", code, out)
	}

	req, _ := http.NewRequest("GET", base+"/v1/admin/roles/"+dev.ID+"/export", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("content-type = %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="role-dev.yaml"` {
		t.Errorf("content-disposition = %q", cd)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "spec", "objects", "fixtures", "role-dev.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("export drifted from spec/objects/fixtures/role-dev.yaml:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	_ = user
}
