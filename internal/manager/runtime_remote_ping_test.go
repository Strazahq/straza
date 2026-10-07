package manager

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/store"
)

// shortPing is the ConnectTimeout these tests give a remote runtime, which
// bounds its health ping.
const shortPing = 300 * time.Millisecond

// within runs f and fails the test when f has not returned after d. It
// answers how long f took.
func within(t *testing.T, d time.Duration, what string, f func()) time.Duration {
	t.Helper()
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
		return time.Since(start)
	case <-time.After(d):
		t.Fatalf("%s still waits after %v", what, d)
		return 0
	}
}

// shortPings gives the runtime of the remote app name the short deadline and
// drops its pooled sessions, so the next check dials them under it.
func shortPings(mgr *Manager, name string) {
	mgr.mu.RLock()
	rt := mgr.byName[name].runtime.(*RemoteRuntime)
	mgr.mu.RUnlock()
	rt.ConnectTimeout = shortPing
	rt.mu.Lock()
	for key, s := range rt.pool {
		delete(rt.pool, key)
		_ = s.Close()
	}
	rt.mu.Unlock()
}

// evictions counts the ring's lines for a session evicted after an error.
func evictions(r *RemoteRuntime) int {
	n := 0
	for _, l := range r.Logs(256) {
		if l == "session evicted after error" {
			n++
		}
	}
	return n
}

// TestRemoteRuntimePingEachSession pins the health ping of a remote server:
// it pings the app-level session and every caller credential's session, each
// under a deadline of ConnectTimeout, answers for the app-level one, and
// evicts only a session whose own ping failed or ran out of time, with one
// ring line each. A ping pass that its caller cancels evicts nothing, the
// rule a call follows.
func TestRemoteRuntimePingEachSession(t *testing.T) {
	t.Parallel()
	app := &Secret{ID: "app", Value: "app-value"}
	callers := []*Secret{{ID: "a", Value: "a-value"}, {ID: "b", Value: "b-value"}}
	cases := []struct {
		name        string
		hang        string // whose pings get no answer: app, a, b, * for all, or none
		cancel      bool   // the caller cancels the pass while the pings wait
		wantErr     string // the start of the error, "" for none
		wantEvicted string // whose session is evicted, "" for none
	}{
		{name: "every session answers"},
		{name: "a caller credential's session stops answering", hang: "a", wantEvicted: "a"},
		{name: "the app-level session stops answering", hang: "app",
			wantErr: "the MCP server up did not answer a ping within 300ms", wantEvicted: "app"},
		{name: "the caller cancels the pass", hang: "*", cancel: true, wantErr: "context canceled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			up := newMethodUpstream(t)
			inject := &InjectSpec{As: InjectHeader, Name: "Authorization", Template: "{{secret}}"}
			r := besideRuntime("up", RemoteSpec{URL: up.URL, Auth: AuthInject}, inject)
			r.ConnectTimeout = shortPing
			t.Cleanup(r.Stop)
			ctx := context.Background()
			if err := r.Ping(ctx, app); err != nil {
				t.Fatalf("warm ping: %v", err)
			}
			for _, c := range callers {
				if _, err := r.Call(ctx, CallInput{Tool: "echo", Secret: c}); err != nil {
					t.Fatalf("warm call as %s: %v", c.ID, err)
				}
			}
			r.mu.Lock()
			sessions := map[string]*mcp.ClientSession{}
			for _, s := range append([]*Secret{app}, callers...) {
				sessions[s.ID] = r.pool[sessionKey(s)]
			}
			r.mu.Unlock()
			switch tc.hang {
			case "":
			case "*":
				up.hangPings(t, "*")
			default:
				up.hangPings(t, sessions[tc.hang].ID())
			}

			passCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			if tc.cancel {
				go func() {
					// Cancel once the three pings wait at the upstream, or
					// after a second when fewer ever arrive.
					for i := 0; i < 50 && up.count("ping") < 4; i++ {
						time.Sleep(20 * time.Millisecond)
					}
					cancel()
				}()
			}
			var err error
			within(t, 3*time.Second, "the ping pass", func() { err = r.Ping(passCtx, app) })
			if tc.wantErr == "" && err != nil {
				t.Fatalf("ping = %v, want nil", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.HasPrefix(err.Error(), tc.wantErr)) {
				t.Fatalf("ping = %v, want it to start %q", err, tc.wantErr)
			}
			if n := up.count("ping"); n != 4 {
				t.Errorf("pings the upstream got = %d, want 1 warm and 3 in the pass", n)
			}
			r.mu.Lock()
			for _, s := range append([]*Secret{app}, callers...) {
				kept := r.pool[sessionKey(s)] == sessions[s.ID]
				if kept == (s.ID == tc.wantEvicted) {
					t.Errorf("session of %s kept = %v, want %v", s.ID, kept, !kept)
				}
			}
			r.mu.Unlock()
			want := 0
			if tc.wantEvicted != "" {
				want = 1
			}
			if n := evictions(r); n != want {
				t.Errorf("eviction lines in the ring = %d, want %d", n, want)
			}
			// The eviction closes the session on its own goroutine, so its
			// DELETE may reach the upstream after Ping returned.
			waitFor(t, 2*time.Second, fmt.Sprintf("%d session terminations at the upstream", want),
				func() bool { return up.count(http.MethodDelete) == want })
		})
	}
}

