package server

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestOwnedRoleExportGolden pins the spec/objects revision 3 shape: a
// server-owned role exports spec.server beside its one binding, byte for
// byte as spec/objects/fixtures/role-owned.yaml.
func TestOwnedRoleExportGolden(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	if _, err := app.store.Apps().Create(ctx, store.App{Name: "demo-tools", RuntimeKind: "remote"}); err != nil {
		t.Fatal(err)
	}
	var created rolePayload
	if code := adminReq(t, "POST", base+"/v1/admin/roles", tok,
		map[string]any{"name": "demo-tools-readers", "description": "Read-only tools of demo-tools for analysts", "server": "demo-tools", "tools": []string{"echo", "add"}}, &created); code != http.StatusCreated {
		t.Fatalf("create owned role = %d (%+v)", code, created)
	}

	req, _ := http.NewRequest("GET", base+"/v1/admin/roles/"+created.ID+"/export", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export = %d", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "spec", "objects", "fixtures", "role-owned.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("export drifted from spec/objects/fixtures/role-owned.yaml:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	_ = user
}
