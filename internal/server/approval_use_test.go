package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// approvalUses returns the data of every consumed approval record the outbox
// holds for the approval id, oldest first. It reads through ListRecent,
// published rows included, because the relay marks rows on its own schedule.
func approvalUses(t *testing.T, app *App, id string) []map[string]any {
	t.Helper()
	rows, err := app.store.Outbox().ListRecent(context.Background(), 1000)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	var out []map[string]any
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Subject != "straza.audit.approval" {
			continue
		}
		var ce struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(rows[i].CE), &ce); err != nil {
			t.Fatalf("bad CE in outbox: %v", err)
		}
		if ce.Data["phase"] == "consumed" && ce.Data["approvalId"] == id {
			out = append(out, ce.Data)
		}
	}
	return out
}

// FindConsumableHold, FindConsumableGrant and MarkConsumed extend the
// faultStore fixture of api_identity_outage_test.go to the use step: the
// lookup of an approval to use faults under "approval-lookup", and the
// atomic use write under "approval-use".
func (a faultApprovals) FindConsumableHold(ctx context.Context, userID, sessionID, ruleID, argvHash string, now time.Time) (store.Approval, error) {
	if err := a.f.fault("approval-lookup"); err != nil {
		return store.Approval{}, err
	}
	return a.ApprovalRepo.FindConsumableHold(ctx, userID, sessionID, ruleID, argvHash, now)
}

func (a faultApprovals) FindConsumableGrant(ctx context.Context, userID, argvHash string, now time.Time) (store.Approval, error) {
	if err := a.f.fault("approval-lookup"); err != nil {
		return store.Approval{}, err
	}
	return a.ApprovalRepo.FindConsumableGrant(ctx, userID, argvHash, now)
}

func (a faultApprovals) MarkConsumed(ctx context.Context, id, consumedBy string, now, asOf time.Time) (bool, error) {
	if err := a.f.fault("approval-use"); err != nil {
		return false, err
	}
	return a.ApprovalRepo.MarkConsumed(ctx, id, consumedBy, now, asOf)
}

