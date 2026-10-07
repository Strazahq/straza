// Package spine runs the event-spine background workers: the
// transactional-outbox relay (DB → JetStream, at-least-once) and the audit
// consumer (JetStream → hash-chained audit_log). Both are idempotent so a
// crash between "tx committed" and "published" loses nothing and duplicates
// nothing.
package spine

import (
	"context"
	"log/slog"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// publisher is the one Bus method the relay uses, as an interface so tests
// can fail specific subjects (e.g. "the audit stream's disk is full").
type publisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

// Relay drains the events_outbox into JetStream: publish each event, then
// mark it published. A kill between publish and mark re-publishes on restart;
// consumers dedupe by CloudEvent id.
type Relay struct {
	store store.Store
	bus   publisher
	log   *slog.Logger
	tick  time.Duration
	batch int

	wake chan struct{}
}

// NewRelay builds the outbox relay. bus is *events.Bus in production.
func NewRelay(st store.Store, bus publisher, log *slog.Logger) *Relay {
	return &Relay{store: st, bus: bus, log: log, tick: time.Second, batch: 128, wake: make(chan struct{}, 1)}
}

// Nudge asks the relay to drain now (best-effort; never blocks).
func (r *Relay) Nudge() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run drains on a ticker (and on Nudge) until ctx is done.
func (r *Relay) Run(ctx context.Context) {
	t := time.NewTicker(r.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.drain(ctx)
		case <-r.wake:
			r.drain(ctx)
		}
	}
}

// drain publishes all currently-unpublished events in two passes: CONTROL
// events first (revocations, policy, identity, apps), then bulk
// audit/capture. A stalled or full audit stream must never head-of-line block
// the kill switch behind transcript turns. Cross-class FIFO is not
// load-bearing (the classes land on different streams with independent
// consumers), and order WITHIN each class is preserved.
func (r *Relay) drain(ctx context.Context) {
	r.drainPass(ctx, true)
	r.drainPass(ctx, false)
}

// drainPass drains one class through the store's claimed drain:
// claim, publish, mark: one transaction per batch, SKIP LOCKED on Postgres,
// so replicas take disjoint batches and share the work instead of
// re-publishing each other's rows. A publish failure ends the pass; the
// already-published rows are marked, the rest retry next tick.
func (r *Relay) drainPass(ctx context.Context, controlOnly bool) {
	for {
		n, err := r.store.Outbox().DrainClaimed(ctx, controlOnly, r.batch, func(e store.OutboxEvent) error {
			pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return r.bus.Publish(pubCtx, e.Subject, []byte(e.CE))
		})
		if err != nil {
			r.log.Warn("relay: drain", "control", controlOnly, "err", err)
			return
		}
		if n < r.batch {
			return // pass drained (or a claim raced another pod; next tick)
		}
	}
}
