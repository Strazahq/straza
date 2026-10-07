package server

import (
	"context"
	"errors"
	"log/slog"

	"github.com/strazahq/straza/internal/approval"
)

// failClosed writes the ONE Error record an internal failure owes when the
// PEP answers it as a fail-closed deny (hook lane) or a tool-level error
// (gateway lane): the approval service, the fingerprint canonicalizer or the
// classifier failed, the agent sees the usual "Denied (fail-closed)" words,
// and without this record the server log would be silent about it. The
// record rides the per-request logger when ctx carries one
// (correlation_id bound by requestID; every resolver receives r.Context() or
// a context derived from it) and a.log otherwise, and bumps
// straza_failclosed_total{lane}.
//
// An *approval.UnroutableError is NOT a failure: it is the revision-14
// "no roles, no usable sponsor" deny, actionable from its reason and counted
// by straza_approvals_unroutable_total. No record, no count.
func (a *App) failClosed(ctx context.Context, lane, rule string, err error) {
	var un *approval.UnroutableError
	if errors.As(err, &un) {
		return
	}
	l := a.reqlog(ctx)
	if l == nil {
		l = slog.Default()
	}
	attrs := []any{"lane", lane}
	if rule != "" {
		attrs = append(attrs, "rule", rule)
	}
	if err != nil {
		attrs = append(attrs, "cause", err)
	}
	l.Error("fail-closed: internal failure answered as a deny", attrs...)
	if a.metrics != nil {
		a.metrics.FailClosed(lane)
	}
}
