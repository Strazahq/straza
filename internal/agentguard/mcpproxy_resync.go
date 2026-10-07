package agentguard

import (
	"context"
	"math/rand/v2"
	"time"
)

// scheduleResync defers a resync to a random point within [0, jitterMax) and
// folds a burst of notifications into one run. A single binding edit or policy
// activation broadcasts list_changed to every live session at once; re-listing
// the instant it arrives would put the whole fleet on the gateway together, and
// the go-sdk server re-notifies per AddTool/RemoveTools, so one logical change
// often lands as several notifications. The pending flag coalesces the burst;
// the jitter spreads the herd. jitterMax == 0 still defers through AfterFunc(0)
// (asynchronous but immediate), so the handler never lists inline.
func (p *mcpProxy) scheduleResync() {
	p.mu.Lock()
	if p.resyncPending || p.resyncStopped {
		p.mu.Unlock()
		return
	}
	p.resyncPending = true
	ctx := p.ctx
	p.timers.Add(1)
	p.mu.Unlock()

	delay := time.Duration(0)
	if p.jitterMax > 0 {
		delay = rand.N(p.jitterMax) // #nosec G404 -- herd-spreading jitter, not a security boundary
	}
	time.AfterFunc(delay, func() {
		defer p.timers.Done()
		p.mu.Lock()
		p.resyncPending = false
		p.mu.Unlock()
		if ctx.Err() != nil {
			return // proxy shut down while the timer was armed
		}
		// Clear pending BEFORE listing so a notification arriving during the
		// walk schedules the next pass rather than being dropped.
		p.resync(ctx)
	})
}

// stopResyncs refuses every later scheduled resync and waits for the armed
// ones, so shutdown leaves no stray resync goroutine behind.
func (p *mcpProxy) stopResyncs() {
	p.mu.Lock()
	p.resyncStopped = true
	p.mu.Unlock()
	p.timers.Wait()
}

// runPeriodicResync is the backstop for notifications that never arrived. The
// SSE hub drops on slow consumers by design and has no resumability, so a proxy
// can miss a list_changed outright; this loop re-lists on a jittered interval so
// the mirror converges regardless. It runs until ctx ends.
func (p *mcpProxy) runPeriodicResync(ctx context.Context) {
	for {
		every := p.periodicEvery
		if p.periodicJitter > 0 {
			every += rand.N(p.periodicJitter) // #nosec G404 -- herd-spreading jitter, not a security boundary
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
			p.resync(ctx)
		}
	}
}
