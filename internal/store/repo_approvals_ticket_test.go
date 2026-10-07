package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestApprovalTicketFields pins the revision-6 ticket columns round-tripping
// through Insert/GetByID on both dialects: class, and the nullable
// grant_expires_at / consumed_at / consumed_by. A plain (hold) insert leaves
// class defaulting to 'hold' and the consume columns NULL.
func TestApprovalTicketFields(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Microsecond)

		// A hold row (no Class set): class defaults 'hold', consume cols nil.
		hold, err := s.Approvals().Insert(ctx, Approval{
			SessionID: "s-hold", RuleID: "r", ArgvHash: "h-hold", State: "pending",
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		})
		if err != nil {
			t.Fatalf("Insert hold: %v", err)
		}
		got, err := s.Approvals().GetByID(ctx, hold.ID)
		if err != nil {
			t.Fatalf("GetByID hold: %v", err)
		}
		if got.Class != "hold" {
			t.Errorf("hold class = %q, want hold (default)", got.Class)
		}
		if got.ConsumedAt != nil || got.ConsumedBy != "" || got.GrantExpiresAt != nil {
			t.Errorf("hold row must have nil consume columns: %+v", got)
		}

		// A ticket row already approved with a live grant window.
		grantExp := now.Add(time.Hour)
		tk, err := s.Approvals().Insert(ctx, Approval{
			SessionID: "s-tk", UserID: "u-req", ArgvHash: "h-tk", Class: "ticket",
			State: "approved", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
			GrantExpiresAt: &grantExp,
		})
		if err != nil {
			t.Fatalf("Insert ticket: %v", err)
		}
		gt, err := s.Approvals().GetByID(ctx, tk.ID)
		if err != nil {
			t.Fatalf("GetByID ticket: %v", err)
		}
		if gt.Class != "ticket" {
			t.Errorf("ticket class = %q, want ticket", gt.Class)
		}
		if gt.GrantExpiresAt == nil || !gt.GrantExpiresAt.Equal(grantExp) {
			t.Errorf("grantExpiresAt round-trip: got %v want %v", gt.GrantExpiresAt, grantExp)
		}
		if gt.ConsumedAt != nil || gt.ConsumedBy != "" {
			t.Errorf("fresh ticket must be unconsumed: %+v", gt)
		}
	})
}

// TestMarkDecidedStampsGrant pins the grant stamp at the store level:
// grant_ttl_seconds round-trips, and MarkDecided with a non-nil grantExpiresAt
// stamps grant_expires_at atomically with the approve flip so the ticket
// becomes consumable, while a nil deadline leaves the column NULL.
func TestMarkDecidedStampsGrant(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Microsecond)

		// A pending ticket carrying its configured consume window.
		tk, err := s.Approvals().Insert(ctx, Approval{
			SessionID: "s", UserID: "u-req", RuleID: "r", ArgvHash: "h-tk", Class: "ticket",
			State: "pending", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
			GrantTTLSeconds: 3600,
		})
		if err != nil {
			t.Fatalf("Insert ticket: %v", err)
		}
		if got, _ := s.Approvals().GetByID(ctx, tk.ID); got.GrantTTLSeconds != 3600 {
			t.Errorf("grant_ttl_seconds round-trip = %d, want 3600", got.GrantTTLSeconds)
		}
		// A pending ticket has no grant deadline yet → not consumable.
		if _, err := s.Approvals().FindConsumableGrant(ctx, "u-req", "h-tk", now); !errors.Is(err, ErrNotFound) {
			t.Errorf("pending ticket must not be consumable: %v", err)
		}

		// Approve with a materialized deadline (decidedAt + grantTTL).
		at := now.Add(time.Minute)
		grantExp := at.Add(3600 * time.Second)
		won, err := s.Approvals().MarkDecided(ctx, tk.ID, "approved", "u-dec", "Ada", "console", at, &grantExp, "", "")
		if err != nil || !won {
			t.Fatalf("MarkDecided = %v, %v (want won)", won, err)
		}
		got, _ := s.Approvals().GetByID(ctx, tk.ID)
		if got.State != "approved" || got.DecidedAt == nil {
			t.Fatalf("decided ticket = %+v", got)
		}
		if got.GrantExpiresAt == nil || !got.GrantExpiresAt.Equal(grantExp) {
			t.Errorf("grant_expires_at = %v, want %v", got.GrantExpiresAt, grantExp)
		}
		// The approved ticket is consumable.
		if _, err := s.Approvals().FindConsumableGrant(ctx, "u-req", "h-tk", at.Add(time.Minute)); err != nil {
			t.Errorf("approved ticket not consumable after stamp: %v", err)
		}

		// A nil deadline leaves grant_expires_at NULL, the row an older build
		// left for an approved hold, so that hold is never a consumable grant.
		hold, err := s.Approvals().Insert(ctx, Approval{
			SessionID: "s2", UserID: "u-req", RuleID: "r", ArgvHash: "h-hold", State: "pending",
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		})
		if err != nil {
			t.Fatalf("Insert hold: %v", err)
		}
		if _, err := s.Approvals().MarkDecided(ctx, hold.ID, "approved", "u-dec", "Ada", "console", at, nil, "", ""); err != nil {
			t.Fatalf("MarkDecided hold: %v", err)
		}
		if gh, _ := s.Approvals().GetByID(ctx, hold.ID); gh.GrantExpiresAt != nil {
			t.Errorf("a nil deadline must not stamp one: %v", gh.GrantExpiresAt)
		}
	})
}

