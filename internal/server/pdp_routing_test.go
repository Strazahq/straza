package server

// Revision 14 approval routing at the PEP: an unroutable approve (no roles,
// no usable sponsor) denies at decide time with the cause and fix in the
// reason, mints no record, and bumps the unroutable counter; a
// sponsor-routed record's pending reason names the person who was actually
// notified instead of pretending the admin role was.

import (
	"context"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// counterValue reads one counter off the app's private registry (gaugeValue's
// counter twin; no testutil import).
func counterValue(t *testing.T, a *App, name string) float64 {
	t.Helper()
	families, err := a.metrics.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() == name {
			return f.GetMetric()[0].GetCounter().GetValue()
		}
	}
	t.Fatalf("counter %s not registered", name)
	return 0
}

func bareApproveDecision(ruleID string) policy.Decision {
	return policy.Decision{
		Effect: policy.EffectAllow, RuleID: ruleID, SetName: "guardrails",
		Approve: &policy.ApproveSpec{TimeoutSeconds: 90, RetryTTLSeconds: 60},
	}
}

func TestUnroutableApproveDeniesAtDecide(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	// An agent with no sponsor has no person behind it, so nothing can route.
	requester, err := app.store.Users().Create(context.Background(), store.User{Username: "bot", Email: "bot@x.io", UserType: store.UserTypeAgent})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	// Capture through the per-request logger: an unroutable deny is a decision
	// and must leave NO fail-closed Error record.
	log, buf := captureLogger()
	ctx := ctxWithCapture(context.Background(), log, "un-corr-1")

	claims := authn.Claims{Session: "sess-1", Subject: requester.ID}
	sub := policy.Subject{User: "bot"}
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "deploy-prod"}

	dec, id := app.resolveApproveHook(ctx, claims, sub, ev, bareApproveDecision("r-bare"))
	if dec.Effect != policy.EffectDeny || id != "" {
		t.Fatalf("unroutable resolve = %+v id=%q, want deny with no record", dec, id)
	}
	for _, want := range []string{
		"Straza: approval cannot be routed: ",
		`agent "bot" has no sponsor`,
		`rule "r-bare" names no approver roles`,
		"Fix: set the requester's sponsor to a person in your identity manager",
		"Denied (fail-closed)",
	} {
		if !strings.Contains(dec.Reason, want) {
			t.Errorf("reason %q missing %q", dec.Reason, want)
		}
	}
	if recs, _ := app.approval.List(ctx, ""); len(recs) != 0 {
		t.Errorf("unroutable deny minted %d records, want 0", len(recs))
	}
	if got := counterValue(t, app, "straza_approvals_unroutable_total"); got != 1 {
		t.Errorf("unroutable counter = %v, want 1", got)
	}

	// The ticket class takes the same instant deny: a doomed ticket must not
	// sit a day-scale decision window.
	tk := bareApproveDecision("r-bare-ticket")
	tk.Approve.Class = policy.ClassTicket
	dec2, id2 := app.resolveApproveHook(ctx, claims, sub, ev, tk)
	if dec2.Effect != policy.EffectDeny || id2 != "" || !strings.Contains(dec2.Reason, "Straza: approval cannot be routed: ") {
		t.Fatalf("unroutable ticket resolve = %+v id=%q, want routed-cause deny", dec2, id2)
	}
	if got := counterValue(t, app, "straza_approvals_unroutable_total"); got != 2 {
		t.Errorf("unroutable counter after ticket = %v, want 2", got)
	}
	if recs := errorRecords(buf); len(recs) != 0 {
		t.Errorf("unroutable denies wrote %d fail-closed Error records, want 0:\n%s", len(recs), buf.String())
	}
	if got := failClosedCounter(t, app, "hook"); got != 0 {
		t.Errorf("straza_failclosed_total{lane=hook} = %v after unroutable denies, want 0", got)
	}
}

