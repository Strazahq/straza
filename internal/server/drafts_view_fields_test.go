package server

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/strazahq/straza/internal/drafts"
)

// TestVerdictForCutsTheFieldsBesideTheWords pins the read cut of the gate
// fields of a gain and of the fields a finding carries beside its words.
// Each name below sits in one field alone, so the words of the verdict name
// none of them, and every reader kind of the other cut tests reads it, with
// three more that read a set without its roles, roles without the set, or
// one server without the other. A gate whose set or role the reader may
// not read is left out, and a line the cut reaches loses its rows, its
// rule, server and tools and binds_proposer. No name a reader may not read
// reaches the answer, and root and a full token read every field.
func TestVerdictForCutsTheFieldsBesideTheWords(t *testing.T) {
	t.Parallel()
	w := drafts.World{Apps: map[string]drafts.App{"github": {Name: "github"}, "jira": {Name: "jira"}},
		Roles:    map[string]drafts.Role{"github-readers": {Name: "github-readers", Owner: "github", Owned: true}, "sec-approvers": {Name: "sec-approvers"}},
		Policies: map[string]drafts.Policy{"dev-access": {Name: "dev-access"}}}
	d := drafts.Draft{ID: "41", Revision: 1, Authors: []drafts.Principal{{UserID: "pat", Via: laneLogin}},
		Items: []drafts.Item{{Kind: drafts.KindRole, Name: "github-readers", Op: drafts.OpPut}}}
	gain := drafts.Gain{Role: "github-readers", Server: "straza", Tool: "approval_request", HolderCount: 1, Before: drafts.OutcomeDenied, After: drafts.OutcomeApproval,
		BeforeWords: "denied by rule no-delete of dev-access", AfterWords: "a hold, up to 2 minutes, decided by sec-approvers",
		BeforeGate: &drafts.GainGate{Rule: "no-delete", Set: "dev-access", Roles: []string{}},
		AfterGate:  &drafts.GainGate{Rule: "hold-it", Set: "dev-access", Class: "hold", Seconds: 120, Roles: []string{"sec-approvers"}}}
	rows := drafts.Finding{Code: "access.ungated", Class: drafts.ClassRisk, Ack: drafts.AckTick, Object: "Role/github-readers",
		Sentence: "Two tools will run with no rule gating them.", Rows: []drafts.ToolRow{{Server: "github", Tool: "get_me"}, {Server: "jira", Tool: "create_issue"}}}
	local := drafts.Finding{Code: "policy.local-allow", Class: drafts.ClassRisk, Ack: drafts.AckTick, Object: "PolicySet/dev-access",
		Sentence: "A rule allows the tools of a server Straza has not registered.", Rule: "allow-chrome", Server: "claude-in-chrome", Tools: []string{"navigate"}}
	guard := drafts.Finding{Code: "policy.guardrail-changed", Class: drafts.ClassRisk, Ack: drafts.AckTick, Object: "PolicySet/dev-access",
		Sentence: "A policy will stop gating a tool.", BindsProposer: true}
	nothing := drafts.Finding{Code: "ready.rule-names-nothing", Class: drafts.ClassWarning, Object: "PolicySet/dev-access",
		Sentence: "A rule names a tool its server does not offer.", Fix: "Fix the name.", Rule: "d", Server: "jira", Tools: []string{"get_you"}}
	v := drafts.Verdict{Draft: "41", Revision: 1, Risks: []drafts.Finding{rows, local, guard}, Warnings: []drafts.Finding{nothing}, Gains: []drafts.Gain{gain}}
	every := []string{"github", "jira", "get_me", "create_issue", "dev-access", "no-delete", "hold-it", "sec-approvers", "allow-chrome", "claude-in-chrome", "navigate", "get_you"}
	type kept struct{ row, before, after, rows, local, guard, nothing bool }
	all := kept{true, true, true, true, true, true, true}
	for _, tc := range []struct {
		name   string
		c      draftCaller
		want   kept
		absent []string
	}{
		{"root", rootCaller("kim"), all, nil},
		{"a full token", adminAPICaller("ci", "drafts:read", "apps:read", "identity:read"), all, nil},
		{"drafts:read and apps:read, who reads no role holder", adminAPICaller("ci", "drafts:read", "apps:read"),
			kept{row: true, before: true, after: true, rows: true, local: true, nothing: true}, nil},
		{"the global MCP admin, who reads no set and no role of its own", personCaller("max", "apps:read", "apps:write"), kept{row: true, rows: true},
			[]string{"dev-access", "no-delete", "hold-it", "sec-approvers", "allow-chrome", "claude-in-chrome", "navigate", "get_you"}},
		{"github's server admin", administering(personCaller("erin"), map[string]string{"a1": "github"}), kept{row: true},
			[]string{"jira", "create_issue", "dev-access", "no-delete", "hold-it", "sec-approvers", "allow-chrome", "claude-in-chrome", "navigate", "get_you"}},
		{"drafts:read and identity:read without apps:read", adminAPICaller("ci", "drafts:read", "identity:read"), kept{row: true, before: true, after: true, local: true, guard: true},
			[]string{"github", "jira", "get_me", "create_issue", "get_you"}},
		{"identity:write", personCaller("ida", "identity:write"), kept{}, append([]string{"github-readers"}, every...)},
		{"the proposer, a set's reader who reads no approver role", personCaller("pat", "policy:read", "apps:read"), kept{row: true, before: true, rows: true, local: true, guard: true, nothing: true},
			[]string{"sec-approvers"}},
		{"a role's reader who reads no set", personCaller("quinn", "identity:read", "apps:read"), kept{row: true, after: true, rows: true},
			[]string{"dev-access", "no-delete", "hold-it", "allow-chrome", "claude-in-chrome", "navigate", "get_you"}},
		{"a set's reader who administers github alone", administering(personCaller("ron", "policy:read"), map[string]string{"a1": "github"}),
			kept{row: true, before: true, local: true}, []string{"sec-approvers", "jira", "create_issue", "get_you"}},
		{"an agent without apps:read", agentCaller("bot", "drafts:read"), kept{local: true},
			[]string{"github", "jira", "get_me", "create_issue", "no-delete", "sec-approvers", "get_you"}},
	} {
		got := verdictFor(tc.c, w, d, v)
		var seen kept
		for _, g := range got.Gains {
			seen.row, seen.before, seen.after = true, g.BeforeGate != nil, g.AfterGate != nil
		}
		for _, f := range append(append([]findingPayload{}, got.Risks...), got.Warnings...) {
			switch f.Code {
			case rows.Code:
				seen.rows = len(f.Rows) > 0
			case local.Code:
				seen.local = f.Rule != "" || f.Server != "" || len(f.Tools) > 0
			case guard.Code:
				seen.guard = f.BindsProposer
			case nothing.Code:
				seen.nothing = f.Rule != "" || f.Server != "" || len(f.Tools) > 0
			}
		}
		if seen != tc.want {
			t.Errorf("%s reads the fields %+v, want %+v", tc.name, seen, tc.want)
		}
		body, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range tc.absent {
			if namesServer(string(body), []string{name}) {
				t.Errorf("%s reads the name %s: %s", tc.name, name, body)
			}
		}
		if tc.want == all && !reflect.DeepEqual(got, keyedAll(v)) {
			t.Errorf("%s reads %+v, want every field", tc.name, got)
		}
	}
	// An approval's words name no set, so a reader of its roles who reads no
	// set keeps them, and the gate without its rule and set. A denial's
	// words name both, so that side leaves whole.
	side := verdictFor(personCaller("quinn", "identity:read", "apps:read"), w, d, v).Gains[0]
	if side.AfterWords != gain.AfterWords || side.AfterGate == nil || side.AfterGate.Rule != "" || side.AfterGate.Set != "" || !slices.Equal(side.AfterGate.Roles, gain.AfterGate.Roles) {
		t.Errorf("a role's reader reads the approval %q %+v, want its words and its gate without rule and set", side.AfterWords, side.AfterGate)
	}
	if side.BeforeWords != "" || side.BeforeGate != nil {
		t.Errorf("a role's reader reads the denial %q %+v, want neither", side.BeforeWords, side.BeforeGate)
	}
	if gain.AfterGate.Set != "dev-access" || gain.AfterGate.Rule != "hold-it" {
		t.Errorf("the cut changed the verdict's own gate: %+v", gain.AfterGate)
	}
}

