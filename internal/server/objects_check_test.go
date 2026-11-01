package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// objectsCheckPolicy gates repo_read on objects-demo behind a tool-scoped
// ticket, so an approval key that left the repo out would let a ticket for
// one repo cover a call on another.
const objectsCheckPolicy = `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: objects-check}
spec:
  rules:
    - id: repo-read-ticket
      tools: [mcp.call]
      apps: [objects-demo]
      toolNames: {allow: ["repo_read"]}
      effect: allow
      mode: approve
      approve: {class: ticket, binding: tool, roles: [sec], ticketTTLSeconds: 86400, grantTTLSeconds: 3600}
`

// objectsCheckApp boots an app serving objects-demo, which pins repo with
// repo_read naming it in repo, whoami reaching none and the listing tool
// repo_list unnamed, so reaching all. Its five repos are listed before it
// answers. The roles bind every tool: od-every on every repo, od-pay on
// Strazahq/pay-api by id, od-a-site on Strazahq/site, and od-bare with no
// covers line.
func objectsCheckApp(t *testing.T, policyYAML string) (*App, string, string, *listingUpstream) {
	t.Helper()
	app, base := testApp(t, func(c *config.Config) { c.Apps.Objects.Enabled = true })
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	ctx := context.Background()
	up := startListingUpstream(t)
	if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(listingManifest(up.URL)), nil); code != http.StatusCreated {
		t.Fatalf("install objects-demo = %d", code)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/apps/objects-demo/secrets", bearer, map[string]any{"value": "app-tok"}, nil); code != http.StatusCreated {
		t.Fatalf("set the server's own secret = %d", code)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/apps/objects-demo/objects/credential", bearer, map[string]any{"value": "listing-tok-NEVER-LEAK"}, nil); code != http.StatusCreated {
		t.Fatalf("set the listing credential = %d", code)
	}
	row, err := app.store.Apps().GetByName(ctx, "objects-demo")
	if err != nil {
		t.Fatal(err)
	}
	scopes := map[string]*store.ObjectScope{
		"od-every":  {Every: true},
		"od-pay":    {Objects: []store.CoveredObject{{ID: "9012332", Value: "Strazahq/pay-api"}}},
		"od-a-site": {Objects: []store.CoveredObject{{ID: "9012331", Value: "Strazahq/site"}}},
		"od-bare":   nil,
	}
	for name, scope := range scopes {
		ro, err := app.store.Roles().Create(ctx, store.Role{Name: name, Kind: store.RoleKindApplication})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: ro.ID, AppID: row.ID, ToolMatcher: `["*"]`, Scope: scope}); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	if policyYAML != "" {
		if _, err := app.store.Policies().Create(ctx, store.PolicySet{Name: "objects-check", Status: "active", YAMLSource: policyYAML}); err != nil {
			t.Fatal(err)
		}
		if _, err := app.snapshots.Recompile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for l := app.objects.Get(row.ID); l == nil || len(l.Objects) != 5; l = app.objects.Get(row.ID) {
		if time.Now().After(deadline) {
			t.Fatalf("the object list of objects-demo never loaded: %+v", l)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return app, base, bearer, up
}

// simAnswer is the simulate answer as an operator's client reads it.
type simAnswer struct {
	Active policy.Decision  `json:"active"`
	Draft  *policy.Decision `json:"draft"`
	Object *simObject       `json:"object"`
}

// TestSimulateObject is acceptance case 20 for simulate: on a server that
// pins an object type it answers as the gateway does, with the object
// gate's verdict in the object member, its refusal as a deny that no rule
// made and no engine reached, in the draft too, and with args or without.
func TestSimulateObject(t *testing.T) {
	t.Parallel()
	_, base, bearer, _ := objectsCheckApp(t, "")
	const draft = "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata: {name: sim-draft}\nspec:\n  rules:\n    - {id: deny-all-mcp, tools: [mcp.call], effect: deny, reason: \"Straza: the draft denies every MCP call\"}\n"
	tests := []struct {
		name   string
		roles  []string
		tool   string
		args   string // "" sends no args
		draft  bool
		effect string
		object *simObject // nil wants no object member
		reason string     // the start of a refusal, "" for an allow
	}{
		{name: "a partial role covers its repo", roles: []string{"od-pay"}, tool: "repo_read", args: `{"repo":"Strazahq/pay-api"}`, effect: "allow",
			object: &simObject{Type: "repo", Reach: "argument", Value: "Strazahq/pay-api", ID: "9012332", Covered: true, Role: "od-pay"}},
		{name: "a partial role does not cover another repo", roles: []string{"od-pay"}, tool: "repo_read", args: `{"repo":"Strazahq/site"}`, effect: "deny",
			object: &simObject{Type: "repo", Reach: "argument", Value: "Strazahq/site", ID: "9012331"},
			reason: "Straza: repo_read on Strazahq/site was refused: none of your roles on objects-demo covers that repo."},
		{name: "the refusal is the draft's answer too", roles: []string{"od-pay"}, tool: "repo_read", args: `{"repo":"Strazahq/site"}`, draft: true, effect: "deny",
			object: &simObject{Type: "repo", Reach: "argument", Value: "Strazahq/site", ID: "9012331"},
			reason: "Straza: repo_read on Strazahq/site was refused: none of your roles on objects-demo covers that repo."},
		{name: "a covered call still meets the draft", roles: []string{"od-pay"}, tool: "repo_read", args: `{"repo":"Strazahq/pay-api"}`, draft: true, effect: "allow",
			object: &simObject{Type: "repo", Reach: "argument", Value: "Strazahq/pay-api", ID: "9012332", Covered: true, Role: "od-pay"}},
		{name: "a call without args is refused for a partial role", roles: []string{"od-pay"}, tool: "repo_read", effect: "deny",
			object: &simObject{Type: "repo", Reach: "argument"},
			reason: "Straza: repo_read needs the repo it works on in its argument repo, and this call carries none."},
		{name: "a call without args is refused for a role on every repo", roles: []string{"od-every"}, tool: "repo_read", effect: "deny",
			object: &simObject{Type: "repo", Reach: "argument"},
			reason: "Straza: repo_read needs the repo it works on in its argument repo, and this call carries none."},
		{name: "a role on every repo covers a repo the list does not hold", roles: []string{"od-every"}, tool: "repo_read", args: `{"repo":"Strazahq/new"}`, effect: "allow",
			object: &simObject{Type: "repo", Reach: "argument", Value: "Strazahq/new", Covered: true, Role: "od-every"}},
		{name: "the first covering role by name grants", roles: []string{"od-every", "od-a-site"}, tool: "repo_read", args: `{"repo":"Strazahq/pay-api"}`, effect: "allow",
			object: &simObject{Type: "repo", Reach: "argument", Value: "Strazahq/pay-api", ID: "9012332", Covered: true, Role: "od-every"}},
		{name: "a partial role first by name grants its own repo", roles: []string{"od-every", "od-a-site"}, tool: "repo_read", args: `{"repo":"Strazahq/site"}`, effect: "allow",
			object: &simObject{Type: "repo", Reach: "argument", Value: "Strazahq/site", ID: "9012331", Covered: true, Role: "od-a-site"}},
		{name: "a binding without covers covers nothing", roles: []string{"od-bare"}, tool: "repo_read", args: `{"repo":"Strazahq/pay-api"}`, effect: "deny",
			object: &simObject{Type: "repo", Reach: "argument", Value: "Strazahq/pay-api", ID: "9012332"},
			reason: "Straza: repo_read on Strazahq/pay-api was refused: none of your roles on objects-demo covers that repo."},
		{name: "a tool that reaches every repo refuses a partial role", roles: []string{"od-pay"}, tool: "repo_list", effect: "deny",
			object: &simObject{Type: "repo", Reach: "all"},
			reason: "Straza: repo_list on objects-demo can reach every repo, so only a role on every repo may call it"},
		{name: "a tool that reaches every repo admits a role on every repo", roles: []string{"od-every"}, tool: "repo_list", effect: "allow",
			object: &simObject{Type: "repo", Reach: "all", Covered: true, Role: "od-every"}},
		{name: "a tool that reaches no repo has no object", roles: []string{"od-pay"}, tool: "whoami", effect: "allow"},
		{name: "a subject with no binding meets the engine alone", roles: []string{"nobody"}, tool: "repo_read", args: `{"repo":"Strazahq/pay-api"}`, effect: "deny"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := map[string]any{"kind": "tool.pre", "tool": "mcp.call", "app": "objects-demo", "toolName": tc.tool}
			if tc.args != "" {
				ev["args"] = json.RawMessage(tc.args)
			}
			body := map[string]any{"event": ev, "subject": map[string]any{"roles": tc.roles}}
			if tc.draft {
				body["draft"] = draft
			}
			var got simAnswer
			if code := adminReq(t, http.MethodPost, base+"/v1/admin/policies/simulate", bearer, body, &got); code != http.StatusOK {
				t.Fatalf("simulate = %d", code)
			}
			if got.Active.Effect != tc.effect {
				t.Errorf("active = %+v, want effect %s", got.Active, tc.effect)
			}
			switch {
			case tc.object == nil && got.Object != nil:
				t.Errorf("object = %+v, want none", got.Object)
			case tc.object != nil && got.Object == nil:
				t.Fatalf("no object member, want %+v", tc.object)
			case tc.object != nil:
				refusal := got.Object.Refusal
				got.Object.Refusal = ""
				if *got.Object != *tc.object {
					t.Errorf("object = %+v, want %+v", got.Object, tc.object)
				}
				if !strings.HasPrefix(refusal, tc.reason) || (tc.reason == "") != (refusal == "") {
					t.Errorf("refusal = %q, want it to open with %q", refusal, tc.reason)
				}
				if tc.reason != "" && (got.Active.Reason != refusal || got.Active.RuleID != "" || got.Active.SetName != "") {
					t.Errorf("active = %+v, want a deny with no rule whose reason is the refusal", got.Active)
				}
			}
			if tc.draft {
				switch {
				case got.Draft == nil:
					t.Fatal("no draft decision")
				case tc.reason != "" && (got.Draft.Effect != "deny" || got.Draft.Reason != got.Active.Reason || got.Draft.RuleID != ""):
					t.Errorf("draft = %+v, want the gate's refusal", got.Draft)
				case tc.reason == "" && got.Draft.RuleID != "deny-all-mcp":
					t.Errorf("draft = %+v, want the draft's rule to decide a covered call", got.Draft)
				}
			}
		})
	}
}

// TestCatalogPreviewObject pins the preview's object member: every for a
// held binding on every repo, some for named repos on a tool that names
// one, none where the gate refuses every call, and no member where the gate
// does not apply.
func TestCatalogPreviewObject(t *testing.T) {
	t.Parallel()
	_, base, bearer, _ := objectsCheckApp(t, "")
	type obj = previewObject
	tests := []struct {
		role string
		want map[string]*obj
	}{
		{"od-every", map[string]*obj{"repo_read": {"repo", "argument", "every"}, "repo_list": {"repo", "all", "every"}, "whoami": nil}},
		{"od-pay", map[string]*obj{"repo_read": {"repo", "argument", "some"}, "repo_list": {"repo", "all", "none"}, "whoami": nil}},
		{"od-bare", map[string]*obj{"repo_read": {"repo", "argument", "none"}, "repo_list": {"repo", "all", "none"}, "whoami": nil}},
	}
	for _, tc := range tests {
		t.Run(tc.role, func(t *testing.T) {
			var got catalogPreviewResponse
			if code := adminReq(t, http.MethodGet, base+"/v1/admin/catalog/preview?app=objects-demo&role="+tc.role, bearer, nil, &got); code != http.StatusOK {
				t.Fatalf("preview = %d", code)
			}
			seen := map[string]bool{}
			for _, e := range got.Entries {
				want, named := tc.want[e.Tool]
				if !named {
					continue
				}
				seen[e.Tool] = true
				switch {
				case want == nil && e.Object != nil:
					t.Errorf("%s object = %+v, want none", e.Tool, e.Object)
				case want != nil && (e.Object == nil || *e.Object != *want):
					t.Errorf("%s object = %+v, want %+v", e.Tool, e.Object, want)
				}
			}
			if len(seen) != len(tc.want) {
				t.Errorf("preview entries %+v miss a tool of %v", got.Entries, tc.want)
			}
		})
	}
}

// TestNativeApprovalRequestObject is acceptance case 19 at the ticket door:
// on a tool that names a repo, approval_request needs the args under a
// tool-scoped rule too, refuses with the gate's sentence what the gate
// refuses, and binds the repo in the key, so a ticket for one repo never
// covers a call on another.
func TestNativeApprovalRequestObject(t *testing.T) {
	t.Parallel()
	app, _, _, _ := objectsCheckApp(t, objectsCheckPolicy)
	ctx := context.Background()
	requester, err := app.store.Users().GetByUsername(ctx, "kim")
	if err != nil {
		t.Fatal(err)
	}
	claims := authn.Claims{Session: "sess-obj", Subject: requester.ID}
	sub := policy.Subject{User: "kim", Roles: []string{"od-pay"}}
	request := func(args map[string]any) map[string]any {
		action := map[string]any{"tool": "mcp.call", "app": "objects-demo", "tool_name": "repo_read"}
		if args != nil {
			action["args"] = args
		}
		return map[string]any{"action": action, "reason": "read the repo"}
	}

	const noArgs = "Straza: repo_read on objects-demo works on one repo per call, and an approval covers one repo, so approval_request needs action.args with the repo in the argument repo. Add action.args with the exact arguments of the later call, then request again"
	if res := requestTicket(ctx, app, mustJSON(request(nil)), claims, sub); !res.IsError || toolErrText(res) != noArgs {
		t.Errorf("no args = %q, want %q", toolErrText(res), noArgs)
	}
	const uncovered = "Straza: repo_read on Strazahq/site was refused: none of your roles on objects-demo covers that repo."
	if res := requestTicket(ctx, app, mustJSON(request(map[string]any{"repo": "Strazahq/site"})), claims, sub); !res.IsError || !strings.HasPrefix(toolErrText(res), uncovered) {
		t.Errorf("uncovered repo = %q, want it to open with %q", toolErrText(res), uncovered)
	}
	if pending, _ := app.approval.List(ctx, "pending"); len(pending) != 0 {
		t.Fatalf("a refused request opened %d tickets", len(pending))
	}

	res := nativeStruct(t, requestTicket(ctx, app, mustJSON(request(map[string]any{"repo": "Strazahq/pay-api"})), claims, sub))
	rec, err := app.approval.Get(ctx, res["ref"].(string))
	if err != nil {
		t.Fatal(err)
	}
	ev := policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: "objects-demo", ToolName: "repo_read", Args: []byte(`{"repo":"Strazahq/pay-api"}`)}
	want, err := approval.EventKeyObject(ev, policy.ApproveBindingTool, approval.Object{Type: "repo", Value: "Strazahq/pay-api"})
	if err != nil {
		t.Fatal(err)
	}
	bare, _ := approval.EventKeyV2(ev, policy.ApproveBindingTool)
	other, _ := approval.EventKeyObject(ev, policy.ApproveBindingTool, approval.Object{Type: "repo", Value: "Strazahq/site"})
	if rec.ArgvHash != want || rec.ArgvHash == bare || rec.ArgvHash == other {
		t.Errorf("ticket key = %q, want %q, which differs from the repo-less key and another repo's key", rec.ArgvHash, want)
	}

	// A role on every repo grants the same repo under the same key, so the
	// request lands on the same ticket whichever role grants it.
	every := nativeStruct(t, requestTicket(ctx, app, mustJSON(request(map[string]any{"repo": "Strazahq/pay-api"})), claims, policy.Subject{User: "kim", Roles: []string{"od-every"}}))
	if every["ref"] != res["ref"] {
		t.Errorf("the request through a role on every repo opened ticket %v, want the same ticket %v", every["ref"], res["ref"])
	}
}

