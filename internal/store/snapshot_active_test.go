package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// TestSnapshotSetActiveExclusive pins the "exactly one active snapshot"
// contract, including self-healing: a fresh enterprise install with
// replicas=2 boots both pods onto an empty store and both Recompile. A
// two-statement SetActive (clear-all then set-one) would let the
// transactions interleave under read-committed so BOTH rows end active.
// SetActive must be atomic and any later call must heal a corrupted
// double-active state.
func TestSnapshotSetActiveExclusive(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		for _, id := range []string{"snap-a", "snap-b"} {
			if _, err := s.Snapshots().Create(ctx, Snapshot{ID: id, SignerKeyID: "k1", Blob: []byte(id)}); err != nil {
				t.Fatal(err)
			}
		}
		countActive := func() int {
			var n int
			if err := s.(*sqlStore).db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM snapshots WHERE active = TRUE`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}

		for _, id := range []string{"snap-a", "snap-b", "snap-a"} {
			if err := s.Snapshots().SetActive(ctx, id); err != nil {
				t.Fatalf("SetActive(%s): %v", id, err)
			}
			if got, err := s.Snapshots().GetActive(ctx); err != nil || got.ID != id {
				t.Fatalf("GetActive after SetActive(%s) = %v, %v", id, got.ID, err)
			}
			if n := countActive(); n != 1 {
				t.Fatalf("active rows after SetActive(%s) = %d, want exactly 1", id, n)
			}
		}

		// The first-boot race artifact: both rows active. Any SetActive must
		// heal it back to exactly one.
		if _, err := s.(*sqlStore).db.ExecContext(ctx, `UPDATE snapshots SET active = TRUE`); err != nil {
			t.Fatal(err)
		}
		if n := countActive(); n != 2 {
			t.Fatalf("corruption setup failed: %d active", n)
		}
		if err := s.Snapshots().SetActive(ctx, "snap-b"); err != nil {
			t.Fatal(err)
		}
		if n := countActive(); n != 1 {
			t.Errorf("double-active not healed: %d active rows", n)
		}
		if got, _ := s.Snapshots().GetActive(ctx); got.ID != "snap-b" {
			t.Errorf("GetActive after heal = %s, want snap-b", got.ID)
		}

		// Unknown id: ErrNotFound and the existing state untouched.
		if err := s.Snapshots().SetActive(ctx, "no-such"); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetActive(no-such) = %v, want ErrNotFound", err)
		}
		if got, _ := s.Snapshots().GetActive(ctx); got.ID != "snap-b" || countActive() != 1 {
			t.Errorf("failed SetActive disturbed state: active=%s count=%d", got.ID, countActive())
		}

		// The race itself: concurrent activations of different snapshots
		// (the fresh-install replicas=2 first boot). MVCC interleaving is a
		// Postgres behavior; sqlite serializes writers, so only the pg leg
		// exercises this meaningfully (CI).
		if strings.Contains(t.Name(), "sqlite") {
			return
		}
		var wg sync.WaitGroup
		for _, id := range []string{"snap-a", "snap-b"} {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				for i := 0; i < 25; i++ {
					if err := s.Snapshots().SetActive(ctx, id); err != nil {
						t.Errorf("concurrent SetActive(%s): %v", id, err)
						return
					}
				}
			}(id)
		}
		wg.Wait()
		if n := countActive(); n != 1 {
			t.Errorf("after concurrent activations: %d active rows, want exactly 1 (the first-boot race)", n)
		}
	})
}

// TestSnapshotSetActiveFrom pins the compare-and-set a policy publish ends
// with: the new snapshot becomes active only while the snapshot the publish
// was built on is still the active one. Two replicas that publish at once
// then cannot both land, so neither silently drops the other's change.
func TestSnapshotSetActiveFrom(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		for _, id := range []string{"snap-a", "snap-b", "snap-c"} {
			if _, err := s.Snapshots().Create(ctx, Snapshot{ID: id, SignerKeyID: "k1", Blob: []byte(id)}); err != nil {
				t.Fatal(err)
			}
		}
		db := s.(*sqlStore).db
		activeIDs := func() []string {
			rows, err := db.QueryContext(ctx, `SELECT id FROM snapshots WHERE active = TRUE ORDER BY id`)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			var ids []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, id)
			}
			return ids
		}

		cases := []struct {
			name   string
			active string // the active snapshot before the call, "" for none
			id     string
			from   string
			want   error
			after  string // the only active snapshot after the call, "" for none
		}{
			{name: "the right base activates", active: "snap-a", id: "snap-b", from: "snap-a", after: "snap-b"},
			{name: "a stale base is a conflict", active: "snap-a", id: "snap-c", from: "snap-b", want: ErrConflict, after: "snap-a"},
			{name: "no active snapshot is a conflict", active: "", id: "snap-b", from: "snap-a", want: ErrConflict, after: ""},
			{name: "an unknown id is not found", active: "snap-a", id: "no-such", from: "snap-a", want: ErrNotFound, after: "snap-a"},
			{name: "the active id itself is a no-op", active: "snap-a", id: "snap-a", from: "snap-a", after: "snap-a"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if tc.active == "" {
					if _, err := db.ExecContext(ctx, `UPDATE snapshots SET active = FALSE`); err != nil {
						t.Fatal(err)
					}
				} else if err := s.Snapshots().SetActive(ctx, tc.active); err != nil {
					t.Fatal(err)
				}
				err := s.Snapshots().SetActiveFrom(ctx, tc.id, tc.from)
				switch {
				case tc.want == nil && err != nil:
					t.Fatalf("SetActiveFrom(%s, %s) = %v, want nil", tc.id, tc.from, err)
				case tc.want != nil && !errors.Is(err, tc.want):
					t.Fatalf("SetActiveFrom(%s, %s) = %v, want %v", tc.id, tc.from, err, tc.want)
				}
				got := activeIDs()
				if tc.after == "" && len(got) != 0 || tc.after != "" && (len(got) != 1 || got[0] != tc.after) {
					t.Fatalf("active snapshots after the call = %v, want only %q", got, tc.after)
				}
			})
		}

		// Two publishes built on the same base race to activate. Exactly one
		// may win and the other must see a conflict, which is what the row
		// lock on Postgres guarantees. SQLite has one writer process, whose
		// snapshot service serializes its publishes, so only the Postgres
		// leg runs this.
		if strings.Contains(t.Name(), "sqlite") {
			return
		}
		for i := 0; i < 20; i++ {
			if err := s.Snapshots().SetActive(ctx, "snap-a"); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			errs := map[string]error{}
			var mu sync.Mutex
			for _, id := range []string{"snap-b", "snap-c"} {
				wg.Add(1)
				go func(id string) {
					defer wg.Done()
					err := s.Snapshots().SetActiveFrom(ctx, id, "snap-a")
					mu.Lock()
					errs[id] = err
					mu.Unlock()
				}(id)
			}
			wg.Wait()
			var winners []string
			for id, err := range errs {
				switch {
				case err == nil:
					winners = append(winners, id)
				case !errors.Is(err, ErrConflict):
					t.Fatalf("round %d: SetActiveFrom(%s) = %v, want nil or ErrConflict", i, id, err)
				}
			}
			if len(winners) != 1 {
				t.Fatalf("round %d: %d publishes on the same base both landed or both failed (%v), want exactly one winner", i, len(winners), errs)
			}
			if got := activeIDs(); len(got) != 1 || got[0] != winners[0] {
				t.Fatalf("round %d: active snapshots = %v, want only the winner %s", i, got, winners[0])
			}
		}
	})
}
