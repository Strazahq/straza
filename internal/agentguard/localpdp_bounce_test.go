package agentguard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// bounceLane fakes the two endpoints the /v1/decide bounce touches: a
// scripted /v1/decide (status per call; 0 = 200 with the verdict) and the
// reacquireCheckin /v1/checkin lanes (refresh vs device credential).
type bounceLane struct {
	checkin    reacquireCheckin
	decide     []int // per-call status; 0 = 200 verdict; calls past the end = 200
	verdict    map[string]any
	decideHits atomic.Int32

	mu          sync.Mutex
	decideAuths []string
}

func (b *bounceLane) handler(t *testing.T) http.HandlerFunc {
	ck := b.checkin.handler(t)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/checkin" {
			ck(w, r)
			return
		}
		if r.URL.Path != "/v1/decide" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		n := int(b.decideHits.Add(1)) - 1
		b.mu.Lock()
		b.decideAuths = append(b.decideAuths, r.Header.Get("Authorization"))
		b.mu.Unlock()
		if n < len(b.decide) && b.decide[n] != 0 {
			w.WriteHeader(b.decide[n])
			_, _ = w.Write([]byte(`{"error":"Straza: session state expired. Check in again"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(b.verdict)
	}
}

func (b *bounceLane) auth(i int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if i < len(b.decideAuths) {
		return b.decideAuths[i]
	}
	return ""
}

// bouncePDP builds a store-backed LocalPDP against the fake, with a session
// far from expiry so RefreshIfStale never muddies the assertions: every
// checkin observed is the bounce's own.
func bouncePDP(t *testing.T, srvURL string, keys map[string]string, signed []byte, snapID string, withIdentity bool) (*LocalPDP, *Store) {
	t.Helper()
	store := reacquireStore(t, srvURL, keys, signed, snapID, 30*time.Minute, withIdentity)
	ses, err := store.LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	pdp, err := NewLocalPDP(store, ses.Subject())
	if err != nil {
		t.Fatal(err)
	}
	return pdp, store
}

// TestDecideOnlineBounce pins the client half of the restart-window recovery:
// a 401 from /v1/decide means the server no longer holds
// this session's checked-in state, and the documented any-401 recovery is a
// re-checkin plus exactly one retry. A refresh check-in goes first, carrying
// the still-valid session token, so the session id (and any pending approval
// exemptions keyed on it) survives; the identity lane runs only when the
// token itself is refused. When nothing restores the session the deny is
// honest: the server REFUSED, so the reason must not claim unreachability
// (transport wording is pinned by TestApproveLane and TestServerCheckLane
// and stays byte-identical).
func TestDecideOnlineBounce(t *testing.T) {
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user"}
	approveLocal := policy.Decision{Effect: policy.EffectAllow, RuleID: "needs-human", Approve: &policy.ApproveSpec{}}
	verdict := map[string]any{
		"effect": "deny", "ruleId": "needs-human",
		"reason": "Straza: approval requested (notified roles [dev]); retry after approval (ref abc123)", "approvalId": "abc123",
	}
	priv, keys := testSnapshotKey(t)
	signed, snapID := testSignedPolicy(t, priv, offlinePolicyDoc)

	t.Run("refresh bounce restores state, retry verdict wins verbatim", func(t *testing.T) {
		b := &bounceLane{checkin: reacquireCheckin{snapshotID: snapID}, decide: []int{http.StatusUnauthorized}, verdict: verdict}
		srv := httptest.NewServer(b.handler(t))
		defer srv.Close()
		p, store := bouncePDP(t, srv.URL, keys, signed, snapID, false)

		d := p.escalate(ev, approveLocal)
		if d.Effect != policy.EffectDeny || d.RuleID != "needs-human" || !strings.Contains(d.Reason, "ref abc123") {
			t.Fatalf("post-bounce verdict = %+v, want the server verdict verbatim", d)
		}
		if got := b.decideHits.Load(); got != 2 {
			t.Fatalf("decide calls = %d, want 401 then one retry", got)
		}
		if b.auth(1) != "Bearer tok-refreshed" {
			t.Errorf("retry carried %q, want the refresh-adopted token", b.auth(1))
		}
		ses, err := store.LoadSession()
		if err != nil {
			t.Fatal(err)
		}
		if ses.SessionToken != "tok-refreshed" || ses.SessionID != "s-old" {
			t.Errorf("persisted session = %s/%s, want the SAME session id with the refreshed token", ses.SessionID, ses.SessionToken)
		}
		if b.checkin.deviceCalls.Load() != 0 {
			t.Errorf("identity lane dialed on a refresh-recoverable bounce")
		}
	})

	t.Run("dead token: identity lane restores, retry wins", func(t *testing.T) {
		b := &bounceLane{
			checkin: reacquireCheckin{refreshStatus: http.StatusUnauthorized, refreshMsg: "session token rejected", snapshotID: snapID},
			decide:  []int{http.StatusUnauthorized}, verdict: verdict,
		}
		srv := httptest.NewServer(b.handler(t))
		defer srv.Close()
		p, store := bouncePDP(t, srv.URL, keys, signed, snapID, true)

		d := p.escalate(ev, approveLocal)
		if d.Effect != policy.EffectDeny || !strings.Contains(d.Reason, "ref abc123") {
			t.Fatalf("post-reacquire verdict = %+v, want the server verdict verbatim", d)
		}
		if b.auth(1) != "Bearer tok2" {
			t.Errorf("retry carried %q, want the re-acquired token", b.auth(1))
		}
		if b.checkin.deviceCalls.Load() != 1 {
			t.Errorf("identity-lane checkins = %d, want 1", b.checkin.deviceCalls.Load())
		}
		if ses, _ := store.LoadSession(); ses.SessionID != "s2" {
			t.Errorf("persisted session id = %q, want the re-acquired s2", ses.SessionID)
		}
	})

	t.Run("no recovery: honest refusal deny, no retry, no unreachable claim", func(t *testing.T) {
		b := &bounceLane{
			checkin: reacquireCheckin{refreshStatus: http.StatusUnauthorized, refreshMsg: "session token rejected", snapshotID: snapID},
			decide:  []int{http.StatusUnauthorized}, verdict: verdict,
		}
		srv := httptest.NewServer(b.handler(t))
		defer srv.Close()
		p, _ := bouncePDP(t, srv.URL, keys, signed, snapID, false) // no identity to reacquire through

		d := p.escalate(ev, approveLocal)
		if d.Effect != policy.EffectDeny || d.RuleID != "needs-human" {
			t.Fatalf("unrecovered bounce = %+v, want a fail-closed deny on the local rule", d)
		}
		if !strings.Contains(d.Reason, "refused") || !strings.Contains(d.Reason, "doctor") {
			t.Errorf("reason %q should name the refusal and a remedy", d.Reason)
		}
		if strings.Contains(d.Reason, "unreachable") {
			t.Errorf("reason %q claims unreachability after the server ANSWERED", d.Reason)
		}
		if got := b.decideHits.Load(); got != 1 {
			t.Errorf("decide calls = %d, want no retry when nothing was restored", got)
		}
	})

	t.Run("retry once only: a second 401 is final", func(t *testing.T) {
		b := &bounceLane{
			checkin: reacquireCheckin{snapshotID: snapID},
			decide:  []int{http.StatusUnauthorized, http.StatusUnauthorized}, verdict: verdict,
		}
		srv := httptest.NewServer(b.handler(t))
		defer srv.Close()
		p, _ := bouncePDP(t, srv.URL, keys, signed, snapID, false)

		d := p.escalate(ev, approveLocal)
		if d.Effect != policy.EffectDeny || strings.Contains(d.Reason, "unreachable") {
			t.Fatalf("double-401 = %+v, want an honest fail-closed deny", d)
		}
		if got := b.decideHits.Load(); got != 2 {
			t.Errorf("decide calls = %d, want exactly one retry, never a loop", got)
		}
	})

	t.Run("serverCheck lane shares the bounce", func(t *testing.T) {
		b := &bounceLane{
			checkin: reacquireCheckin{snapshotID: snapID},
			decide:  []int{http.StatusUnauthorized},
			verdict: map[string]any{"effect": "allow", "ruleId": "sc-rule"},
		}
		srv := httptest.NewServer(b.handler(t))
		defer srv.Close()
		p, _ := bouncePDP(t, srv.URL, keys, signed, snapID, false)

		d := p.escalate(ev, policy.Decision{Effect: policy.EffectAllow, RuleID: "sc-rule", ServerCheck: true})
		if d.Effect != policy.EffectAllow || d.RuleID != "sc-rule" {
			t.Fatalf("serverCheck post-bounce = %+v, want the server allow", d)
		}
		if got := b.decideHits.Load(); got != 2 {
			t.Errorf("decide calls = %d, want the same 401-then-retry dance", got)
		}
	})
}

// TestDecideOnlineBounceKeepsFastPath: a clean first answer never dials
// /v1/checkin at all; the bounce is strictly 401-triggered.
func TestDecideOnlineBounceKeepsFastPath(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	signed, snapID := testSignedPolicy(t, priv, offlinePolicyDoc)
	b := &bounceLane{verdict: map[string]any{"effect": "allow", "ruleId": "sc-rule"}}
	checkins := atomic.Int32{}
	inner := b.handler(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/checkin" {
			checkins.Add(1)
		}
		inner(w, r)
	}))
	defer srv.Close()
	p, _ := bouncePDP(t, srv.URL, keys, signed, snapID, false)

	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "git status"}
	if d := p.escalate(ev, policy.Decision{Effect: policy.EffectAllow, RuleID: "sc-rule", ServerCheck: true}); d.Effect != policy.EffectAllow {
		t.Fatalf("clean serverCheck = %+v", d)
	}
	if b.decideHits.Load() != 1 || checkins.Load() != 0 {
		t.Errorf("decide=%d checkin=%d, want one decide and zero checkins", b.decideHits.Load(), checkins.Load())
	}
}