func TestSponsorRoutedPendingReasonNamesSponsor(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	if _, err := app.store.Users().Create(ctx, store.User{Username: "grace", Email: "g@x.io"}); err != nil {
		t.Fatal(err)
	}
	nova, err := app.store.Users().Create(ctx, store.User{Username: "nova", Email: "n@x.io",
		UserType: store.UserTypeAgent, Sponsor: "grace"})
	if err != nil {
		t.Fatal(err)
	}

	claims := authn.Claims{Session: "sess-2", Subject: nova.ID}
	sub := policy.Subject{User: "nova"}
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "deploy-prod"}

	dec, id := app.resolveApproveHook(ctx, claims, sub, ev, bareApproveDecision("r-bare"))
	if dec.Effect != policy.EffectDeny || id == "" {
		t.Fatalf("sponsor resolve = %+v id=%q, want deny + pending record", dec, id)
	}
	want := "Straza: approval requested (notified grace); a person decides on the self-service page (" + base + "/self-service/), the console, or an enrolled phone. Retry this exact call after approval (ref " + id + ")"
	if dec.Reason != want {
		t.Errorf("reason = %q, want %q", dec.Reason, want)
	}

	// Sponsor beside a role pool: both named, person first.
	both := bareApproveDecision("r-both")
	both.Approve.Roles = []string{"sec-approvers"}
	both.Approve.Deciders = []string{policy.DeciderSponsor}
	ev2 := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "deploy-prod --both"}
	dec2, id2 := app.resolveApproveHook(ctx, claims, sub, ev2, both)
	if id2 == "" || !strings.Contains(dec2.Reason, "(notified grace and roles [sec-approvers])") {
		t.Errorf("both-pools reason = %q, want person-first clause", dec2.Reason)
	}
}

// TestSponsorlessPersonGetsAConfirmHold pins what a person who runs their own
// agent reads under a rule that routes to the person behind the agent: the
// call is held for their own confirmation, on a hold and on a ticket alike,
// and the reason names the devices that can sign it and leaves the console out.
func TestSponsorlessPersonGetsAConfirmHold(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	kim := seedIdentity(t, app) // kim: a person, no sponsor
	ctx := context.Background()
	claims := authn.Claims{Session: "sess-1", Subject: kim.ID}
	sub := policy.Subject{User: "kim"}

	hold := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "deploy-prod"}
	dec, id := app.resolveApproveHook(ctx, claims, sub, hold, bareApproveDecision("r-bare"))
	if dec.Effect != policy.EffectDeny || id == "" {
		t.Fatalf("resolve = %+v id=%q, want a deny that carries a record", dec, id)
	}
	for _, want := range []string{"this call needs the requester's confirmation", "This browser", "(ref " + id + ")"} {
		if !strings.Contains(dec.Reason, want) {
			t.Errorf("hold reason %q missing %q", dec.Reason, want)
		}
	}
	if strings.Contains(dec.Reason, "the console") {
		t.Errorf("hold reason %q names the console, which refuses a person's own request", dec.Reason)
	}
	rec, err := app.approval.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.Mode != policy.ModeConfirm || len(rec.ApproverUsers) != 0 {
		t.Errorf("record = mode %q users %v, want a confirm record with no pool", rec.Mode, rec.ApproverUsers)
	}

	tk := bareApproveDecision("r-bare-ticket")
	tk.Approve.Class = policy.ClassTicket
	ticketEv := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "deploy-stage"}
	dec2, id2 := app.resolveApproveHook(ctx, claims, sub, ticketEv, tk)
	if dec2.Effect != policy.EffectDeny || id2 == "" {
		t.Fatalf("ticket resolve = %+v id=%q, want a deny that carries a ticket", dec2, id2)
	}
	if !strings.Contains(dec2.Reason, "This browser") || strings.Contains(dec2.Reason, "the console") {
		t.Errorf("ticket reason %q must name This browser and leave the console out", dec2.Reason)
	}
}
