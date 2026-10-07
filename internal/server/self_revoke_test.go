package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// revocationCounter counts, per session id, the straza.revocation.session
// control events (singular and inside plural bulk envelopes) and the
// per-target straza.push.session.<id> publishes, the two halves of the
// revoke trio that leave this pod. Deterministic despite the async relay
// because both lanes preserve order: the outbox drains oldest-first and core
// pushes ride one connection, so once a LATER sentinel revocation is
// observed, every earlier count is final.
type revocationCounter struct {
	mu     sync.Mutex
	events map[string]int // singular control events per session id
	plural map[string]int // appearances inside straza.revocation.sessions sets
	pushes map[string]int // per-target push publishes per session id
}

func newRevocationCounter(t *testing.T, app *App) *revocationCounter {
	t.Helper()
	c := &revocationCounter{events: map[string]int{}, plural: map[string]int{}, pushes: map[string]int{}}
	unsub1, err := app.bus.SubscribeCore("straza.revocation.session", func(_ string, data []byte) {
		var env struct {
			Data struct {
				Session string `json:"session"`
			} `json:"data"`
		}
		_ = json.Unmarshal(data, &env)
		c.mu.Lock()
		c.events[env.Data.Session]++
		c.mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unsub1)
	unsub2, err := app.bus.SubscribeCore("straza.revocation.sessions", func(_ string, data []byte) {
		var env struct {
			Data struct {
				Sessions []string `json:"sessions"`
			} `json:"data"`
		}
		_ = json.Unmarshal(data, &env)
		c.mu.Lock()
		for _, id := range env.Data.Sessions {
			c.plural[id]++
		}
		c.mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unsub2)
	unsub3, err := app.bus.SubscribeCore("straza.push.session.>", func(subject string, _ []byte) {
		c.mu.Lock()
		c.pushes[strings.TrimPrefix(subject, "straza.push.session.")]++
		c.mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unsub3)
	return c
}

func (c *revocationCounter) snapshot(id string) (events, plural, pushes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.events[id], c.plural[id], c.pushes[id]
}

// waitSeen blocks until the session has at least one event (singular or
// plural) AND one push, or fails the test: the sentinel that makes earlier
// counts final.
func (c *revocationCounter) waitSeen(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ev, pl, pu := c.snapshot(id)
		if ev+pl > 0 && pu > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no control event/push observed for %s (events=%d plural=%d pushes=%d)", id, ev, pl, pu)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// revocationRows counts persisted session-revocation rows for one id.
func revocationRows(t *testing.T, app *App, id string) int {
	t.Helper()
	revs, err := app.store.Revocations().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, rv := range revs {
		if rv.Kind == store.RevokeSession && rv.TargetID == id {
			n++
		}
	}
	return n
}

// TestSessionSelfRevoke pins POST /v1/session/revoke: the authenticated caller
// ends the session its own token names: no id parameter, no role gate. The
// route exists so `strazactl logout` can revoke server-side for every caller,
// not only holders of straza-admin; the admin route stays admin-gated and
// untouched. Confused-deputy-proof by construction: the only session the
// handler can touch is the one in the verified token's claims.
//
// The replay half is the adversarial pin: tokens.Verify never consults the
// denylist, so the revoked session's token stays VALID for its whole TTL and
// can replay this route freely. Every replay must be the idempotent 200 the
// CLI contract promises and fire NOTHING (no new revocation row, no control
// event, no push), or one logout becomes an authenticated write-amplification
// primitive against the outbox.
func TestSessionSelfRevoke(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app) // kim, role dev, NOT straza-admin
	ctx := context.Background()

	tokenA, sesA := checkinToken(t, app, base)
	tokenB, sesB := checkinToken(t, app, base) // a second live session: must survive A's revoke
	counter := newRevocationCounter(t, app)

	// The admin route is unchanged: the same non-admin token is refused there.
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/sessions/"+sesA+"/revoke", tokenA, nil, nil); code != http.StatusForbidden {
		t.Fatalf("admin revoke as non-admin = %d, want 403 (role gate unchanged)", code)
	}

	// Auth matrix: only a verified SESSION token opens the route.
	authMatrix := []struct {
		name   string
		bearer string
	}{
		{"no token", ""},
		{"garbage token", "not-a-jwt"},
		{"id token names no session", loginDeviceFlow(t, base, "kim", "hunter2!")},
	}
	for _, tc := range authMatrix {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := postJSONAuth(t, base+"/v1/session/revoke", tc.bearer, map[string]any{}); code != http.StatusUnauthorized {
				t.Errorf("self revoke with %s = %d, want 401", tc.name, code)
			}
		})
	}

	// The non-admin caller revokes their own session, and ONLY that one.
	code, out := postJSONAuth(t, base+"/v1/session/revoke", tokenA, map[string]any{})
	if code != http.StatusOK || out["status"] != "revoked" || out["session"] != sesA {
		t.Fatalf("self revoke = %d %v, want 200 revoked %s", code, out, sesA)
	}
	ses, err := app.store.Sessions().GetByID(ctx, sesA)
	if err != nil || ses.Status != store.SessionRevoked {
		t.Errorf("session row = %+v (%v), want status revoked", ses, err)
	}
	other, err := app.store.Sessions().GetByID(ctx, sesB)
	if err != nil || other.Status != store.SessionActive {
		t.Errorf("sibling session = %+v (%v), want untouched and active", other, err)
	}

	// The full kill-switch cascade fired, same as the admin revoke: exactly
	// one revocation row, one control event, one push, and the very next
	// decide fails closed.
	counter.waitSeen(t, sesA)
	if n := revocationRows(t, app, sesA); n != 1 {
		t.Errorf("revocation rows after first revoke = %d, want exactly 1", n)
	}
	code, dec := decide(t, base, tokenA, map[string]any{
		"kind": "tool.pre", "tool": "shell.exec", "command": "git status",
	})
	if code != http.StatusOK || dec["effect"] != "deny" || dec["ruleId"] != "revoked" {
		t.Errorf("post-revoke decision = %d %v, want deny by rule \"revoked\"", code, dec)
	}
	if reason, _ := dec["reason"].(string); !strings.Contains(reason, "revoked") {
		t.Errorf("deny reason not actionable: %v", dec)
	}

	// Replays: still-verifying token, already-dead session. Idempotent 200,
	// id echoed, NOTHING re-fires. (404 stays reserved for servers that
	// predate the route, the CLI's fall-back-to-admin signal.)
	for i := 0; i < 2; i++ {
		if code, out := postJSONAuth(t, base+"/v1/session/revoke", tokenA, map[string]any{}); code != http.StatusOK || out["session"] != sesA {
			t.Fatalf("replay %d = %d %v, want idempotent 200 echoing %s", i+1, code, out, sesA)
		}
	}
	// Sentinel: revoke sesB the same way. Once ITS event and push are
	// observed, every earlier sesA emission would have arrived too (ordered
	// lanes), so the counts below are final, not a sleep-and-hope.
	if code, out := postJSONAuth(t, base+"/v1/session/revoke", tokenB, map[string]any{}); code != http.StatusOK || out["session"] != sesB {
		t.Fatalf("sentinel self revoke = %d %v", code, out)
	}
	counter.waitSeen(t, sesB)
	if ev, pl, pu := counter.snapshot(sesA); ev != 1 || pl != 0 || pu != 1 {
		t.Errorf("sesA control traffic after replays = events %d, plural %d, pushes %d; want exactly 1/0/1", ev, pl, pu)
	}
	if n := revocationRows(t, app, sesA); n != 1 {
		t.Errorf("revocation rows after replays = %d, want still exactly 1", n)
	}
}

