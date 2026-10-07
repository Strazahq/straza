package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/audit"
)

// TestAuditListSinceWindow pins the window read behind the overview
// decision block: rows written at or after since come back ascending by
// seq, rows written before it never do, the afterSeq cursor walks forward
// and the limit bounds one pass. The row written a fraction of a second
// into the window is the sqlite case: created_at is RFC3339Nano text there,
// and an unpadded hour-aligned bound compares greater than it.
func TestAuditListSinceWindow(t *testing.T) {
	forEachStore(t, testAuditListSinceWindow)
}

func testAuditListSinceWindow(t *testing.T, s Store) {
	ctx := context.Background()
	hour := time.Now().UTC().Truncate(time.Hour)
	since := hour.Add(-23 * time.Hour)

	// Append stamps created_at itself, so a row that must sit outside the
	// window goes in with its timestamp spelled out.
	write := func(id string, at time.Time) int64 {
		t.Helper()
		sq := s.(*sqlStore)
		var seq int64
		err := sq.queryRow(ctx, `INSERT INTO audit_log (ce_id, ce, prev_hash, hash, created_at)
			VALUES ($1, $2, $3, $4, $5) RETURNING seq`,
			id, `{"id":"`+id+`"}`, "", "h-"+id, sq.tArg(at)).Scan(&seq)
		if err != nil {
			t.Fatalf("insert %s at %s: %v", id, at, err)
		}
		return seq
	}

	write("ce-ls-old-1", since.Add(-2*time.Hour))
	write("ce-ls-old-2", since.Add(-time.Second))
	edge := write("ce-ls-edge", since)
	frac := write("ce-ls-frac", since.Add(123456*time.Microsecond))
	mid := write("ce-ls-mid", since.Add(11*time.Hour))
	newest := write("ce-ls-new", hour.Add(30*time.Minute))

	seqs := func(recs []AuditRecord) []int64 {
		out := make([]int64, len(recs))
		for i, r := range recs {
			out[i] = r.Seq
		}
		return out
	}
	same := func(name string, got []AuditRecord, want ...int64) {
		t.Helper()
		g := seqs(got)
		if fmt.Sprint(g) != fmt.Sprint(want) {
			t.Fatalf("%s = %v, want %v", name, g, want)
		}
	}

	all, err := s.Audit().ListSince(ctx, since, 0, 100)
	if err != nil {
		t.Fatalf("ListSince: %v", err)
	}
	same("ListSince(since, 0, 100)", all, edge, frac, mid, newest)

	page, err := s.Audit().ListSince(ctx, since, 0, 2)
	if err != nil {
		t.Fatalf("ListSince limit: %v", err)
	}
	same("ListSince(since, 0, 2)", page, edge, frac)

	next, err := s.Audit().ListSince(ctx, since, page[len(page)-1].Seq, 2)
	if err != nil {
		t.Fatalf("ListSince cursor: %v", err)
	}
	same("ListSince(since, frac, 2)", next, mid, newest)

	none, err := s.Audit().ListSince(ctx, hour.Add(time.Hour), 0, 100)
	if err != nil {
		t.Fatalf("ListSince future: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("ListSince from the next hour returned %d rows, want none", len(none))
	}
}

// TestAuditListSinceScanCost times one full paged scan of a day's worth of
// decision records, the read the overview pays once a minute. It asserts
// only that the scan returns every row; the elapsed time is logged, because
// a wall-clock threshold in a test turns into a flake on a loaded box.
func TestAuditListSinceScanCost(t *testing.T) {
	forEachStore(t, testAuditListSinceScanCost)
}

func testAuditListSinceScanCost(t *testing.T, s Store) {
	ctx := context.Background()
	const rows = 5000
	const batch = 500

	link := func(prevHash, ce string) string { return audit.Link(prevHash, ce) }
	for start := 0; start < rows; start += batch {
		events := make([]ChainEvent, 0, batch)
		for i := start; i < start+batch; i++ {
			id := fmt.Sprintf("ce-scan-%04d", i)
			events = append(events, ChainEvent{
				CEID: id,
				CE: fmt.Sprintf(`{"id":%q,"type":"straza.audit.tool","time":%q,`+
					`"data":{"tool":"shell.exec","effect":"deny","reason":"Straza: blocked"}}`,
					id, time.Now().UTC().Format(time.RFC3339Nano)),
			})
		}
		if n, err := s.Audit().AppendChained(ctx, events, audit.Genesis, link); err != nil || n != batch {
			t.Fatalf("AppendChained: %d rows, %v", n, err)
		}
	}

	since := time.Now().UTC().Truncate(time.Hour).Add(-23 * time.Hour)
	start := time.Now()
	seen := 0
	for afterSeq := int64(0); ; {
		page, err := s.Audit().ListSince(ctx, since, afterSeq, 1000)
		if err != nil {
			t.Fatalf("ListSince: %v", err)
		}
		seen += len(page)
		if len(page) < 1000 {
			break
		}
		afterSeq = page[len(page)-1].Seq
	}
	elapsed := time.Since(start)
	if seen != rows {
		t.Fatalf("scan saw %d rows, want %d", seen, rows)
	}
	t.Logf("ListSince scan of %d rows took %s", rows, elapsed)
}
