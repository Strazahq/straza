package approval

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

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
		return store.OutboxEvent{}, fmt.Errorf("the database refused the insert")
	}
	o.rows = append(o.rows, e)
	return e, nil
}

type flakyOutboxStore struct {
	store.Store
	outbox *flakyOutbox
}

func (s flakyOutboxStore) Outbox() store.OutboxRepo { return s.outbox }

// TestApprovalRecordsRetryOnce pins that an approval phase record and a
// Slack tap's login failure are written on a context the caller's end does
// not cancel, tried once more when the insert fails, never written twice
// after an insert that committed but lost its answer, and, when lost after
// the retry, logged once at Error and passed to OnRecordLost once.
func TestApprovalRecordsRetryOnce(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		failures int
		lost     bool
		ended    bool
		attempts int
		rows     int
		lostOnes int
	}{
		{name: "a healthy store", attempts: 1, rows: 1},
		{name: "a caller whose context ended", ended: true, attempts: 1, rows: 1},
		{name: "an insert that fails once", failures: 1, attempts: 2, rows: 1},
		{name: "an insert that committed and lost its answer", failures: 1, lost: true, attempts: 2, rows: 1},
		{name: "an insert that fails twice", failures: 2, attempts: 2, rows: 0, lostOnes: 1},
	}
	writers := []struct {
		name  string
		write func(ctx context.Context, svc *Service)
	}{
		{"an approval phase", func(ctx context.Context, svc *Service) {
			svc.auditPhase(ctx, "request", Record{ID: "a-1", UserID: "u-1", State: StatePending})
		}},
		{"a Slack tap's refusal", func(ctx context.Context, svc *Service) {
			sc := &slackChannel{st: svc.st, svc: svc, log: svc.log}
			sc.auditRefusal(ctx, store.User{ID: "u-1", Username: "kim"}, "a-1", "user is disabled")
		}},
	}
	for _, w := range writers {
		for _, tc := range cases {
			t.Run(w.name+": "+tc.name, func(t *testing.T) {
				var logs bytes.Buffer
				outbox := &flakyOutbox{failures: tc.failures, lost: tc.lost}
				lost := 0
				svc := &Service{st: flakyOutboxStore{outbox: outbox}, log: slog.New(slog.NewTextHandler(&logs, nil)),
					now: time.Now, OnRecordLost: func() { lost++ }}
				ctx, cancel := context.WithCancel(context.Background())
				if tc.ended {
					cancel()
				}
				defer cancel()
				w.write(ctx, svc)
				if outbox.attempts != tc.attempts || len(outbox.rows) != tc.rows || lost != tc.lostOnes {
					t.Errorf("%d attempts, %d rows and %d lost, want %d, %d and %d",
						outbox.attempts, len(outbox.rows), lost, tc.attempts, tc.rows, tc.lostOnes)
				}
				if errors := strings.Count(logs.String(), "level=ERROR"); errors != tc.lostOnes {
					t.Errorf("%d Error lines, want %d:\n%s", errors, tc.lostOnes, logs.String())
				}
			})
		}
	}
}
