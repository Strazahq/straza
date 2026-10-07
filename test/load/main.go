// Command load is the load harness: a virtual-session swarm (checkin +
// hook decisions + MCP calls) plus targeted probes for every published
// budget. It runs everything on loopback against a real strazad (in-process
// boot, real HTTP), writes a JSON+Markdown report artifact, and, with
// -enforce, exits non-zero when any budget is blown.
//
//	go run ./test/load -sessions 100000 -rps 5000 -enforce -report perf-report.json
//
// The hook probe spawns a real straza binary; pass -straza or it is
// skipped. Scale knobs default to laptop-friendly values. The reference
// numbers are 100k sessions, 5k rps and 100k kill-switch clients, run with
// make perf-reference.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"
)

func main() {
	var (
		sessions    = flag.Int("sessions", 2000, "idle-session swarm size (the reference budget uses 100k on a dedicated box)")
		rps         = flag.Int("rps", 5000, "gateway target request rate (the reference budget is 5k rps per node)")
		duration    = flag.Duration("duration", 10*time.Second, "gateway measurement window")
		gwWorkers   = flag.Int("workers", 64, "gateway load workers")
		pdpSamples  = flag.Int("pdp-samples", 200000, "PDP latency samples")
		hookSamples = flag.Int("hook-samples", 150, "hook e2e spawn samples")
		straza      = flag.String("straza", "", "path to a built straza binary (empty = skip hook probe)")
		filler      = flag.Int("filler", 2000, "filler rules in the active gateway snapshot")
		killClients = flag.Int("kill-clients", 2000, "REAL edge push subscriptions, one SSE conn + one session each")
		killRevokes = flag.Int("kill-revokes", 200, "revocations measured for the kill-switch distribution")
		killPods    = flag.Int("kill-pods", 1, "pods behind the kill probe; >1 needs STRAZA_TEST_POSTGRES_DSN (revokes land on pod 0, fleet spreads across pods)")
		killRTT     = flag.Duration("kill-rtt", 0, "WAN-shaped RTT injected on probe reads (one-way = half; e.g. 40ms)")
		reportPath  = flag.String("report", "perf-report.json", "report artifact path (JSON; a .md twin is written next to it)")
		cpuprofile  = flag.String("cpuprofile", "", "write a CPU profile covering the probes (perf digging)")
		enforce     = flag.Bool("enforce", false, "exit 1 when any performance budget is over (the CI gate)")
		only        = flag.String("only", "", "comma-separated probe subset: pdp,compile,coldstart,swarm,gateway,hook,kill,soak,capture,fleet")
		captureEv   = flag.Int("capture-events", 2000, "capture-ingest probe burst size")
		soak        = flag.Duration("soak", 0, "soak window (30m at reference scale; 0 = skip): sustained gateway mix, zero goroutine/heap leak")
		fleetN      = flag.Int("fleet", 0, "standing-fleet rig size (0 = skip): N real sessions each holding push SSE + /mcp SSE + a poll checkin loop")
		fleetDur    = flag.Duration("fleet-duration", 90*time.Second, "fleet steady-state window")
		fleetPoll   = flag.Duration("fleet-poll", 30*time.Second, "fleet checkin refresh interval (the real daemon poll)")
		fleetCapPct = flag.Int("fleet-capture-pct", 10, "percent of the fleet emitting capture turns (the customer 5-10%% shape; 100 = everyone)")
		fleetTurn   = flag.Duration("fleet-turn-every", 20*time.Second, "captured agent turn interval (1 turn = 2 CEs, ~2KiB each)")
		fleetMcpPct = flag.Int("fleet-mcp-pct", 100, "percent of the fleet holding a standing /mcp SSE stream")
		fleetKills  = flag.Int("fleet-kills", 50, "mid-window single revokes measured under load (capped at fleet/10)")
		fleetAppr   = flag.Int("fleet-approvals", 10, "approval ticket flows driven during the window")
		fleetPods   = flag.Int("fleet-pods", 1, "pods behind the fleet rig; >1 needs STRAZA_TEST_POSTGRES_DSN (admin traffic lands on pod 0, fleet spreads across pods)")
		pdpBudget   = flag.Duration("pdp-budget", 100*time.Microsecond, "enforced PDP p99 (reference budget: 100µs; shared CI runners need a wider, documented ceiling)")
		gwAssertP   = flag.Int("gateway-assert-p", 99, "enforced gateway added-latency percentile (reference budget: 99; shared CI runners assert 95 and report 99)")
		gwBudget    = flag.Duration("gateway-budget", 5*time.Millisecond, "enforced gateway added-latency ceiling (reference budget: 5ms; shared CI runners use a wider, documented regression tripwire)")
	)
	flag.Parse()

	want := map[string]bool{}
	for _, p := range strings.Split(*only, ",") {
		if p = strings.TrimSpace(p); p != "" {
			want[p] = true
		}
	}
	run := func(name string) bool { return len(want) == 0 || want[name] }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile) // #nosec G304 -- operator-chosen output path
		if err != nil {
			fmt.Fprintln(os.Stderr, "cpuprofile:", err)
			os.Exit(1)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			fmt.Fprintln(os.Stderr, "cpuprofile:", err)
			os.Exit(1)
		}
		defer pprof.StopCPUProfile()
	}

	report := newReport(map[string]string{
		"sessions": strconv.Itoa(*sessions), "rps": strconv.Itoa(*rps),
		"duration": duration.String(), "workers": strconv.Itoa(*gwWorkers),
		"kill_clients": strconv.Itoa(*killClients), "kill_revokes": strconv.Itoa(*killRevokes),
		"hook_samples": strconv.Itoa(*hookSamples), "pdp_samples": strconv.Itoa(*pdpSamples),
		"fleet": strconv.Itoa(*fleetN), "fleet_capture_pct": strconv.Itoa(*fleetCapPct),
		"fleet_pods":    strconv.Itoa(*fleetPods),
		"fleet_mcp_pct": strconv.Itoa(*fleetMcpPct), "fleet_poll": fleetPoll.String(),
		"fleet_turn_every": fleetTurn.String(), "fleet_duration": fleetDur.String(),
	})

	if run("pdp") {
		report.add(probePDP(*pdpSamples, *pdpBudget))
	}
	if run("capture") {
		report.add(probeCaptureIngest(ctx, *captureEv))
	}
	if run("compile") {
		report.add(probeSnapshotCompile())
	}
	if run("coldstart") {
		report.add(probeColdStart(ctx, tempDir("coldstart")))
	}

	if run("swarm") || run("gateway") || (run("soak") && *soak > 0) {
		if err := swarmAndGateway(ctx, report, *sessions, *rps, *gwWorkers, *filler, *duration, *soak, *gwAssertP, *gwBudget, run); err != nil {
			report.add(failed("swarm-setup", err))
		}
	}

	if run("hook") {
		report.add(probeHook(ctx, *straza, tempDir("hook-data"), tempDir("hook-home"), *hookSamples))
	}
	if run("kill") {
		report.add(probeKillSwitch(ctx, tempDir("kill-data"), tempDir("kill-nats"), *killClients, *killRevokes, *killPods, *killRTT))
	}
	if run("fleet") && *fleetN > 0 {
		for _, p := range probeFleet(ctx, tempDir("fleet-data"), fleetOpts{
			agents: *fleetN, duration: *fleetDur, poll: *fleetPoll,
			capturePct: *fleetCapPct, turnEvery: *fleetTurn, mcpPct: *fleetMcpPct,
			kills: *fleetKills, approvals: *fleetAppr, pods: *fleetPods,
		}) {
			report.add(p)
		}
	}

	if err := report.write(*reportPath); err != nil {
		fmt.Fprintln(os.Stderr, "report write:", err)
		os.Exit(1)
	}
	fmt.Printf("\nreport: %s (pass=%v)\n", *reportPath, report.Pass)
	if *enforce && !report.Pass {
		fmt.Fprintln(os.Stderr, "PERF BUDGET VIOLATION: see report")
		os.Exit(1)
	}
}

