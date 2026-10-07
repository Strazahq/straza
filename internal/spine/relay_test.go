package spine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// fakeBus fails publishes on one subject prefix ("the audit stream's disk
// is full") and records the rest in order.
type fakeBus struct {
	mu         sync.Mutex
	published  []string
	failPrefix string
}

func (f *fakeBus) Publish(_ context.Context, subject string, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPrefix != "" && strings.HasPrefix(subject, f.failPrefix) {
		return errors.New("insufficient resources")
	}
	f.published = append(f.published, subject)
	return nil
}

func (f *fakeBus) subjects() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.published...)
}

func newRelayStore(t *testing.T) store.Store {
	t.Helper()
	cfg := config.Config{Store: config.Store{
		Driver: config.DriverSQLite,
		DSN:    filepath.Join(t.TempDir(), "relay.db"),
	}}
	s, err := store.Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

// TestRelayControlBeforeBulk pins the two-pass drain: with the audit stream
// refusing every publish, a revocation queued BEHIND bulk capture rows
// still publishes on the same drain pass and is marked, while the audit
// rows wait for the stream to heal. A strict-FIFO outbox would head-of-line
// block the kill switch here.
func TestRelayControlBeforeBulk(t *testing.T) {
	st := newRelayStore(t)
	ctx := context.Background()

	seed := []string{
		"straza.audit.tool",
		"straza.audit.prompt",
		"straza.revocation.session",
		"straza.audit.reply",
		"straza.policy.updated",
	}
	for _, subj := range seed {
		if _, err := st.Outbox().Insert(ctx, store.OutboxEvent{Subject: subj, CE: `{"id":"` + subj + `"}`}); err != nil {
			t.Fatalf("insert %s: %v", subj, err)
		}
	}

	bus := &fakeBus{failPrefix: "straza.audit."}
	r := NewRelay(st, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.drain(ctx)

	got := bus.subjects()
	want := []string{"straza.revocation.session", "straza.policy.updated"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("published %v, want control-first %v", got, want)
	}
	control, err := st.Outbox().ListUnpublishedControl(ctx, 10)
	if err != nil {
		t.Fatalf("ListUnpublishedControl: %v", err)
	}
	if len(control) != 0 {
		t.Fatalf("control rows still unpublished: %d", len(control))
	}
	bulk, err := st.Outbox().ListUnpublished(ctx, 10)
	if err != nil {
		t.Fatalf("ListUnpublished: %v", err)
	}
	if len(bulk) != 3 {
		t.Fatalf("audit rows queued = %d, want 3 (kept for the healed stream)", len(bulk))
	}

	// Stream heals: the bulk backlog drains in its original order.
	bus.failPrefix = ""
	r.drain(ctx)
	got = bus.subjects()
	wantAll := append(want, "straza.audit.tool", "straza.audit.prompt", "straza.audit.reply")
	if len(got) != len(wantAll) {
		t.Fatalf("published %v, want %v", got, wantAll)
	}
	for i := range wantAll {
		if got[i] != wantAll[i] {
			t.Fatalf("publish order[%d] = %s, want %s (full: %v)", i, got[i], wantAll[i], got)
		}
	}
	left, err := st.Outbox().ListUnpublished(ctx, 10)
	if err != nil {
		t.Fatalf("ListUnpublished after heal: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("rows still unpublished after heal: %d", len(left))
	}
}
