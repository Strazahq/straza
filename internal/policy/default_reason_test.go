package policy

import "testing"

// TestDefaultDenyReasonsNameRemedy pins the §3.3 default-deny reasons: a
// default deny must name the human's next step, not only what happened.
// Engine strings know no deployment URL by design, so the remedy names the
// concept (who to ask and for what: a role binding or an allow rule), never
// a surface the deployment may not have.
func TestDefaultDenyReasonsNameRemedy(t *testing.T) {
	deny, err := NewEngine(nil, EffectDeny)
	if err != nil {
		t.Fatalf("NewEngine(deny): %v", err)
	}
	allow, err := NewEngine(nil, EffectAllow)
	if err != nil {
		t.Fatalf("NewEngine(allow): %v", err)
	}
	sub := Subject{User: "kim"}

	d := deny.Evaluate(Event{Kind: EventToolPre, Tool: ToolMCPCall, App: "demo-tools", ToolName: "echo"}, sub)
	if d.Effect != EffectDeny || !d.Default {
		t.Fatalf("mcp default = %+v, want default deny", d)
	}
	want := "Straza: no role of yours has access to MCP tool demo-tools/echo. Ask an admin to give a role you hold access to it, or to allow it by policy."
	if d.Reason != want {
		t.Errorf("mcp default reason\n got %q\nwant %q", d.Reason, want)
	}

	d = deny.Evaluate(Event{Kind: EventToolPre, Tool: ToolShellExec, Command: "ls"}, sub)
	if d.Effect != EffectDeny || !d.Default {
		t.Fatalf("local default = %+v, want default deny", d)
	}
	want = "Straza: shell.exec is not permitted by default in this profile. Ask an admin for a policy rule that allows it."
	if d.Reason != want {
		t.Errorf("local default reason\n got %q\nwant %q", d.Reason, want)
	}

	// An unclassified harness tool names itself and the exact rule that
	// allows it, and one that arrives without a name says why no rule can.
	for _, tc := range []struct {
		name string
		ev   Event
		want string
	}{
		{"named", Event{Kind: EventToolPre, Tool: ToolOther, ToolName: "SubagentHandback"},
			"Straza: SubagentHandback is a harness tool Straza does not classify, so this profile denies it. " +
				"To allow it, an admin adds a policy rule with tools: [other] and toolNames: {allow: [SubagentHandback]}."},
		{"nameless", Event{Kind: EventToolPre, Tool: ToolOther},
			"Straza: this harness tool is one Straza does not classify, and the client sent no tool name, so no rule can allow it. " +
				"Update the Straza client or the agent kit on this machine, then ask an admin to allow the tool by name."},
	} {
		d = deny.Evaluate(tc.ev, sub)
		if d.Effect != EffectDeny || !d.Default {
			t.Fatalf("%s other default = %+v, want default deny", tc.name, d)
		}
		if d.Reason != tc.want {
			t.Errorf("%s other default reason\n got %q\nwant %q", tc.name, d.Reason, tc.want)
		}
	}

	// The allow-profile arm is reason-less by design: no remedy belongs on a
	// decision that lets the call through.
	d = allow.Evaluate(Event{Kind: EventToolPre, Tool: ToolShellExec, Command: "ls"}, sub)
	if d.Effect != EffectAllow || d.Reason != "" {
		t.Fatalf("allow-profile local default = %+v, want reason-less allow", d)
	}
}
