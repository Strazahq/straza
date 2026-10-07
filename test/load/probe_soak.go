package main

import (
	"fmt"
	"runtime"
	"time"
)

// probeSoak drives the gateway mix continuously for a long window and proves
// the process neither leaks goroutines nor grows its heap. strazad runs
// in-process here, so the samples cover exactly the code under test (plus
// this harness's own steady-state workers, which is why sampling happens
// after a warm-up, a drain pause, and a double GC on both sides: deltas
// measure leak-shape, not absolute footprint).
func probeSoak(w *strazad, tokens []string, rps, workers int, dur time.Duration) []Probe {
	const name = "soak"
	if len(tokens) == 0 {
		return []Probe{failed(name+"-goroutines", fmt.Errorf("no session tokens"))}
	}
	client := newLoadClient(workers * 2)

	// Warm-up: reach steady state (connections, catalogs, pools) before the
	// baseline sample, or startup allocation reads as "growth".
	warm := runPaced(client, w.base+"/mcp", echoCallBody, tokens, rps, workers, 30*time.Second)
	if warm.err != nil {
		return []Probe{failed(name+"-goroutines", fmt.Errorf("warm-up: %w", warm.err))}
	}
	time.Sleep(2 * time.Second) // in-flight request goroutines drain
	runtime.GC()
	runtime.GC()
	g0 := runtime.NumGoroutine()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)

	// Session tokens live 300 s: a 30-minute soak must refresh them exactly
	// as real clients do, or it measures twenty-five minutes of 401s.
	// Refresh the working set every window.
	work := append([]string(nil), tokens...)
	var sent, errs int64
	for elapsed := time.Duration(0); elapsed < dur; {
		win := time.Minute
		if remaining := dur - elapsed; remaining < win {
			win = remaining
		}
		res := runPaced(client, w.base+"/mcp", echoCallBody, work, rps, workers, win)
		if res.err != nil {
			return []Probe{failed(name+"-goroutines", fmt.Errorf("soak window at %s: %w", elapsed, res.err))}
		}
		sent += res.sent
		errs += res.errs
		elapsed += win
		for i, tok := range work {
			if nt, err := w.refreshSession(client, tok); err == nil {
				work[i] = nt
			} // else: keep the old token; the server stays authoritative
		}
	}

	time.Sleep(2 * time.Second)
	runtime.GC()
	runtime.GC()
	g1 := runtime.NumGoroutine()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	gDelta := g1 - g0
	heapSlack := m0.HeapAlloc/4 + 16<<20 // 25% + 16 MiB absolute floor
	mib := func(b uint64) float64 { return float64(b) / (1 << 20) }
	detail := fmt.Sprintf("%s at %d rps: %d requests, %d errors", dur, rps, sent, errs)

	return []Probe{
		{
			Name:     name + "-goroutines",
			Budget:   "zero leak (Δ ≤ 25 after drain+GC)",
			Observed: fmt.Sprintf("%d → %d (Δ%+d)", g0, g1, gDelta),
			Value:    float64(gDelta),
			Limit:    25,
			Pass:     gDelta <= 25 && errs == 0,
			Detail:   detail,
		},
		{
			Name:     name + "-heap",
			Budget:   "growth ≤ 25% + 16 MiB after GC",
			Observed: fmt.Sprintf("%.1f MiB → %.1f MiB", mib(m0.HeapAlloc), mib(m1.HeapAlloc)),
			Value:    mib(m1.HeapAlloc),
			Limit:    mib(m0.HeapAlloc + heapSlack),
			Pass:     m1.HeapAlloc <= m0.HeapAlloc+heapSlack,
			Detail:   detail,
		},
	}
}
