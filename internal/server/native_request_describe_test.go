package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// describePolicy lets every session call approval_request, gates pr_create
// on ghapp behind a ticket that binds the exact call, gates repo_create
// behind a ticket that names no approver, so the requester's sponsor
// decides it, and denies db_query, which the overlay then hides. Every
// other tool is left to its access row.
const describePolicy = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: describe}
spec:
  rules:
    - id: ask
      tools: [mcp.call]
      apps: [straza]
      toolNames: {allow: [approval_request]}
      effect: allow
    - id: pr-ticket
      tools: [mcp.call]
      apps: [ghapp]
      toolNames: {allow: ["pr_create"]}
      effect: allow
      mode: approve
      approve: {class: ticket, roles: [sec-approvers], ticketTTLSeconds: 86400, grantTTLSeconds: 3600}
    - id: create-ticket
      tools: [mcp.call]
      apps: [ghapp]
      toolNames: {allow: ["repo_create"]}
      effect: allow
      mode: approve
      approve: {class: ticket}
    - id: no-db
      tools: [mcp.call]
      apps: [ghapp]
      toolNames: {deny: ["db_query"]}
      effect: deny
      reason: "Straza: db_query is closed to agents"
`

// The sentences approval_request answers for an mcp.call action that does
// not name its server and tool, and for one that also carries a command or
// argv, pinned word for word.
const (
	wantMCPNotConcrete = "Straza: approval_request needs action.app and action.tool_name for an mcp.call: the server and the tool of the later real call. Add both, then request again."
	wantMCPNoCommand   = "Straza: an mcp.call action takes no action.command or action.argv, because the later real call is an MCP tool call and carries neither. Remove them, then request again."
)

// unknownTool is the sentence approval_request answers for a described
// tool the session cannot call.
func unknownTool(app, tool string) string {
	return "Straza: " + app + ":" + tool + " is not a tool this session can call, so no approval can be requested for it. Check the app and tool name, or ask an admin for access to the tool."
}

// unroutableTicket is the sentence approval_request answers for a call of
// the rule create-ticket whose sponsor half failed with cause.
func unroutableTicket(cause string) string {
	return "Straza: approval cannot be routed: " + cause + `, and rule "create-ticket" names no approver roles. Fix: set the requester's sponsor to a person in your identity manager, or add approve.roles / approve.deciders to the rule. Denied (fail-closed)`
}

// approvalRequests counts the straza.audit.approval request events of the
// user userID in the outbox, one per new approval request.
func approvalRequests(t *testing.T, app *App, userID string) int {
	t.Helper()
	rows, err := app.store.Outbox().ListRecent(context.Background(), 5000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, row := range rows {
		if row.Subject != "straza.audit.approval" {
			continue
		}
		var ce struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(row.CE), &ce); err != nil {
			t.Fatal(err)
		}
		if ce.Data["phase"] == "request" && ce.Data["user"] == userID {
			n++
		}
	}
	return n
}

// pendingTickets counts the pending approvals of every user.
func pendingTickets(t *testing.T, app *App) int {
	t.Helper()
	pending, err := app.approval.List(context.Background(), string(approval.StatePending))
	if err != nil {
		t.Fatal(err)
	}
	return len(pending)
}

