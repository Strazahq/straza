package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestPolicySimulateAccessCheck pins that Test a call runs the access check
// before the engine (spec/policyset revision 17): a granted tool with no
// rule reads allow, an ungranted one reads deny, a gated one reads the
// approve marker, and a body that claims granted for an ungranted tool
// still reads deny.
func TestPolicySimulateAccessCheck(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	simApp, err := app.store.Apps().Create(ctx, store.App{Name: "simapp", Version: "1", RuntimeKind: "remote", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	devRole, err := app.store.Roles().GetByName(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{
		RoleID: devRole.ID, AppID: simApp.ID, ToolMatcher: `["echo","gate"]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	if code := yamlReq(t, http.MethodPut, base+"/v1/admin/policies", adminTok, `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: sim-gates}
spec:
  match: {roles: [dev]}
  rules:
    - id: gate-needs-human
      tools: [mcp.call]
      apps: [simapp]
      toolNames: {allow: ["gate"]}
      effect: allow
      mode: approve
      approve: {deciders: [sponsor], timeoutSeconds: 45}
`, nil); code != http.StatusCreated {
		t.Fatalf("apply = %d", code)
	}
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/policies/sim-gates/activate",
		adminTok, map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}

	cases := []struct {
		name        string
		tool        string
		claim       bool
		wantEffect  string
		wantDefault bool
		wantRule    string
		wantApprove bool
	}{
		{name: "granted no rule allows", tool: "echo", wantEffect: "allow", wantDefault: true},
		{name: "ungranted denies", tool: "env", wantEffect: "deny", wantDefault: true},
		{name: "granted approve rule gates", tool: "gate", wantEffect: "allow", wantRule: "gate-needs-human", wantApprove: true},
		{name: "client claim of granted is ignored", tool: "env", claim: true, wantEffect: "deny", wantDefault: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := map[string]any{"kind": "tool.pre", "tool": "mcp.call", "app": "simapp", "toolName": tc.tool}
			if tc.claim {
				event["granted"] = true
			}
			var res struct {
				Active struct {
					Effect  string         `json:"effect"`
					RuleID  string         `json:"ruleId"`
					Default bool           `json:"default"`
					Approve map[string]any `json:"approve"`
				} `json:"active"`
			}
			req := map[string]any{"event": event, "subject": map[string]any{"roles": []string{"dev"}}}
			if code := adminReq(t, http.MethodPost, base+"/v1/admin/policies/simulate", adminTok, req, &res); code != http.StatusOK {
				t.Fatalf("simulate = %d", code)
			}
			a := res.Active
			if a.Effect != tc.wantEffect || a.Default != tc.wantDefault || a.RuleID != tc.wantRule || (a.Approve != nil) != tc.wantApprove {
				t.Errorf("active = %+v, want effect %s default %v rule %q approve %v", a, tc.wantEffect, tc.wantDefault, tc.wantRule, tc.wantApprove)
			}
		})
	}
}
