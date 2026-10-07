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

// TestDecideApproveZeroCost: /v1/decide never creates an approval record when
// the decision carries no Approve marker (a snapshot with no approve
// rules pays zero). Mirrors TestDecideClassifierZeroCost.
func TestDecideApproveZeroCost(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	seedIdentity(t, app)
	token, _ := checkinToken(t, app, base)

	if _, dec := decide(t, base, token, map[string]any{
		"kind": "tool.pre", "tool": "shell.exec", "command": "git status",
	}); dec["effect"] != "allow" {
		t.Fatalf("default allow expected: %v", dec)
	}
	if _, dec := decide(t, base, token, map[string]any{
		"kind": "tool.pre", "tool": "shell.exec", "command": "rm -rf /tmp/x",
	}); dec["effect"] != "deny" {
		t.Fatalf("starter-policy deny expected: %v", dec)
	}

	recs, err := app.approval.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("non-approve decisions created %d approval records (zero-cost invariant)", len(recs))
	}
}

// seedApprover creates a user holding a role and refreshes the resolver.
func seedApprover(t *testing.T, app *App, username, role string) store.User {
	t.Helper()
	ctx := context.Background()
	u, err := app.store.Users().Create(ctx, store.User{Username: username, Email: username + "@x.io"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := app.store.Roles().Create(ctx, store.Role{Name: role})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: r.ID,
	}); err != nil {
		t.Fatal(err)
	}
	app.resolver.Bump()
	return u
}

// TestResolveApproveHook drives the full hook lane through the server method:
// a miss denies-with-reference and creates a pending record; after a human
// approves, the retry uses the approval at once and allows, a second retry
// raises a new request, and the one use wrote one consumed record.
func TestResolveApproveHook(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	requester := seedIdentity(t, app)
	approver := seedApprover(t, app, "secops", "sec-approvers")
	ctx := context.Background()

	claims := authn.Claims{Session: "sess-1", Subject: requester.ID}
	sub := policy.Subject{User: "kim"}
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "deploy-prod"}
	decision := policy.Decision{
		Effect: policy.EffectAllow, RuleID: "r-approve", SetName: "guardrails",
		Approve: &policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60},
	}

	// Miss: deny with the request reason + a pending record.
	dec, id := app.resolveApproveHook(ctx, claims, sub, ev, decision)
	if dec.Effect != policy.EffectDeny || id == "" {
		t.Fatalf("first hook resolve = %+v id=%q, want deny + id", dec, id)
	}
	wantReason := "Straza: approval requested (notified roles [sec-approvers]); a person decides on the self-service page (" + base + "/self-service/), the console, or an enrolled phone. Retry this exact call after approval (ref " + id + ")"
	if dec.Reason != wantReason {
		t.Errorf("request reason = %q, want %q", dec.Reason, wantReason)
	}
	pending, _ := app.approval.List(ctx, "pending")
	if len(pending) != 1 || pending[0].ID != id {
		t.Fatalf("expected 1 pending record %s, got %+v", id, pending)
	}

	// Human approves (any channel). The approval and its use window land in
	// the database in the same write, so the retry needs no broadcast.
	if _, err := app.approval.Decide(ctx, id, "approved", approver.ID, "console", "", ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	allow, ref := app.resolveApproveHook(ctx, claims, sub, ev, decision)
	if allow.Effect != policy.EffectAllow || ref != id {
		t.Fatalf("retry after approval = %+v ref=%q, want allow on %s", allow, ref, id)
	}
	if allow.Reason != "Straza: approved by secops (ref "+id+")" {
		t.Errorf("allow reason = %q", allow.Reason)
	}

	// One use: a second retry finds nothing to use and raises a new request.
	again, againID := app.resolveApproveHook(ctx, claims, sub, ev, decision)
	if again.Effect != policy.EffectDeny || againID == "" || againID == id {
		t.Errorf("second retry = %+v ref=%q, want a deny with a new request", again, againID)
	}
	if uses := approvalUses(t, app, id); len(uses) != 1 || uses[0]["consumedBy"] != "sess-1" {
		t.Errorf("consumed records for %s = %v, want one used by sess-1", id, uses)
	}
}

