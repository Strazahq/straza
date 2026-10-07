package agentguard

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// sseHandler streams ready + whatever events arrive on send until the client
// disconnects. It asserts the bearer token, so the tests prove the daemon
// authenticates the lane.
func sseHandler(t *testing.T, wantToken string, send <-chan string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/push" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantToken {
			t.Errorf("push auth = %q", got)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "retry: 3000\n\nevent: ready\ndata: {\"subjects\":3}\n\n")
		fl.Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case ev := <-send:
				_, _ = fmt.Fprintf(w, "event: %s\ndata: {}\n\n", ev)
				fl.Flush()
			}
		}
	}
}

// TestDaemonEdgePushKill pins the client half of edge push: with NO client-reachable
// NATS anywhere, a revocation event on the daemon's SSE stream drops session
// state and exits the daemon inside the push budget.
func TestDaemonEdgePushKill(t *testing.T) {
	send := make(chan string, 1)
	srv := httptest.NewServer(sseHandler(t, "tok-edge", send))
	defer srv.Close()

	store := daemonStore(t)
	if err := store.SaveConfig(Config{ServerURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(Session{
		SessionID: "sess-edge", SessionToken: "tok-edge",
		Harness: "claude-code/2.1.0", ExpiresAt: time.Now().Add(time.Hour),
		// The edge lane is the only push path.
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- NewDaemon(store, nil).Run(ctx) }()

	send <- "revocation"
	deadline := time.Now().Add(4 * time.Second)
	for {
		if _, err := store.LoadSession(); err != nil {
			break // state dropped
		}
		if time.Now().After(deadline) {
			t.Fatal("session state never dropped after the edge revocation event")
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("daemon did not exit after revocation")
	}
	if _, err := store.LoadRevocation(); err != nil {
		t.Errorf("revocation marker missing (hooks need the WHY): %v", err)
	}
}

// TestEdgePushPolicyNudge: a policy event only nudges. The subscriber
// signals and KEEPS streaming; nothing is dropped.
func TestEdgePushPolicyNudge(t *testing.T) {
	send := make(chan string, 2)
	srv := httptest.NewServer(sseHandler(t, "tok-n", send))
	defer srv.Close()

	revoked := make(chan struct{}, 1)
	nudged := make(chan struct{}, 1)
	ep := newEdgePush(srv.URL, func() (string, bool) { return "tok-n", true }, revoked, nudged, nil)
	ep.out = testWriter{t}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ep.run(ctx)

	send <- "policy"
	select {
	case <-nudged:
	case <-time.After(3 * time.Second):
		t.Fatal("policy event never nudged")
	}
	select {
	case <-revoked:
		t.Fatal("policy nudge must never signal revocation")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestEdgePushPre31Server: a server without /v1/push (404) parks the lane for
// good, with no retry storm against an endpoint that will never exist.
func TestEdgePushPre31Server(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	ep := newEdgePush(srv.URL, func() (string, bool) { return "tok", true },
		make(chan struct{}, 1), make(chan struct{}, 1), testWriter{t})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan struct{})
	go func() { ep.run(ctx); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		t.Fatal("run did not park on 404")
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("expected exactly one probe against a pre-3.1 server, got %d", n)
	}
}

// TestEdgePushReconnect: a refused connect backs off and retries; the lane
// comes live once the server does.
func TestEdgePushReconnect(t *testing.T) {
	var attempts atomic.Int32
	send := make(chan string, 1)
	inner := sseHandler(t, "tok-r", send)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		inner(w, r)
	}))
	defer srv.Close()

	revoked := make(chan struct{}, 1)
	ep := newEdgePush(srv.URL, func() (string, bool) { return "tok-r", true },
		revoked, make(chan struct{}, 1), testWriter{t})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ep.run(ctx)

	// Wait for the second (successful) connect, then deliver the kill.
	deadline := time.Now().Add(5 * time.Second)
	for attempts.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("no reconnect after 503")
		}
		time.Sleep(20 * time.Millisecond)
	}
	send <- "revocation"
	select {
	case <-revoked:
	case <-time.After(3 * time.Second):
		t.Fatal("revocation not delivered after reconnect")
	}
}

// testWriter routes daemon status lines into the test log.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) { w.t.Log(string(p)); return len(p), nil }
