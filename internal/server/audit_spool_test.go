package server

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/strazahq/straza/internal/audit"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// spoolRecord is a decision record as the spool receives it, with marker in
// its data so a test can prove the data never reaches the log.
func spoolRecord(id, marker string) store.OutboxEvent {
	return store.OutboxEvent{Subject: "straza.audit.tool",
		CE: `{"specversion":"1.0","id":"` + id + `","type":"straza.audit.tool","source":"strazad","data":{"command":"` + marker + `"}}`}
}

// spoolTestConfig is a standalone config on a migrated SQLite file in dir.
func spoolTestConfig(t *testing.T, dir string) config.Config {
	t.Helper()
	cfg := config.Config{
		Profile: config.ProfileStandalone,
		DataDir: dir,
		Server:  config.Server{Listen: "127.0.0.1:0"},
		Log:     config.Log{Level: "error", Format: "json"},
		Store:   config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")},
		Events:  config.Events{Embedded: true},
		Governance: config.Governance{
			OfflineGraceTTL:   15 * time.Minute,
			LocalToolDefault:  config.EffectAllow,
			AuditBackpressure: config.BackpressureDrop,
		},
	}
	seedStoreTemplate(t, cfg)
	return cfg
}

// spoolTestStore opens a migrated store on driver: SQLite in a temp dir, or
// a fresh Postgres database when STRAZA_TEST_POSTGRES_DSN is set (skips
// otherwise).
func spoolTestStore(t *testing.T, driver string) store.Store {
	t.Helper()
	cfg := spoolTestConfig(t, t.TempDir())
	if driver == config.DriverPostgres {
		cfg.Store = config.Store{Driver: config.DriverPostgres, DSN: freshPostgresDSN(t)}
	}
	st, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

// outboxCount counts the outbox rows whose CloudEvent id is ceID.
func outboxCount(t *testing.T, st store.Store, ceID string) int {
	t.Helper()
	rows, err := st.Outbox().ListRecent(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if ceIDOf([]byte(r.CE)) == ceID {
			n++
		}
	}
	return n
}

// spoolErrors returns the Error records whose message names an audit loss.
func spoolErrors(buf *syncBuffer) []string {
	var out []string
	for _, l := range errorRecords(buf) {
		if strings.Contains(l, "audit record") {
			out = append(out, l)
		}
	}
	return out
}

// spoolLines sorts the spool's Error records into the three traces a loss
// leaves: a lost line, a not-confirmed line, or the stop's count.
func spoolLines(buf *syncBuffer) (lost, unsure, atStop []string) {
	for _, l := range spoolErrors(buf) {
		switch {
		case strings.Contains(l, "at shutdown"):
			atStop = append(atStop, l)
		case strings.Contains(l, "not confirmed"):
			unsure = append(unsure, l)
		default:
			lost = append(lost, l)
		}
	}
	return lost, unsure, atStop
}

// errRefused is a failure the database answered, which proves nothing was
// stored.
var errRefused = &pgconn.PgError{Severity: "FATAL", Code: "57P03", Message: "the database system is starting up"}

// TestAuditSpoolWriteRetries pins the retry of a failed outbox write: the
// record keeps one id across attempts; a write that succeeds before the
// retries run out lands once and logs nothing; one the database refuses four
// times is counted once and logged once as lost; and one whose attempt may
// have stored it is counted once and logged once as not confirmed. The log
// carries the type and the id and never the data.
func TestAuditSpoolWriteRetries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		fail         func(attempt int) error // nil means the attempt succeeds
		wantAttempts int
		wantWritten  int
		wantLost     uint64
		wantLine     string // "" means no Error line
	}{
		{"refused every time", func(int) error { return errRefused }, 4, 0, 1, "audit record lost"},
		{"refused twice then succeeds", func(n int) error {
			if n <= 2 {
				return errRefused
			}
			return nil
		}, 3, 1, 0, ""},
		{"succeeds at once", func(int) error { return nil }, 1, 1, 0, ""},
		{"first attempt unanswered, then refused", func(n int) error {
			if n == 1 {
				return context.DeadlineExceeded
			}
			return errRefused
		}, 4, 0, 1, "audit record not confirmed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log, buf := captureLogger()
			var mu sync.Mutex
			var ids []string
			written := 0
			settled := make(chan struct{})
			sp := newAuditSpool(false, log, func(_ context.Context, e store.OutboxEvent) error {
				mu.Lock()
				defer mu.Unlock()
				ids = append(ids, e.ID)
				if len(ids) == tc.wantAttempts {
					defer close(settled)
				}
				if err := tc.fail(len(ids)); err != nil {
					return err
				}
				written++
				return nil
			})
			sp.waits = []time.Duration{0, 0, 0}
			sp.drainBound = time.Second
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { sp.run(ctx); close(done) }()
			_ = sp.submit(context.Background(), spoolRecord("ce-retry", "PAYLOAD-MARKER"))
			select {
			case <-settled:
			case <-time.After(5 * time.Second):
				t.Fatal("the spool never made its last attempt")
			}
			cancel()
			<-done

			mu.Lock()
			defer mu.Unlock()
			if len(ids) != tc.wantAttempts {
				t.Fatalf("attempts = %d, want %d", len(ids), tc.wantAttempts)
			}
			for _, id := range ids {
				if id == "" || id != ids[0] {
					t.Fatalf("attempt ids = %q, want one id kept across attempts", ids)
				}
			}
			if written != tc.wantWritten {
				t.Fatalf("written = %d, want %d", written, tc.wantWritten)
			}
			if got := sp.lost.Load(); got != tc.wantLost {
				t.Fatalf("lost = %d, want %d", got, tc.wantLost)
			}
			errs := spoolErrors(buf)
			if tc.wantLine == "" {
				if len(errs) != 0 {
					t.Fatalf("Error records %q, want none", errs)
				}
			} else {
				if len(errs) != 1 {
					t.Fatalf("Error records = %d, want 1:\n%s", len(errs), buf.String())
				}
				for _, want := range []string{tc.wantLine, "type=straza.audit.tool", "id=ce-retry", "attempts=4", "SQLSTATE 57P03"} {
					if !strings.Contains(errs[0], want) {
						t.Fatalf("Error record lacks %s: %s", want, errs[0])
					}
				}
			}
			if strings.Contains(buf.String(), "PAYLOAD-MARKER") {
				t.Fatalf("the record's data reached the log:\n%s", buf.String())
			}
		})
	}
}

