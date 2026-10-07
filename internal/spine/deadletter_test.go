package spine

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestDeadLetterReplay: parked events replay to the sink in order, each one
// removed from the lane as it lands; a replay stops at the first failure
// and leaves the rest parked (no re-parking, no loss, an honest count).
func TestDeadLetterReplay(t *testing.T) {
	bus, ctx := startSpineBus(t)
	var refuse atomic.Bool
	refuse.Store(true)
	rc := &receiver{statusFor: func(body string) int {
		if refuse.Load() || strings.Contains(body, "still-bad") {
			return http.StatusBadRequest
		}
		return http.StatusOK
	}}
	srv := rc.start(t)
	obs := newFakeSinkMetrics()
	sink := NewWebhookSink("es", srv.URL, nil, nil)

	for _, id := range []string{"r1", "r2", "r3"} {
		if err := bus.Publish(ctx, "straza.audit.tool", []byte(`{"id":"`+id+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewSinkRunner(bus, sink, "STRAZA_AUDIT", []string{"straza.audit.>"}, quietLog())
	runner.Policy = fastPolicy(2, 2)
	runner.Observe = obs
	runRunner(t, ctx, runner)
	waitFor(t, 10*time.Second, "three parked", func() bool {
		n, err := bus.DeadLetterCount(ctx, "es")
		return err == nil && n == 3
	})

	refuse.Store(false)
	res, err := ReplaySink(ctx, bus, sink, 10, obs)
	if err != nil {
		t.Fatalf("ReplaySink: %v", err)
	}
	if res.Replayed != 3 || res.Remaining != 0 || res.Stopped != "" {
		t.Fatalf("replay = %+v", res)
	}
	got := strings.Join(rc.accepted(), " ")
	for _, id := range []string{"r1", "r2", "r3"} {
		if !strings.Contains(got, id) {
			t.Fatalf("replayed bodies %q miss %s", got, id)
		}
	}
	if n, _ := bus.DeadLetterCount(ctx, "es"); n != 0 {
		t.Fatalf("lane not emptied: %d", n)
	}
	if _, _, replayed := obs.counts("es"); replayed != 3 {
		t.Fatalf("observer replayed = %d", replayed)
	}

	// Second round: one replays, the next still fails: stop, keep it parked.
	refuse.Store(true)
	for _, id := range []string{"ok-4", "still-bad-5"} {
		if err := bus.Publish(ctx, "straza.audit.tool", []byte(`{"id":"`+id+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 10*time.Second, "two more parked", func() bool {
		n, err := bus.DeadLetterCount(ctx, "es")
		return err == nil && n == 2
	})
	refuse.Store(false)
	res, err = ReplaySink(ctx, bus, sink, 10, obs)
	if err != nil {
		t.Fatalf("ReplaySink 2: %v", err)
	}
	if res.Replayed != 1 || res.Remaining != 1 || !strings.Contains(res.Stopped, "HTTP 400") {
		t.Fatalf("replay 2 = %+v", res)
	}
	if n, _ := bus.DeadLetterCount(ctx, "es"); n != 1 {
		t.Fatalf("lane after partial replay = %d, want 1", n)
	}

	// A limit smaller than the lane replays that many and reports the rest.
	refuse.Store(true)
	for _, id := range []string{"l6", "l7"} {
		if err := bus.Publish(ctx, "straza.audit.tool", []byte(`{"id":"`+id+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 10*time.Second, "three parked again", func() bool {
		n, err := bus.DeadLetterCount(ctx, "es")
		return err == nil && n == 3
	})
	// still-bad-5 is first in the lane: replay 1 stops on it at once.
	refuse.Store(false)
	res, err = ReplaySink(ctx, bus, sink, 1, obs)
	if err != nil {
		t.Fatalf("ReplaySink 3: %v", err)
	}
	if res.Replayed != 0 || res.Remaining != 3 || res.Stopped == "" {
		t.Fatalf("replay 3 = %+v", res)
	}
}

// TestPipelineMissingScenario pins a receiver whose ingest pipeline is
// missing: it refuses EVERY event with 400, every event parks after the
// attempt budget (the lane never wedges, nothing is lost), the operator
// restores the pipeline, replays, and every event lands exactly once.
func TestPipelineMissingScenario(t *testing.T) {
	bus, ctx := startSpineBus(t)
	var pipelineMissing atomic.Bool
	pipelineMissing.Store(true)
	rc := &receiver{statusFor: func(string) int {
		if pipelineMissing.Load() {
			return http.StatusBadRequest
		}
		return http.StatusOK
	}}
	srv := rc.start(t)
	obs := newFakeSinkMetrics()
	sink := NewWebhookSink("elastic", srv.URL, nil, nil)

	const n = 20
	for i := 0; i < n; i++ {
		if err := bus.Publish(ctx, "straza.audit.tool", []byte(`{"id":"ev-`+itoa(i)+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewSinkRunner(bus, sink, "STRAZA_AUDIT", []string{"straza.audit.>"}, quietLog())
	runner.Policy = fastPolicy(3, 100)
	runner.Observe = obs
	runRunner(t, ctx, runner)

	waitFor(t, 20*time.Second, "all events parked", func() bool {
		c, err := bus.DeadLetterCount(ctx, "elastic")
		return err == nil && c == n
	})
	waitFor(t, 5*time.Second, "lane drained", func() bool {
		pending, inflight, err := runner.Backlog(ctx)
		return err == nil && pending == 0 && inflight == 0
	})
	if rc.hits.Load() != n*3 {
		t.Fatalf("receiver hits = %d, want %d (3 attempts each, then park)", rc.hits.Load(), n*3)
	}

	pipelineMissing.Store(false)
	res, err := ReplaySink(ctx, bus, sink, 1000, obs)
	if err != nil {
		t.Fatalf("ReplaySink: %v", err)
	}
	if res.Replayed != n || res.Remaining != 0 || res.Stopped != "" {
		t.Fatalf("replay = %+v", res)
	}
	seen := map[string]int{}
	for _, b := range rc.accepted() {
		seen[b]++
	}
	if len(seen) != n {
		t.Fatalf("receiver holds %d distinct events, want %d", len(seen), n)
	}
	for b, c := range seen {
		if c != 1 {
			t.Fatalf("event %s landed %d times", b, c)
		}
	}
	if c, _ := bus.DeadLetterCount(ctx, "elastic"); c != 0 {
		t.Fatalf("lane after replay = %d", c)
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

var _ = context.Background
