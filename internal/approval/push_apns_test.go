package approval

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// --- harness ---

func testAPNSConfig() config.APNSPush {
	return config.APNSPush{
		KeyFile: "/unused-in-fromKey-tests",
		KeyID:   "ABC123DEFG",
		TeamID:  "TEAM567890",
		Topic:   "ai.straza.approver",
	}
}

func newTestAPNSSender(t *testing.T) *apnsSender {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := newAPNSSenderFromKey(key, testAPNSConfig())
	if err != nil {
		t.Fatalf("newAPNSSenderFromKey: %v", err)
	}
	return s
}

type apnsCapture struct {
	path   string
	header http.Header
	body   []byte
	proto  int
}

// newAPNSTestServer is a real HTTP/2 TLS server (the only protocol APNs
// speaks); respond decides the status/body per hit, captures land on ch.
func newAPNSTestServer(t *testing.T, respond func(hit int, w http.ResponseWriter)) (*httptest.Server, chan apnsCapture) {
	t.Helper()
	ch := make(chan apnsCapture, 16)
	hits := 0
	var mu sync.Mutex
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		n := hits
		mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		ch <- apnsCapture{path: r.URL.Path, header: r.Header.Clone(), body: b, proto: r.ProtoMajor}
		respond(n, w)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, ch
}

func attachAPNSServer(s *apnsSender, srv *httptest.Server) {
	s.host = srv.URL
	s.httpc = srv.Client()
}

// writeAPNSKeyPEM writes a P-256 PKCS#8 PEM (the .p8 shape Apple mints) into
// a temp dir and returns its path.
func writeAPNSKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "AuthKey_TEST.p8")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// --- 1. ES256 signature shape (the intermittent-403 trap) ---

// TestAPNSProviderJWTRawRS64: the provider-token signature must be raw R||S,
// exactly 64 bytes with leading zeros preserved (RFC 7518 3.4). SignASN1
// output (69-72 bytes) or r.Bytes() without FillBytes (short ~1/256 of the
// time) both yield INTERMITTENT InvalidProviderToken refusals, which is why
// this asserts shape across hundreds of signatures and verifies each one
// independently of the encoding that produced it.
func TestAPNSProviderJWTRawRS64(t *testing.T) {
	s := newTestAPNSSender(t)
	for i := 0; i < 300; i++ {
		token, err := s.signProviderJWT(int64(1754500000 + i))
		if err != nil {
			t.Fatalf("signProviderJWT: %v", err)
		}
		h, c, sig, input := splitJWT(t, token)
		if len(sig) != 64 {
			t.Fatalf("signature length = %d, want exactly 64 (raw R||S)", len(sig))
		}
		r := new(big.Int).SetBytes(sig[:32])
		ss := new(big.Int).SetBytes(sig[32:])
		digest := sha256.Sum256([]byte(input))
		if !ecdsa.Verify(&s.key.PublicKey, digest[:], r, ss) {
			t.Fatalf("signature %d does not verify from its raw halves", i)
		}
		if h["alg"] != "ES256" || h["kid"] != "ABC123DEFG" {
			t.Fatalf("header = %v, want alg ES256 + kid", h)
		}
		if c["iss"] != "TEAM567890" {
			t.Fatalf("claims = %v, want iss = team id", c)
		}
		if int64(c["iat"].(float64)) != int64(1754500000+i) {
			t.Fatalf("iat = %v, want %d", c["iat"], 1754500000+i)
		}
	}
}

