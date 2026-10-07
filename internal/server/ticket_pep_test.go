package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// ticketDecision is a winning allow carrying a normalized ticket-class
// ApproveSpec (what the engine hands the PEP for a `class: ticket` rule).
func ticketDecision() policy.Decision {
	return policy.Decision{
		Effect: policy.EffectAllow, RuleID: "prod-deploy-ticket", SetName: "change-window",
		Approve: &policy.ApproveSpec{
			Class: policy.ClassTicket, Roles: []string{"change-approvers"},
			TicketTTLSeconds: 86400, GrantTTLSeconds: 3600, Bind: policy.BindFingerprint,
		},
	}
}

// v2Key mints the fingerprint a drain looks up for ev under the default
// binding, so a seeded grant is one the lanes can find.
func v2Key(t *testing.T, ev policy.Event) string {
	t.Helper()
	k, err := approval.EventKeyV2(ev, "")
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// seedTicketGrant inserts a live approved grant for (userID, the v2 key of ev) as a
// prior/other session's approval would leave behind.
func seedTicketGrant(t *testing.T, app *App, userID, ruleID string, ev policy.Event) {
	t.Helper()
	grantExp := time.Now().UTC().Add(time.Hour)
	if _, err := app.store.Approvals().Insert(context.Background(), store.Approval{
		SessionID: "planning-sess", UserID: userID, ArgvHash: v2Key(t, ev),
		RuleID: ruleID, Lane: "hook", Class: "ticket", State: "approved",
		DecidedBy: "u-appr", DecidedByName: "Ada Approver",
		CreatedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(24 * time.Hour),
		GrantExpiresAt: &grantExp,
	}); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
}

// TestResolveApproveHookTicketDenyWithTicket: a ticket-class rule NEVER blocks
// and NEVER self-grants. With no consumable grant the hook lane opens (or
// dedupes onto) the one pending ticket and returns a deny-with-ticket reason
// the model can act on; a repeated call attaches to the same ticket.
func TestResolveApproveHookTicketDenyWithTicket(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	requester := seedIdentity(t, app)
	ctx := context.Background()

	claims := authn.Claims{Session: "sess-1", Subject: requester.ID}
	sub := policy.Subject{User: "kim"}
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "deploy-prod"}
	d := ticketDecision()

	dec, id := app.resolveApproveHook(ctx, claims, sub, ev, d)
	if dec.Effect != policy.EffectDeny || id == "" {
		t.Fatalf("ticket miss = %+v id=%q, want deny + id", dec, id)
	}
	if !strings.Contains(dec.Reason, id) ||
		!strings.Contains(dec.Reason, "ticket") ||
		!strings.Contains(dec.Reason, "person decides within") ||
		!strings.Contains(dec.Reason, "the self-service page") ||
		!strings.Contains(dec.Reason, "Retry this exact call after approval") {
		t.Errorf("deny-with-ticket reason = %q", dec.Reason)
	}
	// Starter snapshot: mcp.call default-denies straza:approval_await, so the
	// reason must not send the model at a tool this session cannot see.
	if strings.Contains(dec.Reason, "straza__approval_await") {
		t.Errorf("await tool named while hidden: %q", dec.Reason)
	}

	pending, _ := app.approval.List(ctx, "pending")
	if len(pending) != 1 || pending[0].Class != "ticket" {
		t.Fatalf("want 1 pending ticket, got %+v", pending)
	}
	if got := pending[0].ExpiresAt.Sub(pending[0].CreatedAt); got != 86400*time.Second {
		t.Errorf("ticket decision window = %s, want 24h", got)
	}

	// Dedupe: a repeated denied call attaches to the SAME open ticket.
	dec2, id2 := app.resolveApproveHook(ctx, claims, sub, ev, d)
	if dec2.Effect != policy.EffectDeny || id2 != id {
		t.Errorf("dedupe: dec2=%+v id2=%q, want deny + %q", dec2, id2, id)
	}
	pending, _ = app.approval.List(ctx, "pending")
	if len(pending) != 1 {
		t.Errorf("dedupe must not open a second ticket, got %d", len(pending))
	}
}

