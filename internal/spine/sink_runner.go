package spine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/strazahq/straza/internal/events"
)

// SinkStats is a runner's since-boot delivery tally (admin list, tests).
type SinkStats struct {
	Delivered   uint64    `json:"delivered"`
	Duplicates  uint64    `json:"duplicates"`
	Parked      uint64    `json:"parked"`
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at,omitzero"`
}

// SinkRunner drains one (sink, stream) pair. A sink whose subject filter
// spans both spine streams gets one runner per stream; durables are
// per-stream namespaces, so they share the sink's name.
//
// Delivery outcomes (sink_policy.go): success and 409 ack; a deterministic
// refusal parks after a short budget; a transient failure redelivers with
// exponential backoff and parks after a long budget. Parked events go to
// the dead-letter lane (deadletter.go), counted and replayable; the source
// message is acked only after the park landed, so nothing is ever dropped.
type SinkRunner struct {
	bus     *events.Bus
	sink    Sink
	stream  string
	filters []string
	log     *slog.Logger

	// Policy is the redelivery schedule and park rule (DefaultRetryPolicy).
	Policy RetryPolicy
	// Batch > 1 switches to batched fetch + DeliverBatch when the
	// sink supports it. Opt-in per sink config; 0/1 keeps the documented
	// one-event-per-delivery contract.
	Batch int
	// Observe receives park/duplicate counts (nil = none).
	Observe SinkMetrics
	// WarnInterval rate-limits the failure warn lines: one per interval per
	// class, carrying the suppressed count (default 10 s).
	WarnInterval time.Duration

	delivered  atomic.Uint64
	duplicates atomic.Uint64
	parked     atomic.Uint64

	mu         sync.Mutex
	consumer   jetstream.Consumer
	lastErr    string
	lastErrAt  time.Time
	warnAt     map[string]time.Time
	suppressed map[string]int
}

// NewSinkRunner builds a runner for the filters that overlap one stream.
func NewSinkRunner(bus *events.Bus, sink Sink, stream string, filters []string, log *slog.Logger) *SinkRunner {
	return &SinkRunner{bus: bus, sink: sink, stream: stream, filters: filters, log: log,
		Policy: DefaultRetryPolicy(), WarnInterval: 10 * time.Second,
		warnAt: map[string]time.Time{}, suppressed: map[string]int{}}
}

// Label identifies the runner in logs: "<sink>@<stream>".
func (r *SinkRunner) Label() string { return r.sink.Name() + "@" + r.stream }

// SinkName is the sink this runner feeds.
func (r *SinkRunner) SinkName() string { return r.sink.Name() }

// Stream is the spine stream this runner drains.
func (r *SinkRunner) Stream() string { return r.stream }

// Target is the Sink (the admin replay re-feeds it).
func (r *SinkRunner) Target() Sink { return r.sink }

// Filters exposes the effective subject filters (diagnostics and tests).
func (r *SinkRunner) Filters() []string { return append([]string(nil), r.filters...) }

// Stats is the since-boot tally.
func (r *SinkRunner) Stats() SinkStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return SinkStats{Delivered: r.delivered.Load(), Duplicates: r.duplicates.Load(),
		Parked: r.parked.Load(), LastError: r.lastErr, LastErrorAt: r.lastErrAt}
}

// Backlog reports the durable consumer's not-yet-delivered count and its
// delivered-but-unacked (in-flight or awaiting redelivery) count; an error
// when the runner has no consumer yet.
func (r *SinkRunner) Backlog(ctx context.Context) (pending, inflight uint64, err error) {
	r.mu.Lock()
	cons := r.consumer
	r.mu.Unlock()
	if cons == nil {
		return 0, 0, fmt.Errorf("sink %s: consumer not running", r.Label())
	}
	info, err := cons.Info(ctx)
	if err != nil {
		return 0, 0, err
	}
	return info.NumPending, uint64(info.NumAckPending), nil // #nosec G115 -- consumer counts
}

// Run consumes until ctx is done; the caller restarts Run on hard errors.
func (r *SinkRunner) Run(ctx context.Context) error {
	cons, err := r.bus.SinkConsumer(ctx, r.stream, "sink-"+sanitizeDurable(r.sink.Name()), r.filters)
	if err != nil {
		return fmt.Errorf("spine: sink %s consumer on %s: %w", r.sink.Name(), r.stream, err)
	}
	r.mu.Lock()
	r.consumer = cons
	r.mu.Unlock()
	if bs, ok := r.sink.(BatchSink); ok && r.Batch > 1 {
		return r.runBatched(ctx, cons, bs)
	}
	iter, err := cons.Messages()
	if err != nil {
		return fmt.Errorf("spine: sink %s messages: %w", r.sink.Name(), err)
	}
	go func() {
		<-ctx.Done()
		iter.Stop()
	}()
	for {
		msg, err := iter.Next()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("spine: sink %s next: %w", r.sink.Name(), err)
		}
		r.settle(ctx, []jetstream.Msg{msg}, r.sink.Deliver(ctx, msg.Subject(), msg.Data()))
	}
}

