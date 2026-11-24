package store

import (
	"context"
	"testing"
)

// TestAdminDisableMigration pins migration 47 on both drivers: a user
// revocation an admin disable wrote below it, origin admin with the
// server's own reason, becomes admin-disable, every other row keeps its
// origin, and the move down gives the row the origin admin again.
func TestAdminDisableMigration(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		sq := s.(*sqlStore)
		_, head, err := sq.migrationRange()
		if err != nil {
			t.Fatal(err)
		}
		if head < 47 {
			t.Fatalf("newest migration = %d, want 47 or later", head)
		}
		for v := head; v >= 47; v-- {
			if got, err := sq.MigrateTo(ctx, v-1); err != nil || got != v-1 {
				t.Fatalf("MigrateTo(%d) = %d, %v", v-1, got, err)
			}
		}
		rows := []struct {
			name      string
			r         Revocation
			wantAbove string // the origin at migration 47
		}{
			{"an admin disable", Revocation{Kind: "user", TargetID: "u-disabled", Reason: "user disabled by admin", Origin: RevocationOriginAdmin}, RevocationOriginAdminDisable},
			{"an admin lock", Revocation{Kind: "user", TargetID: "u-locked", Reason: "SOC hold pending review", Origin: RevocationOriginAdmin}, RevocationOriginAdmin},
			{"an external lock", Revocation{Kind: "user", TargetID: "u-soar", Reason: "user disabled by admin", Origin: RevocationOriginExternal}, RevocationOriginExternal},
			{"a SCIM deactivation", Revocation{Kind: "user", TargetID: "u-scim", Reason: "user deactivated via SCIM", Origin: RevocationOriginSCIM}, RevocationOriginSCIM},
			{"an Entra disable", Revocation{Kind: "user", TargetID: "u-entra", Reason: "the account is disabled in Entra", Origin: RevocationOriginEntra}, RevocationOriginEntra},
			{"an admin delete", Revocation{Kind: "user", TargetID: "u-deleted", Reason: "user deleted by admin", Origin: RevocationOriginAdmin}, RevocationOriginAdmin},
			{"a device with the disable's reason", Revocation{Kind: "device", TargetID: "d-1", Reason: "user disabled by admin", Origin: RevocationOriginAdmin}, RevocationOriginAdmin},
		}
		for _, row := range rows {
			if _, err := s.Revocations().Create(ctx, row.r); err != nil {
				t.Fatalf("%s: Create below 47: %v", row.name, err)
			}
		}
		origin := func(kind, target string) string {
			t.Helper()
			got, err := s.Revocations().ListByTarget(ctx, kind, target)
			if err != nil || len(got) != 1 {
				t.Fatalf("ListByTarget(%s, %s) = %v, %v; want one row", kind, target, got, err)
			}
			return got[0].Origin
		}

		if got, err := sq.MigrateTo(ctx, head); err != nil || got != head {
			t.Fatalf("MigrateTo(head) = %d, %v", got, err)
		}
		for _, row := range rows {
			if got := origin(row.r.Kind, row.r.TargetID); got != row.wantAbove {
				t.Errorf("%s after the move up has the origin %q, want %q", row.name, got, row.wantAbove)
			}
		}

		for v := head; v >= 47; v-- {
			if got, err := sq.MigrateTo(ctx, v-1); err != nil || got != v-1 {
				t.Fatalf("MigrateTo(%d) = %d, %v", v-1, got, err)
			}
		}
		for _, row := range rows {
			if got := origin(row.r.Kind, row.r.TargetID); got != row.r.Origin {
				t.Errorf("%s after the move down has the origin %q, want %q", row.name, got, row.r.Origin)
			}
		}
		if got, err := sq.MigrateTo(ctx, head); err != nil || got != head {
			t.Fatalf("MigrateTo(head) again = %d, %v", got, err)
		}
	})
}
