package server

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/drafts"
)

// The words of a line verdictFor cut for its reader: one that names a
// server it may not read, and one that names another object it may not
// read.
const (
	cutLineWords    = "This line names a tool on a server you cannot read, so this view leaves its words out. Ask an administrator for the scope apps:read to see it."
	cutPartWords    = "a tool on a server you cannot read"
	objectLineWords = "This line names an object you cannot read, so this view leaves its words out. Ask an administrator for the grant that reads it."
	objectPartWords = "an object you cannot read"
)

// keyedOne is f as a route answers it to a reader who may read all of it.
func keyedOne(f drafts.Finding) findingPayload { return findingPayload{Finding: f, Key: f.Key()} }

// keyedAll is v as a route answers it to a reader who may read every line,
// each finding with its own key.
func keyedAll(v drafts.Verdict) verdictPayload {
	keys := func(fs []drafts.Finding) []findingPayload {
		out := []findingPayload{}
		for _, f := range fs {
			out = append(out, keyedOne(f))
		}
		return out
	}
	return verdictPayload{Draft: v.Draft, Revision: v.Revision, Snapshot: v.Snapshot, CheckedAt: v.CheckedAt,
		Refused: keys(v.Refused), Risks: keys(v.Risks), Warnings: keys(v.Warnings), Unchecked: keys(v.Unchecked),
		Passed: keys(v.Passed), Info: keys(v.Info), Gains: v.Gains, Needs: v.Needs, RiskDigest: v.RiskDigest}
}

