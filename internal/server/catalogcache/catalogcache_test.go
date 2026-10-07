package catalogcache

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLRUMap pins the bounded LRU: recency-ordered eviction past the cap, Get
// bumping recency, and targeted/whole removal.
func TestLRUMap(t *testing.T) {
	c := NewLRU(2)
	c.Put("a", 1)
	c.Put("b", 2)
	if c.Len() != 2 {
		t.Fatalf("len = %d, want 2", c.Len())
	}

	// Touch "a" so "b" becomes the least-recently-used, then overflow: "b" goes.
	if v, ok := c.Get("a"); !ok || v.(int) != 1 {
		t.Fatalf("Get(a) = %v,%v, want 1,true", v, ok)
	}
	c.Put("c", 3)
	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted as least-recently-used")
	}
	if _, ok := c.Get("a"); !ok {
		t.Error("a should have survived (it was the most-recently-used)")
	}
	if _, ok := c.Get("c"); !ok {
		t.Error("c should be present")
	}

	// Update-in-place must not grow the map or evict.
	c.Put("a", 11)
	if v, _ := c.Get("a"); v.(int) != 11 || c.Len() != 2 {
		t.Errorf("update-in-place: a=%v len=%d, want 11,2", v, c.Len())
	}

	// Delete one key.
	c.Delete("a")
	if _, ok := c.Get("a"); ok {
		t.Error("a should be deleted")
	}

	// DeleteFunc drops only matching entries (fresh map with room for all three).
	d := NewLRU(8)
	d.Put("keep", 1)
	d.Put("drop1", 2)
	d.Put("drop2", 3)
	d.DeleteFunc(func(k string, _ any) bool { return k != "keep" })
	if d.Len() != 1 {
		t.Errorf("after DeleteFunc len = %d, want 1", d.Len())
	}
	if _, ok := d.Get("keep"); !ok {
		t.Error("keep should have survived DeleteFunc")
	}

	// Flush empties everything.
	d.Flush()
	if d.Len() != 0 {
		t.Errorf("after Flush len = %d, want 0", d.Len())
	}
}

// TestFlightGroupCoalesces pins the singleflight: concurrent callers keyed the
// same run fn once and all receive the identical result.
func TestFlightGroupCoalesces(t *testing.T) {
	g := NewFlightGroup()
	const n = 8
	var calls atomic.Int64
	release := make(chan struct{})
	start := make(chan struct{})
	results := make([]any, n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			results[idx] = g.Do("k", func() any {
				calls.Add(1)
				<-release // hold the flight open so the followers pile up on it
				return new(int)
			})
		}(i)
	}
	close(start)
	// Let every goroutine enter Do and block on the leader before releasing it.
	time.Sleep(30 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Errorf("fn ran %d times, want 1 (coalesced)", got)
	}
	for i := 1; i < n; i++ {
		if results[i] != results[0] {
			t.Errorf("caller %d got a different result pointer than caller 0", i)
		}
	}

	// A fresh Do after the flight cleared runs fn again.
	got := g.Do("k", func() any { calls.Add(1); return new(int) })
	if got == results[0] {
		t.Error("a post-flight Do reused the previous result: the key was not cleared")
	}
	if calls.Load() != 2 {
		t.Errorf("fn ran %d times total, want 2", calls.Load())
	}
}

// TestNotifyCoalescer pins both modes: delay 0 fires synchronously per Trigger,
// and a debounced burst within one window collapses to a single fire.
func TestNotifyCoalescer(t *testing.T) {
	var sync0 atomic.Int64
	c0 := NewCoalescer(0, func() { sync0.Add(1) })
	c0.Trigger()
	if sync0.Load() != 1 {
		t.Errorf("delay-0 Trigger fired %d times synchronously, want 1", sync0.Load())
	}
	c0.Trigger()
	if sync0.Load() != 2 {
		t.Errorf("delay-0 second Trigger = %d, want 2", sync0.Load())
	}

	var fires atomic.Int64
	c := NewCoalescer(80*time.Millisecond, func() { fires.Add(1) })
	c.Trigger()
	c.Trigger()
	c.Trigger()
	if got := fires.Load(); got != 0 {
		t.Errorf("debounced fire happened early: %d", got)
	}
	time.Sleep(200 * time.Millisecond)
	if got := fires.Load(); got != 1 {
		t.Errorf("debounced burst fired %d times, want 1", got)
	}

	// A trigger after the window opens a new one.
	c.Trigger()
	time.Sleep(200 * time.Millisecond)
	if got := fires.Load(); got != 2 {
		t.Errorf("post-window trigger fired total %d, want 2", got)
	}

	// SetDelay(0) cancels a pending fire and switches to synchronous.
	var s2 atomic.Int64
	c2 := NewCoalescer(500*time.Millisecond, func() { s2.Add(1) })
	c2.Trigger() // schedules a fire ~500ms out
	c2.SetDelay(0)
	time.Sleep(50 * time.Millisecond)
	if got := s2.Load(); got != 0 {
		t.Errorf("SetDelay(0) did not cancel the pending fire: %d", got)
	}
	c2.Trigger()
	if s2.Load() != 1 {
		t.Errorf("after SetDelay(0), Trigger = %d, want 1 (synchronous)", s2.Load())
	}
}
