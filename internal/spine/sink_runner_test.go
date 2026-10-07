package spine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// receiver is a scripted webhook endpoint: statusFor decides the answer per
// body, and every accepted body is recorded.
type receiver struct {
	mu        sync.Mutex
	bodies    []string
	hits      atomic.Int64
	statusFor func(body string) int
}

func (rc *receiver) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rc.hits.Add(1)
		st := rc.statusFor(string(body))
		if st >= 200 && st <= 299 {
			rc.mu.Lock()
			rc.bodies = append(rc.bodies, string(body))
			rc.mu.Unlock()
		}
		w.WriteHeader(st)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (rc *receiver) accepted() []string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]string(nil), rc.bodies...)
}

func runRunner(t *testing.T, ctx context.Context, r *SinkRunner) {
	t.Helper()
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- r.Run(runCtx) }()
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("runner did not stop")
		}
	})
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestSinkRunnerParksDeterministicAndKeepsFlowing: a poison event the
// receiver answers 400 to parks in the dead-letter lane after the
// deterministic attempt budget, the source message is acked (the lane
// keeps flowing), the record is self-describing, and a healthy event behind
// it is delivered normally.
func TestSinkRunnerParksDeterministicAndKeepsFlowing(t *testing.T) {
	bus, ctx := startSpineBus(t)
	rc := &receiver{statusFor: func(body string) int {
		if strings.Contains(body, "poison") {
			return http.StatusBadRequest
		}
		return http.StatusOK
	}}
	srv := rc.start(t)
	obs := newFakeSinkMetrics()

	if err := bus.Publish(ctx, "straza.audit.tool", []byte(`{"id":"poison-1"}`)); err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(ctx, "straza.audit.tool", []byte(`{"id":"good-1"}`)); err != nil {
		t.Fatal(err)
	}
	runner := NewSinkRunner(bus, NewWebhookSink("es", srv.URL, nil, nil), "STRAZA_AUDIT",
		[]string{"straza.audit.>"}, quietLog())
	runner.Policy = fastPolicy(2, 100)
	runner.Observe = obs
	runRunner(t, ctx, runner)

	waitFor(t, 10*time.Second, "good event delivered and poison parked", func() bool {
		n, err := bus.DeadLetterCount(ctx, "es")
		parked, _, _ := obs.counts("es")
		return err == nil && n == 1 && parked == 1 && len(rc.accepted()) == 1
	})
	if got := rc.accepted(); len(got) != 1 || !strings.Contains(got[0], "good-1") {
		t.Fatalf("accepted = %v, want only good-1", got)
	}
	// The source consumer has nothing in flight: the poison message was acked
	// once parked, so the lane is not wedged behind it.
	waitFor(t, 5*time.Second, "source consumer drained", func() bool {
		pending, inflight, err := runner.Backlog(ctx)
		return err == nil && pending == 0 && inflight == 0
	})
	st := runner.Stats()
	if st.Delivered != 1 || st.Parked != 1 || !strings.Contains(st.LastError, "HTTP 400") || st.LastErrorAt.IsZero() {
		t.Fatalf("Stats = %+v", st)
	}

	// The parked record is self-describing and carries the event verbatim.
	cons, err := bus.DeadLetterConsumer(ctx, "es")
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := cons.Fetch(1)
	if err != nil {
		t.Fatal(err)
	}
	var rec DeadLetterRecord
	for m := range msgs.Messages() {
		if err := json.Unmarshal(m.Data(), &rec); err != nil {
			t.Fatalf("record json: %v", err)
		}
	}
	if rec.Sink != "es" || rec.Stream != "STRAZA_AUDIT" || rec.Subject != "straza.audit.tool" ||
		rec.Attempts != 2 || !strings.Contains(rec.Reason, "HTTP 400") || rec.ParkedAt.IsZero() ||
		!bytes.Equal(bytes.TrimSpace(rec.CE), []byte(`{"id":"poison-1"}`)) {
		t.Fatalf("record = %+v", rec)
	}
}

// TestSinkRunnerRetryableHealsThenCaps: a transient 503 pair heals on the
// third attempt (nothing parked), and an endless 503 parks once the
// retryable attempt cap is spent (the poison-on-5xx case), never looping
// forever.
func TestSinkRunnerRetryableHealsThenCaps(t *testing.T) {
	bus, ctx := startSpineBus(t)
	var fails atomic.Int64
	fails.Store(2)
	rc := &receiver{statusFor: func(body string) int {
		if strings.Contains(body, "forever") {
			return http.StatusServiceUnavailable
		}
		if fails.Add(-1) >= 0 {
			return http.StatusServiceUnavailable
		}
		return http.StatusOK
	}}
	srv := rc.start(t)
	obs := newFakeSinkMetrics()

	if err := bus.Publish(ctx, "straza.audit.tool", []byte(`{"id":"heals-1"}`)); err != nil {
		t.Fatal(err)
	}
	runner := NewSinkRunner(bus, NewWebhookSink("es", srv.URL, nil, nil), "STRAZA_AUDIT",
		[]string{"straza.audit.>"}, quietLog())
	runner.Policy = fastPolicy(3, 4)
	runner.Observe = obs
	runRunner(t, ctx, runner)

	waitFor(t, 10*time.Second, "transient failure healed", func() bool {
		return len(rc.accepted()) == 1
	})
	if n, _ := bus.DeadLetterCount(ctx, "es"); n != 0 {
		t.Fatalf("healed event parked: %d", n)
	}
	if rc.hits.Load() != 3 {
		t.Fatalf("receiver hits = %d, want 3 (two 503s then 200)", rc.hits.Load())
	}

	if err := bus.Publish(ctx, "straza.audit.tool", []byte(`{"id":"forever-1"}`)); err != nil {
		t.Fatal(err)
	}
	// The runner parks the event before it tells the observer, so the wait
	// covers both counts.
	waitFor(t, 10*time.Second, "endless 503 parked at the cap and observed", func() bool {
		n, err := bus.DeadLetterCount(ctx, "es")
		parked, _, _ := obs.counts("es")
		return err == nil && n == 1 && parked == 1
	})
	if rc.hits.Load() != 3+4 {
		t.Fatalf("receiver hits = %d, want 7 (cap of 4 attempts for the poison)", rc.hits.Load())
	}
	parked, _, _ := obs.counts("es")
	if parked != 1 {
		t.Fatalf("observer parked = %d", parked)
	}
}

