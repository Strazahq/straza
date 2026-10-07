package store

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// TestApprovalsListBefore pins the keyset window that backs approver-history
// pagination: empty cursor = newest page, strict DESC-by-id ordering, the
// `id < cursor` window, `limit` capping, the `limit+1` probe that drives `more`,
// and deterministic ordering under an identical created_at (UUIDv7 id breaks the
// tie). Runs on every dialect (sqlite always; postgres when the DSN is set).
func TestApprovalsListBefore(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		base := time.Now().UTC().Truncate(time.Microsecond)

		// Insert n resolved rows oldest-first; UUIDv7 ids are minted in call
		// order, so ids ascend with i. Capture them to assert against.
		const n = 6
		ids := make([]string, 0, n)
		for i := 0; i < n; i++ {
			a, err := s.Approvals().Insert(ctx, Approval{
				SessionID: "s", RuleID: "r", ArgvHash: "h" + strconv.Itoa(i), State: "approved",
				CreatedAt: base.Add(time.Duration(i) * time.Second), ExpiresAt: base.Add(time.Hour),
			})
			if err != nil {
				t.Fatalf("Insert %d: %v", i, err)
			}
			ids = append(ids, a.ID)
		}

		// Empty cursor = newest page, strictly DESC by id.
		got, err := s.Approvals().ListBefore(ctx, "", n)
		if err != nil {
			t.Fatalf("ListBefore(all): %v", err)
		}
		if len(got) != n {
			t.Fatalf("ListBefore(\"\",%d) len = %d, want %d", n, len(got), n)
		}
		for i := 0; i < n; i++ {
			if want := ids[n-1-i]; got[i].ID != want {
				t.Errorf("row %d id = %s, want %s (newest-first)", i, got[i].ID, want)
			}
			if i > 0 && got[i-1].ID <= got[i].ID {
				t.Errorf("ordering not strictly DESC at %d: %s then %s", i, got[i-1].ID, got[i].ID)
			}
		}

		// limit caps the page to the newest `limit` rows.
		page, err := s.Approvals().ListBefore(ctx, "", 2)
		if err != nil || len(page) != 2 {
			t.Fatalf("ListBefore(\"\",2) = %d rows, %v", len(page), err)
		}
		if page[0].ID != ids[n-1] || page[1].ID != ids[n-2] {
			t.Errorf("newest page = [%s %s], want [%s %s]", page[0].ID, page[1].ID, ids[n-1], ids[n-2])
		}

		// before-id window: strictly `id < cursor`. Cursor = second-newest id;
		// the window EXCLUDES it and everything newer.
		before, err := s.Approvals().ListBefore(ctx, ids[n-2], n)
		if err != nil {
			t.Fatalf("ListBefore(before): %v", err)
		}
		if len(before) != n-2 {
			t.Fatalf("ListBefore(ids[n-2]) len = %d, want %d", len(before), n-2)
		}
		for _, r := range before {
			if r.ID >= ids[n-2] {
				t.Errorf("windowed row %s is not strictly < cursor %s", r.ID, ids[n-2])
			}
		}
		if before[0].ID != ids[n-3] {
			t.Errorf("first windowed id = %s, want %s", before[0].ID, ids[n-3])
		}

		// limit+1 probe drives `more`: asking one past a page boundary yields the
		// extra row, so the caller can detect a further page exists.
		probe, err := s.Approvals().ListBefore(ctx, "", 3+1)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if len(probe) != 4 {
			t.Errorf("limit+1 probe returned %d rows, want 4 (signals more)", len(probe))
		}

		// A cursor at/under the oldest id yields an empty page (end of feed).
		if tail, err := s.Approvals().ListBefore(ctx, ids[0], n); err != nil || len(tail) != 0 {
			t.Errorf("ListBefore(oldest id) = %d rows, %v, want 0", len(tail), err)
		}

		// Ties: two more rows minted with an IDENTICAL created_at still order
		// deterministically: DESC by the monotone UUIDv7 id, newest mint first.
		tie := base.Add(time.Hour)
		tieA, err := s.Approvals().Insert(ctx, Approval{
			SessionID: "s", RuleID: "r", ArgvHash: "tieA", State: "denied",
			CreatedAt: tie, ExpiresAt: base.Add(2 * time.Hour),
		})
		if err != nil {
			t.Fatalf("Insert tieA: %v", err)
		}
		tieB, err := s.Approvals().Insert(ctx, Approval{
			SessionID: "s", RuleID: "r", ArgvHash: "tieB", State: "denied",
			CreatedAt: tie, ExpiresAt: base.Add(2 * time.Hour),
		})
		if err != nil {
			t.Fatalf("Insert tieB: %v", err)
		}
		top, err := s.Approvals().ListBefore(ctx, "", 2)
		if err != nil {
			t.Fatalf("ListBefore(top): %v", err)
		}
		if len(top) != 2 || top[0].ID != tieB.ID || top[1].ID != tieA.ID {
			t.Errorf("equal-created_at tie order = %+v, want [%s %s] (id DESC)", idsOf(top), tieB.ID, tieA.ID)
		}
	})
}

func idsOf(rows []Approval) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}
