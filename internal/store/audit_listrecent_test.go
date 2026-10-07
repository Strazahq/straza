package store

import (
	"context"
	"testing"

	"github.com/strazahq/straza/internal/audit"
)

// TestAuditListRecentNewestFirst pins the browsing seam behind
// `strazactl audit tail`: the newest rows come back newest-first and the
// limit bounds the window. List keeps its ascending contract (chain
// verification pages it with an after-cursor), and the last check guards
// against the two being conflated, which would make tail show the oldest
// records.
func TestAuditListRecentNewestFirst(t *testing.T) {
	forEachStore(t, testAuditListRecentNewestFirst)
}

func testAuditListRecentNewestFirst(t *testing.T, s Store) {
	ctx := context.Background()

	prev := audit.Genesis
	var last AuditRecord
	for _, id := range []string{"ce-lr-1", "ce-lr-2", "ce-lr-3"} {
		rec, err := s.Audit().Append(ctx, id, `{"id":"`+id+`"}`, prev, "h-"+id)
		if err != nil {
			t.Fatal(err)
		}
		prev = "h-" + id
		last = rec
	}

	recent, err := s.Audit().ListRecent(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Fatalf("ListRecent(2) returned %d rows, want 2", len(recent))
	}
	if recent[0].Seq != last.Seq {
		t.Fatalf("ListRecent[0].Seq = %d, want the newest seq %d", recent[0].Seq, last.Seq)
	}
	if recent[0].Seq <= recent[1].Seq {
		t.Fatalf("ListRecent not newest-first: seqs %d, %d", recent[0].Seq, recent[1].Seq)
	}

	all, err := s.Audit().ListRecent(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("ListRecent(10) returned %d rows, want all 3", len(all))
	}

	asc, err := s.Audit().List(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(asc) != 3 || asc[0].Seq >= asc[len(asc)-1].Seq {
		t.Fatalf("List ascending contract broken: %d rows, first seq %d, last seq %d",
			len(asc), asc[0].Seq, asc[len(asc)-1].Seq)
	}
}
