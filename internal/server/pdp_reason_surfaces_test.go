package server

import (
	"context"
	"testing"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// awaitVisiblePolicy exposes the native straza app to role dev: the arm where
// pending reasons may name the straza__approval_await tool (visibility rule:
// policy authorization is the native tools' only gate, buildOverlay).
const awaitVisiblePolicy = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: allow-await}
spec:
  match: {roles: [dev]}
  rules:
    - id: allow-straza
      tools: [mcp.call]
      apps: [straza]
      toolNames: {allow: ["approval_await", "approval_status", "approval_request"]}
      effect: allow
`

// TestPendingReasonsNameApprovalsPage pins the arrival copy on all three hook
// pending reasons (hold, confirm, ticket): each names the self-service page (the
// one self-serve human surface; console is admin-scoped, a phone may not be
// enrolled), and the straza__approval_await instruction appears ONLY when the
// session's policy exposes the native straza app, mirroring catalog
// visibility: naming a tool the session cannot see is a dead instruction.
func TestPendingReasonsNameApprovalsPage(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	requester := seedIdentity(t, app)
	ctx := context.Background()
	surfaces := "the self-service page (" + base + "/self-service/), the console, or an enrolled phone"
	// A confirm record is the requester's own, so only a device that signs
	// decides it and the console is left out.
	confirmSurfaces := "their enrolled phone, or the self-service page under This browser (" + base + "/self-service/)"

	holdDecision := func(rule string, confirm bool) policy.Decision {
		return policy.Decision{
			Effect: policy.EffectAllow, RuleID: rule, SetName: "guardrails",
			Approve: &policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60},
			Confirm: confirm,
		}
	}
	ticketDecision := func(rule string) policy.Decision {
		return policy.Decision{
			Effect: policy.EffectAllow, RuleID: rule, SetName: "guardrails",
			Approve: &policy.ApproveSpec{Class: policy.ClassTicket, Roles: []string{"change"}, TicketTTLSeconds: 86400, GrantTTLSeconds: 3600},
		}
	}
	resolve := func(session, command string, d policy.Decision, sub policy.Subject) (policy.Decision, string) {
		t.Helper()
		claims := authn.Claims{Session: session, Subject: requester.ID}
		ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: command}
		dec, id := app.resolveApproveHook(ctx, claims, sub, ev, d)
		if dec.Effect != policy.EffectDeny || id == "" {
			t.Fatalf("resolve(%s) = %+v id=%q, want pending deny + id", command, dec, id)
		}
		return dec, id
	}

	// Arm 1: starter snapshot. mcp.call default-denies straza:approval_await,
	// so the await tool is hidden and no reason may name it.
	subNoAwait := policy.Subject{User: "kim"}

	dec, id := resolve("sess-h1", "deploy-a", holdDecision("r-hold", false), subNoAwait)
	want := "Straza: approval requested (notified roles [sec-approvers]); a person decides on " + surfaces + ". Retry this exact call after approval (ref " + id + ")"
	if dec.Reason != want {
		t.Errorf("hold reason (await hidden)\n got %q\nwant %q", dec.Reason, want)
	}

	dec, id = resolve("sess-h2", "deploy-b", holdDecision("r-confirm", true), subNoAwait)
	want = "Straza: this call needs the requester's confirmation, on " + confirmSurfaces + "; retry after it is confirmed (ref " + id + ")"
	if dec.Reason != want {
		t.Errorf("confirm reason (await hidden)\n got %q\nwant %q", dec.Reason, want)
	}

	dec, id = resolve("sess-h3", "deploy-c", ticketDecision("r-ticket"), subNoAwait)
	want = "Straza: approval ticket " + id + " is pending. A person decides within 24 hours, on " + surfaces + ". Retry this exact call after approval; it fails if denied or expired."
	if dec.Reason != want {
		t.Errorf("ticket reason (await hidden)\n got %q\nwant %q", dec.Reason, want)
	}

	// Arm 2: a set exposes straza:approval_await to role dev; the same three
	// reasons regain the await instruction, page mention intact.
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: "allow-await", Status: "active", YAMLSource: awaitVisiblePolicy,
	}); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatalf("recompile: %v", err)
	}
	subDev := policy.Subject{User: "kim", Roles: []string{"dev"}}

	dec, id = resolve("sess-v1", "deploy-d", holdDecision("r-hold2", false), subDev)
	want = "Straza: approval requested (notified roles [sec-approvers]); a person decides on " + surfaces + ". Wait for the decision with the straza__approval_await tool, then retry (ref " + id + ")"
	if dec.Reason != want {
		t.Errorf("hold reason (await visible)\n got %q\nwant %q", dec.Reason, want)
	}

	dec, id = resolve("sess-v2", "deploy-e", holdDecision("r-confirm2", true), subDev)
	want = "Straza: this call needs the requester's confirmation, on " + confirmSurfaces + "; wait for it with the straza__approval_await tool, then retry (ref " + id + ")"
	if dec.Reason != want {
		t.Errorf("confirm reason (await visible)\n got %q\nwant %q", dec.Reason, want)
	}

	dec, id = resolve("sess-v3", "deploy-f", ticketDecision("r-ticket2"), subDev)
	want = "Straza: approval ticket " + id + " is pending. A person decides within 24 hours, on " + surfaces + ". Wait for the decision with the straza__approval_await tool (ref " + id + "), or retry this exact call after approval; it fails if denied or expired."
	if dec.Reason != want {
		t.Errorf("ticket reason (await visible)\n got %q\nwant %q", dec.Reason, want)
	}
}
