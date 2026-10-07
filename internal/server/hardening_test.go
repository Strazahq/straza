package server

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/server/ratelimit"
)

// TestBodyCap: the global request-body cap makes an oversize body fail at
// read time (memory can no longer balloon), leaves small bodies untouched,
// and exempts the self-capped bulk lanes.
func TestBodyCap(t *testing.T) {
	t.Parallel()
	var readErr error
	var readN int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		readN, readErr = len(b), err
		w.WriteHeader(http.StatusOK)
	})
	h := bodyCap(64, next)

	small := httptest.NewRequest(http.MethodPost, "/v1/checkin", strings.NewReader("under the cap"))
	h.ServeHTTP(httptest.NewRecorder(), small)
	if readErr != nil || readN != len("under the cap") {
		t.Fatalf("small body: n=%d err=%v", readN, readErr)
	}

	big := httptest.NewRequest(http.MethodPost, "/v1/checkin", bytes.NewReader(make([]byte, 1024)))
	h.ServeHTTP(httptest.NewRecorder(), big)
	if readErr == nil {
		t.Fatal("oversize body read fully: cap not applied")
	}

	// Bulk lanes keep their own contracts: the same 1 KiB body passes here.
	bulk := httptest.NewRequest(http.MethodPost, "/v1/audit/batch", bytes.NewReader(make([]byte, 1024)))
	h.ServeHTTP(httptest.NewRecorder(), bulk)
	if readErr != nil || readN != 1024 {
		t.Fatalf("exempt path capped: n=%d err=%v", readN, readErr)
	}

	// Zero disables: the middleware must be pass-through, not cap-at-zero.
	off := bodyCap(0, next)
	off.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/checkin", bytes.NewReader(make([]byte, 1024))))
	if readErr != nil || readN != 1024 {
		t.Fatalf("disabled cap still limited: n=%d err=%v", readN, readErr)
	}
}

// TestPerIPLimit: the approver-listener throttle keys on the transport peer,
// answers 429 with Retry-After when a bucket runs dry, and keeps distinct
// IPs independent.
func TestPerIPLimit(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := perIPLimit(ratelimit.New(), 1, next)

	req := func(ip string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/v1/approver/pending", nil)
		r.RemoteAddr = ip + ":51234"
		return r
	}
	first := httptest.NewRecorder()
	h.ServeHTTP(first, req("198.51.100.7"))
	if first.Code != http.StatusOK {
		t.Fatalf("first request = %d", first.Code)
	}
	second := httptest.NewRecorder()
	h.ServeHTTP(second, req("198.51.100.7"))
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Fatalf("second request = %d (Retry-After %q), want 429 with Retry-After", second.Code, second.Header().Get("Retry-After"))
	}
	other := httptest.NewRecorder()
	h.ServeHTTP(other, req("198.51.100.8"))
	if other.Code != http.StatusOK {
		t.Fatalf("other IP = %d: buckets not independent", other.Code)
	}

	// X-Forwarded-For must not steer the key (spoofable).
	spoofed := req("198.51.100.7")
	spoofed.Header.Set("X-Forwarded-For", "203.0.113.99")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, spoofed)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("spoofed XFF escaped the drained bucket: %d", rec.Code)
	}
}

// TestLoginLimit: only the password submit is throttled; the device-code
// token poll and the login PAGE stay free.
func TestLoginLimit(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := loginLimit(ratelimit.New(), 1, next)

	post := func(path string) int {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader("user=a&pass=b"))
		r.RemoteAddr = "198.51.100.9:40000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	if got := post("/oidc/device"); got != http.StatusOK {
		t.Fatalf("first login = %d", got)
	}
	if got := post("/oidc/device"); got != http.StatusTooManyRequests {
		t.Fatalf("second rapid login = %d, want 429", got)
	}
	if got := post("/oidc/token"); got != http.StatusOK {
		t.Fatalf("token poll throttled: %d", got)
	}
	page := httptest.NewRequest(http.MethodGet, "/oidc/device", nil)
	page.RemoteAddr = "198.51.100.9:40000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, page)
	if rec.Code != http.StatusOK {
		t.Fatalf("login page throttled: %d", rec.Code)
	}
}

// TestMetricsAuth: unset token = open (private-network posture); set token
// demands the exact bearer.
func TestMetricsAuth(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	open := httptest.NewRecorder()
	metricsAuth("", next).ServeHTTP(open, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if open.Code != http.StatusOK {
		t.Fatalf("open metrics = %d", open.Code)
	}

	h := metricsAuth("s3cret-scrape-token", next)
	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"missing", "", http.StatusUnauthorized},
		{"wrong", "Bearer nope", http.StatusUnauthorized},
		{"prefix-only", "Bearer s3cret-scrape", http.StatusUnauthorized},
		{"right", "Bearer s3cret-scrape-token", http.StatusOK},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		if tc.header != "" {
			r.Header.Set("Authorization", tc.header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}

// TestApproverPerIPLimitE2E proves the wiring, not just the middleware: a
// booted app with the throttle on answers the second rapid hit on the
// dedicated approver listener with 429, over real TLS.
func TestApproverPerIPLimitE2E(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t, func(cfg *config.Config) {
		cfg.Server.ApproverTLS.AutoMint = ptrBool(true)
		cfg.Server.ApproverTLS.Listen = "127.0.0.1:0"
		// Near-zero refill (burst still 1): the drained bucket STAYS drained
		// however slowly a loaded -race run schedules the second request.
		cfg.Server.ApproverTLS.PerIPRPS = 0.0001
	})
	certPEM, err := os.ReadFile(app.cfg.Server.ApproverTLS.CertFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12,
	}}}
	url := "https://" + app.approverLn.Addr().String() + "/readyz"

	first, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first /readyz = %d", first.StatusCode)
	}
	second, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second rapid /readyz = %d, want 429", second.StatusCode)
	}
}

// TestSecurityHeadersGlobal pins the browser-hardening headers on every
// route class: an operational probe, a /v1 API answer, the approvals SPA
// (which hosts the Approve button), and the built-in issuer's login page.
// The four headers are the baseline; the full resource CSP is not set
// here.
func TestSecurityHeadersGlobal(t *testing.T) {
	t.Parallel()
	_, base := testApp(t)
	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "no-referrer",
		"Content-Security-Policy": "frame-ancestors 'none'",
	}
	for _, path := range []string{"/healthz", "/version", "/self-service/", "/oidc/device"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		for h, v := range want {
			if got := resp.Header.Get(h); got != v {
				t.Errorf("%s: %s = %q, want %q", path, h, got, v)
			}
		}
	}
}
