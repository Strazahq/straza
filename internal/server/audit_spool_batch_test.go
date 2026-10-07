package server

import (
	"context"
	"fmt"
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

// errBadRow is the database's refusal of one row, which fails the batch
// that holds the row and every later write of the row.
var errBadRow = &pgconn.PgError{Severity: "ERROR", Code: "22021", Message: "invalid byte sequence for encoding"}

// drainSpool queues recs and runs sp with its context already ended, so run
// goes straight to its drain and takes what is queued in order.
func drainSpool(t *testing.T, sp *auditSpool, recs ...store.OutboxEvent) {
	t.Helper()
	for _, r := range recs {
		_ = sp.submit(context.Background(), r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { sp.run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("run did not return within its bound")
	}
}

// spoolRecords returns n decision records with the ids ce-0 to ce-(n-1).
func spoolRecords(n int) []store.OutboxEvent {
	recs := make([]store.OutboxEvent, n)
	for i := range recs {
		recs[i] = spoolRecord(fmt.Sprintf("ce-%d", i), "PAYLOAD-MARKER")
	}
	return recs
}

// TestAuditSpoolWritesQueuedRecordsInBatches pins how the loop cuts what the
// ring holds: a record that is alone is written alone, more are written in
// one transaction of at most auditBatchMax records, every record gets its
// own outbox id, and the records keep the order they were submitted in.
func TestAuditSpoolWritesQueuedRecordsInBatches(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		queued      int
		wantBatches []int
		wantSingles int
	}{
		{1, nil, 1},
		{2, []int{2}, 0},
		{auditBatchMax, []int{auditBatchMax}, 0},
		{auditBatchMax + 1, []int{auditBatchMax}, 1},
		{2*auditBatchMax + 50, []int{auditBatchMax, auditBatchMax, 50}, 0},
	} {
		t.Run(fmt.Sprintf("%d queued", tc.queued), func(t *testing.T) {
			t.Parallel()
			log, buf := captureLogger()
			var batches []int
			var stored []string
			keys := map[string]bool{}
			singles := 0
			sp := newAuditSpool(false, log, func(_ context.Context, e store.OutboxEvent) error {
				singles++
				stored, keys[e.ID] = append(stored, ceIDOf([]byte(e.CE))), true
				return nil
			})
			sp.insertBatch = func(_ context.Context, es []store.OutboxEvent) error {
				batches = append(batches, len(es))
				for _, e := range es {
					stored, keys[e.ID] = append(stored, ceIDOf([]byte(e.CE))), true
				}
				return nil
			}
			drainSpool(t, sp, spoolRecords(tc.queued)...)

			if fmt.Sprint(batches) != fmt.Sprint(tc.wantBatches) || singles != tc.wantSingles {
				t.Fatalf("batches = %v and %d single writes, want %v and %d", batches, singles, tc.wantBatches, tc.wantSingles)
			}
			if len(stored) != tc.queued {
				t.Fatalf("stored %d records, want %d", len(stored), tc.queued)
			}
			for i, id := range stored {
				if want := fmt.Sprintf("ce-%d", i); id != want {
					t.Fatalf("record %d stored is %s, want %s: the records lost their order", i, id, want)
				}
			}
			if len(keys) != tc.queued || keys[""] {
				t.Fatalf("%d distinct outbox ids for %d records (empty id: %v), want one each", len(keys), tc.queued, keys[""])
			}
			if got := sp.lost.Load(); got != 0 || len(spoolErrors(buf)) != 0 {
				t.Fatalf("lost = %d, Error records %q, want none", got, spoolErrors(buf))
			}
		})
	}
}

// TestAuditSpoolBatchFallback pins what follows a batch the database did not
// confirm. The batch is not tried again: each record is written alone under
// the id the batch gave it and starts at once, so a batch that was stored
// after all is not stored twice, a refused row loses only itself, and a
// record whose batch got no answer is reported as not confirmed, not lost.
func TestAuditSpoolBatchFallback(t *testing.T) {
	t.Parallel()
	const held = 5
	always := func(err error) func(string) error { return func(string) error { return err } }
	cases := []struct {
		name        string
		batchErr    error
		batchStores bool               // the batch commits and still returns batchErr
		single      func(string) error // the answer to a single write of a CloudEvent id
		waits       time.Duration
		wantStored  int
		wantLost    []string // "audit record lost" lines must name these ids
		wantUnsure  []string // "not confirmed" lines must name these ids
		wantLine    string   // part of every lost line
		wantTries   map[string]int
	}{
		{name: "one refused row", batchErr: errBadRow, single: func(id string) error {
			if id == "ce-2" {
				return errBadRow
			}
			return nil
		}, wantStored: held - 1, wantLost: []string{"ce-2"}, wantLine: "the database refused the record itself",
			wantTries: map[string]int{"ce-0": 1, "ce-1": 1, "ce-2": 4, "ce-3": 1, "ce-4": 1}},
		{name: "the batch was stored and its answer was lost", batchErr: context.DeadlineExceeded, batchStores: true,
			single: always(nil), wantStored: held,
			wantTries: map[string]int{"ce-0": 1, "ce-1": 1, "ce-2": 1, "ce-3": 1, "ce-4": 1}},
		{name: "no answer to the batch, then every write refused", batchErr: context.DeadlineExceeded,
			single: always(errRefused), wantUnsure: []string{"ce-0", "ce-1", "ce-2", "ce-3", "ce-4"},
			wantTries: map[string]int{"ce-0": 4, "ce-1": 4, "ce-2": 4, "ce-3": 4, "ce-4": 4}},
		{name: "the batch refused with an answer, then every write refused", batchErr: errRefused,
			single: always(errRefused), wantLost: []string{"ce-0", "ce-1", "ce-2", "ce-3", "ce-4"}, wantLine: "every attempt",
			wantTries: map[string]int{"ce-0": 4, "ce-1": 4, "ce-2": 4, "ce-3": 4, "ce-4": 4}},
		// With waits of an hour the test ends only when no single write
		// waits before its first attempt.
		{name: "one failed batch, then the database works", batchErr: errRefused, single: always(nil),
			waits: time.Hour, wantStored: held,
			wantTries: map[string]int{"ce-0": 1, "ce-1": 1, "ce-2": 1, "ce-3": 1, "ce-4": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log, buf := captureLogger()
			stored := map[string]int{}      // outbox id to the times it was stored
			batchKey := map[string]string{} // CloudEvent id to the outbox id the batch gave it
			tries := map[string]int{}
			batches := 0
			sp := newAuditSpool(false, log, func(_ context.Context, e store.OutboxEvent) error {
				id := ceIDOf([]byte(e.CE))
				tries[id]++
				if e.ID == "" || e.ID != batchKey[id] {
					t.Errorf("single write of %s under outbox id %q, want the batch's id %q", id, e.ID, batchKey[id])
				}
				if stored[e.ID] > 0 {
					return store.ErrConflict
				}
				if err := tc.single(id); err != nil {
					return err
				}
				stored[e.ID]++
				return nil
			})
			sp.insertBatch = func(_ context.Context, es []store.OutboxEvent) error {
				batches++
				for _, e := range es {
					batchKey[ceIDOf([]byte(e.CE))] = e.ID
					if tc.batchStores {
						stored[e.ID]++
					}
				}
				return tc.batchErr
			}
			sp.waits = []time.Duration{tc.waits, tc.waits, tc.waits}
			drainSpool(t, sp, spoolRecords(held)...)

			if batches != 1 {
				t.Fatalf("batch writes = %d, want 1: a failed batch is not tried again as a batch", batches)
			}
			if fmt.Sprint(tries) != fmt.Sprint(tc.wantTries) {
				t.Fatalf("single attempts = %v, want %v", tries, tc.wantTries)
			}
			if len(stored) != tc.wantStored {
				t.Fatalf("stored %d records, want %d", len(stored), tc.wantStored)
			}
			for key, n := range stored {
				if n != 1 {
					t.Fatalf("outbox id %s stored %d times, want once", key, n)
				}
			}
			if got, want := sp.lost.Load(), uint64(held-tc.wantStored); got != want {
				t.Fatalf("lost = %d, want %d", got, want)
			}
			lost, unsure, atStop := spoolLines(buf)
			if len(lost) != len(tc.wantLost) || len(unsure) != len(tc.wantUnsure) || len(atStop) != 0 {
				t.Fatalf("lost lines = %d, not-confirmed lines = %d, stop lines = %d, want %d, %d and 0:\n%s",
					len(lost), len(unsure), len(atStop), len(tc.wantLost), len(tc.wantUnsure), buf.String())
			}
			for i, id := range tc.wantLost {
				if !strings.Contains(lost[i], "id="+id+" ") || !strings.Contains(lost[i], tc.wantLine) || !strings.Contains(lost[i], "attempts=4") {
					t.Fatalf("lost line %d lacks id=%s, %q or attempts=4: %s", i, id, tc.wantLine, lost[i])
				}
			}
			for i, id := range tc.wantUnsure {
				if !strings.Contains(unsure[i], "id="+id+" ") {
					t.Fatalf("not-confirmed line %d lacks id=%s: %s", i, id, unsure[i])
				}
			}
			if strings.Contains(buf.String(), "PAYLOAD-MARKER") {
				t.Fatalf("a record's data reached the log:\n%s", buf.String())
			}
		})
	}
}

// TestAuditSpoolBatchShutdownDrain pins the stop while the loop writes
// batches. Every record run could not confirm is counted once and leaves
// one trace. The records of a batch whose write the stop cut off may be
// stored, so each gets its own not-confirmed line, and what the ring still
// holds then goes to the stop's count and is not received.
func TestAuditSpoolBatchShutdownDrain(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		held        int
		insert      func(context.Context) error
		waits       time.Duration
		bound       time.Duration
		wantWritten int
		wantLost    int // own lost lines
		wantUnsure  int // own not-confirmed lines
		wantAtStop  int // the stop line's count; 0 means no stop line
		wantInRing  int // records the stop left in the ring, unreceived
	}{
		{"working store", 5, func(context.Context) error { return nil }, 0, time.Second, 5, 0, 0, 0, 0},
		{"store refuses at once", 5, func(context.Context) error { return errRefused }, 0, time.Second, 0, 5, 0, 0, 0},
		{"bound ends during a retry wait", 5, func(context.Context) error { return errRefused }, time.Hour, 20 * time.Millisecond, 0, 0, 0, 5, 0},
		{"store hangs", 5, func(c context.Context) error { <-c.Done(); return c.Err() }, 0, 20 * time.Millisecond, 0, 0, 5, 0, 0},
		{"store hangs with more queued than one batch", auditBatchMax + 50, func(c context.Context) error { <-c.Done(); return c.Err() },
			0, 20 * time.Millisecond, 0, 0, auditBatchMax, 50, 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log, buf := captureLogger()
			written := map[string]bool{}
			sp := newAuditSpool(false, log, func(c context.Context, e store.OutboxEvent) error {
				if err := tc.insert(c); err != nil {
					return err
				}
				written[e.ID] = true
				return nil
			})
			sp.insertBatch = func(c context.Context, es []store.OutboxEvent) error {
				if err := tc.insert(c); err != nil {
					return err
				}
				for _, e := range es {
					written[e.ID] = true
				}
				return nil
			}
			sp.waits = []time.Duration{tc.waits, tc.waits, tc.waits}
			sp.drainBound = tc.bound
			drainSpool(t, sp, spoolRecords(tc.held)...)

			if len(written) != tc.wantWritten {
				t.Fatalf("written = %d, want %d", len(written), tc.wantWritten)
			}
			if got, want := sp.lost.Load(), uint64(tc.held-tc.wantWritten); got != want {
				t.Fatalf("lost = %d, want %d", got, want)
			}
			lost, unsure, atStop := spoolLines(buf)
			if len(lost) != tc.wantLost || len(unsure) != tc.wantUnsure {
				t.Fatalf("lost lines = %d, not-confirmed lines = %d, want %d and %d", len(lost), len(unsure), tc.wantLost, tc.wantUnsure)
			}
			switch {
			case tc.wantAtStop == 0 && len(atStop) != 0:
				t.Fatalf("stop lines %q, want none", atStop)
			case tc.wantAtStop > 0 && (len(atStop) != 1 || !strings.Contains(atStop[0], fmt.Sprintf("count=%d", tc.wantAtStop))):
				t.Fatalf("stop lines %q, want one with count=%d", atStop, tc.wantAtStop)
			}
			if got := len(sp.ch); got != tc.wantInRing {
				t.Fatalf("the ring holds %d records after the stop, want %d left unreceived", got, tc.wantInRing)
			}
		})
	}
}