// TestResolveApproveHookTicketDedupesAcrossSessions is the PEP half of the
// cross-session ticket dedupe: the user's NEXT session hits
// the same gated call (a new agent run, a restarted CLI) and must be handed
// back the SAME ticket ref the first session opened, not a fresh ticket and a
// second approver push. The model-facing reason is what the human reads, so it
// must name that one ref both times.
func TestResolveApproveHookTicketDedupesAcrossSessions(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	requester := seedIdentity(t, app)
	ctx := context.Background()

	sub := policy.Subject{User: "kim"}
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "deploy-prod"}
	d := ticketDecision()

	first, id := app.resolveApproveHook(ctx,
		authn.Claims{Session: "planning-sess", Subject: requester.ID}, sub, ev, d)
	if first.Effect != policy.EffectDeny || id == "" {
		t.Fatalf("first ticket = %+v id=%q, want deny + a ticket ref", first, id)
	}

	// A FRESH session of the same user, same call.
	second, id2 := app.resolveApproveHook(ctx,
		authn.Claims{Session: "fresh-sess", Subject: requester.ID}, sub, ev, d)
	if second.Effect != policy.EffectDeny {
		t.Fatalf("fresh session = %+v, want deny (a ticket never self-grants)", second)
	}
	if id2 != id {
		t.Errorf("fresh session got ticket %q, want the open ticket %q", id2, id)
	}
	if !strings.Contains(second.Reason, id) {
		t.Errorf("fresh-session reason must name the open ticket %q: %q", id, second.Reason)
	}

	pending, _ := app.approval.List(ctx, "pending")
	if len(pending) != 1 {
		t.Fatalf("a fresh session must not open a second ticket, got %d pending", len(pending))
	}
	if pending[0].SessionID != "planning-sess" {
		t.Errorf("the surviving ticket = session %q, want the original planning-sess",
			pending[0].SessionID)
	}

	// A different user on the same call still gets their own ticket.
	otherUser, err := app.store.Users().Create(ctx, store.User{Username: "dana", Email: "dana@x.io"})
	if err != nil {
		t.Fatalf("create second user: %v", err)
	}
	other, otherID := app.resolveApproveHook(ctx,
		authn.Claims{Session: "dana-sess", Subject: otherUser.ID}, sub, ev, d)
	if other.Effect != policy.EffectDeny || otherID == "" || otherID == id {
		t.Errorf("another user must get their own ticket: %+v id=%q", other, otherID)
	}
	if pending, _ := app.approval.List(ctx, "pending"); len(pending) != 2 {
		t.Errorf("two users = two tickets, got %d pending", len(pending))
	}
}

// TestResolveApproveHookTicketConsumes: a live approved grant raised by a
// DIFFERENT (planning) session is consumed by this fresh session's exact retry
// (allow-once), and the grant is single-use (a second retry denies with a new
// ticket). Proves cross-session (plan-gate) consumption keyed by user, not
// session.
func TestResolveApproveHookTicketConsumes(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	requester := seedIdentity(t, app)
	ctx := context.Background()

	claims := authn.Claims{Session: "fresh-sess", Subject: requester.ID}
	sub := policy.Subject{User: "kim"}
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "deploy-prod"}
	d := ticketDecision()
	seedTicketGrant(t, app, requester.ID, d.RuleID, ev)

	dec, id := app.resolveApproveHook(ctx, claims, sub, ev, d)
	if dec.Effect != policy.EffectAllow {
		t.Fatalf("ticket consume = %+v, want allow", dec)
	}
	if id == "" || !strings.Contains(dec.Reason, id) {
		t.Errorf("consumed allow must echo the grant ref: reason=%q id=%q", dec.Reason, id)
	}

	// Single-use: a second retry no longer finds the grant → deny (new ticket).
	dec2, _ := app.resolveApproveHook(ctx, claims, sub, ev, d)
	if dec2.Effect != policy.EffectDeny {
		t.Errorf("grant must be single-use; second retry = %+v", dec2)
	}
}