// TestRemoteRuntimeStopDuringStalledPing pins that Stop completes while a
// health ping waits on a server that stopped answering: the ping ends at
// its deadline, and the session's close, whose DELETE the server never
// answers when it stopped altogether, ends within its own bound.
func TestRemoteRuntimeStopDuringStalledPing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		fault  func(t *testing.T, u *methodUpstream)
		within time.Duration
	}{
		{"the server stops answering pings", func(t *testing.T, u *methodUpstream) { u.hangPings(t, "*") }, 3 * time.Second},
		{"the server stops answering at all", func(t *testing.T, u *methodUpstream) { u.stall(t) }, 10 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			up := newMethodUpstream(t)
			r := besideRuntime("up", RemoteSpec{URL: up.URL}, nil)
			r.ConnectTimeout = shortPing
			t.Cleanup(r.Stop)
			if err := r.Ping(context.Background(), nil); err != nil {
				t.Fatalf("warm ping: %v", err)
			}
			tc.fault(t, up)
			pinged := make(chan error, 1)
			go func() { pinged <- r.Ping(context.Background(), nil) }()
			waitFor(t, 2*time.Second, "the ping at the upstream", func() bool { return up.count("ping") == 2 })

			t.Logf("Stop took %v", within(t, tc.within, "Stop", r.Stop))
			select {
			case err := <-pinged:
				if err == nil {
					t.Error("the stalled ping answered nil")
				}
			case <-time.After(tc.within):
				t.Fatalf("the ping still waits after %v", tc.within)
			}
			if r.Ready() {
				t.Error("the runtime reads ready after Stop")
			}
		})
	}
}

