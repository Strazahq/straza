package store

import (
	"context"
	"testing"
	"time"
)

// TestOutboxPruneBulkPublished pins the prune: only PUBLISHED bulk
// (straza.audit.>) rows older than the cutoff go; control rows and
// unpublished rows survive at any age: the change feed and the relay
// depend on them.
func TestOutboxPruneBulkPublished(t *testing.T) {
	old := outboxPruneBatch
	outboxPruneBatch = 2
	t.Cleanup(func() { outboxPruneBatch = old })

	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		seed := []struct {
			subject   string
			published bool
			old       bool
		}{
			{"straza.audit.tool", true, true},         // pruned
			{"straza.audit.prompt", true, true},       // pruned
			{"straza.audit.reply", true, true},        // pruned (proves batching: batch=2)
			{"straza.audit.tool", true, false},        // kept: young
			{"straza.audit.mcp", false, true},         // kept: unpublished (relay's)
			{"straza.identity.updated", true, true},   // kept: control = change feed
			{"straza.revocation.session", true, true}, // kept: control
		}
		oldAt := time.Now().Add(-72 * time.Hour)
		for i, row := range seed {
			e, err := s.Outbox().Insert(ctx, OutboxEvent{Subject: row.subject, CE: `{"id":"p` + string(rune('a'+i)) + `"}`})
			if err != nil {
				t.Fatal(err)
			}
			if row.published {
				if err := s.Outbox().MarkPublished(ctx, []string{e.ID}); err != nil {
					t.Fatal(err)
				}
			}
			if row.old {
				st := s.(*sqlStore)
				if _, err := st.exec(ctx, `UPDATE events_outbox SET created_at = $1 WHERE id = $2`,
					st.tArg(oldAt), e.ID); err != nil {
					t.Fatal(err)
				}
			}
		}

		n, err := s.Outbox().PruneBulkPublished(ctx, time.Now().Add(-48*time.Hour))
		if err != nil {
			t.Fatalf("PruneBulkPublished: %v", err)
		}
		if n != 3 {
			t.Fatalf("pruned = %d, want 3", n)
		}

		// Survivors: the young bulk row is published (invisible to
		// ListUnpublished), the unpublished bulk row still awaits the relay,
		// and both control rows remain readable as feed history.
		unpub, err := s.Outbox().ListUnpublished(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(unpub) != 1 || unpub[0].Subject != "straza.audit.mcp" {
			t.Fatalf("unpublished survivors = %+v", unpub)
		}
		feed, err := s.Outbox().ListAfter(ctx, "", []string{"straza.identity.updated", "straza.revocation.session"}, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(feed) != 2 {
			t.Fatalf("control rows in feed = %d, want 2 (pruning must never eat the change feed)", len(feed))
		}
	})
}