// TestAdminSessionRevokeReplayIdempotent pins the same replay contract on the
// admin-gated siblings (lower severity, the caller holds straza-admin, but
// the idempotency contract matches the self route): a re-revoke answers the
// same 200 it always did and fires nothing, and the bulk face counts and
// emits only sessions that actually transitioned, exactly as its openapi text
// ("unknown/already-dead ids are skipped; the response counts what was
// actually revoked") always claimed.
func TestAdminSessionRevokeReplayIdempotent(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)

	_, sesA := checkinToken(t, app, base)
	_, sesB := checkinToken(t, app, base)
	counter := newRevocationCounter(t, app)

	// First single revoke: the real transition.
	var out map[string]string
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/sessions/"+sesA+"/revoke", adminTok, nil, &out); code != http.StatusOK || out["status"] != "revoked" {
		t.Fatalf("revoke = %d %v", code, out)
	}
	counter.waitSeen(t, sesA)

	// Replays: the answer stays 200 "revoked" (the route's long-standing
	// shape for a session that exists), but nothing re-fires.
	for i := 0; i < 2; i++ {
		if code := adminReq(t, http.MethodPost, base+"/v1/admin/sessions/"+sesA+"/revoke", adminTok, nil, &out); code != http.StatusOK || out["status"] != "revoked" {
			t.Fatalf("replay %d = %d %v, want 200 revoked", i+1, code, out)
		}
	}
	// Bulk face, all-dead set: counts zero, emits nothing.
	var bulk struct {
		Revoked int `json:"revoked"`
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/sessions/revoke", adminTok,
		map[string]any{"sessions": []string{sesA, "no-such-session"}}, &bulk); code != http.StatusOK || bulk.Revoked != 0 {
		t.Fatalf("bulk of dead ids = %d revoked=%d, want 200 with 0", code, bulk.Revoked)
	}
	// Bulk face, mixed set: only the live session counts, and the plural
	// event (the sentinel that finalizes sesA's counts) names ONLY it.
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/sessions/revoke", adminTok,
		map[string]any{"sessions": []string{sesA, sesB}}, &bulk); code != http.StatusOK || bulk.Revoked != 1 {
		t.Fatalf("mixed bulk = %d revoked=%d, want 200 with 1", code, bulk.Revoked)
	}
	counter.waitSeen(t, sesB)

	if ev, pl, pu := counter.snapshot(sesA); ev != 1 || pl != 0 || pu != 1 {
		t.Errorf("sesA control traffic = events %d, plural %d, pushes %d; want exactly 1/0/1", ev, pl, pu)
	}
	if evB, plB, puB := counter.snapshot(sesB); evB != 0 || plB != 1 || puB != 1 {
		t.Errorf("sesB control traffic = events %d, plural %d, pushes %d; want exactly 0/1/1", evB, plB, puB)
	}
	if n := revocationRows(t, app, sesA); n != 1 {
		t.Errorf("sesA revocation rows = %d, want exactly 1 across single replays and bulk", n)
	}
	if n := revocationRows(t, app, sesB); n != 1 {
		t.Errorf("sesB revocation rows = %d, want exactly 1", n)
	}
	// An id that never existed keeps 404 on the single face.
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/sessions/no-such-session/revoke", adminTok, nil, nil); code != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404 (single-face contract unchanged)", code)
	}
}
