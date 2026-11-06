package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestFindLatestHoldByKey pins the lookup of a hold's last decision on both
// drivers: it finds the newest hold of the exact user, session, rule and
// argv hash in any state, and no other row. Each case inserts its rows on a
// key of its own and looks the key up unedited. With two rows, the second
// is newer, because ids ascend in insert order.
func TestFindLatestHoldByKey(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Microsecond)
		cases := []struct {
			name        string
			older, edit func(a *Approval) // older is nil for a case with one row
			want        string            // the state of the row found, "" for none
		}{
			{"a denied hold is found", nil, func(a *Approval) {}, "denied"},
			{"a pending hold is found", nil, func(a *Approval) { a.State = "pending" }, "pending"},
			{"an approved hold is found", nil, func(a *Approval) { a.State = "approved" }, "approved"},
			{"an expired hold is found", nil, func(a *Approval) { a.State = "expired" }, "expired"},
			{"the newer of two holds is found", func(a *Approval) { a.State = "approved" }, func(a *Approval) {}, "denied"},
			{"a newer approval wins over an older denial", func(a *Approval) {}, func(a *Approval) { a.State = "approved" }, "approved"},
			{"another session's hold is not", nil, func(a *Approval) { a.SessionID = "s-other" }, ""},
			{"a hold of another rule is not", nil, func(a *Approval) { a.RuleID = "r-other" }, ""},
			{"a hold of another call is not", nil, func(a *Approval) { a.ArgvHash += "-other" }, ""},
			{"another user's hold is not", nil, func(a *Approval) { a.UserID = "u-other" }, ""},
			{"a ticket on the same key is not", nil, func(a *Approval) { a.Class = "ticket" }, ""},
		}
		for i, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				key := Approval{
					SessionID: "s-1", UserID: "u-kim", RuleID: "r-1", ArgvHash: fmt.Sprintf("v2:call:sha256:%02d", i),
					Class: "hold", State: "denied", CreatedAt: now, ExpiresAt: now.Add(90 * time.Second),
				}
				insert := func(edit func(a *Approval)) Approval {
					row := key
					edit(&row)
					in, err := s.Approvals().Insert(ctx, row)
					if err != nil {
						t.Fatalf("Insert: %v", err)
					}
					return in
				}
				if tc.older != nil {
					insert(tc.older)
				}
				in := insert(tc.edit)
				got, err := s.Approvals().FindLatestHoldByKey(ctx, key.UserID, key.SessionID, key.RuleID, key.ArgvHash)
				switch {
				case tc.want != "" && (err != nil || got.ID != in.ID || got.State != tc.want):
					t.Fatalf("FindLatestHoldByKey = %s/%s, %v, want the row %s/%s", got.ID, got.State, err, in.ID, tc.want)
				case tc.want == "" && !errors.Is(err, ErrNotFound):
					t.Fatalf("FindLatestHoldByKey = %s, %v, want ErrNotFound", got.ID, err)
				}
			})
		}
	})
}