// TestGatewayHoldUse pins the gateway gate's use of a hold's approval with a
// scripted service: a retry that uses one runs without a request, the held
// call runs only when its use wins, and every store fault denies with its
// sentence and one fail-closed record.
func TestGatewayHoldUse(t *testing.T) {
	t.Parallel()
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user"}
	claims := authn.Claims{Session: "sess-1", Subject: "user-1"}
	sub := policy.Subject{User: "kim", Roles: []string{"agent"}}
	decision := policy.Decision{
		Effect: policy.EffectAllow, RuleID: "needs-human", SetName: "iga",
		Approve: &policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60},
	}
	approved := func(window time.Duration) approval.Record {
		deadline := time.Now().Add(window)
		return approval.Record{ID: "rec-1", SessionID: "sess-1", Class: policy.ClassHold, State: approval.StateApproved, GrantExpiresAt: &deadline}
	}
	olderServer := approval.Record{ID: "rec-1", SessionID: "sess-1", Class: policy.ClassHold, State: approval.StateApproved}
	ticket := approved(time.Hour)
	ticket.Class = policy.ClassTicket
	usedAlready := "Straza: approval rec-1 allows one run of this call, and that run already happened or its time ran out, so this call did not run. Send the call again to ask for a new approval."
	noUseWindow := "Straza: approval rec-1 was granted on a Straza server that runs an older version, which does not record how long an approval may be used, so this server cannot use it and this call did not run. Send the call again to ask for a new approval. If this keeps happening, ask your Straza administrator to finish upgrading every Straza server to the same version."
	cases := []struct {
		name        string
		gate        func() *fakeApprovalGate
		wantProceed bool
		wantReason  string
		wantReqs    int
		wantHeld    int
		wantLogged  int
	}{
		{"a retry that uses an approval runs without a request",
			func() *fakeApprovalGate { return &fakeApprovalGate{holdHit: true} }, true, "", 0, 0, 0},
		{"a lookup the store cannot answer denies before any request",
			func() *fakeApprovalGate { return &fakeApprovalGate{holdErr: errors.New("db down")} }, false, approvalStoreDownReason, 0, 0, 1},
		{"the held call runs on the approval it waited for",
			func() *fakeApprovalGate { return &fakeApprovalGate{await: approved(time.Minute)} }, true, "", 1, 1, 0},
		{"a held call whose approval another run used is refused",
			func() *fakeApprovalGate { return &fakeApprovalGate{await: approved(time.Minute), heldLost: true} }, false, usedAlready, 1, 1, 0},
		{"a held call whose use cannot be recorded is told the time left",
			func() *fakeApprovalGate {
				return &fakeApprovalGate{await: approved(90 * time.Second), heldErr: errors.New("db down")}
			}, false,
			"Straza: approval rec-1 was granted, but the Straza server could not reach its database to record its use, so the call did not run. Denied (fail-closed). The approval may still be usable: send the same call again within 89 seconds, and if that call asks for a new approval, the person must approve again. If this keeps happening, ask your Straza administrator to check the server's database connection.", 1, 1, 1},
		{"a held call whose use cannot be recorded after the retry window is told the database failed",
			func() *fakeApprovalGate {
				return &fakeApprovalGate{await: approved(-time.Second), heldErr: errors.New("db down")}
			}, false,
			"Straza: approval rec-1 was granted, but the Straza server could not reach its database to record its use, so the call did not run. Denied (fail-closed). Send the call again to ask for a new approval. If this keeps happening, ask your Straza administrator to check the server's database connection.", 1, 1, 1},
		{"a held call whose approval an older server decided is told why",
			func() *fakeApprovalGate { return &fakeApprovalGate{await: olderServer, heldLost: true} }, false, noUseWindow, 1, 1, 0},
		{"a store fault on an approval an older server decided is told the same",
			func() *fakeApprovalGate { return &fakeApprovalGate{await: olderServer, heldErr: errors.New("db down")} }, false, noUseWindow, 1, 1, 1},
		{"a held call that waited on a ticket is told why",
			func() *fakeApprovalGate { return &fakeApprovalGate{await: ticket, heldLost: true} }, false,
			"Straza: approval rec-1 approved a ticket for this call, and a held call cannot use a ticket's approval, so this call did not run. This happens when the rule changed from a ticket to a hold while the ticket was open. Send the call again to ask for a new approval.", 1, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log, buf := captureLogger()
			ctx := ctxWithCapture(context.Background(), log, "gw-use")
			gate := tc.gate()
			res, ok := (&App{}).resolveApprove(ctx, gate, claims, sub, ev, decision, "why", nil)
			if ok != tc.wantProceed || res != tc.wantReason {
				t.Fatalf("resolveApprove = %q, %v, want %q, %v", res, ok, tc.wantReason, tc.wantProceed)
			}
			if gate.reqs != tc.wantReqs || gate.heldUses != tc.wantHeld || gate.holdUses != 1 {
				t.Errorf("requests = %d, held uses = %d, retry lookups = %d, want %d, %d and 1",
					gate.reqs, gate.heldUses, gate.holdUses, tc.wantReqs, tc.wantHeld)
			}
			if got := len(errorRecords(buf)); got != tc.wantLogged {
				t.Errorf("fail-closed records = %d, want %d:\n%s", got, tc.wantLogged, buf.String())
			}
		})
	}
}

