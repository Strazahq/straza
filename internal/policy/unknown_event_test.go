package policy

import (
	"strings"
	"testing"
)

// TestUnknownKindAndToolFailClosed pins the fail-closed rule for unknown
// input on the engine: an event kind outside the canonical set is denied
// in both profiles
// with a sentence that lists the known kinds, and a blocking event whose tool
// no rule matches takes the profile's local default whatever the tool is,
// including a tool outside the canonical set and no tool at all. Where that
// default denies such a tool, the sentence lists the known tools, because no
// rule can name it. The controls keep the canonical defaults as they were.
func TestUnknownKindAndToolFailClosed(t *testing.T) {
	doc, err := Parse([]byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: agent-guardrails }
spec:
  rules:
    - id: no-rm-rf
      tools: [shell.exec]
      command: { denyPatterns: ["rm -rf *"] }
      effect: deny
      reason: "destructive command denied"
`))
	if err != nil {
		t.Fatal(err)
	}
	const (
		unknownKind = `Straza: denied, because the event kind "lf-gi.made-up" is not one Straza knows and policy cannot judge it. ` +
			"Send one of the known kinds: compact.pre, permission.request, prompt.submit, session.end, session.start, subagent.start, subagent.stop, tool.post, tool.pre."
		sendTools  = "Send one of the known tools: file.edit, file.read, file.write, mcp.call, net.fetch, other, shell.exec, task.spawn."
		madeUpTool = `Straza: denied, because the tool "lf-gi-made-up-tool" is not one Straza knows and policy cannot judge it. ` + sendTools
		noTool     = "Straza: denied, because this tool.pre event names no tool and policy cannot judge it. " + sendTools
		otherTool  = "Straza: other is not permitted by default in this profile. Ask an admin for a policy rule that allows it."
	)
	rm := "rm -rf /tmp/lf-gi-probe"
	cases := []struct {
		name        string
		profile     string
		ev          Event
		wantEffect  string
		wantDefault bool
		wantRule    string
		wantReason  string
	}{
		{name: "unknown kind denied under enterprise", profile: EffectDeny, ev: Event{Kind: "lf-gi.made-up", Tool: ToolShellExec, Command: rm}, wantEffect: EffectDeny, wantReason: unknownKind},
		{name: "unknown kind denied under standalone", profile: EffectAllow, ev: Event{Kind: "lf-gi.made-up", Tool: ToolShellExec, Command: rm}, wantEffect: EffectDeny, wantReason: unknownKind},
		{name: "unknown kind denied with no tool", profile: EffectAllow, ev: Event{Kind: "lf-gi.made-up"}, wantEffect: EffectDeny, wantReason: unknownKind},
		{name: "unknown tool under tool.pre takes the enterprise deny", profile: EffectDeny, ev: Event{Kind: EventToolPre, Tool: "lf-gi-made-up-tool", Command: rm}, wantEffect: EffectDeny, wantDefault: true, wantReason: madeUpTool},
		{name: "unknown tool under permission.request takes the enterprise deny", profile: EffectDeny, ev: Event{Kind: EventPermissionRequest, Tool: "lf-gi-made-up-tool"}, wantEffect: EffectDeny, wantDefault: true, wantReason: madeUpTool},
		{name: "unknown tool under tool.pre takes the standalone allow", profile: EffectAllow, ev: Event{Kind: EventToolPre, Tool: "lf-gi-made-up-tool", Command: rm}, wantEffect: EffectAllow, wantDefault: true},
		{name: "no tool under tool.pre takes the enterprise deny", profile: EffectDeny, ev: Event{Kind: EventToolPre, Command: rm}, wantEffect: EffectDeny, wantDefault: true, wantReason: noTool},
		{name: "no tool under tool.pre takes the standalone allow", profile: EffectAllow, ev: Event{Kind: EventToolPre}, wantEffect: EffectAllow, wantDefault: true},
		{name: "control: rule denies shell.exec", profile: EffectAllow, ev: Event{Kind: EventToolPre, Tool: ToolShellExec, Command: rm}, wantEffect: EffectDeny, wantRule: "no-rm-rf", wantReason: "destructive command denied"},
		{name: "control: other takes the enterprise deny", profile: EffectDeny, ev: Event{Kind: EventToolPre, Tool: ToolOther}, wantEffect: EffectDeny, wantDefault: true, wantReason: otherTool},
		{name: "control: shell.exec takes the standalone allow", profile: EffectAllow, ev: Event{Kind: EventToolPre, Tool: ToolShellExec, Command: "ls"}, wantEffect: EffectAllow, wantDefault: true},
		{name: "control: known observe kind allows under enterprise", profile: EffectDeny, ev: Event{Kind: EventSessionEnd}, wantEffect: EffectAllow, wantDefault: true},
		{name: "control: known observe kind with an unknown tool allows", profile: EffectDeny, ev: Event{Kind: EventToolPost, Tool: "lf-gi-made-up-tool"}, wantEffect: EffectAllow, wantDefault: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, err := NewEngine([]Document{doc}, tc.profile)
			if err != nil {
				t.Fatal(err)
			}
			d := eng.Evaluate(tc.ev, Subject{User: "sam", Attestation: "none"})
			if d.Effect != tc.wantEffect || d.Default != tc.wantDefault || d.RuleID != tc.wantRule {
				t.Fatalf("decision = %+v, want effect %s default %v rule %q", d, tc.wantEffect, tc.wantDefault, tc.wantRule)
			}
			if d.Reason != tc.wantReason {
				t.Errorf("reason\n got %q\nwant %q", d.Reason, tc.wantReason)
			}
		})
	}
}

// TestUnknownSentencesListParseVocabulary pins that the kinds and tools the
// unknown-kind and unknown-tool sentences offer are the ones a rule may name,
// so a sentence and the parser can never drift apart.
func TestUnknownSentencesListParseVocabulary(t *testing.T) {
	eng, err := NewEngine(nil, EffectDeny)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		ev     Event
		marker string
		vocab  map[string]bool
	}{
		{name: "unknown kind", ev: Event{Kind: "x"}, marker: "Send one of the known kinds: ", vocab: knownEvents},
		{name: "unknown tool", ev: Event{Kind: EventToolPre, Tool: "x"}, marker: "Send one of the known tools: ", vocab: knownTools},
		{name: "no tool", ev: Event{Kind: EventPermissionRequest}, marker: "Send one of the known tools: ", vocab: knownTools},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := eng.Evaluate(tc.ev, Subject{}).Reason
			_, list, ok := strings.Cut(reason, tc.marker)
			if !ok {
				t.Fatalf("reason %q offers no list after %q", reason, tc.marker)
			}
			offered := strings.Split(strings.TrimSuffix(list, "."), ", ")
			if len(offered) != len(tc.vocab) {
				t.Fatalf("offered %d values %v, the parser knows %d", len(offered), offered, len(tc.vocab))
			}
			for _, v := range offered {
				if !tc.vocab[v] {
					t.Errorf("offered value %q is not in the parse vocabulary", v)
				}
			}
		})
	}
}
