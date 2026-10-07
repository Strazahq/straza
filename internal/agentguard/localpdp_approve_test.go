package agentguard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// TestDirectMcpKeepsHookDance pins the non-gateway half of the single-gate
// contract: a DIRECT MCP tool (no gateway registration in front) keeps the
// full hook approval dance, because there the hook is the only gate. The
// gateway half is the whole-decision deferral at Decide, pinned by
// TestGatewayDeferralWholeDecision.
func TestDirectMcpKeepsHookDance(t *testing.T) {
	ev := policy.Event{
		Kind: policy.EventToolPre, Tool: policy.ToolMCPCall,
		App: "demo-tools", ToolName: "echo",
	}
	session := Session{SessionToken: "tok-1", ExpiresAt: time.Now().Add(time.Hour)}

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"effect": policy.EffectDeny, "ruleId": "needs-human",
			"reason": "Straza: approval requested (notified roles [sec-approvers]); retry after approval (ref abc123)",
		})
	}))
	defer srv.Close()
	p := &LocalPDP{session: session, client: NewClient(srv.URL)}
	d := p.escalate(ev, policy.Decision{Effect: policy.EffectAllow, RuleID: "needs-human", Approve: &policy.ApproveSpec{}})
	if d.Effect != policy.EffectDeny || hits.Load() != 1 {
		t.Errorf("direct MCP: decision=%+v hits=%d (the hook dance must be unchanged)", d, hits.Load())
	}
}

