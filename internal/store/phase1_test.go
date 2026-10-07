package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/audit"
)

// TestOutboxDrainClaimed pins the claimed drain: class filtering,
// in-order publishing, publish-then-mark on partial failure.
func TestOutboxDrainClaimed(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		seed := []string{
			"straza.audit.tool",
			"straza.revocation.session",
			"straza.audit.prompt",
			"straza.policy.updated",
		}
		for _, subj := range seed {
			if _, err := s.Outbox().Insert(ctx, OutboxEvent{Subject: subj, CE: `{"id":"` + subj + `"}`}); err != nil {
				t.Fatalf("insert %s: %v", subj, err)
			}
		}

		// Control pass takes only the two control rows, in order.
		var got []string
		n, err := s.Outbox().DrainClaimed(ctx, true, 10, func(e OutboxEvent) error {
			got = append(got, e.Subject)
			return nil
		})
		if err != nil || n != 2 {
			t.Fatalf("control drain = (%d, %v), want (2, nil); got %v", n, err, got)
		}
		if got[0] != "straza.revocation.session" || got[1] != "straza.policy.updated" {
			t.Fatalf("control order = %v", got)
		}

		// Bulk pass: fail the second publish; the first is still marked.
		got = nil
		n, err = s.Outbox().DrainClaimed(ctx, false, 10, func(e OutboxEvent) error {
			got = append(got, e.Subject)
			if len(got) == 2 {
				return errors.New("stream full")
			}
			return nil
		})
		if err == nil || n != 1 {
			t.Fatalf("partial drain = (%d, %v), want (1, publish error)", n, err)
		}
		left, err := s.Outbox().ListUnpublished(ctx, 10)
		if err != nil {
			t.Fatalf("ListUnpublished: %v", err)
		}
		if len(left) != 1 || left[0].Subject != "straza.audit.prompt" {
			t.Fatalf("leftover = %+v, want just straza.audit.prompt", left)
		}
	})
}

// TestOutboxDrainClaimedConcurrent pins work-sharing: two concurrent
// drains never publish the same row twice. On Postgres SKIP LOCKED hands
// them disjoint sets; on SQLite the single connection serializes them.
func TestOutboxDrainClaimedConcurrent(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		const total = 40
		for i := 0; i < total; i++ {
			if _, err := s.Outbox().Insert(ctx, OutboxEvent{
				Subject: "straza.identity.updated",
				CE:      fmt.Sprintf(`{"id":"ev-%02d"}`, i),
			}); err != nil {
				t.Fatalf("insert %d: %v", i, err)
			}
		}

		var mu sync.Mutex
		seen := map[string]int{}
		drain := func() error {
			for {
				n, err := s.Outbox().DrainClaimed(ctx, false, 8, func(e OutboxEvent) error {
					mu.Lock()
					seen[e.ID]++
					mu.Unlock()
					time.Sleep(time.Millisecond) // widen the race window
					return nil
				})
				if err != nil {
					return err
				}
				if n == 0 {
					return nil
				}
			}
		}
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range errs {
			wg.Add(1)
			go func(i int) { defer wg.Done(); errs[i] = drain() }(i)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("drainer %d: %v", i, err)
			}
		}
		if len(seen) != total {
			t.Fatalf("published %d distinct rows, want %d", len(seen), total)
		}
		for id, count := range seen {
			if count != 1 {
				t.Fatalf("row %s published %d times; drains did not share", id, count)
			}
		}
		left, err := s.Outbox().ListUnpublished(ctx, total)
		if err != nil {
			t.Fatalf("ListUnpublished: %v", err)
		}
		if len(left) != 0 {
			t.Fatalf("unpublished leftovers: %d", len(left))
		}
	})
}

// verifyChain walks the full audit chain and fails on any linearity break.
func verifyChain(t *testing.T, s Store, wantRows int) {
	t.Helper()
	recs, err := s.Audit().List(context.Background(), 0, wantRows+10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != wantRows {
		t.Fatalf("chain rows = %d, want %d", len(recs), wantRows)
	}
	prev := audit.Genesis
	for i, rec := range recs {
		if rec.PrevHash != prev {
			t.Fatalf("row %d (seq %d): prev_hash = %q, want %q (chain fork)", i, rec.Seq, rec.PrevHash, prev)
		}
		if want := audit.Link(prev, rec.CE); rec.Hash != want {
			t.Fatalf("row %d (seq %d): hash mismatch", i, rec.Seq)
		}
		prev = rec.Hash
	}
}

// TestAuditAppendChained pins the batched append: dedupe against the
// table and within the batch, untracked ("" id) events always chain, rerun
// is a no-op, and the chain stays linear.
func TestAuditAppendChained(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()

		// Pre-chain one event the old way; the batch must skip its id.
		if _, err := s.Audit().Append(ctx, "ce-existing", `{"id":"ce-existing"}`, audit.Genesis,
			audit.Link(audit.Genesis, `{"id":"ce-existing"}`)); err != nil {
			t.Fatalf("seed append: %v", err)
		}

		batch := []ChainEvent{
			{CEID: "ce-a", CE: `{"id":"ce-a"}`},
			{CEID: "ce-existing", CE: `{"id":"ce-existing"}`}, // already chained
			{CEID: "ce-b", CE: `{"id":"ce-b"}`},
			{CEID: "ce-a", CE: `{"id":"ce-a"}`}, // duplicate within the batch
			{CEID: "", CE: `{"note":"untracked"}`},
		}
		n, err := s.Audit().AppendChained(ctx, batch, audit.Genesis, audit.Link)
		if err != nil {
			t.Fatalf("AppendChained: %v", err)
		}
		if n != 3 { // ce-a, ce-b, untracked
			t.Fatalf("appended = %d, want 3", n)
		}
		verifyChain(t, s, 4)

		// Rerunning the identical batch appends nothing new except the
		// untracked event (undedupable by design).
		n, err = s.Audit().AppendChained(ctx, batch, audit.Genesis, audit.Link)
		if err != nil {
			t.Fatalf("rerun: %v", err)
		}
		if n != 1 {
			t.Fatalf("rerun appended = %d, want 1 (the untracked event)", n)
		}
		verifyChain(t, s, 5)
	})
}

// TestAuditAppendChainedConcurrent is the linearity proof: concurrent batch
// writers (the multi-pod shape) still produce ONE linear chain: the
// advisory lock (Postgres) or the single connection (SQLite) serializes
// appends, and every row links to its predecessor.
func TestAuditAppendChainedConcurrent(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		const writers, perWriter = 4, 15
		var wg sync.WaitGroup
		errs := make([]error, writers)
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				var batch []ChainEvent
				for i := 0; i < perWriter; i++ {
					id := fmt.Sprintf("ce-w%d-%02d", w, i)
					batch = append(batch, ChainEvent{CEID: id, CE: `{"id":"` + id + `"}`})
				}
				_, errs[w] = s.Audit().AppendChained(ctx, batch, audit.Genesis, audit.Link)
			}(w)
		}
		wg.Wait()
		for w, err := range errs {
			if err != nil {
				t.Fatalf("writer %d: %v", w, err)
			}
		}
		verifyChain(t, s, writers*perWriter)
	})
}
