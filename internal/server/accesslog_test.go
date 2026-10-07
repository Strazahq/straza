package server

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/server/metrics"
)

// accessRecords returns the Debug "http request" lines in the capture.
func accessRecords(buf *syncBuffer) []string {
	var out []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "level=DEBUG") && strings.Contains(line, `msg="http request"`) {
			out = append(out, line)
		}
	}
	return out
}

// waitForAccessRecord polls the capture for an access record containing
// needle: on the real server the record lands after the handler returns,
// racing the client's read of the response.
func waitForAccessRecord(t *testing.T, buf *syncBuffer, needle string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, rec := range accessRecords(buf) {
			if strings.Contains(rec, needle) {
				return rec
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no access record containing %q within 5s:\n%s", needle, buf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestStatusWriterRecordsStatus pins what the wrapper reports: the first
// status wins, a bare Write or Flush implies 200, bytes are counted, and
// every call still reaches the real writer.
func TestStatusWriterRecordsStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		do     func(w http.ResponseWriter)
		status int
		bytes  int64
	}{
		{"no write defaults to 200", func(http.ResponseWriter) {}, http.StatusOK, 0},
		{"WriteHeader 204", func(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }, http.StatusNoContent, 0},
		{"Write only is 200 plus bytes", func(w http.ResponseWriter) { _, _ = w.Write([]byte("hello")) }, http.StatusOK, 5},
		{"first WriteHeader wins", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusCreated)
			w.WriteHeader(http.StatusInternalServerError)
		}, http.StatusCreated, 0},
		{"Flush only is 200", func(w http.ResponseWriter) { w.(http.Flusher).Flush() }, http.StatusOK, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			sw := &statusWriter{ResponseWriter: rec}
			tc.do(sw)
			if got := sw.finalStatus(); got != tc.status {
				t.Fatalf("finalStatus = %d, want %d", got, tc.status)
			}
			if sw.bytes != tc.bytes {
				t.Fatalf("bytes = %d, want %d", sw.bytes, tc.bytes)
			}
			if rec.Code != tc.status {
				t.Fatalf("recorder saw %d, want %d (the wrapper must forward)", rec.Code, tc.status)
			}
		})
	}
}

// TestStatusWriterKeepsFlusherAndUnwrap pins the SSE contract through the
// whole edge chain: the handler's writer is still an http.Flusher,
// http.ResponseController reaches the real writer, and Unwrap returns it.
func TestStatusWriterKeepsFlusherAndUnwrap(t *testing.T) {
	t.Parallel()
	a, _ := liteApp(t)
	rec := httptest.NewRecorder()
	var (
		flusherOK bool
		rcErr     error
		unwrapped http.ResponseWriter
	)
	h := edgeChain(a, "GET /sse", func(w http.ResponseWriter, r *http.Request) {
		_, flusherOK = w.(http.Flusher)
		rcErr = http.NewResponseController(w).Flush()
		if u, ok := w.(interface{ Unwrap() http.ResponseWriter }); ok {
			unwrapped = u.Unwrap()
		}
		_, _ = w.Write([]byte("data: x\n\n"))
	})
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sse", nil))
	if !flusherOK {
		t.Fatal("handler writer is not an http.Flusher through the chain")
	}
	if rcErr != nil {
		t.Fatalf("ResponseController.Flush = %v", rcErr)
	}
	if unwrapped != rec {
		t.Fatalf("Unwrap = %T, want the recorder", unwrapped)
	}
	if !rec.Flushed {
		t.Fatal("Flush did not reach the recorder")
	}
}

// TestAccessLogOneDebugRecord pins the record: exactly one Debug "http
// request" per request with method, route pattern, status, subject, bytes,
// duration and the correlation id; an Info-level logger gets no record but
// the RED series still move.
func TestAccessLogOneDebugRecord(t *testing.T) {
	t.Parallel()
	a, buf := liteApp(t)
	h := edgeChain(a, "GET /t/{x}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/t/1", nil)
	req.Header.Set("X-Request-Id", "acc-1")
	h.ServeHTTP(httptest.NewRecorder(), req)
	recs := accessRecords(buf)
	if len(recs) != 1 {
		t.Fatalf("access records = %d, want 1:\n%s", len(recs), buf.String())
	}
	for _, want := range []string{"correlation_id=acc-1", "method=GET", `route="GET /t/{x}"`, "status=204", "subject=public", "bytes=0", "duration_ms="} {
		if !strings.Contains(recs[0], want) {
			t.Fatalf("record lacks %s: %s", want, recs[0])
		}
	}

	quiet := &syncBuffer{}
	info := slog.New(slog.NewTextHandler(quiet, &slog.HandlerOptions{Level: slog.LevelInfo}))
	b := &App{log: info, metrics: metrics.New(func() float64 { return 0 }, func() float64 { return 0 })}
	h2 := edgeChain(b, "GET /t/{x}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h2.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/t/2", nil))
	if n := len(accessRecords(quiet)); n != 0 {
		t.Fatalf("Info-level logger produced %d access records:\n%s", n, quiet.String())
	}
	if got := httpRequestCounter(t, b, "GET /t/{x}", "204"); got != 1 {
		t.Fatalf("requests counter at Info = %v, want 1", got)
	}
}

