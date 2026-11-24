package store

import (
	"context"
	"testing"
)

// TestRevocationListByTargets pins the page-batched lock read behind the
// users list (0.58.0): one IN query answers a whole page, rows come back
// keyed by target, other kinds' rows never leak in, and targets without
// rows are absent rather than present-and-empty.
func TestRevocationListByTargets(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()

		empty, err := s.Revocations().ListByTargets(ctx, RevokeUser, nil)
		if err != nil {
			t.Fatalf("ListByTargets(no ids): %v", err)
		}
		if len(empty) != 0 {
			t.Errorf("ListByTargets(no ids) = %v, want empty map", empty)
		}

		// u1 holds BOTH lock lanes plus a device-kind row that must not leak
		// into a user-kind read; u2 holds one; u3 holds nothing.
		seed := []Revocation{
			{Kind: RevokeUser, TargetID: "u1", Reason: "admin lock", Origin: RevocationOriginAdmin},
			{Kind: RevokeUser, TargetID: "u1", Reason: "soar lock", Origin: RevocationOriginExternal},
			{Kind: RevokeDevice, TargetID: "u1", Reason: "device row", Origin: RevocationOriginAdmin},
			{Kind: RevokeUser, TargetID: "u2", Reason: "scim disable", Origin: RevocationOriginSCIM},
		}
		for _, rv := range seed {
			if _, err := s.Revocations().Create(ctx, rv); err != nil {
				t.Fatal(err)
			}
		}

		got, err := s.Revocations().ListByTargets(ctx, RevokeUser, []string{"u1", "u2", "u3"})
		if err != nil {
			t.Fatalf("ListByTargets: %v", err)
		}
		if len(got["u1"]) != 2 {
			t.Errorf("u1 rows = %d, want 2 (both lanes, device row excluded)", len(got["u1"]))
		}
		for _, rv := range got["u1"] {
			if rv.Kind != RevokeUser {
				t.Errorf("u1 row kind = %q, want %q", rv.Kind, RevokeUser)
			}
		}
		if len(got["u2"]) != 1 || got["u2"][0].Origin != RevocationOriginSCIM {
			t.Errorf("u2 rows = %+v, want one scim row", got["u2"])
		}
		if _, ok := got["u3"]; ok {
			t.Error("u3 present in map, want absent (no rows)")
		}
	})
}
