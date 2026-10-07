package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/server/metrics"
)

// liteApp builds an App with only the fields the correlation primitives read
// (log, metrics): no listener, no store. Unit tests for the edge middleware
// and the fail writers run against it through an httptest recorder.
func liteApp(t *testing.T) (*App, *syncBuffer) {
	t.Helper()
	log, buf := captureLogger()
	return &App{log: log, metrics: metrics.New(func() float64 { return 0 }, func() float64 { return 0 })}, buf
}

// edgeChain wires a handler the way routes() does (requestID outermost, then
// accessLog, then recoverPanics) under a real ServeMux pattern so r.Pattern
// is populated.
func edgeChain(a *App, pattern string, h http.HandlerFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(pattern, h)
	return a.requestID(a.accessLog(a.recoverPanics(mux)))
}

// TestRequestIDMiddleware pins the X-Request-Id contract: a sane supplied id
// (at most 64 printable ASCII bytes) is accepted and echoed so an ingress id
// correlates end to end; anything else is replaced by a minted UUIDv7; the
// handler sees the same id the response carries.
func TestRequestIDMiddleware(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		supplied string
		echoed   bool
	}{
		{"sane supplied id is echoed", "edge-7f3a-9", true},
		{"64 bytes is the bound and is accepted", strings.Repeat("z", 64), true},
		{"absent id is minted", "", false},
		{"65 bytes is too long and is minted", strings.Repeat("a", 65), false},
		{"space is not printable and is minted", "two words", false},
		{"control character is minted", "tab\there", false},
		{"non-ascii is minted", "idé", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := liteApp(t)
			var seen string
			h := edgeChain(a, "GET /t/{x}", func(w http.ResponseWriter, r *http.Request) {
				seen = reqID(r.Context())
				w.WriteHeader(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodGet, "/t/1", nil)
			if tc.supplied != "" {
				req.Header.Set("X-Request-Id", tc.supplied)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			got := rec.Header().Get("X-Request-Id")
			if got == "" {
				t.Fatal("no X-Request-Id on the response")
			}
			if seen != got {
				t.Fatalf("handler saw %q, response carries %q", seen, got)
			}
			if tc.echoed {
				if got != tc.supplied {
					t.Fatalf("sane id %q was replaced by %q", tc.supplied, got)
				}
				return
			}
			if tc.supplied != "" && got == tc.supplied {
				t.Fatalf("unsafe id %q was echoed verbatim", got)
			}
			u, err := uuid.Parse(got)
			if err != nil || u.Version() != 7 || len(got) != 36 {
				t.Fatalf("minted id %q is not a UUIDv7 (err %v)", got, err)
			}
		})
	}
}

// TestRecoverPanicsAnswers500WithCorrelation pins the structured panic answer:
// 500, a generic body that names the correlation id and nothing of the
// panic, exactly one Error record carrying the same id, the route and a
// stack; http.ErrAbortHandler stays a panic (Go's own abort contract).
func TestRecoverPanicsAnswers500WithCorrelation(t *testing.T) {
	t.Parallel()
	t.Run("panic becomes a correlated 500", func(t *testing.T) {
		a, buf := liteApp(t)
		h := edgeChain(a, "GET /t/{x}", func(http.ResponseWriter, *http.Request) {
			panic("kaboom: secret detail")
		})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/9", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		id := rec.Header().Get("X-Request-Id")
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("body is not JSON: %v (%s)", err, rec.Body.String())
		}
		if body["correlation_id"] != id || id == "" {
			t.Fatalf("body correlation_id = %q, header = %q", body["correlation_id"], id)
		}
		if want := "Straza: internal error (ref " + id + ")"; body["error"] != want {
			t.Fatalf("body error = %q, want %q", body["error"], want)
		}
		if strings.Contains(rec.Body.String(), "secret detail") {
			t.Fatalf("panic text leaked into the body: %s", rec.Body.String())
		}
		rec1 := assertOneErrorWithCorrelation(t, buf, http.StatusInternalServerError, id)
		for _, key := range []string{`route="GET /t/{x}"`, "stack=", "method=GET"} {
			if !strings.Contains(rec1, key) {
				t.Fatalf("Error record lacks %s: %s", key, rec1)
			}
		}
		if got := httpErrorCounter(t, a, "GET /t/{x}", "500"); got != 1 {
			t.Fatalf("straza_http_errors_total{route,500} = %v, want 1", got)
		}
	})
	t.Run("ErrAbortHandler is re-panicked, not converted", func(t *testing.T) {
		a, buf := liteApp(t)
		h := edgeChain(a, "GET /t/{x}", func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		})
		rec := httptest.NewRecorder()
		func() {
			defer func() {
				rec := recover()
				if err, ok := rec.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
					t.Fatalf("recovered %v, want http.ErrAbortHandler to propagate", rec)
				}
			}()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/9", nil))
		}()
		if rec.Code == http.StatusInternalServerError {
			t.Fatalf("ErrAbortHandler was answered with a 500 body: %s", rec.Body.String())
		}
		if n := len(errorRecords(buf)); n != 0 {
			t.Fatalf("ErrAbortHandler logged %d Error records, want 0", n)
		}
	})
}