// TestAccessLogSkipsProbes pins that /healthz, /readyz and /metrics leave
// no record and move no series on either listener.
func TestAccessLogSkipsProbes(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"/healthz", "/readyz", "/metrics"} {
		t.Run(p, func(t *testing.T) {
			a, buf := liteApp(t)
			route := "GET " + p
			h := edgeChain(a, route, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
			if n := len(accessRecords(buf)); n != 0 {
				t.Fatalf("probe %s produced %d access records", p, n)
			}
			if got := httpRequestCounter(t, a, route, "200"); got != 0 {
				t.Fatalf("probe %s counted %v requests", p, got)
			}
			if got := httpLatencyCount(t, a, route); got != 0 {
				t.Fatalf("probe %s observed %d latencies", p, got)
			}
		})
	}
}

// TestAccessLogRecordsPanicAs500 pins that a recovered panic is measured as
// the 500 it answered: one access record, one request counted, and the
// error counter still single-sourced at recoverPanics (both read 1).
func TestAccessLogRecordsPanicAs500(t *testing.T) {
	t.Parallel()
	a, buf := liteApp(t)
	h := edgeChain(a, "GET /boom", func(w http.ResponseWriter, r *http.Request) {
		panic("kaboom")
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	recs := accessRecords(buf)
	if len(recs) != 1 || !strings.Contains(recs[0], "status=500") {
		t.Fatalf("access records = %v, want one with status=500", recs)
	}
	if got := httpRequestCounter(t, a, "GET /boom", "500"); got != 1 {
		t.Fatalf("requests counter = %v, want 1", got)
	}
	if got := httpErrorCounter(t, a, "GET /boom", "500"); got != 1 {
		t.Fatalf("errors counter = %v, want 1", got)
	}
}

// TestAccessLogSilentOnAbortHandler pins Go's "drop the connection silently"
// contract: http.ErrAbortHandler propagates through the chain and leaves no
// record and no series behind.
func TestAccessLogSilentOnAbortHandler(t *testing.T) {
	t.Parallel()
	a, buf := liteApp(t)
	h := edgeChain(a, "GET /abort", func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	})
	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("ErrAbortHandler did not propagate")
		}
		err, ok := rec.(error)
		if !ok || !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", rec)
		}
		if n := len(accessRecords(buf)); n != 0 {
			t.Fatalf("aborted request produced %d access records", n)
		}
		if got := httpRequestCounter(t, a, "GET /abort", "200"); got != 0 {
			t.Fatalf("aborted request counted %v", got)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/abort", nil))
}

