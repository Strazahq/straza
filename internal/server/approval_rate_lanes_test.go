package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// TestApprovalRateOnEveryLane pins that every lane that opens an approval
// meets the approval service's request rate: five new approvals in one
// session open, and the sixth new call is refused with the bounded wait,
// opens nothing and is no failure, so no fail-closed record and no count.
// The first call again joins its pending approval and is not refused.
func TestApprovalRateOnEveryLane(t *testing.T) {
	t.Parallel()
	hold := policy.Decision{
		Effect: policy.EffectAllow, RuleID: "r-hold", SetName: "guardrails",
		Approve: &policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60},
	}
	shell := func(i int) policy.Event {
		return policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: fmt.Sprintf("deploy-prod --step=%d", i)}
	}
	mcp := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "vault", ToolName: "rotate"}
	args := func(i int) json.RawMessage { return json.RawMessage(fmt.Sprintf(`{"step":%d}`, i)) }
	// gateway answers a gateway call whose held wait ends at once, so a hold
	// answers pending rather than wait for a person.
	gateway := func(app *App, ctx context.Context, claims authn.Claims, d policy.Decision, i int) (string, string) {
		cctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		ev := mcp
		ev.Args = args(i)
		o := app.gateApprove(cctx, app.approval, claims, policy.Subject{User: "kim"}, ev, d, "", ev.Args, approval.Object{}, false)
		return o.reason, o.ref
	}
	hook := func(app *App, ctx context.Context, claims authn.Claims, d policy.Decision, i int) (string, string) {
		dec, ref := app.resolveApproveHook(ctx, claims, policy.Subject{User: "kim"}, shell(i), d)
		return dec.Reason, ref
	}
	cases := []struct {
		name, lane string
		decision   policy.Decision
		call       func(app *App, ctx context.Context, claims authn.Claims, d policy.Decision, i int) (string, string)
	}{
		{"a hold on the hook lane", "hook", hold, hook},
		{"a ticket on the hook lane", "hook", ticketDecision(), hook},
		{"a hold on the gateway lane", "gateway", hold, gateway},
		{"a ticket on the gateway lane", "gateway", ticketDecision(), gateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, _ := testApp(t)
			claims := authn.Claims{Session: "sess-1", Subject: seedIdentity(t, app).ID}
			log, buf := captureLogger()
			ctx := ctxWithCapture(context.Background(), log, "rate-"+tc.lane)
			rows := func() int {
				t.Helper()
				all, err := app.approval.List(context.Background(), "")
				if err != nil {
					t.Fatal(err)
				}
				return len(all)
			}
			var first string
			for i := 1; i <= 5; i++ {
				reason, ref := tc.call(app, ctx, claims, tc.decision, i)
				if ref == "" || reason == approvalRateReason || rows() != i {
					t.Fatalf("call %d = %q (ref %q), %d rows, want a new approval", i, reason, ref, rows())
				}
				if first == "" {
					first = ref
				}
			}
			if reason, ref := tc.call(app, ctx, claims, tc.decision, 6); reason != approvalRateReason || ref != "" || rows() != 5 {
				t.Errorf("sixth call = %q (ref %q), %d rows\nwant %q, no ref and 5 rows", reason, ref, rows(), approvalRateReason)
			}
			if reason, ref := tc.call(app, ctx, claims, tc.decision, 1); reason == approvalRateReason || ref != first {
				t.Errorf("the first call again = %q (ref %q), want it to join %s", reason, ref, first)
			}
			if recs := errorRecords(buf); len(recs) != 0 || failClosedCounter(t, app, tc.lane) != 0 {
				t.Errorf("a refusal for the rate wrote %d fail-closed records and counted %v, want none:\n%s",
					len(recs), failClosedCounter(t, app, tc.lane), strings.Join(recs, "\n"))
			}
		})
	}
}

