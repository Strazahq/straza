package store

import (
	"context"
	"strings"
	"testing"
)

// TestSessionClientVersionRoundTrip pins the client build stamp on a session:
// stored as given, read back on get and list, blank for a client that sent
// none (every client older than this column).
func TestSessionClientVersionRoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		owner, err := s.Users().Create(ctx, User{Username: "stamp-owner"})
		if err != nil {
			t.Fatalf("Users.Create: %v", err)
		}
		stamped, err := s.Sessions().Create(ctx, Session{UserID: owner.ID, HarnessName: "claude-code", ClientVersion: "v1.0.0-1080-ga49eca3f"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		blank, err := s.Sessions().Create(ctx, Session{UserID: owner.ID, HarnessName: "claude-code"})
		if err != nil {
			t.Fatalf("Create blank: %v", err)
		}
		got, err := s.Sessions().GetByID(ctx, stamped.ID)
		if err != nil || got.ClientVersion != "v1.0.0-1080-ga49eca3f" {
			t.Fatalf("Get = %q, %v; want the stamp back", got.ClientVersion, err)
		}
		list, err := s.Sessions().List(ctx, "")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		seen := map[string]string{}
		for _, ses := range list {
			seen[ses.ID] = ses.ClientVersion
		}
		if seen[stamped.ID] != "v1.0.0-1080-ga49eca3f" || seen[blank.ID] != "" {
			t.Fatalf("List stamps = %q / %q, want the stamp and blank", seen[stamped.ID], seen[blank.ID])
		}
	})
}

// TestSessionClientCommitRoundTrip pins the commit of the client build on a
// session: stored as given up to 64 bytes and read back on get and list, a
// longer value cut to 64 bytes on a character boundary, and blank for a
// client that sent none.
func TestSessionClientCommitRoundTrip(t *testing.T) {
	full40 := strings.Repeat("0123456789", 4)
	full64 := strings.Repeat("abcdef0123456789", 4)
	cases := []struct {
		name, commit, want string
	}{
		{"a short commit", "011d686b", "011d686b"},
		{"a full sha-1 commit", full40, full40},
		{"a full sha-256 commit, exactly the cap", full64, full64},
		{"one byte over the cap", full64 + "f", full64},
		{"far over the cap", strings.Repeat("x", 4096), strings.Repeat("x", 64)},
		{"a two-byte character across the cap", strings.Repeat("a", 63) + "é", strings.Repeat("a", 63)},
		{"none sent", "", ""},
	}
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		owner, err := s.Users().Create(ctx, User{Username: "commit-owner"})
		if err != nil {
			t.Fatalf("Users.Create: %v", err)
		}
		ids := map[string]string{}
		for _, tc := range cases {
			ses, err := s.Sessions().Create(ctx, Session{UserID: owner.ID, HarnessName: "claude-code", ClientVersion: "v1.0.0", ClientCommit: tc.commit})
			if err != nil {
				t.Fatalf("%s: Create: %v", tc.name, err)
			}
			if ses.ClientCommit != tc.want {
				t.Errorf("%s: Create returned commit %q, want %q", tc.name, ses.ClientCommit, tc.want)
			}
			ids[tc.name] = ses.ID
		}
		list, err := s.Sessions().List(ctx, "")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		listed := map[string]string{}
		for _, ses := range list {
			listed[ses.ID] = ses.ClientCommit
		}
		for _, tc := range cases {
			got, err := s.Sessions().GetByID(ctx, ids[tc.name])
			if err != nil || got.ClientCommit != tc.want {
				t.Errorf("%s: Get commit = %q, %v; want %q", tc.name, got.ClientCommit, err, tc.want)
			}
			if listed[ids[tc.name]] != tc.want {
				t.Errorf("%s: List commit = %q, want %q", tc.name, listed[ids[tc.name]], tc.want)
			}
		}
	})
}

// TestSessionClientCommitMigration pins migration 41 on both drivers: the
// move down drops the column, a session written below 41 reads back an
// empty commit after the move up, and a new session stores its commit.
func TestSessionClientCommitMigration(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		sq := s.(*sqlStore)
		_, head, err := sq.migrationRange()
		if err != nil {
			t.Fatal(err)
		}
		if head < 41 {
			t.Fatalf("newest migration = %d, want 41 or later", head)
		}
		owner, err := s.Users().Create(ctx, User{Username: "old-row-owner"})
		if err != nil {
			t.Fatalf("Users.Create: %v", err)
		}
		for v := head; v >= 41; v-- {
			if got, err := sq.MigrateTo(ctx, v-1); err != nil || got != v-1 {
				t.Fatalf("MigrateTo(%d) = %d, %v", v-1, got, err)
			}
		}
		if _, err := sq.exec(ctx, "SELECT client_commit FROM sessions"); err == nil {
			t.Error("column client_commit survived the move down")
		}
		if _, err := sq.exec(ctx, `INSERT INTO sessions (id, user_id, harness_name, status, started_at, last_seen, client_version)
			VALUES ($1, $2, 'claude-code', 'active', $3, $4, 'v1.0.0-1600-gabcdef01')`, "ses-old", owner.ID, sq.tArg(now()), sq.tArg(now())); err != nil {
			t.Fatalf("insert a session below migration 41: %v", err)
		}
		if got, err := sq.MigrateTo(ctx, head); err != nil || got != head {
			t.Fatalf("MigrateTo(%d) = %d, %v", head, got, err)
		}
		old, err := s.Sessions().GetByID(ctx, "ses-old")
		if err != nil || old.ClientCommit != "" || old.ClientVersion != "v1.0.0-1600-gabcdef01" {
			t.Fatalf("session from below 41 = %+v, %v; want its version and an empty commit", old, err)
		}
		fresh, err := s.Sessions().Create(ctx, Session{UserID: owner.ID, HarnessName: "claude-code", ClientCommit: "011d686b"})
		if err != nil {
			t.Fatalf("Create after the move up: %v", err)
		}
		if got, err := s.Sessions().GetByID(ctx, fresh.ID); err != nil || got.ClientCommit != "011d686b" {
			t.Fatalf("commit after the move up = %q, %v; want 011d686b", got.ClientCommit, err)
		}
	})
}
