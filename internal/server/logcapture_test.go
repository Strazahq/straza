package server

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The capture harness: a capturing logger and the assertion every 5xx
// answer is held to: exactly ONE Error record, carrying the same
// correlation id the response header and body carry, plus the status.

// syncBuffer is a bytes.Buffer safe for the server goroutines to write while
// the test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuffer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b.Reset()
}

// captureLogger is the slog text handler the tests read back (the pattern
// admin_rbac_test.go uses), at Debug so nothing is filtered.
func captureLogger() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// errorRecords returns the level=ERROR lines in the capture.
func errorRecords(buf *syncBuffer) []string {
	var out []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "level=ERROR") {
			out = append(out, line)
		}
	}
	return out
}

// assertOneErrorWithCorrelation asserts the capture holds exactly one Error
// record and that it carries correlation_id=<wantID> and status=<wantStatus>;
// it returns the record for further key checks.
func assertOneErrorWithCorrelation(t *testing.T, buf *syncBuffer, wantStatus int, wantID string) string {
	t.Helper()
	recs := errorRecords(buf)
	if len(recs) != 1 {
		t.Fatalf("Error records = %d, want exactly 1:\n%s", len(recs), buf.String())
	}
	if wantID == "" {
		t.Fatalf("no correlation id to assert against (response had no X-Request-Id)")
	}
	if !strings.Contains(recs[0], "correlation_id="+wantID) {
		t.Fatalf("Error record lacks correlation_id=%s: %s", wantID, recs[0])
	}
	if !strings.Contains(recs[0], "status="+strconv.Itoa(wantStatus)) {
		t.Fatalf("Error record lacks status=%d: %s", wantStatus, recs[0])
	}
	return recs[0]
}

// metricSample finds the series name{labels} on the app's private registry
// (labels must match exactly) and returns its counter value and histogram
// sample count, whichever the series carries; ok is false when no series
// matches.
func metricSample(t *testing.T, a *App, name string, labels map[string]string) (counter float64, histCount uint64, ok bool) {
	t.Helper()
	fams, err := a.metrics.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			if len(m.GetLabel()) != len(labels) {
				continue
			}
			match := true
			for _, l := range m.GetLabel() {
				if labels[l.GetName()] != l.GetValue() {
					match = false
					break
				}
			}
			if match {
				return m.GetCounter().GetValue(), m.GetHistogram().GetSampleCount(), true
			}
		}
	}
	return 0, 0, false
}

// httpErrorCounter reads straza_http_errors_total{route,status} off the app's
// private registry.
func httpErrorCounter(t *testing.T, a *App, route, status string) float64 {
	t.Helper()
	c, _, _ := metricSample(t, a, "straza_http_errors_total", map[string]string{"route": route, "status": status})
	return c
}

// httpRequestCounter reads straza_http_requests_total{route,status}.
func httpRequestCounter(t *testing.T, a *App, route, status string) float64 {
	t.Helper()
	c, _, _ := metricSample(t, a, "straza_http_requests_total", map[string]string{"route": route, "status": status})
	return c
}

// httpLatencyCount reads the sample count of straza_http_request_seconds{route}.
func httpLatencyCount(t *testing.T, a *App, route string) uint64 {
	t.Helper()
	_, n, _ := metricSample(t, a, "straza_http_request_seconds", map[string]string{"route": route})
	return n
}

