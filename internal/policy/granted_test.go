package policy

import "testing"

// TestGrantedFactDefault pins spec/policyset revision 17: an mcp.call that
// carries the granted fact allows by default with its own reason, an
// ungranted one keeps the default deny with the remedy sentence, rules that
// fire win over the fact in both directions, and the fact changes nothing
// for local tools.
func TestGrantedFactDefault(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: scout-role-access }
spec:
  match: { roles: [scout-role] }
  rules:
    - id: deny-get-env
      tools: [mcp.call]
      apps: [scout-tools]
      toolNames: { deny: ["get-env"] }
      effect: deny
      reason: "get-env dumps the environment"
    - id: approve-get-sum
      tools: [mcp.call]
      apps: [scout-tools]
      toolNames: { allow: ["get-sum"] }
      effect: allow
      mode: approve
      approve: { deciders: [sponsor], timeoutSeconds: 90 }
`))
	if err != nil {
		t.Fatal(err)
	}
	sub := Subject{User: "joe", Roles: []string{"scout-role"}}
	mcp := func(tool string, granted bool) Event {
		return Event{Kind: EventToolPre, Tool: ToolMCPCall, App: "scout-tools", ToolName: tool, Granted: granted}
	}
	const grantedReason = "allowed by role access. No policy rule gates this tool."
	const ungrantedReason = "Straza: no role of yours has access to MCP tool scout-tools/echo. Ask an admin to give a role you hold access to it, or to allow it by policy."

	cases := []struct {
		name        string
		profile     string
		ev          Event
		wantEffect  string
		wantDefault bool
		wantRule    string
		wantReason  string
		wantApprove bool
	}{
		{name: "granted no rule allows", profile: EffectAllow, ev: mcp("echo", true), wantEffect: EffectAllow, wantDefault: true, wantReason: grantedReason},
		{name: "granted no rule allows in enterprise", profile: EffectDeny, ev: mcp("echo", true), wantEffect: EffectAllow, wantDefault: true, wantReason: grantedReason},
		{name: "ungranted no rule denies", profile: EffectAllow, ev: mcp("echo", false), wantEffect: EffectDeny, wantDefault: true, wantReason: ungrantedReason},
		{name: "granted deny rule refuses", profile: EffectAllow, ev: mcp("get-env", true), wantEffect: EffectDeny, wantRule: "deny-get-env", wantReason: "get-env dumps the environment"},
		{name: "granted approve rule gates", profile: EffectAllow, ev: mcp("get-sum", true), wantEffect: EffectAllow, wantRule: "approve-get-sum", wantApprove: true},
		{name: "ungranted deny rule refuses by rule", profile: EffectAllow, ev: mcp("get-env", false), wantEffect: EffectDeny, wantRule: "deny-get-env", wantReason: "get-env dumps the environment"},
		{name: "granted local tool keeps profile default", profile: EffectDeny, ev: Event{Kind: EventToolPre, Tool: ToolShellExec, Command: "ls", Granted: true}, wantEffect: EffectDeny, wantDefault: true, wantReason: "Straza: shell.exec is not permitted by default in this profile. Ask an admin for a policy rule that allows it."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, err := NewEngine([]Document{doc}, tc.profile)
			if err != nil {
				t.Fatal(err)
			}
			d := eng.Evaluate(tc.ev, sub)
			if d.Effect != tc.wantEffect || d.Default != tc.wantDefault || d.RuleID != tc.wantRule {
				t.Fatalf("decision = %+v, want effect %s default %v rule %q", d, tc.wantEffect, tc.wantDefault, tc.wantRule)
			}
			if d.Reason != tc.wantReason {
				t.Errorf("reason\n got %q\nwant %q", d.Reason, tc.wantReason)
			}
			if (d.Approve != nil) != tc.wantApprove {
				t.Errorf("approve = %v, want %v", d.Approve != nil, tc.wantApprove)
			}
		})
	}
}
