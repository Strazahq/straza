package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// faultedDraft is the draft id whose read nativeFaults fails.
const faultedDraft = 424242

// nativeFaults wraps a store so the drafts statements of the drafting
// tools can fail: every read of the draft faultedDraft, and the open count
// and the create of each proposer id in countFails and createFails.
type nativeFaults struct {
	store.Store
	mu          sync.Mutex
	countFails  map[string]bool
	createFails map[string]bool
}

func (s *nativeFaults) Drafts() store.DraftRepo { return nativeFaultsRepo{s.Store.Drafts(), s} }

// failCount makes the open count of the proposer id fail.
func (s *nativeFaults) failCount(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.countFails[id] = true
}

// failCreate makes the create of a draft by the proposer id fail.
func (s *nativeFaults) failCreate(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createFails[id] = true
}

// failing reports whether fails holds the proposer id.
func (s *nativeFaults) failing(fails map[string]bool, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fails[id]
}

type nativeFaultsRepo struct {
	store.DraftRepo
	s *nativeFaults
}

var errDraftsRead = errors.New("injected: the database refused the statement")

func (r nativeFaultsRepo) Get(ctx context.Context, id int64) (store.DraftRow, []store.DraftItemRow, error) {
	if id == faultedDraft {
		return store.DraftRow{}, nil, errDraftsRead
	}
	return r.DraftRepo.Get(ctx, id)
}

func (r nativeFaultsRepo) CountOpen(ctx context.Context, proposerID string) (int, error) {
	if r.s.failing(r.s.countFails, proposerID) {
		return 0, errDraftsRead
	}
	return r.DraftRepo.CountOpen(ctx, proposerID)
}

func (r nativeFaultsRepo) Create(ctx context.Context, d store.DraftRow, items []store.DraftItemRow, rev store.DraftRevisionRow) (store.DraftRow, error) {
	if r.s.failing(r.s.createFails, rev.Author.ID) {
		return store.DraftRow{}, errDraftsRead
	}
	return r.DraftRepo.Create(ctx, d, items, rev)
}