func splitJWT(t *testing.T, token string) (header, claims map[string]any, sig []byte, signingInput string) {
	t.Helper()
	parts := regexp.MustCompile(`\.`).Split(token, -1)
	if len(parts) != 3 {
		t.Fatalf("jwt has %d parts", len(parts))
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	sig, err = base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	header, claims = map[string]any{}, map[string]any{}
	if err := json.Unmarshal(hb, &header); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(cb, &claims); err != nil {
		t.Fatal(err)
	}
	return header, claims, sig, parts[0] + "." + parts[1]
}

// --- 2. headers + payload per kind ---

// TestAPNSSendHeadersAndPayloadPerKind pins the whole wire shape: a decide
// push carries the request's expiry and the time-sensitive interruption
// level; a status push rides the default level with the 24h expiration; both
// omit apns-collapse-id (collapse would silently REPLACE an older pending
// approval on the lock screen) and carry only generic copy plus the opaque
// {v,ref,kind} envelope as peers of aps.
func TestAPNSSendHeadersAndPayloadPerKind(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	exp := now.Add(90 * time.Second)

	srv, ch := newAPNSTestServer(t, func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusOK) })

	cases := []struct {
		kind           string
		exp            time.Time
		wantExpiration int64
		wantTimeSens   bool
		wantTitle      string
	}{
		{pushKindDecide, exp, exp.Unix(), true, "Approval requested"},
		{pushKindStatus, exp, now.Add(24 * time.Hour).Unix(), false, "Request update"},
		{pushKindDecide, time.Time{}, now.Add(24 * time.Hour).Unix(), true, "Approval requested"},
	}
	for _, tc := range cases {
		t.Run(tc.kind+"/"+strconv.FormatBool(tc.exp.IsZero()), func(t *testing.T) {
			s := newTestAPNSSender(t)
			s.now = func() time.Time { return now }
			attachAPNSServer(s, srv)

			res, err := s.send(context.Background(), "d3adb33f", tc.kind, "apr-1", tc.exp)
			if err != nil || res.status != http.StatusOK {
				t.Fatalf("send = %+v, %v", res, err)
			}
			cap := <-ch
			if cap.proto != 2 {
				t.Fatalf("request went over HTTP/%d, APNs requires HTTP/2", cap.proto)
			}
			if cap.path != "/3/device/d3adb33f" {
				t.Errorf("path = %q", cap.path)
			}
			h := cap.header
			if h.Get("apns-topic") != "ai.straza.approver" || h.Get("apns-push-type") != "alert" || h.Get("apns-priority") != "10" {
				t.Errorf("topic/push-type/priority = %q/%q/%q", h.Get("apns-topic"), h.Get("apns-push-type"), h.Get("apns-priority"))
			}
			if got := h.Get("apns-expiration"); got != strconv.FormatInt(tc.wantExpiration, 10) {
				t.Errorf("apns-expiration = %q, want %d", got, tc.wantExpiration)
			}
			if _, err := uuid.Parse(h.Get("apns-id")); err != nil {
				t.Errorf("apns-id = %q, want a UUID", h.Get("apns-id"))
			}
			if h.Get("apns-id") != res.id {
				t.Errorf("result id %q != sent header %q (log correlation)", res.id, h.Get("apns-id"))
			}
			if _, present := h["Apns-Collapse-Id"]; present {
				t.Error("apns-collapse-id must be ABSENT (collapse hides pending approvals)")
			}
			if got := h.Get("Authorization"); got == "" || got[:7] != "bearer " {
				t.Errorf("authorization = %q, want bearer provider token", got)
			}

			var body map[string]any
			if err := json.Unmarshal(cap.body, &body); err != nil {
				t.Fatalf("body: %v", err)
			}
			if len(body) != 4 || body["v"] != float64(1) || body["ref"] != "apr-1" || body["kind"] != tc.kind {
				t.Errorf("top-level keys = %v, want exactly aps + v/ref/kind", body)
			}
			aps, _ := body["aps"].(map[string]any)
			level, hasLevel := aps["interruption-level"]
			if tc.wantTimeSens && (level != "time-sensitive" || !hasLevel) {
				t.Errorf("decide push interruption-level = %v, want time-sensitive", level)
			}
			if !tc.wantTimeSens && hasLevel {
				t.Errorf("status push must NOT carry an interruption-level (misuse burns the privilege), got %v", level)
			}
			wantAPSKeys := 2
			if tc.wantTimeSens {
				wantAPSKeys = 3
			}
			if len(aps) != wantAPSKeys || aps["sound"] != "default" {
				t.Errorf("aps = %v, want exactly alert+sound(+interruption-level)", aps)
			}
			alert, _ := aps["alert"].(map[string]any)
			title, _ := alert["title"].(string)
			bodyText, _ := alert["body"].(string)
			if title != tc.wantTitle || bodyText == "" || len(alert) != 2 {
				t.Errorf("alert = %v, want generic title %q + body only", alert, tc.wantTitle)
			}
			// Privacy pin: nothing request-derived may reach a lock screen.
			if regexp.MustCompile(`apr-1`).MatchString(title + bodyText) {
				t.Error("alert copy leaks the approval reference")
			}
		})
	}
}

// --- 3. provider-token bucket ---