// TestAuditSpoolRetryAfterCommittedTimeout pins a write whose first attempt
// committed and still reported a timeout, on both drivers. When a retry
// reaches the database, the outbox key refuses the second copy and the spool
// counts the record as written. When every retry is refused, the spool says
// the record is not confirmed rather than lost, because it is stored. Either
// way the outbox and the chain built from it hold the record once, and the
// chain verifies.
func TestAuditSpoolRetryAfterCommittedTimeout(t *testing.T) {
	t.Parallel()
	for _, driver := range []string{config.DriverSQLite, config.DriverPostgres} {
		for _, tc := range []struct {
			name        string
			retriesFail bool
		}{{"retry reaches the database", false}, {"retries refused", true}} {
			t.Run(driver+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				st := spoolTestStore(t, driver)
				log, buf := captureLogger()
				calls := 0
				sp := newAuditSpool(false, log, func(c context.Context, e store.OutboxEvent) error {
					calls++
					if calls > 1 && tc.retriesFail {
						return errRefused
					}
					_, err := st.Outbox().Insert(c, e)
					if calls == 1 && err == nil {
						return context.DeadlineExceeded // the row committed, the answer was lost
					}
					return err
				})
				sp.waits = []time.Duration{0, 0, 0}

				got := sp.write(ctx, spoolRecord("ce-committed", "x"))
				lost, unsure, _ := spoolLines(buf)
				switch {
				case !tc.retriesFail && (!got || calls != 2 || sp.lost.Load() != 0 || len(spoolErrors(buf)) != 0):
					t.Fatalf("write = %v after %d attempts, lost %d, Error records %q; want written on the second attempt with no loss", got, calls, sp.lost.Load(), spoolErrors(buf))
				case tc.retriesFail && (got || len(lost) != 0 || len(unsure) != 1 || !strings.Contains(unsure[0], "id=ce-committed")):
					t.Fatalf("write = %v, lost lines %q, not-confirmed lines %q; want one not-confirmed line naming ce-committed", got, lost, unsure)
				}
				if n := outboxCount(t, st, "ce-committed"); n != 1 {
					t.Fatalf("outbox rows of the record = %d, want 1", n)
				}

				// Chain every outbox row, oldest first, as the audit consumer does.
				rows, err := st.Outbox().ListRecent(ctx, 1000)
				if err != nil {
					t.Fatal(err)
				}
				var evs []store.ChainEvent
				for i := len(rows) - 1; i >= 0; i-- {
					evs = append(evs, store.ChainEvent{CEID: ceIDOf([]byte(rows[i].CE)), CE: rows[i].CE})
				}
				if _, err := st.Audit().AppendChained(ctx, evs, audit.Genesis, audit.Link); err != nil {
					t.Fatal(err)
				}
				recs, err := st.Audit().List(ctx, 0, 1000)
				if err != nil {
					t.Fatal(err)
				}
				chain := make([]audit.Record, 0, len(recs))
				held := 0
				for _, r := range recs {
					chain = append(chain, audit.Record{Seq: r.Seq, CE: r.CE, PrevHash: r.PrevHash, Hash: r.Hash})
					if ceIDOf([]byte(r.CE)) == "ce-committed" {
						held++
					}
				}
				if ok, seq := audit.Verify(chain, audit.Genesis); !ok {
					t.Fatalf("chain broken at seq %d", seq)
				}
				if held != 1 {
					t.Fatalf("chain rows of the record = %d, want 1", held)
				}
			})
		}
	}
}

