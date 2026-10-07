package agentguard

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"net/http"
	"net/http/httptest"

	"github.com/strazahq/straza/internal/policy"
)

// gatewayPolicyDoc carries an unconditional mcp.call DENY so the engine's
// verdict for the event under test is unambiguous: any hook-lane enforcement
// of it would deny. The deferral's claim is exactly that this verdict is NOT enforced
// for gateway-proxied calls: the gateway PEP is in-path and decides with the
// true names it serves.
const gatewayPolicyDoc = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: gateway-defer-test}
spec:
  match: {roles: [dev]}
  rules:
    - id: no-mcp
      tools: [mcp.call]
      effect: deny
      reason: "Straza: rule-level deny"
`

func gatewayPDP(t *testing.T, expiresAt time.Time, serverURL string) *LocalPDP {
	t.Helper()
	priv, keys := testSnapshotKey(t)
	signed, id := testSignedPolicy(t, priv, gatewayPolicyDoc)
	lookup, err := keyLookup(keys)
	if err != nil {
		t.Fatal(err)
	}
	eng, _, err := policy.OpenSnapshot(signed, id, lookup)
	if err != nil {
		t.Fatal(err)
	}
	return &LocalPDP{
		subject: policy.Subject{User: "kim", Roles: []string{"dev"}},
		engine:  eng,
		session: Session{SessionToken: "tok-1", ExpiresAt: expiresAt},
		client:  NewClient(serverURL),
	}
}

// TestGatewayDeferralWholeDecision pins the whole-decision deferral: for a
// gateway-proxied call the hook lane defers the ENTIRE decision to the
// gateway PEP, meaning allow and observe, honest reason, no server round
// trip, no enforcement of the local engine's verdict. Codex hook payloads
// carry the model-facing sanitized tool name (hyphens become underscores),
// so the hook's copy of a gateway app name is untrustworthy and would deny
// grants the gateway honors ("no policy grants MCP tool demo_tools/echo").
// The gateway is in-path for these calls and evaluates current policy with
// the true names, so the deferral loses no coverage.
func TestGatewayDeferralWholeDecision(t *testing.T) {
	live := time.Now().Add(time.Hour)
	// The event carries the MANGLED app name exactly as codex delivers it.
	// Under enforcement this policy denies it; under the deferral the name never
	// reaches a hook-lane verdict at all.
	mangled := Normalized{
		Event: policy.Event{
			Kind: policy.EventToolPre, Tool: policy.ToolMCPCall,
			App: "demo_tools", ToolName: "echo",
		},
		GatewayProxied: true,
	}

	t.Run("gateway-proxied defers even a rule deny: allow, honest reason, zero server traffic", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			hits.Add(1)
		}))
		defer srv.Close()
		p := gatewayPDP(t, live, srv.URL)
		d := p.Decide(mangled)
		if d.Effect != policy.EffectAllow {
			t.Fatalf("gateway-proxied decision = %+v, want allow (deferred)", d)
		}
		if !strings.Contains(d.Reason, "gateway") {
			t.Errorf("reason must say the decision moved to the gateway (audit honesty): %q", d.Reason)
		}
		if hits.Load() != 0 {
			t.Errorf("deferral hit the server %d times; the gateway call itself is the round trip", hits.Load())
		}
	})

	t.Run("the same event without the gateway flag keeps full enforcement", func(t *testing.T) {
		p := gatewayPDP(t, live, "http://127.0.0.1:0")
		direct := mangled
		direct.GatewayProxied = false
		if d := p.Decide(direct); d.Effect != policy.EffectDeny || d.RuleID != "no-mcp" {
			t.Errorf("direct MCP decision = %+v, want the rule deny (the hook is the only gate there)", d)
		}
	})

	t.Run("the offline fail-closed gate still precedes the deferral", func(t *testing.T) {
		p := gatewayPDP(t, time.Now().Add(-time.Hour), "http://127.0.0.1:0")
		d := p.Decide(mangled)
		if d.Effect != policy.EffectDeny || !strings.Contains(d.Reason, "expired") {
			t.Errorf("expired gateway-proxied decision = %+v, want the offline fail-closed deny", d)
		}
	})
}

// TestGatewayDeferralFromCapturedCodexPayload drives the deferral from a
// PreToolUse payload captured verbatim from codex-cli 0.146.0 on Windows,
// whose mangled app name would produce a false deny. Only the session and
// turn identifiers are shortened and the user profile name is replaced.
func TestGatewayDeferralFromCapturedCodexPayload(t *testing.T) {
	adapters, err := LoadAdapters()
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"session_id":      "019fb68c-c89c-7fd0-a953-5f860511ecef",
		"turn_id":         "019fb68c-e400-7170-aa86-f420fb5e832a",
		"transcript_path": `C:\Users\alice\.codex\sessions\2026\07\31\rollout.jsonl`,
		"cwd":             `C:\Users\alice`,
		"hook_event_name": "PreToolUse",
		"model":           "gpt-5.6-terra",
		"permission_mode": "default",
		"tool_name":       "mcp__straza__demo_tools__echo",
		"tool_input": map[string]any{
			"message":               "strazaaa",
			"_straza_justification": "The user requested an echo invocation to return the exact supplied message.",
		},
		"tool_use_id": "exec-8ab67b73-cb25-4291-8036-3581a92bf031",
	}
	n, err := Normalize(adapters["codex"], payload)
	if err != nil {
		t.Fatal(err)
	}
	if !n.GatewayProxied {
		t.Fatal("captured payload must classify as gateway-proxied (mcp__straza__ prefix)")
	}
	p := gatewayPDP(t, time.Now().Add(time.Hour), "http://127.0.0.1:0")
	if d := p.Decide(n); d.Effect != policy.EffectAllow || !strings.Contains(d.Reason, "gateway") {
		t.Errorf("captured payload decision = %+v, want deferred allow", d)
	}
}
