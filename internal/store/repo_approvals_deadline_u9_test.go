package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestUseDeadlineComparedAsTime pins that the use window of a hold and of a
// ticket is judged as a time on both drivers, by FindConsumableHold,
// FindConsumableGrant and MarkConsumed alike. SQLite keeps a time as
// RFC3339Nano text, which drops trailing zeros, so a deadline on a whole
// second compared as text reads as later than every moment of that second.
// SQLite's time functions round to the millisecond, so there a use that
// rounds to the deadline's millisecond is refused: early by under two
// milliseconds, never late.
func TestUseDeadlineComparedAsTime(t *testing.T) {
	whole := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	frac := whole.Add(250*time.Millisecond + 700*time.Microsecond)
	cases := []struct {
		name     string
		deadline *time.Time
		// raw, when set, overwrites grant_expires_at with this exact text,
		// the form the previous build wrote.
		raw  string
		asOf time.Time
		want bool
		// sameMillisecond marks a use that rounds to the deadline's
		// millisecond, which SQLite refuses although it comes first.
		sameMillisecond bool
	}{
		{name: "a use 300 ms after a deadline on a whole second is refused", deadline: &whole, asOf: whole.Add(300 * time.Millisecond)},
		{name: "a use one microsecond after a deadline on a whole second is refused", deadline: &whole, asOf: whole.Add(time.Microsecond)},
		{name: "a use at a deadline on a whole second is refused", deadline: &whole, asOf: whole},
		{name: "a use 300 ms before a deadline on a whole second is allowed", deadline: &whole, asOf: whole.Add(-300 * time.Millisecond), want: true},
		{name: "a use one millisecond before a fractional deadline is allowed", deadline: &frac, asOf: frac.Add(-time.Millisecond), want: true},
		{name: "a use one microsecond before a fractional deadline is allowed on Postgres and refused on SQLite, in the same millisecond", deadline: &frac, asOf: frac.Add(-time.Microsecond), want: true, sameMillisecond: true},
		{name: "a use one microsecond after a fractional deadline is refused", deadline: &frac, asOf: frac.Add(time.Microsecond)},
		{name: "a previous build's whole-second row, used 300 ms after, is refused", deadline: &whole, raw: "2026-09-29T10:00:00Z", asOf: whole.Add(300 * time.Millisecond)},
		{name: "a previous build's whole-second row, used 300 ms before, is allowed", deadline: &whole, raw: "2026-09-29T10:00:00Z", asOf: whole.Add(-300 * time.Millisecond), want: true},
		{name: "a previous build's nanosecond row, used one millisecond after, is refused", deadline: &frac, raw: "2026-09-29T10:00:00.250700001Z", asOf: frac.Add(time.Millisecond)},
		{name: "a previous build's nanosecond row, used one millisecond before, is allowed", deadline: &frac, raw: "2026-09-29T10:00:00.250700001Z", asOf: frac.Add(-time.Millisecond), want: true},
		{name: "a row with no deadline is never used", asOf: whole.Add(-time.Hour)},
	}
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		sq := s.(*sqlStore)
		for i, tc := range cases {
			for _, class := range []string{"hold", "ticket"} {
				t.Run(class+": "+tc.name, func(t *testing.T) {
					want := tc.want && (sq.d != dialectSQLite || !tc.sameMillisecond)
					row, err := s.Approvals().Insert(ctx, Approval{
						SessionID: "s-1", UserID: "u-kim", RuleID: "r-1",
						ArgvHash: fmt.Sprintf("v2:call:sha256:u9-%02d-%s", i, class), Class: class,
						State: "approved", CreatedAt: whole.Add(-time.Hour), ExpiresAt: whole.Add(-30 * time.Minute),
						GrantExpiresAt: tc.deadline,
					})
					if err != nil {
						t.Fatalf("Insert: %v", err)
					}
					if tc.raw != "" {
						if _, err := sq.exec(ctx, `UPDATE approvals SET grant_expires_at = $1 WHERE id = $2`, tc.raw, row.ID); err != nil {
							t.Fatalf("write the raw deadline %q: %v", tc.raw, err)
						}
					}
					var found Approval
					if class == "hold" {
						found, err = s.Approvals().FindConsumableHold(ctx, row.UserID, row.SessionID, row.RuleID, row.ArgvHash, tc.asOf)
					} else {
						found, err = s.Approvals().FindConsumableGrant(ctx, row.UserID, row.ArgvHash, tc.asOf)
					}
					switch {
					case want && (err != nil || found.ID != row.ID):
						t.Errorf("find = %s, %v, want the row %s", found.ID, err, row.ID)
					case !want && !errors.Is(err, ErrNotFound):
						t.Errorf("find = %s, %v, want ErrNotFound", found.ID, err)
					}
					if won, err := s.Approvals().MarkConsumed(ctx, row.ID, "s-use", tc.asOf, tc.asOf); err != nil || won != want {
						t.Errorf("MarkConsumed = %v, %v, want %v", won, err, want)
					}
				})
			}
		}
	})
}
