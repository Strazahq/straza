package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// probeKillSwitch measures revocation propagation over the GATEWAY-EDGE push
// lane: every simulated daemon is a REAL SSE subscription (one TCP connection
// and one real session on /v1/push), not a NATS sublist entry. Latency runs
// from the admin revoke POST to the revocation event read off that session's
// own stream. Two shapes are measured: the per-session kill (distribution
// over `revokes` calls) and ONE bulk stand-down fanning out to its whole set.
// Topology is part of the number and is stated in the budget line:
//
//	-kill-pods 1 (default): one standalone pod, SQLite, self-contained.
//	-kill-pods N>1: N pods sharing external NATS and a fresh Postgres database
//	  (STRAZA_TEST_POSTGRES_DSN required, same convention as the HA tests);
//	  the fleet spreads across pods while every revoke lands on pod 0, so
//	  arrivals on other pods prove the cross-pod spine fan-out.
//	-kill-rtt D: a WAN-shaped one-way delay (D/2) injected on every probe-side
//	  read, so the SSE arrival pays it exactly once like a real daemon.
func probeKillSwitch(ctx context.Context, dataDir, natsDir string, clients, revokes, pods int, rtt time.Duration) Probe {
	if pods < 1 {
		pods = 1
	}
	if revokes > clients {
		revokes = clients
	}
	name := "killswitch-p99"
	topology := fmt.Sprintf("%d pod(s), %d real edge conns, rtt +%s", pods, clients, rtt)

	// A real NATS with a TCP listener: the spine every pod shares.
	ns, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1,
		JetStream: true, StoreDir: natsDir,
		NoLog: true, NoSigs: true,
	})
	if err != nil {
		return failed(name, err)
	}
	ns.Start()
	defer ns.Shutdown()
	if !ns.ReadyForConnections(10 * time.Second) {
		return failed(name, fmt.Errorf("nats not ready"))
	}

	// Multi-pod needs a shared Postgres (SQLite is the single-pod driver).
	dsn := ""
	if pods > 1 {
		base := os.Getenv("STRAZA_TEST_POSTGRES_DSN")
		if base == "" {
			return failed(name, fmt.Errorf("-kill-pods %d requires STRAZA_TEST_POSTGRES_DSN", pods))
		}
		fresh, drop, err := freshProbeDB(base)
		if err != nil {
			return failed(name, err)
		}
		defer drop()
		dsn = fresh
	}

	// Pods share the NATS spine, the store, and ONE issuer (the LB identity
	// production pods have); pod 0 boots first and migrates/bootstraps.
	issuer := "http://straza-kill-probe.local"
	podList := make([]*strazad, 0, pods)
	defer func() {
		for _, w := range podList {
			w.stop()
		}
	}()
	for i := 0; i < pods; i++ {
		o := strazadOpts{dataDir: filepath.Join(dataDir, fmt.Sprintf("pod%d", i)), natsURL: ns.ClientURL()}
		if pods > 1 {
			o.pgDSN = dsn
			o.issuer = issuer
		}
		if err := os.MkdirAll(o.dataDir, 0o700); err != nil {
			return failed(name, err)
		}
		w, err := bootStrazad(ctx, o)
		if err != nil {
			return failed(name, fmt.Errorf("pod %d: %w", i, err))
		}
		podList = append(podList, w)
	}
	pod0 := podList[0]

	adminTok, err := pod0.adminIDToken(ctx)
	if err != nil {
		return failed(name, err)
	}
	users, err := mintUserTokens(pod0, ctx, min(clients, 200), "kill-dev")
	if err != nil {
		return failed(name, err)
	}

	// The fleet: `clients` REAL sessions, round-robin across pods, each
	// holding a live SSE subscription. Reads pay the injected one-way delay.
	checkinClient := newLoadClient(64)
	sseClient := &http.Client{Transport: &http.Transport{
		DialContext:         delayDialer(rtt / 2),
		MaxIdleConnsPerHost: 0,
		MaxConnsPerHost:     0,
	}}
	type member struct {
		sid string
		tok string
		pod *strazad
	}
	fleet := make([]member, clients)
	for i := range fleet {
		w := podList[i%pods]
		sid, tok, err := w.checkin(checkinClient, users[i%len(users)].idToken)
		if err != nil {
			return failed(name, fmt.Errorf("checkin %d: %w", i, err))
		}
		fleet[i] = member{sid: sid, tok: tok, pod: w}
	}

	arrivals := sync.Map{} // session id → chan time.Time (buffered 1)
	var ready, fleetErrs atomic.Int64
	streamCtx, stopStreams := context.WithCancel(ctx)
	defer stopStreams()
	var streams sync.WaitGroup
	for _, m := range fleet {
		ch := make(chan time.Time, 1)
		arrivals.Store(m.sid, ch)
		streams.Add(1)
		go func(m member, ch chan time.Time) {
			defer streams.Done()
			req, _ := http.NewRequestWithContext(streamCtx, http.MethodGet, m.pod.base+"/v1/push", nil)
			req.Header.Set("Authorization", "Bearer "+m.tok)
			resp, err := sseClient.Do(req)
			if err != nil {
				fleetErrs.Add(1)
				return
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				fleetErrs.Add(1)
				return
			}
			sc := bufio.NewScanner(resp.Body)
			for sc.Scan() {
				switch {
				case strings.HasPrefix(sc.Text(), "event: ready"):
					ready.Add(1)
				case strings.HasPrefix(sc.Text(), "event: revocation"):
					select {
					case ch <- time.Now():
					default:
					}
					return
				}
			}
		}(m, ch)
	}
	deadline := time.Now().Add(60 * time.Second)
	for int(ready.Load()) < clients {
		if fleetErrs.Load() > 0 {
			return failed(name, fmt.Errorf("%d fleet subscriptions failed", fleetErrs.Load()))
		}
		if time.Now().After(deadline) {
			return failed(name, fmt.Errorf("fleet not ready: %d/%d subscribed", ready.Load(), clients))
		}
		time.Sleep(20 * time.Millisecond)
	}

	// --- Shape 1: per-session kills (operator single revokes), modest
	// parallelism, latency distribution POST-start → SSE arrival.
	singles := fleet[:revokes]
	lat := &latencies{ns: make([]int64, 0, revokes)}
	var missed atomic.Int64
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, m := range singles {
		wg.Add(1)
		sem <- struct{}{}
		go func(m member) {
			defer wg.Done()
			defer func() { <-sem }()
			chAny, _ := arrivals.Load(m.sid)
			ch := chAny.(chan time.Time)
			t0 := time.Now()
			req, _ := http.NewRequest(http.MethodPost, pod0.base+"/v1/admin/sessions/"+m.sid+"/revoke", nil)
			req.Header.Set("Authorization", "Bearer "+adminTok)
			resp, err := checkinClient.Do(req)
			if err != nil {
				missed.Add(1)
				return
			}
			_ = resp.Body.Close()
			select {
			case t1 := <-ch:
				lat.add(t1.Sub(t0))
			case <-time.After(5 * time.Second):
				missed.Add(1)
			}
		}(m)
	}
	wg.Wait()
	if lat.count() == 0 {
		return failed(name, fmt.Errorf("no pushes arrived (%d missed)", missed.Load()))
	}

	// --- Shape 2: ONE bulk stand-down over the rest of the fleet
	// (capped at the per-call limit); p99 arrival from the single POST.
	bulk := fleet[revokes:]
	if len(bulk) > 1000 {
		bulk = bulk[:1000]
	}
	bulkLat := &latencies{ns: make([]int64, 0, len(bulk))}
	var bulkMissed int
	if len(bulk) > 0 {
		ids := make([]string, len(bulk))
		for i, m := range bulk {
			ids[i] = m.sid
		}
		body := fmt.Sprintf(`{"sessions":[%s],"reason":"kill probe"}`, `"`+strings.Join(ids, `","`)+`"`)
		t0 := time.Now()
		req, _ := http.NewRequest(http.MethodPost, pod0.base+"/v1/admin/sessions/revoke", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+adminTok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := checkinClient.Do(req)
		if err != nil {
			return failed(name, fmt.Errorf("bulk revoke: %w", err))
		}
		_ = resp.Body.Close()
		for _, m := range bulk {
			chAny, _ := arrivals.Load(m.sid)
			select {
			case t1 := <-chAny.(chan time.Time):
				bulkLat.add(t1.Sub(t0))
			case <-time.After(10 * time.Second):
				bulkMissed++
			}
		}
	}

	p99 := lat.percentile(99)
	pass := p99 < 2*time.Second && missed.Load() == 0 && bulkMissed == 0
	detail := fmt.Sprintf("real SSE conns on /v1/push (32-conn sublist sim retired); reads pay a %s one-way delay; bulk stand-down: %d sessions in ONE call, arrival p99 %s (%d missed)",
		(rtt / 2).Round(time.Millisecond), bulkLat.count()+bulkMissed, bulkLat.percentile(99).Round(time.Millisecond), bulkMissed)
	return Probe{
		Name:     name,
		Budget:   fmt.Sprintf("p99 < 2s @ %s", topology),
		Observed: fmt.Sprintf("p99 = %s (p50 %s, max %s, %d single revokes, %d missed)", p99.Round(time.Millisecond), lat.percentile(50).Round(time.Millisecond), lat.max().Round(time.Millisecond), revokes, missed.Load()),
		Value:    p99.Seconds(),
		Limit:    2,
		Pass:     pass,
		Detail:   detail,
	}
}

