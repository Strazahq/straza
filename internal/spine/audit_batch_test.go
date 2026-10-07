package spine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"

	"github.com/strazahq/straza/internal/audit"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/events"
)

// TestAuditConsumerBatched is the batched end-to-end test: a burst of audit events,
// including duplicate CE ids, flows stream → batched consumer → hash chain,
// and the chain comes out complete, deduped, and linear.
func TestAuditConsumerBatched(t *testing.T) {
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

	st := newRelayStore(t)

	// 150 events (> 2 batches) with every third id duplicated.
	const unique = 150
	published := 0
	for i := 0; i < unique; i++ {
		ce := fmt.Sprintf(`{"id":"ce-%03d","type":"straza.audit.tool"}`, i)
		if err := bus.Publish(ctx, "straza.audit.tool", []byte(ce)); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
		published++
		if i%3 == 0 {
			if err := bus.Publish(ctx, "straza.audit.tool", []byte(ce)); err != nil {
				t.Fatalf("re-publish %d: %v", i, err)
			}
			published++
		}
	}

	cons := NewAuditConsumer(st, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- cons.Run(runCtx) }()

	deadline := time.Now().Add(20 * time.Second)
	for {
		recs, err := st.Audit().List(ctx, 0, unique+50)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(recs) == unique {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("chain rows = %d after %d published, want %d (deduped)", len(recs), published, unique)
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Full linearity walk.
	recs, err := st.Audit().List(ctx, 0, unique+50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != unique {
		t.Fatalf("chain rows = %d, want %d", len(recs), unique)
	}
	prev := audit.Genesis
	for i, rec := range recs {
		if rec.PrevHash != prev || rec.Hash != audit.Link(prev, rec.CE) {
			t.Fatalf("chain break at row %d (seq %d)", i, rec.Seq)
		}
		prev = rec.Hash
	}
}
