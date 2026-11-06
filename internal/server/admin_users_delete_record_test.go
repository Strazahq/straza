package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/strazahq/straza/internal/store"
)

// TestUserDeleteRecord pins the straza.audit.admin record of an admin user
// delete: one user.delete with the actor, the user as target, its username
// and the reason when the delete removes the user, also for a user with no
// grant, device or key and for a delete whose commit lost its answer. A 404,
// the refused break-glass delete and a write the database refused write
// none, and a second delete of the same user writes no second one.
func TestUserDeleteRecord(t *testing.T) {
	t.Parallel()
	app, base, fs := testAppFault(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	admin, _ := checkinToken(t, app, base)
	lostConn := &pgconn.PgError{Severity: "FATAL", Code: "08006", Message: "connection failure during the commit"}
	records := func(id string) []map[string]any {
		var out []map[string]any
		for _, ev := range adminAuditEvents(t, app) {
			if ev["action"] == actionUserDelete && ev["target"] == id {
				out = append(out, ev)
			}
		}
		return out
	}
	del := func(id string) int {
		code, _, _ := callJSON(t, http.MethodDelete, base+"/v1/admin/users/"+id, admin, nil)
		return code
	}
	made := 0
	user := func(t *testing.T) store.User {
		t.Helper()
		made++
		u, err := app.store.Users().Create(context.Background(), store.User{Username: fmt.Sprintf("b180-%d", made)})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	breakGlass, err := app.store.Users().GetByUsername(context.Background(), BreakGlassUsername)
	if errors.Is(err, store.ErrNotFound) {
		breakGlass, err = app.store.Users().Create(context.Background(), store.User{Username: BreakGlassUsername})
	}
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		arm    string
		err    error
		target func(t *testing.T) store.User
		status int
		want   int
	}{
		{"an unknown user", "", nil, func(*testing.T) store.User { return store.User{ID: "no-such-user"} }, http.StatusNotFound, 0},
		{"the break-glass admin", "", nil, func(*testing.T) store.User { return breakGlass }, http.StatusForbidden, 0},
		{"a write the database refused", "users.write", pgDown, user, http.StatusInternalServerError, 0},
		{"a commit whose answer was lost", "users.write.lost", lostConn, user, http.StatusOK, 1},
		{"a user with no grant, device or key", "", nil, user, http.StatusOK, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := tc.target(t)
			if tc.arm != "" {
				fs.arm(tc.arm, tc.err)
			}
			code := del(u.ID)
			fs.disarm()
			if code != tc.status {
				t.Fatalf("the delete = %d, want %d", code, tc.status)
			}
			got := records(u.ID)
			if len(got) != tc.want {
				t.Fatalf("%d user.delete records, want %d: %v", len(got), tc.want, got)
			}
			if tc.want == 0 {
				return
			}
			for k, v := range map[string]any{"actor": "kim", "actorId": kim.ID, "actorVia": "session",
				"target": u.ID, "user": u.ID, "username": u.Username, "reason": "user deleted by admin"} {
				if got[0][k] != v {
					t.Errorf("the record's %s = %v, want %v", k, got[0][k], v)
				}
			}
			if code := del(u.ID); code != http.StatusNotFound {
				t.Errorf("a second delete = %d, want 404", code)
			}
			if n := len(records(u.ID)); n != 1 {
				t.Errorf("%d user.delete records after a second delete, want 1", n)
			}
		})
	}

	t.Run("the record comes before the cascade's records", func(t *testing.T) {
		u := user(t)
		grantAdmin(t, app, u.ID)
		storedSession(t, app, u.ID, "", "claude-code")
		if code := del(u.ID); code != http.StatusOK {
			t.Fatalf("the delete = %d", code)
		}
		rows, err := app.store.Outbox().ListRecent(context.Background(), 1000)
		if err != nil {
			t.Fatal(err)
		}
		// ListRecent answers newest first, so a larger index is older.
		at := map[string]int{}
		for i, row := range rows {
			var ce struct {
				Data map[string]any `json:"data"`
			}
			if json.Unmarshal([]byte(row.CE), &ce) != nil || (ce.Data["user"] != u.ID && ce.Data["target"] != u.ID) {
				continue
			}
			if action, _ := ce.Data["action"].(string); action != "" {
				at[action] = i
			}
		}
		for _, cascade := range []string{actionRolesUnassign, "user.killed"} {
			first, ok1 := at[actionUserDelete]
			later, ok2 := at[cascade]
			if !ok1 || !ok2 || first <= later {
				t.Errorf("user.delete at %d (%v) and %s at %d (%v), want user.delete written first", first, ok1, cascade, later, ok2)
			}
		}
	})
}