// TestApprovalRequestDecidesAsTheRealCall drives approval_request through
// /mcp with policyFilter on and pins that it decides the MCP call it
// describes as the gateway decides the real call: visibility, the overlay,
// the rate limit, the object gate, the engine with the granted fact and
// deny-final. Every case leaves exactly one approval_request record, whose
// decision is the refusal when the call is refused, and a new ticket alone
// leaves an approval request event and a pending ticket. A ticket nobody
// could decide is refused before its record and counted once as unroutable.
func TestApprovalRequestDecidesAsTheRealCall(t *testing.T) {
	t.Parallel()
	r := newGHRig(t, func(c *config.Config) { c.Apps.Catalog.PolicyFilter = true })
	ctx := context.Background()
	if _, err := r.app.store.Policies().Create(ctx, store.PolicySet{Name: "describe", Status: "active", YAMLSource: describePolicy}); err != nil {
		t.Fatal(err)
	}
	catRecompile(t, r.app)
	approver := seedApprover(t, r.app, "secops", "sec-approvers")

	// rae holds rate-tools, which binds rateapp, whose manifest allows one
	// call a second. nob holds a role that binds nothing.
	rateRole, err := r.app.store.Roles().Create(ctx, store.Role{Name: "rate-tools", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	mf, err := managerParse(t, fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: rateapp}
server: {name: straza.test/rateapp, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
  limits: {rps: 1}
`, startGatewayUpstream(t).URL))
	if err != nil {
		t.Fatal(err)
	}
	row, err := r.app.manager.Install(ctx, mf, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: rateRole.ID, AppID: row.ID, ToolMatcher: `["*"]`}); err != nil {
		t.Fatal(err)
	}
	if err := r.app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	catWaitRunning(t, r.app, "rateapp")
	for user, role := range map[string]string{"rae": "rate-tools", "nob": "nobinds"} {
		seedGatewayUser(t, r.app, user, role)
		r.tokens[user] = sessionToken(t, r.base, user)
	}
	// The agents ivy, orphan and lapsed hold gh-every, and nobody can
	// decide a ticket of theirs: ivy has no sponsor, orphan's sponsor is
	// not known to Straza, and lapsed's sponsor is disabled.
	if _, err := r.app.store.Users().Create(ctx, store.User{Username: "gone", Status: store.UserDisabled}); err != nil {
		t.Fatal(err)
	}
	for agent, sponsor := range map[string]string{"ivy": "", "orphan": "ghost", "lapsed": "gone"} {
		seedAgent(t, r.app, agent, sponsor, "gh-every")
		r.tokens[agent] = agentSessionToken(t, r.app, r.base, agent)
	}

	var happyRef, deniedRef string
	rows := []struct {
		name           string
		user           string
		action         map[string]any
		before         func(t *testing.T)
		wantText       func() string // the refusal, which the record's reason repeats
		open           bool          // the answer is a ticket, and the record keeps the rule's empty reason
		sameAs         *string       // the ticket must be this ref
		saveRef        *string       // keep the ticket's ref
		wantEffect     string
		wantEvents     int // approval request events, and new pending tickets
		wantUnroutable int // the rise of straza_approvals_unroutable_total
	}{
		{name: "an unbound tool", user: "nob",
			action:   map[string]any{"tool": "mcp.call", "app": "ghapp", "tool_name": "pr_create", "args": map[string]any{"repo": "Strazahq/site"}},
			wantText: func() string { return unknownTool("ghapp", "pr_create") }, wantEffect: "deny"},
		{name: "a missing app", user: "pam",
			action:   map[string]any{"tool": "mcp.call", "app": "ghost", "tool_name": "echo", "args": map[string]any{}},
			wantText: func() string { return unknownTool("ghost", "echo") }, wantEffect: "deny"},
		{name: "a tool the overlay hides reads as an unbound one", user: "pam",
			action:   map[string]any{"tool": "mcp.call", "app": "ghapp", "tool_name": "db_query", "args": map[string]any{}},
			wantText: func() string { return unknownTool("ghapp", "db_query") }, wantEffect: "deny"},
		{name: "the object gate refuses a repo the roles do not cover", user: "pam",
			action:     map[string]any{"tool": "mcp.call", "app": "ghapp", "tool_name": "pr_create", "args": map[string]any{"repo": "Strazahq/site"}},
			wantText:   func() string { return gwG1("Strazahq/site", "Strazahq/pay-api and Strazahq/pay-web") },
			wantEffect: "deny"},
		{name: "a call that names no repo is asked for its args", user: "pam",
			action: map[string]any{"tool": "mcp.call", "app": "ghapp", "tool_name": "pr_create"},
			wantText: func() string {
				return "Straza: pr_create on ghapp works on one repo per call, and an approval covers one repo, so approval_request needs action.args with the repo in the argument repo. Add action.args with the exact arguments of the later call, then request again"
			},
			wantEffect: "deny"},
		{name: "a bound tool no rule names needs no approval", user: "pam",
			action: map[string]any{"tool": "mcp.call", "app": "ghapp", "tool_name": "whoami", "args": map[string]any{}},
			wantText: func() string {
				return "Straza: no approval needed. The described call is already allowed by policy; just make the call"
			},
			wantEffect: "deny"},
		{name: "the happy path opens a ticket", user: "eve",
			action: map[string]any{"tool": "mcp.call", "app": "ghapp", "tool_name": "pr_create", "args": map[string]any{"repo": "Strazahq/site"}},
			open:   true, saveRef: &happyRef, wantEffect: "allow", wantEvents: 1},
		{name: "the same call joins the pending ticket", user: "eve",
			action: map[string]any{"tool": "mcp.call", "app": "ghapp", "tool_name": "pr_create", "args": map[string]any{"repo": "Strazahq/site"}},
			open:   true, sameAs: &happyRef, wantEffect: "allow", wantEvents: 0},
		{name: "a call to deny opens a ticket", user: "pam",
			action: map[string]any{"tool": "mcp.call", "app": "ghapp", "tool_name": "pr_create", "args": map[string]any{"repo": "Strazahq/pay-api"}},
			open:   true, saveRef: &deniedRef, wantEffect: "allow", wantEvents: 1},
		{name: "a denied call stays denied and opens no ticket", user: "pam",
			action: map[string]any{"tool": "mcp.call", "app": "ghapp", "tool_name": "pr_create", "args": map[string]any{"repo": "Strazahq/pay-api"}},
			before: func(t *testing.T) {
				if _, err := r.app.approval.Decide(ctx, deniedRef, "denied", approver.ID, "console", "", ""); err != nil {
					t.Fatal(err)
				}
			},
			wantText: func() string {
				rec, err := r.app.approval.Get(ctx, deniedRef)
				if err != nil {
					t.Fatal(err)
				}
				return ticketDeniedFinalReason(rec)
			},
			wantEffect: "deny"},
		{name: "the denied call with a command added is refused and opens no ticket", user: "pam",
			action:     map[string]any{"tool": "mcp.call", "app": "ghapp", "tool_name": "pr_create", "args": map[string]any{"repo": "Strazahq/pay-api"}, "command": "zzz"},
			wantText:   func() string { return wantMCPNoCommand },
			wantEffect: "deny"},
		{name: "the denied call with argv added is refused and opens no ticket", user: "pam",
			action:     map[string]any{"tool": "mcp.call", "app": "ghapp", "tool_name": "pr_create", "args": map[string]any{"repo": "Strazahq/pay-api"}, "argv": []string{"zzz"}},
			wantText:   func() string { return wantMCPNoCommand },
			wantEffect: "deny"},
		{name: "the denied call with an empty argv added is refused and opens no ticket", user: "pam",
			action:     map[string]any{"tool": "mcp.call", "app": "ghapp", "tool_name": "pr_create", "args": map[string]any{"repo": "Strazahq/pay-api"}, "argv": []string{}},
			wantText:   func() string { return wantMCPNoCommand },
			wantEffect: "deny"},
		{name: "the denied call with a justification in its arguments stays denied", user: "pam",
			action: map[string]any{"tool": "mcp.call", "app": "ghapp", "tool_name": "pr_create",
				"args": map[string]any{"repo": "Strazahq/pay-api", "_straza_justification": "please"}},
			wantText: func() string {
				rec, err := r.app.approval.Get(ctx, deniedRef)
				if err != nil {
					t.Fatal(err)
				}
				return ticketDeniedFinalReason(rec)
			},
			wantEffect: "deny"},
		{name: "an mcp.call that names no tool is asked for the server and the tool", user: "pam",
			action:     map[string]any{"tool": "mcp.call", "app": "ghapp"},
			wantText:   func() string { return wantMCPNotConcrete },
			wantEffect: "deny"},
		{name: "a different call still opens a ticket", user: "pam",
			action: map[string]any{"tool": "mcp.call", "app": "ghapp", "tool_name": "pr_create", "args": map[string]any{"repo": "Strazahq/pay-web"}},
			open:   true, wantEffect: "allow", wantEvents: 1},
		{name: "a ticket of an agent with no sponsor is refused", user: "ivy",
			action:     map[string]any{"tool": "mcp.call", "app": "ghapp", "tool_name": "repo_create", "args": map[string]any{"n": "ivy"}},
			wantText:   func() string { return unroutableTicket(`agent "ivy" has no sponsor`) },
			wantEffect: "deny", wantUnroutable: 1},
		{name: "a ticket of an agent whose sponsor is not known is refused", user: "orphan",
			action: map[string]any{"tool": "mcp.call", "app": "ghapp", "tool_name": "repo_create", "args": map[string]any{"n": "orphan"}},
			wantText: func() string {
				return unroutableTicket(`sponsor "ghost" is not known to Straza (identity manager sync missing or stale)`)
			},
			wantEffect: "deny", wantUnroutable: 1},
		{name: "a ticket of an agent whose sponsor is disabled is refused", user: "lapsed",
			action:     map[string]any{"tool": "mcp.call", "app": "ghapp", "tool_name": "repo_create", "args": map[string]any{"n": "lapsed"}},
			wantText:   func() string { return unroutableTicket(`sponsor "gone" is not an active user`) },
			wantEffect: "deny", wantUnroutable: 1},
		{name: "a second request within the second is throttled", user: "rae",
			action: map[string]any{"tool": "mcp.call", "app": "rateapp", "tool_name": "echo", "args": map[string]any{}},
			before: func(t *testing.T) {
				// The first request spends the one call a second of
				// rae's session on rateapp, as a real call would.
				callTool(t, r.base, r.tokens["rae"], "straza__approval_request", map[string]any{
					"action": map[string]any{"tool": "mcp.call", "app": "rateapp", "tool_name": "echo", "args": map[string]any{}}})
			},
			wantText: func() string {
				return `Straza: rate limit exceeded for the MCP server "rateapp" (1 rps). Wait a few seconds, then try the call once more. If it is denied again, tell the user.`
			},
			wantEffect: "deny"},
		{name: "a tool on a stopped server reads as an unbound one", user: "rae",
			action: map[string]any{"tool": "mcp.call", "app": "rateapp", "tool_name": "echo", "args": map[string]any{}},
			before: func(t *testing.T) {
				if _, err := r.app.manager.Disable(ctx, "rateapp"); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(10 * time.Second)
				for _, ok := r.app.catalogFor([]string{"rate-tools"}).targets["rateapp__echo"]; ok; _, ok = r.app.catalogFor([]string{"rate-tools"}).targets["rateapp__echo"] {
					if time.Now().After(deadline) {
						t.Fatal("rateapp stayed in the catalog after it stopped")
					}
					time.Sleep(50 * time.Millisecond)
				}
			},
			wantText: func() string { return unknownTool("rateapp", "echo") }, wantEffect: "deny"},
	}
	// The records are read once every call has answered, so the chain's
	// flush is waited for once and not once per row.
	records := make([]map[string]any, len(rows))
	for i, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			if tc.before != nil {
				tc.before(t)
			}
			claims, err := r.app.tokens.Verify(r.tokens[tc.user])
			if err != nil {
				t.Fatal(err)
			}
			nonce := fmt.Sprintf("describe-case-%d", i)
			events := approvalRequests(t, r.app, claims.Subject)
			pending := pendingTickets(t, r.app)
			unroutable := counterValue(t, r.app, "straza_approvals_unroutable_total")
			text, isErr, sc := callTool(t, r.base, r.tokens[tc.user], "straza__approval_request", map[string]any{"action": tc.action, "reason": nonce})
			ref, _ := sc["ref"].(string)
			switch {
			case tc.open && (isErr || sc["state"] != "pending" || ref == ""):
				t.Fatalf("approval_request = %q, want a pending ticket", text)
			case tc.open && tc.sameAs != nil && ref != *tc.sameAs:
				t.Errorf("approval_request answered %s, want the pending ticket %s", ref, *tc.sameAs)
			case tc.open && tc.saveRef != nil:
				*tc.saveRef = ref
			case !tc.open && (!isErr || text != tc.wantText()):
				t.Fatalf("approval_request = %q (error %v), want %q", text, isErr, tc.wantText())
			}
			if got := approvalRequests(t, r.app, claims.Subject) - events; got != tc.wantEvents {
				t.Errorf("approval request events = %d, want %d", got, tc.wantEvents)
			}
			if got := pendingTickets(t, r.app) - pending; got != tc.wantEvents {
				t.Errorf("new pending tickets = %d, want %d", got, tc.wantEvents)
			}
			if got := counterValue(t, r.app, "straza_approvals_unroutable_total") - unroutable; got != float64(tc.wantUnroutable) {
				t.Errorf("straza_approvals_unroutable_total rose by %v, want %d", got, tc.wantUnroutable)
			}
			records[i] = map[string]any{"nonce": nonce, "user": claims.Subject, "session": claims.Session, "app": "straza",
				"effect": tc.wantEffect, "ruleId": "ask", "reason": ""}
			if !tc.open {
				records[i]["reason"] = tc.wantText()
			}
		})
	}
	for i, want := range records {
		if want == nil {
			continue
		}
		t.Run(rows[i].name+" leaves one record", func(t *testing.T) {
			nonce := want["nonce"].(string)
			rec := awaitMCPRecords(t, r.app, 1, func(d map[string]any) bool {
				args, _ := d["arguments"].(string)
				return d["toolName"] == "approval_request" && strings.Contains(args, `"reason":"`+nonce+`"`)
			})[0]
			delete(want, "nonce")
			for k, v := range want {
				if rec[k] != v {
					t.Errorf("the record's %s = %v, want %v", k, rec[k], v)
				}
			}
		})
	}
	if pending, _ := r.app.approval.List(ctx, "pending"); len(pending) != 2 {
		t.Errorf("%d pending tickets, want the happy path's and the different call's", len(pending))
	}
}

// TestApprovalRequestSpendsNoSubmitBudget pins that approval_request for a
// described straza__draft_submit leaves the agent's submit budget of one
// draft every 10 seconds unspent: the real submit right after it is stored.
func TestApprovalRequestSpendsNoSubmitBudget(t *testing.T) {
	t.Parallel()
	r := newDoorRig(t)
	ctx := context.Background()
	src := "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata: {name: ask}\nspec:\n  match: {users: [asker]}\n  rules:\n" +
		"    - id: ask\n      tools: [mcp.call]\n      apps: [straza]\n      toolNames: {allow: [approval_request]}\n      effect: allow\n"
	if _, err := r.app.store.Policies().Create(ctx, store.PolicySet{Name: "ask", Status: "active", YAMLSource: src}); err != nil {
		t.Fatal(err)
	}
	catRecompile(t, r.app)
	asker, tok := r.agent(t, "asker")
	args := map[string]any{"documents": []string{draftApp("nb", "https://nb.example/mcp", "NB.")}}
	const noApproval = "Straza: no approval needed. The described call is already allowed by policy; just make the call"
	text, isErr, _ := callTool(t, r.base, tok, "straza__approval_request", map[string]any{
		"action": map[string]any{"tool": "mcp.call", "app": "straza", "tool_name": "draft_submit", "args": args}})
	if !isErr || text != noApproval {
		t.Fatalf("approval_request = %q, want %q", text, noApproval)
	}
	claims, err := r.app.tokens.Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	rec := awaitMCPRecords(t, r.app, 1, func(d map[string]any) bool { return d["user"] == asker.ID && d["toolName"] == "approval_request" })[0]
	if rec["effect"] != "deny" || rec["reason"] != noApproval || rec["session"] != claims.Session || rec["ruleId"] != "ask" {
		t.Errorf("the record = %v, want a deny of rule ask in session %s with the answer as its reason", rec, claims.Session)
	}
	if n := approvalRequests(t, r.app, asker.ID); n != 0 {
		t.Errorf("%d approval request events, want none", n)
	}
	if text, isErr, sc := submit(t, r.base, tok, args); isErr || sc["draft"] == nil {
		t.Errorf("the real submit after approval_request = %q, want a stored draft", text)
	}
}

// TestApprovalRequestUnderFullAuditQueue pins audit before act: with the
// audit queue full under block, approval_request answers auditQueueFullMsg
// and opens no ticket, because its record could not be queued.
func TestApprovalRequestUnderFullAuditQueue(t *testing.T) {
	t.Parallel()
	var token string
	var kim store.User
	app, _, _, _ := heldAuditApp(t, true, 300*time.Millisecond, func(app *App, base string) {
		kim = seedGatewayUser(t, app, "kim", "dev")
		catActivate(t, app, "held-ticket", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: held-ticket}
spec:
  match: {roles: [dev]}
  rules:
    - id: ask
      tools: [mcp.call]
      apps: [straza]
      toolNames: {allow: [approval_request]}
      effect: allow
    - id: deploy-ticket
      tools: [shell.exec]
      command: {allowPatterns: ["deploy-prod*"]}
      effect: allow
      mode: approve
      approve: {class: ticket, roles: [change], ticketTTLSeconds: 86400, grantTTLSeconds: 3600}
`)
		token, _ = gatewaySession(t, base, "kim")
	})
	ctx := context.Background()
	base := "http://" + app.Addr()
	text, isErr, _ := callTool(t, base, token, "straza__approval_request", map[string]any{
		"action": map[string]any{"tool": "shell.exec", "command": "deploy-prod --now"}, "reason": "ship"})
	if !isErr || text != auditQueueFullMsg {
		t.Fatalf("approval_request = %q (error %v), want auditQueueFullMsg", text, isErr)
	}
	if pending, _ := app.approval.List(ctx, string(approval.StatePending)); len(pending) != 0 {
		t.Errorf("%d tickets opened without their record", len(pending))
	}
	if n := approvalRequests(t, app, kim.ID); n != 0 {
		t.Errorf("%d approval request events, want none", n)
	}
}

