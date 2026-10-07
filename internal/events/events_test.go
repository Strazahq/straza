package events

import (
	"context"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
)

func startTestBus(t *testing.T) *Bus {
	t.Helper()
	cfg := config.Config{
		DataDir: t.TempDir(),
		Events:  config.Events{Embedded: true},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	b, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(b.Close)
	return b
}

func TestEmbeddedBusPublishAndPing(t *testing.T) {
	b := startTestBus(t)
	ctx := context.Background()

	if err := b.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if err := b.Publish(ctx, "straza.identity.created", []byte(`{"id":"u1"}`)); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// The message must land in the STRAZA_EVENTS stream.
	s, err := b.js.Stream(ctx, "STRAZA_EVENTS")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	info, err := s.Info(ctx)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.State.Msgs != 1 {
		t.Errorf("STRAZA_EVENTS msgs = %d, want 1", info.State.Msgs)
	}
}

func TestSubscribeCoreRoundTrip(t *testing.T) {
	b := startTestBus(t)

	got := make(chan []byte, 1)
	unsub, err := b.SubscribeCore("straza.approval.resolved.abc", func(_ string, data []byte) {
		got <- data
	})
	if err != nil {
		t.Fatalf("SubscribeCore: %v", err)
	}

	if err := b.PublishCore("straza.approval.resolved.abc", []byte(`{"state":"approved"}`)); err != nil {
		t.Fatalf("PublishCore: %v", err)
	}
	select {
	case data := <-got:
		if string(data) != `{"state":"approved"}` {
			t.Errorf("payload = %s", data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("core subscription did not receive the broadcast")
	}

	// After unsubscribe, further broadcasts must not reach the handler.
	unsub()
	if err := b.PublishCore("straza.approval.resolved.abc", []byte(`{"state":"denied"}`)); err != nil {
		t.Fatalf("PublishCore: %v", err)
	}
	select {
	case data := <-got:
		t.Errorf("handler fired after unsubscribe: %s", data)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestStreamsCoverTaxonomy(t *testing.T) {
	b := startTestBus(t)
	ctx := context.Background()

	subjects := map[string]string{
		"straza.audit.tool":           "STRAZA_AUDIT",
		"straza.audit.mcp":            "STRAZA_AUDIT",
		"straza.policy.updated":       "STRAZA_EVENTS",
		"straza.revocation.user":      "STRAZA_EVENTS",
		"straza.apps.deployed":        "STRAZA_EVENTS",
		"straza.identity.deactivated": "STRAZA_EVENTS",
	}
	for subject, stream := range subjects {
		if err := b.Publish(ctx, subject, []byte(`{}`)); err != nil {
			t.Errorf("Publish %s (expected stream %s): %v", subject, stream, err)
		}
	}
}

func TestPublishUncoveredSubjectFails(t *testing.T) {
	b := startTestBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// No stream owns this subject, so a JetStream publish must not ack.
	if err := b.Publish(ctx, "not.straza.subject", []byte(`{}`)); err == nil {
		t.Error("publish to uncovered subject should fail")
	}
}

func TestJetStreamPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir, Events: config.Events{Embedded: true}}
	ctx := context.Background()

	b, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := b.Publish(ctx, "straza.audit.tool", []byte(`{"n":1}`)); err != nil {
		b.Close()
		t.Fatalf("Publish: %v", err)
	}
	b.Close()

	b2, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	defer b2.Close()
	s, err := b2.js.Stream(ctx, "STRAZA_AUDIT")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	info, err := s.Info(ctx)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.State.Msgs != 1 {
		t.Errorf("audit stream lost messages across restart: msgs = %d, want 1", info.State.Msgs)
	}
}