// TestApproveLane pins the mode:approve escalation on the hook client.
// The hook lane never blocks: an approve-flagged local allow asks the
// online PDP (POST /v1/decide), which creates/looks-up the approval
// record, consumes any single-use exemption, and answers
// deny-with-reference or allow. The server verdict passes through
// verbatim so the model reads the reason and retries after approval. Fail
// closed exactly like serverCheck: an unreachable server denies with the
// client-side reason. Engine-side flag propagation is pinned by the policy
// decision tables; here the lanes are driven with hand-built decisions through
// the same escalate step Decide runs.
func TestApproveLane(t *testing.T) {
	ev := policy.Event{
		Kind: policy.EventToolPre, Tool: policy.ToolMCPCall,
		App: "midpoint", ToolName: "disable_user",
	}
	approved := policy.Decision{Effect: policy.EffectAllow, RuleID: "needs-human", Approve: &policy.ApproveSpec{TimeoutSeconds: 90, RetryTTLSeconds: 60}}

	t.Run("server verdict passes through verbatim", func(t *testing.T) {
		for _, verdict := range []struct {
			effect, rule, reason string
		}{
			{policy.EffectDeny, "needs-human", "Straza: approval requested (notified roles [sec-approvers]); retry after approval (ref abc123)"},
			{policy.EffectAllow, "needs-human", "Straza: approved by kim (ref abc123)"},
		} {
			var gotToken atomic.Value
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/decide" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				gotToken.Store(r.Header.Get("Authorization"))
				_ = json.NewEncoder(w).Encode(map[string]any{
					"effect": verdict.effect, "ruleId": verdict.rule, "reason": verdict.reason,
					"approvalId": "abc123",
				})
			}))
			p := &LocalPDP{
				session: Session{SessionToken: "tok-1", ExpiresAt: time.Now().Add(time.Hour)},
				client:  NewClient(srv.URL),
			}
			d := p.escalate(ev, approved)
			srv.Close()
			if d.Effect != verdict.effect || d.RuleID != verdict.rule || d.Reason != verdict.reason {
				t.Errorf("verdict %s: got %+v", verdict.effect, d)
			}
			if tok, _ := gotToken.Load().(string); tok != "Bearer tok-1" {
				t.Errorf("decide call carried %q, want the session token", tok)
			}
		}
	})

	t.Run("unreachable server denies fail-closed", func(t *testing.T) {
		// A closed port: connection refused immediately (the fail-closed branch).
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		srv.Close()
		p := &LocalPDP{
			session: Session{SessionToken: "tok-1", ExpiresAt: time.Now().Add(time.Hour)},
			client:  NewClient(srv.URL),
		}
		d := p.escalate(ev, approved)
		if d.Effect != policy.EffectDeny || d.RuleID != "needs-human" {
			t.Fatalf("offline approve allowed: %+v", d)
		}
		if d.Reason != "Straza: security layer unreachable for an approval-gated action. Denied (fail-closed)" {
			t.Errorf("deny reason not the verbatim client-side unreachable string: %q", d.Reason)
		}
	})

	t.Run("server error denies", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		p := &LocalPDP{
			session: Session{SessionToken: "tok-1", ExpiresAt: time.Now().Add(time.Hour)},
			client:  NewClient(srv.URL),
		}
		if d := p.escalate(ev, approved); d.Effect != policy.EffectDeny {
			t.Fatalf("500 from /v1/decide allowed: %+v", d)
		}
	})

	t.Run("approve short-circuits serverCheck", func(t *testing.T) {
		// A decision carrying BOTH markers resolves through the approve branch,
		// which returns before serverCheck. Pinned via the unreachable reason:
		// only approveCheck emits the "approval-gated" wording (serverCheck says
		// "server-checked").
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		srv.Close()
		p := &LocalPDP{
			session: Session{SessionToken: "tok-1", ExpiresAt: time.Now().Add(time.Hour)},
			client:  NewClient(srv.URL),
		}
		both := approved
		both.ServerCheck = true
		d := p.escalate(ev, both)
		if d.Effect != policy.EffectDeny {
			t.Fatalf("both-flagged offline allowed: %+v", d)
		}
		if !strings.Contains(d.Reason, "approval-gated") {
			t.Errorf("approve branch did not win over serverCheck: %q", d.Reason)
		}
	})

	t.Run("classify precedes approve", func(t *testing.T) {
		// classify runs first: a classifier deny is final and never reaches the
		// approve server; a classifier allow proceeds and the server verdict wins.
		t.Run("classifier deny short-circuits", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("approve server dialed after a classifier deny")
			}))
			defer srv.Close()
			p := &LocalPDP{
				classifier: &fakeClassifier{verdict: policy.ClassifyVerdict{Allowed: false, Reason: "shape"}},
				session:    Session{SessionToken: "tok-1", ExpiresAt: time.Now().Add(time.Hour)},
				client:     NewClient(srv.URL),
			}
			both := approved
			both.Classify = true
			d := p.escalate(ev, both)
			if d.Effect != policy.EffectDeny || d.Reason != "Straza: classifier: shape" {
				t.Fatalf("classifier deny should win before approve: %+v", d)
			}
		})
		t.Run("classifier allow proceeds to approve", func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"effect": "deny", "ruleId": "needs-human", "reason": "Straza: approval requested (notified roles [sec-approvers]); retry after approval (ref r1)",
				})
			}))
			defer srv.Close()
			f := &fakeClassifier{verdict: policy.ClassifyVerdict{Allowed: true}}
			p := &LocalPDP{
				classifier: f,
				session:    Session{SessionToken: "tok-1", ExpiresAt: time.Now().Add(time.Hour)},
				client:     NewClient(srv.URL),
			}
			both := approved
			both.Classify = true
			d := p.escalate(ev, both)
			if f.calls.Load() != 1 || hits.Load() != 1 {
				t.Fatalf("classifier=%d approve=%d calls, want 1 and 1", f.calls.Load(), hits.Load())
			}
			if d.Effect != policy.EffectDeny || !strings.Contains(d.Reason, "retry after approval") {
				t.Errorf("approve server verdict should win after a classify allow: %+v", d)
			}
		})
	})

	t.Run("no approve marker never dials", func(t *testing.T) {
		// The zero-cost invariant: a decision without the Approve marker
		// never touches the approve escalation, mirroring the classify lane's
		// "no classify flag never classifies" pin.
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Error("approve lane dialed the server for a non-approve decision")
		}))
		defer srv.Close()
		p := &LocalPDP{
			classifier: &fakeClassifier{verdict: policy.ClassifyVerdict{Allowed: true}},
			session:    Session{SessionToken: "tok-1", ExpiresAt: time.Now().Add(time.Hour)},
			client:     NewClient(srv.URL),
		}
		if d := p.escalate(ev, policy.Decision{Effect: policy.EffectAllow, RuleID: "plain"}); d.Effect != policy.EffectAllow {
			t.Fatalf("plain allow: %+v", d)
		}
		if d := p.escalate(ev, policy.Decision{Effect: policy.EffectAllow, RuleID: "cls", Classify: true}); d.Effect != policy.EffectAllow {
			t.Fatalf("classify-only allow: %+v", d)
		}
		// A deny that (spuriously) carries an Approve marker still never dials:
		// the guard is d.Effect == allow.
		if d := p.escalate(ev, policy.Decision{Effect: policy.EffectDeny, RuleID: "denied", Approve: &policy.ApproveSpec{}}); d.Effect != policy.EffectDeny {
			t.Fatalf("deny with stray approve marker: %+v", d)
		}
	})
}
