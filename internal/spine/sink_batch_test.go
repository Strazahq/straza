package spine

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/events"
)

// TestWebhookDeliverBatch pins the opt-in NDJSON contract: one POST,
// one CE per line, signature over the whole body, X-Straza-Batch count.
func TestWebhookDeliverBatch(t *testing.T) {
	var gotBody []byte
	var gotHeaders http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotHeaders = r.Header.Clone()
	}))
	defer ts.Close()

	secret := []byte("hunter2")
	s := NewWebhookSink("batchy", ts.URL, secret, nil)
	events := []SinkEvent{
		{Subject: "straza.audit.tool", CE: []byte(`{"id":"a"}`)},
		{Subject: "straza.audit.prompt", CE: []byte(`{"id":"b"}`)},
	}
	if err := s.DeliverBatch(context.Background(), events); err != nil {
		t.Fatalf("DeliverBatch: %v", err)
	}
	want := "{\"id\":\"a\"}\n{\"id\":\"b\"}\n"
	if string(gotBody) != want {
		t.Fatalf("body = %q, want %q", gotBody, want)
	}
	if ct := gotHeaders.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if n := gotHeaders.Get("X-Straza-Batch"); n != "2" {
		t.Fatalf("X-Straza-Batch = %q, want 2", n)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(want))
	if sig := gotHeaders.Get("X-Straza-Signature"); sig != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("signature %q does not cover the whole body", sig)
	}
}

// TestSinkRunnerBatched is the batched end-to-end test: with `batch` opted in, a
// burst of events arrives in few NDJSON POSTs instead of one POST each, a
// failed batch redelivers whole (at-least-once), and nothing is lost.
func TestSinkRunnerBatched(t *testing.T) {
	ns, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1,
		JetStream: true, StoreDir: t.TempDir(),
		NoLog: true, NoSigs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ns.Start()
	t.Cleanup(ns.Shutdown)
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats not ready")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bus, err := events.Start(ctx, config.Config{Events: config.Events{URL: ns.ClientURL()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bus.Close)

	var mu sync.Mutex
	var posts []string
	failFirst := true
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		if failFirst {
			failFirst = false
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		posts = append(posts, string(body))
	}))
	defer ts.Close()

	for i := 0; i < 5; i++ {
		if err := bus.Publish(ctx, "straza.audit.tool", []byte(`{"id":"batch-`+string(rune('a'+i))+`"}`)); err != nil {
			t.Fatal(err)
		}
	}

	runner := NewSinkRunner(bus, NewWebhookSink("bt", ts.URL, nil, nil), "STRAZA_AUDIT",
		[]string{"straza.audit.>"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	runner.Batch = 10
	runner.Policy.BaseDelay = 50 * time.Millisecond
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- runner.Run(runCtx) }()

	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		lines := 0
		for _, p := range posts {
			lines += strings.Count(p, "\n")
		}
		nposts := len(posts)
		mu.Unlock()
		if lines >= 5 {
			if nposts > 2 {
				t.Fatalf("delivered in %d posts, want batched (<=2 after one failure)", nposts)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d lines across %d posts after deadline", lines, nposts)
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The failed first POST redelivered whole: every event id is present.
	mu.Lock()
	defer mu.Unlock()
	all := strings.Join(posts, "")
	for i := 0; i < 5; i++ {
		id := `"batch-` + string(rune('a'+i)) + `"`
		if !strings.Contains(all, id) {
			t.Fatalf("event %s missing from delivered posts", id)
		}
	}
}
