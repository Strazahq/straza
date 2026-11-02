package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestDeviceSpendProof pins the shared spent-proof record on both drivers.
// The first spend of a key's proof id answers true and the second false,
// the same proof id under another key is a separate spend, a row past its
// expiry is swept so the same proof id spends again, and only a store
// failure answers an error.
func TestDeviceSpendProof(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		until := now().Add(3 * time.Minute)
		steps := []struct {
			name       string
			thumbprint string
			jti        string
			until      time.Time
			spent      bool
		}{
			{"the first spend of a proof id", "key-a", "jti-1", until, true},
			{"the same proof id again", "key-a", "jti-1", until, false},
			{"the same proof id under another key", "key-b", "jti-1", until, true},
			{"another proof id under the first key", "key-a", "jti-2", until, true},
			{"a spend whose row is already past its expiry", "key-a", "jti-old", now().Add(-time.Minute), true},
			{"the same proof id after its row expired", "key-a", "jti-old", until, true},
			{"that proof id once more", "key-a", "jti-old", until, false},
		}
		for _, st := range steps {
			spent, err := s.Devices().SpendProof(ctx, st.thumbprint, st.jti, st.until)
			if err != nil || spent != st.spent {
				t.Errorf("%s: SpendProof(%q, %q) = %v, %v; want %v", st.name, st.thumbprint, st.jti, spent, err, st.spent)
			}
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if spent, err := s.Devices().SpendProof(cancelled, "key-a", "jti-3", until); err == nil || spent {
			t.Errorf("SpendProof with the store unreachable = %v, %v; want false and an error", spent, err)
		}
	})
}

// TestDeviceSpendProofConcurrent races one proof on many goroutines, the
// shape of a proof replayed on several replicas at once, on both drivers.
// Exactly one spend answers true.
func TestDeviceSpendProofConcurrent(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		until := now().Add(3 * time.Minute)
		const racers = 32
		spent := make([]bool, racers)
		errs := make([]error, racers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range racers {
			wg.Go(func() {
				<-start
				spent[i], errs[i] = s.Devices().SpendProof(ctx, "key-a", "jti-race", until)
			})
		}
		close(start)
		wg.Wait()
		wins := 0
		for i := range racers {
			if errs[i] != nil {
				t.Fatalf("SpendProof racer %d: %v", i, errs[i])
			}
			if spent[i] {
				wins++
			}
		}
		if wins != 1 {
			t.Errorf("%d racers spent one proof, want exactly one", wins)
		}
	})
}
