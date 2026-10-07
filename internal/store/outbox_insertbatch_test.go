package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestOutboxInsertBatch pins the accept-side batch insert: one call stores
// the whole set in
// one transaction, assigns ids, and preserves submission order for the
// drain: capture prompt/reply pairs must chain in the order the client
// emitted them, so intra-batch created_at is strictly increasing.
func TestOutboxInsertBatch(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()

		t.Run("empty is a no-op", func(t *testing.T) {
			n, err := s.Outbox().InsertBatch(ctx, nil)
			if err != nil || n != 0 {
				t.Fatalf("InsertBatch(nil) = (%d, %v), want (0, nil)", n, err)
			}
		})

		t.Run("order and ids", func(t *testing.T) {
			batch := []OutboxEvent{
				{Subject: "straza.audit.prompt", CE: `{"id":"turn-1-prompt"}`},
				{Subject: "straza.audit.reply", CE: `{"id":"turn-1-reply"}`},
				{Subject: "straza.audit.prompt", CE: `{"id":"turn-2-prompt"}`},
				{Subject: "straza.audit.reply", CE: `{"id":"turn-2-reply"}`},
			}
			n, err := s.Outbox().InsertBatch(ctx, batch)
			if err != nil || n != len(batch) {
				t.Fatalf("InsertBatch = (%d, %v), want (%d, nil)", n, err, len(batch))
			}
			var got []string
			if _, err := s.Outbox().DrainClaimed(ctx, false, 10, func(e OutboxEvent) error {
				got = append(got, e.CE)
				return nil
			}); err != nil {
				t.Fatalf("drain: %v", err)
			}
			if len(got) != len(batch) {
				t.Fatalf("drained %d rows, want %d", len(got), len(batch))
			}
			for i, want := range []string{"turn-1-prompt", "turn-1-reply", "turn-2-prompt", "turn-2-reply"} {
				if got[i] != `{"id":"`+want+`"}` {
					t.Fatalf("drain order[%d] = %s, want %s (batch order must survive the drain)", i, got[i], want)
				}
			}
		})

		t.Run("chunk boundary", func(t *testing.T) {
			const total = 450 // crosses the 200-row statement chunk twice
			batch := make([]OutboxEvent, total)
			for i := range batch {
				batch[i] = OutboxEvent{Subject: "straza.audit.tool", CE: fmt.Sprintf(`{"id":"bulk-%03d"}`, i)}
			}
			n, err := s.Outbox().InsertBatch(ctx, batch)
			if err != nil || n != total {
				t.Fatalf("InsertBatch = (%d, %v), want (%d, nil)", n, err, total)
			}
			var first, last string
			count := 0
			if _, err := s.Outbox().DrainClaimed(ctx, false, total+10, func(e OutboxEvent) error {
				if count == 0 {
					first = e.CE
				}
				last = e.CE
				count++
				return nil
			}); err != nil {
				t.Fatalf("drain: %v", err)
			}
			if count != total || first != `{"id":"bulk-000"}` || last != fmt.Sprintf(`{"id":"bulk-%03d"}`, total-1) {
				t.Fatalf("drained %d (first %s, last %s), want %d in submission order", count, first, last, total)
			}
		})

		// A cancelled context ends the call before it reaches the database,
		// so only the wait can keep it past its stamps.
		t.Run("returns after its last stamp", func(t *testing.T) {
			gone, cancel := context.WithCancel(ctx)
			cancel()
			batch := make([]OutboxEvent, 5000)
			for i := range batch {
				batch[i] = OutboxEvent{Subject: "straza.audit.tool", CE: `{}`}
			}
			if _, err := s.Outbox().InsertBatch(gone, batch); err == nil {
				t.Fatal("InsertBatch on a cancelled context returned no error")
			}
			if last := batch[len(batch)-1].CreatedAt.Truncate(time.Microsecond); !now().After(last) {
				t.Fatalf("InsertBatch returned before its last stamp %s had passed, so the next write could sort before it", last)
			}
		})
	})
}
