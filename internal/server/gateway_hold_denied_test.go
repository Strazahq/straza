package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/policy"
)

// gatewayHoldDecision is a hold on vault's rotate tool with a two-minute
// decision window.
func gatewayHoldDecision() policy.Decision {
	return policy.Decision{
		Effect: policy.EffectAllow, RuleID: "rotate-held", SetName: "vault-gate",
		Approve: &policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 120, RetryTTLSeconds: 60},
	}
}

// TestGatewayHoldDeniedLookup pins the gateway lane's use of a person's
// standing denial of a hold, on a scripted gate: a standing denial refuses
// the call with the hook lane's sentence, or in git's words on the git
// grant route, and opens no request, a store fault on the lookup fails
// closed with one record and one count, and an approval left to use is
// used before the lookup runs.
func TestGatewayHoldDeniedLookup(t *testing.T) {
	t.Parallel()
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "vault", ToolName: "rotate"}
	denial := approval.Record{ID: "rec-denied", State: approval.StateDenied, DecidedByName: "alice", ExpiresAt: time.Now().Add(time.Minute)}
	gitDenied := "Straza: approval rec-denied was denied by alice, so git did not run, and the same git command stays refused until " +
		denial.ExpiresAt.UTC().Format(time.RFC3339) + "."
	cases := []struct {
		name       string
		words      approveWords
		gate       *fakeApprovalGate
		wantReason string
		wantRef    string
		wantRun    bool
		wantFault  bool
	}{
		{"a standing denial refuses the call", wordsCall, &fakeApprovalGate{deniedHold: denial, deniedHoldHit: true}, holdDeniedFinalReason(denial), "rec-denied", false, false},
		{"a standing denial refuses a git command in git's words", wordsGit, &fakeApprovalGate{deniedHold: denial, deniedHoldHit: true}, gitDenied, "rec-denied", false, false},
		{"a store fault on the lookup fails closed", wordsCall, &fakeApprovalGate{deniedHoldErr: errors.New("db down")}, approvalStoreDownReason, "", false, true},
		{"an approval left to use runs first", wordsCall, &fakeApprovalGate{holdHit: true, deniedHoldHit: true}, "", "", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, _ := testApp(t)
			log, buf := captureLogger()
			ctx := ctxWithCapture(context.Background(), log, "gw-denied")
			o := app.gateApproveWithin(ctx, tc.gate, authn.Claims{Session: "sess-1", Subject: "user-1"}, policy.Subject{User: "kim"}, ev, gatewayHoldDecision(), "", json.RawMessage(`{"key":"root"}`), approval.Object{}, tc.words, time.Second)
			if o.reason != tc.wantReason || o.run != tc.wantRun || (tc.wantRef != "" && o.ref != tc.wantRef) || o.pending {
				t.Errorf("outcome = %+v, want reason %q, run %v, ref %q", o, tc.wantReason, tc.wantRun, tc.wantRef)
			}
			if tc.gate.reqs != 0 || tc.gate.awaits != 0 {
				t.Errorf("requests %d, waits %d, want neither", tc.gate.reqs, tc.gate.awaits)
			}
			if tc.wantRun && tc.gate.deniedHoldChecks != 0 {
				t.Errorf("the denial lookup ran %d times after an approval was used", tc.gate.deniedHoldChecks)
			}
			counted := failClosedCounter(t, app, "gateway")
			switch {
			case tc.wantFault:
				assertOneFailClosedRecord(t, buf, "gateway", "gw-denied")
				if counted != 1 {
					t.Errorf("straza_failclosed_total{lane=gateway} = %v, want 1", counted)
				}
			case len(errorRecords(buf)) != 0 || counted != 0:
				t.Errorf("fail-closed records %d, count %v, want none", len(errorRecords(buf)), counted)
			}
		})
	}
}

// TestGatewayHoldDenialStands pins the gateway lane on the real service: a
// held call that answered pending once its in-request wait ended, and that
// a person then denied, is refused when the same call comes again inside
// the decision window, with the person's name and the same approval, and
// no new approval opens. A changed call asks again.
func TestGatewayHoldDenialStands(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	kim := seedIdentity(t, app)
	ada := seedApprover(t, app, "ada", "sec-approvers")
	ctx := context.Background()
	claims := authn.Claims{Session: "sess-gw", Subject: kim.ID}
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "vault", ToolName: "rotate"}
	call := func(args string) approveOutcome {
		cctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		e := ev
		e.Args = json.RawMessage(args)
		return app.gateApprove(cctx, app.approval, claims, policy.Subject{User: "kim"}, e, gatewayHoldDecision(), "", e.Args, approval.Object{}, false)
	}

	first := call(`{"key":"root"}`)
	if !first.pending || first.ref == "" {
		t.Fatalf("first call = %+v, want a pending approval", first)
	}
	if _, err := app.approval.Decide(ctx, first.ref, "denied", ada.ID, "console", "", ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	denied, err := app.approval.Get(ctx, first.ref)
	if err != nil {
		t.Fatal(err)
	}
	if again := call(`{"key":"root"}`); again.run || again.pending || again.ref != first.ref || again.reason != holdDeniedFinalReason(denied) {
		t.Errorf("the same call again = %+v\nwant %q on %s", again, holdDeniedFinalReason(denied), first.ref)
	}
	if changed := call(`{"key":"backup"}`); !changed.pending || changed.ref == first.ref {
		t.Errorf("a changed call = %+v, want a new pending approval", changed)
	}
	if all, err := app.approval.List(ctx, ""); err != nil || len(all) != 2 {
		t.Errorf("approvals = %d, %v, want the denied one and the changed call's", len(all), err)
	}
}
