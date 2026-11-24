package store

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// TestApprovalPageByUser pins the user filter of PageByState: one user's
// records newest first, AND with the state and the cursor, none for an id
// no record has, and every user's when the filter is empty. Runs on both
// drivers.
func TestApprovalPageByUser(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC()
		var ann, all []string
		for i, a := range []struct{ user, state string }{
			{"ann", "pending"}, {"bob", "pending"}, {"ann", "approved"}, {"ann", "pending"}, {"bob", "denied"},
		} {
			rec, err := s.Approvals().Insert(ctx, Approval{SessionID: "s-" + a.user, UserID: a.user, RuleID: "r",
				ArgvHash: "h" + strconv.Itoa(i), State: a.state, CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			all = append([]string{rec.ID}, all...)
			if a.user == "ann" {
				ann = append([]string{rec.ID}, ann...)
			}
		}
		cases := []struct {
			name, state, user, before string
			want                      []string
		}{
			{"one user", "", "ann", "", ann},
			{"one user and a state", "pending", "ann", "", []string{ann[0], ann[2]}},
			{"one user after a cursor", "", "ann", ann[0], ann[1:]},
			{"an id no record has", "", "nobody", "", nil},
			{"every user", "", "", "", all},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := s.Approvals().PageByState(ctx, tc.state, tc.user, tc.before, 10)
				if err != nil {
					t.Fatal(err)
				}
				ids := make([]string, len(got))
				for i, a := range got {
					ids[i] = a.ID
				}
				if len(ids) != len(tc.want) {
					t.Fatalf("PageByState = %v, want %v", ids, tc.want)
				}
				for i := range ids {
					if ids[i] != tc.want[i] {
						t.Fatalf("PageByState = %v, want %v newest first", ids, tc.want)
					}
				}
			})
		}
	})
}
