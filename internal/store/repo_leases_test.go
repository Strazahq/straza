package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestLeases pins who holds a lease after each step, on both drivers.
func TestLeases(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		t0 := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
		at := func(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }
		steps := []struct {
			name    string
			holder  string
			now     int
			until   int
			release bool
			want    bool
		}{
			{name: "a free lease goes to the first asker", holder: "a", now: 0, until: 60, want: true},
			{name: "another replica is refused while it is held", holder: "b", now: 10, until: 70, want: false},
			{name: "the holder renews it", holder: "a", now: 30, until: 90, want: true},
			{name: "the renewed time counts", holder: "b", now: 70, until: 130, want: false},
			{name: "it goes to another replica after its time", holder: "b", now: 91, until: 150, want: true},
			{name: "the old holder is refused now", holder: "a", now: 100, until: 160, want: false},
			{name: "a release by a replica that does not hold it changes nothing", holder: "a", release: true},
			{name: "the lease is still held", holder: "a", now: 101, until: 160, want: false},
			{name: "the holder releases it", holder: "b", release: true},
			{name: "a released lease is free at once", holder: "a", now: 102, until: 160, want: true},
		}
		for _, st := range steps {
			if st.release {
				if err := s.Leases().Release(ctx, "job", st.holder); err != nil {
					t.Fatalf("%s: Release: %v", st.name, err)
				}
				continue
			}
			got, err := s.Leases().Acquire(ctx, "job", st.holder, at(st.now), at(st.until))
			if err != nil {
				t.Fatalf("%s: Acquire: %v", st.name, err)
			}
			if got != st.want {
				t.Errorf("%s: Acquire = %v, want %v", st.name, got, st.want)
			}
		}
		if got, err := s.Leases().Acquire(ctx, "other-job", "b", at(103), at(160)); err != nil || !got {
			t.Errorf("a lease of another name = %v, %v, want held: names are independent", got, err)
		}
	})
}

// TestLeaseOneWinner pins that replicas asking at once for a free lease
// get exactly one holder.
func TestLeaseOneWinner(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
		var wg sync.WaitGroup
		var mu sync.Mutex
		won := 0
		for _, holder := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, err := s.Leases().Acquire(ctx, "race", holder, now, now.Add(time.Minute))
				if err != nil {
					t.Errorf("Acquire for %s: %v", holder, err)
				}
				if got {
					mu.Lock()
					won++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if won != 1 {
			t.Errorf("%d replicas hold the lease, want exactly 1", won)
		}
	})
}
