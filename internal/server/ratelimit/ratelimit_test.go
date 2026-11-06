package ratelimit

import (
	"strconv"
	"testing"
	"time"
)

func TestTokenBucketRefill(t *testing.T) {
	l := New()
	base := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return base }

	// burst = rps = 5 → 5 immediate allows, 6th denied.
	for i := 0; i < 5; i++ {
		if !l.Allow("k", 5) {
			t.Fatalf("call %d denied within burst", i+1)
		}
	}
	if l.Allow("k", 5) {
		t.Fatal("6th call allowed; burst not enforced")
	}

	// After 1s at 5 rps, 5 more tokens (capped at burst).
	l.now = func() time.Time { return base.Add(time.Second) }
	got := 0
	for i := 0; i < 10; i++ {
		if l.Allow("k", 5) {
			got++
		}
	}
	if got != 5 {
		t.Errorf("refill granted %d allows, want 5", got)
	}
}

// TestNewKeyLimitedAtEvictionThreshold: once the map crosses the sweep
// threshold, a brand-new key must still be rate limited. A fresh bucket
// with a zero `last` would be evicted by the sweep inside the same allow()
// call, and every call would get a full-burst bucket: unlimited rps for
// exactly the many-key load the limiter exists to contain.
func TestNewKeyLimitedAtEvictionThreshold(t *testing.T) {
	l := New()
	base := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return base }

	for i := 0; i < 1100; i++ {
		if !l.Allow("session-"+strconv.Itoa(i)+"|app", 1) {
			t.Fatalf("cold key %d denied", i)
		}
	}
	if !l.Allow("hot|app", 1) {
		t.Fatal("first call on the new key must pass")
	}
	if l.Allow("hot|app", 1) {
		t.Fatal("second call on the new key allowed: limiter bypassed at >=1024 tracked keys")
	}
}

// TestRateChangeAppliesToLiveBuckets: lowering an app's manifest rps must take
// effect on actively-used keys immediately, not after idle eviction.
func TestRateChangeAppliesToLiveBuckets(t *testing.T) {
	l := New()
	base := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return base }

	for i := 0; i < 3; i++ {
		if !l.Allow("k", 100) {
			t.Fatalf("call %d denied at rps=100", i+1)
		}
	}
	// Redeploy drops the limit to 1: the bucket must clamp, not coast on the
	// ~97 tokens it accumulated under the old burst.
	if !l.Allow("k", 1) {
		t.Fatal("first call after limit change must still pass (clamped to new burst)")
	}
	if l.Allow("k", 1) {
		t.Fatal("old rate survived the manifest change")
	}
}

func TestRateLimitUnlimitedAndPerKey(t *testing.T) {
	l := New()
	base := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return base }

	// rps <= 0 disables limiting.
	for i := 0; i < 1000; i++ {
		if !l.Allow("free", 0) {
			t.Fatal("unlimited key throttled")
		}
	}
	// Keys are independent.
	if !l.Allow("a", 1) || !l.Allow("b", 1) {
		t.Fatal("first call per key must pass")
	}
	if l.Allow("a", 1) {
		t.Error("second call on key a should throttle")
	}
	if l.Allow("b", 1) {
		t.Error("second call on key b should throttle")
	}
}

// TestAllowBurst pins a bucket whose burst is given apart from its rate:
// five calls at once and then one every 12 seconds, refilled to the burst
// and never past it. Each step runs calls at the given offset from the
// start, in order, on one key.
func TestAllowBurst(t *testing.T) {
	l := New()
	base := time.Unix(1_700_000_000, 0)
	steps := []struct {
		name  string
		at    time.Duration
		calls int
		want  int // calls allowed
	}{
		{"the burst passes at once and the next call is refused", 0, 6, 5},
		{"less than one refill later nothing passes", 11 * time.Second, 1, 0},
		{"one refill later one call passes", 12 * time.Second, 2, 1},
		{"a long idle refills to the burst and no further", 10 * time.Minute, 7, 5},
	}
	for _, st := range steps {
		l.now = func() time.Time { return base.Add(st.at) }
		got := 0
		for i := 0; i < st.calls; i++ {
			if l.AllowBurst("k", 5.0/60, 5) {
				got++
			}
		}
		if got != st.want {
			t.Errorf("%s: allowed %d of %d, want %d", st.name, got, st.calls, st.want)
		}
	}
}

// TestReady pins the look that spends nothing: it answers what AllowBurst
// would answer now, on a cold key, a spent key and a refilled key, and
// any number of looks leave the bucket as it was.
func TestReady(t *testing.T) {
	l := New()
	base := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return base }
	for i := 0; i < 10; i++ {
		if !l.Ready("k", 5.0/60, 5) {
			t.Fatal("a cold key is not ready")
		}
	}
	for i := 0; i < 5; i++ {
		if !l.AllowBurst("k", 5.0/60, 5) {
			t.Fatalf("call %d refused after the looks, which must spend nothing", i+1)
		}
	}
	if l.Ready("k", 5.0/60, 5) {
		t.Error("a spent key is ready")
	}
	l.now = func() time.Time { return base.Add(12 * time.Second) }
	if !l.Ready("k", 5.0/60, 5) || !l.AllowBurst("k", 5.0/60, 5) || l.Ready("k", 5.0/60, 5) {
		t.Error("one refill must make the key ready for exactly one call")
	}
}
