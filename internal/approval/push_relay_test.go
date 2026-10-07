package approval

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// relayTestServer stands in for push.straza.ai: anonymous register plus the
// push face, recording what it receives so tests can pin the wire shape
// (push service README v1: unknown fields are a 400, so the shape IS the
// contract).
type relayTestServer struct {
	srv          *httptest.Server
	registerHits int
	pushHits     int
	lastAuth     string
	lastBody     map[string]any
	pushStatus   int
	pushResponse map[string]any
}

func newRelayTestServer(t *testing.T) *relayTestServer {
	rs := &relayTestServer{pushStatus: http.StatusOK,
		pushResponse: map[string]any{"delivered": true, "downstream_status": 200, "prune": false}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/register", func(w http.ResponseWriter, _ *http.Request) {
		rs.registerHits++
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "wpt_test1"})
	})
	mux.HandleFunc("POST /v1/push", func(w http.ResponseWriter, r *http.Request) {
		rs.pushHits++
		rs.lastAuth = r.Header.Get("Authorization")
		rs.lastBody = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&rs.lastBody)
		if rs.lastAuth != "Bearer wpt_test1" && rs.pushStatus == http.StatusOK {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unknown token"})
			return
		}
		w.WriteHeader(rs.pushStatus)
		_ = json.NewEncoder(w).Encode(rs.pushResponse)
	})
	rs.srv = httptest.NewServer(mux)
	t.Cleanup(rs.srv.Close)
	return rs
}