// TestNativeToolRecordMatchesItsAnswer pins the one straza.audit.mcp record
// of a call of a built-in straza tool against what the call answered. Each
// refusal before the record is a deny with the allowing rule kept, whose
// reason is the sentence the agent read, or that sentence without the ref,
// the draft id or the document text it quotes. A store write
// refused after the record keeps the allow, and an answered call is an
// allow, each with the session and the user of the caller and the tool.
func TestNativeToolRecordMatchesItsAnswer(t *testing.T) {
	t.Parallel()
	faults := &nativeFaults{countFails: map[string]bool{}, createFails: map[string]bool{}}
	r := &doorRig{newDraftsFixture(t, []func(*App){func(a *App) { faults.Store, a.store = a.store, faults }})}
	ctx := context.Background()
	if _, err := r.app.store.Policies().Create(ctx, store.PolicySet{Name: "door-approvals", Status: "active", YAMLSource: "apiVersion: straza.dev/v1beta1\nkind: PolicySet\n" +
		"metadata: {name: door-approvals}\nspec:\n  match: {roles: [" + DraftConfigRole + "]}\n  rules:\n    - id: approvals\n      tools: [mcp.call]\n      apps: [straza]\n" +
		"      toolNames: {allow: [approval_status, approval_await]}\n      effect: allow\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}
	ticket := func(t *testing.T, owner store.User) string {
		t.Helper()
		rec, err := r.app.store.Approvals().Insert(ctx, store.Approval{SessionID: "s-" + owner.Username, UserID: owner.ID, RuleID: "deploy-ticket",
			Class: "ticket", State: "approved", DecidedBy: r.kim.ID, DecidedByName: "kim",
			CreatedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(24 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return rec.ID
	}
	stranger, _ := r.agent(t, "stranger")
	strangerTicket := ticket(t, stranger)
	strangerDraft := strconv.FormatInt(r.storeDraft(t, "straza-app", r.agentActor(stranger), time.Now().Add(time.Hour), draftApp("weather", weatherURL, "The weather.")).ID, 10)
	doc := draftApp("weather", weatherURL, "The weather.")
	faulted := strconv.Itoa(faultedDraft)
	longRef := strings.Repeat("r", auditArgsMax+10)
	// The record of a ref or a draft id that names nothing keeps the
	// sentence without it, word for word.
	const noSuchApproval, noSuchDraft = "Straza: no such approval.", "Straza: no such draft."

	cases := []struct {
		name string
		tool string
		// args answers the arguments of the call made as the caller u.
		args func(t *testing.T, u store.User) map[string]any
		// refusal answers the sentence the call must refuse with, or ""
		// for an answered call. A sentence ending in a space is a prefix.
		refusal func(args map[string]any) string
		rule    string
		// record is the reason the deny record keeps when it is not the
		// sentence the agent read, and after marks a refusal that comes
		// after the record, which then keeps its allow.
		record string
		after  bool
	}{
		{"approval_status without a ref", "approval_status",
			func(*testing.T, store.User) map[string]any { return map[string]any{"ref": ""} },
			func(map[string]any) string { return "Straza: approval_status requires a ref" }, "approvals", "", false},
		{"approval_status of another user's ticket", "approval_status",
			func(*testing.T, store.User) map[string]any { return map[string]any{"ref": strangerTicket} },
			func(map[string]any) string { return "Straza: no such approval " + strangerTicket }, "approvals", noSuchApproval, false},
		{"approval_status of an unknown ref", "approval_status",
			func(*testing.T, store.User) map[string]any { return map[string]any{"ref": "nope"} },
			func(map[string]any) string { return "Straza: no such approval nope" }, "approvals", noSuchApproval, false},
		{"approval_status of a ref longer than the arguments cap", "approval_status",
			func(*testing.T, store.User) map[string]any { return map[string]any{"ref": longRef} },
			func(map[string]any) string { return "Straza: no such approval " + longRef }, "approvals", noSuchApproval, false},
		{"approval_status of the caller's ticket", "approval_status",
			func(t *testing.T, u store.User) map[string]any { return map[string]any{"ref": ticket(t, u)} },
			func(map[string]any) string { return "" }, "approvals", "", false},
		{"approval_await without a ref", "approval_await",
			func(*testing.T, store.User) map[string]any { return map[string]any{"max_wait_seconds": 1} },
			func(map[string]any) string { return "Straza: approval_await requires a ref" }, "approvals", "", false},
		{"approval_await of another user's ticket", "approval_await",
			func(*testing.T, store.User) map[string]any {
				return map[string]any{"ref": strangerTicket, "max_wait_seconds": 1}
			},
			func(map[string]any) string { return "Straza: no such approval " + strangerTicket }, "approvals", noSuchApproval, false},
		{"approval_await of the caller's decided ticket", "approval_await",
			func(t *testing.T, u store.User) map[string]any {
				return map[string]any{"ref": ticket(t, u), "max_wait_seconds": 1}
			},
			func(map[string]any) string { return "" }, "approvals", "", false},
		{"draft_status without a draft", "draft_status",
			func(*testing.T, store.User) map[string]any { return map[string]any{"draft": ""} },
			func(map[string]any) string { return wantStatusShape }, "", "", false},
		{"draft_status of an unknown draft", "draft_status",
			func(*testing.T, store.User) map[string]any { return map[string]any{"draft": "999999"} },
			func(map[string]any) string { return fmt.Sprintf(wantNoSuchDraft, "999999") }, "", noSuchDraft, false},
		{"draft_status of another agent's draft", "draft_status",
			func(*testing.T, store.User) map[string]any { return map[string]any{"draft": strangerDraft} },
			func(map[string]any) string { return fmt.Sprintf(wantNoSuchDraft, strangerDraft) }, "", noSuchDraft, false},
		{"draft_status whose read the database refused", "draft_status",
			func(*testing.T, store.User) map[string]any { return map[string]any{"draft": faulted} },
			func(map[string]any) string { return statusReadRefusal }, "", "", false},
		{"draft_status of the caller's draft", "draft_status",
			func(t *testing.T, u store.User) map[string]any {
				row := r.storeDraft(t, "straza-app", r.agentActor(u), time.Now().Add(time.Hour), doc)
				return map[string]any{"draft": strconv.FormatInt(row.ID, 10)}
			},
			func(map[string]any) string { return "" }, "", "", false},
		{"draft_submit with arguments of another shape", "draft_submit",
			func(*testing.T, store.User) map[string]any { return map[string]any{"documents": "not a list"} },
			func(map[string]any) string { return wantSubmitShape }, "", "", false},
		{"draft_submit of a draft that is not the caller's", "draft_submit",
			func(*testing.T, store.User) map[string]any {
				return map[string]any{"documents": []string{doc}, "draft": strangerDraft}
			},
			func(map[string]any) string { return fmt.Sprintf(wantNoOpenDraft, strangerDraft) }, "", "", false},
		{"draft_submit whose draft read the database refused", "draft_submit",
			func(*testing.T, store.User) map[string]any {
				return map[string]any{"documents": []string{doc}, "draft": faulted}
			},
			func(map[string]any) string { return submitStoreRefusal }, "", "", false},
		{"draft_submit of a secret", "draft_submit",
			func(*testing.T, store.User) map[string]any {
				return map[string]any{"documents": []string{draftApp("leaky", "https://svc:hunter2hunter2hunter2@leaky.example/mcp", "Leaky.")}}
			},
			func(map[string]any) string { return wantNotSaved + "App/leaky: " }, "", wantNotSaved + "Refused: secret.userinfo, secret.userinfo.", false},
		{"draft_submit past the open draft limit", "draft_submit",
			func(t *testing.T, u store.User) map[string]any {
				for range 10 {
					r.storeDraft(t, "straza-app", r.agentActor(u), time.Now().Add(time.Hour), doc)
				}
				return map[string]any{"documents": []string{doc}}
			},
			func(map[string]any) string { return wantNotSaved }, "", wantNotSaved + "Refused: draft.open-limit.", false},
		{"draft_submit whose open count the database refused", "draft_submit",
			func(_ *testing.T, u store.User) map[string]any {
				faults.failCount(u.ID)
				return map[string]any{"documents": []string{doc}}
			},
			func(map[string]any) string { return submitStoreRefusal }, "", "", false},
		{"draft_submit of a draft id that is no number", "draft_submit",
			func(*testing.T, store.User) map[string]any {
				return map[string]any{"documents": []string{doc}, "draft": "not-a-number"}
			},
			func(map[string]any) string { return fmt.Sprintf(wantNoOpenDraft, "not-a-number") }, "", draftNotIDRecord, false},
		{"draft_submit whose store write fails after the record", "draft_submit",
			func(_ *testing.T, u store.User) map[string]any {
				faults.failCreate(u.ID)
				return map[string]any{"documents": []string{doc}}
			},
			func(map[string]any) string { return submitStoreRefusal }, "", "", true},
		{"draft_submit that stores a draft", "draft_submit",
			func(*testing.T, store.User) map[string]any { return map[string]any{"documents": []string{doc}} },
			func(map[string]any) string { return "" }, "", "", false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, tok := r.agent(t, fmt.Sprintf("native%d", i))
			claims, err := r.app.tokens.Verify(tok)
			if err != nil {
				t.Fatal(err)
			}
			args := tc.args(t, u)
			text, isErr, _ := callTool(t, r.base, tok, "straza__"+tc.tool, args)
			want := tc.refusal(args)
			switch {
			case want == "" && isErr:
				t.Fatalf("the call was refused: %q", text)
			case want != "" && !isErr:
				t.Fatalf("the call answered %q, want the refusal %q", text, want)
			case strings.HasSuffix(want, " ") && !strings.HasPrefix(text, want):
				t.Fatalf("the refusal = %q, want it to start %q", text, want)
			case want != "" && !strings.HasSuffix(want, " ") && text != want:
				t.Fatalf("the refusal = %q, want %q", text, want)
			}
			rec := awaitMCPRecords(t, r.app, 1, func(d map[string]any) bool { return d["user"] == u.ID && d["toolName"] == tc.tool })[0]
			wantRec := map[string]any{"session": claims.Session, "user": u.ID, "app": nativeAppName, "toolName": tc.tool,
				"effect": "allow", "ruleId": tc.rule}
			switch {
			case want != "" && tc.after:
				wantRec["reason"] = "allowed by role access. No policy rule gates this tool."
			case tc.record != "":
				wantRec["effect"], wantRec["reason"] = "deny", tc.record
			case want != "":
				wantRec["effect"], wantRec["reason"] = "deny", text
			}
			for k, v := range wantRec {
				if rec[k] != v {
					t.Errorf("the record's %s = %v, want %v", k, rec[k], v)
				}
			}
		})
	}
}

// awaitPolicy lets the role dev call straza__approval_await.
const awaitPolicy = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: await-tool}
spec:
  match: {roles: [dev]}
  rules:
    - id: await
      tools: [mcp.call]
      apps: [straza]
      toolNames: {allow: [approval_await]}
      effect: allow
`

// pendingTicket stores a pending ticket of owner that a holder of the role
// change decides, and answers its id.
func pendingTicket(t *testing.T, app *App, owner store.User) string {
	t.Helper()
	rec, err := app.store.Approvals().Insert(context.Background(), store.Approval{SessionID: "s-" + owner.Username, UserID: owner.ID,
		RuleID: "deploy-ticket", Class: "ticket", State: "pending", Lane: "gateway", ApproverRoles: []string{"change"},
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return rec.ID
}

// TestApprovalAwaitRecordPrecedesTheWait pins the order of approval_await
// through the gateway. The call's one record reaches the chain while the
// call still waits on its pending ticket, a decision made during the wait
// comes after that record on the chain, and the call answers the decision.
func TestApprovalAwaitRecordPrecedesTheWait(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedGatewayUser(t, app, "kim", "dev")
	ada := seedApprover(t, app, "ada", "change")
	catActivate(t, app, "await-tool", awaitPolicy)
	tok, sid := gatewaySession(t, base, "kim")
	ref := pendingTicket(t, app, kim)

	// chainIndex answers the position on the chain of the first record that
	// sel picks, -1 when none.
	chainIndex := func(sel func(typ string, d map[string]any) bool) int {
		recs, err := app.store.Audit().List(ctx, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		for i, r := range recs {
			var ce struct {
				Type string         `json:"type"`
				Data map[string]any `json:"data"`
			}
			if json.Unmarshal([]byte(r.CE), &ce) == nil && sel(ce.Type, ce.Data) {
				return i
			}
		}
		return -1
	}
	isAwait := func(typ string, d map[string]any) bool {
		return typ == "straza.audit.mcp" && d["toolName"] == nativeToolApprovalAwait && d["user"] == kim.ID
	}
	isDecision := func(typ string, d map[string]any) bool {
		return typ == "straza.audit.approval" && d["phase"] == "resolution" && d["approvalId"] == ref
	}

	type answer struct {
		text  string
		isErr bool
		sc    map[string]any
	}
	done := make(chan answer, 1)
	go func() {
		text, isErr, sc := callTool(t, base, tok, "straza__approval_await", map[string]any{"ref": ref, "max_wait_seconds": 60})
		done <- answer{text, isErr, sc}
	}()
	// The wait lasts up to 60 seconds and nobody decides before the record
	// shows, so a record written after the wait never shows in time.
	for deadline := time.Now().Add(20 * time.Second); chainIndex(isAwait) < 0; time.Sleep(50 * time.Millisecond) {
		select {
		case a := <-done:
			t.Fatalf("the await answered %q before its record reached the chain", a.text)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the await's record did not reach the chain while the call waited")
		}
	}
	if _, err := app.approval.Decide(ctx, ref, "approved", ada.ID, "console", "", ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	select {
	case a := <-done:
		if a.isErr || a.sc["state"] != "approved" || a.sc["timed_out"] != false {
			t.Errorf("the await answered %q %v, want the approval with timed_out false", a.text, a.sc)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the await never answered after the decision")
	}
	waitFor(t, "the decision record to reach the chain", func() bool { return chainIndex(isDecision) >= 0 })
	if at, decided := chainIndex(isAwait), chainIndex(isDecision); at > decided {
		t.Errorf("the await's record sits at %d on the chain, after the decision at %d", at, decided)
	}
	rec := awaitMCPRecords(t, app, 1, func(d map[string]any) bool { return d["toolName"] == nativeToolApprovalAwait })[0]
	for k, v := range map[string]any{"session": sid, "user": kim.ID, "effect": "allow", "ruleId": "await"} {
		if rec[k] != v {
			t.Errorf("the record's %s = %v, want %v", k, rec[k], v)
		}
	}
}

// TestApprovalAwaitUnderFullAuditQueue pins that approval_await under
// block answers auditQueueFullMsg when its record finds no room, at once
// and before any wait on its pending ticket.
func TestApprovalAwaitUnderFullAuditQueue(t *testing.T) {
	t.Parallel()
	var token string
	var kim store.User
	app, _, _, _ := heldAuditApp(t, true, 300*time.Millisecond, func(app *App, base string) {
		kim = seedGatewayUser(t, app, "kim", "dev")
		catActivate(t, app, "await-tool", awaitPolicy)
		token, _ = gatewaySession(t, base, "kim")
	})
	ref := pendingTicket(t, app, kim)
	start := time.Now()
	text, isErr, _ := callTool(t, "http://"+app.Addr(), token, "straza__approval_await", map[string]any{"ref": ref, "max_wait_seconds": 30})
	if !isErr || text != auditQueueFullMsg {
		t.Fatalf("approval_await = %q (error %v), want auditQueueFullMsg", text, isErr)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the refusal came after %s, want it before the 30 second wait", d)
	}
}

// TestDraftSubmitRecordQuotesNoDocument pins that the record of a refused
// straza__draft_submit carries no word the agent sent. A token in a
// document's kind, in a PolicySet's name or in the draft to revise reaches
// the sentence the agent reads, which quotes it before any secret scan,
// and never the chained record. A token in an App's address, which the
// secret scan refuses, stays out of both.
func TestDraftSubmitRecordQuotesNoDocument(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t)
	token := "ghp_" + strings.Repeat("Q7x", 12)
	docs := func(doc string) map[string]any { return map[string]any{"documents": []string{doc}} }
	cases := []struct {
		name string
		args map[string]any
		// quoted marks a refusal whose sentence quotes the token to the
		// agent, and record is the reason the record keeps, "" for any
		// form of the intake refusal.
		quoted bool
		record string
	}{
		{"a token as the kind", docs("apiVersion: straza.dev/v1beta1\nkind: " + token + "\nmetadata:\n  name: weather\n"),
			true, wantNotSaved + "Refused: bundle.kind in document 1."},
		{"a token as a PolicySet's name", docs("apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata:\n  name: " + token +
			"\nspec:\n  rules:\n    - id: a\n      tools: [mcp.call]\n      effect: allow\n"), true, ""},
		{"a token in an App's address", docs(draftApp("leaky", "https://svc:"+token+"@leaky.example/mcp", "Leaky.")), false, ""},
		{"a token as the draft to revise", map[string]any{"documents": []string{draftApp("weather", weatherURL, "The weather.")}, "draft": token},
			true, draftNotIDRecord},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, tok := r.agent(t, fmt.Sprintf("leak%d", i))
			text, isErr, _ := callTool(t, r.base, tok, "straza__draft_submit", tc.args)
			if !isErr || strings.Contains(text, token) != tc.quoted {
				t.Fatalf("the agent read %q (error %v), want a refusal that quotes the token: %v", text, isErr, tc.quoted)
			}
			sel := func(d map[string]any) bool { return d["user"] == u.ID && d["toolName"] == nativeToolDraftSubmit }
			rec := awaitMCPRecords(t, r.app, 1, sel)[0]
			if raw := rawMCPRecord(t, r.app, sel); strings.Contains(raw, token) {
				t.Errorf("the chained record carries the token: %s", raw)
			}
			reason, _ := rec["reason"].(string)
			if rec["effect"] != "deny" || (tc.record != "" && reason != tc.record) || (tc.record == "" && !strings.HasPrefix(reason, wantNotSaved+"Refused: ")) {
				t.Errorf("the record = %v %q, want a deny with the reason %q", rec["effect"], reason, tc.record)
			}
		})
	}
}

// TestCallFailedMsg pins the answer on /mcp to a call that did not reach a
// result: a call that may have run, because the server ended its session
// while it ran, it ran out of time or its request ended, says so and to
// check before trying once more, with no retry advice, and any other
// failure names the cause and the bounded retry.
func TestCallFailedMsg(t *testing.T) {
	t.Parallel()
	alive := context.Background()
	expired, cancelExpired := context.WithDeadline(alive, time.Now().Add(-time.Second))
	defer cancelExpired()
	left, cancelLeft := context.WithCancel(alive)
	cancelLeft()
	const check = ", so the call may have taken effect, and Straza does not send it again. Check whether it took effect before you try it once more."
	rows := []struct {
		name string
		ctx  context.Context
		err  error
		want string
	}{
		{"the server ended its session while the call ran", alive,
			fmt.Errorf("the MCP server pg ended its session while the call ran, so %w, and Straza does not send it again. Check whether it took effect before you try it once more", manager.ErrMayHaveRun),
			"Straza: the MCP server pg ended its session while the call ran, so the call may have taken effect, and Straza does not send it again. Check whether it took effect before you try it once more."},
		{"the call ran out of time", expired, context.DeadlineExceeded, "Straza: the MCP server pg did not answer the call within 30s" + check},
		{"the request ended", left, context.Canceled, "Straza: the MCP server pg did not answer the call before the request ended" + check},
		{"the server is down", alive, errors.New("the connection to MCP server pg failed: dial tcp: connection refused. An administrator checks that the server runs."),
			"Straza: the call to the MCP server pg failed: the connection to MCP server pg failed: dial tcp: connection refused. An administrator checks that the server runs. " +
				"Try the call once more in a minute. If it fails again, tell the user."},
	}
	refusal := func(status int) error {
		return fmt.Errorf("calling: %w", &manager.RefusedError{Server: "pg", Status: status})
	}
	rows = append(rows, []struct {
		name string
		ctx  context.Context
		err  error
		want string
	}{
		{"the server refuses the credential", alive, refusal(http.StatusUnauthorized),
			"Straza: the MCP server pg refused the credential Straza sent with the call (HTTP 401 Unauthorized), so the call did not run. " +
				"Tell the user, so that an administrator sets a working credential with strazactl apps secret set pg."},
		{"the server refuses the caller", alive, refusal(http.StatusForbidden),
			"Straza: the MCP server pg refused the credential Straza sent with the call (HTTP 403 Forbidden), so the call did not run. " +
				"Tell the user, so that an administrator sets a working credential with strazactl apps secret set pg."},
		{"the server refuses the request", alive, refusal(http.StatusBadRequest),
			"Straza: the MCP server pg answered the call with HTTP 400 Bad Request, so the call did not run. The same call gets the same answer, so tell the user."},
		{"the server is busy", alive, refusal(http.StatusTooManyRequests),
			"Straza: the call to the MCP server pg failed: the MCP server pg answered the call with HTTP 429 Too Many Requests, so the call did not run. " +
				"Try the call once more in a minute. If it fails again, tell the user."},
	}...)
	fix := credentialFix("pg", "", policy.Subject{})
	for _, row := range rows {
		if got := callFailedMsg(row.ctx, "pg", 30*time.Second, fix, row.err); got != row.want {
			t.Errorf("%s:\n got %q\nwant %q", row.name, got, row.want)
		}
	}
}

// TestCredentialFix pins who renews a credential an MCP server refused, by
// where the call's credential came from and, for the caller's own, by
// whether the caller is a person.
func TestCredentialFix(t *testing.T) {
	t.Parallel()
	person := policy.Subject{User: "kim"}
	agent := policy.Subject{User: "bot", UserType: store.UserTypeAgent, Sponsor: "kim"}
	service := policy.Subject{User: "svc", UserType: store.UserTypeService}
	rows := []struct {
		name, source string
		sub          policy.Subject
		want         string
	}{
		{"a person's own", manager.SourceOwn, person, "the user connects to pg again with straza connect pg"},
		{"a person's own, typed human", manager.SourceOwn, policy.Subject{User: "kim", UserType: store.UserTypeHuman},
			"the user connects to pg again with straza connect pg"},
		{"an agent's own", manager.SourceOwn, agent, "an administrator sets the token of bot again with strazactl connect pg --user bot"},
		{"a service account's own", manager.SourceOwn, service, "an administrator sets the token of svc again with strazactl connect pg --user svc"},
		{"the sponsor's", manager.SourceSponsor, agent, "the sponsor kim connects to pg again on their credentials page"},
		{"the agent's client", manager.SourceClientCredentials, agent, "an administrator checks the client of this agent at the provider of pg"},
		{"a shared one", manager.SourceShared, person, "an administrator sets a working credential with strazactl apps secret set pg"},
		{"no source", "", person, "an administrator sets a working credential with strazactl apps secret set pg"},
	}
	for _, row := range rows {
		if got := credentialFix("pg", row.source, row.sub); got != row.want {
			t.Errorf("%s: credentialFix = %q, want %q", row.name, got, row.want)
		}
	}
}