// TestAuditSpoolShutdownDrain pins the stop: run writes what the ring holds
// before it returns when the store works, and when the store fails or hangs
// it returns within its bound. Every record it could not confirm is counted
// once and leaves exactly one trace: its own lost or not-confirmed line, or
// its place in the one stop line's count.
func TestAuditSpoolShutdownDrain(t *testing.T) {
	t.Parallel()
	const held = 5
	cases := []struct {
		name        string
		insert      func(context.Context) error
		waits       time.Duration
		bound       time.Duration
		wantWritten int
		wantLost    int // own lost lines
		wantUnsure  int // own not-confirmed lines
		wantAtStop  int // the stop line's count; 0 means no stop line
	}{
		{"working store", func(context.Context) error { return nil }, 0, time.Second, held, 0, 0, 0},
		{"store refuses at once", func(context.Context) error { return errRefused }, 0, time.Second, 0, held, 0, 0},
		{"bound ends during a retry wait", func(context.Context) error { return errRefused }, time.Hour, 20 * time.Millisecond, 0, 0, 0, held},
		{"store hangs", func(c context.Context) error { <-c.Done(); return c.Err() }, 0, 20 * time.Millisecond, 0, 0, 1, held - 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log, buf := captureLogger()
			var mu sync.Mutex
			written := map[string]bool{}
			sp := newAuditSpool(false, log, func(c context.Context, e store.OutboxEvent) error {
				if err := tc.insert(c); err != nil {
					return err
				}
				mu.Lock()
				written[e.ID] = true
				mu.Unlock()
				return nil
			})
			sp.waits = []time.Duration{tc.waits, tc.waits, tc.waits}
			sp.drainBound = tc.bound
			for i := range held {
				_ = sp.submit(context.Background(), spoolRecord(fmt.Sprintf("ce-held-%d", i), "x"))
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // the last submitter has stopped
			done := make(chan struct{})
			go func() { sp.run(ctx); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("run did not return within its bound")
			}

			mu.Lock()
			defer mu.Unlock()
			if len(written) != tc.wantWritten {
				t.Fatalf("written = %d, want %d", len(written), tc.wantWritten)
			}
			if got, want := sp.lost.Load(), uint64(held-tc.wantWritten); got != want {
				t.Fatalf("lost = %d, want %d", got, want)
			}
			lost, unsure, atStop := spoolLines(buf)
			if len(lost) != tc.wantLost || len(unsure) != tc.wantUnsure {
				t.Fatalf("lost lines = %d, not-confirmed lines = %d, want %d and %d:\n%s", len(lost), len(unsure), tc.wantLost, tc.wantUnsure, buf.String())
			}
			switch {
			case tc.wantAtStop == 0 && len(atStop) != 0:
				t.Fatalf("stop lines %q, want none", atStop)
			case tc.wantAtStop > 0 && (len(atStop) != 1 || !strings.Contains(atStop[0], fmt.Sprintf("count=%d", tc.wantAtStop))):
				t.Fatalf("stop lines %q, want one with count=%d", atStop, tc.wantAtStop)
			}
		})
	}
}