// TestAPNSJWTBucket: minting is tracked globally per team (DTS 702056), so
// the token refreshes on a deterministic wall-clock bucket: sends inside one
// bucket reuse the token, a rollover re-mints, and concurrent callers
// single-flight through the mint.
func TestAPNSJWTBucket(t *testing.T) {
	s := newTestAPNSSender(t)
	base := time.Date(2026, 8, 7, 12, 1, 0, 0, time.UTC)
	current := base
	var mu sync.Mutex
	s.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return current }

	tok1, err := s.providerToken()
	if err != nil {
		t.Fatal(err)
	}
	// Same bucket: reused verbatim, no second mint.
	mu.Lock()
	current = base.Add(10 * time.Minute)
	mu.Unlock()
	tok2, err := s.providerToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok1 != tok2 {
		t.Error("token re-minted inside one bucket (per-request minting earns 429 TooManyProviderTokenUpdates)")
	}
	// Bucket rollover: re-minted.
	mu.Lock()
	current = base.Add(apnsJWTBucket)
	mu.Unlock()
	tok3, err := s.providerToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok3 == tok1 {
		t.Error("token not re-minted after the bucket rolled over")
	}

	// Concurrent callers land on ONE token.
	tokens := make(chan string, 32)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := s.providerToken()
			if err != nil {
				t.Error(err)
				return
			}
			tokens <- tok
		}()
	}
	wg.Wait()
	close(tokens)
	for tok := range tokens {
		if tok != tok3 {
			t.Fatal("concurrent providerToken calls returned different tokens")
		}
	}
}

// --- 4. error mapping ---

// TestAPNSClassify: handling branches on the reason string, never the status
// alone (the two 429s demand opposite fixes). The never-retry set drops, the
// dead-token set prunes, ExpiredProviderToken re-mints, 5xx waits for the
// next natural push.
func TestAPNSClassify(t *testing.T) {
	cases := []struct {
		status int
		reason string
		want   apnsAction
	}{
		{410, "Unregistered", apnsPrune},
		{400, "BadDeviceToken", apnsPrune},
		{400, "DeviceTokenNotForTopic", apnsPrune},
		{400, "ExpiredToken", apnsPrune},
		{403, "ExpiredProviderToken", apnsRemint},
		{413, "PayloadTooLarge", apnsDrop},
		{403, "Forbidden", apnsDrop},
		{403, "InvalidProviderToken", apnsDrop},
		// The 429 twins: same status, opposite handling. A token-updates 429
		// means OUR jwt cache is broken (fix the code, dropping is all a
		// single send can do); a requests 429 backs off the device token.
		{429, "TooManyProviderTokenUpdates", apnsDrop},
		{429, "TooManyRequests", apnsRetryLater},
		{500, "InternalServerError", apnsRetryLater},
		{503, "ServiceUnavailable", apnsRetryLater},
		{503, "Shutdown", apnsRetryLater},
		{400, "BadCollapseId", apnsDrop},
		{400, "", apnsDrop},
	}
	for _, tc := range cases {
		if got := classifyAPNS(tc.status, tc.reason); got != tc.want {
			t.Errorf("classifyAPNS(%d, %q) = %v, want %v", tc.status, tc.reason, got, tc.want)
		}
	}
}

