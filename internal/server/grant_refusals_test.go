package server

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/authn"
)

// grantRefusalRow is one refusal class fired at /mcp and at /v1/grants with
// the same rig, user, tool and repo.
type grantRefusalRow struct {
	class, user, op string
	filter          bool             // the rig whose catalog filters by policy and caps ghapp at 0.01 rps
	refs            []authn.GrantRef // the grant's refs
	tool, args      string           // the /mcp tools/call
	prep            func(r *grantRig, s *grantSessionT)
	deny            bool // a person denies the approval the call opens
	rule, reason    string
}

// TestGrantRefusalsMatchTheGateway walks every refusal class of the shared
// decision on /mcp and on /v1/grants with the same setup: visibility, the
// overlay, the rate limit, the object gate, the engine, the approval and
// an open ticket. Each path refuses and writes exactly one record under the
// same rule with the row's reason, so a gate added to one path and missing
// on the other fails here. A ticket's sentence speaks of a call on /mcp and
// of a git command on the grant route, so its row checks the common start.
// /mcp's git_push takes no refs by its schema, so the approval rows send
// the branch alone.
func TestGrantRefusalsMatchTheGateway(t *testing.T) {
	t.Parallel()
	drain := func(r *grantRig, s *grantSessionT) {
		if !r.app.gateway.limiter.Allow(s.claims.Session+"|ghapp", 0.01) {
			t.Fatal("the session's rate bucket was empty before the drain")
		}
	}
	rows := []grantRefusalRow{
		{class: "visibility", user: "fen", op: "push", tool: "ghapp__git_push", args: `{"repo":"Strazahq/pay-api"}`,
			reason: "no access row for this session's roles admits it"},
		{class: "overlay", user: "den", op: "fetch", filter: true, tool: "ghapp__git_fetch", args: `{"repo":"Strazahq/pay-api"}`,
			rule: "deny-git-fetch", reason: "Straza: git fetch is off for this role"},
		{class: "rate limit", user: "eve", op: "fetch", filter: true, tool: "ghapp__git_fetch", args: `{"repo":"Strazahq/pay-api"}`,
			prep: drain, reason: `Straza: rate limit exceeded for the MCP server "ghapp" (0 rps). Retry shortly`},
		{class: "object gate", user: "pam", op: "fetch", tool: "ghapp__git_fetch", args: `{"repo":"Strazahq/site"}`,
			reason: "git_fetch on Strazahq/site was refused: none of your roles on ghapp covers that repo."},
		{class: "engine", user: "den", op: "fetch", tool: "ghapp__git_fetch", args: `{"repo":"Strazahq/pay-api"}`,
			rule: "deny-git-fetch", reason: "Straza: git fetch is off for this role"},
		{class: "approval", user: "hol", op: "push", refs: mainRef, tool: "ghapp__git_push", args: `{"repo":"Strazahq/pay-api","branch":"main"}`,
			deny: true, rule: "hold-git-push", reason: "Straza: approval denied by secops"},
		{class: "ticket", user: "tic", op: "push", refs: mainRef, tool: "ghapp__git_push", args: `{"repo":"Strazahq/pay-api","branch":"main"}`,
			rule: "ticket-git-push", reason: "Straza: approval ticket "},
	}
	rigs := map[bool]*grantRig{false: newGrantRig(t, grantRigOpts{}), true: newGrantRig(t, grantRigOpts{filter: true, rps: 0.01})}
	for _, row := range rows {
		t.Run(row.class, func(t *testing.T) {
			t.Parallel()
			r := rigs[row.filter]
			gw, gs := r.session(t, row.user), r.session(t, row.user)
			if row.prep != nil {
				row.prep(r, gw)
				row.prep(r, gs)
			}
			answer := make(chan map[string]any, 1)
			go func() {
				var out map[string]any
				defer func() { answer <- out }()
				out = r.callTool(t, gw, row.tool, row.args)
			}()
			if row.deny {
				r.decide(t, r.pendingOf(t, gw), "denied")
			}
			if out := <-answer; !mcpRefused(out) {
				t.Errorf("/mcp answered %v, want a refusal", out)
			}
			var ch <-chan grantAnswer
			if repo := strings.Contains(row.args, "site"); repo {
				ch = asyncGrant(t, r, gs, "https://github.com/Strazahq/site", row.op, row.refs)
			} else {
				ch = asyncGrant(t, r, gs, payAPI, row.op, row.refs)
			}
			if row.deny {
				r.decide(t, r.pendingOf(t, gs), "denied")
			}
			if ans := <-ch; ans.status != 403 || ans.out["grant"] != nil {
				t.Errorf("/v1/grants answered %d %v, want 403", ans.status, ans.out)
			}
			for path, s := range map[string]*grantSessionT{"/mcp": gw, "/v1/grants": gs} {
				recs := r.records(t, s)
				if len(recs) != 1 {
					t.Errorf("%s wrote %d records, want one: %v", path, len(recs), recs)
					continue
				}
				d := recs[0].data
				reason, _ := d["reason"].(string)
				// /mcp's record of an unseen name names the probe as asked,
				// and a grant's record always names the server.
				if d["effect"] != "deny" || d["ruleId"] != row.rule || !strings.Contains(reason, row.reason) ||
					d["user"] != s.claims.Subject || (path == "/v1/grants" && d["app"] != "ghapp") {
					t.Errorf("%s record = %v, want a deny of user %s under rule %q with %q", path, d, s.claims.Subject, row.rule, row.reason)
				}
				want := map[string]string{"/mcp": "tool.pre", "/v1/grants": "grant"}[path]
				if d["event"] != want {
					t.Errorf("%s record event = %v, want %s", path, d["event"], want)
				}
			}
		})
	}
}

