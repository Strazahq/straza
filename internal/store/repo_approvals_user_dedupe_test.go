package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestFindOpenTicketByUser pins the user-scoped pending-ticket lookup:
// it finds the pending ticket a DIFFERENT session raised
// for the same (user, rule, argv), returns the newest when more than one row
// could match, and excludes hold rows, resolved rows, other users and other
// rules.
func TestFindOpenTicketByUser(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Microsecond)

		if _, err := s.Approvals().FindOpenTicketByUser(ctx, "u-kim", "r-1", "sha256:deploy"); !errors.Is(err, ErrNotFound) {
			t.Errorf("empty lookup = %v, want ErrNotFound", err)
		}

		mk := func(a Approval) Approval {
			a.CreatedAt, a.ExpiresAt = now, now.Add(24*time.Hour)
			out, err := s.Approvals().Insert(ctx, a)
			if err != nil {
				t.Fatalf("Insert %+v: %v", a, err)
			}
			return out
		}

		// The ticket an earlier (planning) session put in front of a human.
		want := mk(Approval{SessionID: "planning-sess", UserID: "u-kim", RuleID: "r-1",
			ArgvHash: "sha256:deploy", Class: "ticket", State: "pending"})

		// Distractors that must NOT be returned for (u-kim, r-1, sha256:deploy).
		mk(Approval{SessionID: "s-hold", UserID: "u-kim", RuleID: "r-1",
			ArgvHash: "sha256:deploy", State: "pending"}) // hold class (session-scoped by design)
		mk(Approval{SessionID: "s-approved", UserID: "u-kim", RuleID: "r-1",
			ArgvHash: "sha256:deploy2", Class: "ticket", State: "approved"})
		mk(Approval{SessionID: "s-denied", UserID: "u-kim", RuleID: "r-1",
			ArgvHash: "sha256:deploy3", Class: "ticket", State: "denied"})
		mk(Approval{SessionID: "s-expired", UserID: "u-kim", RuleID: "r-1",
			ArgvHash: "sha256:deploy4", Class: "ticket", State: "expired"})
		mk(Approval{SessionID: "s-other-user", UserID: "u-other", RuleID: "r-1",
			ArgvHash: "sha256:deploy", Class: "ticket", State: "pending"})
		mk(Approval{SessionID: "s-other-rule", UserID: "u-kim", RuleID: "r-2",
			ArgvHash: "sha256:deploy", Class: "ticket", State: "pending"})

		// A FRESH session asks: it finds the earlier session's ticket.
		got, err := s.Approvals().FindOpenTicketByUser(ctx, "u-kim", "r-1", "sha256:deploy")
		if err != nil {
			t.Fatalf("FindOpenTicketByUser: %v", err)
		}
		if got.ID != want.ID {
			t.Fatalf("FindOpenTicketByUser = %s (session %s, class %s), want %s",
				got.ID, got.SessionID, got.Class, want.ID)
		}

		// Resolved states never match, even for the exact tuple.
		if _, err := s.Approvals().FindOpenTicketByUser(ctx, "u-kim", "r-1", "sha256:deploy2"); !errors.Is(err, ErrNotFound) {
			t.Errorf("approved ticket matched a pending lookup: %v", err)
		}
		if _, err := s.Approvals().FindOpenTicketByUser(ctx, "u-kim", "r-1", "sha256:deploy3"); !errors.Is(err, ErrNotFound) {
			t.Errorf("denied ticket matched a pending lookup: %v", err)
		}
		if _, err := s.Approvals().FindOpenTicketByUser(ctx, "u-kim", "r-1", "sha256:deploy4"); !errors.Is(err, ErrNotFound) {
			t.Errorf("expired ticket matched a pending lookup: %v", err)
		}

		// A pending HOLD alone never satisfies a ticket lookup (holds stay
		// session-scoped: FindPendingByKey owns them).
		mk(Approval{SessionID: "s-hold-only", UserID: "u-hold", RuleID: "r-9",
			ArgvHash: "sha256:hold-only", State: "pending"})
		if _, err := s.Approvals().FindOpenTicketByUser(ctx, "u-hold", "r-9", "sha256:hold-only"); !errors.Is(err, ErrNotFound) {
			t.Errorf("hold row matched a ticket lookup: %v", err)
		}

		// Newest wins. The unique index forbids a second PENDING ticket for the
		// tuple, so the "more than one candidate" case is reached the way a real
		// database reaches it: the older row resolved first, then a new ticket was
		// raised for the same call.
		if won, err := s.Approvals().MarkDecided(ctx, want.ID, "denied", "u-appr", "Ada", "console", now, nil, "", ""); err != nil || !won {
			t.Fatalf("MarkDecided = %v, %v", won, err)
		}
		newest := mk(Approval{SessionID: "third-sess", UserID: "u-kim", RuleID: "r-1",
			ArgvHash: "sha256:deploy", Class: "ticket", State: "pending"})
		got, err = s.Approvals().FindOpenTicketByUser(ctx, "u-kim", "r-1", "sha256:deploy")
		if err != nil || got.ID != newest.ID {
			t.Fatalf("newest lookup = %s, %v, want %s", got.ID, err, newest.ID)
		}
	})
}

