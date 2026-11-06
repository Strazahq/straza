package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/strazahq/straza/internal/server/metrics"
	"github.com/strazahq/straza/internal/store"
)

// flakyOutbox is an in-memory outbox whose first inserts fail. An insert
// that fails with lost set stores its row first, as a commit whose answer
// the connection lost does. A row whose id is taken answers ErrConflict, and
// a row without an id gets a fresh one, as the store's insert does.
type flakyOutbox struct {
	store.OutboxRepo
	mu       sync.Mutex
	failures int
	lost     bool
	rows     []store.OutboxEvent
	attempts int
	next     int
}

func (o *flakyOutbox) Insert(ctx context.Context, e store.OutboxEvent) (store.OutboxEvent, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.attempts++
	if err := ctx.Err(); err != nil {
		return store.OutboxEvent{}, err
	}
	if e.ID == "" {
		o.next++
		e.ID = fmt.Sprintf("row-%d", o.next)
	}
	for _, row := range o.rows {
		if row.ID == e.ID {
			return store.OutboxEvent{}, fmt.Errorf("%w: duplicate key", store.ErrConflict)
		}
	}
	if o.failures > 0 {
		o.failures--
		if o.lost {
			o.rows = append(o.rows, e)
		}
		return store.OutboxEvent{}, pgDown
	}
	o.rows = append(o.rows, e)
	return e, nil
}

type flakyOutboxStore struct {
	store.Store
	outbox *flakyOutbox
}

func (s flakyOutboxStore) Outbox() store.OutboxRepo { return s.outbox }

// TestEmitEventRetriesOnce pins that a straza.audit.* record whose outbox
// insert fails is tried once more, that an insert which committed before its
// answer was lost is not written twice, and that a record lost after the
// retry leaves exactly one Error line, with its subject and action and no
// other field of the record, and one increment of
// straza_outbox_records_lost_total. An event that is not such a record is
// tried once and counts nothing, as before.
func TestEmitEventRetriesOnce(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		subject  string
		failures int
		lost     bool
		attempts int
		rows     int
		counted  float64
	}{
		{name: "a healthy store", subject: "straza.audit.admin", attempts: 1, rows: 1},
		{name: "an insert that fails once", subject: "straza.audit.admin", failures: 1, attempts: 2, rows: 1},
		{name: "an insert that committed and lost its answer", subject: "straza.audit.identity", failures: 1, lost: true, attempts: 2, rows: 1},
		{name: "an event that is not a record", subject: "straza.apps.updated", failures: 2, attempts: 1, rows: 0},
		{name: "an insert that fails twice", subject: "straza.audit.authn", failures: 2, attempts: 2, rows: 0, counted: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log, buf := captureLogger()
			outbox := &flakyOutbox{failures: tc.failures, lost: tc.lost}
			app := &App{store: flakyOutboxStore{outbox: outbox}, log: log,
				metrics: metrics.New(func() float64 { return 0 }, func() float64 { return 0 })}
			app.emitEventCtx(context.Background(), tc.subject, map[string]any{"action": "user.delete", "username": "the-record-field"})
			if outbox.attempts != tc.attempts || len(outbox.rows) != tc.rows {
				t.Errorf("%d attempts and %d rows, want %d and %d", outbox.attempts, len(outbox.rows), tc.attempts, tc.rows)
			}
			if got := counterValue(t, app, "straza_outbox_records_lost_total"); got != tc.counted {
				t.Errorf("straza_outbox_records_lost_total = %v, want %v", got, tc.counted)
			}
			lines := errorRecords(buf)
			if tc.counted == 0 {
				if len(lines) != 0 {
					t.Errorf("Error lines %q, want none", lines)
				}
				return
			}
			if len(lines) != 1 {
				t.Fatalf("Error lines %q, want one", lines)
			}
			for _, want := range []string{"subject=" + tc.subject, "action=user.delete"} {
				if !strings.Contains(lines[0], want) {
					t.Errorf("the Error line %q does not name %s", lines[0], want)
				}
			}
			if strings.Contains(lines[0], "the-record-field") {
				t.Errorf("the Error line %q carries a field of the record", lines[0])
			}
		})
	}
}

// TestRevokeRecordRetriesOnce pins that a user revoke's own records, the
// straza.revocation.user event and the user.killed record, get the same one
// retry as every other record, and that one lost after the retry is
// answered as an error for the caller's Error line and counted in
// straza_outbox_records_lost_total.
func TestRevokeRecordRetriesOnce(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		failures int
		lost     bool
		rows     int
		counted  float64
	}{
		{name: "an insert that fails once", failures: 1, rows: 1},
		{name: "an insert that committed and lost its answer", failures: 1, lost: true, rows: 1},
		{name: "an insert that fails twice", failures: 2, rows: 0, counted: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log, _ := captureLogger()
			outbox := &flakyOutbox{failures: tc.failures, lost: tc.lost}
			app := &App{store: flakyOutboxStore{outbox: outbox}, log: log,
				metrics: metrics.New(func() float64 { return 0 }, func() float64 { return 0 })}
			err := app.emitRevokeRecord(context.Background(), "straza.audit.identity", map[string]any{"action": "user.killed"})
			if (err != nil) != (tc.counted > 0) || outbox.attempts != 2 || len(outbox.rows) != tc.rows {
				t.Errorf("err %v, %d attempts and %d rows, want an error %v, 2 attempts and %d rows",
					err, outbox.attempts, len(outbox.rows), tc.counted > 0, tc.rows)
			}
			if got := counterValue(t, app, "straza_outbox_records_lost_total"); got != tc.counted {
				t.Errorf("straza_outbox_records_lost_total = %v, want %v", got, tc.counted)
			}
		})
	}
}

// TestApproverRefusalRecordLossIsCounted pins that the login failure an
// approver token's refused request writes, when the outbox refuses it twice,
// is not counted in straza_outbox_records_lost_total, because the phone's
// next refused request writes it, and that the approval service's lost
// records reach the counter.
func TestApproverRefusalRecordLossIsCounted(t *testing.T) {
	t.Parallel()
	log, _ := captureLogger()
	app, base, fs := testAppFaultLog(t, log)
	kim := seedIdentity(t, app)
	_, _, tok := enrollApprover(t, app, kim.ID)
	app.denylist.revokeUser(kim.ID)
	fs.arm("outbox:straza.audit.authn", pgDown)
	if code := adminReq(t, "GET", base+"/v1/approver/pending?scope=decidable", tok, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("poll = %d, want 401", code)
	}
	fs.disarm()
	if got := counterValue(t, app, "straza_outbox_records_lost_total"); got != 0 {
		t.Errorf("straza_outbox_records_lost_total after the lost refusal record = %v, want 0", got)
	}
	if app.approval.OnRecordLost == nil {
		t.Fatal("the approval service has no lost-record callback")
	}
	app.approval.OnRecordLost()
	if got := counterValue(t, app, "straza_outbox_records_lost_total"); got != 1 {
		t.Errorf("straza_outbox_records_lost_total after the approval service lost a record = %v, want 1", got)
	}
}