// TestApprovalRequestStoreFault drives approval_request through /mcp while
// one of its lookups meets a store fault, the deny-final lookup, the
// pending-ticket lookup of the route check or its read of the requester who
// decides, and pins that the agent reads the sentence that says to retry,
// the one the other lanes answer, that the call's one record is a deny with
// that sentence as its reason, that no ticket opens, and that the native
// lane writes its one fail-closed Error record and counts it once.
func TestApprovalRequestStoreFault(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name      string
		repo      string // the faultStore key armed for the call
		wantCause string // the op-wrapped cause the Error record carries
		approve   string // the approve block of the ticket rule
		want      string // the sentence the agent reads and the record keeps
	}{
		{name: "the deny-final lookup", repo: "approvals-latest", wantCause: "approval denied-ticket-find: db down",
			approve: "{class: ticket, roles: [change]}", want: approvalStoreDownReason},
		{name: "the pending-ticket lookup of the route check", repo: "approvals", wantCause: "approval request-dedupe: db down",
			approve: "{class: ticket, roles: [change]}", want: approvalStoreDownReason},
		{name: "the read of the requester who decides", repo: "users", wantCause: "a user record that says who decides could not be read",
			approve: "{class: ticket}", want: approvalDeciderUnreadReason},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log, buf := captureLogger()
			app, base, fs := testAppFaultLog(t, log)
			kim := seedIdentity(t, app)
			catActivate(t, app, "route-ticket", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: route-ticket}
spec:
  match: {roles: [dev]}
  rules:
    - id: ask
      tools: [mcp.call]
      apps: [straza]
      toolNames: {allow: [approval_request]}
      effect: allow
    - id: deploy-ticket
      tools: [shell.exec]
      command: {allowPatterns: ["deploy-prod*"]}
      effect: allow
      mode: approve
      approve: `+tc.approve+`
`)
			token, session := gatewaySession(t, base, "kim")
			// Only the call's own records count, so the capture starts here.
			buf.Reset()
			fs.arm(tc.repo, errors.New("db down"))
			text, isErr, _ := callTool(t, base, token, "straza__approval_request", map[string]any{
				"action": map[string]any{"tool": "shell.exec", "command": "deploy-prod --now"}, "reason": "store-fault"})
			fs.disarm()
			if !isErr || text != tc.want {
				t.Fatalf("approval_request = %q (error %v), want %q", text, isErr, tc.want)
			}
			rec := assertOneFailClosedRecord(t, buf, "native", "")
			for _, want := range []string{"correlation_id=", "rule=deploy-ticket", tc.wantCause} {
				if !strings.Contains(rec, want) {
					t.Errorf("fail-closed record lacks %q: %s", want, rec)
				}
			}
			if got := failClosedCounter(t, app, "native"); got != 1 {
				t.Errorf("straza_failclosed_total{lane=native} = %v, want 1", got)
			}
			if got := counterValue(t, app, "straza_approvals_unroutable_total"); got != 0 {
				t.Errorf("straza_approvals_unroutable_total = %v, want 0 for a store fault", got)
			}
			if n := pendingTickets(t, app); n != 0 {
				t.Errorf("%d tickets opened on a store fault", n)
			}
			got := awaitMCPRecords(t, app, 1, func(d map[string]any) bool {
				return d["user"] == kim.ID && d["toolName"] == "approval_request"
			})[0]
			want := map[string]any{"effect": "deny", "reason": tc.want, "ruleId": "ask", "session": session}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("the record's %s = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}
