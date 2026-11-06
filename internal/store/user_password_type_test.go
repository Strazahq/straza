package store

import (
	"context"
	"fmt"
	"testing"
)

// TestUserUpdateClearsPasswordOfNonPerson pins that a full user write that
// leaves the row an AI agent or a service account stores no password hash,
// whatever hash the row or the write carried, so a retype over SCIM leaves
// no password behind. A write that leaves a person keeps the hash.
func TestUserUpdateClearsPasswordOfNonPerson(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		cases := []struct {
			name   string
			retype func(u *User)
			kept   bool
		}{
			{"a retype to agent", func(u *User) { u.UserType = UserTypeAgent }, false},
			{"a retype to service", func(u *User) { u.UserType = UserTypeService }, false},
			{"a write to a row of kind nhi", func(u *User) { u.Attrs = `{"kind":"nhi"}` }, false},
			{"a write that leaves a person", func(u *User) { u.Display = "Kim" }, true},
		}
		for i, tc := range cases {
			u, err := s.Users().Create(ctx, User{Username: fmt.Sprintf("pw-%d", i), PasswordHash: "$2a$10$hash"})
			if err != nil {
				t.Fatal(err)
			}
			tc.retype(&u)
			got, err := s.Users().Update(ctx, u)
			if err != nil {
				t.Fatal(err)
			}
			if (got.PasswordHash != "") != tc.kept {
				t.Errorf("%s: the stored hash is set %v, want %v", tc.name, got.PasswordHash != "", tc.kept)
			}
		}
	})
}