// TestAPNSExpiredProviderTokenRetriesOnce: a refused provider token re-mints
// and retries exactly once. A second refusal is surfaced, not looped on.
func TestAPNSExpiredProviderTokenRetriesOnce(t *testing.T) {
	refuse := func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"reason":"ExpiredProviderToken"}`))
	}
	srv, ch := newAPNSTestServer(t, func(hit int, w http.ResponseWriter) {
		if hit == 1 {
			refuse(w)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	s := newTestAPNSSender(t)
	attachAPNSServer(s, srv)
	res, err := s.send(context.Background(), "tok", pushKindDecide, "apr-1", time.Time{})
	if err != nil || res.status != http.StatusOK {
		t.Fatalf("send after re-mint = %+v, %v; want 200", res, err)
	}
	first, second := <-ch, <-ch
	if first.header.Get("Authorization") == second.header.Get("Authorization") {
		t.Error("retry reused the refused provider token instead of re-minting")
	}

	// Every hit refuses: exactly two attempts, then the caller sees it.
	srv2, ch2 := newAPNSTestServer(t, func(_ int, w http.ResponseWriter) { refuse(w) })
	s2 := newTestAPNSSender(t)
	attachAPNSServer(s2, srv2)
	res, err = s2.send(context.Background(), "tok", pushKindDecide, "apr-1", time.Time{})
	if err != nil || res.reason != "ExpiredProviderToken" {
		t.Fatalf("send = %+v, %v; want the surfaced refusal", res, err)
	}
	if hits := len(ch2); hits != 2 {
		t.Errorf("send attempted %d times, want exactly 2 (one retry)", hits)
	}
}

// --- 5. the 410 prune race guard ---

// TestAPNSPruneGuard: a 410 Unregistered prunes the registration iff the app
// has not re-confirmed it after Apple's invalidation timestamp. The device
// may have re-registered after APNs invalidated an old token; unconditional
// pruning would silently un-ring that phone forever (everything then times
// out into deny, an availability hole no operator sees).
func TestAPNSPruneGuard(t *testing.T) {
	invalidatedAt := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name         string
		registeredAt time.Time
		wantPruned   bool
	}{
		{"registration older than the invalidation prunes", invalidatedAt.Add(-time.Hour), true},
		{"re-registration after the invalidation survives", invalidatedAt.Add(time.Minute), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			srv, _ := newAPNSTestServer(t, func(_ int, w http.ResponseWriter) {
				w.WriteHeader(http.StatusGone)
				_, _ = w.Write([]byte(`{"reason":"Unregistered","timestamp":` + strconv.FormatInt(invalidatedAt.UnixMilli(), 10) + `}`))
			})
			s := newTestAPNSSender(t)
			attachAPNSServer(s, srv)

			dev, err := h.st.Approvers().InsertDevice(ctx, store.ApproverDevice{UserID: "u-1", Name: "iphone"})
			if err != nil {
				t.Fatalf("InsertDevice: %v", err)
			}
			if err := h.st.Approvers().UpsertPush(ctx, store.ApproverPush{
				DeviceID: dev.ID, Kind: "apns", TokenOrEndpoint: "tok-1", RegisteredAt: tc.registeredAt,
			}); err != nil {
				t.Fatalf("UpsertPush: %v", err)
			}
			pd := &pushDelivery{svc: h.svc, st: h.st, log: slog.Default(), apns: s}
			reg := store.ApproverPushTarget{
				ApproverPush: store.ApproverPush{DeviceID: dev.ID, Kind: "apns", TokenOrEndpoint: "tok-1", RegisteredAt: tc.registeredAt},
				UserID:       "u-1",
			}
			if err := pd.sendAPNS(reg, pushKindDecide, "apr-410", time.Time{}); err == nil {
				t.Fatal("a 410 send must report an error")
			}
			targets, err := h.st.Approvers().ListPushTargets(ctx)
			if err != nil {
				t.Fatalf("ListPushTargets: %v", err)
			}
			if pruned := len(targets) == 0; pruned != tc.wantPruned {
				t.Errorf("pruned = %v, want %v (targets: %+v)", pruned, tc.wantPruned, targets)
			}
		})
	}
}

// --- boot validation (never-mint) ---

// TestAPNSSenderBoot: the .p8 loads strictly. A missing file is a boot error
// and is NOT minted (Apple is the only mint; the VAPID generate-once idiom
// deliberately does not apply), bad shapes fail with the fix named, and the
// environment knob picks the endpoint.
func TestAPNSSenderBoot(t *testing.T) {
	cfg := testAPNSConfig()

	// Missing file: boot error, and the file must NOT appear afterward.
	missing := filepath.Join(t.TempDir(), "absent.p8")
	cfg.KeyFile = missing
	if _, err := newAPNSSender(cfg); err == nil {
		t.Fatal("missing key file must fail boot")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a missing .p8 must never be auto-minted")
	}

	// Garbage file.
	garbage := filepath.Join(t.TempDir(), "garbage.p8")
	if err := os.WriteFile(garbage, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.KeyFile = garbage
	if _, err := newAPNSSender(cfg); err == nil {
		t.Fatal("garbage key file must fail boot")
	}

	// An RSA key is not an ES256 signer.
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	rsaPath := filepath.Join(t.TempDir(), "rsa.p8")
	if err := os.WriteFile(rsaPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.KeyFile = rsaPath
	if _, err := newAPNSSender(cfg); err == nil {
		t.Fatal("an RSA key must fail boot (ES256 needs P-256)")
	}

	// The real shape boots; environment picks the endpoint.
	cfg.KeyFile = writeAPNSKeyPEM(t)
	s, err := newAPNSSender(cfg)
	if err != nil {
		t.Fatalf("valid .p8 must boot: %v", err)
	}
	if s.host != apnsHostProduction {
		t.Errorf("default host = %q, want production", s.host)
	}
	cfg.Environment = "sandbox"
	s, err = newAPNSSender(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s.host != apnsHostSandbox {
		t.Errorf("sandbox host = %q", s.host)
	}
}

// --- transport + toolchain gates ---

// TestAPNSTransportPinned: the transport settings are each load-bearing.
// ForceAttemptHTTP2 (APNs speaks h2 only; falling to HTTP/1.1 is an outage,
// not a downgrade), SendPingTimeout (the half-open-connection health check;
// without it a zombie connection hangs the sender while approvals silently
// stop ringing), IdleConnTimeout 0 (one connection serves hours of pushes;
// churn reads as DoS to Apple).
func TestAPNSTransportPinned(t *testing.T) {
	tr := newAPNSTransport()
	if !tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 must be set")
	}
	if tr.HTTP2 == nil || tr.HTTP2.SendPingTimeout < 15*time.Second || tr.HTTP2.SendPingTimeout > 30*time.Second {
		t.Errorf("HTTP2.SendPingTimeout = %+v, want 15-30s", tr.HTTP2)
	}
	if tr.IdleConnTimeout != 0 {
		t.Errorf("IdleConnTimeout = %s, want 0 (keep the connection)", tr.IdleConnTimeout)
	}
}

// TestGoDirectiveRequires126: http.Transport's HTTP2Config exists since go
// 1.24 but is INERT until 1.26 (go.dev/issue/67813): on an older toolchain
// the ping health check above compiles, reviews clean, and silently does
// nothing. The go.mod directive is what forbids such a build, so this test
// fails any change that lowers it.
func TestGoDirectiveRequires126(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^go (\d+)\.(\d+)`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("go.mod has no go directive")
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major == 1 && minor < 26 {
		t.Fatalf("go.mod directive is go %s.%s; the APNs sender needs >= 1.26 (HTTP2Config.SendPingTimeout is inert before it)", m[1], m[2])
	}
}

