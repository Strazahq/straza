package approval

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// seedResolved inserts a resolved (non-pending) approval owned by ownerID with
// the given created_at, returning its store id. Direct-to-store so the fill-loop
// tests can shape a precise visible/invisible layout without the request+decide
// dance.
func (h *harness) seedResolved(t *testing.T, ownerID, argv string, created time.Time) string {
	t.Helper()
	a, err := h.st.Approvals().Insert(context.Background(), store.Approval{
		SessionID: "s", UserID: ownerID, Username: "who", RuleID: "r", ArgvHash: argv,
		State: "approved", CreatedAt: created, ExpiresAt: created.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("seedResolved(%s): %v", argv, err)
	}
	return a.ID
}

// TestHistoryDensePagesNoGap pins the fill loop: even when the visible rows are
// sparse in each scanned window, a page still returns a DENSE `limit` of visible
// rows, and paging with the returned cursor walks the whole visible feed
// newest-first with no overlap and no gap, ending with an empty next_cursor.
func TestHistoryDensePagesNoGap(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	viewer := h.seedUser(t, "viewer") // no approver roles → sees only own rows
	stranger := h.seedUser(t, "stranger")

	// 20 rows oldest-first, alternating owner: even index = viewer (visible),
	// odd = stranger (invisible). Each 3-row window is therefore sparse.
	base := time.Now().UTC().Truncate(time.Microsecond)
	all := make([]string, 20)
	for i := 0; i < 20; i++ {
		owner := viewer.ID
		if i%2 == 1 {
			owner = stranger.ID
		}
		all[i] = h.seedResolved(t, owner, "a"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Second))
	}
	// Visible ids newest-first: even indices, descending.
	var wantVisible []string
	for i := 18; i >= 0; i -= 2 {
		wantVisible = append(wantVisible, all[i])
	}

	const limit = 3
	items, next, err := h.svc.History(ctx, viewer.ID, "", limit)
	if err != nil {
		t.Fatalf("History page1: %v", err)
	}
	if len(items) != limit {
		t.Errorf("page1 len = %d, want a dense %d despite sparse windows", len(items), limit)
	}
	if next == "" {
		t.Fatal("page1 next_cursor must be non-empty (more visible rows remain)")
	}

	var got []string
	cursor := ""
	for i := 0; i < 100; i++ { // guard against a non-terminating loop
		items, next, err := h.svc.History(ctx, viewer.ID, cursor, limit)
		if err != nil {
			t.Fatalf("History(cursor=%q): %v", cursor, err)
		}
		if len(items) > limit {
			t.Fatalf("page over limit: %d > %d", len(items), limit)
		}
		for _, it := range items {
			got = append(got, it.ID)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(got) != len(wantVisible) {
		t.Fatalf("paged %d visible rows, want %d", len(got), len(wantVisible))
	}
	for i := range wantVisible {
		if got[i] != wantVisible[i] {
			t.Errorf("visible[%d] = %s, want %s (newest-first, no gap/overlap)", i, got[i], wantVisible[i])
		}
	}
}

// TestHistoryScanBudgetReachesOldRows pins the scan budget AND that keyset paging
// removes the old hard-500 List wall: 505 invisible rows sit NEWEST, three
// visible rows sit OLDEST (permanently unreachable under List(500)). The first
// page spends the 500-row budget on invisible rows, returns zero items but a
// non-empty cursor pointing at the last scanned row, and paging on reaches the
// three old rows.
func TestHistoryScanBudgetReachesOldRows(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	viewer := h.seedUser(t, "viewer")
	stranger := h.seedUser(t, "stranger")

	base := time.Now().UTC().Truncate(time.Microsecond)
	var all []string
	// 3 visible rows first = oldest.
	for i := 0; i < 3; i++ {
		all = append(all, h.seedResolved(t, viewer.ID, "v"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Second)))
	}
	// 505 invisible rows after = newest.
	for i := 0; i < 505; i++ {
		all = append(all, h.seedResolved(t, stranger.ID, "x"+strconv.Itoa(i), base.Add(time.Duration(100+i)*time.Second)))
	}

	const (
		limit  = 50
		budget = 500 // the fixed scan budget per request
	)
	items, next, err := h.svc.History(ctx, viewer.ID, "", limit)
	if err != nil {
		t.Fatalf("History page1: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("page1 items = %d, want 0 (budget spent entirely on invisible rows)", len(items))
	}
	if next == "" {
		t.Fatal("page1 next_cursor must be non-empty: the budget capped the scan, more remain")
	}
	// next_cursor = the id of the last scanned row = the 500th-newest overall.
	// all has 508 ids (idx 0..507); the 500 newest are idx 507..8, so the last
	// scanned is all[8].
	if want := all[8]; next != want {
		t.Errorf("page1 next_cursor = %s, want the last-scanned id %s (advances on scanned, not visible)", next, want)
	}

	// Page on until the feed ends; the three old rows must be reachable.
	var got []string
	cursor := next
	for i := 0; i < 100 && cursor != ""; i++ {
		items, cursor, err = h.svc.History(ctx, viewer.ID, cursor, limit)
		if err != nil {
			t.Fatalf("History(cursor): %v", err)
		}
		for _, it := range items {
			got = append(got, it.ID)
		}
	}
	want := []string{all[2], all[1], all[0]} // newest-first of the visible three
	if len(got) != len(want) {
		t.Fatalf("reachable visible rows = %d, want %d (the old List(500) wall is gone)", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("old-row[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

// TestHistoryShortWindowEarlyFillKeepsTail pins the fill-loop edge where the
// page fills mid-window while the DB returned a SHORT window (feed tail): the
// unscanned remainder of that short window must stay reachable via next_cursor.
// A bare `len(rows) < window` exhaustion check fires after the early break and
// would stamp next_cursor="", which permanently hides the tail rows.
func TestHistoryShortWindowEarlyFillKeepsTail(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	viewer := h.seedUser(t, "viewer") // no approver roles → sees only own rows
	stranger := h.seedUser(t, "stranger")

	// Seed oldest→newest so newest-first reads: v1, s1, v2, v3, v4(oldest tail).
	// limit=3 ⇒ window1 scans [v1,s1,v2] → out=2; window2 (short: 2 rows) scans
	// [v3] → out=3, breaks with v4 unscanned. next_cursor must NOT be empty.
	base := time.Now().UTC().Truncate(time.Microsecond)
	tail := h.seedResolved(t, viewer.ID, "tail", base) // oldest = v4, the at-risk row
	h.seedResolved(t, viewer.ID, "v3", base.Add(1*time.Second))
	h.seedResolved(t, viewer.ID, "v2", base.Add(2*time.Second))
	h.seedResolved(t, stranger.ID, "s1", base.Add(3*time.Second))
	h.seedResolved(t, viewer.ID, "v1", base.Add(4*time.Second))

	items, next, err := h.svc.History(ctx, viewer.ID, "", 3)
	if err != nil {
		t.Fatalf("History page1: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("page1: got %d items, want 3", len(items))
	}
	if next == "" {
		t.Fatalf("page1: next cursor is empty, the unscanned tail row %s is unreachable", tail)
	}
	items2, next2, err := h.svc.History(ctx, viewer.ID, next, 3)
	if err != nil {
		t.Fatalf("History page2: %v", err)
	}
	if len(items2) != 1 || items2[0].ID != tail {
		t.Fatalf("page2: want exactly the tail row %s, got %d items", tail, len(items2))
	}
	if next2 != "" {
		// One benign extra hop is allowed only if it terminates empty.
		items3, next3, err := h.svc.History(ctx, viewer.ID, next2, 3)
		if err != nil || len(items3) != 0 || next3 != "" {
			t.Fatalf("page3: want empty terminal page, got %d items next=%q err=%v", len(items3), next3, err)
		}
	}
}
