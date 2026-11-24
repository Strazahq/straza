package server

import (
	"context"
	"encoding/json"
	"net/http"
)

// actionAdminRefused is the action of the straza.audit.admin record of an
// admin change the admin plane refused with 403.
const actionAdminRefused = "admin.refused"

// refusalWatch is the writer an authenticated admin change runs on. It
// keeps the first status answered and, when that status is 403, the body,
// so the wrapper that installed it can record the refusal once.
type refusalWatch struct {
	http.ResponseWriter
	status int
	body   []byte
	// recorded is set when the handler wrote its own record of the refusal,
	// which the watch then does not repeat.
	recorded bool
}

// WriteHeader keeps the first status and always forwards.
func (w *refusalWatch) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write implies 200 when no status was written, as net/http does, and keeps
// the body of a 403.
func (w *refusalWatch) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status == http.StatusForbidden {
		w.body = append(w.body, p...)
	}
	return w.ResponseWriter.Write(p)
}

type refusalWatchKey struct{}

// watched reports whether the refusal watch logs and records a 403 answered
// to r: every method but GET and HEAD. A wrapper's own WARN line of a
// refusal is for the requests it does not watch, so a refused write logs
// one line.
func watched(r *http.Request) bool {
	return r.Method != http.MethodGet && r.Method != http.MethodHead
}

// watchRefusals runs right after an admin wrapper authenticated the caller
// act. For a watched request it answers a writer and a request that watch
// the answer, and a report the wrapper defers, which records a 403 with
// recordRefusal. A read passes through and its report does nothing. A late
// write that runBounded holds back reaches the watch as the bound's 503 and
// is not recorded.
func (a *App) watchRefusals(w http.ResponseWriter, r *http.Request, act auditActor) (http.ResponseWriter, *http.Request, func()) {
	if !watched(r) {
		return w, r, func() {}
	}
	rw := &refusalWatch{ResponseWriter: w}
	r = r.WithContext(context.WithValue(r.Context(), refusalWatchKey{}, rw))
	return rw, r, func() { a.recordRefusal(r, act, rw) }
}

// refusalRecorded tells the watch of the request behind ctx that the handler
// wrote its own record of the refusal it answers, so the refusal keeps that
// one record.
func refusalRecorded(ctx context.Context) {
	if rw, ok := ctx.Value(refusalWatchKey{}).(*refusalWatch); ok {
		rw.recorded = true
	}
}

// recordRefusal writes one WARN line and one admin.refused record under act
// for a change the watch saw answered 403 and no handler recorded: the
// route pattern, the status, the error sentence the caller was answered as
// the reason, and the id the route names as target. It never writes the
// request body or a credential.
func (a *App) recordRefusal(r *http.Request, act auditActor, rw *refusalWatch) {
	if rw.status != http.StatusForbidden || rw.recorded {
		return
	}
	var answer struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rw.body, &answer)
	ctx := withActor(r.Context(), act)
	route := routeLabel(r)
	data := map[string]any{"action": actionAdminRefused, "route": route, "httpStatus": http.StatusForbidden, "reason": answer.Error}
	attrs := []any{"status", http.StatusForbidden, "route", route, "actor", act.Name}
	if id := r.PathValue("id"); id != "" {
		data["target"] = id
		attrs = append(attrs, "target", id)
	}
	a.reqlog(ctx).Warn("admin change refused", append(attrs, "reason", answer.Error)...)
	a.emitIdentityAudit(ctx, data)
}