// TestApprovalRateRecords pins the one record each lane keeps for a call
// the request rate refuses: the hook lane's tool record through
// /v1/decide, and the gateway lane's MCP record through tools/call. Each
// is a deny whose reason is the refusal, with the person and the session.
func TestApprovalRateRecords(t *testing.T) {
	t.Parallel()
	t.Run("the hook lane", func(t *testing.T) {
		t.Parallel()
		app, base := testApp(t)
		kim := seedIdentity(t, app)
		ctx := context.Background()
		if _, err := app.store.Policies().Create(ctx, store.PolicySet{Name: "hold-gate", Status: "active", YAMLSource: holdGateYAML}); err != nil {
			t.Fatal(err)
		}
		if _, err := app.snapshots.Recompile(ctx); err != nil {
			t.Fatal(err)
		}
		token, session := checkinToken(t, app, base)
		var last map[string]any
		for i := 1; i <= 6; i++ {
			_, last = decide(t, base, token, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": fmt.Sprintf("deploy-prod --step=%d", i)})
		}
		if last["effect"] != "deny" || last["reason"] != approvalRateReason {
			t.Fatalf("sixth decision = %v, want the rate refusal", last)
		}
		var kept []map[string]any
		for deadline := time.Now().Add(10 * time.Second); len(kept) == 0 && time.Now().Before(deadline); {
			time.Sleep(20 * time.Millisecond)
			kept = nil
			for _, r := range toolRecords(t, app) {
				if r["reason"] == approvalRateReason {
					kept = append(kept, r)
				}
			}
		}
		if len(kept) != 1 || kept[0]["user"] != kim.ID || kept[0]["session"] != session || kept[0]["effect"] != "deny" {
			t.Errorf("records of the refusal = %v, want one deny of kim's session", kept)
		}
	})
	t.Run("the gateway lane", func(t *testing.T) {
		t.Parallel()
		app, _ := testApp(t, func(c *config.Config) { c.Apps.AllowLoopbackUpstreams = true })
		kim := seedGatewayUser(t, app, "kim", "dev")
		up, _ := echoRunsUpstream(t)
		installEchoApp(t, app, up, "echoapp", 0)
		if _, err := app.store.Policies().Create(context.Background(), store.PolicySet{Name: "echo-tickets", Status: "active", YAMLSource: `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: echo-tickets}
spec:
  match: {roles: [dev]}
  rules:
    - id: echo-ticket
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {allow: ["echo"]}
      effect: allow
      mode: approve
      approve: {class: ticket, roles: [change]}
`}); err != nil {
			t.Fatal(err)
		}
		catRecompile(t, app)
		claims := authn.Claims{Session: "sess-gw", Subject: kim.ID}
		sub := policy.Subject{User: "kim", Roles: []string{"dev"}}
		var text string
		for i := 1; i <= 6; i++ {
			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"echoapp__echo","arguments":{"text":"step %d"}}}`, i, i)
			var req rpcRequest
			if err := json.Unmarshal([]byte(body), &req); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			app.handleToolCall(w, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body)), req, claims, sub)
			text = w.Body.String()
		}
		if !strings.Contains(text, jsonText(approvalRateReason)) {
			t.Fatalf("sixth answer = %s, want the rate refusal", text)
		}
		var kept []map[string]any
		for deadline := time.Now().Add(10 * time.Second); len(kept) == 0 && time.Now().Before(deadline); {
			time.Sleep(20 * time.Millisecond)
			kept = requestRecords(t, app, approvalRateReason)
		}
		if len(kept) != 1 || kept[0]["user"] != kim.ID || kept[0]["session"] != "sess-gw" || kept[0]["effect"] != "deny" {
			t.Errorf("records of the refusal = %v, want one deny of kim's session", kept)
		}
	})
}

// jsonText is s as it reads inside a JSON string.
func jsonText(s string) string {
	b, _ := json.Marshal(s)
	return strings.Trim(string(b), `"`)
}