// --- delivery arm wiring ---

// TestAPNSSendOneDisabled: a stored apns registration with the backend off is
// an actionable error on the synchronous channel-test path and a quiet skip
// on the async path (matching the fcm arm).
func TestAPNSSendOneDisabled(t *testing.T) {
	h := newHarness(t)
	pd := &pushDelivery{svc: h.svc, st: h.st, log: slog.Default(), httpc: &http.Client{Timeout: pushTimeout}}
	reg := store.ApproverPushTarget{
		ApproverPush: store.ApproverPush{DeviceID: "apd_1", Kind: "apns", TokenOrEndpoint: "tok"},
		UserID:       "u-1",
	}
	if err := pd.sendOne(reg, pushKindStatus, "apr-1", time.Time{}); err == nil {
		t.Error("sendOne on a disabled apns backend must error (channel test honesty)")
	}
	// The async path must not panic and must not deliver.
	pd.transport = pd.deliverOne
	pd.deliverOne(reg, pushKindDecide, "apr-1", time.Time{})
}

// TestAPNSDeliverOneDispatch: the async fan-out reaches the wire for apns
// registrations once the backend is configured.
func TestAPNSDeliverOneDispatch(t *testing.T) {
	h := newHarness(t)
	srv, ch := newAPNSTestServer(t, func(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusOK) })
	s := newTestAPNSSender(t)
	attachAPNSServer(s, srv)
	pd := &pushDelivery{svc: h.svc, st: h.st, log: slog.Default(), apns: s}
	reg := store.ApproverPushTarget{
		ApproverPush: store.ApproverPush{DeviceID: "apd_1", Kind: "apns", TokenOrEndpoint: "tok"},
		UserID:       "u-1",
	}
	pd.deliverOne(reg, pushKindDecide, "apr-77", time.Time{})
	select {
	case cap := <-ch:
		if cap.path != "/3/device/tok" {
			t.Errorf("path = %q", cap.path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("async apns delivery never reached the wire")
	}
}