// TestHTTPRequestMetrics pins the RED series: every answer counts by route
// pattern and exact status, the histogram observes once per request, an
// unmatched path lands on route "-", and straza_http_errors_total keeps
// counting only at the a.fail sites.
func TestHTTPRequestMetrics(t *testing.T) {
	t.Parallel()
	a, _ := liteApp(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ok", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("x")) })
	mux.HandleFunc("GET /bad", func(w http.ResponseWriter, r *http.Request) {
		a.fail(w, r, http.StatusServiceUnavailable, "down", nil)
	})
	chain := a.requestID(a.accessLog(a.recoverPanics(mux)))
	for _, p := range []string{"/ok", "/bad", "/nope"} {
		chain.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
	}
	want := []struct {
		route, status string
	}{{"GET /ok", "200"}, {"GET /bad", "503"}, {"-", "404"}}
	for _, w := range want {
		if got := httpRequestCounter(t, a, w.route, w.status); got != 1 {
			t.Fatalf("requests{%s,%s} = %v, want 1", w.route, w.status, got)
		}
		if got := httpLatencyCount(t, a, w.route); got != 1 {
			t.Fatalf("latency{%s} count = %d, want 1", w.route, got)
		}
	}
	if got := httpErrorCounter(t, a, "GET /bad", "503"); got != 1 {
		t.Fatalf("errors{GET /bad,503} = %v, want 1", got)
	}
	if got := httpErrorCounter(t, a, "GET /ok", "200"); got != 0 {
		t.Fatalf("errors{GET /ok,200} = %v, want 0 (2xx is not an error)", got)
	}
}

// TestSubjectKind pins the route-prefix table behind the subject label.
func TestSubjectKind(t *testing.T) {
	t.Parallel()
	cases := []struct{ route, kind string }{
		{"GET /v1/admin/users", "admin"},
		{"POST /v1/approver/decide", "approver"},
		{"GET /v1/approvals", "approver"},
		{"GET /v1/approval/{id}", "approver"},
		{"GET /approvals/", "approver"},
		{"GET /self-service/", "approver"},
		{"/mcp", "gateway"},
		{"POST /v1/decide", "pdp"},
		{"POST /v1/enroll", "pdp"},
		{"POST /v1/checkin", "pdp"},
		{"POST /v1/audit/batch", "pdp"},
		{"GET /v1/push", "pdp"},
		{"GET /v1/snapshot", "pdp"},
		{"GET /v1/harness-config", "pdp"},
		{"DELETE /v1/session/{id}", "pdp"},
		{"GET /scim/v2/Users", "scim"},
		{"GET /console/", "console"},
		{"POST /oidc/token", "oidc"},
		{"GET /.well-known/straza/snapshot-keys.json", "oidc"},
		{"GET /v1/connect", "public"},
		{"GET /version", "public"},
		{"-", "public"},
	}
	for _, tc := range cases {
		if got := subjectKind(tc.route); got != tc.kind {
			t.Errorf("subjectKind(%q) = %q, want %q", tc.route, got, tc.kind)
		}
	}
}

// TestAccessLogOnRealServer drives the real app: a plain answer is recorded
// once on the public surface, probes stay silent, an SSE stream still
// flushes through the wrapper and is recorded when it ends, and a 503 on the
// outage lane is recorded with the same correlation id its one Error record
// carries.
func TestAccessLogOnRealServer(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	app, base, fs := testAppFaultLog(t, log)
	seedIdentity(t, app)
	token, _ := checkinToken(t, app, base)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	buf.Reset()

	resp, err := http.Get(base + "/version")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	rec := waitForAccessRecord(t, buf, `route="GET /version"`)
	for _, want := range []string{"status=200", "subject=public", "correlation_id=" + resp.Header.Get("X-Request-Id")} {
		if !strings.Contains(rec, want) {
			t.Fatalf("version record lacks %s: %s", want, rec)
		}
	}

	probe, err := http.Get(base + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	_ = probe.Body.Close()

	lines, cancel, sresp := sseStream(t, base, token)
	if sresp.StatusCode != http.StatusOK {
		t.Fatalf("sse status = %d", sresp.StatusCode)
	}
	waitForLine(t, lines, "event: ready")
	cancel()
	rec = waitForAccessRecord(t, buf, `route="GET /v1/push"`)
	for _, want := range []string{"status=200", "subject=pdp"} {
		if !strings.Contains(rec, want) {
			t.Fatalf("push record lacks %s: %s", want, rec)
		}
	}
	for _, r := range accessRecords(buf) {
		if strings.Contains(r, `route="GET /readyz"`) {
			t.Fatalf("probe was recorded: %s", r)
		}
	}

	fs.arm("users", errors.New("boom"))
	fs.setPing(errors.New("boom"))
	code, hdr, _ := callJSON(t, "POST", base+"/v1/checkin", "", map[string]any{
		"id_token": idToken, "harness": map[string]string{"name": "claude-code", "version": "1.0"},
	})
	fs.disarm()
	if code != http.StatusServiceUnavailable {
		t.Fatalf("checkin during outage = %d, want 503", code)
	}
	id := hdr.Get("X-Request-Id")
	rec = waitForAccessRecord(t, buf, "correlation_id="+id)
	if !strings.Contains(rec, "status=503") || !strings.Contains(rec, `route="POST /v1/checkin"`) {
		t.Fatalf("outage access record = %s", rec)
	}
	assertOneErrorWithCorrelation(t, buf, http.StatusServiceUnavailable, id)
}
