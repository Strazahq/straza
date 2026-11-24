package store

import (
	"context"
	"sort"
	"testing"
)

// TestEntraHolderBesideOthers pins that a holder has at most one row the
// Entra sync wrote and one row of any other origin, that either can end
// without the other, and that the counts name a holder once.
func TestEntraHolderBesideOthers(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		role, err := s.Roles().Create(ctx, Role{Name: "both-sources", Kind: RoleKindBusiness})
		if err != nil {
			t.Fatal(err)
		}
		row := func(user, origin string) RoleAssignment {
			return RoleAssignment{SubjectKind: SubjectUser, SubjectID: user, Origin: origin}
		}
		holders := func() []string {
			t.Helper()
			asg, err := s.Roles().AssignmentsByRole(ctx, role.ID)
			if err != nil {
				t.Fatal(err)
			}
			out := make([]string, 0, len(asg))
			for _, a := range asg {
				out = append(out, a.SubjectID+"/"+a.Origin)
			}
			sort.Strings(out)
			return out
		}
		steps := []struct {
			name      string
			add       RoleAssignment
			wantAdded int
			want      []string
		}{
			{"an admin row", row("kim", OriginAdmin), 1, []string{"kim/admin"}},
			{"an Entra row for the same holder stands beside it", row("kim", OriginEntra), 1, []string{"kim/admin", "kim/entra"}},
			{"a second Entra row is skipped", row("kim", OriginEntra), 0, []string{"kim/admin", "kim/entra"}},
			{"a SCIM row beside the admin row is skipped", row("kim", OriginSCIM), 0, []string{"kim/admin", "kim/entra"}},
			{"an Entra row alone", row("bob", OriginEntra), 1, []string{"bob/entra", "kim/admin", "kim/entra"}},
			{"a SCIM row beside the Entra row", row("bob", OriginSCIM), 1, []string{"bob/entra", "bob/scim", "kim/admin", "kim/entra"}},
		}
		for _, st := range steps {
			added, _, err := s.Roles().ApplyMembership(ctx, role.ID, []RoleAssignment{st.add}, nil)
			if err != nil {
				t.Fatalf("%s: %v", st.name, err)
			}
			if len(added) != st.wantAdded {
				t.Errorf("%s: %d rows added, want %d", st.name, len(added), st.wantAdded)
			}
			if got := holders(); !equalStrings(got, st.want) {
				t.Errorf("%s: holders = %v, want %v", st.name, got, st.want)
			}
		}
		counts, err := s.Roles().AssignmentCountsByRole(ctx)
		if err != nil || counts[role.ID] != 2 {
			t.Errorf("AssignmentCountsByRole = %d, %v, want 2 holders for 4 rows", counts[role.ID], err)
		}
		if n, err := s.Roles().HolderCount(ctx, role.ID); err != nil || n != 2 {
			t.Errorf("HolderCount = %d, %v, want 2", n, err)
		}
		asg, _ := s.Roles().AssignmentsByRole(ctx, role.ID)
		for _, a := range asg {
			if a.SubjectID == "kim" && a.Origin == OriginEntra {
				if _, removed, err := s.Roles().ApplyMembership(ctx, role.ID, nil, []string{a.ID}); err != nil || len(removed) != 1 {
					t.Fatalf("ending kim's Entra row = %+v, %v", removed, err)
				}
			}
		}
		if got, want := holders(), []string{"bob/entra", "bob/scim", "kim/admin"}; !equalStrings(got, want) {
			t.Errorf("holders after kim's Entra row ended = %v, want %v", got, want)
		}
		seedUserIDs(t, s, "kim")
		if _, err := s.Roles().Assign(ctx, RoleAssignment{SubjectKind: SubjectUser, SubjectID: "kim", RoleID: role.ID}); err == nil {
			t.Errorf("a second admin row for kim was stored, want the unique key to refuse it")
		}
	})
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestRoleGroupsMigration pins migration 44 on both drivers: assignments
// written below it survive the move up with their ids and origins, the
// origin check still refuses an unknown origin, and the move down ends the
// rows the Entra sync wrote and keeps the others.
func TestRoleGroupsMigration(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		sq := s.(*sqlStore)
		_, head, err := sq.migrationRange()
		if err != nil {
			t.Fatal(err)
		}
		if head < 44 {
			t.Fatalf("newest migration = %d, want 44 or later", head)
		}
		for v := head; v >= 44; v-- {
			if got, err := sq.MigrateTo(ctx, v-1); err != nil || got != v-1 {
				t.Fatalf("MigrateTo(%d) = %d, %v", v-1, got, err)
			}
		}
		role, err := s.Roles().Create(ctx, Role{Name: "migrated", Kind: RoleKindBusiness})
		if err != nil {
			t.Fatal(err)
		}
		seedUserIDs(t, s, "kim", "bob", "eve")
		old := map[string]string{}
		for user, origin := range map[string]string{"kim": OriginAdmin, "bob": OriginSCIM} {
			a, err := s.Roles().Assign(ctx, RoleAssignment{SubjectKind: SubjectUser, SubjectID: user, RoleID: role.ID, Origin: origin})
			if err != nil {
				t.Fatalf("Assign below 44: %v", err)
			}
			old[a.ID] = user + "/" + origin
		}
		if _, err := s.Roles().Assign(ctx, RoleAssignment{SubjectKind: SubjectUser, SubjectID: "eve", RoleID: role.ID, Origin: OriginEntra}); err == nil {
			t.Fatal("an Entra row was stored below 44, want the old origin check to refuse it")
		}
		if got, err := sq.MigrateTo(ctx, head); err != nil || got != head {
			t.Fatalf("MigrateTo(head) = %d, %v", got, err)
		}
		rows := func() map[string]string {
			t.Helper()
			asg, err := s.Roles().AssignmentsByRole(ctx, role.ID)
			if err != nil {
				t.Fatal(err)
			}
			out := map[string]string{}
			for _, a := range asg {
				out[a.ID] = a.SubjectID + "/" + a.Origin
			}
			return out
		}
		got := rows()
		if len(got) != len(old) {
			t.Fatalf("rows after the move up = %v, want %v", got, old)
		}
		for id, want := range old {
			if got[id] != want {
				t.Errorf("row %s after the move up = %q, want %q", id, got[id], want)
			}
		}
		if _, err := s.Roles().Assign(ctx, RoleAssignment{SubjectKind: SubjectUser, SubjectID: "eve", RoleID: role.ID, Origin: "bogus"}); err == nil {
			t.Error("an unknown origin was stored at 44, want the origin check to refuse it")
		}
		if _, err := s.Roles().Assign(ctx, RoleAssignment{SubjectKind: SubjectUser, SubjectID: "kim", RoleID: role.ID, Origin: OriginEntra}); err != nil {
			t.Fatalf("an Entra row beside kim's admin row at 44: %v", err)
		}
		if err := s.Roles().Delete(ctx, role.ID); err != nil {
			t.Fatalf("Roles.Delete: %v", err)
		}
		if left := rows(); len(left) != 0 {
			t.Errorf("rows after the role was deleted = %v, want the cascade to end them", left)
		}
		role2, err := s.Roles().Create(ctx, Role{Name: "migrated-down", Kind: RoleKindBusiness})
		if err != nil {
			t.Fatal(err)
		}
		role = role2
		for user, origin := range map[string]string{"kim": OriginAdmin, "eve": OriginEntra} {
			if _, err := s.Roles().Assign(ctx, RoleAssignment{SubjectKind: SubjectUser, SubjectID: user, RoleID: role.ID, Origin: origin}); err != nil {
				t.Fatal(err)
			}
		}
		for v := head; v >= 44; v-- {
			if got, err := sq.MigrateTo(ctx, v-1); err != nil || got != v-1 {
				t.Fatalf("MigrateTo(%d) = %d, %v", v-1, got, err)
			}
		}
		down := rows()
		if len(down) != 1 {
			t.Fatalf("rows after the move down = %v, want kim's admin row alone", down)
		}
		for _, v := range down {
			if v != "kim/admin" {
				t.Errorf("row after the move down = %q, want kim/admin", v)
			}
		}
		if _, err := sq.exec(ctx, "SELECT name FROM leases"); err == nil {
			t.Error("the leases table survived the move down")
		}
		if got, err := sq.MigrateTo(ctx, head); err != nil || got != head {
			t.Fatalf("MigrateTo(head) again = %d, %v", got, err)
		}
	})
}
