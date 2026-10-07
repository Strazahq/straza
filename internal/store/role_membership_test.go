package store

import (
	"context"
	"sort"
	"testing"
)

// TestApplyMembership pins the one write a SCIM Group change makes: every
// delete and insert lands in one transaction, the call reports only the
// rows it really deleted and inserted, a row already gone or a holder
// already present is skipped rather than refused, and a failure partway
// through rolls back all of it.
func TestApplyMembership(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		role, err := s.Roles().Create(ctx, Role{Name: "dev", Kind: RoleKindBusiness})
		if err != nil {
			t.Fatal(err)
		}
		held, err := s.Roles().Assign(ctx, RoleAssignment{SubjectKind: SubjectUser, SubjectID: "alice", RoleID: role.ID})
		if err != nil {
			t.Fatal(err)
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
		scim := func(who string) RoleAssignment {
			return RoleAssignment{SubjectKind: SubjectUser, SubjectID: who, Origin: OriginSCIM}
		}

		added, removed, err := s.Roles().ApplyMembership(ctx, role.ID, []RoleAssignment{scim("bob"), scim("kim")}, []string{held.ID})
		if err != nil {
			t.Fatalf("ApplyMembership: %v", err)
		}
		if len(removed) != 1 || removed[0].ID != held.ID || removed[0].SubjectID != "alice" || removed[0].Origin != OriginAdmin {
			t.Errorf("removed = %+v, want alice's admin-origin row %s", removed, held.ID)
		}
		if len(added) != 2 || added[0].SubjectID != "bob" || added[1].SubjectID != "kim" {
			t.Fatalf("added = %+v, want bob then kim", added)
		}
		for _, a := range added {
			if a.ID == "" || a.RoleID != role.ID || a.Origin != OriginSCIM || a.SubjectKind != SubjectUser || a.CreatedAt.IsZero() {
				t.Errorf("added row %+v lacks its id, role, scim origin, kind or creation time", a)
			}
		}
		if got := holders(); len(got) != 2 || got[0] != "bob/scim" || got[1] != "kim/scim" {
			t.Errorf("holders = %v, want bob and kim", got)
		}

		// A holder already present and a row already gone are skipped.
		added, removed, err = s.Roles().ApplyMembership(ctx, role.ID, []RoleAssignment{scim("bob")}, []string{held.ID})
		if err != nil {
			t.Fatalf("ApplyMembership of a present holder and a gone row: %v", err)
		}
		if len(added) != 0 || len(removed) != 0 {
			t.Errorf("added %+v and removed %+v, want nothing reported for rows that did not change", added, removed)
		}

		// A row of another role is never deleted through this role.
		other, err := s.Roles().Create(ctx, Role{Name: "ops", Kind: RoleKindBusiness})
		if err != nil {
			t.Fatal(err)
		}
		foreign, err := s.Roles().Assign(ctx, RoleAssignment{SubjectKind: SubjectUser, SubjectID: "sam", RoleID: other.ID})
		if err != nil {
			t.Fatal(err)
		}
		if _, removed, err = s.Roles().ApplyMembership(ctx, role.ID, nil, []string{foreign.ID}); err != nil || len(removed) != 0 {
			t.Errorf("removing another role's row through dev = %+v, %v, want nothing removed", removed, err)
		}
		if _, err := s.Roles().Assignment(ctx, foreign.ID); err != nil {
			t.Errorf("another role's row is gone: %v", err)
		}

		// A failure after a delete and an insert rolls all of it back: the
		// invalid origin breaks the origin check on both engines.
		bad := RoleAssignment{SubjectKind: SubjectUser, SubjectID: "eve", Origin: "bogus"}
		bobRow := ""
		asg, _ := s.Roles().AssignmentsByRole(ctx, role.ID)
		for _, a := range asg {
			if a.SubjectID == "bob" {
				bobRow = a.ID
			}
		}
		if _, _, err := s.Roles().ApplyMembership(ctx, role.ID, []RoleAssignment{scim("dan"), bad}, []string{bobRow}); err == nil {
			t.Fatal("ApplyMembership with an invalid row succeeded, want an error")
		}
		if got := holders(); len(got) != 2 || got[0] != "bob/scim" || got[1] != "kim/scim" {
			t.Errorf("holders = %v after a failed call, want bob and kim untouched and no dan", got)
		}
	})
}
