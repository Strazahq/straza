package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// probeFleet is the slice of the reference rig that runs on one box: a
// standing fleet where every agent holds what a REAL enrolled daemon holds:
// a /v1/push SSE subscription, an /mcp SSE stream (the listChanged hub), and
// a poll-interval checkin loop, while a configurable share of the fleet
// emits conversation capture at a realistic turn rate. Under that standing
// load it measures checkin under load, gateway added latency, listChanged broadcast spread, the approval
// ticket flow, kill push, sustained capture ingest + drain, and per-agent
// resource cost. Topology is printed beside every number; the strazad under
// test runs IN-PROCESS with the probe clients, so resource rows are a
// combined upper bound and say so.
type fleetOpts struct {
	agents     int
	duration   time.Duration
	poll       time.Duration
	capturePct int
	turnEvery  time.Duration
	mcpPct     int
	kills      int
	approvals  int
	// pods >1 is the multi-pod shape: N in-process pods
	// sharing external NATS + a fresh Postgres database (the kill probe's
	// -kill-pods convention, STRAZA_TEST_POSTGRES_DSN required). Members
	// spread round-robin and drive their OWN pod; admin traffic lands on
	// pod 0, so approval resume, kill push, and listChanged all cross pods.
	pods int
}

type fleetMember struct {
	idx int
	sid string
	pod *strazad

	mu  sync.Mutex
	tok string
}

func (m *fleetMember) token() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tok
}

func (m *fleetMember) setToken(t string) {
	m.mu.Lock()
	m.tok = t
	m.mu.Unlock()
}

