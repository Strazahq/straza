package main

import (
	"reflect"
	"testing"

	"github.com/strazahq/straza/internal/ctl"
)

// The cases of the console's confirm blocks suite
// (web/ui/src/lib/drafts-model.test.tsx), so both surfaces group alike: a
// server removal typed by its name, three loosened sets typed by their
// count and two roles that reach more of an invented object type under one
// tick.
var (
	blockRemoval = ctl.DraftFinding{Key: "k-rm", Code: "server.removal", Ack: "typed", Object: "App/legacy-desk", Typed: "legacy-desk"}
	blockSets    = []ctl.DraftFinding{blockSet("a"), blockSet("b"), blockSet("c")}
	blockWider   = []ctl.DraftFinding{blockWide("x"), blockWide("y")}
)

// blockSet is the loosened set s-access, typed in the group of its code.
func blockSet(s string) ctl.DraftFinding {
	return ctl.DraftFinding{Key: "k-set-" + s, Code: "policy.guardrail-changed", Ack: "typed", Object: "PolicySet/" + s + "-access",
		Typed: s + "-access", Group: "policy.guardrail-changed"}
}

// blockWide is the held role desk-r reaching more tickets, a tick in the
// group of its code.
func blockWide(r string) ctl.DraftFinding {
	return ctl.DraftFinding{Key: "k-wide-" + r, Code: "role.covers-wider", Ack: "tick", Object: "Role/desk-" + r, Group: "role.covers-wider",
		Sentence: "desk-" + r + " will reach more tickets on helpdesk: T-2."}
}

// blockShape is what a test reads of a block.
type blockShape struct {
	kind, expect string
	keys         []string
	blocked      bool
}

// TestConfirmBlocks pins the grouping rule the console's confirmBlocks
// keeps, case for case: names first, then counts, then ticks; a group of
// one typed by its name; a verdict with no groups read line by line; and a
// group with a line cut for the reader kept whole and blocked.
func TestConfirmBlocks(t *testing.T) {
	cut := blockSet("c")
	cut.Typed, cut.Object = "", ""
	tests := []struct {
		name  string
		risks []ctl.DraftFinding
		want  []blockShape
	}{
		{
			name:  "a removal by its name, a group by its count and a group of ticks once, names first",
			risks: []ctl.DraftFinding{blockWider[0], blockSets[0], blockSets[1], blockSets[2], blockRemoval, blockWider[1]},
			want: []blockShape{
				{blockName, "legacy-desk", []string{"k-rm"}, false},
				{blockCount, "3", []string{"k-set-a", "k-set-b", "k-set-c"}, false},
				{blockTick, "", []string{"k-wide-x", "k-wide-y"}, false},
			},
		},
		{
			name: "a removal and an agent's loosening of a set with the same name stay two blocks",
			risks: []ctl.DraftFinding{
				{Key: "k-rm-desk", Code: "server.removal", Ack: "typed", Object: "App/desk", Typed: "desk"},
				{Key: "k-loosen-desk", Code: agentLoosenCode, Ack: "typed", Object: "PolicySet/desk", Typed: "desk"},
			},
			want: []blockShape{{blockName, "desk", []string{"k-rm-desk"}, false}, {blockName, "desk", []string{"k-loosen-desk"}, false}},
		},
		{
			name: "an agent's loosening and the gates its set loses share the set's name",
			risks: []ctl.DraftFinding{
				{Key: "k-agent", Code: agentLoosenCode, Ack: "typed", Object: "PolicySet/agent-guardrails", Typed: "agent-guardrails"},
				{Key: "k-gate", Code: "policy.guardrail-changed", Ack: "typed", Object: "PolicySet/agent-guardrails", Typed: "Agent-Guardrails "},
			},
			want: []blockShape{{blockName, "agent-guardrails", []string{"k-agent", "k-gate"}, false}},
		},
		{
			name:  "a group of one line is typed by that line's name",
			risks: []ctl.DraftFinding{blockSets[1]},
			want:  []blockShape{{blockName, "b-access", []string{"k-set-b"}, false}},
		},
		{
			name: "a verdict whose lines carry no group reads line by line",
			risks: []ctl.DraftFinding{
				{Key: "k-host", Code: "host.new", Ack: "typed", Object: "App/github", Typed: "api.githubcopilot.com"},
				{Key: "k-reach", Code: "reach.new", Ack: "tick", Object: "Role/github-readers"},
				{Key: "k-reach-2", Code: "reach.new", Ack: "tick", Object: "Role/github-writers"},
			},
			want: []blockShape{{blockName, "api.githubcopilot.com", []string{"k-host"}, false}, {blockTick, "", []string{"k-reach"}, false}, {blockTick, "", []string{"k-reach-2"}, false}},
		},
		{
			name:  "a group with a line cut for the reader stays whole and is blocked",
			risks: []ctl.DraftFinding{blockSets[0], blockSets[1], cut},
			want:  []blockShape{{blockCount, "3", []string{"k-set-a", "k-set-b", "k-set-c"}, true}},
		},
		{
			name:  "a typed line cut for the reader stands alone and is blocked",
			risks: []ctl.DraftFinding{{Key: "k-cut", Code: "server.runs-code", Ack: "typed"}},
			want:  []blockShape{{blockName, "", []string{"k-cut"}, true}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []blockShape
			for _, b := range confirmBlocks(tc.risks) {
				var keys []string
				for _, f := range b.lines {
					keys = append(keys, f.Key)
				}
				got = append(got, blockShape{b.kind, b.expect, keys, b.blocked})
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("blocks = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestBlockHint pins what show prints once under a block's lines: what a
// person types when they publish the draft in the console, why a reader
// cannot confirm a blocked block, and nothing for a tick.
func TestBlockHint(t *testing.T) {
	loosen := []ctl.DraftFinding{
		{Key: "k-agent", Code: agentLoosenCode, Ack: "typed", Object: "PolicySet/agent-guardrails", Typed: "agent-guardrails"},
		{Key: "k-gate", Code: "policy.guardrail-changed", Ack: "typed", Object: "PolicySet/agent-guardrails", Typed: "agent-guardrails"},
	}
	twice := []ctl.DraftFinding{blockRemoval, {Key: "k-rm-2", Code: "server.runs-code", Ack: "typed", Object: "App/legacy-desk", Typed: "legacy-desk"}}
	tests := []struct {
		name  string
		risks []ctl.DraftFinding
		want  string
	}{
		{"a name", []ctl.DraftFinding{blockRemoval}, "Type legacy-desk to confirm it when you publish the draft in the console under Drafts."},
		{"two lines typed by one name", twice, "Type legacy-desk to confirm them when you publish the draft in the console under Drafts."},
		{"a count", blockSets, "Type 3 to confirm them when you publish the draft in the console under Drafts."},
		{"an agent's loosening", loosen, "Type agent-guardrails to accept it when you publish the draft in the console under Drafts."},
		{"a line cut for the reader", []ctl.DraftFinding{{Key: "k-cut", Ack: "typed"}}, "Only someone who can read what this line names can confirm it, so ask an admin who can read it to publish this draft."},
		{"a tick", blockWider, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := confirmBlocks(tc.risks)[0].hint(); got != tc.want {
				t.Errorf("hint = %q, want %q", got, tc.want)
			}
		})
	}
}