// swarmAndGateway boots the main strazad (embedded events), runs the
// virtual-session swarm, then drives the gateway mix over the surviving
// tokens. One instance for both: the gateway must serve while the idle swarm
// population is resident, which is exactly the reference budget's combination.
func swarmAndGateway(ctx context.Context, report *Report, sessions, rps, workers, filler int, dur, soak time.Duration, gwAssertP int, gwBudget time.Duration, run func(string) bool) error {
	w, err := bootStrazad(ctx, strazadOpts{dataDir: tempDir("swarm-data")})
	if err != nil {
		return err
	}
	defer w.stop()

	adminTok, err := w.adminIDToken(ctx)
	if err != nil {
		return err
	}
	upstream, stopUpstream, err := startEchoUpstream()
	if err != nil {
		return err
	}
	defer stopUpstream()
	// Install first: the install creates loadtest-dev, the app's own role,
	// and seeding the users assigns it.
	if err := w.installEchoApp(adminTok, upstream, "loadtest-dev"); err != nil {
		return err
	}
	users, err := mintUserTokens(w, ctx, min(sessions, 500), "loadtest-dev")
	if err != nil {
		return err
	}
	// A realistic active snapshot: filler shell-rule sets bound to other
	// roles (production shape: non-matching sets are skipped wholesale, and
	// the PDP bench partitions the same way) plus the small matched set
	// carrying the mcp allow rule the gateway path needs.
	for _, p := range gatewayPolicies(filler) {
		if err := w.applyPolicy(adminTok, p.name, p.yaml); err != nil {
			return err
		}
	}
	keep := min(sessions, max(workers*8, 512))
	swarm := runCheckinSwarm(w, users, sessions, keep, min(32, workers))
	if run("swarm") {
		report.add(swarm.checkinProbe)
		report.add(swarm.memProbe)
	}
	if run("gateway") {
		report.add(probeGateway(w, upstream, swarm.tokens, rps, workers, dur, gwAssertP, gwBudget))
		report.add(probeDecide(w, swarm.tokens, max(rps/5, 100), max(workers/2, 8), dur/2+time.Second))
	}
	if run("soak") && soak > 0 {
		for _, p := range probeSoak(w, swarm.tokens, rps, workers, soak) {
			report.add(p)
		}
	}
	return nil
}

