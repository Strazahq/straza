package server

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/objects"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// TestGatewayApproveObjectKey is threat T9 on the gateway's hold lane: in
// both binding scopes the approval key binds the object a call names, so an
// approval for one repo never runs a call on another, and a call that names
// no object keys exactly as before.
func TestGatewayApproveObjectKey(t *testing.T) {
	t.Parallel()
	claims := authn.Claims{Session: "sess-1", Subject: "user-1"}
	sub := policy.Subject{User: "pam", Roles: []string{"gh-pay"}}
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "ghapp", ToolName: "pr_create",
		Args: json.RawMessage(`{"repo":"Strazahq/pay-api"}`)}
	decision := func(binding string) policy.Decision {
		return policy.Decision{Effect: policy.EffectAllow, RuleID: "pr-hold", SetName: "gh",
			Approve: &policy.ApproveSpec{Roles: []string{"sec"}, TimeoutSeconds: 1, RetryTTLSeconds: 60, Binding: binding}}
	}
	payAPI := approval.Object{Type: "repo", Value: "Strazahq/pay-api"}
	site := approval.Object{Type: "repo", Value: "Strazahq/site"}
	app := &App{}
	mint := func(t *testing.T, binding string, obj approval.Object) string {
		t.Helper()
		g := &fakeApprovalGate{await: approval.Record{ID: "rec-1", State: approval.StateApproved}}
		if o := app.gateApprove(context.Background(), g, claims, sub, ev, decision(binding), "", ev.Args, obj, true); !o.run {
			t.Fatalf("an approved hold did not run: %+v", o)
		}
		return g.lastInput.ArgvHash
	}
	for _, binding := range []string{policy.ApproveBindingTool, policy.ApproveBindingCall} {
		a, b := mint(t, binding, payAPI), mint(t, binding, site)
		if a == b {
			t.Errorf("%s scope: an approval for one repo keys %q, the same as another repo's", binding, a)
		}
		before, err := approval.EventKeyV2(ev, binding)
		if err != nil {
			t.Fatal(err)
		}
		if zero := mint(t, binding, approval.Object{}); zero != before {
			t.Errorf("%s scope: a call that names no object keys %q, want today's %q", binding, zero, before)
		}
		// A retry on the other repo finds no approval to use and asks again.
		g := &fakeApprovalGate{holdHit: true, holdKey: a, await: approval.Record{ID: "rec-2", State: approval.StateDenied, DecidedByName: "ada"}}
		if o := app.gateApprove(context.Background(), g, claims, sub, ev, decision(binding), "", ev.Args, site, true); o.run || g.reqs != 1 {
			t.Errorf("%s scope: the approval for Strazahq/pay-api ran a call on Strazahq/site: %+v, requests %d", binding, o, g.reqs)
		}
	}
}

