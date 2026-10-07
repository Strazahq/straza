package server

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestEmitOnAGoneClient pins what emitEventCtx writes when the caller's
// context is cancelled before the call, as it is when a client disconnects
// after the store write it records. Every event that records a change that
// already happened still lands in the outbox: the chained records of
// straza.audit.admin, with the actor the context holds, straza.audit.authn
// and straza.audit.identity, and the straza.identity.* announcements. An
// event on any other subject keeps riding the caller's context.
func TestEmitOnAGoneClient(t *testing.T) {
	t.Parallel()
	cases := []struct {
		subject string
		lands   bool
	}{
		{subject: "straza.audit.admin", lands: true},
		{subject: "straza.audit.authn", lands: true},
		{subject: "straza.audit.identity", lands: true},
		{subject: "straza.identity.created", lands: true},
		{subject: "straza.identity.updated", lands: true},
		{subject: "straza.identity.deactivated", lands: true},
		{subject: "straza.apps.updated", lands: false},
	}
	for _, tc := range cases {
		t.Run(tc.subject, func(t *testing.T) {
			t.Parallel()
			app := bootApp(bootStore(t), slog.New(slog.DiscardHandler))
			ctx, cancel := context.WithCancel(withActor(context.Background(), auditActor{Name: "kim", ID: "kim-id", Via: "login"}))
			cancel()
			app.emitEventCtx(ctx, tc.subject, map[string]any{"action": "user.update", "target": "ada-id", "id": "ada-id"})
			got := outboxDataFor(t, app, tc.subject)
			if landed := len(got) == 1; landed != tc.lands {
				t.Fatalf("%s on a cancelled context: %d events in the outbox, want landed %v", tc.subject, len(got), tc.lands)
			}
			if tc.subject == "straza.audit.admin" && (got[0]["actor"] != "kim" || got[0]["actorId"] != "kim-id" || got[0]["actorVia"] != "login") {
				t.Errorf("the record lost its actor: %v", got[0])
			}
		})
	}
}

// hungStore is a store whose outbox insert never answers: it returns only
// when its context ends, or after a long safety wait that the test reports.
type hungStore struct{ store.Store }

func (s hungStore) Outbox() store.OutboxRepo { return hungOutbox{s.Store.Outbox()} }

type hungOutbox struct{ store.OutboxRepo }

func (hungOutbox) Insert(ctx context.Context, _ store.OutboxEvent) (store.OutboxEvent, error) {
	select {
	case <-ctx.Done():
		return store.OutboxEvent{}, ctx.Err()
	case <-time.After(3 * auditInsertTimeout):
		return store.OutboxEvent{}, context.DeadlineExceeded
	}
}

// TestEmitOnAHungStore pins that a straza.audit.admin emit whose client has
// gone still gives up on a store that stops answering: it returns within
// auditInsertTimeout, the wait the decision path's audit spool uses, and
// logs the failed insert at Warn.
func TestEmitOnAHungStore(t *testing.T) {
	t.Parallel()
	logger, logs := captureLogger()
	app := bootApp(hungStore{bootStore(t)}, logger)
	ctx, cancel := context.WithCancel(withActor(context.Background(), auditActor{Name: "kim", ID: "kim-id", Via: "login"}))
	cancel()
	start := time.Now()
	app.emitEventCtx(ctx, "straza.audit.admin", map[string]any{"action": "roles.assign", "target": "as-id"})
	took := time.Since(start)
	if took < auditInsertTimeout-100*time.Millisecond || took > auditInsertTimeout+2*time.Second {
		t.Errorf("the emit returned after %v, want about %v", took, auditInsertTimeout)
	}
	line := ""
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "outbox insert failed") {
			line = l
		}
	}
	if !strings.Contains(line, "level=WARN") || !strings.Contains(line, "subject=straza.audit.admin") || !strings.Contains(line, "deadline exceeded") {
		t.Errorf("want one Warn line for the failed insert, got:\n%s", logs.String())
	}
}
