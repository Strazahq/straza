package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestAppAdminRoleName pins the derivation of a minted admin role's name:
// the reserved prefix, the namespace before the last slash, the short name
// after it, and every character outside a-z, 0-9 and the hyphen folded to
// one hyphen, so a registry name reads as a role name.
func TestAppAdminRoleName(t *testing.T) {
	cases := []struct{ app, want string }{
		{"jira", "mcp-admin-jira"},
		{"finance/jira", "mcp-admin-finance-jira"},
		{"io.github.acme/My Server", "mcp-admin-io-github-acme-my-server"},
		{"Demo_Tools", "mcp-admin-demo-tools"},
		{"--odd--/--name--", "mcp-admin-odd-name"},
		{"a/b/c", "mcp-admin-a-b-c"},
	}
	for _, tc := range cases {
		if got := AppAdminRoleName(tc.app); got != tc.want {
			t.Errorf("AppAdminRoleName(%q) = %q, want %q", tc.app, got, tc.want)
		}
	}
}

// TestAppCreateMintsAdminRole pins the never-empty invariant at the store:
// Create mints a control-plane role named for the server and points the
// row at it, a name already taken gets a numeric suffix, an explicit role
// id is kept, a refused insert leaves no stray role, and a revived row
// whose role is gone is re-minted and stores {} when it names no manifest.
func TestAppCreateMintsAdminRole(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		jira, err := s.Apps().Create(ctx, App{Name: "finance/jira", RuntimeKind: "remote"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if jira.AdminRoleID == "" {
			t.Fatal("Create left admin_role_id empty")
		}
		role, err := s.Roles().GetByID(ctx, jira.AdminRoleID)
		if err != nil {
			t.Fatalf("minted role: %v", err)
		}
		if role.Name != "mcp-admin-finance-jira" || role.Plane != RolePlaneControl || role.Kind != RoleKindBusiness {
			t.Errorf("minted role = %+v, want mcp-admin-finance-jira on the control plane", role)
		}
		if role.Description != AppAdminRoleDescription("finance/jira") {
			t.Errorf("description = %q", role.Description)
		}
		got, err := s.Apps().GetByName(ctx, "finance/jira")
		if err != nil || got.AdminRoleID != jira.AdminRoleID {
			t.Fatalf("GetByName = %+v, %v; want the minted role id %s", got, err, jira.AdminRoleID)
		}

		// A taken name gets the next free suffix.
		if _, err := s.Roles().Create(ctx, Role{Name: "mcp-admin-jira"}); err != nil {
			t.Fatal(err)
		}
		plain, err := s.Apps().Create(ctx, App{Name: "jira", RuntimeKind: "remote"})
		if err != nil {
			t.Fatalf("Create jira: %v", err)
		}
		if r2, err := s.Roles().GetByID(ctx, plain.AdminRoleID); err != nil || r2.Name != "mcp-admin-jira-2" {
			t.Errorf("suffixed role = %+v, %v; want mcp-admin-jira-2", r2, err)
		}

		// An explicit role is kept, and an unknown one is refused.
		team, err := s.Roles().Create(ctx, Role{Name: "finance-mcp-admin", Plane: RolePlaneControl})
		if err != nil {
			t.Fatal(err)
		}
		db, err := s.Apps().Create(ctx, App{Name: "finance/db", RuntimeKind: "remote", AdminRoleID: team.ID})
		if err != nil || db.AdminRoleID != team.ID {
			t.Fatalf("Create with a role = %+v, %v", db, err)
		}
		if _, err := s.Apps().Create(ctx, App{Name: "finance/x", RuntimeKind: "remote", AdminRoleID: "nope"}); !errors.Is(err, ErrNotFound) {
			t.Errorf("Create with an unknown role = %v, want ErrNotFound", err)
		}

		// ListByAdminRoles answers the live rows of those roles only.
		rows, err := s.Apps().ListByAdminRoles(ctx, []string{team.ID, jira.AdminRoleID})
		if err != nil || len(rows) != 2 {
			t.Fatalf("ListByAdminRoles = %d rows, %v; want 2", len(rows), err)
		}
		if rows, err := s.Apps().ListByAdminRoles(ctx, nil); err != nil || len(rows) != 0 {
			t.Errorf("ListByAdminRoles(nil) = %v, %v; want none", rows, err)
		}

		// A conflict on Create never leaves a stray role behind.
		if _, err := s.Apps().Create(ctx, App{Name: "jira", RuntimeKind: "remote"}); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate Create = %v, want ErrConflict", err)
		}
		if _, err := s.Roles().GetByName(ctx, "mcp-admin-jira-3"); !errors.Is(err, ErrNotFound) {
			t.Errorf("a refused Create minted a role: %v", err)
		}

		// A revived row whose role was deleted is minted again.
		if err := s.Apps().SoftDelete(ctx, plain.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Roles().Delete(ctx, plain.AdminRoleID); err != nil {
			t.Fatal(err)
		}
		revived, err := s.Apps().Revive(ctx, App{Name: "jira", RuntimeKind: "remote", Status: "starting", Source: "api"})
		if err != nil {
			t.Fatalf("Revive: %v", err)
		}
		if revived.Manifest != "{}" {
			t.Errorf("Revive with no manifest stored %q, want {} as Create stores", revived.Manifest)
		}
		if revived.AdminRoleID == "" || revived.AdminRoleID == plain.AdminRoleID {
			t.Fatalf("Revive kept a dead role id %q", revived.AdminRoleID)
		}
		if r3, err := s.Roles().GetByID(ctx, revived.AdminRoleID); err != nil || r3.Name != "mcp-admin-jira-2" {
			t.Errorf("re-minted role = %+v, %v; want mcp-admin-jira-2 again, the name is free", r3, err)
		}
	})
}

