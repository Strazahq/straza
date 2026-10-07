package store

import (
	"context"
	"testing"
	"time"
)

// TestAssignmentCountsByRole pins the grouped count the roles list renders:
// assignment ROWS per role, windows included (it mirrors the assignment list
// an admin sees and can revoke, not effective validity), group subjects
// counted like user subjects, zero-assignment roles absent from the map.
func TestAssignmentCountsByRole(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		a, err := s.Roles().Create(ctx, Role{Name: "reader"})
		if err != nil {
			t.Fatal(err)
		}
		b, err := s.Roles().Create(ctx, Role{Name: "writer"})
		if err != nil {
			t.Fatal(err)
		}
		c, err := s.Roles().Create(ctx, Role{Name: "empty"})
		if err != nil {
			t.Fatal(err)
		}

		past := time.Now().Add(-time.Hour).UTC()
		for _, asg := range []RoleAssignment{
			{SubjectKind: SubjectUser, SubjectID: "u1", RoleID: a.ID},
			{SubjectKind: SubjectUser, SubjectID: "u2", RoleID: a.ID},
			{SubjectKind: SubjectUser, SubjectID: "u3", RoleID: a.ID},
			{SubjectKind: SubjectUser, SubjectID: "u1", RoleID: b.ID},
			// An expired window still counts: the number matches the rows
			// the assignment list shows, not effective validity.
			{SubjectKind: SubjectUser, SubjectID: "u2", RoleID: b.ID, ValidTo: &past},
		} {
			if _, err := s.Roles().Assign(ctx, asg); err != nil {
				t.Fatalf("Assign %+v: %v", asg, err)
			}
		}

		got, err := s.Roles().AssignmentCountsByRole(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got[a.ID] != 3 || got[b.ID] != 2 {
			t.Errorf("counts = %v, want %s:3 %s:2", got, a.ID, b.ID)
		}
		if _, ok := got[c.ID]; ok {
			t.Errorf("zero-assignment role %s should be absent, got %v", c.ID, got)
		}
	})
}

// TestAssignmentPairsInForce pins the flat read the roles list folds into
// holder counts: one pair per assignment row inside its validity window at
// t, from inclusive and to exclusive, expired and not-yet-valid rows left
// out, on both drivers.
func TestAssignmentPairsInForce(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		a, err := s.Roles().Create(ctx, Role{Name: "reader"})
		if err != nil {
			t.Fatal(err)
		}
		b, err := s.Roles().Create(ctx, Role{Name: "writer"})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		past := now.Add(-time.Hour)
		future := now.Add(time.Hour)
		for _, asg := range []RoleAssignment{
			{SubjectKind: SubjectUser, SubjectID: "u1", RoleID: a.ID},
			{SubjectKind: SubjectUser, SubjectID: "u2", RoleID: a.ID, ValidTo: &past},
			{SubjectKind: SubjectUser, SubjectID: "u3", RoleID: a.ID, ValidFrom: &future},
			{SubjectKind: SubjectUser, SubjectID: "u4", RoleID: a.ID, ValidFrom: &now},
			{SubjectKind: SubjectUser, SubjectID: "u1", RoleID: b.ID, ValidTo: &now},
			{SubjectKind: SubjectUser, SubjectID: "u5", RoleID: b.ID, ValidFrom: &past, ValidTo: &future},
		} {
			if _, err := s.Roles().Assign(ctx, asg); err != nil {
				t.Fatalf("Assign %+v: %v", asg, err)
			}
		}
		got, err := s.Roles().AssignmentPairsInForce(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		want := map[AssignmentPair]bool{{a.ID, "u1"}: true, {a.ID, "u4"}: true, {b.ID, "u5"}: true}
		if len(got) != len(want) {
			t.Fatalf("pairs = %v, want %v", got, want)
		}
		for _, p := range got {
			if !want[p] {
				t.Errorf("unexpected pair %+v", p)
			}
		}
	})
}