// TestFindLatestTicketByKey pins the terminal deny-final lookup: it returns the
// newest ticket row for (user, rule, argv) regardless of state, ErrNotFound
// when none, and never matches a hold row.
func TestFindLatestTicketByKey(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Microsecond)

		if _, err := s.Approvals().FindLatestTicketByKey(ctx, "u", "r", "h-none"); !errors.Is(err, ErrNotFound) {
			t.Errorf("empty lookup = %v, want ErrNotFound", err)
		}

		mk := func(state string, created time.Time) Approval {
			a, err := s.Approvals().Insert(ctx, Approval{
				SessionID: "s", UserID: "u", RuleID: "r", ArgvHash: "h", Class: "ticket",
				State: state, CreatedAt: created, ExpiresAt: created.Add(24 * time.Hour),
			})
			if err != nil {
				t.Fatalf("Insert %s: %v", state, err)
			}
			return a
		}
		// Older denied, then newer pending (UUIDv7 ids ascend in insert order).
		_ = mk("denied", now.Add(-2*time.Hour))
		newest := mk("pending", now.Add(-time.Hour))

		got, err := s.Approvals().FindLatestTicketByKey(ctx, "u", "r", "h")
		if err != nil {
			t.Fatalf("FindLatestTicketByKey: %v", err)
		}
		if got.ID != newest.ID || got.State != "pending" {
			t.Errorf("latest = %s/%s, want newest %s/pending", got.ID, got.State, newest.ID)
		}

		// A hold row with the same shape is never returned (class filter).
		if _, err := s.Approvals().Insert(ctx, Approval{
			SessionID: "s", UserID: "u2", RuleID: "r2", ArgvHash: "h2", State: "denied",
			CreatedAt: now, ExpiresAt: now.Add(time.Hour), // class defaults 'hold'
		}); err != nil {
			t.Fatalf("Insert hold: %v", err)
		}
		if _, err := s.Approvals().FindLatestTicketByKey(ctx, "u2", "r2", "h2"); !errors.Is(err, ErrNotFound) {
			t.Errorf("hold row must not match a ticket lookup: %v", err)
		}
	})
}

// TestMarkConsumedSingleUse pins the atomic single-use consume gate: exactly
// one MarkConsumed wins on a live approved grant; it flips consumed_at/by; a
// second call loses; a pending row or an expired grant is never consumable.
func TestMarkConsumedSingleUse(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Microsecond)

		mkTicket := func(argv, state string, grantExp time.Time) Approval {
			a, err := s.Approvals().Insert(ctx, Approval{
				SessionID: "s", UserID: "u-req", ArgvHash: argv, Class: "ticket",
				State: state, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
				GrantExpiresAt: &grantExp,
			})
			if err != nil {
				t.Fatalf("Insert %s: %v", argv, err)
			}
			return a
		}

		live := mkTicket("h-live", "approved", now.Add(time.Hour))
		won, err := s.Approvals().MarkConsumed(ctx, live.ID, "sess-A", now, now)
		if err != nil || !won {
			t.Fatalf("MarkConsumed first = %v, %v (want won)", won, err)
		}
		// Second consume loses (already consumed).
		won, err = s.Approvals().MarkConsumed(ctx, live.ID, "sess-B", now.Add(time.Second), now.Add(time.Second))
		if err != nil || won {
			t.Fatalf("MarkConsumed second = %v, %v (want lost)", won, err)
		}
		after, _ := s.Approvals().GetByID(ctx, live.ID)
		if after.ConsumedAt == nil || after.ConsumedBy != "sess-A" || after.State != "approved" {
			t.Errorf("consumed record = %+v (want consumedBy sess-A, state still approved)", after)
		}

		// A pending ticket is not consumable (grant only lives after approval).
		pending := mkTicket("h-pending", "pending", now.Add(time.Hour))
		if won, _ := s.Approvals().MarkConsumed(ctx, pending.ID, "sess-C", now, now); won {
			t.Error("pending ticket must not be consumable")
		}
		// An approved-but-expired grant (grant_expires_at in the past) is dead.
		expired := mkTicket("h-expired", "approved", now.Add(-time.Minute))
		if won, _ := s.Approvals().MarkConsumed(ctx, expired.ID, "sess-D", now, now); won {
			t.Error("expired grant must not be consumable")
		}
		// A DENIED ticket is never consumable, even inside a live grant window:
		// the state guard, not the deadline, is what refuses it (a denial must
		// never be convertible into a grant).
		denied := mkTicket("h-denied", "denied", now.Add(time.Hour))
		if won, err := s.Approvals().MarkConsumed(ctx, denied.ID, "sess-E", now, now); won || err != nil {
			t.Errorf("denied ticket consume = %v, %v (want false, nil)", won, err)
		}
		if after, _ := s.Approvals().GetByID(ctx, denied.ID); after.ConsumedAt != nil || after.State != "denied" {
			t.Errorf("refused consume must leave the denied row untouched: %+v", after)
		}
	})
}

