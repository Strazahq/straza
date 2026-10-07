package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestRoleKindIsFixedAtCreate pins that a role update never changes the kind:
// every kind rule (access rows on application roles only, policy match on
// application roles only, approver roles standing alone) is judged against
// the kind a role was created with, so a kind that moved afterwards would
// carry rows the server refuses to create directly.
func TestRoleKindIsFixedAtCreate(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	kinds := []string{"business", "application", "approver", "straza"}
	ids := map[string]string{}
	for _, kind := range kinds {
		var created struct {
			ID string `json:"id"`
		}
		if code := adminReq(t, "POST", base+"/v1/admin/roles", tok,
			map[string]string{"name": "kindfix-" + kind, "kind": kind}, &created); code != http.StatusCreated {
			t.Fatalf("create %s role = %d", kind, code)
		}
		ids[kind] = created.ID
	}

	for _, from := range kinds {
		for _, to := range kinds {
			t.Run(from+" to "+to, func(t *testing.T) {
				var out struct {
					Kind  string `json:"kind"`
					Error string `json:"error"`
				}
				code := adminReq(t, "PATCH", base+"/v1/admin/roles/"+ids[from], tok, map[string]string{"kind": to}, &out)
				if from == to {
					if code != http.StatusOK || out.Kind != from {
						t.Fatalf("restating the kind = %d (%+v), want 200 and %s", code, out, from)
					}
					return
				}
				if code != http.StatusBadRequest {
					t.Fatalf("PATCH kind = %d (%+v), want 400", code, out)
				}
				for _, want := range []string{"fixed at create", "Create a new role"} {
					if !strings.Contains(out.Error, want) {
						t.Errorf("error = %q, want it to say %q", out.Error, want)
					}
				}
				stored, err := app.store.Roles().GetByID(ctx, ids[from])
				if err != nil || wireRoleKind(stored) != from {
					t.Errorf("stored kind = %q, %v; want %s kept", wireRoleKind(stored), err, from)
				}
			})
		}
	}

	t.Run("an application role with an access row stays an application role", func(t *testing.T) {
		demo, err := app.store.Apps().Create(ctx, store.App{Name: "kindfix-tools", RuntimeKind: "remote"})
		if err != nil {
			t.Fatal(err)
		}
		// A global role's row is a store fixture: only a role a server owns
		// gains a new row through the API.
		if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: ids["application"], AppID: demo.ID, ToolMatcher: `["echo"]`}); err != nil {
			t.Fatal(err)
		}
		if code := adminReq(t, "PATCH", base+"/v1/admin/roles/"+ids["application"], tok,
			map[string]string{"kind": "business"}, nil); code != http.StatusBadRequest {
			t.Fatalf("PATCH kind on a role with an access row = %d, want 400", code)
		}
	})
}