func testRelayCfg(t *testing.T, url string) config.RelayPush {
	t.Helper()
	return config.RelayPush{Enabled: true, URL: url,
		TokenFile: filepath.Join(t.TempDir(), "relay-token")}
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestRelayTokenMintAndReuse: a missing token file is minted anonymously at
// construction (0600) and reused on the next boot without a second register.
func TestRelayTokenMintAndReuse(t *testing.T) {
	rs := newRelayTestServer(t)
	cfg := testRelayCfg(t, rs.srv.URL)
	httpc := &http.Client{Timeout: pushTimeout}

	r1, err := newRelaySender(cfg, httpc, discardLog())
	if err != nil {
		t.Fatalf("newRelaySender: %v", err)
	}
	_ = r1
	if rs.registerHits != 1 {
		t.Fatalf("register hits = %d, want 1 (boot mint)", rs.registerHits)
	}
	raw, err := os.ReadFile(cfg.TokenFile)
	if err != nil || string(raw) != "wpt_test1" {
		t.Fatalf("token file = %q err %v, want the minted token persisted", raw, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(cfg.TokenFile); fi != nil && fi.Mode().Perm() != 0o600 {
			t.Errorf("token file mode = %v, want 0600", fi.Mode().Perm())
		}
	}

	if _, err := newRelaySender(cfg, httpc, discardLog()); err != nil {
		t.Fatalf("second newRelaySender: %v", err)
	}
	if rs.registerHits != 1 {
		t.Errorf("register hits after reuse = %d, want still 1", rs.registerHits)
	}
}

// TestRelaySendShapePinned: the push body is EXACTLY {platform, route,
// envelope{v,ref,kind}, expires_at} with the bearer token, nothing else (the
// relay 400s unknown fields, and the envelope is the privacy contract).
func TestRelaySendShapePinned(t *testing.T) {
	rs := newRelayTestServer(t)
	r, err := newRelaySender(testRelayCfg(t, rs.srv.URL), &http.Client{Timeout: pushTimeout}, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(90 * time.Second).Truncate(time.Second)
	res, err := r.send(t.Context(), "apns", "devtoken123", pushKindDecide, "ref-1", exp)
	if err != nil || !res.delivered {
		t.Fatalf("send: res=%+v err=%v", res, err)
	}
	if rs.lastAuth != "Bearer wpt_test1" {
		t.Errorf("auth = %q", rs.lastAuth)
	}
	if len(rs.lastBody) != 4 {
		t.Errorf("body has %d keys, want exactly 4: %v", len(rs.lastBody), rs.lastBody)
	}
	if rs.lastBody["platform"] != "apns" || rs.lastBody["route"] != "devtoken123" {
		t.Errorf("platform/route = %v/%v", rs.lastBody["platform"], rs.lastBody["route"])
	}
	env, _ := rs.lastBody["envelope"].(map[string]any)
	if len(env) != 3 || env["v"] != float64(1) || env["ref"] != "ref-1" || env["kind"] != "decide" {
		t.Errorf("envelope = %v, want exactly {v:1, ref, kind}", env)
	}
	if got := rs.lastBody["expires_at"]; got != float64(exp.Unix()) {
		t.Errorf("expires_at = %v, want %d", got, exp.Unix())
	}
}

// TestRelaySendZeroExpiry: a zero exp maps to expires_at 0 (relay default
// window), never a negative or garbage epoch.
func TestRelaySendZeroExpiry(t *testing.T) {
	rs := newRelayTestServer(t)
	r, err := newRelaySender(testRelayCfg(t, rs.srv.URL), &http.Client{Timeout: pushTimeout}, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.send(t.Context(), "fcm", "tok", pushKindStatus, "ref-2", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got := rs.lastBody["expires_at"]; got != float64(0) {
		t.Errorf("expires_at = %v, want 0 for a zero expiry", got)
	}
}

// TestRelaySendPruneVerdict: a completed forward with prune:true surfaces
// prune to the caller with NO error masking (the relay answered 200; the
// downstream refusal is the verdict, per the error-origin discipline).
func TestRelaySendPruneVerdict(t *testing.T) {
	rs := newRelayTestServer(t)
	rs.pushResponse = map[string]any{"delivered": false, "downstream_status": 410,
		"reason": "Unregistered", "prune": true}
	r, err := newRelaySender(testRelayCfg(t, rs.srv.URL), &http.Client{Timeout: pushTimeout}, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.send(t.Context(), "apns", "deadtoken", pushKindDecide, "ref-3", time.Time{})
	if err != nil {
		t.Fatalf("send: %v (a relay 200 is never a send error)", err)
	}
	if res.delivered || !res.prune || res.downstreamStatus != 410 || res.reason != "Unregistered" {
		t.Errorf("res = %+v, want the downstream verdict surfaced", res)
	}
}

// TestRelayRemintOnUnauthorized: a 401 (revoked or lost token) re-registers
// once and retries with the fresh token instead of going silent until a
// restart.
func TestRelayRemintOnUnauthorized(t *testing.T) {
	rs := newRelayTestServer(t)
	cfg := testRelayCfg(t, rs.srv.URL)
	if err := os.WriteFile(cfg.TokenFile, []byte("wpt_stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := newRelaySender(cfg, &http.Client{Timeout: pushTimeout}, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.send(t.Context(), "apns", "tok", pushKindDecide, "ref-4", time.Time{})
	if err != nil || !res.delivered {
		t.Fatalf("send after remint: res=%+v err=%v", res, err)
	}
	if rs.registerHits != 1 {
		t.Errorf("register hits = %d, want 1 (the remint)", rs.registerHits)
	}
	if raw, _ := os.ReadFile(cfg.TokenFile); string(raw) != "wpt_test1" {
		t.Errorf("token file = %q, want the fresh token persisted", raw)
	}
}

// TestRelayTwoFirstMintsShareOneToken pins that two replicas that start at
// once on one new token file end with one token in the file and both senders
// holding it, and that only the replica that lost the file logs that it uses
// the other's token. The fake relay answers no register until both have
// arrived, so both mints sit between the absence check and the persist.
// TestRelayRemintOnUnauthorized is the positive control: a re-mint after the
// relay refused the held token writes over the file.
func TestRelayTwoFirstMintsShareOneToken(t *testing.T) {
	var mu sync.Mutex
	issued := []string{}
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	go func() {
		defer close(release)
		for range 2 {
			select {
			case <-arrived:
			case <-time.After(5 * time.Second):
				return
			}
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/register", func(w http.ResponseWriter, _ *http.Request) {
		arrived <- struct{}{}
		<-release
		mu.Lock()
		tok := "wpt_replica_" + string(rune('a'+len(issued)))
		issued = append(issued, tok)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": tok})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := testRelayCfg(t, srv.URL)
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	senders := make([]*relaySender, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range senders {
		wg.Go(func() {
			senders[i], errs[i] = newRelaySender(cfg, &http.Client{Timeout: pushTimeout}, log)
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("sender %d: %v", i, err)
		}
	}
	if len(issued) != 2 {
		t.Fatalf("the relay issued %v, want two registers racing", issued)
	}
	raw, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	file := string(raw)
	if file != issued[0] && file != issued[1] {
		t.Fatalf("token file = %q, want one of the issued %v", file, issued)
	}
	for i, s := range senders {
		if got := s.currentToken(); got != file {
			t.Errorf("sender %d holds %q, want the file's %q", i, got, file)
		}
	}
	if n := strings.Count(logs.String(), "another replica registered first"); n != 1 {
		t.Errorf("adoption lines = %d, want exactly 1 from the replica that lost the file:\n%s", n, logs.String())
	}
}

// TestRelayRefusedTokenConvergesOnTheFile pins that two replicas that share a
// token file converge on one token after the relay revokes the shared one:
// the first refused sender re-mints and writes the file, and the second
// refused sender takes the newer token from the file instead of registering
// again.
func TestRelayRefusedTokenConvergesOnTheFile(t *testing.T) {
	var mu sync.Mutex
	issued := map[string]bool{}
	var pushedWith []string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/register", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		tok := "wpt_fresh_" + string(rune('a'+len(issued)))
		issued[tok] = true
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": tok})
	})
	mux.HandleFunc("POST /v1/push", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		ok := issued[tok]
		if ok {
			pushedWith = append(pushedWith, tok)
		}
		mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"delivered": true, "downstream_status": 200})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := testRelayCfg(t, srv.URL)
	if err := os.WriteFile(cfg.TokenFile, []byte("wpt_revoked"), 0o600); err != nil {
		t.Fatal(err)
	}
	senders := make([]*relaySender, 2)
	for i := range senders {
		r, err := newRelaySender(cfg, &http.Client{Timeout: pushTimeout}, discardLog())
		if err != nil {
			t.Fatal(err)
		}
		senders[i] = r
	}
	for i, s := range senders {
		if res, err := s.send(t.Context(), "apns", "tok", pushKindDecide, "ref-c", time.Time{}); err != nil || !res.delivered {
			t.Fatalf("sender %d send: res=%+v err=%v", i, res, err)
		}
	}
	file := readRelayTokenFile(cfg.TokenFile)
	if len(issued) != 1 || !issued[file] {
		t.Fatalf("the relay issued %v and the file holds %q, want one register whose token the file holds", issued, file)
	}
	for i, s := range senders {
		if got := s.currentToken(); got != file {
			t.Errorf("sender %d holds %q, want the file's %q", i, got, file)
		}
	}
	if len(pushedWith) != 2 || pushedWith[0] != file || pushedWith[1] != file {
		t.Errorf("accepted pushes carried %v, want two with %q", pushedWith, file)
	}
}

// TestRelayDispatchFallback: with no direct sender configured, apns and fcm
// registrations route through the relay; with a direct sender present the
// relay is never consulted for that lane (config already boot-blocks the
// ambiguous both-set case).
func TestRelayDispatchFallback(t *testing.T) {
	rs := newRelayTestServer(t)
	p := &pushDelivery{log: discardLog(), httpc: &http.Client{Timeout: pushTimeout}}
	r, err := newRelaySender(testRelayCfg(t, rs.srv.URL), p.httpc, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	p.relay = r
	p.svc = &Service{now: time.Now}
	reg := store.ApproverPushTarget{ApproverPush: store.ApproverPush{DeviceID: "dev-1", Kind: "apns", TokenOrEndpoint: "devtoken-x"}}
	if err := p.sendOne(reg, pushKindDecide, "ref-5", time.Time{}); err != nil {
		t.Fatalf("sendOne via relay: %v", err)
	}
	if rs.pushHits != 1 {
		t.Errorf("relay push hits = %d, want 1", rs.pushHits)
	}
}