// asyncGrant asks for a grant on upstream in the background.
func asyncGrant(t *testing.T, r *grantRig, s *grantSessionT, upstream, op string, refs []authn.GrantRef) <-chan grantAnswer {
	ch := make(chan grantAnswer, 1)
	go func() {
		var ans grantAnswer
		defer func() { ch <- ans }()
		ans.status, ans.out = r.grant(t, s, upstream, op, refs, nil)
	}()
	return ch
}

// mcpRefused reports whether a tools/call answer refused the call: a
// JSON-RPC error or a tool result marked as an error.
func mcpRefused(out map[string]any) bool {
	if _, ok := out["error"]; ok {
		return true
	}
	res, _ := out["result"].(map[string]any)
	return res["isError"] == true
}

// TestGrantRefusalSentences pins the sentence each refusal class answers on
// /v1/grants: the visibility sentence names the op, the tool and the
// server, and the overlay answers the denying rule's reason, which /mcp
// hides behind unknown tool.
func TestGrantRefusalSentences(t *testing.T) {
	t.Parallel()
	r := newGrantRig(t, grantRigOpts{filter: true})
	if status, out := r.grant(t, r.session(t, "fen"), payAPI, "push", nil, nil); status != 403 ||
		out["error"] != fmt.Sprintf(grantInvisibleMsg, "push", "git_push", "ghapp") {
		t.Errorf("visibility = %d %v", status, out)
	}
	if status, out := r.grant(t, r.session(t, "den"), payAPI, "fetch", nil, nil); status != 403 ||
		out["error"] != "Straza: git fetch is off for this role" {
		t.Errorf("overlay = %d %v", status, out)
	}
}

// TestGrantExamplesMatchTheProducer pins the field names of the revision 48
// examples to the records the grant route writes: a push grant issued after
// an approval, and a fetch the object gate refused.
func TestGrantExamplesMatchTheProducer(t *testing.T) {
	t.Parallel()
	r := newGrantRig(t, grantRigOpts{})
	issued := r.session(t, "hol")
	ch := asyncGrant(t, r, issued, payAPI, "push", mainRef)
	r.decide(t, r.pendingOf(t, issued), "approved")
	if ans := <-ch; ans.status != 200 {
		t.Fatalf("push grant = %d %v", ans.status, ans.out)
	}
	refused := r.session(t, "pam")
	if status, out := r.grant(t, refused, "https://github.com/Strazahq/site", "fetch", nil, nil); status != 403 {
		t.Fatalf("uncovered fetch = %d %v", status, out)
	}
	for example, s := range map[string]*grantSessionT{"valid-audit-mcp-grant-issued.json": issued, "valid-audit-mcp-grant-refused.json": refused} {
		recs := r.records(t, s)
		if len(recs) != 1 {
			t.Fatalf("%s: %d records, want one", example, len(recs))
		}
		want, got := recordFields(exampleData(t, example), ""), recordFields(recs[0].data, "")
		if !slices.Equal(want, got) {
			t.Errorf("%s names %v\nthe grant route writes %v", example, want, got)
		}
	}
}
