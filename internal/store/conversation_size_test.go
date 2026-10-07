package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestConversationStorageBytes pins the watermark measurement the retention
// janitor rides: StorageBytes answers a positive size on a store holding
// turns and never errors on an empty one. The number is dialect-honest
// rather than dialect-identical (Postgres = pg_total_relation_size of
// conversation_turns; SQLite = whole-file page math), so the pin is
// direction and positivity, not an exact byte count.
func TestConversationStorageBytes(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()

		empty, err := s.Conversations().StorageBytes(ctx)
		if err != nil {
			t.Fatalf("StorageBytes on empty store: %v", err)
		}
		if empty < 0 {
			t.Fatalf("StorageBytes on empty store = %d, want >= 0", empty)
		}

		// A deliberately bulky payload so growth clears page granularity on
		// both dialects (sqlite measures whole pages, postgres whole
		// relations).
		bulk := strings.Repeat("transcript bytes fill the disk over weeks ", 4096)
		base := time.Now().UTC()
		for i, ce := range []string{"ce-sz-1", "ce-sz-2", "ce-sz-3"} {
			if _, err := s.Conversations().Insert(ctx, ConversationTurn{
				CEID: ce, SessionID: "s-sz", UserID: "u-bob", Kind: "reply",
				Mode: "verbatim", Content: bulk, ContentHash: "sha256:" + ce,
				At: base.Add(time.Duration(i) * time.Second),
			}); err != nil {
				t.Fatalf("Insert %s: %v", ce, err)
			}
		}

		full, err := s.Conversations().StorageBytes(ctx)
		if err != nil {
			t.Fatalf("StorageBytes on filled store: %v", err)
		}
		if full <= empty {
			t.Fatalf("StorageBytes did not grow: empty=%d full=%d", empty, full)
		}
	})
}
