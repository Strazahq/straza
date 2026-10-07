// Package ratelimit is the lazy per-key token-bucket limiter strazad uses for
// per-IP, per-session and per-app throttling (in-memory, per pod).
package ratelimit

import (
	"sync"
	"time"
)

// tokenBucket is a lazy (no background goroutine) token-bucket rate limiter.
// Tokens refill continuously at `rate` per second up to `burst`; each allowed
// event costs one token. In-memory and per-pod: there is no global limit
// across pods.
type tokenBucket struct {
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func (b *tokenBucket) allow(now time.Time) bool {
	elapsed := now.Sub(b.last).Seconds()
	b.last = now
	b.tokens = min(b.burst, b.tokens+elapsed*b.rate)
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// Limiter holds per-key buckets, evicting idle ones so the map cannot
// grow without bound under churn (many short sessions).
type Limiter struct {
	mu        sync.Mutex
	buckets   map[string]*tokenBucket
	lastSweep time.Time
	// now is injectable for tests.
	now func() time.Time
}

// New returns an empty limiter; the clock is injectable for tests.
func New() *Limiter {
	return &Limiter{buckets: map[string]*tokenBucket{}, now: time.Now}
}

// Allow reports whether an event on key is permitted given rps (events/sec).
// rps <= 0 disables limiting for that key. burst is max(rps, 1) so a single
// call is never rejected on a cold bucket.
func (l *Limiter) Allow(key string, rps float64) bool {
	if rps <= 0 {
		return true
	}
	burst := max(rps, 1)
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b == nil {
		// Born full and touched now, so a sweep in this same call can never
		// evict the bucket it is about to charge.
		b = &tokenBucket{rate: rps, burst: burst, tokens: burst, last: now}
		l.buckets[key] = b
	} else if b.rate != rps || b.burst != burst {
		// The manifest limit changed (redeploy): live buckets adopt it
		// immediately instead of enforcing the old rate until eviction.
		b.rate = rps
		b.burst = burst
		b.tokens = min(b.tokens, burst)
	}
	l.evictIdle(now)
	return b.allow(now)
}

// evictIdle drops buckets untouched for 10 minutes. Amortized: it scans only
// when the map is large enough to matter and at most once per minute, so the
// gateway hot path never pays a full-map sweep per call.
func (l *Limiter) evictIdle(now time.Time) {
	if len(l.buckets) < 1024 || now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		if now.Sub(b.last) > 10*time.Minute {
			delete(l.buckets, k)
		}
	}
}
