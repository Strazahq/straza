package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/logging"
	"github.com/strazahq/straza/internal/wire"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestReadyzDegraded(t *testing.T) {
	t.Parallel()
	h := handleReadyz(map[string]Pinger{
		"store": fakePinger{},
		"bus":   fakePinger{err: errors.New("nats gone")},
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503", rec.Code)
	}
	var body wire.ReadyStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "degraded" || body.Components["store"] != "ok" || body.Components["bus"] != "nats gone" {
		t.Errorf("unexpected body: %+v", body)
	}
}

func TestReadyzOK(t *testing.T) {
	t.Parallel()
	h := handleReadyz(map[string]Pinger{"store": fakePinger{}})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
}

// TestAppBoot pins the boot path: a standalone-profile strazad boots
// with sqlite + embedded NATS and answers all three operational endpoints.
func TestAppBoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := config.Config{
		Profile: config.ProfileStandalone,
		DataDir: dir,
		Server:  config.Server{Listen: "127.0.0.1:0"},
		Log:     config.Log{Level: "error", Format: "json"},
		Store:   config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")},
		Events:  config.Events{Embedded: true},
		Governance: config.Governance{
			OfflineGraceTTL:   15 * time.Minute,
			LocalToolDefault:  config.EffectAllow,
			AuditBackpressure: config.BackpressureDrop,
		},
	}
	log := logging.New(cfg.Log, io.Discard)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app, err := New(ctx, cfg, log)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	base := "http://" + app.Addr()
	for _, ep := range []string{"/healthz", "/readyz", "/version"} {
		resp, err := http.Get(base + ep)
		if err != nil {
			t.Fatalf("GET %s: %v", ep, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d: %s", ep, resp.StatusCode, body)
		}
	}

	resp, err := http.Get(base + "/version")
	if err != nil {
		t.Fatal(err)
	}
	var v wire.VersionStatus
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if v.Profile != config.ProfileStandalone || v.Version == "" {
		t.Errorf("unexpected version payload: %+v", v)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("graceful shutdown timed out")
	}
}

func TestAppBootColdStartBudget(t *testing.T) {
	// Serial: its 5 second cold-start budget flakes under the load of parallel servers.
	// Perf budget: standalone cold start < 1 s to serving. CI machines are
	// noisy, so this enforces a soft 5 s.
	dir := t.TempDir()
	cfg := config.Config{
		Profile: config.ProfileStandalone,
		DataDir: dir,
		Server:  config.Server{Listen: "127.0.0.1:0"},
		Log:     config.Log{Level: "error", Format: "json"},
		Store:   config.Store{Driver: config.DriverSQLite, DSN: filepath.Join(dir, "straza.db")},
		Events:  config.Events{Embedded: true},
		Governance: config.Governance{
			OfflineGraceTTL:   15 * time.Minute,
			LocalToolDefault:  config.EffectAllow,
			AuditBackpressure: config.BackpressureDrop,
		},
	}
	start := time.Now()
	app, err := New(context.Background(), cfg, logging.New(cfg.Log, io.Discard))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	elapsed := time.Since(start)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	cancel()
	// Wait for full shutdown: the embedded NATS store must be closed before
	// t.TempDir cleanup removes it.
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("graceful shutdown timed out")
	}
	// The budget measures the product's boot, so it holds on the build that
	// ships. Under -race every instruction is instrumented and the boot takes
	// about 8 s on a 4 core box, so the pure-Go test pass carries this check.
	if !raceEnabled && elapsed > 5*time.Second {
		t.Errorf("cold start took %s, budget is <1s (soft CI limit 5s)", elapsed)
	}
	fmt.Printf("cold start: %s\n", elapsed)
}
