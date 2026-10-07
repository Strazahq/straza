package store

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

// TestOutboxControlList pins the relay's priority lane: control-plane
// subjects (everything outside straza.audit.>) are listable on their own so
// revocations never queue behind bulk capture.
func TestOutboxControlList(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		subjects := []string{
			"straza.audit.tool",
			"straza.revocation.session",
			"straza.audit.prompt",
			"straza.policy.updated",
			"straza.identity.updated",
			"straza.audit.reply",
		}
		for _, subj := range subjects {
			if _, err := s.Outbox().Insert(ctx, OutboxEvent{Subject: subj, CE: `{"id":"` + subj + `"}`}); err != nil {
				t.Fatalf("insert %s: %v", subj, err)
			}
		}

		control, err := s.Outbox().ListUnpublishedControl(ctx, 10)
		if err != nil {
			t.Fatalf("ListUnpublishedControl: %v", err)
		}
		if len(control) != 3 {
			t.Fatalf("control rows = %d, want 3", len(control))
		}
		for _, e := range control {
			if e.Subject == "straza.audit.tool" || e.Subject == "straza.audit.prompt" || e.Subject == "straza.audit.reply" {
				t.Errorf("bulk subject %q leaked into the control list", e.Subject)
			}
		}

		// The full list still returns everything (the bulk pass drains what
		// the control pass already marked as published without re-listing it).
		all, err := s.Outbox().ListUnpublished(ctx, 10)
		if err != nil {
			t.Fatalf("ListUnpublished: %v", err)
		}
		if len(all) != 6 {
			t.Fatalf("all rows = %d, want 6", len(all))
		}

		// Marking the control rows published removes them from both lists.
		ids := make([]string, len(control))
		for i, e := range control {
			ids[i] = e.ID
		}
		if err := s.Outbox().MarkPublished(ctx, ids); err != nil {
			t.Fatalf("MarkPublished: %v", err)
		}
		control, err = s.Outbox().ListUnpublishedControl(ctx, 10)
		if err != nil {
			t.Fatalf("ListUnpublishedControl after mark: %v", err)
		}
		if len(control) != 0 {
			t.Fatalf("control rows after mark = %d, want 0", len(control))
		}
	})
}

// TestAuditLastHash pins the chain writer's hot read: same identity as
// Last(), without dragging the CE body.
func TestAuditLastHash(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()

		if _, _, err := s.Audit().LastHash(ctx); !errors.Is(err, ErrNotFound) {
			t.Fatalf("LastHash on empty chain: err = %v, want ErrNotFound", err)
		}

		if _, err := s.Audit().Append(ctx, "ce-1", `{"id":"ce-1"}`, "genesis", "h1"); err != nil {
			t.Fatalf("append 1: %v", err)
		}
		if _, err := s.Audit().Append(ctx, "ce-2", `{"id":"ce-2"}`, "h1", "h2"); err != nil {
			t.Fatalf("append 2: %v", err)
		}

		last, err := s.Audit().Last(ctx)
		if err != nil {
			t.Fatalf("Last: %v", err)
		}
		seq, hash, err := s.Audit().LastHash(ctx)
		if err != nil {
			t.Fatalf("LastHash: %v", err)
		}
		if seq != last.Seq || hash != last.Hash {
			t.Fatalf("LastHash = (%d, %q), want Last's (%d, %q)", seq, hash, last.Seq, last.Hash)
		}
	})
}

// TestConversationPurgeBatched pins the janitor's batched DELETE: the
// full backlog goes, in bounded transactions, and newer rows survive.
func TestConversationPurgeBatched(t *testing.T) {
	old := conversationPurgeBatch
	conversationPurgeBatch = 3
	t.Cleanup(func() { conversationPurgeBatch = old })

	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		cutoff := time.Now()
		for i := 0; i < 10; i++ {
			if _, err := s.Conversations().Insert(ctx, ConversationTurn{
				CEID: "old-" + string(rune('a'+i)), SessionID: "s1", Kind: "prompt",
				Content: "old", At: cutoff.Add(-time.Hour),
			}); err != nil {
				t.Fatalf("insert old %d: %v", i, err)
			}
		}
		for i := 0; i < 2; i++ {
			if _, err := s.Conversations().Insert(ctx, ConversationTurn{
				CEID: "new-" + string(rune('a'+i)), SessionID: "s1", Kind: "prompt",
				Content: "new", At: cutoff.Add(time.Hour),
			}); err != nil {
				t.Fatalf("insert new %d: %v", i, err)
			}
		}

		n, err := s.Conversations().PurgeBefore(ctx, cutoff)
		if err != nil {
			t.Fatalf("PurgeBefore: %v", err)
		}
		if n != 10 {
			t.Fatalf("purged = %d, want 10 (all batches drained)", n)
		}
		left, err := s.Conversations().ListBySession(ctx, "s1", 0)
		if err != nil {
			t.Fatalf("ListBySession: %v", err)
		}
		if len(left) != 2 {
			t.Fatalf("surviving turns = %d, want 2", len(left))
		}
	})
}

// TestPostgresPoolBounds pins the pool configuration: database/sql's
// unlimited-open/2-idle defaults become a bounded, recycled pool. sql.Open is
// lazy, so no live Postgres is needed to observe the limits.
func TestPostgresPoolBounds(t *testing.T) {
	st, err := openPostgres("postgres://straza:straza@127.0.0.1:5432/straza")
	if err != nil {
		t.Fatalf("openPostgres: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	db := st.(*sqlStore).db
	want := 4 * runtime.GOMAXPROCS(0)
	if want < 16 {
		want = 16
	}
	if got := db.Stats().MaxOpenConnections; got != want {
		t.Fatalf("MaxOpenConnections = %d, want %d", got, want)
	}
}
