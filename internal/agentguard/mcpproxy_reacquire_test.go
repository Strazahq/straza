package agentguard

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

// refuseHeldSession makes the gateway refuse the token of the session on
// disk and answer every refresh of it with status and msg, the state after
// strazad closed a session whose token still reads as live to the client.
func refuseHeldSession(g *fakeGateway, status int, msg string) {
	g.refreshMsg = msg
	g.refreshStatus.Store(int32(status))
	g.rotate()
}

// TestMCPProxyChecksInWhenItsSessionIsRefused pins what the proxy does once
// the gateway refuses the token of a session that still reads as live on
// disk, by strazad's answer to the refresh. A 401 says the session is over,
// so the proxy checks in once with the enrolled identity and the call runs
// on the new session. A 403 judged the identity and a 503 judged nothing,
// so neither checks in and the call fails.
func TestMCPProxyChecksInWhenItsSessionIsRefused(t *testing.T) {
	rows := []struct {
		name         string
		status       int
		msg          string
		wantCheckins int64
		wantRun      bool
	}{
		{"a session strazad calls over", http.StatusUnauthorized, "session is no longer active", 1, true},
		{"an identity strazad refuses", http.StatusForbidden, "user is disabled. Contact your administrator", 0, false},
		{"a strazad that cannot answer", http.StatusServiceUnavailable, "Straza is temporarily unavailable. Try again in a moment", 0, false},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			g := newFakeGateway(t)
			store := proxyTestStore(t, g.URL)
			ctx, _, harness := buildProxy(t, g, store)
			held, err := store.LoadSession()
			if err != nil || time.Until(held.ExpiresAt) < time.Minute {
				t.Fatalf("the session on disk = %+v, %v, want one that reads as live", held, err)
			}
			before := g.checkins.Load()
			refuseHeldSession(g, row.status, row.msg)

			ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			txt, err := echoCall(ctx, harness, "after the refusal")
			if ran := err == nil; ran != row.wantRun {
				t.Fatalf("the call after the refusal = %q, %v, want it to run: %v", txt, err, row.wantRun)
			}
			if row.wantRun && txt != "gateway: after the refusal" {
				t.Errorf("the call's answer = %q", txt)
			}
			if got := g.checkins.Load() - before; got != row.wantCheckins {
				t.Errorf("device check-ins after the refusal = %d, want %d", got, row.wantCheckins)
			}
			now, err := store.LoadSession()
			if err != nil {
				t.Fatalf("the session on disk after the refusal: %v", err)
			}
			if renewed := now.SessionToken != held.SessionToken; renewed != row.wantRun {
				t.Errorf("the session on disk was replaced: %v, want %v", renewed, row.wantRun)
			}
		})
	}
}

// TestMCPProxyRefusedSessionSpendsOneCheckin pins the single flight of the
// re-acquire: calls that meet the refusal together share one check-in, and
// every one of them runs on the session it minted.
func TestMCPProxyRefusedSessionSpendsOneCheckin(t *testing.T) {
	g := newFakeGateway(t)
	store := proxyTestStore(t, g.URL)
	ctx, _, harness := buildProxy(t, g, store)
	before := g.checkins.Load()
	refuseHeldSession(g, http.StatusUnauthorized, "session is no longer active")

	const callers = 8
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = echoCall(ctx, harness, "together")
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d failed after the refusal: %v", i, err)
		}
	}
	if got := g.checkins.Load() - before; got != 1 {
		t.Errorf("device check-ins for %d callers = %d, want exactly 1", callers, got)
	}
}

// TestProxyMintStoresTheIdentityType pins that a session the proxy mints
// carries the identity type of the check-in answer, on the value it returns
// and on disk, as a refresh writes it.
func TestProxyMintStoresTheIdentityType(t *testing.T) {
	g := newFakeGateway(t)
	g.identity = map[string]any{"user_type": "agent", "agency_mode": "autonomous", "swarm_id": "sw-1"}
	store := proxyTestStore(t, g.URL)

	minted, err := ensureSession(context.Background(), store, NewClient(g.URL), "claude-code")
	if err != nil {
		t.Fatalf("ensureSession: %v", err)
	}
	saved, err := store.LoadSession()
	if err != nil {
		t.Fatalf("the minted session on disk: %v", err)
	}
	for name, ses := range map[string]Session{"returned": minted, "saved": saved} {
		if ses.UserType != "agent" || ses.AgencyMode != "autonomous" || ses.SwarmID != "sw-1" {
			t.Errorf("the %s session's identity type = %q, %q, %q, want agent, autonomous, sw-1",
				name, ses.UserType, ses.AgencyMode, ses.SwarmID)
		}
	}
}
