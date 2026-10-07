package store

import (
	"context"
	"testing"
	"time"
)

// TestConversationInsertBatchSummary pins that batched turn inserts with
// in-tx dedupe keep the summary table exactly consistent with the turns
// (counts, first/last activity, latest preview), and the retention purge
// reconciles it.
func TestConversationInsertBatchSummary(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		base := time.Now().UTC().Truncate(time.Second)

		batch := []ConversationTurn{
			{CEID: "t1", SessionID: "s1", UserID: "u1", Kind: "prompt", Content: "first prompt", At: base},
			{CEID: "t2", SessionID: "s1", UserID: "u1", Kind: "reply", Content: "latest reply", At: base.Add(time.Minute)},
			{CEID: "t1", SessionID: "s1", UserID: "u1", Kind: "prompt", Content: "dup in batch", At: base},
			{CEID: "t3", SessionID: "s2", UserID: "u2", Kind: "prompt", Content: "other session", At: base.Add(2 * time.Minute)},
		}
		n, err := s.Conversations().InsertBatch(ctx, batch)
		if err != nil {
			t.Fatalf("InsertBatch: %v", err)
		}
		if n != 3 {
			t.Fatalf("inserted = %d, want 3 (in-batch dup dropped)", n)
		}

		// Redelivery of the same batch inserts nothing.
		n, err = s.Conversations().InsertBatch(ctx, batch)
		if err != nil {
			t.Fatalf("redelivery: %v", err)
		}
		if n != 0 {
			t.Fatalf("redelivery inserted %d, want 0", n)
		}

		convs, err := s.Conversations().ListConversations(ctx, 10)
		if err != nil {
			t.Fatalf("ListConversations: %v", err)
		}
		if len(convs) != 2 {
			t.Fatalf("sessions = %d, want 2", len(convs))
		}
		// Newest activity first: s2 then s1.
		if convs[0].SessionID != "s2" || convs[1].SessionID != "s1" {
			t.Fatalf("order = %s, %s; want s2, s1", convs[0].SessionID, convs[1].SessionID)
		}
		s1 := convs[1]
		if s1.Turns != 2 || s1.UserID != "u1" || s1.Preview != "latest reply" {
			t.Fatalf("s1 summary = %+v, want 2 turns / u1 / latest-reply preview", s1)
		}
		if !s1.FirstAt.Equal(base) || !s1.LastAt.Equal(base.Add(time.Minute)) {
			t.Fatalf("s1 window = %v..%v, want %v..%v", s1.FirstAt, s1.LastAt, base, base.Add(time.Minute))
		}

		// A later batch for s1 bumps count, last_at, and preview.
		if _, err := s.Conversations().InsertBatch(ctx, []ConversationTurn{
			{CEID: "t4", SessionID: "s1", UserID: "u1", Kind: "prompt", Content: "newest", At: base.Add(3 * time.Minute)},
		}); err != nil {
			t.Fatalf("second batch: %v", err)
		}
		convs, _ = s.Conversations().ListConversations(ctx, 10)
		if convs[0].SessionID != "s1" || convs[0].Turns != 3 || convs[0].Preview != "newest" {
			t.Fatalf("s1 after bump = %+v", convs[0])
		}

		// Purge everything older than base+2m: s1 loses t1/t2 (keeps t4),
		// s2 keeps its only turn (at base+2m, not < cutoff).
		purged, err := s.Conversations().PurgeBefore(ctx, base.Add(2*time.Minute))
		if err != nil {
			t.Fatalf("PurgeBefore: %v", err)
		}
		if purged != 2 {
			t.Fatalf("purged = %d, want 2", purged)
		}
		convs, _ = s.Conversations().ListConversations(ctx, 10)
		if len(convs) != 2 {
			t.Fatalf("sessions after purge = %d, want 2", len(convs))
		}
		if convs[0].SessionID != "s1" || convs[0].Turns != 1 ||
			!convs[0].FirstAt.Equal(base.Add(3*time.Minute)) {
			t.Fatalf("s1 after purge = %+v, want 1 turn first_at %v", convs[0], base.Add(3*time.Minute))
		}

		// Purge the rest: both summaries disappear with their turns.
		if _, err := s.Conversations().PurgeBefore(ctx, base.Add(time.Hour)); err != nil {
			t.Fatalf("final purge: %v", err)
		}
		convs, _ = s.Conversations().ListConversations(ctx, 10)
		if len(convs) != 0 {
			t.Fatalf("sessions after full purge = %d, want 0", len(convs))
		}
	})
}
