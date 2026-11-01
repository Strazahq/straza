package spine

import (
	"bytes"
	"context"
	"database/sql"
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

func (f *fakeBus) MaxPayload() int64 { return 1 << 20 }

func (f *fakeBus) subjects() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.published...)
}

func newRelayStore(t *testing.T) store.Store {
	t.Helper()
	st, _ := newRelayStoreAt(t)
	return st
}

// newRelayStoreAt is newRelayStore that also answers the database file, so
// a test can write a row the way an operator's SQL would, past the store.
func newRelayStoreAt(t *testing.T) (store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relay.db")
	cfg := config.Config{Store: config.Store{
		Driver: config.DriverSQLite,
		DSN:    path,
	}}
	s, err := store.Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s, path
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
	r := NewRelay(st, bus, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
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

// seedRaw writes outbox rows straight into the database file at path, in
// the given order, as an operator's SQL would: the store's own writes bound
// every record, so only this way can a row be over the bus's payload.
func seedRaw(t *testing.T, path string, rows ...store.OutboxEvent) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for i, e := range rows {
		if _, err := db.Exec(`INSERT INTO events_outbox (id, subject, ce, published, attempts, created_at) VALUES (?, ?, ?, 0, 0, ?)`,
			e.ID, e.Subject, e.CE, "2026-10-31T10:00:0"+string(rune('1'+i))+"Z"); err != nil {
			t.Fatalf("seed %s: %v", e.ID, err)
		}
	}
}

// relayRows answers the outbox rows by id.
func relayRows(t *testing.T, st store.Store) map[string]store.OutboxEvent {
	t.Helper()
	recent, err := st.Outbox().ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.OutboxEvent{}
	for _, e := range recent {
		out[e.ID] = e
	}
	return out
}

// setAsideLines counts the Error lines the relay wrote for a row set aside.
func setAsideLines(log *bytes.Buffer) int {
	n := 0
	for _, line := range strings.Split(log.String(), "\n") {
		if strings.Contains(line, "level=ERROR") && strings.Contains(line, "outbox row set aside") {
			n++
		}
	}
	return n
}

// TestRelaySetsAsideRowOverMaxPayload pins that a row over the bus's
// maximum payload cannot stop the chain: the real bus refuses it, the relay
// sets it aside with one count and one Error line, the rows on both sides of
// it publish, and no later pass tries it again. A bus outage, by contrast,
// sets nothing aside and keeps every row for the next pass.
func TestRelaySetsAsideRowOverMaxPayload(t *testing.T) {
	big := `{"id":"big","data":{"command":"` + strings.Repeat("x", 1<<20) + `"}}`
	rows := []store.OutboxEvent{
		{ID: "before", Subject: "straza.audit.tool", CE: `{"id":"before"}`},
		{ID: "big", Subject: "straza.audit.tool", CE: big},
		{ID: "after", Subject: "straza.audit.tool", CE: `{"id":"after"}`},
	}
	t.Run("over the payload", func(t *testing.T) {
		bus, ctx := startSpineBus(t)
		st, path := newRelayStoreAt(t)
		seedRaw(t, path, rows...)
		var log bytes.Buffer
		count := 0
		r := NewRelay(st, bus, slog.New(slog.NewTextHandler(&log, nil)), func() { count++ })
		r.drain(ctx)
		r.drain(ctx)
		got := relayRows(t, st)
		if !got["before"].Published || !got["after"].Published {
			t.Fatalf("the rows around the big one did not publish: before %v, after %v", got["before"].Published, got["after"].Published)
		}
		if b := got["big"]; b.Published || b.Attempts != -1 {
			t.Fatalf("big row published=%v attempts=%d, want unpublished and set aside (-1)", b.Published, b.Attempts)
		}
		if count != 1 || setAsideLines(&log) != 1 {
			t.Fatalf("set aside counted %d times with %d Error lines, want 1 and 1:\n%s", count, setAsideLines(&log), log.String())
		}
		if line := log.String(); !strings.Contains(line, "id=big") || !strings.Contains(line, "subject=straza.audit.tool") ||
			!strings.Contains(line, "bytes=") || !strings.Contains(line, "max_payload=1048576") {
			t.Fatalf("Error line does not name the row's id, subject and size and the bus's max_payload:\n%s", line)
		}
	})
	t.Run("bus outage", func(t *testing.T) {
		st, path := newRelayStoreAt(t)
		seedRaw(t, path, rows...)
		var log bytes.Buffer
		count := 0
		r := NewRelay(st, &fakeBus{failPrefix: "straza."}, slog.New(slog.NewTextHandler(&log, nil)), func() { count++ })
		r.drain(context.Background())
		for id, e := range relayRows(t, st) {
			if e.Published || e.Attempts != 0 {
				t.Fatalf("row %s published=%v attempts=%d during an outage, want it kept for the next pass", id, e.Published, e.Attempts)
			}
		}
		if count != 0 || setAsideLines(&log) != 0 {
			t.Fatalf("an outage set %d rows aside with %d Error lines, want none", count, setAsideLines(&log))
		}
	})
}