// runBatched is the opt-in batched loop: fetch up to Batch messages,
// deliver them in ONE call, then settle the whole batch together (the
// receiver answered for all of them and cannot say which line offended).
func (r *SinkRunner) runBatched(ctx context.Context, cons jetstream.Consumer, sink BatchSink) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		batch, err := cons.Fetch(r.Batch, jetstream.FetchMaxWait(2*time.Second))
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("spine: sink %s fetch: %w", r.sink.Name(), err)
		}
		var msgs []jetstream.Msg
		var evs []SinkEvent
		for m := range batch.Messages() {
			msgs = append(msgs, m)
			evs = append(evs, SinkEvent{Subject: m.Subject(), CE: m.Data()})
		}
		if err := batch.Error(); err != nil {
			r.log.Warn("sink fetch", "sink", r.sink.Name(), "err", err)
		}
		if len(msgs) == 0 {
			continue
		}
		r.settle(ctx, msgs, sink.DeliverBatch(ctx, evs))
	}
}

// settle applies one delivery outcome to the messages it covered.
func (r *SinkRunner) settle(ctx context.Context, msgs []jetstream.Msg, err error) {
	n := uint64(len(msgs))
	switch class := classify(err); class {
	case classDelivered:
		for _, m := range msgs {
			_ = m.Ack()
		}
		r.delivered.Add(n)
	case classDuplicate:
		for _, m := range msgs {
			_ = m.Ack()
			if r.Observe != nil {
				r.Observe.SinkDuplicate(r.sink.Name())
			}
		}
		r.duplicates.Add(n)
		r.log.Info("sink reported the event already delivered; acked", "sink", r.sink.Name(), "events", n)
	default:
		attempts := maxDelivered(msgs)
		r.noteError(err)
		if r.Policy.park(class, attempts) {
			r.parkAll(ctx, msgs, attempts, err)
			return
		}
		delay := r.Policy.delay(attempts)
		for _, m := range msgs {
			_ = m.NakWithDelay(delay)
		}
		r.warn("retry", "sink delivery failing; will redeliver", "sink", r.sink.Name(),
			"class", class.String(), "attempt", attempts, "next_in", delay, "events", n, "err", err)
	}
}

// parkAll moves every message of a failed delivery to the dead-letter lane
// and acks it on the source stream; a park that cannot land (lane full, bus
// down) keeps the message retrying instead, loudly.
func (r *SinkRunner) parkAll(ctx context.Context, msgs []jetstream.Msg, attempts uint64, cause error) {
	key := sanitizeDurable(r.sink.Name())
	reason := cause.Error()
	var parked uint64
	for _, m := range msgs {
		var seq uint64
		if md, err := m.Metadata(); err == nil {
			seq = md.Sequence.Stream
		}
		rec := newDeadLetterRecord(r.sink.Name(), r.stream, m.Subject(), seq, attempts, reason, m.Data())
		raw, err := json.Marshal(rec)
		if err == nil {
			err = r.bus.ParkDeadLetter(ctx, key, raw)
		}
		if err != nil {
			_ = m.NakWithDelay(r.Policy.delay(attempts))
			r.warn("parkfail", "sink dead-letter park failed; keeping the event in its stream",
				"sink", r.sink.Name(), "err", err)
			continue
		}
		_ = m.Ack()
		parked++
		if r.Observe != nil {
			r.Observe.SinkParked(r.sink.Name())
		}
	}
	r.parked.Add(parked)
	r.warn("park", "sink delivery parked to dead-letter; replay with strazactl sinks replay",
		"sink", r.sink.Name(), "events", parked, "attempts", attempts, "reason", reason)
}

// noteError records the latest failure for the admin surface.
func (r *SinkRunner) noteError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastErr = err.Error()
	r.lastErrAt = time.Now().UTC()
}

// warn logs at most one line per WarnInterval per key, carrying how many
// lines it swallowed since the last one, since a failing receiver can
// produce many lines per second.
func (r *SinkRunner) warn(key, msg string, attrs ...any) {
	r.mu.Lock()
	now := time.Now()
	if last, ok := r.warnAt[key]; ok && now.Sub(last) < r.WarnInterval {
		r.suppressed[key]++
		r.mu.Unlock()
		return
	}
	suppressed := r.suppressed[key]
	r.suppressed[key] = 0
	r.warnAt[key] = now
	r.mu.Unlock()
	r.log.Warn(msg, append(attrs, "suppressed", suppressed)...)
}

// maxDelivered is the highest delivery count across a batch (the batch
// NAKs as one, so the counts move together; max is the honest attempt).
func maxDelivered(msgs []jetstream.Msg) uint64 {
	var attempts uint64 = 1
	for _, m := range msgs {
		if md, err := m.Metadata(); err == nil && md.NumDelivered > attempts {
			attempts = md.NumDelivered
		}
	}
	return attempts
}

// sanitizeDurable maps a sink name onto the durable-name alphabet.
func sanitizeDurable(name string) string {
	return strings.Map(func(c rune) rune {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			return c
		default:
			return '-'
		}
	}, name)
}
