package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"

	"github.com/google/uuid"
)

// Request correlation at the HTTP edge.
//
// Every response carries an X-Request-Id. A client-supplied id is accepted
// when it is sane (at most 64 printable ASCII bytes, so an ingress or LB id
// correlates end to end) and replaced by a minted UUIDv7 otherwise; the id is
// echoed in the response header, stored in the request context, and a
// per-request logger carrying it rides along. Inside Straza the value is
// the CORRELATION id: the log key and the 5xx body field are both
// `correlation_id`, never `request_id`, because on the approver wire
// `request_id` already names the approval record (approver_decide.go, the
// signed decide payload, the approver app). The header keeps the standard name.
//
// Both listeners inherit the chain: the approver server wraps a.http.Handler
// (build.go), and requestID is the outermost layer of routes().

const (
	requestIDHeader = "X-Request-Id"
	// correlationKey is the slog attribute AND the JSON body key.
	correlationKey  = "correlation_id"
	maxRequestIDLen = 64
	// maxPanicStack bounds the stack a recovered panic logs (one record,
	// never the whole goroutine dump).
	maxPanicStack = 8 << 10
)

type reqIDCtxKey struct{}
type reqLogCtxKey struct{}

// saneRequestID reports whether a client-supplied id may be echoed: bounded,
// printable ASCII, no spaces or control characters (it lands in log records
// and response headers verbatim).
func saneRequestID(v string) bool {
	if v == "" || len(v) > maxRequestIDLen {
		return false
	}
	for i := 0; i < len(v); i++ {
		if c := v[i]; c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

// mintRequestID returns a fresh UUIDv7 (time-ordered, the id shape the rest
// of Straza uses); the v4 fallback exists only so the function never returns
// an empty id.
func mintRequestID() string {
	if u, err := uuid.NewV7(); err == nil {
		return u.String()
	}
	return uuid.NewString()
}

// reqID returns the correlation id of the request behind ctx ("" outside a
// request).
func reqID(ctx context.Context) string {
	id, _ := ctx.Value(reqIDCtxKey{}).(string)
	return id
}

// reqlog returns the per-request logger (a.log with correlation_id bound)
// or a.log itself when ctx carries none, so callers never nil-check.
func (a *App) reqlog(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(reqLogCtxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return a.log
}

// requestID is the outermost middleware: accept-sane-else-mint, echo the
// header before the handler runs (so even a panic answer carries it), and
// bind the id plus the per-request logger into the context.
func (a *App) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !saneRequestID(id) {
			id = mintRequestID()
		}
		w.Header().Set(requestIDHeader, id)
		ctx := context.WithValue(r.Context(), reqIDCtxKey{}, id)
		ctx = context.WithValue(ctx, reqLogCtxKey{}, a.log.With(correlationKey, id))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recoverPanics turns a handler panic into a structured answer: one Error
// record with the correlation id, route, method, path and a bounded stack,
// then a generic 500 whose body names the id and nothing of the panic.
// http.ErrAbortHandler is Go's own "drop the connection silently" contract
// and is re-panicked untouched. If the handler had already started the
// response, the 500 cannot be written; the record still lands and Go's
// server logs the superfluous WriteHeader through ErrorLog (slog Warn).
func (a *App) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(rec)
			}
			stack := debug.Stack()
			if len(stack) > maxPanicStack {
				stack = stack[:maxPanicStack]
			}
			id := reqID(r.Context())
			a.reqlog(r.Context()).Error("panic serving request",
				"status", http.StatusInternalServerError, "route", routeLabel(r), "method", r.Method,
				"path", r.URL.Path, "panic", fmt.Sprint(rec), "stack", string(stack))
			a.countHTTPError(r, strconv.Itoa(http.StatusInternalServerError))
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error":        fmt.Sprintf("Straza: internal error (ref %s)", id),
				correlationKey: id,
			})
		}()
		next.ServeHTTP(w, r)
	})
}

// routeLabel is the matched mux pattern ("GET /v1/decide"), the bounded
// label every failure record and the error counter carry; "-" when no
// pattern matched (the request never reached a route).
func routeLabel(r *http.Request) string {
	if r.Pattern == "" {
		return "-"
	}
	return r.Pattern
}

func (a *App) countHTTPError(r *http.Request, status string) {
	if a.metrics != nil {
		a.metrics.HTTPError(routeLabel(r), status)
	}
}

// logFailure writes the ONE Error record every 5xx answer owes: the message
// as the record text, then correlation_id (from the per-request logger),
// status, route, method, the authenticated actor when the admin plane
// bound one, and the cause when there is one.
func (a *App) logFailure(r *http.Request, status, msg string, err error, extra ...any) {
	attrs := []any{"status", status, "route", routeLabel(r), "method", r.Method}
	if act, ok := actorFrom(r.Context()); ok && act.Name != "" {
		attrs = append(attrs, "actor", act.Name)
	}
	attrs = append(attrs, extra...)
	if err != nil {
		attrs = append(attrs, "cause", err)
	}
	a.reqlog(r.Context()).Error(msg, attrs...)
}

// fail answers a 5xx through the shared {"error"} envelope plus the
// correlation id, logging exactly once. Use it for every server-side
// failure; 4xx answers stay on apiError (client mistakes, not incidents).
func (a *App) fail(w http.ResponseWriter, r *http.Request, status int, msg string, err error) {
	a.logFailure(r, strconv.Itoa(status), msg, err)
	a.countHTTPError(r, strconv.Itoa(status))
	writeJSON(w, status, map[string]string{"error": msg, correlationKey: reqID(r.Context())})
}