// TestTicketBindPredicateBindsExactCall pins the reserved bind: predicate on
// both approval lanes: a grant approved for one call is used only by that
// exact call, whether the rule says fingerprint or predicate. A call with a
// different command or different arguments opens a fresh ticket and leaves
// the grant in place. It fails the day a lane lets predicate widen a grant.
func TestTicketBindPredicateBindsExactCall(t *testing.T) {
	t.Parallel()
	shell := func(cmd string) policy.Event {
		return policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: cmd}
	}
	call := func(args string) policy.Event {
		return policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "slack", ToolName: "postMessage", Args: json.RawMessage(args)}
	}
	lanes := []struct {
		name            string
		approved, other policy.Event
	}{
		{"hook", shell("deploy-prod --tag a"), shell("deploy-prod --tag b")},
		{"gateway", call(`{"text":"a"}`), call(`{"text":"b"}`)},
	}
	for _, lane := range lanes {
		for _, bind := range []string{policy.BindFingerprint, policy.BindPredicate} {
			t.Run(lane.name+"/"+bind, func(t *testing.T) {
				t.Parallel()
				app, _ := testApp(t)
				requester := seedIdentity(t, app)
				ctx := context.Background()
				claims := authn.Claims{Session: "fresh-sess", Subject: requester.ID}
				sub := policy.Subject{User: "kim"}
				d := ticketDecision()
				d.Approve.Bind, d.Approve.Binding = bind, policy.ApproveBindingCall
				seedTicketGrant(t, app, requester.ID, d.RuleID, lane.approved)
				allowed := func(ev policy.Event) bool {
					if lane.name == "hook" {
						dec, _ := app.resolveApproveHook(ctx, claims, sub, ev, d)
						return dec.Effect == policy.EffectAllow
					}
					_, ok := app.resolveApprove(ctx, app.approval, claims, sub, ev, d, "", ev.Args)
					return ok
				}
				if allowed(lane.other) {
					t.Fatal("a different call used the grant")
				}
				if pending, _ := app.approval.List(ctx, "pending"); len(pending) != 1 {
					t.Errorf("a different call left %d pending tickets, want 1", len(pending))
				}
				if !allowed(lane.approved) {
					t.Fatal("the approved call did not use its grant")
				}
				if allowed(lane.approved) {
					t.Error("the grant was used twice")
				}
			})
		}
	}
}

// TestResolveApproveHookTicketDeniedFinal: when a human already denied the
// latest ticket for this exact call and its decision window is still open, the
// hook lane denies FINAL: it names the denier + the blocked-until deadline and
// does NOT open a fresh ticket (no approver re-spam within the window).
func TestResolveApproveHookTicketDeniedFinal(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	requester := seedIdentity(t, app)
	ctx := context.Background()

	claims := authn.Claims{Session: "sess-1", Subject: requester.ID}
	sub := policy.Subject{User: "kim"}
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "deploy-prod"}
	d := ticketDecision()

	// A prior, within-window denial for this exact call.
	future := time.Now().UTC().Add(12 * time.Hour)
	if _, err := app.store.Approvals().Insert(ctx, store.Approval{
		SessionID: "old-sess", UserID: requester.ID, RuleID: d.RuleID, ArgvHash: v2Key(t, ev),
		Lane: "hook", Class: "ticket", State: "denied",
		DecidedBy: "u-appr", DecidedByName: "Ada Approver",
		CreatedAt: time.Now().Add(-time.Minute), ExpiresAt: future,
	}); err != nil {
		t.Fatalf("seed denied ticket: %v", err)
	}

	dec, id := app.resolveApproveHook(ctx, claims, sub, ev, d)
	if dec.Effect != policy.EffectDeny || id == "" {
		t.Fatalf("deny-final = %+v id=%q, want deny + the denied ref", dec, id)
	}
	if !strings.Contains(dec.Reason, "was denied by Ada Approver") ||
		!strings.Contains(dec.Reason, "stays denied until") {
		t.Errorf("deny-final reason = %q", dec.Reason)
	}
	// It must NOT open a fresh ticket.
	if pending, _ := app.approval.List(ctx, "pending"); len(pending) != 0 {
		t.Errorf("deny-final must not open a ticket, got %d pending", len(pending))
	}
}

