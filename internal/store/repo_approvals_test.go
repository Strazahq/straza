package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestApprovalsLifecycle(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Microsecond)

		seed := Approval{
			SessionID: "s-1", UserID: "u-req", Username: "nova", RuleID: "r-1",
			SetName: "guardrails", ArgvHash: "sha256:abc", Lane: "hook",
			Summary: "mcp.call midpoint:disable_user", Justification: "offboarding",
			ApproverRoles: []string{"sec-approvers", "straza-admin"}, SelfApproval: false,
			TimeoutSeconds: 90, RetryTTLSeconds: 60, State: "pending",
			ChannelRefs: map[string]string{"slack_ts": "1.2"},
			CreatedAt:   now, ExpiresAt: now.Add(90 * time.Second),
			ArgsPreview: "{\n  \"user\": \"nova\"\n}", ArgsTruncated: true, ArgsBytes: 4096,
			Notify: []string{"push", "console"},
		}
		rec, err := s.Approvals().Insert(ctx, seed)
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}
		if rec.ID == "" {
			t.Fatal("Insert did not assign an id")
		}

		got, err := s.Approvals().GetByID(ctx, rec.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if len(got.ApproverRoles) != 2 || got.ApproverRoles[0] != "sec-approvers" {
			t.Errorf("approver roles round-trip: %v", got.ApproverRoles)
		}
		if got.ChannelRefs["slack_ts"] != "1.2" {
			t.Errorf("channel refs round-trip: %v", got.ChannelRefs)
		}
		if got.SelfApproval {
			t.Error("selfApproval should round-trip false")
		}
		if got.DecidedAt != nil {
			t.Errorf("pending record must have nil decidedAt, got %v", got.DecidedAt)
		}
		if !got.ExpiresAt.Equal(seed.ExpiresAt) {
			t.Errorf("expiresAt round-trip: got %v want %v", got.ExpiresAt, seed.ExpiresAt)
		}
		if got.ArgsPreview != seed.ArgsPreview || !got.ArgsTruncated || got.ArgsBytes != 4096 {
			t.Errorf("args preview round-trip: got (%q, %v, %d), want (%q, true, 4096)",
				got.ArgsPreview, got.ArgsTruncated, got.ArgsBytes, seed.ArgsPreview)
		}
		if len(got.Notify) != 2 || got.Notify[0] != "push" || got.Notify[1] != "console" {
			t.Errorf("notify round-trip: %v, want [push console]", got.Notify)
		}

		// Partial unique index: a second pending record for the same
		// (session, rule, argv) tuple is a conflict (cross-pod dedupe).
		if _, err := s.Approvals().Insert(ctx, seed); !errors.Is(err, ErrConflict) {
			t.Errorf("duplicate pending key: want ErrConflict, got %v", err)
		}

		found, err := s.Approvals().FindPendingByKey(ctx, "s-1", "r-1", "sha256:abc")
		if err != nil || found.ID != rec.ID {
			t.Fatalf("FindPendingByKey = %+v, %v", found, err)
		}

		// One-time gate: the first MarkDecided wins, the second loses. A nil
		// grantExpiresAt materializes no use window.
		won, err := s.Approvals().MarkDecided(ctx, rec.ID, "approved", "u-dec", "kim", "console", now.Add(time.Second), nil, "", "")
		if err != nil || !won {
			t.Fatalf("MarkDecided first = %v, %v (want won)", won, err)
		}
		won, err = s.Approvals().MarkDecided(ctx, rec.ID, "denied", "u-other", "sam", "console", now.Add(2*time.Second), nil, "", "")
		if err != nil || won {
			t.Fatalf("MarkDecided second = %v, %v (want lost)", won, err)
		}
		decided, _ := s.Approvals().GetByID(ctx, rec.ID)
		if decided.State != "approved" || decided.DecidedByName != "kim" || decided.DecidedAt == nil {
			t.Errorf("decided record = %+v", decided)
		}

		// A resolved tuple no longer occupies the pending index, so the same
		// call can be requested again.
		seed2 := seed
		seed2.CreatedAt = now.Add(3 * time.Second)
		if _, err := s.Approvals().Insert(ctx, seed2); err != nil {
			t.Fatalf("re-insert after resolution: %v", err)
		}
	})
}

func TestApprovalsExpiryAndList(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		base := time.Now().UTC().Truncate(time.Microsecond)

		mk := func(session string, created time.Time, ttl time.Duration) Approval {
			a, err := s.Approvals().Insert(ctx, Approval{
				SessionID: session, RuleID: "r", ArgvHash: session, State: "pending",
				CreatedAt: created, ExpiresAt: created.Add(ttl),
			})
			if err != nil {
				t.Fatalf("Insert %s: %v", session, err)
			}
			return a
		}
		stale := mk("stale", base.Add(-10*time.Minute), time.Minute) // already expired
		fresh := mk("fresh", base, time.Hour)                        // still pending

		exp, err := s.Approvals().ListExpirable(ctx, base, 0)
		if err != nil || len(exp) != 1 || exp[0].ID != stale.ID {
			t.Fatalf("ListExpirable = %+v, %v (want just stale)", exp, err)
		}
		won, err := s.Approvals().ClaimExpired(ctx, stale.ID, base)
		if err != nil || !won {
			t.Fatalf("ClaimExpired = %v, %v (want claimed)", won, err)
		}
		// Idempotent: a second claim loses (already expired, not pending).
		if won, _ := s.Approvals().ClaimExpired(ctx, stale.ID, base); won {
			t.Error("second ClaimExpired should not re-claim")
		}
		// The still-pending fresh record must not be claimable.
		if won, _ := s.Approvals().ClaimExpired(ctx, fresh.ID, base); won {
			t.Error("fresh record must not be claimable as expired")
		}

		pending, _ := s.Approvals().List(ctx, "pending")
		if len(pending) != 1 || pending[0].ID != fresh.ID {
			t.Errorf("List(pending) = %+v", pending)
		}
		all, _ := s.Approvals().List(ctx, "")
		if len(all) != 2 {
			t.Errorf("List(all) = %d, want 2", len(all))
		}

		if err := s.Approvals().SetChannelRefs(ctx, fresh.ID, map[string]string{"slack_ts": "9.9"}); err != nil {
			t.Fatalf("SetChannelRefs: %v", err)
		}
		got, _ := s.Approvals().GetByID(ctx, fresh.ID)
		if got.ChannelRefs["slack_ts"] != "9.9" || got.State != "pending" {
			t.Errorf("SetChannelRefs disturbed the record: %+v", got)
		}

		// Purge terminal records older than a cutoff; pending survives.
		n, err := s.Approvals().PurgeBefore(ctx, base.Add(time.Minute))
		if err != nil || n != 1 {
			t.Fatalf("PurgeBefore = %d, %v (want 1)", n, err)
		}
		if _, err := s.Approvals().GetByID(ctx, stale.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("purged record still present: %v", err)
		}
		if _, err := s.Approvals().GetByID(ctx, fresh.ID); err != nil {
			t.Errorf("pending record must survive purge: %v", err)
		}
	})
}