// delayDialer wraps every dialed conn so reads deliver one WAN-shaped
// one-way delay late; sparse SSE traffic pays it exactly once per event.
// Zero delay = plain dialer.
func delayDialer(oneWay time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 10 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := d.DialContext(ctx, network, addr)
		if err != nil || oneWay <= 0 {
			return c, err
		}
		return delayedConn{Conn: c, d: oneWay}, nil
	}
}

type delayedConn struct {
	net.Conn
	d time.Duration
}

func (c delayedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		time.Sleep(c.d)
	}
	return n, err
}

// freshProbeDB creates a throwaway database on the shared PG server (same
// convention as freshPostgresDSN in the server tests) and returns its DSN
// plus a dropper.
func freshProbeDB(base string) (string, func(), error) {
	admin, err := sql.Open("pgx", base)
	if err != nil {
		return "", nil, err
	}
	dbName := fmt.Sprintf("straza_kill_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + dbName); err != nil {
		_ = admin.Close()
		return "", nil, fmt.Errorf("create probe db: %w", err)
	}
	u, err := url.Parse(base)
	if err != nil {
		_ = admin.Close()
		return "", nil, err
	}
	u.Path = "/" + dbName
	drop := func() {
		_, _ = admin.Exec("DROP DATABASE " + dbName + " WITH (FORCE)")
		_ = admin.Close()
	}
	return u.String(), drop, nil
}