// TestVerdictFor pins the one cut of every verdict a route answers:
// holder names only for root and identity:read, counts for
// everyone, gain rows only on servers the caller may read with the scope
// apps:read or the server's admin role, with one info line that counts the
// rows left out and names no server, the built-in straza app's rows for
// everyone, the words of every risk, warning, unchecked and info line that
// names a server the caller may not read cut while its code, class, ack and
// key stay, the same cut for a line on a role or a set a caller without root
// or drafts:read may not read, which also loses every gain row of such a
// role, counted in words that name roles, and the agent's view for an
// agent, whatever its grants.
func TestVerdictFor(t *testing.T) {
	t.Parallel()
	github := drafts.Gain{Role: "dev", Server: "github", Tool: "push_files", Holders: []string{"alice", "bob"}, HolderCount: 2, Before: "denied", After: "runs"}
	jira := drafts.Gain{Role: "dev", Server: "jira", Tool: "delete_project", Holders: []string{"alice"}, HolderCount: 1, Before: "not-reachable", After: "runs"}
	native := drafts.Gain{Role: "dev", Server: "straza", Tool: "approval_request", Holders: []string{"alice"}, HolderCount: 1, Before: "denied", After: "needs-approval"}
	line := func(code string, class drafts.Class, object, sentence string) drafts.Finding {
		return drafts.Finding{Code: code, Class: class, Object: object, Sentence: sentence}
	}
	stale := line("draft.stale", drafts.ClassRefused, "Role/dev", "Role/dev changed after this draft was checked, when bob published draft 40 at 2026-09-24 07:05 UTC.")
	parse := line("app.parse", drafts.ClassRefused, "App/jira", "App/jira holds no server document: yaml: line 3.")
	ghRisk := drafts.Finding{Code: "access.ungated", Class: drafts.ClassRisk, Ack: drafts.AckTyped, Object: "Role/dev", Typed: "dev",
		Sentence: "push_files on github will run for holders of dev with no rule gating it, where today denied by rule r of s.",
		Before:   "github/push_files=denied by rule r of s", After: "github/push_files"}
	jiraRisk := drafts.Finding{Code: "server.runs-code", Class: drafts.ClassRisk, Ack: drafts.AckTyped, Object: "App/jira", Typed: "jira",
		Sentence: "Publishing starts /usr/bin/jira-mcp on the Straza host as Straza's own user, now and after every restart.", After: "command /usr/bin/jira-mcp"}
	hostRisk := drafts.Finding{Code: "server.credential-host", Class: drafts.ClassRisk, Ack: drafts.AckTyped, Object: "App/jira", Typed: "api.atlassian.com",
		Sentence: "Straza will send jira's secret to api.atlassian.com, a host jira has not used before.", Before: "mcp.example.net", After: "static api.atlassian.com"}
	pool := line("ready.pool-empty", drafts.ClassWarning, "PolicySet/dev-access", "No person who could decide rule hold of dev-access holds approvers yet.")
	unknown := drafts.Finding{Code: "ready.tool-unknown", Class: drafts.ClassWarning, Object: "Role/dev", Sentence: "dev names get_you, which github does not offer.",
		Fix: "Fix the name, or check the list with strazactl apps tools github."}
	secret := drafts.Finding{Code: "ready.secret-missing", Class: drafts.ClassWarning, Object: "App/jira",
		Sentence: "jira needs a static secret and none is stored yet, so every call fails until one is.", Fix: "After publishing, run strazactl apps secret set jira."}
	registry := line("heuristic.registry-address", drafts.ClassWarning, "App/linear", "The address of linear differs from the one in the registry record its server block copies.")
	tools := line("unchecked.tools", drafts.ClassUnchecked, "", "Straza has not contacted mcp.example.net, because an address in a draft is contacted only when a person asks, so the tool names of jira are unknown.")
	nobody := line("info.nobody-holds", drafts.ClassInfo, "Role/jira-writers", "Nobody holds jira-writers yet, so it reaches nothing until someone is assigned it.")
	down := line("info.server-down", drafts.ClassInfo, "App/jira", "jira reads down now: connection refused.")
	verdict := func() drafts.Verdict {
		return drafts.Verdict{Draft: "41", Revision: 2, Snapshot: "snap", CheckedAt: "2026-09-24T10:00:00Z",
			Refused: []drafts.Finding{stale, parse}, Risks: []drafts.Finding{ghRisk, jiraRisk, hostRisk},
			Warnings:   []drafts.Finding{pool, unknown, secret, registry},
			Unchecked:  []drafts.Finding{tools},
			Passed:     []drafts.Finding{line("passed.secrets", drafts.ClassPassed, "", "No document holds a secret or the shape of one.")},
			Info:       []drafts.Finding{nobody, down},
			Gains:      []drafts.Gain{github, jira, native},
			Needs:      []drafts.Need{{Object: "Role/dev", Standing: "the scope identity:write"}},
			RiskDigest: "digest"}
	}
	v := verdict()
	w := drafts.World{Apps: map[string]drafts.App{"github": {Name: "github"}, "jira": {Name: "jira"}}}
	d := drafts.Draft{ID: "41", Revision: 2, Items: []drafts.Item{
		{Kind: drafts.KindApp, Name: "linear", Op: drafts.OpPut}, {Kind: drafts.KindApp, Name: "github", Op: drafts.OpPut}, {Kind: drafts.KindRole, Name: "dev", Op: drafts.OpPut}}}
	unnamed := func(gains ...drafts.Gain) []drafts.Gain {
		out := make([]drafts.Gain, len(gains))
		for i, g := range gains {
			g.Holders = nil
			out[i] = g
		}
		return out
	}
	hidden := drafts.Finding{Code: "info.gains-hidden", Class: drafts.ClassInfo,
		Sentence: "1 row of who gains what is on a server you cannot read, so this view leaves it out. Ask an administrator for the scope apps:read to see it."}
	twoHidden := hidden
	twoHidden.Sentence = "2 rows of who gains what are on servers you cannot read, so this view leaves them out. Ask an administrator for the scope apps:read to see them."

	// A cut line keeps its code, class, ack and key, and its object and
	// typed text unless its object names the server: an App line loses
	// both, whatever its typed text is.
	ghCut := findingPayload{Finding: drafts.Finding{Code: "access.ungated", Class: drafts.ClassRisk, Ack: drafts.AckTyped, Object: "Role/dev", Typed: "dev",
		Sentence: cutLineWords, Before: cutPartWords, After: cutPartWords}, Key: ghRisk.Key()}
	jiraCut := findingPayload{Finding: drafts.Finding{Code: "server.runs-code", Class: drafts.ClassRisk, Ack: drafts.AckTyped, Sentence: cutLineWords, After: cutPartWords}, Key: jiraRisk.Key()}
	hostCut := findingPayload{Finding: drafts.Finding{Code: "server.credential-host", Class: drafts.ClassRisk, Ack: drafts.AckTyped,
		Sentence: cutLineWords, Before: cutPartWords, After: cutPartWords}, Key: hostRisk.Key()}
	unknownCut := findingPayload{Finding: drafts.Finding{Code: "ready.tool-unknown", Class: drafts.ClassWarning, Object: "Role/dev", Sentence: cutLineWords}, Key: unknown.Key()}
	secretCut := findingPayload{Finding: drafts.Finding{Code: "ready.secret-missing", Class: drafts.ClassWarning, Sentence: cutLineWords}, Key: secret.Key()}
	registryCut := findingPayload{Finding: drafts.Finding{Code: "heuristic.registry-address", Class: drafts.ClassWarning, Sentence: cutLineWords}, Key: registry.Key()}
	toolsCut := findingPayload{Finding: drafts.Finding{Code: "unchecked.tools", Class: drafts.ClassUnchecked, Sentence: cutLineWords}, Key: tools.Key()}
	downCut := findingPayload{Finding: drafts.Finding{Code: "info.server-down", Class: drafts.ClassInfo, Sentence: cutLineWords}, Key: down.Key()}

	// A reader without root or drafts:read also reads a line cut that names a
	// role or a set it may not read: its object and typed text go when the
	// object is one, and the server words win when a server is named too.
	ghObject := findingPayload{Finding: drafts.Finding{Code: "access.ungated", Class: drafts.ClassRisk, Ack: drafts.AckTyped,
		Sentence: objectLineWords, Before: objectPartWords, After: objectPartWords}, Key: ghRisk.Key()}
	ghBoth := findingPayload{Finding: drafts.Finding{Code: "access.ungated", Class: drafts.ClassRisk, Ack: drafts.AckTyped,
		Sentence: cutLineWords, Before: cutPartWords, After: cutPartWords}, Key: ghRisk.Key()}
	poolObject := findingPayload{Finding: drafts.Finding{Code: "ready.pool-empty", Class: drafts.ClassWarning, Sentence: objectLineWords}, Key: pool.Key()}
	unknownObject := findingPayload{Finding: drafts.Finding{Code: "ready.tool-unknown", Class: drafts.ClassWarning, Sentence: objectLineWords}, Key: unknown.Key()}
	unknownBoth := findingPayload{Finding: drafts.Finding{Code: "ready.tool-unknown", Class: drafts.ClassWarning, Sentence: cutLineWords}, Key: unknown.Key()}
	nobodyObject := findingPayload{Finding: drafts.Finding{Code: "info.nobody-holds", Class: drafts.ClassInfo, Sentence: objectLineWords}, Key: nobody.Key()}

	// Every gain row here is of the role dev, which a reader without
	// identity:read, root or drafts:read may not read.
	rolesHidden := keyedOne(drafts.Finding{Code: "info.gains-hidden", Class: drafts.ClassInfo,
		Sentence: "3 rows of who gains what name roles or servers you cannot read, so this view leaves them out. Ask an administrator for the grants that read them."})
	counted := v
	counted.Gains = unnamed(github, jira, native)
	maxReads := keyedAll(counted)
	maxReads.Gains = []drafts.Gain{}
	maxReads.Risks = []findingPayload{ghObject, keyedOne(jiraRisk), keyedOne(hostRisk)}
	maxReads.Warnings = []findingPayload{poolObject, unknownObject, keyedOne(secret), keyedOne(registry)}
	maxReads.Info = []findingPayload{rolesHidden, nobodyObject, keyedOne(down)}
	onGithub := v
	onGithub.Gains, onGithub.Info = unnamed(github, native), []drafts.Finding{hidden, nobody, down}
	erinReads := keyedAll(onGithub)
	erinReads.Gains = []drafts.Gain{}
	erinReads.Risks = []findingPayload{ghObject, jiraCut, hostCut}
	erinReads.Warnings = []findingPayload{poolObject, unknownObject, secretCut, registryCut}
	erinReads.Unchecked = []findingPayload{toolsCut}
	erinReads.Info = []findingPayload{rolesHidden, nobodyObject, downCut}
	onNeither := v
	onNeither.Gains, onNeither.Info = unnamed(native), []drafts.Finding{twoHidden, nobody, down}
	blind := func(v drafts.Verdict) verdictPayload {
		p := keyedAll(v)
		p.Risks = []findingPayload{ghCut, jiraCut, hostCut}
		p.Warnings = []findingPayload{keyedOne(pool), unknownCut, secretCut, registryCut}
		p.Unchecked = []findingPayload{toolsCut}
		p.Info = []findingPayload{keyedOne(twoHidden), keyedOne(nobody), downCut}
		return p
	}
	idaReads := blind(onNeither)
	idaReads.Gains = []drafts.Gain{}
	idaReads.Risks = []findingPayload{ghBoth, jiraCut, hostCut}
	idaReads.Warnings = []findingPayload{poolObject, unknownBoth, secretCut, registryCut}
	idaReads.Info = []findingPayload{rolesHidden, nobodyObject, downCut}
	// Which findings an agent reads is ForAgent's to decide, so the agent's
	// view is built from it, and the cut adds no person, count or pool. An
	// agent without apps:read reads its lines on servers cut as a person does.
	av := v.ForAgent()
	agentView := keyedAll(drafts.Verdict{Draft: "41", Revision: 2, Snapshot: "snap", CheckedAt: "2026-09-24T10:00:00Z",
		Refused: av.Refused, Risks: av.Risks, Warnings: av.Warnings, Unchecked: av.Unchecked,
		Passed: []drafts.Finding{}, Info: []drafts.Finding{}, Gains: []drafts.Gain{}, Needs: []drafts.Need{}})
	agentCut := agentView
	agentCut.Risks = []findingPayload{jiraCut, hostCut}
	agentCut.Warnings = []findingPayload{unknownCut, secretCut, registryCut}
	agentCut.Unchecked = []findingPayload{toolsCut}
	if got := verdictFor(agentCaller("bot"), w, d, v); strings.Contains(fmt.Sprintf("%+v", got), "bob") || strings.Contains(fmt.Sprintf("%+v", got), "approvers") {
		t.Errorf("the agent's view names a person or a pool: %+v", got)
	}
	cases := []struct {
		name string
		c    draftCaller
		want verdictPayload
	}{
		{"root reads every line, every row and the holders", rootCaller("kim"), keyedAll(v)},
		{"apps:read and identity:read read every line, every row and the holders", adminAPICaller("ci", "drafts:read", "apps:read", "identity:read"), keyedAll(v)},
		{"apps:read reads every line and row and counts the holders", adminAPICaller("ci", "drafts:read", "apps:read"), keyedAll(counted)},
		{"the global MCP admin reads no row and no line of a role or a set", personCaller("max", "apps:read", "apps:write"), maxReads},
		{"github's server admin reads github's lines alone, and no row and no line of a role or a set", administering(personCaller("erin"), map[string]string{"a1": "github"}), erinReads},
		{"identity:read without apps:read reads the built-in app's rows alone", adminAPICaller("ci", "drafts:read", "identity:read"), blind(withHolders(onNeither, native))},
		{"identity:write reads no row and no line of a role or a set", personCaller("ida", "identity:write"), idaReads},
		{"an agent reads the agent's view", agentCaller("bot", "drafts:read", "identity:read", "apps:read"), agentView},
		{"an agent without apps:read reads the agent's view with its lines on servers cut", agentCaller("bot", "drafts:read"), agentCut},
	}
	for _, tc := range cases {
		if got := verdictFor(tc.c, w, d, v); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
	if !reflect.DeepEqual(v, verdict()) {
		t.Errorf("the cut changed the verdict it was given: %+v", v)
	}
}

// TestVerdictForKeepsAnAgentsNameLinesOnItsOwnDraft pins the agent's name
// lines under the read cut: an agent without apps:read reads
// ready.tool-unknown and
// ready.rule-names-nothing whole on a draft whose every revision it wrote,
// so it can fix a name, and cut on a draft a person also wrote. Its other
// lines on a server it cannot read are cut either way.
func TestVerdictForKeepsAnAgentsNameLinesOnItsOwnDraft(t *testing.T) {
	t.Parallel()
	bot := agentCaller("bot", "drafts:read")
	w := drafts.World{Apps: map[string]drafts.App{"github": {Name: "github"}, "jira": {Name: "jira"}}}
	unknown := drafts.Finding{Code: "ready.tool-unknown", Class: drafts.ClassWarning, Object: "Role/dev", Sentence: "dev names get_you, which github does not offer.",
		Fix: "Fix the name, or check the list with strazactl apps tools github."}
	nothing := drafts.Finding{Code: "ready.rule-names-nothing", Class: drafts.ClassWarning, Object: "PolicySet/guard",
		Sentence: "Rule r of guard names get_you on github, which the server does not offer.", Fix: "Fix the name."}
	runs := drafts.Finding{Code: "server.runs-code", Class: drafts.ClassRisk, Ack: drafts.AckTyped, Object: "Role/dev", Typed: "dev",
		Sentence: "dev reaches jira, which starts /usr/bin/jira-mcp on the Straza host.", Before: "command /usr/bin/jira-mcp"}
	tools := drafts.Finding{Code: "unchecked.tools", Class: drafts.ClassUnchecked, Object: "Role/dev",
		Sentence: "Straza has not contacted jira, so the tool names of jira are unknown."}
	v := drafts.Verdict{Draft: "41", Revision: 2, Risks: []drafts.Finding{runs}, Warnings: []drafts.Finding{unknown, nothing}, Unchecked: []drafts.Finding{tools}}
	cut := func(f drafts.Finding) findingPayload {
		c := f
		c.Sentence, c.Fix = cutLineWords, ""
		if c.Before != "" {
			c.Before = cutPartWords
		}
		return findingPayload{Finding: c, Key: f.Key()}
	}
	items := []drafts.Item{{Kind: drafts.KindRole, Name: "dev", Op: drafts.OpPut}, {Kind: drafts.KindPolicySet, Name: "guard", Op: drafts.OpPut}}
	for _, tc := range []struct {
		name     string
		authors  []drafts.Principal
		warnings []findingPayload
	}{
		{"a draft whose every revision the agent wrote", []drafts.Principal{bot.author}, []findingPayload{keyedOne(unknown), keyedOne(nothing)}},
		{"a draft a person also wrote", []drafts.Principal{bot.author, rootCaller("kim").author}, []findingPayload{cut(unknown), cut(nothing)}},
	} {
		got := verdictFor(bot, w, drafts.Draft{ID: "41", Revision: 2, Authors: tc.authors, Items: items}, v)
		if !reflect.DeepEqual(got.Warnings, tc.warnings) {
			t.Errorf("%s: the agent reads the warnings %+v, want %+v", tc.name, got.Warnings, tc.warnings)
		}
		if !reflect.DeepEqual(got.Risks, []findingPayload{cut(runs)}) || !reflect.DeepEqual(got.Unchecked, []findingPayload{cut(tools)}) {
			t.Errorf("%s: the agent reads the risks %+v and the unchecked lines %+v, want both cut", tc.name, got.Risks, got.Unchecked)
		}
	}
}

// TestVerdictForCutsNamesTheVerdictTouches pins where the object cut
// finds the roles and sets a reader may not read: the draft's items, each
// line's object, a live role or set named as a whole name in a line's
// before, after or typed words, and the role of a gain row. A live role or
// set the verdict does not touch cuts no line, so a role named person
// leaves whole every sentence that says "a person".
func TestVerdictForCutsNamesTheVerdictTouches(t *testing.T) {
	t.Parallel()
	erin := administering(personCaller("erin"), map[string]string{"a1": "github"})
	w := drafts.World{Apps: map[string]drafts.App{"github": {Name: "github"}},
		Roles: map[string]drafts.Role{"person": {Name: "person"}, "payroll-admins": {Name: "payroll-admins"}}, Policies: map[string]drafts.Policy{"payroll-guard": {}}}
	github := drafts.Item{Kind: drafts.KindApp, Name: "github", Op: drafts.OpPut}
	line := func(sentence string, edit func(*drafts.Finding)) drafts.Finding {
		f := drafts.Finding{Code: "info.server-down", Class: drafts.ClassInfo, Object: "App/github", Sentence: sentence}
		if edit != nil {
			edit(&f)
		}
		return f
	}
	reach := "github reads down, and payroll-admins reach it."
	for _, tc := range []struct {
		name  string
		f     drafts.Finding
		items []drafts.Item
		gains []drafts.Gain
		cut   bool
	}{
		{"a live role named person, a word of the prose", line("github reads down, so its tools are unknown until a person asks.", nil), nil, nil, false},
		{"a live role the prose alone names, which nothing else of the verdict touches", line(reach, nil), nil, nil, false},
		{"a live role named in the before words", line(reach, func(f *drafts.Finding) { f.Before = "payroll-admins/get_me" }), nil, nil, true},
		{"a live set named in the typed text", line("github reads down, and payroll-guard gates it.", func(f *drafts.Finding) { f.Ack, f.Typed = drafts.AckTyped, "payroll-guard" }), nil, nil, true},
		{"a live role a gain row names", line(reach, nil), nil, []drafts.Gain{{Role: "payroll-admins", Server: "github", Tool: "get_me", Before: "denied", After: "runs"}}, true},
		{"a role the draft holds", line(reach, nil), []drafts.Item{{Kind: drafts.KindRole, Name: "payroll-admins", Op: drafts.OpPut}}, nil, true},
	} {
		d := drafts.Draft{ID: "41", Revision: 1, Items: append([]drafts.Item{github}, tc.items...)}
		got := verdictFor(erin, w, d, drafts.Verdict{Draft: "41", Revision: 1, Info: []drafts.Finding{tc.f}, Gains: tc.gains})
		var seen *findingPayload
		for i := range got.Info {
			if got.Info[i].Code == tc.f.Code {
				seen = &got.Info[i]
			}
		}
		switch {
		case seen == nil:
			t.Errorf("%s: erin reads no %s line: %+v", tc.name, tc.f.Code, got.Info)
		case tc.cut && seen.Sentence != objectLineWords:
			t.Errorf("%s: erin reads %+v, want its words cut", tc.name, seen.Finding)
		case !tc.cut && seen.Finding != tc.f:
			t.Errorf("%s: erin reads %+v, want it whole", tc.name, seen.Finding)
		}
	}
}

// withHolders is v with its gains named as named are.
func withHolders(v drafts.Verdict, named ...drafts.Gain) drafts.Verdict {
	v.Gains = append([]drafts.Gain{}, named...)
	return v
}

// TestCutFinding pins that a server named in any text of a line cuts the
// line, and that its object and typed text go only when they name it. The
// lines are made up so that each names jira in one place alone. A role or
// a set the reader may not read cuts a line with the object's words, and
// the server's words win in a line that names both.
func TestCutFinding(t *testing.T) {
	t.Parallel()
	base := drafts.Finding{Code: "access.gated", Class: drafts.ClassRisk, Ack: drafts.AckTick, Object: "Role/dev", Sentence: "dev will reach a tool."}
	cut := func(f drafts.Finding) drafts.Finding {
		f.Sentence, f.Fix = cutLineWords, ""
		if f.Before != "" {
			f.Before = cutPartWords
		}
		if f.After != "" {
			f.After = cutPartWords
		}
		return f
	}
	with := func(edit func(*drafts.Finding)) drafts.Finding {
		f := base
		edit(&f)
		return f
	}
	objectCut := func(f drafts.Finding) drafts.Finding {
		f = cut(f)
		f.Sentence = objectLineWords
		if f.Before != "" {
			f.Before = objectPartWords
		}
		if f.After != "" {
			f.After = objectPartWords
		}
		return f
	}
	cases := []struct {
		name string
		f    drafts.Finding
		want drafts.Finding
	}{
		{"a line that names no such server", base, base},
		{"the sentence", with(func(f *drafts.Finding) { f.Sentence = "dev will reach create_issue on jira." }), cut(base)},
		{"the object", with(func(f *drafts.Finding) { f.Object = "App/jira" }), with(func(f *drafts.Finding) { f.Object, f.Sentence = "", cutLineWords })},
		{"the object, with the typed text", with(func(f *drafts.Finding) { f.Object, f.Ack, f.Typed = "App/jira", drafts.AckTyped, "api.atlassian.com" }),
			with(func(f *drafts.Finding) { f.Object, f.Ack, f.Sentence = "", drafts.AckTyped, cutLineWords })},
		{"the typed text", with(func(f *drafts.Finding) { f.Ack, f.Typed = drafts.AckTyped, "jira" }),
			with(func(f *drafts.Finding) { f.Ack, f.Sentence = drafts.AckTyped, cutLineWords })},
		{"the fix", with(func(f *drafts.Finding) { f.Fix = "Run strazactl apps tools jira." }), cut(base)},
		{"the before words", with(func(f *drafts.Finding) { f.Before = "jira/create_issue" }), cut(with(func(f *drafts.Finding) { f.Before = "x" }))},
		{"the after words", with(func(f *drafts.Finding) { f.After = "jira/create_issue" }), cut(with(func(f *drafts.Finding) { f.After = "x" }))},
	}
	for _, tc := range cases {
		if got := cutFinding(tc.f, []string{"jira"}, nil); got != tc.want {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
	objects := []struct {
		name   string
		f      drafts.Finding
		hidden []string
		want   drafts.Finding
	}{
		{"a line on a role the reader may not read", with(func(f *drafts.Finding) { f.Before, f.Ack, f.Typed = "dev/x", drafts.AckTyped, "dev" }), []string{"dev"},
			objectCut(with(func(f *drafts.Finding) { f.Object, f.Before, f.Ack = "", "x", drafts.AckTyped }))},
		{"a line that names such a set in its sentence alone", with(func(f *drafts.Finding) { f.Sentence = "dev will reach a tool guard gates." }), []string{"guard"},
			objectCut(base)},
		{"a line that names such a role and such a server", with(func(f *drafts.Finding) { f.Sentence = "dev will reach a tool on jira." }), []string{"dev"},
			with(func(f *drafts.Finding) { f.Object, f.Sentence = "", cutLineWords })},
		{"a set named only as a part of a longer name", with(func(f *drafts.Finding) { f.Sentence = "dev will reach a tool guard-rails gates." }), []string{"guard"},
			with(func(f *drafts.Finding) { f.Sentence = "dev will reach a tool guard-rails gates." })},
	}
	for _, tc := range objects {
		if got := cutFinding(tc.f, []string{"jira"}, tc.hidden); got != tc.want {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
}

// TestNamesServer pins what names a server: its name as a whole, and never
// as a part of a longer name such as a role, a set or a tool.
func TestNamesServer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		text string
		want bool
	}{
		{"App/jira", true},
		{"jira", true},
		{"create_issue on jira will run for holders of dev.", true},
		{"jira/create_issue, jira/delete_project", true},
		{"jira-writers holds jira/create_issue", true},
		{"jira-writers", false},
		{"jira_search on github", false},
		{"ajira and jira2", false},
		{"Ajira", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := namesServer(tc.text, []string{"", "github-enterprise", "jira"}); got != tc.want {
			t.Errorf("namesServer(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
	if namesServer("jira/create_issue", nil) {
		t.Error("a text names a server when no server may not be read")
	}
}

// TestVerdictForCutsGainsOnServersTheCallerCannotRead runs the gain cut
// through a real check: a server admin of github, who
// reads global roles, reads a draft that widens a global role's row on jira
// and gives a new role of github every tool of github. It reads
// github's gain rows and no row on jira, and one info line counts the rows
// left out without naming jira.
func TestVerdictForCutsGainsOnServersTheCallerCannotRead(t *testing.T) {
	t.Parallel()
	w := offeringWorld(t)
	// A global role gains no new row, so probe-reach holds an empty row on
	// jira from before.
	w.Roles["probe-reach"] = drafts.Role{ID: "r5", Name: "probe-reach", Kind: drafts.RoleKindApplication, Plane: drafts.PlaneAccess}
	w.Access["probe-reach"] = drafts.Access{ID: "b5", Server: "jira"}
	erin := administering(personCaller("erin", "identity:read"), map[string]string{"a1": "github"})
	d := drafts.Draft{ID: "41", Revision: 1, Authors: []drafts.Principal{erin.author},
		Items: []drafts.Item{
			{Kind: drafts.KindRole, Name: "probe-reach", Op: drafts.OpPut, Doc: roleText(t, "probe-reach", "", "", "jira", "*")},
			{Kind: drafts.KindRole, Name: "github-probe", Op: drafts.OpPut, Doc: roleText(t, "github-probe", "github", "", "github", "*")},
		}}
	v := drafts.Check(w, d, drafts.CheckInput{Now: time.Now()})
	if len(v.Refused) > 0 {
		t.Fatalf("refused: %+v", v.Refused)
	}
	on := func(gains []drafts.Gain, server string) int {
		n := 0
		for _, g := range gains {
			if g.Server == server {
				n++
			}
		}
		return n
	}
	if on(v.Gains, "jira") != 2 || on(v.Gains, "github") == 0 {
		t.Fatalf("the check's gains = %+v, want two rows on jira and some on github", v.Gains)
	}
	seen := verdictFor(erin, w, d, v)
	if on(seen.Gains, "jira") != 0 || on(seen.Gains, "github") != on(v.Gains, "github") {
		t.Errorf("erin reads %d rows on jira and %d of github's %d", on(seen.Gains, "jira"), on(seen.Gains, "github"), on(v.Gains, "github"))
	}
	want := "2 rows of who gains what are on servers you cannot read, so this view leaves them out. Ask an administrator for the scope apps:read to see them."
	found := false
	for _, f := range seen.Info {
		if strings.Contains(f.Sentence, "jira") {
			t.Errorf("an info line names jira: %q", f.Sentence)
		}
		found = found || (f.Code == "info.gains-hidden" && f.Sentence == want)
	}
	if !found {
		t.Errorf("no info line counts the rows left out: %+v", seen.Info)
	}
	if all := verdictFor(rootCaller("kim"), w, d, v); on(all.Gains, "jira") != 2 {
		t.Errorf("root reads %d rows on jira, want 2", on(all.Gains, "jira"))
	}
}

// offeringWorld is standingWorld with the tools github and jira offer and
// local tools allowed by default.
func offeringWorld(t *testing.T) drafts.World {
	w := standingWorld(t)
	w.LocalToolDefault = "allow"
	for name, tools := range map[string][]string{"jira": {"create_issue", "delete_project"}, "github": {"get_me", "push_files"}} {
		app := w.Apps[name]
		app.Offered = tools
		w.Apps[name] = app
	}
	return w
}

// TestVerdictForCutsLinesOnServersTheCallerCannotRead runs the cut of the
// lines through a real check. Two people hold dev, whose row on github gains
// a tool, and tickets, a global role whose row on jira gains two, and
// github's server admin, who reads global roles,
// reads the verdict: every risk, warning, unchecked and info line that
// names jira has its words cut and keeps its code, class, ack and the key
// a publish acknowledges it by, which the answer's JSON carries, and every
// other line reads as the check wrote it. Root reads every line, and an
// agent reads the agent's view, which keeps ready.tool-unknown.
func TestVerdictForCutsLinesOnServersTheCallerCannotRead(t *testing.T) {
	t.Parallel()
	w := offeringWorld(t)
	// A global role gains no new row, so tickets holds a row on jira from
	// before.
	w.Roles["tickets"] = drafts.Role{ID: "r5", Name: "tickets", Kind: drafts.RoleKindApplication, Plane: drafts.PlaneAccess}
	w.Access["tickets"] = drafts.Access{ID: "b5", Server: "jira", Tools: []string{"delete_project"}}
	w.HolderCounts["tickets"] = 2
	two := []drafts.Holder{{Username: "alice"}, {Username: "bob"}}
	w.Holders = map[string][]drafts.Holder{"dev": two, "tickets": two}
	erin := administering(personCaller("erin", "identity:read"), map[string]string{"a1": "github"})
	d := drafts.Draft{ID: "41", Revision: 1, Authors: []drafts.Principal{erin.author},
		Items: []drafts.Item{{Kind: drafts.KindRole, Name: "dev", Op: drafts.OpPut, Doc: roleText(t, "dev", "", "", "github", "get_me", "push_files")},
			{Kind: drafts.KindRole, Name: "tickets", Op: drafts.OpPut, Doc: roleText(t, "tickets", "", "", "jira", "create_issue", "delete_project", "ghost")}}}
	v := drafts.Check(w, d, drafts.CheckInput{Now: time.Now()})
	if len(v.Refused) > 0 {
		t.Fatalf("refused: %+v", v.Refused)
	}
	namesJira := func(f drafts.Finding) bool {
		return namesServer(strings.Join([]string{f.Object, f.Sentence, f.Fix, f.Before, f.After, f.Typed}, "\n"), []string{"jira"})
	}
	seen := verdictFor(erin, w, d, v)
	cuts, kept := 0, 0
	lists := []struct {
		name      string
		raw       []drafts.Finding
		seen      []findingPayload
		extraSeen int
	}{
		{"risks", v.Risks, seen.Risks, 0}, {"warnings", v.Warnings, seen.Warnings, 0}, {"unchecked", v.Unchecked, seen.Unchecked, 0},
		{"info", v.Info, seen.Info, len(seen.Info) - len(v.Info)},
	}
	for _, l := range lists {
		if len(l.seen)-l.extraSeen != len(l.raw) {
			t.Fatalf("%s: erin reads %d lines of %d", l.name, len(l.seen)-l.extraSeen, len(l.raw))
		}
		for i, raw := range l.raw {
			got := l.seen[i+l.extraSeen]
			if got.Key != raw.Key() {
				t.Errorf("%s %s: key %s, want the check's %s", l.name, raw.Code, got.Key, raw.Key())
			}
			if !namesJira(raw) {
				kept++
				if got.Finding != raw {
					t.Errorf("%s %s: erin reads %+v, want the check's %+v", l.name, raw.Code, got.Finding, raw)
				}
				continue
			}
			cuts++
			if namesJira(got.Finding) || got.Sentence != cutLineWords || got.Code != raw.Code || got.Class != raw.Class || got.Ack != raw.Ack {
				t.Errorf("%s %s: erin reads %+v", l.name, raw.Code, got.Finding)
			}
		}
	}
	risky := false
	for _, f := range v.Risks {
		risky = risky || namesJira(f)
	}
	if !risky || cuts < 2 || kept == 0 {
		t.Fatalf("the check wrote %d lines on jira, a risk among them %v, and %d others; want a risk, another line and a line on github: %+v", cuts, risky, kept, v)
	}
	body, err := json.Marshal(seen)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"key":"`+v.Risks[0].Key()+`"`) || strings.Contains(string(body), "jira") {
		t.Errorf("the answer carries no risk's key, or names jira: %s", body)
	}
	if all := verdictFor(rootCaller("kim"), w, d, v); !reflect.DeepEqual(all, keyedAll(v)) {
		t.Errorf("root reads %+v, want every line", all)
	}
	bot := agentCaller("bot", "drafts:read")
	for _, tc := range []struct {
		name    string
		authors []drafts.Principal
		whole   bool
	}{
		{"erin's draft", d.Authors, false},
		{"a draft the agent alone wrote", []drafts.Principal{bot.author}, true},
	} {
		written := d
		written.Authors = tc.authors
		agent := verdictFor(bot, w, written, v)
		unknown := false
		for _, f := range agent.Warnings {
			unknown = unknown || (f.Code == "ready.tool-unknown" && namesJira(f.Finding))
		}
		if unknown != tc.whole {
			t.Errorf("%s: the agent reads ready.tool-unknown on jira whole %v, want %v: %+v", tc.name, unknown, tc.whole, agent.Warnings)
		}
	}
}