func probeFleet(ctx context.Context, dataDir string, o fleetOpts) []Probe {
	const name = "fleet-rig"
	pods := max(o.pods, 1)
	store := "embedded NATS, sqlite"
	if pods > 1 {
		store = "external NATS, postgres"
	}
	topology := fmt.Sprintf("%d pod(s) (in-process, %s), %d agents, mcp %d%%, capture %d%% @ 1 turn/%s, poll %s",
		pods, store, o.agents, o.mcpPct, o.capturePct, o.turnEvery, o.poll)
	fail := func(err error) []Probe { return []Probe{failed(name, err)} }

	// Multi-pod plumbing first, so the pod-stop defer (declared last, runs
	// first) shuts every pod down BEFORE the probe database drops, which is
	// the kill probe's teardown order.
	podOpts := []strazadOpts{{dataDir: dataDir}}
	if pods > 1 {
		ns, err := natsserver.NewServer(&natsserver.Options{
			Host: "127.0.0.1", Port: -1,
			JetStream: true, StoreDir: filepath.Join(dataDir, "nats"),
			NoLog: true, NoSigs: true,
		})
		if err != nil {
			return fail(err)
		}
		ns.Start()
		defer ns.Shutdown()
		if !ns.ReadyForConnections(10 * time.Second) {
			return fail(fmt.Errorf("nats not ready"))
		}
		base := os.Getenv("STRAZA_TEST_POSTGRES_DSN")
		if base == "" {
			return fail(fmt.Errorf("-fleet-pods %d requires STRAZA_TEST_POSTGRES_DSN", pods))
		}
		fresh, drop, err := freshProbeDB(base)
		if err != nil {
			return fail(err)
		}
		defer drop()
		podOpts = podOpts[:0]
		for i := 0; i < pods; i++ {
			podOpts = append(podOpts, strazadOpts{
				dataDir: filepath.Join(dataDir, fmt.Sprintf("pod%d", i)),
				natsURL: ns.ClientURL(), pgDSN: fresh,
				issuer: "http://straza-fleet-probe.local",
			})
		}
	}
	podList := make([]*strazad, 0, pods)
	defer func() {
		for _, p := range podList {
			p.stop()
		}
	}()
	bootPod := func(i int) error {
		po := podOpts[i]
		if err := os.MkdirAll(po.dataDir, 0o700); err != nil {
			return err
		}
		p, err := bootStrazad(ctx, po)
		if err != nil {
			return fmt.Errorf("pod %d: %w", i, err)
		}
		podList = append(podList, p)
		return nil
	}
	if err := bootPod(0); err != nil {
		return fail(err)
	}
	w := podList[0] // pod 0: migrations, seeding, and every admin lane

	adminTok, err := w.adminIDToken(ctx)
	if err != nil {
		return fail(err)
	}
	upstream, stopUpstream, err := startEchoUpstream()
	if err != nil {
		return fail(err)
	}
	defer stopUpstream()
	// Install first: the install creates loadtest-fleet, the app's own
	// role, and seeding the users assigns it.
	if err := w.installEchoApp(adminTok, upstream, "loadtest-fleet"); err != nil {
		return fail(err)
	}
	users, err := mintUserTokens(w, ctx, min(o.agents, 500), "loadtest-fleet")
	if err != nil {
		return fail(err)
	}
	approverTok, err := fleetApprover(ctx, w)
	if err != nil {
		return fail(err)
	}
	if err := w.applyPolicy(adminTok, "fleet-approve", `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: fleet-approve}
spec:
  match: {roles: [loadtest-fleet]}
  rules:
    - id: approve-gate
      tools: [mcp.call]
      apps: [loadtest]
      toolNames: {allow: ["gate"]}
      effect: allow
      mode: approve
      approve: {roles: [fleet-approvers], timeoutSeconds: 60}
    - id: allow-echo
      tools: [mcp.call]
      apps: [loadtest]
      toolNames: {allow: ["echo"]}
      effect: allow
`); err != nil {
		return fail(err)
	}

	// Peer pods boot AFTER the app/bindings/policy exist: a fresh pod loads
	// all three from the shared store at boot (the k8s scale-up shape). A
	// server installed later through one pod's admin API reaches the other
	// pods through their converge consumers, once it runs on that pod.
	for i := 1; i < len(podOpts); i++ {
		if err := bootPod(i); err != nil {
			return fail(err)
		}
	}

	// ---- ramp: real checkins, then the standing conns per member ----------
	runtime.GC()
	var msBase runtime.MemStats
	runtime.ReadMemStats(&msBase)
	fdBase := fdCount()

	checkinClient := newLoadClient(128)
	sseClient := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 0, MaxConnsPerHost: 0}}

	fleet := make([]*fleetMember, o.agents)
	rampStart := time.Now()
	var rampErr atomic.Value
	var rampWG sync.WaitGroup
	rampSem := make(chan struct{}, 32)
	for i := 0; i < o.agents; i++ {
		rampWG.Add(1)
		rampSem <- struct{}{}
		go func(i int) {
			defer rampWG.Done()
			defer func() { <-rampSem }()
			pod := podList[i%len(podList)]
			sid, tok, err := pod.checkin(checkinClient, users[i%len(users)].idToken)
			if err != nil {
				rampErr.Store(fmt.Errorf("checkin %d: %w", i, err))
				return
			}
			fleet[i] = &fleetMember{idx: i, sid: sid, tok: tok, pod: pod}
		}(i)
	}
	rampWG.Wait()
	if err, _ := rampErr.Load().(error); err != nil {
		return fail(err)
	}
	ramp := time.Since(rampStart)

	// One member per pod waits for that pod's gateway catalog so tool calls
	// can't race the app's first health probe (multi-pod: the install lands
	// on pod 0, the others must converge over the shared store/spine).
	for i := 0; i < len(podList) && i < o.agents; i++ {
		if err := waitCatalog(podList[i], fleet[i].token(), "loadtest__gate", 20*time.Second); err != nil {
			return fail(fmt.Errorf("pod %d catalog: %w", i, err))
		}
	}

	// Standing connections + loops. All spawned goroutines stop on fleetCtx.
	fleetCtx, stopFleet := context.WithCancel(ctx)
	defer stopFleet()
	var (
		streams      sync.WaitGroup
		pushReady    atomic.Int64
		mcpOpen      atomic.Int64
		connErrs     atomic.Int64
		checkinLat   = &latencies{}
		checkinErrs  atomic.Int64
		capLat       = &latencies{}
		capSent      atomic.Int64
		capAccepted  atomic.Int64
		capErrs      atomic.Int64
		capErrLast   atomic.Value // last unexpected capture error text: names the failure class in the report
		dead         sync.Map     // sid → true, set before an expected revoke
		killArrivals sync.Map     // sid → chan time.Time
		lcTrigger    atomic.Int64
		lcLat        = &latencies{}
		lcSeen       atomic.Int64
	)
	mcpCohort := 0
	capCohort := 0
	perMemberSent := make([]int64, o.agents)
	for _, m := range fleet {
		ch := make(chan time.Time, 1)
		killArrivals.Store(m.sid, ch)

		// /v1/push standing subscription (every agent).
		streams.Add(1)
		go func(m *fleetMember, ch chan time.Time) {
			defer streams.Done()
			req, _ := http.NewRequestWithContext(fleetCtx, http.MethodGet, m.pod.base+"/v1/push", nil)
			req.Header.Set("Authorization", "Bearer "+m.token())
			resp, err := sseClient.Do(req)
			if err != nil {
				connErrs.Add(1)
				return
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				connErrs.Add(1)
				return
			}
			sc := bufio.NewScanner(resp.Body)
			for sc.Scan() {
				switch {
				case strings.HasPrefix(sc.Text(), "event: ready"):
					pushReady.Add(1)
				case strings.HasPrefix(sc.Text(), "event: revocation"):
					select {
					case ch <- time.Now():
					default:
					}
					return
				}
			}
		}(m, ch)

		// /mcp standing SSE (the listChanged hub) for the cohort.
		if inCohort(m.idx, o.agents, o.mcpPct) {
			mcpCohort++
			streams.Add(1)
			go func(m *fleetMember) {
				defer streams.Done()
				req, _ := http.NewRequestWithContext(fleetCtx, http.MethodGet, m.pod.base+"/mcp", nil)
				req.Header.Set("Authorization", "Bearer "+m.token())
				req.Header.Set("Accept", "text/event-stream")
				resp, err := sseClient.Do(req)
				if err != nil {
					connErrs.Add(1)
					return
				}
				defer func() { _ = resp.Body.Close() }()
				if resp.StatusCode != http.StatusOK {
					connErrs.Add(1)
					return
				}
				mcpOpen.Add(1)
				sc := bufio.NewScanner(resp.Body)
				counted := int64(0)
				for sc.Scan() {
					if !strings.Contains(sc.Text(), "list_changed") {
						continue
					}
					if t0 := lcTrigger.Load(); t0 != 0 && counted != t0 {
						counted = t0 // one sample per member per trigger
						lcLat.add(time.Since(time.Unix(0, t0)))
						lcSeen.Add(1)
					}
				}
			}(m)
		}

		// Poll-interval checkin refresh, staggered across the fleet.
		streams.Add(1)
		go func(m *fleetMember) {
			defer streams.Done()
			stagger := time.Duration(int64(o.poll) * int64(m.idx) / int64(max(o.agents, 1)))
			select {
			case <-fleetCtx.Done():
				return
			case <-time.After(stagger):
			}
			t := time.NewTicker(o.poll)
			defer t.Stop()
			for {
				select {
				case <-fleetCtx.Done():
					return
				case <-t.C:
					t0 := time.Now()
					tok, err := m.pod.refreshSession(checkinClient, m.token())
					if err != nil {
						if _, expected := dead.Load(m.sid); !expected {
							checkinErrs.Add(1)
						}
						return
					}
					checkinLat.add(time.Since(t0))
					m.setToken(tok)
				}
			}
		}(m)

		// Capture cohort: one 2-CE turn (prompt+reply, ~2 KiB each) per
		// turnEvery: the spool-flush shape a live captured agent produces.
		if inCohort(m.idx, o.agents, o.capturePct) {
			capCohort++
			streams.Add(1)
			go func(m *fleetMember) {
				defer streams.Done()
				stagger := time.Duration(int64(o.turnEvery) * int64(m.idx) / int64(max(o.agents, 1)))
				select {
				case <-fleetCtx.Done():
					return
				case <-time.After(stagger):
				}
				t := time.NewTicker(o.turnEvery)
				defer t.Stop()
				seq := 0
				for {
					select {
					case <-fleetCtx.Done():
						return
					case <-t.C:
						t0 := time.Now()
						n, err := postTurn(fleetCtx, m.pod, checkinClient, m.token(), m.idx, seq)
						if err != nil {
							if fleetCtx.Err() != nil {
								return // window teardown canceled an in-flight post, not an error
							}
							if _, expected := dead.Load(m.sid); !expected {
								capErrs.Add(1)
								capErrLast.Store(err.Error())
							}
							return
						}
						capLat.add(time.Since(t0))
						capSent.Add(2)
						capAccepted.Add(int64(n))
						atomic.AddInt64(&perMemberSent[m.idx], int64(n))
						seq++
					}
				}
			}(m)
		}
	}

	// All push subscriptions must be live before the window counts.
	readyDeadline := time.Now().Add(60 * time.Second)
	for int(pushReady.Load()) < o.agents {
		if connErrs.Load() > 0 {
			return fail(fmt.Errorf("%d standing connections failed during ramp", connErrs.Load()))
		}
		if time.Now().After(readyDeadline) {
			return fail(fmt.Errorf("push fleet not ready: %d/%d", pushReady.Load(), o.agents))
		}
		time.Sleep(20 * time.Millisecond)
	}

	runtime.GC()
	var msFleet runtime.MemStats
	runtime.ReadMemStats(&msFleet)
	fdFleet := fdCount()
	goroutines := runtime.NumGoroutine()

	// ---- steady-state window ---------------------------------------------
	windowStart := time.Now()
	windowEnd := windowStart.Add(o.duration)

	// Gateway echo sampler: fixed modest rate, 4 workers.
	echoLat := &latencies{}
	var echoErrs atomic.Int64
	var samplerWG sync.WaitGroup
	echoTick := time.NewTicker(40 * time.Millisecond)
	defer echoTick.Stop()
	echoJobs := make(chan int, 64)
	for k := 0; k < 4; k++ {
		samplerWG.Add(1)
		go func() {
			defer samplerWG.Done()
			for i := range echoJobs {
				m := fleet[i%o.agents]
				if _, expected := dead.Load(m.sid); expected {
					continue
				}
				t0 := time.Now()
				if err := callEcho(m.pod, checkinClient, m.token()); err != nil {
					echoErrs.Add(1)
					continue
				}
				echoLat.add(time.Since(t0))
			}
		}()
	}
	samplerCtx, stopSampler := context.WithCancel(fleetCtx)
	defer stopSampler()
	samplerWG.Add(1)
	go func() {
		defer samplerWG.Done()
		defer close(echoJobs)
		i := 0
		for {
			select {
			case <-samplerCtx.Done():
				return
			case <-echoTick.C:
				select {
				case echoJobs <- i:
					i++
				default:
				}
			}
		}
	}()

	// Approval ticket flows, sequential: call blocks on the approve gate,
	// the decider finds it in the pending list and approves.
	apVisible, apResume, apE2E := &latencies{}, &latencies{}, &latencies{}
	apErrs := 0
	seen := map[string]bool{}
	for k := 0; k < o.approvals && time.Now().Before(windowEnd); k++ {
		m := fleet[(k*37)%o.agents]
		if _, expected := dead.Load(m.sid); expected {
			continue
		}
		done := make(chan error, 1)
		t0 := time.Now()
		go func() { done <- callGate(m.pod, m.token()) }()
		id, tVisible, err := awaitPending(w, checkinClient, adminTok, seen, 10*time.Second)
		if err != nil {
			apErrs++
			<-done
			continue
		}
		tApprove := time.Now()
		if err := decideApproval(w, checkinClient, approverTok, id); err != nil {
			apErrs++
			<-done
			continue
		}
		if err := <-done; err != nil {
			apErrs++
			continue
		}
		tDone := time.Now()
		apVisible.add(tVisible.Sub(t0))
		apResume.add(tDone.Sub(tApprove))
		apE2E.add(tDone.Sub(t0))
	}

	// listChanged broadcast at mid-window: one binding write → hub broadcast.
	sleepUntil(fleetCtx, windowStart.Add(o.duration/2))
	lcExpected := int(mcpOpen.Load())
	lcTrigger.Store(time.Now().UnixNano())
	if err := bindListChanged(fleetCtx, w, checkinClient, adminTok); err != nil {
		return fail(fmt.Errorf("listChanged trigger: %w", err))
	}
	lcDeadline := time.Now().Add(10 * time.Second)
	for int(lcSeen.Load()) < lcExpected && time.Now().Before(lcDeadline) {
		time.Sleep(20 * time.Millisecond)
	}

	// Kill push under load at 2/3 window.
	sleepUntil(fleetCtx, windowStart.Add(o.duration*2/3))
	killLat := &latencies{}
	var killMissed atomic.Int64
	killSem := make(chan struct{}, 4)
	var killWG sync.WaitGroup
	for k := 0; k < min(o.kills, o.agents/10); k++ {
		m := fleet[o.agents-1-k] // tail members: keeps sampler indices mostly alive
		dead.Store(m.sid, true)
		killWG.Add(1)
		killSem <- struct{}{}
		go func(m *fleetMember) {
			defer killWG.Done()
			defer func() { <-killSem }()
			chAny, _ := killArrivals.Load(m.sid)
			t0 := time.Now()
			req, _ := http.NewRequest(http.MethodPost, w.base+"/v1/admin/sessions/"+m.sid+"/revoke", nil)
			req.Header.Set("Authorization", "Bearer "+adminTok)
			resp, err := checkinClient.Do(req)
			if err != nil {
				killMissed.Add(1)
				return
			}
			_ = resp.Body.Close()
			select {
			case t1 := <-chAny.(chan time.Time):
				killLat.add(t1.Sub(t0))
			case <-time.After(5 * time.Second):
				killMissed.Add(1)
			}
		}(m)
	}
	killWG.Wait()

	sleepUntil(fleetCtx, windowEnd)
	stopSampler()
	stopFleet()
	streams.Wait()
	samplerWG.Wait()
	window := time.Since(windowStart)

	// ---- drain: sampled captured members must land in conversation_turns --
	drain := time.Duration(0)
	if capCohort > 0 {
		drainStart := time.Now()
		deadline := drainStart.Add(60 * time.Second)
		samples := fleetCaptureSamples(fleet, perMemberSent, o, 3)
		for _, s := range samples {
			for {
				turns, err := w.st.Conversations().ListBySession(ctx, s.sid, int(s.sent)+100)
				if err != nil {
					return fail(err)
				}
				if int64(len(turns)) >= s.sent {
					break
				}
				if time.Now().After(deadline) {
					return fail(fmt.Errorf("capture drain incomplete after 60s: session %s has %d/%d turns", s.sid, len(turns), s.sent))
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		drain = time.Since(drainStart)
	}

	// ---- report -----------------------------------------------------------
	heapPerAgent := uint64(0)
	if o.agents > 0 && msFleet.HeapAlloc > msBase.HeapAlloc {
		heapPerAgent = (msFleet.HeapAlloc - msBase.HeapAlloc) / uint64(o.agents)
	}
	fdPerAgent := float64(fdFleet-fdBase) / float64(max(o.agents, 1))
	capRate := float64(capAccepted.Load()) / window.Seconds()
	capDetail := "2-CE ~2KiB turns per agent per interval: the per-event-insert worst case, which limits ingest"
	if s, _ := capErrLast.Load().(string); s != "" {
		capDetail += "; last unexpected error: " + s
	}

	probes := []Probe{
		{
			Name:     "fleet-ramp",
			Budget:   "informational @ " + topology,
			Observed: fmt.Sprintf("%d real checkins + standing conns in %.1fs (%.0f agents/s)", o.agents, ramp.Seconds(), float64(o.agents)/ramp.Seconds()),
			Value:    ramp.Seconds(), Limit: 0, Pass: true,
			Detail: "enrollment-storm shape: every agent checks in and opens push SSE before the window starts",
		},
		{
			Name:     "fleet-checkin-p99",
			Budget:   fmt.Sprintf("p99 < 500ms under standing load (tripwire) @ %s", topology),
			Observed: fmt.Sprintf("p99 = %s (p50 %s, %d refreshes, %d unexpected errors)", checkinLat.percentile(99).Round(time.Millisecond), checkinLat.percentile(50).Round(time.Millisecond), checkinLat.count(), checkinErrs.Load()),
			Value:    checkinLat.percentile(99).Seconds(), Limit: 0.5,
			Pass:   checkinLat.percentile(99) < 500*time.Millisecond && checkinErrs.Load() == 0,
			Detail: "the 30s-poll control lane every daemon rides; errors exclude deliberately killed members",
		},
		{
			Name:     "fleet-gateway-p99",
			Budget:   fmt.Sprintf("echo e2e p99 < 50ms under standing load (tripwire) @ %s", topology),
			Observed: fmt.Sprintf("p99 = %s (p50 %s, %d calls, %d errors)", echoLat.percentile(99).Round(time.Millisecond), echoLat.percentile(50).Round(time.Millisecond), echoLat.count(), echoErrs.Load()),
			Value:    echoLat.percentile(99).Seconds(), Limit: 0.05,
			Pass:   echoLat.count() > 0 && echoLat.percentile(99) < 50*time.Millisecond && echoErrs.Load() == 0,
			Detail: "governed tools/call against a near-zero upstream: PDP + credential + proxy cost while the fleet stands",
		},
		{
			Name:     "fleet-mcp-listchanged",
			Budget:   fmt.Sprintf("all %d standing /mcp streams notified, spread p99 < 5s @ %s", lcExpected, topology),
			Observed: fmt.Sprintf("%d/%d cohort streams open; %d/%d notified, first %s, p50 %s, p99 %s", mcpOpen.Load(), mcpCohort, lcSeen.Load(), lcExpected, lcLat.percentile(1).Round(time.Millisecond), lcLat.percentile(50).Round(time.Millisecond), lcLat.percentile(99).Round(time.Millisecond)),
			Value:    lcLat.percentile(99).Seconds(), Limit: 5,
			Pass:   int(lcSeen.Load()) == lcExpected && lcLat.percentile(99) < 5*time.Second,
			Detail: "one binding write → coalesced hub broadcast; spread includes the debounce by design (drop-don't-block hub)",
		},
		{
			Name:     "fleet-approval-flow",
			Budget:   fmt.Sprintf("approve→resume p99 < 2s under standing load @ %s", topology),
			Observed: fmt.Sprintf("approve→resume p99 = %s; request-visible p99 = %s; e2e p99 = %s (%d flows, %d errors)", apResume.percentile(99).Round(time.Millisecond), apVisible.percentile(99).Round(time.Millisecond), apE2E.percentile(99).Round(time.Millisecond), apE2E.count(), apErrs),
			Value:    apResume.percentile(99).Seconds(), Limit: 2,
			Pass:   apE2E.count() > 0 && apErrs == 0 && apResume.percentile(99) < 2*time.Second,
			Detail: "gateway Await lane: blocked tools/call resumes on the decision fan-out; visible = call start → pending in the admin list",
		},
		{
			Name:     "fleet-kill-under-load",
			Budget:   fmt.Sprintf("push p99 < 2s while the fleet stands @ %s", topology),
			Observed: fmt.Sprintf("p99 = %s (p50 %s, %d revokes, %d missed)", killLat.percentile(99).Round(time.Millisecond), killLat.percentile(50).Round(time.Millisecond), killLat.count(), killMissed.Load()),
			Value:    killLat.percentile(99).Seconds(), Limit: 2,
			Pass:   killLat.count() > 0 && killMissed.Load() == 0 && killLat.percentile(99) < 2*time.Second,
			Detail: "same lane the dedicated kill probe measures, but with checkin/capture/gateway load concurrent",
		},
		{
			Name:     "fleet-capture-sustained",
			Budget:   fmt.Sprintf("zero loss, sampled sessions drained ≤ 60s after stop @ %s", topology),
			Observed: fmt.Sprintf("%d agents captured: %d/%d CEs accepted (%.0f ev/s sustained), batch p99 %s, drain %.1fs, %d unexpected errors", capCohort, capAccepted.Load(), capSent.Load(), capRate, capLat.percentile(99).Round(time.Millisecond), drain.Seconds(), capErrs.Load()),
			Value:    drain.Seconds(), Limit: 60,
			Pass:   capSent.Load() == capAccepted.Load() && capErrs.Load() == 0 && (capCohort == 0 || drain <= 60*time.Second),
			Detail: capDetail,
		},
		{
			Name:     "fleet-resources",
			Budget:   "informational @ " + topology,
			Observed: fmt.Sprintf("heap +%d KiB/agent, %.1f fds/agent, %d goroutines at steady state", heapPerAgent/1024, fdPerAgent, goroutines),
			Value:    float64(heapPerAgent), Limit: 0, Pass: true,
			Detail: "in-process harness: probe-side clients are included, so heap and fds are a combined UPPER bound (each SSE conn counts both ends)",
		},
	}
	return probes
}