// TestHealthCheckMovesPastAStalledServer pins the health loop's bound on a
// remote server that stops answering: the pass ends within the ping deadline
// and what closing the session costs, the app turns degraded with a reason
// that says the ping timed out, and the next app is still checked. The next
// pass, which dials, says the handshake timed out when the server answers
// nothing, in one line. A server that fails its ping turns degraded with
// that failure as the reason.
func TestHealthCheckMovesPastAStalledServer(t *testing.T) {
	t.Parallel()
	const (
		timedOut  = "the MCP server a-stalled did not answer a ping within 300ms, so Straza closed its session"
		handshake = "the MCP server a-stalled did not answer the connect handshake within 300ms, so Straza dials it again"
	)
	cases := []struct {
		name       string
		fault      func(t *testing.T, u *methodUpstream)
		within     time.Duration
		wantReason string // the start of the reason, "" for any reason but a timeout
		wantNext   string // the same after the next pass
	}{
		{"the server stops answering pings", func(t *testing.T, u *methodUpstream) { u.hangPings(t, "*") }, 3 * time.Second, timedOut, timedOut},
		{"the server stops answering at all", func(t *testing.T, u *methodUpstream) { u.stall(t) }, 10 * time.Second, timedOut, handshake},
		{"the server fails its ping", func(_ *testing.T, u *methodUpstream) { u.failPings() }, 3 * time.Second, "", ""},
	}
	reasonIs := func(t *testing.T, pass, got, want string) {
		t.Helper()
		if strings.Contains(got, "\n") {
			t.Errorf("a-stalled reason after the %s pass has a line break: %q", pass, got)
		}
		if want != "" && !strings.HasPrefix(got, want) {
			t.Errorf("a-stalled reason after the %s pass = %q, want it to start %q", pass, got, want)
		}
		if want == "" && (got == "" || strings.Contains(got, "did not answer")) {
			t.Errorf("a-stalled reason after the %s pass = %q, want the ping's failure", pass, got)
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mgr, _ := testManager(t)
			ctx := context.Background()
			stalled := newMethodUpstream(t)
			for name, url := range map[string]string{"a-stalled": stalled.URL, "b-fine": newMethodUpstream(t).URL} {
				if _, err := mgr.Install(ctx, remoteManifest(t, name, url), store.AppSourceAPI); err != nil {
					t.Fatal(err)
				}
				waitStatus(t, mgr, name, StatusRunning)
				shortPings(mgr, name)
			}
			mgr.HealthCheck(ctx)
			before, _ := mgr.View("b-fine")
			if a, _ := mgr.View("a-stalled"); a.Status != StatusRunning || before.Status != StatusRunning {
				t.Fatalf("before the fault: a-stalled %s, b-fine %s, want both running", a.Status, before.Status)
			}
			tc.fault(t, stalled)

			t.Logf("the health pass took %v", within(t, tc.within, "the health pass", func() { mgr.HealthCheck(ctx) }))
			got, _ := mgr.View("a-stalled")
			if got.Status != StatusDegraded {
				t.Fatalf("a-stalled status = %s (%s), want degraded", got.Status, got.Detail)
			}
			reasonIs(t, "first", got.Detail, tc.wantReason)
			fine, _ := mgr.View("b-fine")
			if fine.Status != StatusRunning || !fine.LastProbe.After(before.LastProbe) {
				t.Errorf("b-fine after the pass: status %s, probed %v after %v", fine.Status, fine.LastProbe, before.LastProbe)
			}

			// The session is gone now, so the next pass dials a new one,
			// whose failed handshake must end in the same bound.
			t.Logf("the next health pass took %v", within(t, tc.within, "the next health pass", func() { mgr.HealthCheck(ctx) }))
			if got, _ := mgr.View("a-stalled"); got.Status != StatusDegraded {
				t.Errorf("a-stalled status after the next pass = %s, want degraded", got.Status)
			} else {
				reasonIs(t, "next", got.Detail, tc.wantNext)
			}
			if next, _ := mgr.View("b-fine"); !next.LastProbe.After(fine.LastProbe) {
				t.Errorf("b-fine was not probed in the next pass: %v, then %v", fine.LastProbe, next.LastProbe)
			}
		})
	}
}