// TestGatewayHoldApprovalRunsOnce drives the gateway lane against the real
// service: whether the call was still held when the person approved or
// had already answered pending and was retried, the
// approval runs one call, the identical call after it raises a new request,
// and the one use wrote one consumed record naming the session.
func TestGatewayHoldApprovalRunsOnce(t *testing.T) {
	t.Parallel()
	for _, heldWhenApproved := range []bool{true, false} {
		name := "approved while held"
		if !heldWhenApproved {
			name = "approved after the pending answer"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app, _ := testApp(t)
			requester := seedIdentity(t, app)
			approver := seedApprover(t, app, "secops", "sec-approvers")
			ctx := context.Background()
			claims := authn.Claims{Session: "sess-1", Subject: requester.ID}
			sub := policy.Subject{User: "kim"}
			args := json.RawMessage(`{"user":"bob"}`)
			ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user", Args: args}
			d := policy.Decision{
				Effect: policy.EffectAllow, RuleID: "r-hold", SetName: "iga",
				Approve: &policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60},
			}
			call := func(wait time.Duration) (string, bool) {
				cctx, cancel := context.WithTimeout(ctx, wait)
				defer cancel()
				return app.resolveApprove(cctx, app.approval, claims, sub, ev, d, "why", args)
			}

			type result struct {
				reason string
				ok     bool
			}
			first := make(chan result, 1)
			wait := 200 * time.Millisecond
			if heldWhenApproved {
				wait = 10 * time.Second
			}
			go func() { r, ok := call(wait); first <- result{r, ok} }()
			id := onePending(t, app)
			if !heldWhenApproved {
				if r := <-first; r.ok || !strings.Contains(r.reason, "approval pending (ref "+id+")") {
					t.Fatalf("first call = %q, %v, want the pending answer", r.reason, r.ok)
				}
			}
			if _, err := app.approval.Decide(ctx, id, "approved", approver.ID, "console", "", ""); err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if heldWhenApproved {
				if r := <-first; !r.ok {
					t.Fatalf("held call after approval = %q, want it to run", r.reason)
				}
			} else if r, ok := call(10 * time.Second); !ok {
				t.Fatalf("retry after approval = %q, want it to run", r)
			}

			if r, ok := call(200 * time.Millisecond); ok || !strings.Contains(r, "approval pending (ref ") || strings.Contains(r, id) {
				t.Fatalf("identical call after the run = %q, %v, want a new request answered pending", r, ok)
			}
			if again := onePending(t, app); again == id {
				t.Fatalf("the pending record is still %s, want a new request", id)
			}
			if uses := approvalUses(t, app, id); len(uses) != 1 || uses[0]["consumedBy"] != "sess-1" || uses[0]["user"] != requester.ID {
				t.Errorf("consumed records for %s = %v, want one used by sess-1 for %s", id, uses, requester.ID)
			}
		})
	}
}