// TestAuditSpoolBatchStopKeepsWaitersBlocked pins the stop under block while
// a batch is in the loop, the ring is full and the database never answers.
// When the drain's bound ends, run stops receiving, so a decision still
// waiting for room in submit never gets it and never runs. The three
// records of the batch get a not-confirmed line each, and the three in the
// ring go to the stop's count.
func TestAuditSpoolBatchStopKeepsWaitersBlocked(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	var batches atomic.Int64
	hang := func(c context.Context) error { <-c.Done(); return c.Err() }
	sp := newAuditSpool(true, log, func(c context.Context, _ store.OutboxEvent) error { return hang(c) })
	sp.insertBatch = func(c context.Context, _ []store.OutboxEvent) error { batches.Add(1); return hang(c) }
	sp.ch = make(chan store.OutboxEvent, 3)
	sp.drainBound = 50 * time.Millisecond
	recs := spoolRecords(8)
	for _, r := range recs[:3] {
		_ = sp.submit(context.Background(), r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sp.run(ctx); close(done) }()
	waitFor(t, "the loop to hold the first three records as one batch", func() bool { return batches.Load() == 1 && len(sp.ch) == 0 })
	for _, r := range recs[3:6] {
		_ = sp.submit(context.Background(), r)
	}
	var entered, ran atomic.Int64
	var waiters sync.WaitGroup
	for _, r := range recs[6:] {
		waiters.Add(1)
		go func() {
			defer waiters.Done()
			entered.Add(1)
			if sp.submit(context.Background(), r) == nil {
				ran.Add(1) // the decision would run here
			}
		}()
	}
	waitFor(t, "two decisions to wait for room", func() bool { return entered.Load() == 2 })
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return within its bound")
	}
	time.Sleep(100 * time.Millisecond)

	if got := ran.Load(); got != 0 {
		t.Fatalf("%d waiting decisions ran after the stop, without their records", got)
	}
	if got := len(sp.ch); got != 3 {
		t.Fatalf("the ring holds %d records after the stop, want 3 left unreceived", got)
	}
	if got := sp.lost.Load(); got != 6 {
		t.Fatalf("lost = %d, want 6: the three in the batch and the three in the ring", got)
	}
	lost, unsure, atStop := spoolLines(buf)
	if len(lost) != 0 || len(unsure) != 3 || len(atStop) != 1 || !strings.Contains(atStop[0], "count=3") {
		t.Fatalf("lost lines %q, not-confirmed lines %q, stop lines %q; want three not-confirmed lines and one stop line with count=3", lost, unsure, atStop)
	}
	for range 5 { // free the two waiters so the test leaves no goroutine behind
		<-sp.ch
	}
	waiters.Wait()
}

