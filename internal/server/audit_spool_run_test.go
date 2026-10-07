package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// spoolApp builds an App on a fresh SQLite store without running it, and
// returns what a stop-order test needs: the app, its config, its store, the
// captured log and the context whose end stops Run.
func spoolApp(t *testing.T) (*App, config.Config, store.Store, *syncBuffer, context.Context, context.CancelFunc) {
	t.Helper()
	cfg := spoolTestConfig(t, t.TempDir())
	st, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	log, buf := captureLogger()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	app, err := build(ctx, cfg, log, st)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return app, cfg, st, buf, ctx, cancel
}

// runUntilStopped runs app until ctx ends and fails the test when Run errs
// or does not return.
func runUntilStopped(t *testing.T, app *App, ctx context.Context) chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	return done
}

func waitStopped(t *testing.T, done chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// requireOutbox reopens the database behind cfg and requires one outbox row
// for each CloudEvent id.
func requireOutbox(t *testing.T, cfg config.Config, ids ...string) {
	t.Helper()
	reopened, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	for _, id := range ids {
		if n := outboxCount(t, reopened, id); n != 1 {
			t.Fatalf("outbox rows of %s = %d, want 1", id, n)
		}
	}
}

// TestRunWritesAuditSpoolBeforeStoreCloses pins the second half of the stop
// order in Run: the records the spool holds when the server stops are in
// the database after Run returns, so the drain ran before the store closed.
// It also pins that straza_audit_lost_total reads the spool's lost counter.
func TestRunWritesAuditSpoolBeforeStoreCloses(t *testing.T) {
	t.Parallel()
	app, cfg, st, buf, ctx, cancel := spoolApp(t)
	gate := make(chan struct{})
	app.audit = newAuditSpool(false, app.log, func(c context.Context, e store.OutboxEvent) error {
		<-gate
		_, err := st.Outbox().Insert(c, e)
		return err
	})
	done := runUntilStopped(t, app, ctx)
	const held = 5
	ids := make([]string, 0, held)
	for i := range held {
		ids = append(ids, fmt.Sprintf("ce-run-%d", i))
		_ = app.audit.submit(context.Background(), spoolRecord(ids[i], "x"))
	}
	cancel()
	close(gate)
	waitStopped(t, done)

	app.audit.lost.Add(2)
	if v, _, ok := metricSample(t, app, "straza_audit_lost_total", nil); !ok || v != 2 {
		t.Fatalf("straza_audit_lost_total = %v (found %v), want 2", v, ok)
	}
	if errs := spoolErrors(buf); len(errs) != 0 {
		t.Fatalf("Error records %q, want none", errs)
	}
	requireOutbox(t, cfg, ids...)
}

// TestRunKeepsAuditSpoolWhileRequestsFinish pins the first half of the stop
// order: the spool keeps writing until the listeners have shut down, so a
// request still running after the signal gets its record written. The spool
// here stops writing the moment its context ends, so a Run that ended the
// spool at the signal would leave the late record unwritten.
func TestRunKeepsAuditSpoolWhileRequestsFinish(t *testing.T) {
	t.Parallel()
	app, cfg, st, buf, ctx, cancel := spoolApp(t)
	written := make(chan string, 8)
	app.audit = newAuditSpool(false, app.log, func(c context.Context, e store.OutboxEvent) error {
		if _, err := st.Outbox().Insert(c, e); err != nil {
			return err
		}
		written <- ceIDOf([]byte(e.CE))
		return nil
	})
	app.audit.drainBound = 0
	entered, release := make(chan struct{}), make(chan struct{})
	mux := http.NewServeMux()
	mux.Handle("/", app.http.Handler)
	mux.HandleFunc("GET /test/hold", func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		_ = app.audit.submit(context.Background(), spoolRecord("ce-late", "x"))
		select {
		case <-written:
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusNoContent)
	})
	app.http.Handler = mux
	done := runUntilStopped(t, app, ctx)
	go func() {
		if resp, err := http.Get("http://" + app.Addr() + "/test/hold"); err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-entered
	cancel()
	close(release)
	waitStopped(t, done)

	if errs := spoolErrors(buf); len(errs) != 0 {
		t.Fatalf("Error records %q, want none", errs)
	}
	requireOutbox(t, cfg, "ce-late")
}

// TestShutdownEndsEventStreams pins that the main listener's Shutdown ends
// the daemon push stream and the MCP event stream when it starts, instead of
// waiting out its bound for clients that never leave on their own, so the
// audit spool's drain starts well inside the stop grace period.
func TestShutdownEndsEventStreams(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	seedIdentity(t, app)
	token, _ := checkinToken(t, app, base)

	push, cancelPush, resp := sseStream(t, base, token)
	defer cancelPush()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/push = %d, want 200", resp.StatusCode)
	}
	waitForLine(t, push, "event: ready")
	req, err := http.NewRequest(http.MethodGet, base+"/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	mcp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if mcp.StatusCode != http.StatusOK {
		t.Fatalf("GET /mcp = %d, want 200", mcp.StatusCode)
	}
	mcpDone := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, mcp.Body); _ = mcp.Body.Close(); close(mcpDone) }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.http.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown with two open streams = %v, want it to return before its bound", err)
	}
	for open := true; open; {
		select {
		case _, open = <-push:
		case <-time.After(5 * time.Second):
			t.Fatal("the push stream did not end")
		}
	}
	select {
	case <-mcpDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the MCP stream did not end")
	}
}

