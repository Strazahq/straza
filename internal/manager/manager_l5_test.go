package manager

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestRecheckWhoseRequestEndsSaysWhy pins the reason a recheck records when
// the request that asked for it ends while a dial to a stopped remote server
// holds the runtime lock: a sentence that says what failed, why and what to
// do next, never the bare context error. A recheck whose request stays keeps
// the sentence of a pass that waited too long to be checked.
func TestRecheckWhoseRequestEndsSaysWhy(t *testing.T) {
	t.Parallel()
	ended := regexp.MustCompile(`^the health check of the MCP server a-stopped ended after [0-9.]+m?s with no answer from the server, ` +
		`because the request that asked for it ended while Straza was still waiting on the server or on another connection attempt to it\. ` +
		`An administrator checks that the server runs and answers at the address in its manifest, then runs strazactl apps recheck a-stopped again$`)
	const notChecked = "the MCP server a-stopped was not checked within 300ms, because Straza was still waiting on another connection attempt to it"
	cases := []struct {
		name string
		// leaveAfter is when the request that asked for the recheck ends,
		// 0 for a request that stays.
		leaveAfter time.Duration
		wantEnded  bool
	}{
		{"the request ends after the pass's own deadline", time.Second, true},
		{"the request ends before the pass's own deadline", 100 * time.Millisecond, true},
		{"the request stays", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mgr, _ := testManager(t)
			up := newMethodUpstream(t)
			if _, err := mgr.Install(context.Background(), remoteManifest(t, "a-stopped", up.URL), store.AppSourceAPI); err != nil {
				t.Fatal(err)
			}
			waitStatus(t, mgr, "a-stopped", StatusRunning)
			shortPings(mgr, "a-stopped")
			mgr.mu.RLock()
			rt := mgr.byName["a-stopped"].runtime.(*RemoteRuntime)
			mgr.mu.RUnlock()
			up.stall(t)

			// A call dials the stopped server and holds the runtime lock for
			// the handshake's bound, the SDK's cancel notice and the DELETE.
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_, _ = rt.Call(ctx, CallInput{Tool: "echo"})
			}()
			waitFor(t, 2*time.Second, "the call's handshake at the upstream", func() bool { return up.count("initialize") == 2 })

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.leaveAfter > 0 {
				time.AfterFunc(tc.leaveAfter, cancel)
			}
			var v AppView
			t.Logf("the recheck took %v", within(t, shortPing+5*time.Second+shortPing+2*time.Second, "the recheck", func() {
				v, _ = mgr.HealthCheckOne(ctx, "a-stopped")
			}))
			if v.Status != StatusDegraded {
				t.Fatalf("status = %s (%s), want degraded", v.Status, v.Detail)
			}
			if tc.wantEnded && !ended.MatchString(v.Detail) {
				t.Errorf("reason = %q, want the sentence of a check whose request ended", v.Detail)
			}
			if !tc.wantEnded && !strings.HasPrefix(v.Detail, notChecked) {
				t.Errorf("reason = %q, want it to start %q", v.Detail, notChecked)
			}
		})
	}
}

