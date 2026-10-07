package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// tenKRuleDocs builds the reference workload: 100 sets × 100 rules across
// mixed roles (mirrors BenchmarkEvaluate10kRules so numbers stay comparable).
func tenKRuleDocs() ([]policy.Document, [][]byte, error) {
	var docs []policy.Document
	var raws [][]byte
	for s := 0; s < 100; s++ {
		var rules strings.Builder
		for r := 0; r < 100; r++ {
			fmt.Fprintf(&rules, `
    - id: rule-%03d
      tools: [shell.exec]
      command: { denyPatterns: ["dangerous-%d-*"] }
      effect: deny
      reason: "no"
`, r, r)
		}
		raw := []byte(fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: bench-set-%03d }
spec:
  match: { roles: [role-%d] }
  rules:%s`, s, s%10, rules.String()))
		doc, err := policy.Parse(raw)
		if err != nil {
			return nil, nil, err
		}
		docs = append(docs, doc)
		raws = append(raws, raw)
	}
	return docs, raws, nil
}

// probePDP measures the local PDP decision latency on a 10k-rule snapshot.
// Budget: p99 < 100 µs (the published PDP latency row). Samples are
// single-threaded; the budget is per-decision latency, not throughput.
// probePDP measures pure in-memory decision latency. limit is the enforced
// p99: 100µs is the budget on a dedicated box. Shared CI runners cannot
// assert µs-scale tails reliably, because scheduler jitter alone produces
// multi-ms max outliers at healthy p50s, so the CI gate passes a
// documented, wider ceiling.
func probePDP(samples int, limit time.Duration) Probe {
	docs, _, err := tenKRuleDocs()
	if err != nil {
		return failed("pdp-decision", err)
	}
	eng, err := policy.NewEngine(docs, policy.EffectAllow)
	if err != nil {
		return failed("pdp-decision", err)
	}
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "git status --porcelain"}
	sub := policy.Subject{User: "load", Roles: []string{"role-3"}, Attestation: "advisory"}

	// Warm up, then sample. On coarse-timer platforms (Windows ticks are
	// ~0.5–1 ms, far above the budget) each sample averages a batch; on
	// Linux (where the CI gate runs) batch=1 keeps true per-call tails.
	for i := 0; i < 1000; i++ {
		_ = eng.Evaluate(ev, sub)
	}
	batch := 1
	if res := timerResolution(); res > 10*time.Microsecond {
		batch = 256
	}
	rounds := samples / batch
	lat := &latencies{ns: make([]int64, 0, rounds)}
	for i := 0; i < rounds; i++ {
		t0 := time.Now()
		for j := 0; j < batch; j++ {
			if d := eng.Evaluate(ev, sub); d.Effect != policy.EffectAllow {
				return failed("pdp-decision", fmt.Errorf("unexpected decision %+v", d))
			}
		}
		lat.add(time.Since(t0) / time.Duration(batch))
	}
	p99 := lat.percentile(99)
	detail := ""
	if batch > 1 {
		detail = fmt.Sprintf("coarse platform timer: each sample averages a %d-call batch (per-call tails need the Linux CI run)", batch)
	}
	return Probe{
		Name:     "pdp-decision",
		Budget:   fmt.Sprintf("p99 < %s here (reference budget: 100µs, 10k-rule snapshot)", limit),
		Observed: fmt.Sprintf("p99 = %s (p50 %s, max %s, n=%d×%d)", p99, lat.percentile(50), lat.max(), rounds, batch),
		Value:    float64(p99.Nanoseconds()) / 1000,
		Limit:    float64(limit.Nanoseconds()) / 1000,
		Pass:     p99 < limit,
		Detail:   detail,
	}
}

// probeSnapshotCompile measures the full snapshot produce-verify cycle for
// 10k rules: parse+validate → canonical compile → ed25519 sign → re-open.
// Budget: < 2 s.
func probeSnapshotCompile() Probe {
	_, raws, err := tenKRuleDocs()
	if err != nil {
		return failed("snapshot-compile", err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return failed("snapshot-compile", err)
	}

	t0 := time.Now()
	snap, err := policy.Compile(policy.CompileInput{
		Documents:    raws,
		LocalDefault: policy.EffectAllow,
		MaxAge:       900,
		CreatedUnix:  time.Now().Unix(),
	})
	if err != nil {
		return failed("snapshot-compile", err)
	}
	signed, id, err := snap.Sign("load-key", priv)
	if err != nil {
		return failed("snapshot-compile", err)
	}
	if _, _, err := policy.OpenSnapshot(signed, id, func(string) (ed25519.PublicKey, bool) { return pub, true }); err != nil {
		return failed("snapshot-compile", err)
	}
	elapsed := time.Since(t0)
	return Probe{
		Name:     "snapshot-compile",
		Budget:   "< 2s (10k rules, compile+sign+verify)",
		Observed: fmt.Sprintf("%s (signed blob %d KB)", elapsed.Round(time.Millisecond), len(signed)>>10),
		Value:    elapsed.Seconds(),
		Limit:    2,
		Pass:     elapsed < 2*time.Second,
	}
}

// probeColdStart boots a fresh standalone strazad (empty data dir) and
// measures New→serving. Budget: < 1 s.
func probeColdStart(ctx context.Context, dataDir string) Probe {
	w, err := bootStrazad(ctx, strazadOpts{dataDir: dataDir})
	if err != nil {
		return failed("cold-start", err)
	}
	started := w.started
	w.stop()
	return Probe{
		Name:     "cold-start",
		Budget:   "< 1s to serving (standalone)",
		Observed: started.Round(time.Millisecond).String(),
		Value:    started.Seconds(),
		Limit:    1,
		Pass:     started < time.Second,
	}
}

// swarmResult carries what the checkin swarm leaves behind for later probes.
type swarmResult struct {
	memProbe     Probe
	checkinProbe Probe
	sessionIDs   []string
	tokens       []string // session tokens retained for traffic (first `keep`)
}

// runCheckinSwarm establishes n virtual sessions over the real HTTP checkin
// path and measures (a) checkin latency (report-only) and (b) server-side
// heap growth per idle session. Budget: < 50 KB/session.
//
// Memory accounting: the harness shares the process with the server, so ID
// tokens are pre-minted before the baseline and only `keep` session tokens
// are retained after the swarm; everything else the harness allocated is
// garbage by the second GC, leaving the server's per-session state as the
// dominant retained delta.
func runCheckinSwarm(w *strazad, users []userToken, n, keep, workers int) swarmResult {
	if keep > n {
		keep = n
	}
	client := newLoadClient(workers)
	lat := &latencies{ns: make([]int64, 0, n)}
	sessionIDs := make([]string, n)
	tokens := make([]string, n)

	runtime.GC()
	runtime.GC()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)

	var wg sync.WaitGroup
	work := make(chan int)
	var firstErr error
	var errOnce sync.Once
	for wkr := 0; wkr < workers; wkr++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				t0 := time.Now()
				sid, tok, err := w.checkin(client, users[i%len(users)].idToken)
				lat.add(time.Since(t0))
				if err != nil {
					errOnce.Do(func() { firstErr = err })
					continue
				}
				sessionIDs[i] = sid
				tokens[i] = tok
			}
		}()
	}
	swarmStart := time.Now()
	for i := 0; i < n; i++ {
		work <- i
	}
	close(work)
	wg.Wait()
	swarmWall := time.Since(swarmStart)

	if firstErr != nil {
		return swarmResult{memProbe: failed("idle-session-mem", firstErr), checkinProbe: failed("checkin-swarm", firstErr)}
	}

	// Drop everything the idle sessions do not need client-side.
	kept := append([]string(nil), tokens[:keep]...)
	keptIDs := append([]string(nil), sessionIDs...)
	tokens = nil // the bulk of harness-retained bytes
	_ = tokens
	var retained int
	for _, t := range kept {
		retained += len(t)
	}
	runtime.GC()
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	perSession := (float64(m1.HeapAlloc) - float64(m0.HeapAlloc) - float64(retained)) / float64(n) / 1024
	if perSession < 0 {
		perSession = 0
	}
	mem := Probe{
		Name:     "idle-session-mem",
		Budget:   "< 50 KB heap per idle session",
		Observed: fmt.Sprintf("%.1f KB/session (heap %0.1f→%0.1f MB over %d sessions)", perSession, float64(m0.HeapAlloc)/1e6, float64(m1.HeapAlloc)/1e6, n),
		Value:    perSession,
		Limit:    50,
		Pass:     perSession < 50,
	}
	rate := float64(n) / swarmWall.Seconds()
	chk := Probe{
		Name:     "checkin-swarm",
		Budget:   "report-only (session establishment)",
		Observed: fmt.Sprintf("%d checkins in %s (%.0f/s, p50 %s, p99 %s)", n, swarmWall.Round(time.Millisecond), rate, lat.percentile(50), lat.percentile(99)),
		Value:    rate,
		Limit:    0,
		Pass:     true,
	}
	return swarmResult{memProbe: mem, checkinProbe: chk, sessionIDs: keptIDs, tokens: kept}
}

// userToken pairs a seeded user with its pre-minted ID token.
type userToken struct {
	idToken string
}

func mintUserTokens(w *strazad, ctx context.Context, count int, role string) ([]userToken, error) {
	users, err := w.seedUsers(ctx, count, role)
	if err != nil {
		return nil, err
	}
	out := make([]userToken, len(users))
	for i, u := range users {
		tok, err := w.idToken(u)
		if err != nil {
			return nil, err
		}
		out[i] = userToken{idToken: tok}
	}
	return out, nil
}

func failed(name string, err error) Probe {
	return Probe{Name: name, Budget: "n/a", Observed: "error: " + err.Error(), Pass: false}
}

// timerResolution reports the smallest nonzero interval time.Now can see:
// coarse on Windows (~0.5–1 ms), ~ns on Linux.
func timerResolution() time.Duration {
	best := time.Duration(1 << 62)
	for i := 0; i < 50; i++ {
		t0 := time.Now()
		var d time.Duration
		for d == 0 {
			d = time.Since(t0)
		}
		if d < best {
			best = d
		}
	}
	return best
}
