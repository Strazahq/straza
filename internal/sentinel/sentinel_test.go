package sentinel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/events"
)

// TestSentinelEndToEnd runs the whole sentinel loop against a real JetStream:
// synthetic audit CEs published to STRAZA_AUDIT reach the durable sentinel
// consumer, the deny-burst detector fires, and the verdict lands back on the
// stream as a straza.audit.sentinel CloudEvent with the pinned data contract,
// where the audit-chain consumer (straza.audit.>) would hash-chain it.
func TestSentinelEndToEnd(t *testing.T) {
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

	cfg := testCfg()
	s := New(bus, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go func() { _ = s.Run(ctx) }()
	// DeliverNew: the durable must exist before the events are published.
	time.Sleep(300 * time.Millisecond)

	now := time.Now().UTC()
	for i := 0; i < cfg.DenyBurstWarn; i++ {
		ce, _ := json.Marshal(map[string]any{
			"specversion": "1.0",
			"id":          fmt.Sprintf("deny-%d", i),
			"type":        "straza.audit.tool",
			"source":      "strazad",
			"time":        now.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			"data": map[string]any{
				"session": "sess-e2e", "user": "eve", "event": "tool.pre",
				// Dissimilar commands: similar ones would (correctly) trip
				// denyThenVariant too; this test isolates the burst lane.
				"tool": "shell.exec", "command": fmt.Sprintf("probe%d target%d flag%d", i, i, i),
				"effect": "deny", "ruleId": "guard", "reason": "blocked",
			},
		})
		if err := bus.Publish(ctx, "straza.audit.tool", ce); err != nil {
			t.Fatalf("publish deny %d: %v", i, err)
		}
	}

	// Read the verdict back off the SAME stream (DeliverAll durable, so
	// ordering vs. the sentinel's publish does not matter).
	cons, err := bus.SinkConsumer(ctx, "STRAZA_AUDIT", "test-verdicts", []string{SubjectVerdict})
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	deadline := time.Now().Add(10 * time.Second)
	for raw == nil && time.Now().Before(deadline) {
		batch, err := cons.Fetch(1)
		if err != nil {
			t.Fatal(err)
		}
		for msg := range batch.Messages() {
			raw = msg.Data()
			_ = msg.Ack()
		}
	}
	if raw == nil {
		t.Fatal("no straza.audit.sentinel verdict arrived on the stream")
	}

	var ce struct {
		Specversion string `json:"specversion"`
		ID          string `json:"id"`
		Type        string `json:"type"`
		Source      string `json:"source"`
		Data        struct {
			Session  string   `json:"session"`
			User     string   `json:"user"`
			Detector string   `json:"detector"`
			Severity string   `json:"severity"`
			Reason   string   `json:"reason"`
			Evidence []string `json:"evidence"`
			Window   string   `json:"window"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &ce); err != nil {
		t.Fatalf("verdict CE unmarshal: %v (%s)", err, raw)
	}
	if ce.Specversion != "1.0" || ce.Type != SubjectVerdict || ce.Source != "strazad-sentinel" || ce.ID == "" {
		t.Errorf("CE envelope wrong: %+v", ce)
	}
	if ce.Data.Session != "sess-e2e" || ce.Data.User != "eve" {
		t.Errorf("verdict subject wrong: session=%q user=%q", ce.Data.Session, ce.Data.User)
	}
	if ce.Data.Detector != DetectorDenyBurst || ce.Data.Severity != SeverityWarn {
		t.Errorf("verdict = %s/%s, want denyBurst/warn", ce.Data.Detector, ce.Data.Severity)
	}
	if len(ce.Data.Evidence) != cfg.DenyBurstWarn || ce.Data.Evidence[0] != "deny-0" {
		t.Errorf("evidence = %v, want the %d deny CE ids", ce.Data.Evidence, cfg.DenyBurstWarn)
	}
	if ce.Data.Window != "1m0s" || ce.Data.Reason == "" {
		t.Errorf("window=%q reason=%q", ce.Data.Window, ce.Data.Reason)
	}
}