// onePending waits for exactly one pending approval record and returns its id.
func onePending(t *testing.T, app *App) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		recs, err := app.approval.List(context.Background(), "pending")
		if err == nil && len(recs) == 1 {
			return recs[0].ID
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending records = %d, %v, want exactly 1", len(recs), err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestApprovalUseStoreDown pins fail-closed at the use step against the real
// service on both lanes and for both classes: when the database does not
// answer the lookup or the use write of an approval, the call is denied with
// the sentence that says what failed and what to do, one fail-closed record
// is written, and the approval stays unused.
func TestApprovalUseStoreDown(t *testing.T) {
	t.Parallel()
	hold := policy.Decision{
		Effect: policy.EffectAllow, RuleID: "r-hold", SetName: "iga",
		Approve: &policy.ApproveSpec{Roles: []string{"sec-approvers"}, TimeoutSeconds: 90, RetryTTLSeconds: 60},
	}
	events := map[string]policy.Event{
		"hook":    {Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "deploy-prod"},
		"gateway": {Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user", Args: json.RawMessage(`{"user":"bob"}`)},
	}
	for _, lane := range []string{"hook", "gateway"} {
		for _, class := range []string{"hold", "ticket"} {
			for _, fault := range []string{"approval-lookup", "approval-use"} {
				t.Run(lane+"/"+class+"/"+fault, func(t *testing.T) {
					t.Parallel()
					log, buf := captureLogger()
					app, _, fs := testAppFaultLog(t, log)
					requester := seedIdentity(t, app)
					ctx := ctxWithCapture(context.Background(), log, "use-down")
					claims := authn.Claims{Session: "sess-1", Subject: requester.ID}
					ev, d := events[lane], hold
					if class == "ticket" {
						d = ticketDecision()
						seedTicketGrant(t, app, requester.ID, d.RuleID, ev)
					} else {
						seedHoldApproval(t, app, requester.ID, d.RuleID, ev)
					}
					fs.arm(fault, errors.New("db down"))
					var reason string
					if lane == "hook" {
						dec, _ := app.resolveApproveHook(ctx, claims, policy.Subject{User: "kim"}, ev, d)
						reason = dec.Reason
					} else {
						reason, _ = app.resolveApprove(ctx, app.approval, claims, policy.Subject{User: "kim"}, ev, d, "why", ev.Args)
					}
					fs.disarm()
					if reason != approvalStoreDownReason {
						t.Fatalf("reason = %q, want the database sentence", reason)
					}
					rec := assertOneFailClosedRecord(t, buf, lane, "use-down")
					if !strings.Contains(rec, "db down") {
						t.Errorf("fail-closed record lacks the cause: %s", rec)
					}
					unused, err := app.store.Approvals().List(context.Background(), "approved")
					if err != nil || len(unused) != 1 || unused[0].ConsumedAt != nil {
						t.Errorf("approved rows = %+v, %v, want the one approval still unused", unused, err)
					}
				})
			}
		}
	}
}

// seedHoldApproval inserts an approved hold for (sess-1, ruleID, the v2 key
// of ev) with a use window a minute long, as a decision leaves it.
func seedHoldApproval(t *testing.T, app *App, userID, ruleID string, ev policy.Event) {
	t.Helper()
	now := time.Now().UTC()
	deadline := now.Add(time.Minute)
	if _, err := app.store.Approvals().Insert(context.Background(), store.Approval{
		SessionID: "sess-1", UserID: userID, Username: "kim", RuleID: ruleID, ArgvHash: v2Key(t, ev),
		Lane: "hook", Class: "hold", State: "approved", DecidedBy: "u-appr", DecidedByName: "Ada Approver",
		TimeoutSeconds: 90, RetryTTLSeconds: 60, CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute),
		DecidedAt: &now, GrantExpiresAt: &deadline,
	}); err != nil {
		t.Fatalf("seed hold approval: %v", err)
	}
}

// TestHoldUseFieldsOnEverySurface pins that a used hold shows when its use
// window ends, when it was used and by which session on the admin API, the
// phone feed and the approval status tool, the fields a ticket already shows.
func TestHoldUseFieldsOnEverySurface(t *testing.T) {
	t.Parallel()
	deadline := time.Date(2026, 9, 27, 10, 1, 0, 0, time.UTC)
	used := time.Date(2026, 9, 27, 10, 0, 5, 0, time.UTC)
	rec := approval.Record{
		ID: "a-hold", Class: "hold", State: approval.StateApproved, CreatedAt: used, ExpiresAt: deadline,
		GrantExpiresAt: &deadline, ConsumedAt: &used, ConsumedBy: "sess-1",
	}
	var row approverRow
	applyApproverTicketFields(&row, rec)
	admin, _ := json.Marshal(toApprovalPayload(rec))
	phone, _ := json.Marshal(row)
	status, _ := json.Marshal(nativeStatusPayload(rec))
	surfaces := []struct {
		name string
		raw  string
		want []string
	}{
		{"admin API", string(admin), []string{`"class":"hold"`, `"grantExpiresAt":"2026-09-27T10:01:00Z"`, `"consumedAt":"2026-09-27T10:00:05Z"`, `"consumedBy":"sess-1"`}},
		{"phone feed", string(phone), []string{`"class":"hold"`, `"grant_expires_at":"2026-09-27T10:01:00Z"`, `"consumed_at":"2026-09-27T10:00:05Z"`, `"consumed_by":"sess-1"`}},
		{"approval status tool", string(status), []string{`"class":"hold"`, `"grant_expires_at":"2026-09-27T10:01:00Z"`, `"consumed_at":"2026-09-27T10:00:05Z"`, `"consumed_by":"sess-1"`}},
	}
	for _, s := range surfaces {
		for _, w := range s.want {
			if !strings.Contains(s.raw, w) {
				t.Errorf("%s lacks %s: %s", s.name, w, s.raw)
			}
		}
	}
}
