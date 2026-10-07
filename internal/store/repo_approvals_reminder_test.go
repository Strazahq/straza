package store

import (
	"context"
	"testing"
	"time"
)

// reminderIDs is a small helper for readable assertions on reminder-sweep rows.
func reminderIDs(rows []Approval) []string {
	out := make([]string, len(rows))
	for i, a := range rows {
		out[i] = a.ArgvHash // argv is the per-row label in these tests
	}
	return out
}

// TestListTicketsForReminder pins the near-expiry reminder sweep input: only
// pending, ticket-class rows whose expiry falls in (now, dueBefore]. It excludes
// a past-expiry row, a row already reminded (channel_refs carries the marker), a
// hold, a pending ticket still outside the window, and a resolved ticket; the
// result is ordered by expiry ascending and honors the limit. Existing notifier
// refs on an unreminded row survive the read.
func TestListTicketsForReminder(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Microsecond)
		dueBefore := now.Add(2 * time.Hour)

		mk := func(argv, class, state string, expires time.Time, refs map[string]string) Approval {
			a, err := s.Approvals().Insert(ctx, Approval{
				SessionID: "s-" + argv, UserID: "u", RuleID: "r", ArgvHash: argv, Class: class,
				State: state, CreatedAt: now.Add(-24 * time.Hour), ExpiresAt: expires,
				ChannelRefs: refs,
			})
			if err != nil {
				t.Fatalf("Insert %s: %v", argv, err)
			}
			return a
		}

		// The wanted set: in-window pending tickets (due within the next 2h).
		inA := mk("in-a", "ticket", "pending", now.Add(30*time.Minute), nil)
		inB := mk("in-b", "ticket", "pending", now.Add(90*time.Minute), map[string]string{"slack_ts": "111"})
		// Excluded distractors.
		mk("past", "ticket", "pending", now.Add(-time.Minute), nil) // already past expiry
		mk("far", "ticket", "pending", now.Add(6*time.Hour), nil)   // outside the window
		mk("marked", "ticket", "pending", now.Add(45*time.Minute),  // already reminded
			map[string]string{"reminder_pushed_at": now.Format(time.RFC3339)})
		mk("hold", "hold", "pending", now.Add(20*time.Minute), nil)        // wrong class
		mk("resolved", "ticket", "approved", now.Add(30*time.Minute), nil) // not pending

		got, err := s.Approvals().ListTicketsForReminder(ctx, now, dueBefore, 256)
		if err != nil {
			t.Fatalf("ListTicketsForReminder: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d rows, want 2 (in-a,in-b): %v", len(got), reminderIDs(got))
		}
		// Ordered by expiry ascending: inA (30m) before inB (90m).
		if got[0].ID != inA.ID || got[1].ID != inB.ID {
			t.Errorf("order = %v, want [in-a in-b] by expiry", reminderIDs(got))
		}
		// Existing refs on an unreminded row survive the read.
		if got[1].ChannelRefs["slack_ts"] != "111" {
			t.Errorf("in-b slack_ts = %q, want 111", got[1].ChannelRefs["slack_ts"])
		}

		// The limit is respected (soonest-expiry first).
		lim, err := s.Approvals().ListTicketsForReminder(ctx, now, dueBefore, 1)
		if err != nil {
			t.Fatalf("ListTicketsForReminder limit: %v", err)
		}
		if len(lim) != 1 || lim[0].ID != inA.ID {
			t.Errorf("limit=1 = %v, want [in-a]", reminderIDs(lim))
		}
	})
}

// TestClaimTicketReminder pins the atomic single-reminder claim (same discipline
// as ClaimExpired): the first claim wins and stamps the reminder_pushed_at marker
// into channel_refs (surviving a scan round-trip alongside a pre-existing ref); a
// second claim on the now-marked row loses (the NOT LIKE guard now matches); and
// a claim on a non-pending row loses (state guard).
func TestClaimTicketReminder(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Microsecond)

		tk, err := s.Approvals().Insert(ctx, Approval{
			SessionID: "s", UserID: "u", RuleID: "r", ArgvHash: "h-tk", Class: "ticket",
			State: "pending", CreatedAt: now.Add(-24 * time.Hour), ExpiresAt: now.Add(time.Hour),
			ChannelRefs: map[string]string{"slack_ts": "999"},
		})
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}

		// The refs the service submits: the row's map cloned + the marker added.
		refs := map[string]string{"slack_ts": "999", "reminder_pushed_at": now.Format(time.RFC3339)}
		won, err := s.Approvals().ClaimTicketReminder(ctx, tk.ID, refs)
		if err != nil || !won {
			t.Fatalf("first claim = %v, %v (want won)", won, err)
		}
		// Marker + pre-existing ref both survive the round-trip (decodeStringMap).
		got, _ := s.Approvals().GetByID(ctx, tk.ID)
		if got.ChannelRefs["reminder_pushed_at"] == "" {
			t.Errorf("reminder_pushed_at not persisted: %+v", got.ChannelRefs)
		}
		if got.ChannelRefs["slack_ts"] != "999" {
			t.Errorf("existing slack_ts lost: %+v", got.ChannelRefs)
		}

		// Second claim loses: the stamped marker trips the NOT LIKE guard.
		won, err = s.Approvals().ClaimTicketReminder(ctx, tk.ID, refs)
		if err != nil || won {
			t.Fatalf("second claim = %v, %v (want lost)", won, err)
		}

		// A claim on a non-pending row loses (state guard).
		resolved, err := s.Approvals().Insert(ctx, Approval{
			SessionID: "s2", UserID: "u", RuleID: "r", ArgvHash: "h-res", Class: "ticket",
			State: "approved", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		})
		if err != nil {
			t.Fatalf("Insert resolved: %v", err)
		}
		if won, _ := s.Approvals().ClaimTicketReminder(ctx, resolved.ID,
			map[string]string{"reminder_pushed_at": now.Format(time.RFC3339)}); won {
			t.Error("claim on a non-pending row must lose")
		}
	})
}