// TestFindConsumableGrant pins the retry-path lookup: it returns the live,
// unconsumed, unexpired ticket grant bound to (user, argvHash) and excludes
// consumed, expired, wrong-user, and hold rows.
func TestFindConsumableGrant(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Microsecond)
		grantExp := now.Add(time.Hour)

		mk := func(a Approval) Approval {
			a.CreatedAt, a.ExpiresAt = now, now.Add(24*time.Hour)
			out, err := s.Approvals().Insert(ctx, a)
			if err != nil {
				t.Fatalf("Insert: %v", err)
			}
			return out
		}
		want := mk(Approval{SessionID: "s1", UserID: "u-kim", ArgvHash: "sha256:deploy",
			Class: "ticket", State: "approved", GrantExpiresAt: &grantExp})
		// Distractors that must NOT be returned for (u-kim, sha256:deploy).
		past := now.Add(-time.Minute)
		mk(Approval{SessionID: "s2", UserID: "u-kim", ArgvHash: "sha256:deploy",
			Class: "ticket", State: "approved", GrantExpiresAt: &past}) // expired grant
		mk(Approval{SessionID: "s3", UserID: "u-other", ArgvHash: "sha256:deploy",
			Class: "ticket", State: "approved", GrantExpiresAt: &grantExp}) // wrong user
		mk(Approval{SessionID: "s4", UserID: "u-kim", ArgvHash: "sha256:other",
			Class: "ticket", State: "approved", GrantExpiresAt: &grantExp}) // wrong fingerprint
		// A hold row on the exact tuple, carrying a grant deadline it could
		// never legitimately have: only the class filter keeps it out, so this
		// pins the doc comment's "a hold is never a consumable grant" claim
		// even against a row that satisfies every other predicate.
		mk(Approval{SessionID: "s5", UserID: "u-kim", ArgvHash: "sha256:deploy",
			State: "approved", GrantExpiresAt: &grantExp}) // class defaults 'hold'

		got, err := s.Approvals().FindConsumableGrant(ctx, "u-kim", "sha256:deploy", now)
		if err != nil {
			t.Fatalf("FindConsumableGrant: %v", err)
		}
		if got.ID != want.ID {
			t.Fatalf("FindConsumableGrant = %s, want %s", got.ID, want.ID)
		}

		// Once consumed, the grant is no longer findable.
		if _, err := s.Approvals().MarkConsumed(ctx, want.ID, "sess-X", now, now); err != nil {
			t.Fatalf("MarkConsumed: %v", err)
		}
		if _, err := s.Approvals().FindConsumableGrant(ctx, "u-kim", "sha256:deploy", now); !errors.Is(err, ErrNotFound) {
			t.Errorf("consumed grant still findable: %v", err)
		}
	})
}

// TestMarkConsumedRace is the double-consume race: two goroutines consume the
// same live grant concurrently; exactly one wins (single-use holds under
// concurrency). Runs under -race in the gate.
func TestMarkConsumedRace(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Microsecond)
		grantExp := now.Add(time.Hour)
		tk, err := s.Approvals().Insert(ctx, Approval{
			SessionID: "s", UserID: "u", ArgvHash: "h", Class: "ticket", State: "approved",
			CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), GrantExpiresAt: &grantExp,
		})
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}

		const racers = 8
		var wg sync.WaitGroup
		results := make(chan bool, racers)
		start := make(chan struct{})
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func(who int) {
				defer wg.Done()
				<-start
				won, err := s.Approvals().MarkConsumed(ctx, tk.ID, "sess", now, now)
				if err != nil {
					t.Errorf("MarkConsumed error: %v", err)
				}
				results <- won
			}(i)
		}
		close(start)
		wg.Wait()
		close(results)

		wins := 0
		for won := range results {
			if won {
				wins++
			}
		}
		if wins != 1 {
			t.Fatalf("double-consume race: %d winners, want exactly 1", wins)
		}
	})
}
