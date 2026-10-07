package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// adminWriteBound is the longest an admin write runs once its handler
// starts. It is a backstop against a store that never answers, not the
// operator's timeout: a client that stops waiting sooner, as strazactl does
// at 30 seconds, tells its operator that strazad took the request and may
// still be running it, and a write that legitimately runs long, such as a
// sink replay, still finishes whole. Var, not const: tests shrink it.
var adminWriteBound = 5 * time.Minute

// adminWriteTimedOut is the sentence an admin write answers when it ran
// past adminWriteBound, formatted with the bound and the route.
const adminWriteTimedOut = "this change did not finish within %s, because the database or a service it waits on did not answer in time, " +
	"so it may or may not have been applied. Read the change back before you try again, " +
	"and if this keeps happening, look at strazad's log around this time for %s."

// lateBodyMax caps how much of a held-back refusal's body its Error record
// keeps.
const lateBodyMax = 256

// serveAdmin runs an authorized admin handler under runBounded's rule. An
// answer that a write started after the bound gives way to the bound's
// 503, which keeps one Error record: the handler's own when it answered a
// 5xx through fail, else one that names the handler's status as the cause.
func (a *App) serveAdmin(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	held, r := runBounded(w, r, next)
	if held == nil {
		return
	}
	msg := fmt.Sprintf(adminWriteTimedOut, humanDuration(adminWriteBound), routeLabel(r))
	if held.status >= http.StatusInternalServerError {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": msg, correlationKey: reqID(r.Context())})
		return
	}
	a.fail(w, r, http.StatusServiceUnavailable, msg, held.heldBack())
}

// runBounded runs next under the rule of an admin write and answers the
// request next saw. A read, GET or HEAD, runs on the request's context, so
// a client that leaves stops it. Any other method is a write and runs on a
// context that the client's leaving does not cancel, bounded by
// adminWriteBound, because a write whose row committed must finish and
// chain its record. An answer that a write starts after the bound, success
// or not, is held back, because the steps after the bound ran on an expired
// context. runBounded then answers its writer, and nil otherwise, so the
// caller answers the bound's 503 in its own shape.
func runBounded(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) (*lateErrorWriter, *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		next(w, r)
		return nil, r
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), adminWriteBound)
	defer cancel()
	r = r.WithContext(ctx)
	lw := &lateErrorWriter{ResponseWriter: w, ctx: ctx}
	next(lw, r)
	if !lw.late {
		return nil, r
	}
	return lw, r
}

// lateErrorWriter holds back the answer a write handler starts once its
// context has ended, the status and the start of the body, so the caller
// of runBounded answers the bound's 503 instead. A status written before
// the context ended passes, and so does the body that follows it.
type lateErrorWriter struct {
	http.ResponseWriter
	ctx     context.Context
	started bool
	late    bool
	status  int
	body    []byte
	// err is why the context had ended when the answer was held back.
	err error
}

// WriteHeader forwards a status written while the context lives and holds
// back the first one written after it ended.
func (w *lateErrorWriter) WriteHeader(code int) {
	switch {
	case w.late:
	case !w.started && w.ctx.Err() != nil:
		w.late, w.status, w.err = true, code, w.ctx.Err()
	default:
		w.started = true
		w.ResponseWriter.WriteHeader(code)
	}
}

// Write implies 200 when no status was written, as net/http does, and
// keeps the start of a held-back body instead of sending it.
func (w *lateErrorWriter) Write(p []byte) (int, error) {
	if !w.started && !w.late {
		w.WriteHeader(http.StatusOK)
	}
	if !w.late {
		return w.ResponseWriter.Write(p)
	}
	if room := lateBodyMax - len(w.body); room > 0 {
		w.body = append(w.body, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// heldBack answers the cause the Error record of a held-back answer names:
// why the context ended, the handler's status and, for a refusal, the first
// line of its body. The body of a success is never named, because it can
// carry a credential the write minted.
func (w *lateErrorWriter) heldBack() error {
	if w.status < http.StatusBadRequest {
		return fmt.Errorf("%w, and the handler then answered %d", w.err, w.status)
	}
	line, _, _ := strings.Cut(string(w.body), "\n")
	return fmt.Errorf("%w, and the handler then answered %d %s", w.err, w.status, line)
}
