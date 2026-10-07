package agentguard

import (
	"context"
	"crypto/ed25519"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// refusedPolicyPhrase is the fixed part of the deny a person sees once a
// refused policy fetch has outlived the session time held at the refusal.
const refusedPolicyPhrase = "this session's policy is out of date: the server holds a newer policy that straza could not fetch"

// srvOriginPlaceholder stands for the fake server's origin in an expected
// reason, which each row knows only once its server runs.
const srvOriginPlaceholder = "<origin>"

// testEnterprisePolicy compiles and signs a policy with the enterprise
// offline grace of zero, so the offline gate denies the moment a session
// time runs out.
func testEnterprisePolicy(t *testing.T, priv ed25519.PrivateKey, doc string) ([]byte, string) {
	t.Helper()
	snap, err := policy.Compile(policy.CompileInput{
		Documents: [][]byte{[]byte(doc)}, LocalDefault: "allow", MaxAge: 0,
		CreatedUnix: time.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	signed, id, err := snap.Sign("k1", priv)
	if err != nil {
		t.Fatal(err)
	}
	return signed, id
}

// shellEvent is a governed shell call running command.
func shellEvent(command string) policy.Event {
	return policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: command}
}

// decideNow decides from the stored state with no renewal, which is what the
// offline gate alone says.
func decideNow(t *testing.T, store *Store, command string) policy.Decision {
	t.Helper()
	ses, err := store.LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	pdp, err := NewLocalPDP(store, ses.Subject())
	if err != nil {
		t.Fatal(err)
	}
	return pdp.Decide(Normalized{Event: shellEvent(command)})
}

// seedSession stores the session a lane starts from: pinned to snapID,
// holding token tok-seed, with its session time ending at expires.
func seedSession(t *testing.T, store *Store, snapID string, expires time.Time) {
	t.Helper()
	if err := store.SaveSession(Session{
		SessionID: "s-seed", SessionToken: "tok-seed", SnapshotID: snapID,
		User: "kim", Roles: []string{"dev"}, Harness: "claude-code/2.1.0",
		IssuedAt: time.Now().Add(-time.Hour), ExpiresAt: expires,
	}); err != nil {
		t.Fatal(err)
	}
}

// renewalLanes are the places that renew a session, each with the session
// time it starts from and whether the server refuses the token refresh.
// enters says whether a refused fetch starts a refusal there: the drain
// pulse renews nothing and cannot tell a newer policy from an expired
// token, so it only ever ends one.
var renewalLanes = []struct {
	name          string
	seed          time.Duration
	refreshStatus int
	enters        bool
	run           func(ctx context.Context, t *testing.T, store *Store, serverURL string, out io.Writer) error
}{
	{name: "hook refresh", seed: 30 * time.Second, enters: true, run: hookRefresh},
	{name: "hook re-acquire", seed: -time.Hour, refreshStatus: http.StatusUnauthorized, enters: true, run: hookRefresh},
	{name: "daemon tick", seed: time.Hour, enters: true, run: daemonTick},
	{name: "daemon re-acquire", seed: time.Hour, refreshStatus: http.StatusUnauthorized, enters: true, run: daemonTick},
	{name: "MCP proxy and exec mint", seed: -time.Hour, enters: true,
		run: func(ctx context.Context, _ *testing.T, store *Store, serverURL string, _ io.Writer) error {
			_, err := ensureSession(ctx, store, NewClient(serverURL), "claude-code")
			return err
		}},
	{name: "MCP proxy token refresh", seed: time.Hour, enters: true, run: proxyRefresh},
	{name: "drain pulse", seed: time.Hour,
		run: func(context.Context, *testing.T, *Store, string, io.Writer) error {
			_, err := DrainOnce(5 * time.Second)
			return err
		}},
}

// proxyRefresh runs the token refresh of the MCP proxy and the session API,
// which a 401 from the server triggers.
func proxyRefresh(ctx context.Context, _ *testing.T, store *Store, serverURL string, _ io.Writer) error {
	ses, err := store.LoadSession()
	if err != nil {
		return err
	}
	tr := &sessionAuthTransport{base: http.DefaultTransport, store: store, client: NewClient(serverURL), harness: "claude-code"}
	_, err = tr.refresh(ctx, ses)
	return err
}

// TestRefusedPolicyFetchEndsInDeny pins that a session that renews while the
// server refuses to send the newer policy does not decide on the old one for
// ever: once the session time held at the refusal has run out, every
// governed call denies with the server's sentence, however long ago the
// session expired.
func TestRefusedPolicyFetchEndsInDeny(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	blobA, idA := testEnterprisePolicy(t, priv, pinPolicyA)
	blobB, idB := testEnterprisePolicy(t, priv, pinPolicyB)
	g := &tokenGatedServer{blob: blobB, id: idB, refreshStatus: http.StatusUnauthorized, refuseAll: true}
	srv := httptest.NewServer(g.handler(t))
	defer srv.Close()
	store := pinStore(t, srv.URL, keys)
	if err := store.SaveSnapshot(blobA); err != nil {
		t.Fatal(err)
	}
	for days := 1; days <= 3; days++ {
		seedSession(t, store, idA, time.Now().Add(-time.Duration(days)*24*time.Hour))
		// b-cmd is what the server's policy denies, a-cmd what the stale
		// one denies, and git status what both allow.
		for _, command := range []string{"b-cmd /x", "a-cmd /x", "git status"} {
			d := decideAfterRefresh(t, store, shellEvent(command))
			if d.Effect != policy.EffectDeny || !strings.Contains(d.Reason, refusedPolicyPhrase) || !strings.Contains(d.Reason, gatedRefusal) {
				t.Errorf("session expired %dd ago, %q = %s %q, want the refused-policy deny with the server's sentence", days, command, d.Effect, d.Reason)
			}
		}
	}
}

// TestRenewalPolicyRefusalByLane pins, for every place that renews, which
// answers of the snapshot route start a refusal: a server refusal below 500,
// a redirect the client will not follow, and a snapshot the pinned keys
// cannot verify, each for a policy other than the one held. It keeps the
// reason and the session time held before the renewal as the deadline. A
// 503 and a dropped connection keep the old behaviour, so an outage never
// stops work, and a client that holds the policy the server reports never
// enters it.
func TestRenewalPolicyRefusalByLane(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	blobA, idA := testEnterprisePolicy(t, priv, pinPolicyA)
	blobB, idB := testEnterprisePolicy(t, priv, pinPolicyB)

	outcomes := []struct {
		name   string
		server func(g *tokenGatedServer)
		// why is the reason a refusal keeps, "" when the answer starts none.
		why string
	}{
		{"server refuses the newer policy", func(g *tokenGatedServer) { g.refuseAll = true },
			"The server refused it: " + gatedRefusal},
		{"server refuses with a bare status", func(g *tokenGatedServer) { g.failStatus = http.StatusTooManyRequests },
			"The server refused it with HTTP 429 and gave no reason."},
		{"redirect to another origin", func(g *tokenGatedServer) { g.redirectTo = "http://127.0.0.1:1/v1/snapshot" },
			"The fetch was redirected: " + srvOriginPlaceholder + " answered with a redirect to http://127.0.0.1:1"},
		{"redirect loop on the same origin", func(g *tokenGatedServer) { g.redirectTo = "/v1/snapshot" },
			"The fetch was redirected 10 times in a row and never reached the policy."},
		{"redirect with no Location", func(g *tokenGatedServer) { g.failStatus = http.StatusFound },
			"The server refused it with HTTP 302 and gave no reason."},
		{"snapshot the pinned keys cannot verify", func(g *tokenGatedServer) { g.blob = []byte("not a signed snapshot") },
			"The policy the server sent does not verify against the snapshot keys this machine pinned, so straza will not enforce it."},
		{"server error 503", func(g *tokenGatedServer) { g.failStatus = http.StatusServiceUnavailable }, ""},
		{"network failure", func(g *tokenGatedServer) { g.dropSnapshot = true }, ""},
		{"client holds the reported policy", func(g *tokenGatedServer) { g.blob, g.id, g.refuseAll = blobA, idA, true }, ""},
	}
	for _, lane := range renewalLanes {
		for _, oc := range outcomes {
			t.Run(lane.name+", "+oc.name, func(t *testing.T) {
				g := &tokenGatedServer{blob: blobB, id: idB, refreshStatus: lane.refreshStatus}
				oc.server(g)
				srv := httptest.NewServer(g.handler(t))
				defer srv.Close()
				store := pinStore(t, srv.URL, keys)
				if err := store.SaveSnapshot(blobA); err != nil {
					t.Fatal(err)
				}
				held := time.Now().Add(lane.seed).Truncate(time.Second)
				seedSession(t, store, idA, held)
				g.mu.Lock()
				g.live = "tok-seed"
				g.mu.Unlock()

				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				_ = lane.run(ctx, t, store, srv.URL, io.Discard)

				ses, err := store.LoadSession()
				if err != nil {
					t.Fatalf("no session after the lane: %v", err)
				}
				if ses.SnapshotID != idA {
					t.Errorf("pin = %q, want the held policy %q", ses.SnapshotID, idA)
				}
				if oc.why == "" || !lane.enters {
					if ses.PolicyRefused != "" || !ses.PolicyDeadline.IsZero() {
						t.Fatalf("refusal entered: %q until %v, want none", ses.PolicyRefused, ses.PolicyDeadline)
					}
					if d := decideNow(t, store, "git status"); d.Effect != policy.EffectAllow {
						t.Errorf("git status = %s %q, want the old behaviour's allow", d.Effect, d.Reason)
					}
					return
				}
				why := strings.ReplaceAll(oc.why, srvOriginPlaceholder, srv.URL)
				if !strings.HasPrefix(ses.PolicyRefused, why) || !ses.PolicyDeadline.Equal(held) {
					t.Fatalf("refusal = %q until %v, want %q until %v", ses.PolicyRefused, ses.PolicyDeadline, why, held)
				}
				// Before the held session time runs out the old policy still
				// decides; after it, the gate denies with the sentence.
				if held.After(time.Now()) {
					if d := decideNow(t, store, "git status"); d.Effect != policy.EffectAllow {
						t.Errorf("git status before the deadline = %s %q, want allow", d.Effect, d.Reason)
					}
					ses.PolicyDeadline = time.Now().Add(-time.Second)
					if err := store.SaveSession(ses); err != nil {
						t.Fatal(err)
					}
				}
				if d := decideNow(t, store, "git status"); d.Effect != policy.EffectDeny ||
					!strings.Contains(d.Reason, refusedPolicyPhrase) || !strings.Contains(d.Reason, why) {
					t.Errorf("git status past the deadline = %s %q, want the refused-policy deny with %q", d.Effect, d.Reason, why)
				}
			})
		}
	}
}

// TestRefusedPolicyRecoversOnFetch pins the way out: after a refused fetch
// has turned the hook lane to deny, the first renewal lane whose fetch
// succeeds adopts the server's policy and ends the refusal, with no new
// enrolment.
func TestRefusedPolicyRecoversOnFetch(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	blobA, idA := testEnterprisePolicy(t, priv, pinPolicyA)
	blobB, idB := testEnterprisePolicy(t, priv, pinPolicyB)
	for _, lane := range renewalLanes {
		t.Run(lane.name, func(t *testing.T) {
			g := &tokenGatedServer{blob: blobB, id: idB, refreshStatus: http.StatusUnauthorized, refuseAll: true}
			srv := httptest.NewServer(g.handler(t))
			defer srv.Close()
			store := pinStore(t, srv.URL, keys)
			if err := store.SaveSnapshot(blobA); err != nil {
				t.Fatal(err)
			}
			seedSession(t, store, idA, time.Now().Add(-time.Hour))
			if d := decideAfterRefresh(t, store, shellEvent("git status")); d.Effect != policy.EffectDeny || !strings.Contains(d.Reason, refusedPolicyPhrase) {
				t.Fatalf("refused fetch = %s %q, want the refused-policy deny", d.Effect, d.Reason)
			}
			enrolled, _ := store.LoadIdentity()

			// The server serves again. Each lane runs when its own trigger
			// holds, so the stored session time is moved to that trigger.
			g.mu.Lock()
			g.refuseAll, g.refreshStatus = false, lane.refreshStatus
			g.mu.Unlock()
			ses, _ := store.LoadSession()
			ses.ExpiresAt = time.Now().Add(lane.seed)
			if err := store.SaveSession(ses); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := lane.run(ctx, t, store, srv.URL, io.Discard); err != nil {
				t.Fatalf("lane: %v", err)
			}

			ses, _ = store.LoadSession()
			if ses.SnapshotID != idB || ses.PolicyRefused != "" || !ses.PolicyDeadline.IsZero() {
				t.Fatalf("after the fetch: pin %q, refusal %q until %v; want %q and no refusal", ses.SnapshotID, ses.PolicyRefused, ses.PolicyDeadline, idB)
			}
			if d := decideNow(t, store, "git status"); d.Effect != policy.EffectAllow {
				t.Errorf("git status = %s %q, want allow", d.Effect, d.Reason)
			}
			if d := decideNow(t, store, "b-cmd /x"); d.Effect != policy.EffectDeny || d.RuleID != "rb" {
				t.Errorf("b-cmd = %s by %q, want the server's policy to deny it", d.Effect, d.RuleID)
			}
			if id, _ := store.LoadIdentity(); id != enrolled {
				t.Errorf("identity changed from %+v to %+v, want the same enrolment", enrolled, id)
			}
		})
	}
}

// TestRefusedRenewalsKeepFirstDeadline pins the core of the rule: once a
// refusal has set the deadline, later renewals that are refused again leave
// it exactly where it was. A busy session that renews through the daemon,
// the hook and the MCP proxy mint therefore still denies once the first
// deadline passes, although its token stays fresh throughout.
func TestRefusedRenewalsKeepFirstDeadline(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	blobA, idA := testEnterprisePolicy(t, priv, pinPolicyA)
	blobB, idB := testEnterprisePolicy(t, priv, pinPolicyB)
	g := &tokenGatedServer{blob: blobB, id: idB, refuseAll: true}
	srv := httptest.NewServer(g.handler(t))
	defer srv.Close()
	store := pinStore(t, srv.URL, keys)
	if err := store.SaveSnapshot(blobA); err != nil {
		t.Fatal(err)
	}
	seedSession(t, store, idA, time.Now().Add(1500*time.Millisecond))
	decideAfterRefresh(t, store, shellEvent("git status"))
	first, _ := store.LoadSession()
	if first.PolicyDeadline.IsZero() {
		t.Fatal("the first refused renewal set no deadline")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	steps := []struct {
		name string
		// expires moves the stored session time to the step's trigger; 0
		// keeps the time the previous renewal left.
		expires time.Duration
		run     func()
	}{
		{"daemon tick", 0, func() { NewDaemon(store, io.Discard).refreshOnce(t) }},
		{"hook refresh", time.Minute, func() { decideAfterRefresh(t, store, shellEvent("git status")) }},
		{"MCP proxy and exec mint", 10 * time.Second, func() {
			if _, err := ensureSession(ctx, store, NewClient(srv.URL), "claude-code"); err != nil {
				t.Fatal(err)
			}
		}},
		{"MCP proxy token refresh", 0, func() {
			if err := proxyRefresh(ctx, t, store, srv.URL, io.Discard); err != nil {
				t.Fatal(err)
			}
		}},
		{"second daemon tick", 0, func() { NewDaemon(store, io.Discard).refreshOnce(t) }},
	}
	for _, step := range steps {
		if step.expires != 0 {
			ses, _ := store.LoadSession()
			ses.ExpiresAt = time.Now().Add(step.expires)
			if err := store.SaveSession(ses); err != nil {
				t.Fatal(err)
			}
		}
		step.run()
		ses, _ := store.LoadSession()
		if !ses.PolicyDeadline.Equal(first.PolicyDeadline) {
			t.Errorf("after the %s the deadline moved from %v to %v", step.name, first.PolicyDeadline, ses.PolicyDeadline)
		}
	}
	if ses, _ := store.LoadSession(); !ses.ExpiresAt.After(first.PolicyDeadline) {
		t.Fatalf("the token was not renewed past the deadline (expires %v), so the row proves nothing", ses.ExpiresAt)
	}
	time.Sleep(time.Until(first.PolicyDeadline) + 100*time.Millisecond)
	if d := decideNow(t, store, "git status"); d.Effect != policy.EffectDeny || !strings.Contains(d.Reason, refusedPolicyPhrase) {
		t.Errorf("git status after the first deadline = %s %q, want the refused-policy deny", d.Effect, d.Reason)
	}
}

// TestEmptyReportKeepsRefusal pins that a renewal naming no policy, which a
// server with no snapshot loaded answers, leaves an outstanding refusal as
// it was on every renewing lane. The deny goes on, and the next refused
// renewal keeps the first deadline rather than starting a later one.
func TestEmptyReportKeepsRefusal(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	blobA, idA := testEnterprisePolicy(t, priv, pinPolicyA)
	blobB, idB := testEnterprisePolicy(t, priv, pinPolicyB)
	for _, lane := range renewalLanes {
		if !lane.enters {
			continue
		}
		t.Run(lane.name, func(t *testing.T) {
			g := &tokenGatedServer{blob: blobB, refreshStatus: lane.refreshStatus, failStatus: http.StatusServiceUnavailable}
			srv := httptest.NewServer(g.handler(t))
			defer srv.Close()
			store := pinStore(t, srv.URL, keys)
			if err := store.SaveSnapshot(blobA); err != nil {
				t.Fatal(err)
			}
			first := time.Now().Add(-time.Hour).Truncate(time.Second)
			seedSession(t, store, idA, time.Now())
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			for i, reported := range []string{"", idB} {
				g.mu.Lock()
				g.id, g.live = reported, "tok-seed"
				if reported != "" {
					g.failStatus, g.refuseAll = 0, true
				}
				g.mu.Unlock()
				ses, _ := store.LoadSession()
				ses.SessionToken, ses.ExpiresAt = "tok-seed", time.Now().Add(lane.seed)
				if i == 0 {
					ses.PolicyRefused, ses.PolicyDeadline = "The server refused it: "+gatedRefusal, first
				}
				if err := store.SaveSession(ses); err != nil {
					t.Fatal(err)
				}
				_ = lane.run(ctx, t, store, srv.URL, io.Discard)
				ses, _ = store.LoadSession()
				if ses.PolicyRefused == "" || !ses.PolicyDeadline.Equal(first) {
					t.Fatalf("after a renewal that reports %q: refusal %q until %v, want one kept until %v", reported, ses.PolicyRefused, ses.PolicyDeadline, first)
				}
				if d := decideNow(t, store, "git status"); d.Effect != policy.EffectDeny || !strings.Contains(d.Reason, refusedPolicyPhrase) {
					t.Errorf("after a renewal that reports %q: git status = %s %q, want the refused-policy deny", reported, d.Effect, d.Reason)
				}
			}
		})
	}
}

// TestProxyRefreshSettlesPolicy pins the MCP proxy's
// own token refresh. Behind an edge proxy that drops the Authorization
// header, every call through the Straza MCP proxy gets a 401 and renews the
// token through the body, so on a machine with no daemon the hook never
// renews by itself. That renewal must still record the refused policy: five
// rounds of agent work end with the hook denying b-cmd, which the server's
// policy denies and the cached one allows.
func TestProxyRefreshSettlesPolicy(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	blobA, idA := testEnterprisePolicy(t, priv, pinPolicyA)
	blobB, idB := testEnterprisePolicy(t, priv, pinPolicyB)
	g := &tokenGatedServer{blob: blobB, id: idB}
	serve := g.handler(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("Authorization") // the edge proxy
		if r.URL.Path == "/mcp" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		serve(w, r)
	}))
	defer srv.Close()
	store := pinStore(t, srv.URL, keys)
	if err := store.SaveSnapshot(blobA); err != nil {
		t.Fatal(err)
	}
	seedSession(t, store, idA, time.Now().Add(200*time.Second))
	gateway := gatewayHTTPClient(store, NewClient(srv.URL), "claude-code")
	var bash policy.Decision
	for range 5 {
		// The MCP call: its own hook first, then the proxy, which gets a 401
		// and renews the token.
		decideAfterRefresh(t, store, shellEvent("b-cmd via-mcp"))
		resp, err := gateway.Post(srv.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		// About 170 s of work pass, so every stored time moves back by that.
		ses, _ := store.LoadSession()
		ses.ExpiresAt = ses.ExpiresAt.Add(-170 * time.Second)
		if !ses.PolicyDeadline.IsZero() {
			ses.PolicyDeadline = ses.PolicyDeadline.Add(-170 * time.Second)
		}
		if err := store.SaveSession(ses); err != nil {
			t.Fatal(err)
		}
		// A Bash call, and the drain pulse it spawns.
		bash = decideAfterRefresh(t, store, shellEvent("b-cmd x"))
		_ = os.Remove(store.statePath("snapshot-checked"))
		_, _ = DrainOnce(3 * time.Second)
	}
	g.mu.Lock()
	minted := g.minted
	g.mu.Unlock()
	if minted != 5 {
		t.Fatalf("%d token renewals, want the proxy's one per round and none by the hook", minted)
	}
	if bash.Effect != policy.EffectDeny || !strings.Contains(bash.Reason, refusedPolicyPhrase) || !strings.Contains(bash.Reason, gatedRefusal) {
		t.Errorf("b-cmd after five rounds = %s %q, want the refused-policy deny with the server's sentence", bash.Effect, bash.Reason)
	}
}

// TestDoctorReportsPolicyRefusal pins that `straza doctor`, which the deny
// names as the next step, shows the refusal with its reason and deadline as
// a failing line of its own, while the session and the cached snapshot still
// read fine. Without a refusal the line is absent.
func TestDoctorReportsPolicyRefusal(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	blobA, idA := testEnterprisePolicy(t, priv, pinPolicyA)
	blobB, idB := testEnterprisePolicy(t, priv, pinPolicyB)
	g := &tokenGatedServer{blob: blobB, id: idB, refreshStatus: http.StatusUnauthorized, refuseAll: true}
	srv := httptest.NewServer(g.handler(t))
	defer srv.Close()
	store := pinStore(t, srv.URL, keys)
	if err := store.SaveSnapshot(blobA); err != nil {
		t.Fatal(err)
	}
	seedSession(t, store, idA, time.Now().Add(-time.Hour))

	byName := func() map[string]Check {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out := map[string]Check{}
		for _, c := range Doctor(ctx, store) {
			out[c.Name] = c
		}
		return out
	}
	if _, ok := byName()["policy"]; ok {
		t.Fatal("doctor reports a policy refusal before any refusal")
	}
	decideAfterRefresh(t, store, shellEvent("git status"))
	ses, _ := store.LoadSession()
	checks := byName()
	c, ok := checks["policy"]
	if !ok || c.Status != checkFail || !strings.Contains(c.Detail, "The server refused it: "+gatedRefusal) ||
		!strings.Contains(c.Detail, ses.PolicyDeadline.Local().Format(time.RFC3339)) {
		t.Fatalf("policy line = %+v (present %v), want a fail with the reason and the deadline", c, ok)
	}
	if checks["session"].Status == checkFail || checks["snapshot"].Status != checkOK {
		t.Errorf("session %+v, snapshot %+v: the refusal must show on its own line", checks["session"], checks["snapshot"])
	}
}

// TestMintCarriesPolicyRefusal pins that a mint replacing a refused session
// keeps the refusal and its first deadline when its own fetch fails in
// transit, so a flaky network during a mint never forgets a refusal.
func TestMintCarriesPolicyRefusal(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	blobA, idA := testEnterprisePolicy(t, priv, pinPolicyA)
	blobB, idB := testEnterprisePolicy(t, priv, pinPolicyB)
	g := &tokenGatedServer{blob: blobB, id: idB, dropSnapshot: true}
	srv := httptest.NewServer(g.handler(t))
	defer srv.Close()
	store := pinStore(t, srv.URL, keys)
	if err := store.SaveSnapshot(blobA); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	seedSession(t, store, idA, time.Now().Add(-time.Hour))
	ses, _ := store.LoadSession()
	ses.PolicyRefused, ses.PolicyDeadline = gatedRefusal, deadline
	if err := store.SaveSession(ses); err != nil {
		t.Fatal(err)
	}
	minted, err := ensureSession(context.Background(), store, NewClient(srv.URL), "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if minted.SessionID == "s-seed" || minted.PolicyRefused != gatedRefusal || !minted.PolicyDeadline.Equal(deadline) {
		t.Fatalf("minted session %q with refusal %q until %v; want a new session that keeps the refusal until %v", minted.SessionID, minted.PolicyRefused, minted.PolicyDeadline, deadline)
	}
	if d := decideNow(t, store, "git status"); d.Effect != policy.EffectDeny || !strings.Contains(d.Reason, refusedPolicyPhrase) {
		t.Errorf("git status = %s %q, want the refused-policy deny", d.Effect, d.Reason)
	}
}
