package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const echoCallBody = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"loadtest__echo","arguments":{"text":"load"}}}`

const decideBody = `{"event":{"kind":"tool.pre","tool":"shell.exec","command":"git status --porcelain"}}`

// probeGateway measures the gateway's ADDED latency per MCP call (budget:
// added p99 < 5 ms @ 5k rps/node). Two identical paced runs, same body, rate,
// workers and box, first straight at the upstream (calibration), then through
// the gateway PEP: added = gateway tail minus direct tail, so load-generator
// and loopback saturation cancel out and what remains is the gateway's own
// work (auth, catalog, PDP, audit spool, proxying).
//
// assertP/limit select the enforced tail. p99 < 5 ms is the budget on a
// dedicated box. On a shared 4-vCPU runner the GATEWAY leg alone carries
// double-digit-ms tails at sub-ms medians, because the measured run also hosts
// the entire real async pipeline the direct calibration never triggers (outbox
// inserts, relay publishes, JetStream disk writes, chain appends). The CI gate
// therefore enforces a wider, documented ceiling as a regression tripwire (it
// still catches the ~50 ms pooled-transport class of bug), and the reference
// number is asserted by `make perf-reference` on real hardware.
func probeGateway(w *strazad, upstreamURL string, tokens []string, targetRPS, workers int, dur time.Duration, assertP int, limit time.Duration) Probe {
	if len(tokens) == 0 {
		return failed("gateway-added-p99", fmt.Errorf("no session tokens"))
	}
	client := newLoadClient(workers * 2)

	// Warm both paths (connections, JIT-ish caches, the per-roleset catalog).
	for i := 0; i < 100; i++ {
		if code, err := postOnce(client, upstreamURL, echoCallBody, ""); err != nil || code >= 500 {
			return failed("gateway-added-p99", fmt.Errorf("upstream warmup: code=%d err=%v", code, err))
		}
		if code, err := postOnce(client, w.base+"/mcp", echoCallBody, tokens[i%len(tokens)]); err != nil || code != http.StatusOK {
			return failed("gateway-added-p99", fmt.Errorf("gateway warmup: code=%d err=%v", code, err))
		}
	}

	direct := runPaced(client, upstreamURL, echoCallBody, nil, targetRPS, workers, dur)
	if direct.err != nil {
		return failed("gateway-added-p99", fmt.Errorf("direct calibration: %w", direct.err))
	}
	gw := runPaced(client, w.base+"/mcp", echoCallBody, tokens, targetRPS, workers, dur)
	if gw.err != nil {
		return failed("gateway-added-p99", fmt.Errorf("gateway run: %w", gw.err))
	}

	added := gw.lat.percentile(float64(assertP)) - direct.lat.percentile(float64(assertP))
	if added < 0 {
		added = 0
	}
	pass := added < limit && gw.errs == 0
	detail := fmt.Sprintf("gateway %.0f/s: p50 %s p95 %s p99 %s (%d sent, %d errors, %d dropped) · direct %.0f/s: p50 %s p95 %s p99 %s",
		gw.achieved, gw.lat.percentile(50), gw.lat.percentile(95), gw.lat.percentile(99), gw.sent, gw.errs, gw.dropped,
		direct.achieved, direct.lat.percentile(50), direct.lat.percentile(95), direct.lat.percentile(99))
	// A node that cannot keep ~95% of the offered rate is over budget even if
	// the surviving requests were fast.
	if gw.achieved < 0.95*float64(targetRPS) {
		pass = false
		detail += " (offered rate not sustained through the gateway)"
	}
	return Probe{
		Name:     "gateway-added-p99",
		Budget:   fmt.Sprintf("p%d < %s added @ %d rps/node here (reference budget: p99 < 5ms @ 5k)", assertP, limit, targetRPS),
		Observed: fmt.Sprintf("added p%d = %s @ %.0f rps (gateway p%d %s − direct p%d %s)", assertP, added.Round(10*time.Microsecond), gw.achieved, assertP, gw.lat.percentile(float64(assertP)).Round(10*time.Microsecond), assertP, direct.lat.percentile(float64(assertP)).Round(10*time.Microsecond)),
		Value:    float64(added.Microseconds()) / 1000,
		Limit:    float64(limit.Microseconds()) / 1000,
		Pass:     pass,
		Detail:   detail,
	}
}

type pacedResult struct {
	lat      *latencies
	sent     int64
	errs     int64
	dropped  int64
	achieved float64
	err      error
}

// probeDecide drives the server PDP endpoint (/v1/decide, the serverCheck
// and Tier-2 hook path) at a modest rate on the same instance. Report-only:
// The budgets cover the LOCAL hook path (hook-e2e) and the in-proc PDP. This keeps
// the swarm a true checkin+hooks+MCP mix and tracks the online-decide tail.
func probeDecide(w *strazad, tokens []string, targetRPS, workers int, dur time.Duration) Probe {
	if len(tokens) == 0 {
		return failed("decide-online", fmt.Errorf("no session tokens"))
	}
	client := newLoadClient(workers)
	res := runPaced(client, w.base+"/v1/decide", decideBody, tokens, targetRPS, workers, dur)
	if res.err != nil {
		return failed("decide-online", res.err)
	}
	return Probe{
		Name:     "decide-online",
		Budget:   "report-only (serverCheck / Tier-2 decide path)",
		Observed: fmt.Sprintf("p99 = %s @ %.0f rps (p50 %s, %d sent, %d errors)", res.lat.percentile(99).Round(10*time.Microsecond), res.achieved, res.lat.percentile(50), res.sent, res.errs),
		Value:    float64(res.lat.percentile(99).Microseconds()) / 1000,
		Limit:    0,
		Pass:     res.errs == 0,
	}
}

// runPaced drives url at targetRPS for dur with a catch-up pacer (coarse
// platform timers cannot tick at 5 kHz, so each wakeup releases the requests
// the target rate is owed). tokens == nil sends unauthenticated (the direct
// calibration); otherwise tokens rotate per request.
func runPaced(client *http.Client, url, body string, tokens []string, targetRPS, workers int, dur time.Duration) pacedResult {
	lat := &latencies{ns: make([]int64, 0, targetRPS*int(dur.Seconds())+workers)}
	var sent, errs, dropped int64
	var mu sync.Mutex

	ticks := make(chan struct{}, workers*4)
	stopPacer := make(chan struct{})
	var pacerWG sync.WaitGroup
	pacerWG.Add(1)
	go func() {
		defer pacerWG.Done()
		tk := time.NewTicker(2 * time.Millisecond)
		defer tk.Stop()
		start := time.Now()
		var issued int64
		for {
			select {
			case <-stopPacer:
				close(ticks)
				return
			case <-tk.C:
				owed := int64(time.Since(start).Seconds()*float64(targetRPS)) - issued
				for ; owed > 0; owed-- {
					select {
					case ticks <- struct{}{}:
					default:
						mu.Lock()
						dropped++
						mu.Unlock()
					}
					issued++
				}
			}
		}
	}()

	wantCode := http.StatusOK
	var wg sync.WaitGroup
	for wk := 0; wk < workers; wk++ {
		wg.Add(1)
		go func(wk int) {
			defer wg.Done()
			i := wk
			for range ticks {
				bearer := ""
				if len(tokens) > 0 {
					bearer = tokens[i%len(tokens)]
					i += workers
				}
				t0 := time.Now()
				code, err := postOnce(client, url, body, bearer)
				d := time.Since(t0)
				lat.add(d)
				mu.Lock()
				sent++
				if err != nil || code != wantCode {
					errs++
				}
				mu.Unlock()
			}
		}(wk)
	}

	time.Sleep(dur)
	close(stopPacer)
	pacerWG.Wait()
	wg.Wait()

	if sent == 0 {
		return pacedResult{err: fmt.Errorf("no requests completed")}
	}
	return pacedResult{lat: lat, sent: sent, errs: errs, dropped: dropped, achieved: float64(sent) / dur.Seconds()}
}

func postOnce(client *http.Client, url, body, bearer string) (int, error) {
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
