package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHarnessSmoke runs every probe at toy scale and asserts the harness
// machinery works end to end: probes execute without infrastructure errors
// and the report artifact is valid. It deliberately does NOT assert the
// budgets, which are machine-dependent and enforced by `make perf-reference`
// at real scale (hook probe included there; here it needs a built binary so
// it is exercised only when STRAZA_LOAD_AGENT_BIN points at one).
func TestHarnessSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("perf smoke is not a -short test")
	}
	tmp := t.TempDir()
	oldTempDir := tempDir
	tempDir = func(name string) string {
		dir := filepath.Join(tmp, name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	defer func() { tempDir = oldTempDir }()

	// A hang guard, not a budget: under -race on a 4 core box that runs other
	// packages beside it, the toy run takes two to three minutes.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	report := newReport(map[string]string{"mode": "smoke"})
	report.add(probePDP(5000, 100*time.Microsecond))
	report.add(probeSnapshotCompile())
	report.add(probeColdStart(ctx, tempDir("coldstart")))
	if err := swarmAndGateway(ctx, report, 30, 200, 8, 100, time.Second, 0, 99, 5*time.Millisecond, func(string) bool { return true }); err != nil {
		t.Fatalf("swarm/gateway setup: %v", err)
	}
	report.add(probeHook(ctx, os.Getenv("STRAZA_LOAD_AGENT_BIN"), tempDir("hook-data"), tempDir("hook-home"), 10))
	// 200 REAL SSE conns, single pod, a 20ms injected RTT so the smoke also
	// exercises the WAN-delay path.
	report.add(probeKillSwitch(ctx, tempDir("kill-data"), tempDir("kill-nats"), 200, 10, 1, 20*time.Millisecond))
	// Toy fleet: every lane of the 4.4 rig executes (compressed poll/turn
	// intervals so refreshes and capture happen inside the short window).
	for _, p := range probeFleet(ctx, tempDir("fleet-data"), fleetOpts{
		agents: 40, duration: 8 * time.Second, poll: 3 * time.Second,
		capturePct: 50, turnEvery: 2 * time.Second, mcpPct: 100,
		kills: 4, approvals: 2,
	}) {
		report.add(p)
	}

	wantProbes := []string{
		"pdp-decision", "snapshot-compile", "cold-start",
		"checkin-swarm", "idle-session-mem", "gateway-added-p99",
		"decide-online", "hook-e2e", "killswitch-p99",
		"fleet-ramp", "fleet-checkin-p99", "fleet-gateway-p99",
		"fleet-mcp-listchanged", "fleet-approval-flow", "fleet-kill-under-load",
		"fleet-capture-sustained", "fleet-resources",
	}
	got := map[string]Probe{}
	for _, p := range report.Probes {
		got[p.Name] = p
	}
	for _, name := range wantProbes {
		p, ok := got[name]
		if !ok {
			t.Errorf("probe %s missing from the report", name)
			continue
		}
		// Budgets are machine-dependent; infrastructure failures are not.
		if strings.HasPrefix(p.Observed, "error:") {
			t.Errorf("probe %s errored: %s", name, p.Observed)
		}
	}

	path := filepath.Join(tmp, "report.json")
	if err := report.write(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("report is not valid JSON: %v", err)
	}
	if len(back.Probes) != len(report.Probes) {
		t.Errorf("report round-trip lost probes: %d != %d", len(back.Probes), len(report.Probes))
	}
	if _, err := os.Stat(filepath.Join(tmp, "report.md")); err != nil {
		t.Errorf("markdown twin missing: %v", err)
	}
}

// TestFleetMultiPodSmoke runs the stage-A fleet shape at toy scale: 2 pods
// sharing external NATS + a fresh Postgres database, admin traffic on pod 0.
// Every fleet lane must execute across pods without infrastructure errors.
// PG-gated like the multi-pod kill shape; budgets stay machine-dependent.
func TestFleetMultiPodSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("perf smoke is not a -short test")
	}
	if os.Getenv("STRAZA_TEST_POSTGRES_DSN") == "" {
		t.Skip("multi-pod fleet needs STRAZA_TEST_POSTGRES_DSN")
	}
	tmp := t.TempDir()
	oldTempDir := tempDir
	tempDir = func(name string) string {
		dir := filepath.Join(tmp, name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	defer func() { tempDir = oldTempDir }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	probes := probeFleet(ctx, tempDir("fleet-pg"), fleetOpts{
		agents: 40, duration: 8 * time.Second, poll: 3 * time.Second,
		capturePct: 50, turnEvery: 2 * time.Second, mcpPct: 100,
		kills: 4, approvals: 2, pods: 2,
	})
	got := map[string]Probe{}
	for _, p := range probes {
		got[p.Name] = p
	}
	for _, name := range []string{
		"fleet-ramp", "fleet-checkin-p99", "fleet-gateway-p99",
		"fleet-mcp-listchanged", "fleet-approval-flow", "fleet-kill-under-load",
		"fleet-capture-sustained", "fleet-resources",
	} {
		p, ok := got[name]
		if !ok {
			t.Errorf("probe %s missing from the multi-pod fleet", name)
			continue
		}
		if strings.HasPrefix(p.Observed, "error:") {
			t.Errorf("probe %s errored: %s", name, p.Observed)
		}
		if !strings.Contains(p.Budget, "2 pod(s)") {
			t.Errorf("probe %s topology does not state 2 pods: %s", name, p.Budget)
		}
	}
}