// TestPendingTicketUserUniqueIndex pins idx_approvals_pending_ticket_user:
// the store itself refuses a second pending ticket for one
// (user, rule, argv) even from a different session, the race window the
// service-side dedupe cannot close on its own. Different users, rules and
// fingerprints are unaffected, and hold rows are NOT constrained (two sessions
// may each hold their own).
func TestPendingTicketUserUniqueIndex(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Microsecond)

		ticket := Approval{
			SessionID: "sess-A", UserID: "u-kim", RuleID: "r-1", ArgvHash: "sha256:deploy",
			Class: "ticket", State: "pending", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
		}
		if _, err := s.Approvals().Insert(ctx, ticket); err != nil {
			t.Fatalf("Insert first ticket: %v", err)
		}

		// A FRESH session, same user + rule + call: blocked at the store.
		dup := ticket
		dup.SessionID = "sess-B"
		if _, err := s.Approvals().Insert(ctx, dup); !errors.Is(err, ErrConflict) {
			t.Errorf("cross-session duplicate pending ticket = %v, want ErrConflict", err)
		}

		// Not blocked: another user, another rule, another fingerprint.
		otherUser := dup
		otherUser.UserID = "u-other"
		if _, err := s.Approvals().Insert(ctx, otherUser); err != nil {
			t.Errorf("another user's ticket must be allowed: %v", err)
		}
		otherRule := dup
		otherRule.RuleID = "r-2"
		if _, err := s.Approvals().Insert(ctx, otherRule); err != nil {
			t.Errorf("another rule's ticket must be allowed: %v", err)
		}
		otherKey := dup
		otherKey.ArgvHash = "sha256:other"
		if _, err := s.Approvals().Insert(ctx, otherKey); err != nil {
			t.Errorf("another fingerprint's ticket must be allowed: %v", err)
		}

		// Hold rows are untouched by the new index: two sessions each get their
		// own pending hold for the same user + rule + call (only the older
		// session-scoped index constrains them, and these differ by session).
		// Fresh session ids, since idx_approvals_pending_key is class-agnostic
		// and sess-A/sess-B already carry pending rows for this tuple.
		hold := Approval{
			SessionID: "sess-H1", UserID: "u-kim", RuleID: "r-1", ArgvHash: "sha256:deploy",
			State: "pending", CreatedAt: now, ExpiresAt: now.Add(90 * time.Second),
		}
		if _, err := s.Approvals().Insert(ctx, hold); err != nil {
			t.Fatalf("Insert first hold: %v", err)
		}
		hold2 := hold
		hold2.SessionID = "sess-H2"
		if _, err := s.Approvals().Insert(ctx, hold2); err != nil {
			t.Errorf("a second session's hold must be allowed (holds are session-scoped): %v", err)
		}

		// Resolving the winner frees the tuple: the call can be ticketed again.
		open, err := s.Approvals().FindOpenTicketByUser(ctx, "u-kim", "r-1", "sha256:deploy")
		if err != nil {
			t.Fatalf("FindOpenTicketByUser: %v", err)
		}
		if won, err := s.Approvals().MarkDecided(ctx, open.ID, "approved", "u-appr", "Ada", "console", now, nil, "", ""); err != nil || !won {
			t.Fatalf("MarkDecided = %v, %v", won, err)
		}
		again := ticket
		again.SessionID = "sess-C"
		if _, err := s.Approvals().Insert(ctx, again); err != nil {
			t.Errorf("a resolved tuple must leave the pending index: %v", err)
		}
	})
}
