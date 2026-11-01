package server

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// TestLoginLimit pins that only the password submits of /oidc/device and
// /oidc/authorize are throttled, from one budget per client address, and
// that the token poll and the login pages stay free. Device sign-in starts
// are throttled from a second budget, and both limits are inert at zero.
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
	if got := post("/oidc/authorize"); got != http.StatusTooManyRequests {
		t.Fatalf("authorize submit after a drained bucket = %d, want 429 from the shared budget", got)
	}
	for _, path := range []string{"/oidc/device", "/oidc/authorize"} {
		page := httptest.NewRequest(http.MethodGet, path, nil)
		page.RemoteAddr = "198.51.100.9:40000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, page)
		if rec.Code != http.StatusOK {
			t.Fatalf("login page %s throttled: %d", path, rec.Code)
		}
	}

	// The authorization endpoint's password submit is throttled on its own.
	fresh := loginLimit(ratelimit.New(), 1, next)
	for i, want := range []int{http.StatusOK, http.StatusTooManyRequests} {
		r := httptest.NewRequest(http.MethodPost, "/oidc/authorize", strings.NewReader("user=a&pass=b"))
		r.RemoteAddr = "198.51.100.10:40000"
		rec := httptest.NewRecorder()
		fresh.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Fatalf("authorize submit %d = %d, want %d", i+1, rec.Code, want)
		}
	}

	// Device sign-in starts draw on a budget of their own, chained the way
	// the server wires both limits. A near-zero rate keeps a drained bucket
	// drained however slowly a loaded run gets to the next step.
	type step struct {
		path string
		want int
	}
	const start = "/oidc/device_authorization"
	for _, tc := range []struct {
		name  string
		rps   float64
		steps []step
	}{
		{name: "a device start is throttled", rps: 0.0001, steps: []step{
			{start, http.StatusOK}, {start, http.StatusTooManyRequests},
		}},
		{name: "device starts leave the password budget whole", rps: 0.0001, steps: []step{
			{start, http.StatusOK}, {start, http.StatusTooManyRequests},
			{"/oidc/device", http.StatusOK}, {"/oidc/authorize", http.StatusTooManyRequests},
		}},
		{name: "password submits leave the device start budget whole", rps: 0.0001, steps: []step{
			{"/oidc/device", http.StatusOK}, {"/oidc/authorize", http.StatusTooManyRequests},
			{start, http.StatusOK}, {start, http.StatusTooManyRequests},
		}},
		{name: "zero is inert", rps: 0, steps: []step{
			{start, http.StatusOK}, {start, http.StatusOK}, {start, http.StatusOK},
			{"/oidc/device", http.StatusOK}, {"/oidc/device", http.StatusOK},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := deviceStartLimit(ratelimit.New(), tc.rps, loginLimit(ratelimit.New(), tc.rps, next))
			for i, s := range tc.steps {
				r := httptest.NewRequest(http.MethodPost, s.path, strings.NewReader("client_id=strazactl"))
				r.RemoteAddr = "198.51.100.11:40000"
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, r)
				if rec.Code != s.want {
					t.Fatalf("step %d, POST %s = %d, want %d", i+1, s.path, rec.Code, s.want)
				}
				if s.path == start && s.want == http.StatusTooManyRequests {
					if got := rec.Header().Get("Retry-After"); got != "1" {
						t.Errorf("step %d: Retry-After = %q, want 1", i+1, got)
					}
					if !strings.Contains(rec.Body.String(), deviceStartsRefused) {
						t.Errorf("step %d: body = %s, want the sentence %q", i+1, rec.Body, deviceStartsRefused)
					}
				}
			}
		})
	}
}

// deviceStartsRefused is the sentence a throttled device sign-in start
// answers with.
const deviceStartsRefused = "Too many sign-in starts from this address. Wait a few seconds and sign in again."

// TestDeviceStartLimitE2E proves the wiring on both profiles: a booted app
// throttles a second rapid device sign-in start from one address, and the
// password submit after it still has its own budget.
func TestDeviceStartLimitE2E(t *testing.T) {
	t.Parallel()
	for _, profile := range []string{config.ProfileStandalone, config.ProfileEnterprise} {
		t.Run(profile, func(t *testing.T) {
			t.Parallel()
			_, base := testApp(t, func(cfg *config.Config) {
				cfg.Profile = profile
				cfg.Server.LoginPerIPRPS = 0.0001
			})
			post := func(path string, form url.Values) (int, string) {
				t.Helper()
				resp, err := http.PostForm(base+path, form)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = resp.Body.Close() }()
				body, _ := io.ReadAll(resp.Body)
				return resp.StatusCode, string(body)
			}
			startForm := url.Values{"client_id": {"strazactl"}}
			if code, body := post("/oidc/device_authorization", startForm); code != http.StatusOK {
				t.Fatalf("first start = %d %s, want 200", code, body)
			}
			if code, body := post("/oidc/device_authorization", startForm); code != http.StatusTooManyRequests || !strings.Contains(body, deviceStartsRefused) {
				t.Fatalf("second rapid start = %d %s, want 429 with the sentence", code, body)
			}
			submit := url.Values{"user_code": {"XXXX-XXXX"}, "username": {"kim"}, "password": {"guess"}}
			if code, body := post("/oidc/device", submit); code != http.StatusOK {
				t.Fatalf("password submit after the starts = %d %s, want 200 from its own budget", code, body)
			}
			if code, _ := post("/oidc/device", submit); code != http.StatusTooManyRequests {
				t.Fatalf("second rapid password submit = %d, want 429", code)
			}
		})
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
