package agentguard

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// serverCheckEngine compiles a PolicySet with one serverCheck-gated allow
// (the §2.6 online-gate mode) plus a plain allow lane for contrast.
func serverCheckEngine(t *testing.T) *policy.Engine {
	t.Helper()
	doc := []byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: online-gate}
spec:
  match: {roles: [dev]}
  rules:
    - id: gated-fetch
      tools: [net.fetch]
      effect: allow
      mode: serverCheck
      reason: "online gate"
`)
	snap, err := policy.Compile(policy.CompileInput{
		Documents: [][]byte{doc}, LocalDefault: "allow", MaxAge: 900, CreatedUnix: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signed, id, err := snap.Sign("k1", priv)
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	eng, _, err := policy.OpenSnapshot(signed, id, func(kid string) (ed25519.PublicKey, bool) {
		return pub, kid == "k1"
	})
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

func serverCheckPDP(t *testing.T, serverURL string) *LocalPDP {
	t.Helper()
	return &LocalPDP{
		subject: policy.Subject{User: "kim", Roles: []string{"dev"}},
		engine:  serverCheckEngine(t),
		session: Session{SessionToken: "tok-1", ExpiresAt: time.Now().Add(time.Hour)},
		client:  NewClient(serverURL),
	}
}

// TestServerCheckLane pins the online-gate escalation:
// a serverCheck-flagged local allow consults POST /v1/decide, the server's
// verdict wins verbatim, and ANY failure to reach it is a deny. The rule
// author opted into an online gate, so offline can never mean allow.
func TestServerCheckLane(t *testing.T) {
	fetchEvent := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolNetFetch, Paths: []string{"https://x.io"}}

	t.Run("server verdict wins", func(t *testing.T) {
		for _, verdict := range []struct {
			effect, rule, reason string
		}{
			{policy.EffectDeny, "central-veto", "Straza: vetoed centrally"},
			{policy.EffectAllow, "central-ok", ""},
		} {
			var gotToken atomic.Value
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/decide" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				gotToken.Store(r.Header.Get("Authorization"))
				_ = json.NewEncoder(w).Encode(map[string]any{
					"effect": verdict.effect, "ruleId": verdict.rule, "reason": verdict.reason,
				})
			}))
			p := serverCheckPDP(t, srv.URL)
			d := p.Decide(Normalized{Event: fetchEvent})
			srv.Close()
			if d.Effect != verdict.effect || d.RuleID != verdict.rule {
				t.Errorf("verdict %s: got %+v", verdict.effect, d)
			}
			if tok, _ := gotToken.Load().(string); tok != "Bearer tok-1" {
				t.Errorf("decide call carried %q, want the session token", tok)
			}
		}
	})

	t.Run("unreachable server denies", func(t *testing.T) {
		// A closed port: connection refused immediately (the fail-closed branch).
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		srv.Close()
		p := serverCheckPDP(t, srv.URL)
		d := p.Decide(Normalized{Event: fetchEvent})
		if d.Effect != policy.EffectDeny {
			t.Fatalf("offline serverCheck allowed: %+v", d)
		}
		if !strings.Contains(d.Reason, "unreachable") || !strings.Contains(d.Reason, "doctor") {
			t.Errorf("deny reason not actionable: %q", d.Reason)
		}
	})

	t.Run("server error denies", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		p := serverCheckPDP(t, srv.URL)
		if d := p.Decide(Normalized{Event: fetchEvent}); d.Effect != policy.EffectDeny {
			t.Fatalf("500 from /v1/decide allowed: %+v", d)
		}
	})

	t.Run("plain allow never phones home", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"effect": "allow"})
		}))
		defer srv.Close()
		p := serverCheckPDP(t, srv.URL)
		// shell.exec matches no rule → profile default allow, NOT serverCheck.
		d := p.Decide(Normalized{Event: policy.Event{
			Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "git status",
		}})
		if d.Effect != policy.EffectAllow {
			t.Fatalf("default allow expected: %+v", d)
		}
		// Observational events never consult the engine or the server.
		if d := p.Decide(Normalized{Event: policy.Event{Kind: policy.EventSessionEnd}}); d.Effect != policy.EffectAllow || !d.Default {
			t.Fatalf("observational event: %+v", d)
		}
		if hits.Load() != 0 {
			t.Errorf("fast path phoned home %d times (invariant: no network on the fast path)", hits.Load())
		}
	})
}