// TestOutageAnswersCarryCorrelation pins the aligned outage writers on the
// real server: the identity lane (answerOutage -> a.fail), the approver
// bearer lane (answerOutageCode -> a.failCode) and the approver service lane
// (answerServiceOutageCode -> a.failCode) each answer the same generic 503
// with Retry-After, with correlation_id in the body equal to
// the X-Request-Id header, and exactly one Error record carrying that id.
func TestOutageAnswersCarryCorrelation(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	app, base, fs := testAppFaultLog(t, log)
	user := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	harness := map[string]string{"name": "claude-code", "version": "1.0"}
	_, approverDevice, approverTok := enrollApprover(t, app, user.ID)

	lanes := []struct {
		name     string
		arm      func()
		call     func() (int, http.Header, map[string]any)
		wantCode string
	}{
		{"identity exchange (answerOutage)", func() { fs.arm("users", errors.New("boom")); fs.setPing(errors.New("boom")) }, func() (int, http.Header, map[string]any) {
			return callJSON(t, "POST", base+"/v1/checkin", "", map[string]any{"id_token": idToken, "harness": harness})
		}, ""},
		{"approver bearer (answerOutageCode)", func() { fs.arm("approvers", pgDown) }, func() (int, http.Header, map[string]any) {
			return callJSON(t, "GET", base+"/v1/approver/pending?scope=decidable", approverTok, nil)
		}, codeServiceUnavailable},
		{"approver service (answerServiceOutageCode)", func() { fs.arm("approvers", pgDown) }, func() (int, http.Header, map[string]any) {
			return callJSON(t, "POST", base+"/v1/approver/refresh/challenge", "", map[string]any{"device_id": approverDevice})
		}, codeServiceUnavailable},
	}
	for _, lane := range lanes {
		t.Run(lane.name, func(t *testing.T) {
			buf.Reset()
			lane.arm()
			code, hdr, out := lane.call()
			fs.disarm()
			if code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d %v, want 503", code, out)
			}
			if out["error"] != loginOutageBody {
				t.Fatalf("body error = %v, want %q", out["error"], loginOutageBody)
			}
			if hdr.Get("Retry-After") == "" {
				t.Fatal("no Retry-After on the 503")
			}
			id := hdr.Get("X-Request-Id")
			if id == "" || out["correlation_id"] != id {
				t.Fatalf("correlation_id = %v, X-Request-Id = %q", out["correlation_id"], id)
			}
			if lane.wantCode != "" && out["code"] != lane.wantCode {
				t.Fatalf("code = %v, want %s", out["code"], lane.wantCode)
			}
			assertOneErrorWithCorrelation(t, buf, http.StatusServiceUnavailable, id)
		})
	}
}

// remaining5xxSites is the ratchet on uncorrelated 5xx answers: the number
// of 5xx answers in internal/server still written through apiError /
// apiErrorCode / a bare internal-class writeRPCError instead of a.fail /
// a.failCode / a.rpcFail (which log once with the correlation id). It stays
// at 0.
const remaining5xxSites = 0

var (
	fiveXXStatus      = `(InternalServerError|ServiceUnavailable|BadGateway|GatewayTimeout|NotImplemented)`
	reAPIError5xx     = regexp.MustCompile(`\bapiError\(w, http\.Status` + fiveXXStatus)
	reAPIErrorCode5xx = regexp.MustCompile(`\bapiErrorCode\(w, http\.Status` + fiveXXStatus)
	reRPCInternal     = regexp.MustCompile(`\bwriteRPCError\(w, [^,]+, -32(603|000)\b`)
)

// TestRemaining5xxSitesWithoutFail scans the package source (every non-test
// .go file) and refuses to let the number of uncorrelated 5xx writers grow.
func TestRemaining5xxSitesWithoutFail(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	perFile := map[string]int{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		n := len(reAPIError5xx.FindAll(raw, -1)) + len(reAPIErrorCode5xx.FindAll(raw, -1)) + len(reRPCInternal.FindAll(raw, -1))
		if n > 0 {
			perFile[name] = n
			total += n
		}
	}
	if total > remaining5xxSites {
		t.Fatalf("uncorrelated 5xx writers = %d, ratchet allows %d (new 5xx answers must go through a.fail / a.failCode / a.rpcFail): %v", total, remaining5xxSites, perFile)
	}
	if total < remaining5xxSites {
		t.Logf("uncorrelated 5xx writers = %d, ratchet is %d: lower remaining5xxSites at the next pass (%v)", total, remaining5xxSites, perFile)
	}
}