// TestRemoteRuntimePingPassBounds pins what a health ping pass waits for. It
// never waits for a tool call still running on a session it evicts, and one
// eviction writes one ring line although the call then fails on the same
// session. It never waits for calls queued on the runtime lock whose own
// deadline passed, and a pass whose wait for the lock outlasted its deadline
// answers a sentence instead of dialing. A call with a rotated credential
// never waits for a call still running on the superseded session. The
// handshake and the ping each get ConnectTimeout, so a slow server that
// answers both in time reads healthy.
func TestRemoteRuntimePingPassBounds(t *testing.T) {
	t.Parallel()
	app, caller := &Secret{ID: "app", Value: "app-value"}, &Secret{ID: "a", Value: "a-value"}
	cases := []struct {
		name string
		// arrange sets the fault and answers the credential the pass pings
		// with and a check to run after the pass.
		arrange       func(t *testing.T, r *RemoteRuntime, up *methodUpstream) (*Secret, func())
		within        time.Duration
		wantErr       string // the start of the pass's error, "" for none
		wantEvictions int
	}{
		{"a tool call runs on the session the pass evicts", func(t *testing.T, r *RemoteRuntime, up *methodUpstream) (*Secret, func()) {
			if err := r.Ping(context.Background(), app); err != nil {
				t.Fatalf("warm ping: %v", err)
			}
			if _, err := r.Call(context.Background(), CallInput{Tool: "echo", Secret: caller}); err != nil {
				t.Fatalf("warm call: %v", err)
			}
			r.mu.Lock()
			s := r.pool[sessionKey(caller)]
			r.mu.Unlock()
			up.hangSession(s.ID())
			up.hangPings(t, s.ID())
			called := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_, err := r.Call(ctx, CallInput{Tool: "echo", Secret: caller})
				called <- err
			}()
			waitFor(t, 2*time.Second, "the call at the upstream", func() bool { return up.count("tools/call") == 2 })
			return app, func() {
				if err := <-called; err == nil {
					t.Error("the call on the stalled session answered nil")
				}
				r.mu.Lock()
				kept := r.pool[sessionKey(caller)] == s
				r.mu.Unlock()
				if kept {
					t.Error("the pool still holds the session whose ping timed out")
				}
			}
		}, time.Second, "", 1},
		{"calls wait on the lock behind a dial to a stalled server", func(t *testing.T, r *RemoteRuntime, up *methodUpstream) (*Secret, func()) {
			up.stall(t)
			for i := 0; i < 2; i++ {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), shortPing)
					defer cancel()
					_, _ = r.Call(ctx, CallInput{Tool: "echo"})
				}()
			}
			time.Sleep(100 * time.Millisecond)
			return nil, func() {
				if n := up.count("initialize"); n != 1 {
					t.Errorf("handshakes the upstream got = %d, want 1: a call whose deadline passed in the queue dialed", n)
				}
			}
		}, shortPing + 5*time.Second + shortPing + time.Second,
			"the MCP server up was not checked within 300ms, because Straza was still waiting on another connection attempt to it", 0},
		{"a credential rotates while a call runs on its old session", func(t *testing.T, r *RemoteRuntime, up *methodUpstream) (*Secret, func()) {
			old := &Secret{ID: "a", Value: "a-old"}
			if _, err := r.Call(context.Background(), CallInput{Tool: "echo", Secret: old}); err != nil {
				t.Fatalf("warm call: %v", err)
			}
			r.mu.Lock()
			s := r.pool[sessionKey(old)]
			r.mu.Unlock()
			up.hangSession(s.ID())
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_, _ = r.Call(ctx, CallInput{Tool: "echo", Secret: old})
			}()
			waitFor(t, 2*time.Second, "the call at the upstream", func() bool { return up.count("tools/call") == 2 })
			// The sweep of the superseded session must not wait for the call
			// still running on it, holding the lock and the new call's time.
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			var err error
			took := within(t, time.Second, "the call with the rotated value", func() {
				_, err = r.Call(ctx, CallInput{Tool: "echo", Secret: &Secret{ID: "a", Value: "a-new"}})
			})
			if err != nil {
				t.Errorf("the call with the rotated value took %v and answered %v, want nil", took, err)
			}
			return nil, func() {}
		}, time.Second, "", 0},
		{"the handshake and the ping are slow but each answers in time", func(t *testing.T, r *RemoteRuntime, up *methodUpstream) (*Secret, func()) {
			r.ConnectTimeout = 600 * time.Millisecond
			up.slowDown("initialize", 400*time.Millisecond)
			up.slowDown("ping", 400*time.Millisecond)
			return nil, func() {
				if err := r.Ping(context.Background(), nil); err != nil {
					t.Errorf("the second pass: %v", err)
				}
				if n := up.count("initialize"); n != 1 {
					t.Errorf("handshakes = %d, want 1: the session was not kept", n)
				}
			}
		}, 2 * time.Second, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			up := newMethodUpstream(t)
			// No injection: each secret still keys its own session, and the
			// calls of the queued row may run uncredentialed.
			r := besideRuntime("up", RemoteSpec{URL: up.URL}, nil)
			r.ConnectTimeout = shortPing
			t.Cleanup(r.Stop)
			secret, after := tc.arrange(t, r, up)
			var err error
			t.Logf("the pass took %v", within(t, tc.within, "the ping pass", func() { err = r.Ping(context.Background(), secret) }))
			if tc.wantErr == "" && err != nil {
				t.Errorf("ping = %v, want nil", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.HasPrefix(err.Error(), tc.wantErr)) {
				t.Errorf("ping = %v, want it to start %q", err, tc.wantErr)
			}
			after()
			if n := evictions(r); n != tc.wantEvictions {
				t.Errorf("eviction lines in the ring = %d, want %d", n, tc.wantEvictions)
			}
		})
	}
}

