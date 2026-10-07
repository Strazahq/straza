package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestFindConsumableHold pins the retry lookup of a hold's approval on both
// drivers: it finds the approved, unused hold of the exact user, session,
// rule and argv hash whose use deadline is ahead, and no other row. Each case
// inserts one row on a key of its own and looks it up with the unedited key.
func TestFindConsumableHold(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Microsecond)
		ahead, past := now.Add(time.Minute), now.Add(-time.Minute)
		used := now.Add(-time.Second)
		cases := []struct {
			name string
			edit func(a *Approval)
			want bool
		}{
			{"an approved unused hold inside its window is found", func(a *Approval) {}, true},
			{"a pending hold is not", func(a *Approval) { a.State = "pending" }, false},
			{"a denied hold is not", func(a *Approval) { a.State = "denied" }, false},
			{"a hold whose window closed is not", func(a *Approval) { a.GrantExpiresAt = &past }, false},
			{"a hold approved with no deadline by an older build is not", func(a *Approval) { a.GrantExpiresAt = nil }, false},
			{"a hold already used is not", func(a *Approval) { a.ConsumedAt, a.ConsumedBy = &used, "s-1" }, false},
			{"another session's hold is not", func(a *Approval) { a.SessionID = "s-other" }, false},
			{"a hold of another rule is not", func(a *Approval) { a.RuleID = "r-other" }, false},
			{"a hold of another call is not", func(a *Approval) { a.ArgvHash += "-other" }, false},
			{"another user's hold is not", func(a *Approval) { a.UserID = "u-other" }, false},
			{"a ticket on the same key is not", func(a *Approval) { a.Class = "ticket" }, false},
		}
		for i, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				key := Approval{
					SessionID: "s-1", UserID: "u-kim", RuleID: "r-1", ArgvHash: fmt.Sprintf("v2:call:sha256:%02d", i),
					Class: "hold", State: "approved", CreatedAt: now, ExpiresAt: now.Add(90 * time.Second),
					GrantExpiresAt: &ahead,
				}
				row := key
				tc.edit(&row)
				in, err := s.Approvals().Insert(ctx, row)
				if err != nil {
					t.Fatalf("Insert: %v", err)
				}
				got, err := s.Approvals().FindConsumableHold(ctx, key.UserID, key.SessionID, key.RuleID, key.ArgvHash, now)
				switch {
				case tc.want && (err != nil || got.ID != in.ID):
					t.Fatalf("FindConsumableHold = %s, %v, want the row %s", got.ID, err, in.ID)
				case !tc.want && !errors.Is(err, ErrNotFound):
					t.Fatalf("FindConsumableHold = %s, %v, want ErrNotFound", got.ID, err)
				}
			})
		}
	})
}

// TestMarkConsumedHold pins the one-use gate on hold rows: the first use of
// an approved hold with a deadline wins and records the session, a second
// use loses, and a denied hold or a hold with no deadline never wins.
func TestMarkConsumedHold(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Microsecond)
		ahead := now.Add(time.Minute)
		mk := func(argv, state string, deadline *time.Time) Approval {
			a, err := s.Approvals().Insert(ctx, Approval{
				SessionID: "s-1", UserID: "u-kim", RuleID: "r-1", ArgvHash: argv, Class: "hold",
				State: state, CreatedAt: now, ExpiresAt: now.Add(90 * time.Second), GrantExpiresAt: deadline,
			})
			if err != nil {
				t.Fatalf("Insert %s: %v", argv, err)
			}
			return a
		}
		live := mk("h-live", "approved", &ahead)
		for i, want := range []bool{true, false} {
			won, err := s.Approvals().MarkConsumed(ctx, live.ID, fmt.Sprintf("s-use-%d", i), now, now)
			if err != nil || won != want {
				t.Fatalf("use %d = %v, %v, want %v", i, won, err, want)
			}
		}
		if after, _ := s.Approvals().GetByID(ctx, live.ID); after.ConsumedAt == nil || after.ConsumedBy != "s-use-0" || after.State != "approved" {
			t.Errorf("used hold = %+v, want consumedBy s-use-0 and state approved", after)
		}
		for _, row := range []Approval{mk("h-denied", "denied", &ahead), mk("h-legacy", "approved", nil)} {
			if won, err := s.Approvals().MarkConsumed(ctx, row.ID, "s-use", now, now); won || err != nil {
				t.Errorf("use of %s = %v, %v, want false", row.ArgvHash, won, err)
			}
		}
	})
}

// TestMarkConsumedAsOf pins that the use window is judged at asOf while
// consumed_at records now: a held call judged at its decision uses an
// approval after the retry window closed and records the real time of the
// use, and the same use judged at that later time loses.
func TestMarkConsumedAsOf(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		decided := time.Now().UTC().Truncate(time.Microsecond)
		deadline := decided.Add(time.Second)
		late := decided.Add(10 * time.Second)
		cases := []struct {
			name string
			asOf time.Time
			want bool
		}{
			{"judged at the decision, a late use wins", decided, true},
			{"judged when it runs, the same late use loses", late, false},
			{"judged at the deadline itself, the use loses", deadline, false},
		}
		for i, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				row, err := s.Approvals().Insert(ctx, Approval{
					SessionID: "s-1", UserID: "u-kim", RuleID: "r-1", ArgvHash: fmt.Sprintf("h-asof-%d", i), Class: "hold",
					State: "approved", CreatedAt: decided, ExpiresAt: decided.Add(90 * time.Second),
					DecidedAt: &decided, GrantExpiresAt: &deadline,
				})
				if err != nil {
					t.Fatalf("Insert: %v", err)
				}
				won, err := s.Approvals().MarkConsumed(ctx, row.ID, "s-1", late, tc.asOf)
				if err != nil || won != tc.want {
					t.Fatalf("MarkConsumed = %v, %v, want %v", won, err, tc.want)
				}
				after, _ := s.Approvals().GetByID(ctx, row.ID)
				switch {
				case tc.want && (after.ConsumedAt == nil || !after.ConsumedAt.Equal(late)):
					t.Errorf("consumed_at = %v, want the real time of the use %v", after.ConsumedAt, late)
				case !tc.want && after.ConsumedAt != nil:
					t.Errorf("consumed_at = %v, want the approval unused", after.ConsumedAt)
				}
			})
		}
	})
}