// TestFailWritersLogOnceAndCarryCorrelation pins the three 5xx writers:
// a.fail (plain envelope), a.failCode (approver
// coded envelope) and a.rpcFail (JSON-RPC error body, unchanged shape). Each
// writes the id into the body (rpcFail: unchanged body), logs exactly one
// Error record with correlation_id/status/route/method/cause, and counts
// one straza_http_errors_total.
func TestFailWritersLogOnceAndCarryCorrelation(t *testing.T) {
	t.Parallel()
	cause := errors.New("pg: connection refused")
	t.Run("fail", func(t *testing.T) {
		a, buf := liteApp(t)
		h := edgeChain(a, "POST /v1/thing/{id}", func(w http.ResponseWriter, r *http.Request) {
			a.fail(w, r, http.StatusServiceUnavailable, "Straza is temporarily unavailable. Try again in a moment", cause)
		})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/thing/7", nil))
		id := rec.Header().Get("X-Request-Id")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		var body map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body["error"] != "Straza is temporarily unavailable. Try again in a moment" || body["correlation_id"] != id || id == "" {
			t.Fatalf("body = %v, header id = %q", body, id)
		}
		if _, hasCode := body["code"]; hasCode {
			t.Fatalf("plain envelope grew a code: %v", body)
		}
		rec1 := assertOneErrorWithCorrelation(t, buf, http.StatusServiceUnavailable, id)
		for _, key := range []string{`route="POST /v1/thing/{id}"`, "method=POST", `cause="pg: connection refused"`} {
			if !strings.Contains(rec1, key) {
				t.Fatalf("Error record lacks %s: %s", key, rec1)
			}
		}
		if got := httpErrorCounter(t, a, "POST /v1/thing/{id}", "503"); got != 1 {
			t.Fatalf("counter = %v, want 1", got)
		}
	})
	t.Run("failCode", func(t *testing.T) {
		a, buf := liteApp(t)
		h := edgeChain(a, "GET /v1/approver/pending", func(w http.ResponseWriter, r *http.Request) {
			a.failCode(w, r, http.StatusServiceUnavailable, codeServiceUnavailable, loginOutageBody, cause)
		})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/approver/pending", nil))
		id := rec.Header().Get("X-Request-Id")
		var body map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != http.StatusServiceUnavailable || body["error"] != loginOutageBody || body["code"] != codeServiceUnavailable || body["correlation_id"] != id {
			t.Fatalf("status %d body %v header id %q", rec.Code, body, id)
		}
		assertOneErrorWithCorrelation(t, buf, http.StatusServiceUnavailable, id)
	})
	t.Run("rpcFail keeps the JSON-RPC body and logs once", func(t *testing.T) {
		a, buf := liteApp(t)
		h := edgeChain(a, "POST /mcp", func(w http.ResponseWriter, r *http.Request) {
			a.rpcFail(w, r, json.RawMessage(`7`), -32000, "upstream call failed: dial tcp: refused", cause)
		})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
		id := rec.Header().Get("X-Request-Id")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (JSON-RPC errors ride HTTP 200)", rec.Code)
		}
		var body struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Error   rpcError        `json:"error"`
			Extra   map[string]any  `json:"-"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.JSONRPC != "2.0" || string(body.ID) != "7" || body.Error.Code != -32000 || body.Error.Message != "upstream call failed: dial tcp: refused" {
			t.Fatalf("rpc body changed: %s", rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "correlation_id") {
			t.Fatalf("rpc body grew a field: %s", rec.Body.String())
		}
		rec1 := assertOneErrorWithCorrelation(t, buf, http.StatusOK, id)
		if !strings.Contains(rec1, "rpc_code=-32000") {
			t.Fatalf("Error record lacks rpc_code: %s", rec1)
		}
		if got := httpErrorCounter(t, a, "POST /mcp", "rpc-32000"); got != 1 {
			t.Fatalf("counter = %v, want 1", got)
		}
	})
	t.Run("nil cause logs without a cause key", func(t *testing.T) {
		a, buf := liteApp(t)
		h := edgeChain(a, "GET /x", func(w http.ResponseWriter, r *http.Request) {
			a.fail(w, r, http.StatusInternalServerError, "internal", nil)
		})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		rec1 := assertOneErrorWithCorrelation(t, buf, http.StatusInternalServerError, rec.Header().Get("X-Request-Id"))
		if strings.Contains(rec1, "cause=") {
			t.Fatalf("nil cause rendered: %s", rec1)
		}
	})
}
