package store

import (
	"context"
	"testing"
)

// TestSessionWhereRoundTrip pins the machine and the folder of a session:
// stored as given, read back by id, on the list and on the page, and empty
// for a client that sent neither.
func TestSessionWhereRoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		owner, err := s.Users().Create(ctx, User{Username: "where-owner"})
		if err != nil {
			t.Fatalf("Users.Create: %v", err)
		}
		placed, err := s.Sessions().Create(ctx, Session{UserID: owner.ID, HarnessName: "claude-code",
			Host: "build-box-7", Folder: "~/src/billing"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		blank, err := s.Sessions().Create(ctx, Session{UserID: owner.ID, HarnessName: "claude-code"})
		if err != nil {
			t.Fatalf("Create blank: %v", err)
		}
		want := map[string][2]string{placed.ID: {"build-box-7", "~/src/billing"}, blank.ID: {"", ""}}
		check := func(read string, rows []Session) {
			t.Helper()
			seen := 0
			for _, ses := range rows {
				w, ok := want[ses.ID]
				if !ok {
					continue
				}
				seen++
				if ses.Host != w[0] || ses.Folder != w[1] {
					t.Errorf("%s: session %s is on %q in %q, want %q in %q", read, ses.ID, ses.Host, ses.Folder, w[0], w[1])
				}
			}
			if seen != len(want) {
				t.Errorf("%s answered %d of the %d sessions", read, seen, len(want))
			}
		}
		one, err := s.Sessions().GetByID(ctx, placed.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		other, err := s.Sessions().GetByID(ctx, blank.ID)
		if err != nil {
			t.Fatalf("GetByID blank: %v", err)
		}
		check("GetByID", []Session{one, other})
		list, err := s.Sessions().List(ctx, "")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		check("List", list)
		page, err := s.Sessions().Page(ctx, SessionFilter{UserID: owner.ID}, Sort{}, Cursor{}, 10)
		if err != nil {
			t.Fatalf("Page: %v", err)
		}
		check("Page", page)
		byUser, err := s.Sessions().Page(ctx, SessionFilter{Q: "where-owner"}, Sort{Key: "user", Asc: true}, Cursor{}, 10)
		if err != nil {
			t.Fatalf("Page by user: %v", err)
		}
		check("Page joined on users", byUser)
	})
}

// TestSessionWhereMigration pins migration 43 on both drivers: the move
// down drops the two columns, a session written below 43 reads back with no
// machine and no folder after the move up, and a new session stores both.
func TestSessionWhereMigration(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		sq := s.(*sqlStore)
		_, head, err := sq.migrationRange()
		if err != nil {
			t.Fatal(err)
		}
		if head < 43 {
			t.Fatalf("newest migration = %d, want 43 or later", head)
		}
		owner, err := s.Users().Create(ctx, User{Username: "old-where-owner"})
		if err != nil {
			t.Fatalf("Users.Create: %v", err)
		}
		for v := head; v >= 43; v-- {
			if got, err := sq.MigrateTo(ctx, v-1); err != nil || got != v-1 {
				t.Fatalf("MigrateTo(%d) = %d, %v", v-1, got, err)
			}
		}
		for _, col := range []string{"host", "folder"} {
			if _, err := sq.exec(ctx, "SELECT "+col+" FROM sessions"); err == nil {
				t.Errorf("column %s survived the move down", col)
			}
		}
		if _, err := sq.exec(ctx, `INSERT INTO sessions (id, user_id, harness_name, status, started_at, last_seen, client_commit)
			VALUES ($1, $2, 'claude-code', 'active', $3, $4, '011d686b')`, "ses-before-where", owner.ID, sq.tArg(now()), sq.tArg(now())); err != nil {
			t.Fatalf("insert a session below migration 43: %v", err)
		}
		if got, err := sq.MigrateTo(ctx, head); err != nil || got != head {
			t.Fatalf("MigrateTo(%d) = %d, %v", head, got, err)
		}
		old, err := s.Sessions().GetByID(ctx, "ses-before-where")
		if err != nil || old.Host != "" || old.Folder != "" || old.ClientCommit != "011d686b" {
			t.Fatalf("session from below 43 = %+v, %v; want its commit, no machine and no folder", old, err)
		}
		fresh, err := s.Sessions().Create(ctx, Session{UserID: owner.ID, HarnessName: "claude-code", Host: "build-box-7", Folder: "~"})
		if err != nil {
			t.Fatalf("Create after the move up: %v", err)
		}
		if got, err := s.Sessions().GetByID(ctx, fresh.ID); err != nil || got.Host != "build-box-7" || got.Folder != "~" {
			t.Fatalf("after the move up the session is on %q in %q, %v; want build-box-7 in ~", got.Host, got.Folder, err)
		}
	})
}
