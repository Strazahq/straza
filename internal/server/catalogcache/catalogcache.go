// Package catalogcache holds the bounded LRU, the minimal singleflight and the
// notification coalescer the gateway's two-tier tool catalog cache is built from.
package catalogcache

import (
	"container/list"
	"sync"
	"time"
)

// This file holds the small concurrency primitives the gateway catalog cache is
// built from: a bounded LRU map, a minimal singleflight, and a notification
// coalescer. They are deliberately stdlib-only and unaware of catalogs; the
// gateway wires them together in catalog.go and gateway.go. None is safe for
// concurrent use on its own except where noted; the LRU expects the caller to
// hold the gateway mutex, while the flight group and coalescer carry their own.

// LRU is a string-keyed cache with least-recently-used eviction: a map for
// O(1) lookup beside a container/list that orders entries by recency (front =
// most recently used). It bounds the number of live entries so a large or
// churning fleet cannot grow the catalog caches without limit, and it does
// so without throwing everything away. Values are opaque (any); callers
// type-assert what they stored. NOT safe
// for concurrent use: the gateway holds its mutex across every call.
type LRU struct {
	cap int
	ll  *list.List               // front = most recently used, back = eviction target
	m   map[string]*list.Element // key → element carrying an *lruEntry
}

type lruEntry struct {
	key string
	val any
}

// NewLRU builds an LRU bounded to capacity entries. A capacity <= 0 makes the
// map unbounded (no eviction); the gateway always passes a positive cap.
func NewLRU(capacity int) *LRU {
	return &LRU{cap: capacity, ll: list.New(), m: map[string]*list.Element{}}
}

// Get returns the value for key and bumps it to most-recently-used.
func (c *LRU) Get(key string) (any, bool) {
	if e, ok := c.m[key]; ok {
		c.ll.MoveToFront(e)
		return e.Value.(*lruEntry).val, true
	}
	return nil, false
}

// Put inserts or updates key and, when that pushes the map past its cap, evicts
// the least-recently-used entries until it fits.
func (c *LRU) Put(key string, val any) {
	if e, ok := c.m[key]; ok {
		e.Value.(*lruEntry).val = val
		c.ll.MoveToFront(e)
		return
	}
	c.m[key] = c.ll.PushFront(&lruEntry{key: key, val: val})
	for c.cap > 0 && c.ll.Len() > c.cap {
		if oldest := c.ll.Back(); oldest != nil {
			c.ll.Remove(oldest)
			delete(c.m, oldest.Value.(*lruEntry).key)
		}
	}
}

// Delete removes key if present.
func (c *LRU) Delete(key string) {
	if e, ok := c.m[key]; ok {
		c.ll.Remove(e)
		delete(c.m, key)
	}
}

// DeleteFunc drops every entry for which pred reports true: the mechanism
// behind targeted invalidation, where only cache entries touching a changed
// role are evicted.
func (c *LRU) DeleteFunc(pred func(key string, val any) bool) {
	var next *list.Element
	for e := c.ll.Front(); e != nil; e = next {
		next = e.Next()
		ent := e.Value.(*lruEntry)
		if pred(ent.key, ent.val) {
			c.ll.Remove(e)
			delete(c.m, ent.key)
		}
	}
}

// Flush empties the map: the full-invalidation hammer.
func (c *LRU) Flush() {
	c.ll.Init()
	c.m = map[string]*list.Element{}
}

// Len reports the number of live entries.
func (c *LRU) Len() int { return c.ll.Len() }

// FlightGroup is a minimal singleflight: concurrent callers keyed the same
// share ONE execution of fn, each receiving its result. It collapses the
// post-invalidation rebuild storm: N sessions racing to build the identical
// tier-1 catalog compute it once. Stdlib only, on purpose: the one thing it does
// is not worth a dependency on golang.org/x/sync.
type FlightGroup struct {
	mu sync.Mutex
	m  map[string]*flightCall
}

type flightCall struct {
	wg  sync.WaitGroup
	val any
}

// NewFlightGroup returns an empty group.
func NewFlightGroup() *FlightGroup {
	return &FlightGroup{m: map[string]*flightCall{}}
}

// Do runs fn for key unless a call for the same key is already in flight, in
// which case it waits for that call and returns its result. The leader clears
// the key once fn returns, so the next Do starts fresh.
func (g *FlightGroup) Do(key string, fn func() any) any {
	g.mu.Lock()
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val
	}
	c := &flightCall{}
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	c.val = fn()
	c.wg.Done()

	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()
	return c.val
}

// Coalescer debounces a fan-out callback so a burst of triggers collapses
// to a single fire. With delay 0 it fires synchronously on the calling
// goroutine (the pre-debounce behavior tests lean on). Otherwise the first
// Trigger schedules fire after delay via time.AfterFunc and records the pending
// timer; triggers landing while a fire is pending ride the already-scheduled
// one, so an invalidation herd becomes one broadcast. The caller must never hold
// the gateway mutex across Trigger; fire fans out to the SSE hub.
type Coalescer struct {
	fire func()

	mu    sync.Mutex
	delay time.Duration
	timer *time.Timer // non-nil while a fire is pending
}

// NewCoalescer returns a coalescer that runs fire once per burst after delay
// (0 = synchronous on the triggering goroutine).
func NewCoalescer(delay time.Duration, fire func()) *Coalescer {
	return &Coalescer{delay: delay, fire: fire}
}

// Trigger requests a fire, coalescing with any already pending.
func (c *Coalescer) Trigger() {
	c.mu.Lock()
	if c.delay <= 0 {
		c.mu.Unlock()
		c.fire()
		return
	}
	if c.timer != nil {
		c.mu.Unlock()
		return
	}
	c.timer = time.AfterFunc(c.delay, func() {
		c.mu.Lock()
		c.timer = nil
		c.mu.Unlock()
		c.fire()
	})
	c.mu.Unlock()
}

// SetDelay stops any pending fire and switches to a new delay. Tests use it to
// make notifications synchronous (delay 0) without a fire scheduled by an
// earlier trigger straggling onto a later subscriber.
func (c *Coalescer) SetDelay(d time.Duration) {
	c.mu.Lock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.delay = d
	c.mu.Unlock()
}