// TestAppBackfillAdminRoles pins the boot backfill: a row with no role
// (one that predates the column) and a row naming a role that no longer
// exists both receive a minted role, and rows in good standing are left
// alone and not reported.
func TestAppBackfillAdminRoles(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		sq := s.(*sqlStore)
		fine, err := s.Apps().Create(ctx, App{Name: "fine", RuntimeKind: "remote"})
		if err != nil {
			t.Fatal(err)
		}
		empty, err := s.Apps().Create(ctx, App{Name: "old/empty", RuntimeKind: "remote"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sq.exec(ctx, `UPDATE apps SET admin_role_id = NULL WHERE id = $1`, empty.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Roles().Delete(ctx, empty.AdminRoleID); err != nil {
			t.Fatal(err)
		}
		dangling, err := s.Apps().Create(ctx, App{Name: "dangling", RuntimeKind: "remote"})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Roles().Delete(ctx, dangling.AdminRoleID); err != nil {
			t.Fatal(err)
		}
		gone, err := s.Apps().Create(ctx, App{Name: "gone", RuntimeKind: "remote"})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Apps().SoftDelete(ctx, gone.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := sq.exec(ctx, `UPDATE apps SET admin_role_id = NULL WHERE id = $1`, gone.ID); err != nil {
			t.Fatal(err)
		}

		changed, err := s.Apps().BackfillAdminRoles(ctx)
		if err != nil {
			t.Fatalf("BackfillAdminRoles: %v", err)
		}
		names := map[string]string{}
		for _, a := range changed {
			role, err := s.Roles().GetByID(ctx, a.AdminRoleID)
			if err != nil {
				t.Fatalf("backfilled role of %s: %v", a.Name, err)
			}
			names[a.Name] = role.Name
		}
		if len(changed) != 2 || names["old/empty"] != "mcp-admin-old-empty" || names["dangling"] != "mcp-admin-dangling" {
			t.Errorf("backfill changed %v, want old/empty and dangling only", names)
		}
		if got, _ := s.Apps().GetByID(ctx, fine.ID); got.AdminRoleID != fine.AdminRoleID {
			t.Errorf("backfill touched a row in good standing: %s -> %s", fine.AdminRoleID, got.AdminRoleID)
		}
		if again, err := s.Apps().BackfillAdminRoles(ctx); err != nil || len(again) != 0 {
			t.Errorf("second backfill = %v, %v; want nothing", again, err)
		}
	})
}

// TestAppBackfillAdoptsPeerRole pins the backfill of a row that another
// replica backfilled at the same moment. The call working from the row as
// it read it adopts the peer's role: it mints no second role and leaves the
// row on the peer's role. That holds when the peer finished first and, on
// Postgres, when this call's insert of the role name waits behind the
// peer's uncommitted one and then hits the unique index. A row removed in
// the meantime still refuses, and the refused mint leaves no role behind.
func TestAppBackfillAdoptsPeerRole(t *testing.T) {
	cases := []struct {
		name    string
		empty   bool // the row names no role, as one that predates the column
		removed bool // the row is removed after the read instead of backfilled
		racing  bool // the peer's mint is uncommitted when this call inserts
	}{
		{name: "old/empty", empty: true},
		{name: "dangling"},
		{name: "gone", removed: true},
		{name: "racing/empty", empty: true, racing: true},
		{name: "racing", racing: true},
	}
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		sq := s.(*sqlStore)
		for _, tc := range cases {
			a, err := s.Apps().Create(ctx, App{Name: tc.name, RuntimeKind: "remote"})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Roles().Delete(ctx, a.AdminRoleID); err != nil {
				t.Fatal(err)
			}
			if tc.empty {
				if _, err := sq.exec(ctx, `UPDATE apps SET admin_role_id = NULL WHERE id = $1`, a.ID); err != nil {
					t.Fatal(err)
				}
			}
			stale, err := s.Apps().GetByID(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.removed {
				if err := s.Apps().SoftDelete(ctx, a.ID); err != nil {
					t.Fatal(err)
				}
				if did, err := (appRepo{sq}).ensureAdminRole(ctx, stale); did || !errors.Is(err, ErrNotFound) {
					t.Errorf("%s: ensureAdminRole on a removed row = %v, %v; want ErrNotFound", tc.name, did, err)
				}
				if _, err := s.Roles().GetByName(ctx, AppAdminRoleName(tc.name)); !errors.Is(err, ErrNotFound) {
					t.Errorf("%s: the refused mint left a role behind: %v", tc.name, err)
				}
				continue
			}
			var peerRole string
			var did bool
			if tc.racing {
				if sq.d != dialectPostgres {
					continue // SQLite's one connection runs the two transactions one after the other
				}
				peerRole, did, err = ensureBehindPeerMint(t, sq, stale)
			} else {
				peer, peerErr := s.Apps().BackfillAdminRoles(ctx)
				if peerErr != nil || len(peer) != 1 {
					t.Fatalf("%s: the peer's backfill = %v, %v; want the one row", tc.name, peer, peerErr)
				}
				peerRole = peer[0].AdminRoleID
				did, err = (appRepo{sq}).ensureAdminRole(ctx, stale)
			}
			if err != nil || did {
				t.Errorf("%s: ensureAdminRole after the peer = %v, %v; want the peer's role adopted", tc.name, did, err)
			}
			if got, _ := s.Apps().GetByID(ctx, a.ID); got.AdminRoleID != peerRole {
				t.Errorf("%s: the row moved from the peer's role %s to %s", tc.name, peerRole, got.AdminRoleID)
			}
			if _, err := s.Roles().GetByName(ctx, AppAdminRoleName(tc.name)+"-2"); !errors.Is(err, ErrNotFound) {
				t.Errorf("%s: a second admin role was minted: %v", tc.name, err)
			}
		}
	})
}

// ensureBehindPeerMint runs ensureAdminRole on the row as stale holds it
// while a peer's transaction holds an uncommitted mint of the same role
// name, and commits the peer once the call waits on that name. The call's
// insert then answers the unique violation, the path a Postgres replica
// takes when it loses the backfill. It answers the peer's role id and the
// call's result.
func ensureBehindPeerMint(t *testing.T, sq *sqlStore, stale App) (string, bool, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := sq.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	role, err := (appRepo{sq}).mintAdminRole(ctx, tx, stale.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := (appRepo{sq}).setAdminRole(ctx, tx, stale.ID, stale.AdminRoleID, role.ID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		did bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		did, err := (appRepo{sq}).ensureAdminRole(ctx, stale)
		done <- result{did, err}
	}()
	waiting := 0
	for deadline := time.Now().Add(10 * time.Second); waiting == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if err := sq.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
			AND wait_event_type = 'Lock' AND query LIKE 'INSERT INTO roles%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if waiting == 0 {
		t.Fatalf("the call never waited behind the peer's insert of %s", role.Name)
	}
	return role.ID, r.did, r.err
}