// TestNativeApprovalRequestMissed pins that approval_request, a real
// request, asks for a listing when it names a repo the list does not hold,
// as the gateway does, so the refusal's promise to refresh the list holds.
func TestNativeApprovalRequestMissed(t *testing.T) {
	t.Parallel()
	app, _, _, up := objectsCheckApp(t, objectsCheckPolicy)
	ctx := context.Background()
	row, err := app.store.Apps().GetByName(ctx, "objects-demo")
	if err != nil {
		t.Fatal(err)
	}
	// The last listing is moved two minutes back, so a miss may list again
	// at once, while the next listing by schedule stays ten minutes away.
	ol := app.store.ObjectLists()
	now := time.Now().UTC()
	if err := ol.MarkDue(ctx, row.ID, now); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := ol.Claim(ctx, row.ID, "lane-e-test", now, now.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("claim the list = %v %v", ok, err)
	}
	if _, err := ol.Settle(ctx, row.ID, "lane-e-test", claimed.DeclHash, store.Listing{Outcome: store.ObjectsOK, At: now.Add(-2 * time.Minute), Due: now.Add(10 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	before := len(up.listingAuths())

	requester, err := app.store.Users().GetByUsername(ctx, "kim")
	if err != nil {
		t.Fatal(err)
	}
	res := requestTicket(ctx, app, mustJSON(map[string]any{"action": map[string]any{
		"tool": "mcp.call", "app": "objects-demo", "tool_name": "repo_read", "args": map[string]any{"repo": "Strazahq/new"},
	}}), authn.Claims{Session: "sess-miss", Subject: requester.ID}, policy.Subject{User: "kim", Roles: []string{"od-pay"}})
	if !res.IsError || !strings.HasPrefix(toolErrText(res), "Straza: repo_read on Strazahq/new was refused") {
		t.Fatalf("unlisted repo = %q, want the uncovered refusal", toolErrText(res))
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(up.listingAuths()) == before {
		if time.Now().After(deadline) {
			t.Fatal("the miss asked for no listing")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