// TestCutFindingDropsTheFieldsBesideTheWords pins that cutFinding reads the
// fields beside a line's words as it reads the words: a server named in its
// rows or its server field, or a set or role named in its rule or tools,
// cuts the line, and a cut line carries none of those fields.
func TestCutFindingDropsTheFieldsBesideTheWords(t *testing.T) {
	t.Parallel()
	base := drafts.Finding{Code: "policy.local-allow", Class: drafts.ClassRisk, Ack: drafts.AckTick, Object: "PolicySet/guard", Sentence: "A rule allows a tool.", BindsProposer: true}
	with := func(edit func(*drafts.Finding)) drafts.Finding {
		f := base
		edit(&f)
		return f
	}
	cut := func(sentence string) drafts.Finding {
		return drafts.Finding{Code: base.Code, Class: base.Class, Ack: base.Ack, Object: base.Object, Sentence: sentence}
	}
	for _, tc := range []struct {
		name   string
		f      drafts.Finding
		hidden []string
		want   drafts.Finding
	}{
		{"fields that name nothing the reader may not read", with(func(f *drafts.Finding) {
			f.Rows, f.Rule, f.Server, f.Tools = []drafts.ToolRow{{Server: "github", Tool: "get_me"}}, "r", "github", []string{"get_me"}
		}), nil, with(func(f *drafts.Finding) {
			f.Rows, f.Rule, f.Server, f.Tools = []drafts.ToolRow{{Server: "github", Tool: "get_me"}}, "r", "github", []string{"get_me"}
		})},
		{"a row on such a server", with(func(f *drafts.Finding) {
			f.Rows = []drafts.ToolRow{{Server: "github", Tool: "a"}, {Server: "jira", Tool: "b"}}
		}), nil, cut(cutLineWords)},
		{"the server field", with(func(f *drafts.Finding) { f.Rule, f.Server = "r", "jira" }), nil, cut(cutLineWords)},
		{"a rule named as such a set", with(func(f *drafts.Finding) { f.Rule = "payroll" }), []string{"payroll"}, cut(objectLineWords)},
		{"a tool named as such a role", with(func(f *drafts.Finding) { f.Tools = []string{"payroll"} }), []string{"payroll"}, cut(objectLineWords)},
		{"a line cut by its object alone", base, []string{"guard"}, drafts.Finding{Code: base.Code, Class: base.Class, Ack: base.Ack, Sentence: objectLineWords}},
	} {
		if got := cutFinding(tc.f, []string{"jira"}, tc.hidden); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
}
