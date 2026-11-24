package store

import (
	"context"
	"errors"
	"testing"
)

// seedUserIDs writes one live user per id, named after it, so a test can
// assign roles to readable subject ids. It writes only the columns of the
// baseline schema, so it works at every migration a test steps to.
func seedUserIDs(t *testing.T, s Store, ids ...string) {
	t.Helper()
	sq := s.(*sqlStore)
	for _, id := range ids {
		at := sq.tArg(now())
		if _, err := sq.exec(context.Background(), `INSERT INTO users (id, username, created_at, updated_at) VALUES ($1, $2, $3, $4)`,
			id, id, at, at); err != nil {
			t.Fatalf("seed user %s: %v", id, err)
		}
	}
}

// TestAssignChecksTheSubject pins how Assign treats the user it is given: a
// live user gets the row, while an id no user has and a soft-deleted user
// answer ErrNotFound. A role that does not exist fails with an error that is
// not ErrNotFound, so a caller can tell the two refusals apart. A refused
// call stores no row.
func TestAssignChecksTheSubject(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		role, err := s.Roles().Create(ctx, Role{Name: "dev"})
		if err != nil {
			t.Fatal(err)
		}
		live, err := s.Users().Create(ctx, User{Username: "kim"})
		if err != nil {
			t.Fatal(err)
		}
		gone, err := s.Users().Create(ctx, User{Username: "eve"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Users().SoftDelete(ctx, gone.ID, nil); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name, subject, role string
			stored, notFound    bool
		}{
			{name: "a live user", subject: live.ID, role: role.ID, stored: true},
			{name: "an id no user has", subject: "no-such-user", role: role.ID, notFound: true},
			{name: "a soft-deleted user", subject: gone.ID, role: role.ID, notFound: true},
			{name: "a role that does not exist", subject: live.ID, role: "no-such-role"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := s.Roles().Assign(ctx, RoleAssignment{SubjectKind: SubjectUser, SubjectID: tc.subject, RoleID: tc.role})
				if (err == nil) != tc.stored || errors.Is(err, ErrNotFound) != tc.notFound {
					t.Fatalf("Assign = %v, want stored %v and ErrNotFound %v", err, tc.stored, tc.notFound)
				}
				held, err := s.Roles().ListAssignments(ctx, SubjectUser, tc.subject)
				if err != nil {
					t.Fatal(err)
				}
				rows := 0
				for _, a := range held {
					if a.RoleID == tc.role {
						rows++
					}
				}
				if want := map[bool]int{true: 1}[tc.stored]; rows != want {
					t.Errorf("rows of %s with role %s = %d, want %d", tc.subject, tc.role, rows, want)
				}
			})
		}
	})
}
