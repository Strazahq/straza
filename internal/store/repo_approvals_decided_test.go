package store

import (
	"context"
	"testing"
	"time"
)

// TestMarkDecidedPersistsReasonAndDevice pins the decided-attribution
// columns: the decider's reason and the enrolled device
// that signed the decision persist atomically with the verdict flip and
// round-trip on read, on both dialects. A decision carrying neither leaves
// both empty, so legacy callers stay bit-identical.
func TestMarkDecidedPersistsReasonAndDevice(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Microsecond)

		rec, err := s.Approvals().Insert(ctx, Approval{
			SessionID: "s1", UserID: "u-req", Username: "nova", RuleID: "r1",
			ArgvHash: "h1", State: "pending",
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		})
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}

		reason := "deploy window closed; re-raise tomorrow"
		won, err := s.Approvals().MarkDecided(ctx, rec.ID, "denied", "u-dec", "bob", "browser",
			now.Add(time.Second), nil, reason, "apd_dev1")
		if err != nil || !won {
			t.Fatalf("MarkDecided = %v, %v (want won)", won, err)
		}
		got, err := s.Approvals().GetByID(ctx, rec.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if got.DecidedReason != reason {
			t.Errorf("DecidedReason = %q, want %q", got.DecidedReason, reason)
		}
		if got.DecidedDeviceID != "apd_dev1" {
			t.Errorf("DecidedDeviceID = %q, want %q", got.DecidedDeviceID, "apd_dev1")
		}
		if got.Channel != "browser" {
			t.Errorf("Channel = %q, want browser", got.Channel)
		}

		// Reason-less legacy shape: both columns stay empty.
		plain, err := s.Approvals().Insert(ctx, Approval{
			SessionID: "s2", UserID: "u-req", Username: "nova", RuleID: "r1",
			ArgvHash: "h2", State: "pending",
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		})
		if err != nil {
			t.Fatalf("Insert plain: %v", err)
		}
		if won, err := s.Approvals().MarkDecided(ctx, plain.ID, "approved", "u-dec", "bob", "console",
			now.Add(time.Second), nil, "", ""); err != nil || !won {
			t.Fatalf("MarkDecided plain = %v, %v (want won)", won, err)
		}
		got, err = s.Approvals().GetByID(ctx, plain.ID)
		if err != nil {
			t.Fatalf("GetByID plain: %v", err)
		}
		if got.DecidedReason != "" || got.DecidedDeviceID != "" {
			t.Errorf("legacy decide carried reason=%q device=%q, want empty", got.DecidedReason, got.DecidedDeviceID)
		}
	})
}
