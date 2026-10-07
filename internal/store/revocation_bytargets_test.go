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

// TestPackListBindings pins the one-query pack-role read behind the packs
// admin list (0.58.0): every edge comes back with the role name joined in.
func TestPackListBindings(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()

		r1, err := s.Roles().Create(ctx, Role{Name: "pb-dev"})
		if err != nil {
			t.Fatal(err)
		}
		r2, err := s.Roles().Create(ctx, Role{Name: "pb-ops"})
		if err != nil {
			t.Fatal(err)
		}
		p1, err := s.Packs().Create(ctx, KnowledgePack{Name: "pb-style", Content: "x", Checksum: "c"})
		if err != nil {
			t.Fatal(err)
		}
		p2, err := s.Packs().Create(ctx, KnowledgePack{Name: "pb-rules", Content: "y", Checksum: "c"})
		if err != nil {
			t.Fatal(err)
		}
		for _, edge := range []struct{ role, pack string }{
			{r1.ID, p1.ID}, {r2.ID, p1.ID}, {r1.ID, p2.ID},
		} {
			if err := s.Packs().Bind(ctx, edge.role, edge.pack); err != nil {
				t.Fatal(err)
			}
		}

		rows, err := s.Packs().ListBindings(ctx)
		if err != nil {
			t.Fatalf("ListBindings: %v", err)
		}
		byPack := map[string]map[string]string{} // pack id → role id → role name
		for _, b := range rows {
			if byPack[b.PackID] == nil {
				byPack[b.PackID] = map[string]string{}
			}
			byPack[b.PackID][b.RoleID] = b.RoleName
		}
		if len(byPack[p1.ID]) != 2 || byPack[p1.ID][r1.ID] != "pb-dev" || byPack[p1.ID][r2.ID] != "pb-ops" {
			t.Errorf("p1 bindings = %v, want pb-dev and pb-ops", byPack[p1.ID])
		}
		if len(byPack[p2.ID]) != 1 || byPack[p2.ID][r1.ID] != "pb-dev" {
			t.Errorf("p2 bindings = %v, want pb-dev only", byPack[p2.ID])
		}

		// Unbind drops exactly one edge; the second delete of the same edge
		// answers ErrNotFound (the API's 404).
		if err := s.Packs().Unbind(ctx, r2.ID, p1.ID); err != nil {
			t.Fatalf("Unbind: %v", err)
		}
		rows, err = s.Packs().ListBindings(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range rows {
			if b.PackID == p1.ID && b.RoleID == r2.ID {
				t.Error("unbound edge still listed")
			}
		}
		if err := s.Packs().Unbind(ctx, r2.ID, p1.ID); err != ErrNotFound {
			t.Errorf("second Unbind = %v, want ErrNotFound", err)
		}
	})
}