// TestAuditSpoolSubmitNeverWaits pins asynchronous audit for both profiles:
// with the store hung and room in the ring, submit returns at once.
func TestAuditSpoolSubmitNeverWaits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		block bool
	}{{"standalone drops when full", false}, {"enterprise blocks when full", true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log, _ := captureLogger()
			sp := newAuditSpool(tc.block, log, func(c context.Context, _ store.OutboxEvent) error {
				<-c.Done()
				return c.Err()
			})
			sp.waits = []time.Duration{0, 0, 0}
			sp.drainBound = time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { sp.run(ctx); close(done) }()
			t.Cleanup(func() { cancel(); <-done })

			submitted := make(chan struct{})
			go func() {
				for i := range 1000 {
					_ = sp.submit(context.Background(), spoolRecord(fmt.Sprintf("ce-fast-%d", i), "x"))
				}
				close(submitted)
			}()
			select {
			case <-submitted:
			case <-time.After(5 * time.Second):
				t.Fatal("submit waited on the database")
			}
			if got := sp.dropped.Load(); got != 0 {
				t.Fatalf("dropped = %d with room in the ring, want 0", got)
			}
		})
	}
}

// spoolFaults returns two real write errors of driver: an outage, where the
// database cannot be reached, and the refusal of a row, from st where the
// driver can refuse an outbox row and from a scratch table where it cannot.
func spoolFaults(t *testing.T, driver string, st store.Store) (outage, badRow error) {
	t.Helper()
	ctx := context.Background()
	if driver == config.DriverPostgres {
		down, err := store.Open(config.Config{Store: config.Store{Driver: config.DriverPostgres,
			DSN: "postgres://straza@127.0.0.1:1/straza?connect_timeout=1"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = down.Close() })
		_, outage = down.Outbox().Insert(ctx, spoolRecord("ce-down", "x"))
		_, badRow = st.Outbox().Insert(ctx, spoolRecord("ce-nul", "a\x00b"))
		return outage, badRow
	}
	down, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "missing-dir", "x.db")+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = down.Close() })
	_, outage = down.ExecContext(ctx, `INSERT INTO t VALUES ('x')`)
	scratch, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "scratch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Close() })
	if _, err := scratch.ExecContext(ctx, `CREATE TABLE t (v TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	_, badRow = scratch.ExecContext(ctx, `INSERT INTO t VALUES (NULL)`)
	return outage, badRow
}

// TestAuditSpoolOutageHonoursBackpressure pins a database outage under each
// audit setting, on both drivers, with real driver errors. Under block the
// loop keeps trying the record it holds, so nothing is lost: a decision that
// finds the queue full waits until the database is back, and then every
// record is written once. Under drop-with-counter the same outage loses the
// record after four attempts, the control that the outage is real. A row the
// database refuses is lost after four attempts under block as well, so it
// cannot hold the queue.
func TestAuditSpoolOutageHonoursBackpressure(t *testing.T) {
	t.Parallel()
	for _, driver := range []string{config.DriverSQLite, config.DriverPostgres} {
		for _, tc := range []struct {
			name     string
			block    bool
			badRow   bool   // the fault is a refused row, not an outage
			wantLine string // the Error line the fault leaves, naming ce-A
		}{
			{"block waits out an outage", true, false, "audit queue waiting for the database"},
			{"drop loses the record in an outage", false, false, "audit record lost: every attempt"},
			{"block gives up on a refused row", true, true, "audit record lost: the database refused"},
		} {
			t.Run(driver+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				st := spoolTestStore(t, driver)
				fault, badRow := spoolFaults(t, driver, st)
				if tc.badRow {
					fault = badRow
				}
				if fault == nil {
					t.Fatal("the driver returned no error for the fault")
				}
				waits := tc.block && !tc.badRow
				var down atomic.Bool
				var attemptsA atomic.Int64
				down.Store(true)
				log, buf := captureLogger()
				sp := newAuditSpool(tc.block, log, func(c context.Context, e store.OutboxEvent) error {
					if ceIDOf([]byte(e.CE)) == "ce-A" && (tc.badRow || down.Load()) {
						attemptsA.Add(1)
						return fault
					}
					_, err := st.Outbox().Insert(c, e)
					return err
				})
				sp.ch = make(chan store.OutboxEvent, 1)
				sp.waits = []time.Duration{time.Millisecond, time.Millisecond, 5 * time.Millisecond}
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan struct{})
				go func() { sp.run(ctx); close(done) }()
				defer func() { cancel(); <-done }()

				_ = sp.submit(context.Background(), spoolRecord("ce-A", "x"))
				waitFor(t, "the first attempt on ce-A", func() bool { return attemptsA.Load() >= 1 })
				_ = sp.submit(context.Background(), spoolRecord("ce-B", "x")) // the queue of one is full
				cDone := make(chan struct{})
				go func() { _ = sp.submit(context.Background(), spoolRecord("ce-C", "x")); close(cDone) }()
				waitFor(t, "eight attempts on ce-A or its loss", func() bool { return attemptsA.Load() >= 8 || sp.lost.Load() > 0 })
				if waits {
					select {
					case <-cDone:
						t.Fatal("a decision ran while the queue was full and the database was down")
					case <-time.After(50 * time.Millisecond):
					}
				} else {
					waitFor(t, "the decision behind ce-A", func() bool {
						select {
						case <-cDone:
							return true
						default:
							return false
						}
					})
					if n := attemptsA.Load(); n != 4 {
						t.Fatalf("attempts on ce-A = %d, want 4", n)
					}
				}
				var lines []string
				for _, l := range errorRecords(buf) {
					if strings.Contains(l, tc.wantLine) && strings.Contains(l, "id=ce-A") {
						lines = append(lines, l)
					}
				}
				if len(lines) != 1 || len(errorRecords(buf)) != 1 {
					t.Fatalf("Error records:\n%s\nwant one, with %q and id=ce-A", buf.String(), tc.wantLine)
				}

				down.Store(false) // the database is back
				<-cDone
				cancel()
				<-done
				wantA, wantC := 0, 1
				if waits {
					wantA = 1
				}
				if !tc.block {
					wantC = outboxCount(t, st, "ce-C") // queued or dropped, and never waited
					if uint64(1-wantC) != sp.dropped.Load() {
						t.Fatalf("ce-C written %d times and dropped %d times, want one of them", wantC, sp.dropped.Load())
					}
				}
				for id, want := range map[string]int{"ce-A": wantA, "ce-B": 1, "ce-C": wantC} {
					if n := outboxCount(t, st, id); n != want {
						t.Fatalf("outbox rows of %s = %d, want %d", id, n, want)
					}
				}
				if got, want := sp.lost.Load(), uint64(1-wantA); got != want {
					t.Fatalf("lost = %d, want %d", got, want)
				}
				if moved := strings.Contains(buf.String(), "audit queue moving again"); moved != waits {
					t.Fatalf("the line that the queue moves again logged = %v, want %v:\n%s", moved, waits, buf.String())
				}
			})
		}
	}
}