// outboxStamps returns the created_at of each outbox row by CloudEvent id
// and fails the test when an id is stored more than once.
func outboxStamps(t *testing.T, st store.Store) map[string]time.Time {
	t.Helper()
	rows, err := st.Outbox().ListRecent(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	stamps := map[string]time.Time{}
	for _, r := range rows {
		id := ceIDOf([]byte(r.CE))
		if _, twice := stamps[id]; twice {
			t.Fatalf("outbox holds %s more than once", id)
		}
		stamps[id] = r.CreatedAt
	}
	return stamps
}

// requireStampOrder requires that the records ids name are stored once each
// and that their created_at rises in the order of ids, which is the order
// the relay publishes them in.
func requireStampOrder(t *testing.T, st store.Store, ids ...string) {
	t.Helper()
	stamps := outboxStamps(t, st)
	for i, id := range ids {
		at, ok := stamps[id]
		if !ok {
			t.Fatalf("outbox lacks %s", id)
		}
		if i > 0 && !at.After(stamps[ids[i-1]]) {
			t.Fatalf("%s is stamped %s, not after %s at %s: the records lost their order",
				id, at.Format(time.RFC3339Nano), ids[i-1], stamps[ids[i-1]].Format(time.RFC3339Nano))
		}
	}
}

// realSpool is a spool that writes to st the way the server wires it.
func realSpool(block bool, st store.Store) (*auditSpool, *syncBuffer) {
	log, buf := captureLogger()
	sp := newAuditSpool(block, log, func(c context.Context, e store.OutboxEvent) error {
		_, err := st.Outbox().Insert(c, e)
		return err
	})
	sp.insertBatch = func(c context.Context, es []store.OutboxEvent) error {
		_, err := st.Outbox().InsertBatch(c, es)
		return err
	}
	return sp, buf
}

// TestAuditSpoolBatchOnStores pins the batch write against both databases:
// a batch, a lone record and a second batch are stamped in the order they
// were submitted; a batch that committed and still reported a timeout is
// in the outbox and the chain once and the chain verifies; and on Postgres,
// which refuses a NUL byte, a real refused row loses only itself.
func TestAuditSpoolBatchOnStores(t *testing.T) {
	t.Parallel()
	for _, driver := range []string{config.DriverSQLite, config.DriverPostgres} {
		t.Run(driver+"/order across a batch, a lone record and a batch", func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			st := spoolTestStore(t, driver)
			sp, buf := realSpool(false, st)
			recs := spoolRecords(7)
			// The ring holds what follows the record in hand: two records
			// make a batch of three, and none leaves the record alone.
			for _, cut := range [][]store.OutboxEvent{recs[0:3], recs[3:4], recs[4:7]} {
				for _, r := range cut[1:] {
					_ = sp.submit(ctx, r)
				}
				sp.writeQueued(ctx, cut[0])
			}
			requireStampOrder(t, st, "ce-0", "ce-1", "ce-2", "ce-3", "ce-4", "ce-5", "ce-6")
			if got := sp.lost.Load(); got != 0 || len(spoolErrors(buf)) != 0 {
				t.Fatalf("lost = %d, Error records %q, want none", got, spoolErrors(buf))
			}
		})

		t.Run(driver+"/a batch that committed and reported a timeout", func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			st := spoolTestStore(t, driver)
			sp, buf := realSpool(false, st)
			stores := sp.insertBatch
			sp.insertBatch = func(c context.Context, es []store.OutboxEvent) error {
				if err := stores(c, es); err != nil {
					return err
				}
				return context.DeadlineExceeded // the rows committed, the answer was lost
			}
			sp.waits = []time.Duration{0, 0, 0}
			drainSpool(t, sp, spoolRecords(5)...)

			if got := sp.lost.Load(); got != 0 || len(spoolErrors(buf)) != 0 {
				t.Fatalf("lost = %d, Error records %q, want every record counted as written", got, spoolErrors(buf))
			}
			requireStampOrder(t, st, "ce-0", "ce-1", "ce-2", "ce-3", "ce-4")

			// Chain every outbox row, oldest first, as the audit consumer does.
			rows, err := st.Outbox().ListRecent(ctx, 1000)
			if err != nil {
				t.Fatal(err)
			}
			var evs []store.ChainEvent
			for i := len(rows) - 1; i >= 0; i-- {
				evs = append(evs, store.ChainEvent{CEID: ceIDOf([]byte(rows[i].CE)), CE: rows[i].CE})
			}
			if n, err := st.Audit().AppendChained(ctx, evs, audit.Genesis, audit.Link); err != nil || n != 5 {
				t.Fatalf("chained %d records (%v), want 5", n, err)
			}
			recs, err := st.Audit().List(ctx, 0, 1000)
			if err != nil {
				t.Fatal(err)
			}
			chain := make([]audit.Record, 0, len(recs))
			for _, r := range recs {
				chain = append(chain, audit.Record{Seq: r.Seq, CE: r.CE, PrevHash: r.PrevHash, Hash: r.Hash})
			}
			if ok, seq := audit.Verify(chain, audit.Genesis); !ok || len(chain) != 5 {
				t.Fatalf("chain of %d records, broken at seq %d (verified %v); want 5 that verify", len(chain), seq, ok)
			}
		})
	}

	t.Run("postgres/a refused row loses only itself", func(t *testing.T) {
		t.Parallel()
		st := spoolTestStore(t, config.DriverPostgres)
		sp, buf := realSpool(true, st)
		sp.waits = []time.Duration{0, 0, 0}
		drainSpool(t, sp, spoolRecord("ce-0", "x"), spoolRecord("ce-nul", "a\x00b"), spoolRecord("ce-2", "x"))

		requireStampOrder(t, st, "ce-0", "ce-2")
		if n := len(outboxStamps(t, st)); n != 2 {
			t.Fatalf("outbox rows = %d, want 2: the refused record is not stored", n)
		}
		// The NUL byte also makes the record unreadable as JSON, so its
		// line carries the database's answer and no id.
		lost, unsure, atStop := spoolLines(buf)
		if sp.lost.Load() != 1 || len(lost) != 1 || len(unsure) != 0 || len(atStop) != 0 ||
			!strings.Contains(lost[0], "the database refused the record itself") || !strings.Contains(lost[0], "SQLSTATE 22021") {
			t.Fatalf("lost = %d, lost lines %q, not-confirmed lines %q, stop lines %q; want one refused line with SQLSTATE 22021",
				sp.lost.Load(), lost, unsure, atStop)
		}
	})
}

