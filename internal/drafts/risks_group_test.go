package drafts

import (
	"reflect"
	"testing"
)

// TestRiskGroups pins Finding.Group, table-driven over the codes: a risk
// carries its code, except a server's removal and an agent's loosening of
// a set that binds it, which a publisher confirms one by one by name, and,
// on an agent's draft, the policy.guardrail-changed lines of such a set,
// typed by the set's name beside its agent.guardrail line. On a person's
// draft, and for a set that does not bind the agent, those lines keep
// their group. No other class carries a group, and a group is no part of a
// risk's key.
func TestRiskGroups(t *testing.T) {
	t.Parallel()
	removeSet := func(name string) Item { return Item{Kind: KindPolicySet, Name: name, Op: OpRemove} }
	cases := []struct {
		name  string
		agent bool
		items []Item
		want  map[string]string
	}{
		{"a person's draft", false, []Item{removeSet("agent-guardrails"), {Kind: KindApp, Name: "jira", Op: OpRemove},
			gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: [get_me, push_files]\n")},
			map[string]string{codeGuardrail: codeGuardrail, codeRemoval: "", codeUngated: codeUngated}},
		{"an agent's draft that loosens a set that binds it", true, []Item{removeSet("agent-guardrails")},
			map[string]string{codeAgentGuardrail: "", codeGuardrail: ""}},
		{"an agent's draft that loosens a set that does not bind it", true, []Item{removeSet("readers-guard")},
			map[string]string{codeGuardrail: codeGuardrail}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := withSet(guardWorld(), "readers-guard", gainSet("readers-guard", "{ roles: [readers] }", holdShell))
			d, in := stamped(w, tc.items...), CheckInput{Proposer: Holder{Username: "erin", UserType: "human"}, Now: checkNow}
			if tc.agent {
				d, in.Proposer = agentDraftOf(w, tc.items...), ciBot
			}
			v := Check(w, d, in)
			if len(v.Refused) > 0 {
				t.Fatalf("refused: %q", findingLines(v.Refused))
			}
			got := map[string]string{}
			for _, f := range v.Risks {
				if g, seen := got[f.Code]; seen && g != f.Group {
					t.Errorf("%s carries the groups %q and %q", f.Code, g, f.Group)
				}
				got[f.Code] = f.Group
				bare := f
				bare.Group = ""
				if bare.Key() != f.Key() {
					t.Errorf("the group of %s moves its key", f.Code)
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("groups by code\n got %v\nwant %v", got, tc.want)
			}
			for _, list := range [][]Finding{v.Warnings, v.Unchecked, v.Passed, v.Info} {
				for _, f := range list {
					if f.Group != "" {
						t.Errorf("%s, a %s line, carries the group %q", f.Code, f.Class, f.Group)
					}
				}
			}
		})
	}
}
