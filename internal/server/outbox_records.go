package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/store"
)

// emitEvent inserts an event into the transactional outbox, which the
// relay publishes to NATS.
func (a *App) emitEvent(r *http.Request, subject string, data map[string]any) {
	a.emitEventCtx(r.Context(), subject, data)
}

// emitEventCtx is the context-only variant, for callers without a request
// (snapshot service, background workers). The CE source carries the pod's
// unique instance id so consumers can tell their own events from other
// pods' (the convergence consumer skips self-events; in-process handlers
// already applied the change). The chained records of straza.audit.admin,
// straza.audit.authn and straza.audit.identity and the straza.identity.*
// announcements are written even when ctx is cancelled, because the change
// they record has already happened, and each insert waits at most
// auditInsertTimeout for the store. Such a record whose insert fails is
// tried once more, and one still not written is logged at Error with its
// subject and action, never its fields, and counted in
// straza_outbox_records_lost_total. An event on any other subject is tried
// once, on the caller's context.
func (a *App) emitEventCtx(ctx context.Context, subject string, data map[string]any) {
	record := false
	switch {
	case subject == "straza.audit.admin", subject == "straza.audit.authn", subject == "straza.audit.identity",
		strings.HasPrefix(subject, "straza.identity."):
		// A client that disconnects after the store write must not take
		// the record with it, and a store that stops answering must not
		// hold the handler for ever. The actor is a context value, which
		// survives.
		record = true
		var cancel context.CancelFunc
		ctx, cancel = afterCommit(ctx)
		defer cancel()
	}
	// Admin mutations are attributed to the authenticated principal
	// requireAdmin stored in the context: cloudEvent is the one seam that
	// stamps it, for every emit site and for the records of a publish.
	ev, err := a.cloudEvent(ctx, subject, data)
	if !record {
		if err == nil {
			_, err = a.store.Outbox().Insert(ctx, ev)
		}
		if err != nil {
			a.log.Warn("outbox insert failed", "subject", subject, "err", err)
		}
		return
	}
	if err == nil {
		err = a.insertRecord(ctx, ev)
	}
	if err == nil {
		return
	}
	a.recordLost()
	action, _ := data["action"].(string)
	a.reqlog(ctx).Error("outbox: a record could not be written after one retry, so the change it records stands without it on the audit chain",
		"subject", subject, "action", action, "err", err)
}

// recordLost counts one record that could not be written to the outbox
// even after its retry in straza_outbox_records_lost_total.
func (a *App) recordLost() {
	if a.metrics != nil {
		a.metrics.OutboxRecordLost()
	}
}

// insertRecord writes the record ev to the outbox and, when that fails,
// tries once more on a fresh afterCommit context, so a store that failed
// one write or ran out the first try's time gets a second chance. The row
// id is fixed before the first try, so a retry after an insert that
// committed but lost its answer meets that row, answers ErrConflict and
// writes no second one.
func (a *App) insertRecord(ctx context.Context, ev store.OutboxEvent) error {
	ev.ID = uuid.Must(uuid.NewV7()).String()
	_, err := a.store.Outbox().Insert(ctx, ev)
	if err == nil {
		return nil
	}
	rctx, cancel := afterCommit(ctx)
	defer cancel()
	_, retryErr := a.store.Outbox().Insert(rctx, ev)
	if retryErr == nil || errors.Is(retryErr, store.ErrConflict) {
		return nil
	}
	return errors.Join(err, retryErr)
}

// afterCommit answers the context one write that follows a committed change
// runs on: ctx's values without its cancel or its deadline, so a client that
// left or a write that ran past its bound cannot cut it, and at most
// auditInsertTimeout for the store.
func afterCommit(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), auditInsertTimeout)
}