// TestAuditSpoolBatchOutageHonoursBackpressure pins a database fault that
// meets a batch, under each audit setting, on both drivers, with real
// driver errors. ce-A, ce-B and ce-C are one batch. Under block an outage
// holds ce-A, so the two behind it wait in the loop, a decision that finds
// the ring full waits, and once the database is back every record is
// written once and in order. Under drop-with-counter the outage loses ce-A
// after four attempts. A refused row is lost after four attempts under
// block as well, so it cannot hold the queue. The other records of the
// batch are written in every case.
func TestAuditSpoolBatchOutageHonoursBackpressure(t *testing.T) {
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
				var attemptsA, failedBatches atomic.Int64
				down.Store(true)
				faulty := func(e store.OutboxEvent) bool {
					return ceIDOf([]byte(e.CE)) == "ce-A" && (tc.badRow || down.Load())
				}
				sp, buf := realSpool(tc.block, st)
				writes, writesBatch := sp.insert, sp.insertBatch
				sp.insert = func(c context.Context, e store.OutboxEvent) error {
					if faulty(e) {
						attemptsA.Add(1)
						return fault
					}
					return writes(c, e)
				}
				sp.insertBatch = func(c context.Context, es []store.OutboxEvent) error {
					for _, e := range es {
						if faulty(e) {
							failedBatches.Add(1)
							return fault
						}
					}
					return writesBatch(c, es)
				}
				sp.ch = make(chan store.OutboxEvent, 3)
				sp.waits = []time.Duration{time.Millisecond, time.Millisecond, 5 * time.Millisecond}
				for _, id := range []string{"ce-A", "ce-B", "ce-C"} {
					_ = sp.submit(context.Background(), spoolRecord(id, "x"))
				}
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan struct{})
				go func() { sp.run(ctx); close(done) }()
				defer func() { cancel(); <-done }()

				waitFor(t, "the batch to fail and ce-A to be tried alone", func() bool { return attemptsA.Load() >= 1 })
				for _, id := range []string{"ce-D", "ce-E", "ce-F"} {
					_ = sp.submit(context.Background(), spoolRecord(id, "x")) // the ring of three is full under an outage
				}
				gDone := make(chan struct{})
				go func() { _ = sp.submit(context.Background(), spoolRecord("ce-G", "x")); close(gDone) }()
				waitFor(t, "eight attempts on ce-A or its loss", func() bool { return attemptsA.Load() >= 8 || sp.lost.Load() > 0 })
				if waits {
					select {
					case <-gDone:
						t.Fatal("a decision ran while the ring was full and the database was down")
					case <-time.After(50 * time.Millisecond):
					}
				} else {
					<-gDone
					if n := attemptsA.Load(); n != 4 {
						t.Fatalf("attempts on ce-A = %d, want 4", n)
					}
				}
				if n := failedBatches.Load(); n != 1 {
					t.Fatalf("failed batch writes = %d, want 1: a failed batch is not tried again as a batch", n)
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
				<-gDone
				waitFor(t, "the ring to be written", func() bool { return len(sp.ch) == 0 })
				cancel()
				<-done
				want := []string{"ce-B", "ce-C", "ce-D", "ce-E", "ce-F"}
				if waits {
					want = append([]string{"ce-A"}, want...)
				} else if n := outboxCount(t, st, "ce-A"); n != 0 {
					t.Fatalf("outbox rows of ce-A = %d, want 0", n)
				}
				wantG := 1
				if !tc.block {
					wantG = outboxCount(t, st, "ce-G") // queued or dropped, and never waited
					if uint64(1-wantG) != sp.dropped.Load() {
						t.Fatalf("ce-G written %d times and dropped %d times, want one of them", wantG, sp.dropped.Load())
					}
				}
				if wantG == 1 {
					want = append(want, "ce-G")
				}
				requireStampOrder(t, st, want...)
				wantLost := uint64(1)
				if waits {
					wantLost = 0
				}
				if got := sp.lost.Load(); got != wantLost {
					t.Fatalf("lost = %d, want %d", got, wantLost)
				}
				if moved := strings.Contains(buf.String(), "audit queue moving again"); moved != waits {
					t.Fatalf("the line that the queue moves again logged = %v, want %v:\n%s", moved, waits, buf.String())
				}
			})
		}
	}
}

// TestBuildWiresTheAuditBatchInsert pins that the server gives its spool the
// store's batch insert: records queued together reach the outbox through
// one batch write.
func TestBuildWiresTheAuditBatchInsert(t *testing.T) {
	t.Parallel()
	app, cfg, _, buf, ctx, cancel := spoolApp(t)
	built := app.audit.insertBatch
	if built == nil {
		t.Fatal("build left the audit spool without a batch insert, so every record is written alone")
	}
	var batched atomic.Int64
	app.audit.insertBatch = func(c context.Context, es []store.OutboxEvent) error {
		batched.Add(int64(len(es)))
		return built(c, es)
	}
	const held = 5
	ids := make([]string, 0, held)
	for i := range held {
		ids = append(ids, fmt.Sprintf("ce-wired-%d", i))
		_ = app.audit.submit(context.Background(), spoolRecord(ids[i], "x"))
	}
	done := runUntilStopped(t, app, ctx)
	waitFor(t, "the spool to write the queued records", func() bool { return batched.Load() == held })
	cancel()
	waitStopped(t, done)
	if errs := spoolErrors(buf); len(errs) != 0 {
		t.Fatalf("Error records %q, want none", errs)
	}
	requireOutbox(t, cfg, ids...)
}
