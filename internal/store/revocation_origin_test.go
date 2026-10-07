package store

import (
	"context"
	"testing"
)

// TestRevocationOrigin pins the two-lane activation store contract:
// revocations carry an origin, deletion can be origin-selective (the SCIM
// lift), and rows written before the origin column existed default to
// "scim" so an IdM reactivation keeps lifting them (behavior-preserving
// backfill, recorded in the migration).
func TestRevocationOrigin(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()

		explicit := []struct {
			origin string
			want   string
		}{
			{RevocationOriginSCIM, "scim"},
			{RevocationOriginAdmin, "admin"},
			{RevocationOriginExternal, "external"},
			// Empty origin defaults to the strongest survival class: a row of
			// unknown origin must never be liftable by an IdM write.
			{"", "admin"},
		}
		for _, tc := range explicit {
			rv, err := s.Revocations().Create(ctx, Revocation{
				Kind: RevokeUser, TargetID: "origin-" + tc.want + tc.origin, Reason: "r", Origin: tc.origin,
			})
			if err != nil {
				t.Fatalf("Create(origin=%q): %v", tc.origin, err)
			}
			if rv.Origin != tc.want {
				t.Errorf("Create(origin=%q).Origin = %q, want %q", tc.origin, rv.Origin, tc.want)
			}
		}

		// One user holding both lanes: the SCIM lift removes only its own rows.
		target := "u-two-lane"
		for _, origin := range []string{RevocationOriginSCIM, RevocationOriginAdmin, RevocationOriginExternal} {
			if _, err := s.Revocations().Create(ctx, Revocation{Kind: RevokeUser, TargetID: target, Reason: "r", Origin: origin}); err != nil {
				t.Fatal(err)
			}
		}
		// A same-origin row on a DIFFERENT target must survive the delete.
		if _, err := s.Revocations().Create(ctx, Revocation{Kind: RevokeUser, TargetID: "u-other", Reason: "r", Origin: RevocationOriginSCIM}); err != nil {
			t.Fatal(err)
		}
		if err := s.Revocations().DeleteByOrigin(ctx, RevokeUser, target, RevocationOriginSCIM); err != nil {
			t.Fatalf("DeleteByOrigin: %v", err)
		}
		rows, err := s.Revocations().ListByTarget(ctx, RevokeUser, target)
		if err != nil {
			t.Fatalf("ListByTarget: %v", err)
		}
		got := map[string]bool{}
		for _, rv := range rows {
			got[rv.Origin] = true
		}
		if len(rows) != 2 || !got["admin"] || !got["external"] {
			t.Fatalf("after SCIM-selective delete: rows = %+v, want exactly admin+external", rows)
		}
		other, err := s.Revocations().ListByTarget(ctx, RevokeUser, "u-other")
		if err != nil || len(other) != 1 || other[0].Origin != "scim" {
			t.Fatalf("unrelated target rows = %+v, %v (want the scim row intact)", other, err)
		}

		// Full delete (the admin unlock / reactivation) clears every lane.
		if err := s.Revocations().Delete(ctx, RevokeUser, target); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		rows, err = s.Revocations().ListByTarget(ctx, RevokeUser, target)
		if err != nil || len(rows) != 0 {
			t.Fatalf("after full delete: %d rows, %v", len(rows), err)
		}

		// A row inserted without the origin column (a pre-migration writer)
		// lands as "scim": the IdM keeps the power to lift what predates the
		// two-lane split.
		ss, ok := s.(*sqlStore)
		if !ok {
			t.Fatalf("test store is %T, want *sqlStore", s)
		}
		if _, err := ss.exec(ctx, `INSERT INTO revocations (id, kind, target_id, reason, created_at)
			VALUES ($1, $2, $3, $4, $5)`, newID(), RevokeUser, "u-legacy", "old row", ss.tArg(now())); err != nil {
			t.Fatalf("legacy insert: %v", err)
		}
		legacy, err := s.Revocations().ListByTarget(ctx, RevokeUser, "u-legacy")
		if err != nil || len(legacy) != 1 {
			t.Fatalf("legacy rows = %d, %v", len(legacy), err)
		}
		if legacy[0].Origin != RevocationOriginSCIM {
			t.Errorf("legacy row origin = %q, want scim (column default is the backfill)", legacy[0].Origin)
		}
	})
}
