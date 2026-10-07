package server

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The HTTP edge access log and the RED metrics.
//
// accessLog sits inside requestID, so the per-request logger already carries
// correlation_id, and outside recoverPanics, so a recovered panic's 500 is
// measured like any other answer. Every completed request on both listeners
// increments straza_http_requests_total{route,status} and observes
// straza_http_request_seconds{route}, and those two series are always on. The
// Debug record "http request" is the developer and eval instrument, built only
// when the logger is enabled at Debug, so the Info default pays one wrapper
// allocation and one metrics observe per request and no attribute work at all.
// Probes (/healthz, /readyz, /metrics) are excluded from both before the
// wrapper is allocated, because on two listeners they would dominate the
// counter and flatten the histogram with zero operational signal.

// accessLogSkip lists the probe paths excluded from the access log and the
// RED series on both listeners.
var accessLogSkip = map[string]bool{"/healthz": true, "/readyz": true, "/metrics": true}

// statusWriter records what the handler answered (first status, bytes
// written) while forwarding every call to the real writer. It is the one
// http.ResponseWriter wrapper on every route, it forwards Flush and it
// implements Unwrap, so the SSE hub (ssehub.go) and the push edge (push.go)
// keep their Flusher and http.ResponseController reaches the real writer. One
// accepted degradation: http.MaxBytesReader (hardening.go, api_apps.go,
// gateway.go) signals requestTooLarge() through an unexported interface
// assertion on the writer it is handed, which does not Unwrap, so after an
// oversize body the server drains up to 256 KiB before closing instead of
// closing at once. The 400 answer itself is unchanged.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

// WriteHeader records the first status code and always forwards (a second
// call still reaches net/http, whose "superfluous WriteHeader" warning lands
// in ErrorLog as before).
func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write implies 200 when no header was written (net/http does the same) and
// counts the bytes the handler wrote.
func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

// Flush implies 200 (net/http sends the headers on the first flush) and
// forwards through ResponseController, so a non-flushing writer is a no-op,
// never a panic.
func (w *statusWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap exposes the real writer to http.ResponseController (deadlines,
// full-duplex, hijack).
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// finalStatus is the status the client saw: the recorded one, or 200 when
// the handler returned without writing anything (net/http answers 200 then).
func (w *statusWriter) finalStatus() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// accessLog is the edge middleware that records every completed request:
// the RED series always, the Debug "http request" record when the logger is
// enabled at Debug. A panic that crosses it (only http.ErrAbortHandler can,
// recoverPanics handles the rest inside) is neither logged nor counted: Go's
// contract for it is "drop the connection silently".
func (a *App) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if accessLogSkip[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		completed := false
		defer func() {
			if completed {
				a.observeRequest(r, sw, time.Since(start))
			}
		}()
		next.ServeHTTP(sw, r)
		completed = true
	})
}

// observeRequest records one completed request: metrics first and always,
// then the Debug record behind the level check so the Info default builds
// no attributes.
func (a *App) observeRequest(r *http.Request, sw *statusWriter, d time.Duration) {
	route := routeLabel(r)
	status := sw.finalStatus()
	if a.metrics != nil {
		a.metrics.HTTPRequest(route, strconv.Itoa(status), d)
	}
	ctx := r.Context()
	log := a.reqlog(ctx)
	if !log.Enabled(ctx, slog.LevelDebug) {
		return
	}
	log.LogAttrs(ctx, slog.LevelDebug, "http request",
		slog.String("method", r.Method),
		slog.String("route", route),
		slog.Int("status", status),
		slog.Float64("duration_ms", float64(d.Microseconds())/1000),
		slog.String("subject", subjectKind(route)),
		slog.Int64("bytes", sw.bytes),
	)
}

// subjectKind names the surface a route belongs to from the matched pattern
// alone (no auth coupling): admin, approver, gateway, pdp, scim, console,
// oidc or public. "-" (no pattern matched) is public.
func subjectKind(route string) string {
	path := route
	if i := strings.LastIndexByte(path, ' '); i >= 0 {
		path = path[i+1:]
	}
	switch {
	case strings.HasPrefix(path, "/v1/admin/"):
		return "admin"
	case strings.HasPrefix(path, "/v1/approver/"), strings.HasPrefix(path, "/v1/approvals"),
		strings.HasPrefix(path, "/v1/approval/"), strings.HasPrefix(path, "/approvals"),
		strings.HasPrefix(path, "/self-service"):
		return "approver"
	case strings.HasPrefix(path, "/mcp"):
		return "gateway"
	case strings.HasPrefix(path, "/v1/decide"), strings.HasPrefix(path, "/v1/enroll"),
		strings.HasPrefix(path, "/v1/checkin"), strings.HasPrefix(path, "/v1/audit"),
		strings.HasPrefix(path, "/v1/push"), strings.HasPrefix(path, "/v1/snapshot"),
		strings.HasPrefix(path, "/v1/harness-config"), strings.HasPrefix(path, "/v1/session"):
		return "pdp"
	case strings.HasPrefix(path, "/scim/"):
		return "scim"
	case strings.HasPrefix(path, "/console"):
		return "console"
	case strings.HasPrefix(path, "/oidc/"), strings.HasPrefix(path, "/.well-known/"):
		return "oidc"
	}
	return "public"
}