// TestResolveApproveHookTicketServiceError: a store failure on the ticket lane
// denies fail-closed (never allow, never a stray grant).
func TestResolveApproveHookTicketServiceError(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	requester := seedIdentity(t, app)
	ctx := context.Background()
	_ = app.store.Close() // break the DB: FindConsumableGrant/Request both fail

	claims := authn.Claims{Session: "sess-2", Subject: requester.ID}
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "deploy-prod"}
	dec, id := app.resolveApproveHook(ctx, claims, policy.Subject{}, ev, ticketDecision())
	if dec.Effect != policy.EffectDeny {
		t.Fatalf("ticket service error = %+v, want deny", dec)
	}
	if id != "" || !strings.Contains(dec.Reason, "fail-closed") {
		t.Errorf("service-error resolve = reason:%q id:%q, want fail-closed deny", dec.Reason, id)
	}
}

// TestGatewayResolveApproveTicket pins the gateway ticket lane: it NEVER blocks
// (Await is never called), a consumable grant proceeds allow-once, a miss
// returns the deny-with-ticket tool error, and a service error fails closed.
func TestGatewayResolveApproveTicket(t *testing.T) {
	t.Parallel()
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "vault", ToolName: "rotate_root_key"}
	claims := authn.Claims{Session: "sess-1", Subject: "user-1"}
	sub := policy.Subject{User: "kim", Roles: []string{"agent"}}
	decision := policy.Decision{
		Effect: policy.EffectAllow, RuleID: "rotate-ticket", SetName: "change",
		Approve: &policy.ApproveSpec{Class: policy.ClassTicket, TicketTTLSeconds: 86400, GrantTTLSeconds: 3600, Bind: policy.BindFingerprint},
	}
	app := &App{}

	t.Run("grant hit proceeds, never blocks", func(t *testing.T) {
		g := &fakeApprovalGate{grantHit: true, grant: approval.Record{ID: "grant-1", DecidedByName: "Ada"}}
		res, ok := app.resolveApprove(context.Background(), g, claims, sub, ev, decision, "why", nil)
		if !ok || res != "" {
			t.Fatalf("grant hit should proceed: ok=%v res=%v", ok, res)
		}
		if g.awaits != 0 {
			t.Errorf("ticket path must never Await, got %d", g.awaits)
		}
		if g.reqs != 0 {
			t.Errorf("grant hit must not open a ticket, got %d requests", g.reqs)
		}
	})

	t.Run("miss opens a ticket and denies-with-ticket, never blocks", func(t *testing.T) {
		g := &fakeApprovalGate{grantHit: false}
		res, ok := app.resolveApprove(context.Background(), g, claims, sub, ev, decision, "rotate the root key", nil)
		if ok {
			t.Fatal("a ticket miss must not proceed")
		}
		if g.awaits != 0 {
			t.Errorf("ticket path must never Await, got %d", g.awaits)
		}
		if g.reqs != 1 || g.lastInput.Lane != "gateway" || g.lastInput.Spec.Class != policy.ClassTicket {
			t.Errorf("ticket request = reqs:%d input:%+v", g.reqs, g.lastInput)
		}
		got := res
		if !strings.Contains(got, "rec-1") || !strings.Contains(got, "ticket") ||
			!strings.Contains(got, "the self-service page") ||
			!strings.Contains(got, "Retry this exact call after approval") {
			t.Errorf("deny-with-ticket reason = %q", got)
		}
	})

	t.Run("service error fails closed", func(t *testing.T) {
		g := &fakeApprovalGate{grantErr: context.DeadlineExceeded}
		res, ok := app.resolveApprove(context.Background(), g, claims, sub, ev, decision, "", nil)
		if ok {
			t.Fatal("a ticket service error must not proceed")
		}
		if got := res; got != approvalStoreDownReason {
			t.Errorf("service-error reason = %q", got)
		}
	})

	t.Run("within-window denial denies final, never opens a ticket", func(t *testing.T) {
		g := &fakeApprovalGate{deniedFinalHit: true, deniedFinal: approval.Record{
			ID: "tk-9", DecidedByName: "Ada", ExpiresAt: time.Now().Add(6 * time.Hour),
		}}
		res, ok := app.resolveApprove(context.Background(), g, claims, sub, ev, decision, "", nil)
		if ok {
			t.Fatal("a within-window denial must not proceed")
		}
		if g.reqs != 0 {
			t.Errorf("deny-final must not open a ticket, got %d requests", g.reqs)
		}
		if g.awaits != 0 {
			t.Errorf("ticket path must never Await, got %d", g.awaits)
		}
		got := res
		if !strings.Contains(got, "tk-9") || !strings.Contains(got, "was denied by Ada") ||
			!strings.Contains(got, "stays denied until") {
			t.Errorf("deny-final reason = %q", got)
		}
	})
}