// TestResolveApproveHookV2KeyOnly pins the closed fingerprint drain on the
// hook lane against the real service: a hold approval or a ticket grant keyed
// by the v2 fingerprint resolves the retry, and one keyed by v1 alone never
// does. A skipped v1 hold approval is proven present, and still unused, by
// reading its row afterwards.
func TestResolveApproveHookV2KeyOnly(t *testing.T) {
	t.Parallel()
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "deploy-prod"}
	v2, err := approval.EventKeyV2(ev, "")
	if err != nil {
		t.Fatal(err)
	}
	v1 := "sha256:stale-v1-key"
	hold := policy.Decision{
		Effect: policy.EffectAllow, RuleID: "r-approve", SetName: "guardrails",
		Approve: &policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60},
	}
	cases := []struct {
		name      string
		ticket    bool
		key       string
		wantAllow bool
	}{
		{"a hold approval under the v2 key resolves", false, v2, true},
		{"a hold approval under the v1 key stays unresolved", false, v1, false},
		{"ticket grant under the v2 key resolves", true, v2, true},
		{"ticket grant under the v1 key stays unresolved", true, v1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, _ := testApp(t)
			requester := seedIdentity(t, app)
			approver := seedApprover(t, app, "secops", "sec-approvers")
			ctx := context.Background()
			claims := authn.Claims{Session: "sess-1", Subject: requester.ID}
			sub := policy.Subject{User: "kim"}
			now := time.Now().UTC()

			if tc.ticket {
				d := ticketDecision()
				grantExp := now.Add(time.Hour)
				if _, err := app.store.Approvals().Insert(ctx, store.Approval{
					SessionID: "planning-sess", UserID: requester.ID, ArgvHash: tc.key,
					RuleID: d.RuleID, Lane: "hook", Class: "ticket", State: "approved",
					DecidedBy: approver.ID, DecidedByName: "secops",
					CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(24 * time.Hour),
					GrantExpiresAt: &grantExp,
				}); err != nil {
					t.Fatalf("seed grant: %v", err)
				}
				dec, _ := app.resolveApproveHook(ctx, claims, sub, ev, d)
				if got := dec.Effect == policy.EffectAllow; got != tc.wantAllow {
					t.Fatalf("ticket retry = %+v, want allow %v", dec, tc.wantAllow)
				}
				return
			}

			// Seed the pending hold under the key and approve it.
			row, err := app.store.Approvals().Insert(ctx, store.Approval{
				SessionID: "sess-1", UserID: requester.ID, Username: "kim", RuleID: hold.RuleID,
				SetName: hold.SetName, ArgvHash: tc.key, Lane: "hook", State: "pending",
				ApproverRoles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60,
				CreatedAt: now, ExpiresAt: now.Add(90 * time.Second),
			})
			if err != nil {
				t.Fatalf("seed hold: %v", err)
			}
			if _, err := app.approval.Decide(ctx, row.ID, "approved", approver.ID, "console", "", ""); err != nil {
				t.Fatalf("Decide: %v", err)
			}

			dec, _ := app.resolveApproveHook(ctx, claims, sub, ev, hold)
			if got := dec.Effect == policy.EffectAllow; got != tc.wantAllow {
				t.Fatalf("hold retry = %+v, want allow %v", dec, tc.wantAllow)
			}
			if !tc.wantAllow {
				if after, err := app.store.Approvals().GetByID(ctx, row.ID); err != nil || after.GrantExpiresAt == nil || after.ConsumedAt != nil {
					t.Fatalf("the v1 approval = %+v, %v, want approved with a use window and unused, or the deny proves nothing", after, err)
				}
			}
		})
	}
}

// TestResolveApproveHookServiceError: a store that does not answer denies
// fail-closed at the first approval read, the lookup of an approval to use.
func TestResolveApproveHookServiceError(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	requester := seedIdentity(t, app)
	// The per-request logger rides the context exactly as requestID binds it,
	// so the one fail-closed record lands in the capture.
	log, buf := captureLogger()
	ctx := ctxWithCapture(context.Background(), log, "hook-corr-1")

	// Close the store so the approval lookup fails: the hook must deny with
	// the fail-closed database sentence, never allow.
	_ = app.store.Close()

	claims := authn.Claims{Session: "sess-2", Subject: requester.ID}
	decision := policy.Decision{
		Effect: policy.EffectAllow, RuleID: "r-approve",
		Approve: &policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60},
	}
	dec, id := app.resolveApproveHook(ctx, claims, policy.Subject{}, policy.Event{Tool: policy.ToolShellExec, Command: "x"}, decision)
	if dec.Effect != policy.EffectDeny || id != "" {
		t.Fatalf("service error resolve = %+v id=%q, want deny + empty id", dec, id)
	}
	if dec.Reason != approvalStoreDownReason {
		t.Errorf("service-error reason = %q", dec.Reason)
	}
	rec := assertOneFailClosedRecord(t, buf, "hook", "hook-corr-1")
	if !strings.Contains(rec, "rule=r-approve") {
		t.Errorf("fail-closed record lacks the rule: %s", rec)
	}
	if got := failClosedCounter(t, app, "hook"); got != 1 {
		t.Errorf("straza_failclosed_total{lane=hook} = %v, want 1", got)
	}
}