type namedPolicy struct{ name, yaml string }

// gatewayPolicies builds the active snapshot for the gateway run: n filler
// shell.exec rules partitioned into 100-rule sets bound to OTHER roles (the
// production shape: the engine skips non-matching sets wholesale, and the
// PDP bench partitions the same way) plus a small matched set carrying the
// tools/call allow for the echo app. A single giant set matched by the
// calling role costs ~250 ns/rule/call linear scan, measured by this
// harness; partition policy sets by role.
func gatewayPolicies(n int) []namedPolicy {
	var out []namedPolicy
	for set := 0; set*100 < n; set++ {
		count := min(100, n-set*100)
		var b strings.Builder
		fmt.Fprintf(&b, `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: load-filler-%03d }
spec:
  match: { roles: [bench-role-%d] }
  rules:
`, set, set)
		for i := 0; i < count; i++ {
			fmt.Fprintf(&b, `    - id: filler-%04d
      tools: [shell.exec]
      command: { denyPatterns: ["never-matches-%d-*"] }
      effect: deny
      reason: "no"
`, i, i)
		}
		out = append(out, namedPolicy{name: fmt.Sprintf("load-filler-%03d", set), yaml: b.String()})
	}
	out = append(out, namedPolicy{name: "load-mix", yaml: `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: load-mix }
spec:
  match: { roles: [loadtest-dev] }
  rules:
    - id: allow-echo
      tools: [mcp.call]
      apps: [loadtest]
      toolNames: { allow: ["*"] }
      effect: allow
`})
	return out
}

// tempDir is a var so the smoke test can route scratch space through
// t.TempDir (auto-cleaned) instead of the OS temp root.
var tempDir = func(name string) string {
	dir, err := os.MkdirTemp("", "straza-load-"+name+"-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		os.Exit(1)
	}
	return dir
}
