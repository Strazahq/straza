package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestEntraGroups pins the life of a link between a role and its group in
// Entra on both drivers: a row is stored whole, replaced whole, moved
// between states by one winner, kept when its role is deleted, and removed.
func TestEntraGroups(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		role, err := s.Roles().Create(ctx, Role{Name: "linked-role", Kind: RoleKindBusiness})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.EntraGroups().Get(ctx, role.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get before any row = %v, want ErrNotFound", err)
		}
		g := EntraRoleGroup{RoleID: role.ID, UniqueName: "straza-abc-role-" + role.ID, State: EntraGroupCreating}
		if err := s.EntraGroups().Put(ctx, g); err != nil {
			t.Fatalf("Put: %v", err)
		}
		got, err := s.EntraGroups().Get(ctx, role.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != EntraGroupCreating || got.GroupID != "" || got.ReadAt != nil || got.NotApplied != "[]" || got.CreatedAt.IsZero() {
			t.Errorf("a new row = %+v, want creating, no group id, no read and an empty list", got)
		}
		read := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
		g.GroupID, g.State, g.ReadAt, g.Error, g.NotApplied = "group-1", EntraGroupLinked, &read, "a sentence", `[{"id":"x"}]`
		if err := s.EntraGroups().Put(ctx, g); err != nil {
			t.Fatalf("Put again: %v", err)
		}
		got, err = s.EntraGroups().Get(ctx, role.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.GroupID != "group-1" || got.State != EntraGroupLinked || got.ReadAt == nil || !got.ReadAt.Equal(read) ||
			got.Error != "a sentence" || got.NotApplied != `[{"id":"x"}]` {
			t.Errorf("a replaced row = %+v", got)
		}
		for _, st := range []struct {
			from, to string
			want     bool
		}{
			{EntraGroupHeld, EntraGroupReleased, false},
			{EntraGroupLinked, EntraGroupHeld, true},
			{EntraGroupHeld, EntraGroupReleased, true},
			{EntraGroupHeld, EntraGroupReleased, false},
		} {
			if ok, err := s.EntraGroups().SetState(ctx, role.ID, st.from, st.to); err != nil || ok != st.want {
				t.Errorf("SetState %s to %s = %v, %v, want %v", st.from, st.to, ok, err, st.want)
			}
		}
		if err := s.EntraGroups().Put(ctx, EntraRoleGroup{RoleID: "r2", UniqueName: g.UniqueName, State: EntraGroupCreating}); err == nil {
			t.Error("a second row with the same uniqueName was stored, want the unique key to refuse it")
		}
		if err := s.EntraGroups().Put(ctx, EntraRoleGroup{RoleID: "r3", UniqueName: "u3", State: "bogus"}); err == nil {
			t.Error("a row with an unknown state was stored, want the check to refuse it")
		}
		if err := s.Roles().Delete(ctx, role.ID); err != nil {
			t.Fatal(err)
		}
		all, err := s.EntraGroups().List(ctx)
		if err != nil || len(all) != 1 || all[0].RoleID != role.ID {
			t.Fatalf("List after the role was deleted = %+v, %v, want the row to outlive its role", all, err)
		}
		if err := s.EntraGroups().Delete(ctx, role.ID); err != nil {
			t.Fatal(err)
		}
		if all, err := s.EntraGroups().List(ctx); err != nil || len(all) != 0 {
			t.Errorf("List after Delete = %+v, %v, want none", all, err)
		}
	})
}
