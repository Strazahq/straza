package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestRunWaitsForARecorderInFlight pins the stop order for a worker that
// changes the store and then writes the record of the change: a stop that
// arrives between the two steps waits for the pass, so the record is in the
// database after Run returns.
func TestRunWaitsForARecorderInFlight(t *testing.T) {
	t.Parallel()
	app, cfg, _, buf, ctx, cancel := spoolApp(t)
	inPass := make(chan struct{})
	release := make(chan struct{})
	app.goRecorder(ctx, func(ctx context.Context) {
		close(inPass)
		<-ctx.Done()
		// The change is in the store and the stop has arrived. The record
		// follows only after the store answers again.
		<-release
		app.emitEventCtx(ctx, "straza.audit.admin", map[string]any{"action": "signing-keys.promote", "kid": "stop-test"})
	})
	done := runUntilStopped(t, app, ctx)
	<-inPass
	cancel()
	time.Sleep(200 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("Run returned while a recorder was between its store write and its record")
	default:
	}
	close(release)
	waitStopped(t, done)
	if strings.Contains(buf.String(), "background worker") {
		t.Errorf("the log reports a worker that was still writing: %s", buf.String())
	}

	found := recorderRows(t, cfg, "stop-test")
	if found != 1 {
		t.Fatalf("outbox rows of the recorder's change = %d, want 1", found)
	}
}

// TestRunStopsWhenARecorderHangs pins the bound of that wait: a worker that
// never returns holds the stop for recorderDrainTimeout and no longer, and
// the stop leaves an Error line that says a record may be missing.
func TestRunStopsWhenARecorderHangs(t *testing.T) {
	t.Parallel()
	app, _, _, buf, ctx, cancel := spoolApp(t)
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	app.goRecorder(ctx, func(context.Context) { <-hang })
	done := runUntilStopped(t, app, ctx)
	time.Sleep(100 * time.Millisecond)
	stopped := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(recorderDrainTimeout + 8*time.Second):
		t.Fatal("Run did not return after the bound of the recorder wait")
	}
	if waited := time.Since(stopped); waited < recorderDrainTimeout {
		t.Errorf("Run returned after %s, before the bound of %s", waited, recorderDrainTimeout)
	}
	if !strings.Contains(buf.String(), "the audit record of its last change may be missing") {
		t.Errorf("the stop left no Error line about the worker: %s", buf.String())
	}
}

// TestRunListenerFailureWaitsForARecorder pins the other stop path: when a
// listener dies, Run waits for the pass a recorder is in before it closes
// the store. It also ends the recorders it started itself, or the stop
// would last the whole bound of the wait.
func TestRunListenerFailureWaitsForARecorder(t *testing.T) {
	t.Parallel()
	app, cfg, _, _, ctx, _ := spoolApp(t)
	inPass := make(chan struct{})
	release := make(chan struct{})
	app.goRecorder(ctx, func(ctx context.Context) {
		close(inPass)
		<-release
		app.emitEventCtx(ctx, "straza.audit.admin", map[string]any{"action": "signing-keys.retire", "kid": "listener-test"})
	})
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	<-inPass
	time.Sleep(50 * time.Millisecond)
	if err := app.ln.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("Run returned while a recorder was between its store write and its record")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(recorderDrainTimeout / 2):
		t.Fatal("Run did not return soon after the recorder's pass ended, so it left its own recorders running")
	}
	if n := recorderRows(t, cfg, "listener-test"); n != 1 {
		t.Fatalf("outbox rows of the recorder's change = %d, want 1", n)
	}
}

// recorderRows counts the outbox rows whose record names kid, read from a
// store opened after Run closed its own.
func recorderRows(t *testing.T, cfg config.Config, kid string) int {
	t.Helper()
	reopened, err := store.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	rows, err := reopened.Outbox().ListRecent(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, row := range rows {
		var ce struct {
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal([]byte(row.CE), &ce) == nil && ce.Data["kid"] == kid {
			found++
		}
	}
	return found
}