// TestRemoteRuntimeHandshakeReason pins the sentence a dial that ran out of
// time answers: one line that names the handshake and the dial's own bound,
// ConnectTimeout, or the caller's deadline when that is sooner, as a
// manifest's shorter call timeout makes it.
func TestRemoteRuntimeHandshakeReason(t *testing.T) {
	t.Parallel()
	sentence := regexp.MustCompile(`^the MCP server up did not answer the connect handshake within (\S+), so Straza dials it again on the next call or health check\. ` +
		`An administrator checks that the server runs and answers at the address in its manifest$`)
	cases := []struct {
		name     string
		deadline time.Duration // the caller's, 0 for none
		min, max time.Duration // the bound the sentence may name
	}{
		{"the dial's own timeout", 0, shortPing, shortPing},
		{"a caller deadline sooner than the dial's", 200 * time.Millisecond, 150 * time.Millisecond, 200 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			up := newMethodUpstream(t)
			r := besideRuntime("up", RemoteSpec{URL: up.URL}, nil)
			r.ConnectTimeout = shortPing
			t.Cleanup(r.Stop)
			up.stall(t)
			ctx := context.Background()
			if tc.deadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.deadline)
				defer cancel()
			}
			_, err := r.Call(ctx, CallInput{Tool: "echo"})
			if err == nil {
				t.Fatal("a call to a stalled server answered nil")
			}
			m := sentence.FindStringSubmatch(err.Error())
			if m == nil {
				t.Fatalf("error = %q, want the handshake sentence", err)
			}
			if d, perr := time.ParseDuration(m[1]); perr != nil || d < tc.min || d > tc.max {
				t.Errorf("the sentence names %s, want a bound from %v to %v", m[1], tc.min, tc.max)
			}
		})
	}
}

// TestRemoteRuntimeCloseIgnoresDeleteRedirect pins that the DELETE ending a
// session never follows a redirect: net/http would send the hop as a GET
// with no deadline, so a server that redirects it would hold Close.
func TestRemoteRuntimeCloseIgnoresDeleteRedirect(t *testing.T) {
	t.Parallel()
	up := newMethodUpstream(t)
	r := besideRuntime("up", RemoteSpec{URL: up.URL}, nil)
	r.ConnectTimeout = shortPing
	t.Cleanup(r.Stop)
	s, _, err := r.session(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	up.redirectDeletes(t)
	within(t, 2*time.Second, "Close", func() { _ = s.Close() })
	if n := up.count(http.MethodDelete); n != 1 {
		t.Errorf("DELETEs the upstream saw = %d, want 1", n)
	}
	if n := up.count(http.MethodGet); n != 0 {
		t.Errorf("GETs the upstream saw after the redirect = %d, want 0", n)
	}
}