// TestApprovalObject pins what an approval key binds: the object a call
// names by its type and value for every grant on a tool whose reach is
// argument, whichever role granted it and whether the list holds the value,
// and nothing for any other reach.
func TestApprovalObject(t *testing.T) {
	hit := func(reach, value, id string) *objects.Hit {
		return &objects.Hit{Type: "repo", Reach: reach, Value: value, ID: id}
	}
	cases := []struct {
		name string
		v    objects.Verdict
		want approval.Object
	}{
		{"a partial grant", objects.Verdict{Object: hit("argument", "Strazahq/pay-api", "101"), SendID: true},
			approval.Object{Type: "repo", Value: "Strazahq/pay-api"}},
		{"an every grant on a listed repo", objects.Verdict{Object: hit("argument", "Strazahq/pay-api", "101")},
			approval.Object{Type: "repo", Value: "Strazahq/pay-api"}},
		{"an every grant on an unlisted repo", objects.Verdict{Object: hit("argument", "Strazahq/new", "")},
			approval.Object{Type: "repo", Value: "Strazahq/new"}},
		{"reach credential", objects.Verdict{Object: hit("credential", "", "")}, approval.Object{}},
		{"reach all", objects.Verdict{}, approval.Object{}},
	}
	for _, tc := range cases {
		if got := approvalObject(tc.v); got != tc.want {
			t.Errorf("%s: approvalObject = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// ghTicketPolicy gates pr_create on ghapp behind a tool-scoped ticket, so
// the repo a call names is the only part of its arguments the key binds.
const ghTicketPolicy = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: gh-tickets}
spec:
  rules:
    - id: pr-ticket
      tools: [mcp.call]
      apps: [ghapp]
      toolNames: {allow: ["pr_create"]}
      effect: allow
      mode: approve
      approve: {class: ticket, binding: tool, roles: [sec-approvers], ticketTTLSeconds: 86400, grantTTLSeconds: 3600}
`

// TestGatewayObjectTicket pins that a ticket opened through approval_request
// for a repo is used by the real tools/call with the same arguments, keyed
// by the repo's value whichever role grants it and whether the list holds
// the repo, also when the repo joins the list or the grant flips between a
// role on every repo and a role on some, and that a ticket for one repo is
// never used by a call on another. It also pins acceptance case 4 and 7
// under the ticket rule: an uncovered call opens no approval and records no
// rule, and an approved call forwards the arguments with the justification
// stripped and the numbers intact.
func TestGatewayObjectTicket(t *testing.T) {
	t.Parallel()
	r := newGHRig(t)
	ctx := context.Background()
	if _, err := r.app.store.Policies().Create(ctx, store.PolicySet{Name: "gh-tickets", Status: "active", YAMLSource: ghTicketPolicy}); err != nil {
		t.Fatal(err)
	}
	catRecompile(t, r.app)
	approver := seedApprover(t, r.app, "secops", "sec-approvers")
	ep := ghEndpoints[0]
	cases := []struct {
		user, role, approved, other string
		listed                      bool // the repo joins the list between the approval and the call
	}{
		{"pam", "gh-pay", "Strazahq/pay-api", "Strazahq/pay-web", false},
		{"eve", "gh-every", "Strazahq/site", "Strazahq/pay-api", false},
		{"eve", "gh-every", "Strazahq/unlisted", "Strazahq/unlisted-too", false},
		{"eve", "gh-every", "Strazahq/brand-new", "Strazahq/site", true},
	}
	for _, tc := range cases {
		t.Run(tc.user+" on "+tc.approved, func(t *testing.T) {
			u, err := r.app.store.Users().GetByUsername(ctx, tc.user)
			if err != nil {
				t.Fatal(err)
			}
			claims := authn.Claims{Session: "sess-ticket", Subject: u.ID}
			sub := policy.Subject{User: tc.user, Roles: []string{tc.role}}
			req := map[string]any{"reason": "ship the fix", "action": map[string]any{"tool": "mcp.call", "app": "ghapp",
				"tool_name": "pr_create", "args": map[string]any{"repo": tc.approved}}}
			opened := nativeStruct(t, requestTicket(ctx, r.app, mustJSON(req), claims, sub))
			ref, _ := opened["ref"].(string)
			if ref == "" {
				t.Fatalf("approval_request opened no ticket: %v", opened)
			}
			if _, err := r.app.approval.Decide(ctx, ref, "approved", approver.ID, "console", "", ""); err != nil {
				t.Fatal(err)
			}
			if tc.listed {
				r.up.add(tc.approved, "104")
				if err := r.app.objects.MarkDue(ctx, r.appID); err != nil {
					t.Fatal(err)
				}
				r.waitList(t, func(l []string) bool { return slices.Contains(l, tc.approved) })
			}
			before := len(r.up.toolCalls("pr_create"))
			if text, isErr := r.call(t, ep, tc.user, "pr_create", `{"repo":`+jsonString(tc.other)+`}`, ""); !isErr {
				t.Errorf("the ticket for %s ran a call on %s: %q", tc.approved, tc.other, text)
			}
			if text, isErr := r.call(t, ep, tc.user, "pr_create", `{"repo":`+jsonString(tc.approved)+`}`, ""); isErr {
				t.Errorf("the approved ticket was not used by the real call on %s: %q", tc.approved, text)
			}
			if got := len(r.up.toolCalls("pr_create")); got != before+1 {
				t.Errorf("the upstream got %d pr_create calls, want exactly the approved one", got-before)
			}
		})
	}

	t.Run("a ticket survives the grant flipping between roles", func(t *testing.T) {
		// mix holds gh-a-fresh on Strazahq/fresh by id and gh-every. Before
		// the repo is listed only gh-every covers it, after the listing
		// gh-a-fresh grants first by name, and the ticket keys alike.
		ro, err := r.app.store.Roles().Create(ctx, store.Role{Name: "gh-a-fresh", Kind: store.RoleKindApplication})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: ro.ID, AppID: r.appID, ToolMatcher: `["*"]`,
			Scope: &store.ObjectScope{Objects: []store.CoveredObject{{ID: "105", Value: "Strazahq/fresh"}}}}); err != nil {
			t.Fatal(err)
		}
		if err := r.app.refreshBindings(ctx); err != nil {
			t.Fatal(err)
		}
		u := seedGatewayUser(t, r.app, "mix", "gh-a-fresh")
		every, err := r.app.store.Roles().GetByName(ctx, "gh-every")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.app.store.Roles().Assign(ctx, store.RoleAssignment{SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: every.ID}); err != nil {
			t.Fatal(err)
		}
		r.app.resolver.Bump()
		r.tokens["mix"] = sessionToken(t, r.base, "mix")
		sub := policy.Subject{User: "mix", Roles: []string{"gh-a-fresh", "gh-every"}}
		req := map[string]any{"reason": "ship the fix", "action": map[string]any{"tool": "mcp.call", "app": "ghapp",
			"tool_name": "pr_create", "args": map[string]any{"repo": "Strazahq/fresh"}}}
		opened := nativeStruct(t, requestTicket(ctx, r.app, mustJSON(req), authn.Claims{Session: "sess-mix", Subject: u.ID}, sub))
		ref, _ := opened["ref"].(string)
		if ref == "" {
			t.Fatalf("approval_request opened no ticket: %v", opened)
		}
		if _, err := r.app.approval.Decide(ctx, ref, "approved", approver.ID, "console", "", ""); err != nil {
			t.Fatal(err)
		}
		r.up.add("Strazahq/fresh", "105")
		if err := r.app.objects.MarkDue(ctx, r.appID); err != nil {
			t.Fatal(err)
		}
		r.waitList(t, func(l []string) bool { return slices.Contains(l, "Strazahq/fresh") })
		if text, isErr := r.call(t, ep, "mix", "pr_create", `{"repo":"Strazahq/fresh"}`, ""); isErr {
			t.Fatalf("the ticket approved under gh-every was not used once gh-a-fresh granted: %q", text)
		}
		calls := r.up.toolCalls("pr_create")
		if got := calls[len(calls)-1].meta[objectIDMeta]; got != "105" {
			t.Errorf("the call ran without gh-a-fresh's grant: object-id %v, want 105", got)
		}
	})

	t.Run("an uncovered call opens no approval", func(t *testing.T) {
		pending, _ := r.app.approval.List(ctx, "pending")
		text, isErr := r.call(t, ep, "pam", "pr_create", `{"repo":"Strazahq/site","n":"ticketuncovered"}`, "")
		if !isErr || text != gwG1("Strazahq/site", "Strazahq/pay-api and Strazahq/pay-web") {
			t.Errorf("uncovered call under a ticket rule = %q (error %v)", text, isErr)
		}
		if after, _ := r.app.approval.List(ctx, "pending"); len(after) != len(pending) {
			t.Errorf("an uncovered call opened %d approvals", len(after)-len(pending))
		}
		recs := r.mcpRecords(t, "ticketuncovered", 1)
		if len(recs) != 1 || recs[0]["ruleId"] != "" || recs[0]["effect"] != "deny" {
			t.Errorf("the refusal's records = %v, want one deny with no rule", recs)
		}
	})

	t.Run("a gated call forwards the stripped arguments", func(t *testing.T) {
		u, err := r.app.store.Users().GetByUsername(ctx, "pam")
		if err != nil {
			t.Fatal(err)
		}
		req := map[string]any{"reason": "ship the fix", "action": map[string]any{"tool": "mcp.call", "app": "ghapp",
			"tool_name": "pr_create", "args": map[string]any{"repo": "Strazahq/pay-web"}}}
		opened := nativeStruct(t, requestTicket(ctx, r.app, mustJSON(req), authn.Claims{Session: "sess-pam", Subject: u.ID},
			policy.Subject{User: "pam", Roles: []string{"gh-pay"}}))
		ref, _ := opened["ref"].(string)
		if _, err := r.app.approval.Decide(ctx, ref, "approved", approver.ID, "console", "", ""); err != nil {
			t.Fatal(err)
		}
		args := `{"_straza_justification":"ship it","repo":"Strazahq/pay-web","big":98765432109876543210,"dec":0.1000000000000000000000001}`
		if text, isErr := r.call(t, ep, "pam", "pr_create", args, ""); isErr {
			t.Fatalf("the approved gated call was refused: %q", text)
		}
		calls := r.up.toolCalls("pr_create")
		const want = `{"big":98765432109876543210,"dec":0.1000000000000000000000001,"repo":"Strazahq/pay-web"}`
		if got := calls[len(calls)-1].args; got != want {
			t.Errorf("the upstream got %s\nwant %s", got, want)
		}
	})
}
