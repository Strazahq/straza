package agentguard_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard"
)

// fakeIssuer is the minimal RFC 8628 issuer + enroll surface Enroll touches:
// idp discovery (a 404 sends oidcflow to the built-in issuer paths, so no
// openid-configuration is needed), device authorization, token polling,
// /v1/enroll and the snapshot keys. tokenSeq is the answer for each token
// poll: a non-empty string is an OAuth `error` returned as the wire's HTTP
// 400, "" mints an id_token; the last entry repeats. tokenHits counts polls
// so a test can assert the loop never sleeps before the first one.
type fakeIssuer struct {
	tokenHits int64 // atomic; first field for 64-bit alignment
	interval  int
	expiresIn int
	tokenSeq  []string
	// enrollKind is the client_kind the enrol body carried, "" until then.
	enrollKind string
}

func (f *fakeIssuer) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	// Older-server discovery: a 404 sends oidcflow to the built-in issuer paths.
	mux.HandleFunc("GET /.well-known/straza/idp.json", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("POST /oidc/device_authorization", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"device_code":"dc-1","user_code":"WXYZ-2345","verification_uri_complete":"https://issuer.example/device?code=WXYZ-2345","expires_in":%d,"interval":%d}`,
			f.expiresIn, f.interval)
	})
	mux.HandleFunc("POST /oidc/token", func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt64(&f.tokenHits, 1)
		idx := int(n - 1)
		if idx >= len(f.tokenSeq) {
			idx = len(f.tokenSeq) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		if e := f.tokenSeq[idx]; e != "" {
			w.WriteHeader(http.StatusBadRequest) // pending/slow_down are HTTP 400 on the wire
			fmt.Fprintf(w, `{"error":%q}`, e)
			return
		}
		fmt.Fprint(w, `{"id_token":"id-token-xyz"}`)
	})
	mux.HandleFunc("POST /v1/enroll", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ClientKind string `json:"client_kind"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.enrollKind = body.ClientKind
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"user_id":"u-1","username":"kim","device_id":"dev-1","device_token":"dtok-1","device_token_expires_in":86400}`)
	})
	mux.HandleFunc("GET /.well-known/straza/snapshot-keys.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"keys":[{"kid":"k1","key":"AAAA"}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestEnrollReturnsOnFirstPoll pins poll-first for enroll: with the grant
// already approved and a large advertised interval, the loop finishes well
// under one interval. A wait-then-poll loop would sleep the full 5 s first.
func TestEnrollReturnsOnFirstPoll(t *testing.T) {
	f := &fakeIssuer{interval: 5, expiresIn: 600, tokenSeq: []string{""}}
	srv := f.server(t)
	t.Setenv("STRAZA_HOME", t.TempDir())
	store, err := agentguard.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out := &syncBuf{}
	start := time.Now()
	if err := agentguard.Enroll(ctx, store, srv.URL, out); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if d := time.Since(start); d >= 2*time.Second {
		t.Fatalf("Enroll took %v; it waited before the first poll (interval 5 s)", d)
	}
	if got := atomic.LoadInt64(&f.tokenHits); got != 1 {
		t.Fatalf("token polls = %d, want exactly 1 (approved before the first poll)", got)
	}
	if _, err := store.LoadIdentity(); err != nil {
		t.Fatalf("identity not persisted: %v", err)
	}
	if f.enrollKind != "kit" {
		t.Fatalf("enrol client_kind = %q, want kit (the kit names itself)", f.enrollKind)
	}
}

// TestEnrollPreCancelledContextPollsNothing pins the fail-closed guard: an
// already-cancelled context returns context.Canceled and makes no token poll.
func TestEnrollPreCancelledContextPollsNothing(t *testing.T) {
	f := &fakeIssuer{interval: 5, expiresIn: 600, tokenSeq: []string{""}}
	srv := f.server(t)
	t.Setenv("STRAZA_HOME", t.TempDir())
	store, err := agentguard.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the flow starts

	out := &syncBuf{}
	err = agentguard.Enroll(ctx, store, srv.URL, out)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := atomic.LoadInt64(&f.tokenHits); got != 0 {
		t.Fatalf("token polls = %d on a cancelled context, want 0 (fail closed, no HTTP)", got)
	}
}

// TestEnrollPendingThenApproved keeps the between-polls wait honest: the first
// poll is pending, the approval lands on the second, so the loop must sit on
// its bottom wait once (interval 1 s): ≥2 polls, ≥1 interval of wall time.
func TestEnrollPendingThenApproved(t *testing.T) {
	f := &fakeIssuer{interval: 1, expiresIn: 600, tokenSeq: []string{"authorization_pending", ""}}
	srv := f.server(t)
	t.Setenv("STRAZA_HOME", t.TempDir())
	store, err := agentguard.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out := &syncBuf{}
	start := time.Now()
	if err := agentguard.Enroll(ctx, store, srv.URL, out); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if got := atomic.LoadInt64(&f.tokenHits); got < 2 {
		t.Fatalf("token polls = %d, want ≥2 (pending then approved)", got)
	}
	if d := time.Since(start); d < 1*time.Second {
		t.Fatalf("Enroll took %v; no wait between polls (interval 1 s)", d)
	}
	if _, err := store.LoadIdentity(); err != nil {
		t.Fatalf("identity not persisted: %v", err)
	}
}