// TestHumanDuration pins the words a ticket reason uses for its decision
// window: seconds under two minutes, then hours and minutes with a seconds
// remainder, singular at one.
func TestHumanDuration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "0 seconds"},
		{"one second", time.Second, "1 second"},
		{"ninety seconds", 90 * time.Second, "90 seconds"},
		{"two minutes", 2 * time.Minute, "2 minutes"},
		{"minutes and seconds", 150 * time.Second, "2 minutes 30 seconds"},
		{"one hour", time.Hour, "1 hour"},
		{"hour and minutes", 90 * time.Minute, "1 hour 30 minutes"},
		{"hours minutes seconds", 2*time.Hour + time.Minute + 5*time.Second, "2 hours 1 minute 5 seconds"},
		{"a day", 24 * time.Hour, "24 hours"},
		{"sub-second dropped", 3*time.Minute + 500*time.Millisecond, "3 minutes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := humanDuration(tc.d); got != tc.want {
				t.Errorf("humanDuration(%v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}

// TestApproveHookDeniesNameTheSet pins the deciding set name on every hook-lane
// deny. The straza.audit.tool record is written from the decision, so a deny
// that drops SetName files a held call under no set at all, while the gateway
// lane records the same call with its set. The cases are a fresh hold, a
// confirm-class hold, a fresh ticket, a ticket denied inside its decision
// window, and an mcp payload whose fingerprint is unavailable.
func TestApproveHookDeniesNameTheSet(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	requester := seedIdentity(t, app)
	ctx := context.Background()
	sub := policy.Subject{User: "kim"}

	hold := func(rule string, confirm bool) policy.Decision {
		return policy.Decision{
			Effect: policy.EffectAllow, RuleID: rule, SetName: "guardrails",
			Approve: &policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60},
			Confirm: confirm,
		}
	}
	shell := func(command string) policy.Event {
		return policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: command}
	}
	deniedTicket, deniedEv := ticketDecision(), shell("deploy-denied")

	cases := []struct {
		name     string
		decision policy.Decision
		ev       policy.Event
		seed     func(t *testing.T)
	}{
		{name: "a fresh hold", decision: hold("r-hold", false), ev: shell("deploy-hold")},
		{name: "a confirm-class hold", decision: hold("r-confirm", true), ev: shell("deploy-confirm")},
		{name: "a fresh ticket", decision: ticketDecision(), ev: shell("deploy-ticket")},
		{
			name: "a ticket denied inside its window", decision: deniedTicket, ev: deniedEv,
			seed: func(t *testing.T) {
				now := time.Now().UTC()
				if _, err := app.store.Approvals().Insert(ctx, store.Approval{
					SessionID: "prior-sess", UserID: requester.ID, Username: "kim",
					RuleID: deniedTicket.RuleID, SetName: deniedTicket.SetName, ArgvHash: v2Key(t, deniedEv),
					Lane: "hook", Class: "ticket", State: "denied",
					DecidedBy: "u-appr", DecidedByName: "Ada Approver",
					CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(24 * time.Hour),
				}); err != nil {
					t.Fatalf("seed the denied ticket: %v", err)
				}
			},
		},
		{
			name:     "an mcp call with no fingerprint",
			decision: hold("r-fingerprint", false),
			ev: policy.Event{
				Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "demo", ToolName: "run",
				Args: json.RawMessage(`{"a":1,"a":2}`),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.seed != nil {
				tc.seed(t)
			}
			claims := authn.Claims{Session: "sess-" + tc.decision.RuleID, Subject: requester.ID}
			dec, _ := app.resolveApproveHook(ctx, claims, sub, tc.ev, tc.decision)
			if dec.Effect != policy.EffectDeny {
				t.Fatalf("resolveApproveHook = %+v, want a deny", dec)
			}
			if dec.SetName != tc.decision.SetName {
				t.Errorf("deny set name = %q, want %q", dec.SetName, tc.decision.SetName)
			}
		})
	}
}
