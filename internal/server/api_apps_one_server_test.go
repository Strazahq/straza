package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestOneServerPerRole pins the one-server rule on POST
// /v1/admin/apps/{id}/bindings for a global application role whose row
// predates server-owned roles: a second server answers 409 with the
// sentence naming the first server and the way out, the same server again
// keeps the route-to-edit answer, the refused creates write no record, and
// once the row is gone the role reaches no server again, because a role
// that gains a row belongs to that server.
func TestOneServerPerRole(t *testing.T) {
	t.Parallel()
	rig := newOwnedRig(t)
	ctx := context.Background()
	if code, msg := rig.call(t, "POST", "/v1/admin/roles", rig.root, map[string]any{"name": "dev-tools", "kind": "application"}); code != http.StatusCreated {
		t.Fatalf("dev-tools = %d %q", code, msg)
	}
	role, err := rig.app.store.Roles().GetByName(ctx, "dev-tools")
	if err != nil {
		t.Fatal(err)
	}
	echoapp, err := rig.app.store.Apps().GetByName(ctx, "echoapp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rig.app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: role.ID, AppID: echoapp.ID, ToolMatcher: `["echo"]`}); err != nil {
		t.Fatal(err)
	}
	bind := func(app string) (int, string) {
		return rig.call(t, "POST", "/v1/admin/apps/"+app+"/bindings", rig.root, map[string]any{"role": "dev-tools", "tools": []string{"echo"}})
	}
	cases := []struct {
		name, app string
		want      int
		err       string
	}{
		{"a second server is refused with the way out", "otherapp", http.StatusConflict,
			"dev-tools already reaches echoapp. An application role reaches one server: make a role for otherapp and compose both from a business role."},
		{"the same server again routes to edit", "echoapp", http.StatusConflict,
			"this role already has an access row on this server: edit its tools instead of creating a second one"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code, msg := bind(tc.app); code != tc.want || msg != tc.err {
				t.Errorf("bind on %s = %d %q, want %d %q", tc.app, code, msg, tc.want, tc.err)
			}
		})
	}
	if n, _ := countRecords(t, rig.app, "apps.binding.create", "dev-tools"); n != 0 {
		t.Errorf("apps.binding.create records for dev-tools = %d, want none", n)
	}
	first := rig.bindingOf(t, "dev-tools", "echoapp")
	if code, msg := rig.call(t, "DELETE", "/v1/admin/bindings/"+first, rig.root, nil); code != http.StatusOK {
		t.Fatalf("remove the first row = %d %q, want 200", code, msg)
	}
	want := "dev-tools belongs to no MCP server, so it cannot be given access to otherapp. A role that reaches a server belongs to that server, so the identity manager and the console can name the server it reaches. " +
		"Create a role of otherapp with strazactl roles create otherapp-<word> --app otherapp --tools <tool,...>, and compose it and dev-tools into a business role."
	if code, msg := bind("otherapp"); code != http.StatusConflict || msg != want {
		t.Errorf("the other server once the row is gone = %d %q, want 409 %q", code, msg, want)
	}
}
