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