// TestSinkRunnerDuplicateIsDelivered: a 409 means the receiver already
// holds the event (the documented dedupe contract), so the message is acked
// and counted as a duplicate, never parked, never retried.
func TestSinkRunnerDuplicateIsDelivered(t *testing.T) {
	bus, ctx := startSpineBus(t)
	rc := &receiver{statusFor: func(string) int { return http.StatusConflict }}
	srv := rc.start(t)
	obs := newFakeSinkMetrics()
	if err := bus.Publish(ctx, "straza.audit.tool", []byte(`{"id":"dup-1"}`)); err != nil {
		t.Fatal(err)
	}
	runner := NewSinkRunner(bus, NewWebhookSink("es", srv.URL, nil, nil), "STRAZA_AUDIT",
		[]string{"straza.audit.>"}, quietLog())
	runner.Policy = fastPolicy(2, 2)
	runner.Observe = obs
	runRunner(t, ctx, runner)

	waitFor(t, 10*time.Second, "duplicate acked", func() bool {
		_, dups, _ := obs.counts("es")
		pending, inflight, err := runner.Backlog(ctx)
		return dups == 1 && err == nil && pending == 0 && inflight == 0
	})
	time.Sleep(100 * time.Millisecond)
	if rc.hits.Load() != 1 {
		t.Fatalf("receiver hits = %d, want exactly 1 (no retry on 409)", rc.hits.Load())
	}
	if n, _ := bus.DeadLetterCount(ctx, "es"); n != 0 {
		t.Fatalf("duplicate parked: %d", n)
	}
	if st := runner.Stats(); st.Duplicates != 1 || st.Parked != 0 {
		t.Fatalf("Stats = %+v", st)
	}
}

// TestSinkRunnerBatchedParksWholeBatch: the opt-in batched face parks every
// event of a batch the receiver deterministically refuses (it cannot know
// which line offended), and the lane moves on.
func TestSinkRunnerBatchedParksWholeBatch(t *testing.T) {
	bus, ctx := startSpineBus(t)
	rc := &receiver{statusFor: func(string) int { return http.StatusRequestEntityTooLarge }}
	srv := rc.start(t)
	obs := newFakeSinkMetrics()
	for i := 0; i < 3; i++ {
		if err := bus.Publish(ctx, "straza.audit.tool", []byte(`{"id":"b`+string(rune('0'+i))+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewSinkRunner(bus, NewWebhookSink("bt", srv.URL, nil, nil), "STRAZA_AUDIT",
		[]string{"straza.audit.>"}, quietLog())
	runner.Batch = 10
	runner.Policy = fastPolicy(2, 2)
	runner.Observe = obs
	runRunner(t, ctx, runner)

	waitFor(t, 10*time.Second, "whole batch parked", func() bool {
		n, err := bus.DeadLetterCount(ctx, "bt")
		return err == nil && n == 3
	})
	waitFor(t, 5*time.Second, "batch consumer drained", func() bool {
		pending, inflight, err := runner.Backlog(ctx)
		return err == nil && pending == 0 && inflight == 0
	})
	parked, _, _ := obs.counts("bt")
	if parked != 3 {
		t.Fatalf("observer parked = %d, want 3", parked)
	}
}

// TestSinkRunnerWarnIsRateLimited: a failing receiver produces ONE warn
// line per WarnInterval per runner (carrying the suppressed count), not one
// per attempt.
func TestSinkRunnerWarnIsRateLimited(t *testing.T) {
	bus, ctx := startSpineBus(t)
	rc := &receiver{statusFor: func(string) int { return http.StatusServiceUnavailable }}
	srv := rc.start(t)

	var mu sync.Mutex
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&lockedWriter{mu: &mu, w: &buf}, nil))
	for i := 0; i < 5; i++ {
		if err := bus.Publish(ctx, "straza.audit.tool", []byte(`{"id":"w`+string(rune('0'+i))+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewSinkRunner(bus, NewWebhookSink("es", srv.URL, nil, nil), "STRAZA_AUDIT",
		[]string{"straza.audit.>"}, log)
	runner.Policy = fastPolicy(3, 1000)
	runner.WarnInterval = time.Hour
	runRunner(t, ctx, runner)

	waitFor(t, 10*time.Second, "many failed attempts", func() bool { return rc.hits.Load() >= 30 })
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if n := strings.Count(out, "sink delivery failing"); n != 1 {
		t.Fatalf("warn lines = %d, want exactly 1 within the interval:\n%s", n, out)
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
