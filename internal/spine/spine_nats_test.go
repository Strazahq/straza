package spine

import (
	"context"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/events"
)

// startSpineBus boots a throwaway JetStream server and a Bus over it (the
// same shape TestSinkRunnerBatched uses), for runner tests that need real
// redelivery semantics rather than a mocked consumer.
func startSpineBus(t *testing.T) (*events.Bus, context.Context) {
	t.Helper()
	ns, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1,
		JetStream: true, StoreDir: t.TempDir(),
		NoLog: true, NoSigs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ns.Start()
	t.Cleanup(ns.Shutdown)
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats not ready")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bus, err := events.Start(ctx, config.Config{Events: config.Events{URL: ns.ClientURL()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bus.Close)
	return bus, ctx
}

// fakeSinkMetrics records the observer calls the runner makes.
type fakeSinkMetrics struct {
	mu       sync.Mutex
	parked   map[string]int
	dups     map[string]int
	replayed map[string]int
}

func newFakeSinkMetrics() *fakeSinkMetrics {
	return &fakeSinkMetrics{parked: map[string]int{}, dups: map[string]int{}, replayed: map[string]int{}}
}

func (f *fakeSinkMetrics) SinkParked(sink string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.parked[sink]++
}

func (f *fakeSinkMetrics) SinkDuplicate(sink string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dups[sink]++
}

func (f *fakeSinkMetrics) SinkReplayed(sink string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replayed[sink]++
}

func (f *fakeSinkMetrics) counts(sink string) (parked, dups, replayed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.parked[sink], f.dups[sink], f.replayed[sink]
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// fastPolicy is a test retry policy with millisecond delays.
func fastPolicy(det, retry int) RetryPolicy {
	p := DefaultRetryPolicy()
	p.BaseDelay = 5 * time.Millisecond
	p.MaxDelay = 20 * time.Millisecond
	p.DeterministicAttempts = det
	p.RetryableAttempts = retry
	return p
}