// TestProbeWhoseContextEndsSaysWhy pins the reason of a probe whose own
// context ends while the tool listing that follows the handshake and the
// ping waits: never the bare context error. A request that ends, a
// recheck's, an install's, an enable's or a secret change's, records the
// sentence of a check whose request ended. A converge start whose own
// deadline ends records the listing sentence with the time it waited. The
// health loop cut short by strazad's own stop records nothing.
func TestProbeWhoseContextEndsSaysWhy(t *testing.T) {
	t.Parallel()
	const app, slowList = "a-slow", 2 * time.Second
	request := regexp.MustCompile(`^the health check of the MCP server a-slow ended after [0-9.]+m?s with no answer from the server, ` +
		`because the request that asked for it ended while Straza was still waiting on the server or on another connection attempt to it\. ` +
		`An administrator checks that the server runs and answers at the address in its manifest, then runs strazactl apps recheck a-slow again$`)
	listing := regexp.MustCompile(`^the MCP server a-slow did not answer its tool listing within [1-5][0-9]0ms, so Straza closed its session ` +
		`and lists its tools again at the next health check\. An administrator checks that the server runs and answers at the address in its manifest$`)
	bg := context.Background()
	install := func(t *testing.T, mgr *Manager, up *methodUpstream) {
		if _, err := mgr.Install(bg, remoteManifest(t, app, up.URL), store.AppSourceAPI); err != nil {
			t.Fatal(err)
		}
		waitStatus(t, mgr, app, StatusRunning)
	}
	// running answers a manager whose a-slow runs with a pooled session and
	// a ConnectTimeout of 3 s, and whose listing now takes 2 s.
	running := func(t *testing.T) *Manager {
		mgr, _ := testManager(t)
		up := newMethodUpstream(t)
		install(t, mgr, up)
		mgr.mu.RLock()
		mgr.byName[app].runtime.(*RemoteRuntime).ConnectTimeout = 3 * time.Second
		mgr.mu.RUnlock()
		up.slowDown("tools/list", slowList)
		return mgr
	}
	// leaving answers a context that ends 500 ms from now, as a request
	// whose client leaves.
	leaving := func(t *testing.T) context.Context {
		ctx, cancel := context.WithCancel(bg)
		t.Cleanup(cancel)
		time.AfterFunc(500*time.Millisecond, cancel)
		return ctx
	}
	cases := []struct {
		name string
		// run makes the row's call and answers the manager whose a-slow the
		// row reads.
		run  func(t *testing.T) *Manager
		want *regexp.Regexp // nil: nothing is recorded, a-slow stays running
	}{
		{"a recheck whose request ends", func(t *testing.T) *Manager {
			mgr := running(t)
			mgr.HealthCheckOne(leaving(t), app)
			return mgr
		}, request},
		{"an install whose request ends", func(t *testing.T) *Manager {
			mgr, _ := testManager(t)
			up := newMethodUpstream(t)
			up.slowDown("tools/list", slowList)
			if _, err := mgr.Install(leaving(t), remoteManifest(t, app, up.URL), store.AppSourceAPI); err != nil {
				t.Fatal(err)
			}
			return mgr
		}, request},
		{"an enable whose request ends", func(t *testing.T) *Manager {
			mgr := running(t)
			if _, err := mgr.Disable(bg, app); err != nil {
				t.Fatal(err)
			}
			if _, err := mgr.Enable(leaving(t), app); err != nil {
				t.Fatal(err)
			}
			return mgr
		}, request},
		{"a secret set or delete whose request ends", func(t *testing.T) *Manager {
			mgr := running(t)
			v, _ := mgr.View(app)
			mgr.SecretUpdated(leaving(t), v.ID)
			return mgr
		}, request},
		{"a replica's converge start whose own deadline ends", func(t *testing.T) *Manager {
			a, b, _ := twoReplicas(t)
			up := newMethodUpstream(t)
			install(t, a, up)
			up.slowDown("tools/list", slowList)
			rows, err := b.opts.Store.Apps().List(bg)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(bg, 500*time.Millisecond)
			defer cancel()
			b.StartMissing(ctx, rows, nil)
			return b
		}, listing},
		{"the health loop that strazad's stop cuts short", func(t *testing.T) *Manager {
			mgr := running(t)
			mgr.HealthCheck(leaving(t))
			return mgr
		}, nil},
		{"the health loop after strazad stopped", func(t *testing.T) *Manager {
			mgr := running(t)
			ctx, cancel := context.WithCancel(bg)
			cancel()
			mgr.HealthCheck(ctx)
			return mgr
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mgr *Manager
			within(t, slowList+2*time.Second, "the call", func() { mgr = tc.run(t) })
			v, ok := mgr.View(app)
			if !ok {
				t.Fatal("a-slow has no instance")
			}
			if tc.want == nil {
				degraded := 0
				lines, _ := mgr.Logs(app, 256)
				for _, l := range lines {
					if strings.HasPrefix(l, "status → degraded") {
						degraded++
					}
				}
				if v.Status != StatusRunning || v.Detail != "" || degraded != 0 {
					t.Errorf("status %s, reason %q, degraded lines in the ring %d; want running, no reason and none", v.Status, v.Detail, degraded)
				}
				return
			}
			if v.Status != StatusDegraded || !tc.want.MatchString(v.Detail) {
				t.Errorf("status %s, reason %q; want degraded with %s", v.Status, v.Detail, tc.want)
			}
		})
	}
}