// TestGatewayHoldUnaffectedByTicketPath is the hold regression pin: a hold-class
// decision still takes the blocking exemption/Await lane and NEVER touches the
// ticket ConsumeGrant path (the two lanes stay independent).
func TestGatewayHoldUnaffectedByTicketPath(t *testing.T) {
	t.Parallel()
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user"}
	claims := authn.Claims{Session: "sess-1", Subject: "user-1"}
	sub := policy.Subject{User: "kim"}
	hold := policy.Decision{
		Effect: policy.EffectAllow, RuleID: "needs-human", SetName: "iga",
		Approve: &policy.ApproveSpec{Class: policy.ClassHold, Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60},
	}
	app := &App{}

	g := &fakeApprovalGate{await: approval.Record{ID: "rec-1", State: approval.StateApproved}}
	if _, ok := app.resolveApprove(context.Background(), g, claims, sub, ev, hold, "why", nil); !ok {
		t.Fatal("approved hold should proceed")
	}
	if g.awaits != 1 {
		t.Errorf("a hold must block on Await exactly once, got %d", g.awaits)
	}
	if g.consumes != 0 {
		t.Errorf("a hold must never touch the ticket ConsumeGrant path, got %d", g.consumes)
	}
}

// TestTicketRoutesThroughApproveLane confirms a ticket rule flows through the
// same escalation lane hold approve rules do: the engine hands the PEP an allow
// carrying Approve with Class=ticket, so the client's approveCheck (which fails
// closed offline) governs it exactly like a hold; a ticket can never be
// self-granted offline. The server half then proves it denies (never grants)
// without a consumable grant.
func TestTicketRoutesThroughApproveLane(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	requester := seedIdentity(t, app)
	ctx := context.Background()

	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: "ticket-deploy", Status: "active", YAMLSource: `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: ticket-deploy}
spec:
  match: {roles: [dev]}
  rules:
    - id: deploy-ticket
      tools: [shell.exec]
      command: {allowPatterns: ["deploy-prod*"]}
      effect: allow
      mode: approve
      approve:
        class: ticket
        roles: [change-approvers]
        ticketTTLSeconds: 86400
        grantTTLSeconds: 3600
`,
	}); err != nil {
		t.Fatalf("Create policy: %v", err)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatalf("Recompile: %v", err)
	}

	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "deploy-prod --now"}
	dec := app.snapshots.Current().Engine.Evaluate(ev, policy.Subject{Roles: []string{"dev"}})
	if dec.Effect != policy.EffectAllow || dec.Approve == nil {
		t.Fatalf("ticket rule must yield an approve-gated allow, got %+v", dec)
	}
	if dec.Approve.Class != policy.ClassTicket {
		t.Errorf("Approve.Class = %q, want ticket (routes through the approve/offline-deny lane like a hold)", dec.Approve.Class)
	}

	// The server (online) half: with no consumable grant, the ticket DENIES;
	// it never self-grants. Offline the client cannot reach here at all ⇒ deny.
	claims := authn.Claims{Session: "s", Subject: requester.ID}
	out, _ := app.resolveApproveHook(ctx, claims, policy.Subject{Roles: []string{"dev"}}, ev, dec)
	if out.Effect != policy.EffectDeny {
		t.Fatalf("ticket without a grant must deny (never grant), got %+v", out)
	}
}
