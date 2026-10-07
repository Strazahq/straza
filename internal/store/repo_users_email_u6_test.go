package store

import (
	"context"
	"slices"
	"testing"
)

// TestUsersListByEmail pins that ListByEmail answers every undeleted user
// with the email, of any status or type, oldest first, and nothing for an
// empty or unknown email. The Slack lane picks among these rows, because
// emails are not unique.
func TestUsersListByEmail(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		create := func(u User) User {
			t.Helper()
			got, err := s.Users().Create(ctx, u)
			if err != nil {
				t.Fatalf("Create %s: %v", u.Username, err)
			}
			return got
		}
		old := create(User{Username: "ana-old", Email: "ana@example.com", Status: UserDisabled})
		agent := create(User{Username: "ana-agent", Email: "ana@example.com", UserType: UserTypeAgent})
		gone := create(User{Username: "ana-gone", Email: "ana@example.com"})
		ana := create(User{Username: "ana", Email: "ana@example.com"})
		kim := create(User{Username: "kim", Email: "kim@example.com"})
		if _, err := s.Users().SoftDelete(ctx, gone.ID); err != nil {
			t.Fatalf("SoftDelete: %v", err)
		}

		cases := []struct {
			name, email string
			want        []string
		}{
			{"every undeleted row of any status or type, oldest first", "ana@example.com", []string{old.ID, agent.ID, ana.ID}},
			{"one row", "kim@example.com", []string{kim.ID}},
			{"an unknown email", "nobody@example.com", nil},
			{"an empty email never matches the rows without an address", "", nil},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				us, err := s.Users().ListByEmail(ctx, tc.email)
				if err != nil {
					t.Fatalf("ListByEmail(%q): %v", tc.email, err)
				}
				var ids []string
				for _, u := range us {
					if u.Email != tc.email {
						t.Errorf("row %s has email %q, want %q", u.Username, u.Email, tc.email)
					}
					ids = append(ids, u.ID)
				}
				if !slices.Equal(ids, tc.want) {
					t.Errorf("ListByEmail(%q) = %v, want %v", tc.email, ids, tc.want)
				}
			})
		}
	})
}
