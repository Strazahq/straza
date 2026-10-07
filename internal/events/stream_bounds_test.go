package events

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/strazahq/straza/internal/config"
)

// TestAuditStreamDefaultBounds pins that a default deployment's STRAZA_AUDIT
// stream is bounded (2x the effective capture retention, 2 GiB) with
// DiscardOld, never the unbounded stream whose full disk stops revocation
// publication.
func TestAuditStreamDefaultBounds(t *testing.T) {
	b := startTestBus(t)
	ctx := context.Background()

	s, err := b.js.Stream(ctx, "STRAZA_AUDIT")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	info, err := s.Info(ctx)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if want := 2 * 30 * 24 * time.Hour; info.Config.MaxAge != want {
		t.Errorf("MaxAge = %v, want %v (2x capture-retention default)", info.Config.MaxAge, want)
	}
	if want := int64(defaultAuditStreamMaxBytes); info.Config.MaxBytes != want {
		t.Errorf("MaxBytes = %d, want %d", info.Config.MaxBytes, want)
	}
	if info.Config.Discard != jetstream.DiscardOld {
		t.Errorf("Discard = %v, want DiscardOld", info.Config.Discard)
	}

	// The control stream keeps its semantics: revocation replay rebuilds
	// denylists from history, so STRAZA_EVENTS stays unbounded.
	ev, err := b.js.Stream(ctx, "STRAZA_EVENTS")
	if err != nil {
		t.Fatalf("Stream events: %v", err)
	}
	evInfo, err := ev.Info(ctx)
	if err != nil {
		t.Fatalf("Info events: %v", err)
	}
	if evInfo.Config.MaxBytes > 0 || evInfo.Config.MaxAge > 0 {
		t.Errorf("STRAZA_EVENTS gained bounds (MaxAge %v MaxBytes %d); phase 0 bounds audit only",
			evInfo.Config.MaxAge, evInfo.Config.MaxBytes)
	}
}

// TestAuditStreamCapKeepsPublishing pins that with the byte cap reached, the
// stream sheds its oldest messages and every publish still succeeds: a full
// audit stream cannot stop the relay, and with it revocation delivery.
func TestAuditStreamCapKeepsPublishing(t *testing.T) {
	cfg := config.Config{
		DataDir: t.TempDir(),
		Events: config.Events{
			Embedded:            true,
			AuditStreamMaxAge:   time.Hour,
			AuditStreamMaxBytes: 1 << 20, // 1 MiB
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	b, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(b.Close)

	// 3 MiB of capture-sized payloads into a 1 MiB stream.
	payload := bytes.Repeat([]byte("x"), 64<<10)
	for i := 0; i < 48; i++ {
		if err := b.Publish(ctx, "straza.audit.prompt", payload); err != nil {
			t.Fatalf("publish %d failed (a capped stream must shed, not refuse): %v", i, err)
		}
	}

	s, err := b.js.Stream(ctx, "STRAZA_AUDIT")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	info, err := s.Info(ctx)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	// Enforcement granularity is block-level; allow one payload of slack.
	if max := uint64(1<<20 + 66<<10); info.State.Bytes > max {
		t.Errorf("stream bytes = %d, want <= %d (cap enforced)", info.State.Bytes, max)
	}
	if info.State.Msgs >= 48 {
		t.Errorf("stream holds all %d messages; nothing was shed", info.State.Msgs)
	}
}

// TestAuditStreamExplicitKnobs pins that operator-set values win over the
// derived defaults.
func TestAuditStreamExplicitKnobs(t *testing.T) {
	cfg := config.Config{
		DataDir: t.TempDir(),
		Events: config.Events{
			Embedded:            true,
			AuditStreamMaxAge:   48 * time.Hour,
			AuditStreamMaxBytes: 5 << 20,
		},
		Governance: config.Governance{CaptureRetention: 2 * time.Hour},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	b, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(b.Close)

	s, err := b.js.Stream(ctx, "STRAZA_AUDIT")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	info, err := s.Info(ctx)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Config.MaxAge != 48*time.Hour {
		t.Errorf("MaxAge = %v, want 48h (explicit wins over derived)", info.Config.MaxAge)
	}
	if info.Config.MaxBytes != 5<<20 {
		t.Errorf("MaxBytes = %d, want %d", info.Config.MaxBytes, 5<<20)
	}
}