// TestDeploymentsGiveStrazadTimeToStop pins the stop grace of every shipped
// deployment of strazad at 30 seconds. Run waits up to 10 seconds for the
// requests in flight, then the audit spool drains for up to 5 seconds, and
// on SQLite a write behind another process's lock can add 5 more. A shorter
// grace kills strazad before it writes its last audit records.
func TestDeploymentsGiveStrazadTimeToStop(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	for _, rel := range []string{"deploy/compose/docker-compose.yaml", "deploy/compose/eval-stack/compose.yaml"} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Services map[string]struct {
				StopGracePeriod string `yaml:"stop_grace_period"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if got := doc.Services["strazad"].StopGracePeriod; got != "30s" {
			t.Errorf("%s: strazad stop_grace_period = %q, want 30s", rel, got)
		}
	}
	for _, scenario := range []string{"turnkey", "byo", "standalone", "apps"} {
		rel := filepath.Join("deploy", "helm", "straza", "tests", "golden", scenario+".yaml")
		f, err := os.Open(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		dec := yaml.NewDecoder(f)
		for {
			var doc struct {
				Kind string `yaml:"kind"`
				Spec struct {
					Template struct {
						Spec struct {
							Grace      *int `yaml:"terminationGracePeriodSeconds"`
							Containers []struct {
								Name string `yaml:"name"`
							} `yaml:"containers"`
						} `yaml:"spec"`
					} `yaml:"template"`
				} `yaml:"spec"`
			}
			err := dec.Decode(&doc)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("%s: %v", rel, err)
			}
			pod := doc.Spec.Template.Spec
			if doc.Kind != "Deployment" || len(pod.Containers) == 0 || pod.Containers[0].Name != "strazad" {
				continue
			}
			found = true
			if pod.Grace == nil || *pod.Grace != 30 {
				t.Errorf("%s: strazad terminationGracePeriodSeconds = %v, want 30", rel, pod.Grace)
			}
		}
		_ = f.Close()
		if !found {
			t.Errorf("%s: no strazad Deployment", rel)
		}
	}
}

// TestRunStopKeepsBlockedDecisionsBlocked pins the stop under block with a
// full queue and a database that never answers. When the drain's bound
// ends, run counts what the queue holds and stops receiving, so a decision
// still waiting for room never runs: it stays blocked until the process
// exits, and its client gets no answer from it. The record in the loop gets
// its own not-confirmed line, and the two in the queue go to the stop count.
func TestRunStopKeepsBlockedDecisionsBlocked(t *testing.T) {
	t.Parallel()
	app, _, _, buf, ctx, cancel := spoolApp(t)
	sp := newAuditSpool(true, app.log, func(c context.Context, _ store.OutboxEvent) error { <-c.Done(); return c.Err() })
	sp.ch = make(chan store.OutboxEvent, 2)
	sp.drainBound = 200 * time.Millisecond
	app.audit = sp
	var entered, ran atomic.Int64
	mux := http.NewServeMux()
	mux.Handle("/", app.http.Handler)
	mux.HandleFunc("GET /test/decide", func(w http.ResponseWriter, r *http.Request) {
		if err := sp.submit(r.Context(), spoolRecord(fmt.Sprintf("ce-dec-%d", entered.Add(1)), "x")); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable) // refused: the decision does not run
			return
		}
		ran.Add(1) // the decision runs here: its answer and, on /mcp, the upstream call
		w.WriteHeader(http.StatusOK)
	})
	app.http.Handler = mux
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	const fired = 10
	var clients sync.WaitGroup
	for range fired {
		clients.Add(1)
		go func() {
			defer clients.Done()
			if resp, err := http.Get("http://" + app.Addr() + "/test/decide"); err == nil {
				_ = resp.Body.Close()
			}
		}()
	}
	waitFor(t, "one record in the loop, two in the queue and seven decisions waiting", func() bool {
		return entered.Load() == fired && ran.Load() == 3 && len(sp.ch) == 2
	})
	cancel()
	select {
	case <-done: // Shutdown gives up after 10 s on the seven waiting requests
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return")
	}
	time.Sleep(200 * time.Millisecond)
	if got := ran.Load(); got != 3 {
		t.Fatalf("decisions that ran = %d after the stop, want 3: the stop released %d waiting decisions without their records", got, got-3)
	}
	if got := sp.lost.Load(); got != 3 {
		t.Fatalf("lost = %d, want 3: the record in the loop and the two in the queue", got)
	}
	lost, unsure, atStop := spoolLines(buf)
	if len(lost) != 0 || len(unsure) != 1 || len(atStop) != 1 || !strings.Contains(atStop[0], "count=2") {
		t.Fatalf("lost lines %q, not-confirmed lines %q, stop lines %q; want one not-confirmed line and one stop line with count=2", lost, unsure, atStop)
	}
	for range fired - 1 { // free the handlers so the test leaves no goroutine behind
		<-sp.ch
	}
	clients.Wait()
}

// TestRunListenerFailureKeepsSpoolWhileRequestsFinish pins the stop order in
// the branch of Run where a listener dies: the spool keeps writing until the
// other requests have finished, so a record a request submits after the
// failure is in the outbox after Run returns.
func TestRunListenerFailureKeepsSpoolWhileRequestsFinish(t *testing.T) {
	t.Parallel()
	app, cfg, st, buf, ctx, _ := spoolApp(t)
	written := make(chan string, 8)
	app.audit = newAuditSpool(false, app.log, func(c context.Context, e store.OutboxEvent) error {
		if _, err := st.Outbox().Insert(c, e); err != nil {
			return err
		}
		written <- ceIDOf([]byte(e.CE))
		return nil
	})
	app.audit.drainBound = 0
	entered, release := make(chan struct{}), make(chan struct{})
	mux := http.NewServeMux()
	mux.Handle("/", app.http.Handler)
	mux.HandleFunc("GET /test/hold", func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		_ = app.audit.submit(context.Background(), spoolRecord("ce-fail-late", "x"))
		select {
		case <-written:
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusNoContent)
	})
	app.http.Handler = mux
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	go func() {
		if resp, err := http.Get("http://" + app.Addr() + "/test/hold"); err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-entered
	if err := app.ln.Close(); err != nil { // the main listener dies
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil after the listener died, want the listener's error")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after the listener died")
	}
	if errs := spoolErrors(buf); len(errs) != 0 {
		t.Fatalf("spool Error records %q, want none", errs)
	}
	requireOutbox(t, cfg, "ce-fail-late")
}

// TestRunListenerFailureDrainsBeforeStoreCloses pins the other half of that
// branch: the records the spool holds when a listener dies are in the
// database after Run returns, so the drain ran before the store closed.
func TestRunListenerFailureDrainsBeforeStoreCloses(t *testing.T) {
	t.Parallel()
	app, cfg, st, buf, ctx, _ := spoolApp(t)
	gate := make(chan struct{})
	app.audit = newAuditSpool(false, app.log, func(c context.Context, e store.OutboxEvent) error {
		<-gate
		_, err := st.Outbox().Insert(c, e)
		return err
	})
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	ids := []string{"ce-lf-0", "ce-lf-1", "ce-lf-2"}
	for _, id := range ids {
		_ = app.audit.submit(context.Background(), spoolRecord(id, "x"))
	}
	time.Sleep(50 * time.Millisecond)
	if err := app.ln.Close(); err != nil { // the main listener dies
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	close(gate)
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after the listener died")
	}
	if errs := spoolErrors(buf); len(errs) != 0 || app.audit.lost.Load() != 0 {
		t.Fatalf("spool Error records %q, lost = %d; want none", errs, app.audit.lost.Load())
	}
	requireOutbox(t, cfg, ids...)
}
