package drafts

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// fieldScenarios are checks whose findings carry the fields a finding
// holds beside its words: rows of who gains, the rule, server and tools of
// a rule that opens a server Straza has not registered, and the mark of a
// set that applies to the proposer.
var fieldScenarios = []struct {
	name     string
	items    []Item
	proposer Holder
	agent    bool
}{
	{"ungated", []Item{gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: ['*']\n")}, Holder{}, false},
	{"gated", []Item{gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: [get_me, push_files]\n"),
		{Kind: KindPolicySet, Name: "readers-access", Op: OpPut, Doc: gainSet("readers-access", "{ roles: [readers] }",
			"    - id: hold-push\n      tools: [mcp.call]\n      toolNames: { allow: [push_files] }\n      mode: approve\n      approve: { roles: [sec-approvers] }\n")}}, Holder{}, false},
	{"denygone", []Item{devAccessWith("    - id: no-delete\n      tools: [mcp.call]\n      toolNames: { deny: [delete_repo] }\n", "")}, Holder{}, false},
	{"chrome", []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: devAccess +
		"    - id: allow-chrome\n      events: [tool.pre]\n      tools: [mcp.call]\n      apps: [claude-in-chrome]\n      toolNames: { allow: [navigate, form_input] }\n      effect: allow\n"}},
		Holder{Username: "alice", UserType: "human"}, false},
	{"aliceremove", []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpRemove}}, Holder{Username: "alice", UserType: "human"}, false},
	{"carolremove", []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpRemove}}, Holder{Username: "carol", UserType: "human"}, false},
	{"bobunbound", []Item{gainRole("engineering", "    kind: business\n")}, Holder{Username: "bob", UserType: "human"}, false},
	{"agentremove", []Item{{Kind: KindPolicySet, Name: "dev-access", Op: OpRemove}}, ciBot, true},
}

// fieldCheck runs the field scenario named name.
func fieldCheck(t *testing.T, name string) Verdict {
	t.Helper()
	for _, s := range fieldScenarios {
		if s.name != name {
			continue
		}
		w := gainWorld()
		d := stamped(w, s.items...)
		if s.agent {
			d = agentDraftOf(w, s.items...)
		}
		v := Check(w, d, CheckInput{Proposer: s.proposer, Now: checkNow,
			HarnessTools: func() (map[string]string, error) { return map[string]string{"Bash": policy.ToolShellExec}, nil }})
		if len(v.Refused) > 0 {
			t.Fatalf("%s refused: %q", name, findingLines(v.Refused))
		}
		return v
	}
	t.Fatalf("no field scenario %s", name)
	return Verdict{}
}

// fieldLines spells each risk and warning that carries a field beside its
// words: its code and object, then its rows, its rule, server and tools,
// and whether it binds the proposer.
func fieldLines(v Verdict) []string {
	out := []string{}
	for _, f := range append(append([]Finding{}, v.Risks...), v.Warnings...) {
		var parts []string
		for _, r := range f.Rows {
			parts = append(parts, r.Server+"/"+r.Tool)
		}
		if f.Rule != "" || f.Server != "" || len(f.Tools) > 0 {
			parts = append(parts, "rule "+f.Rule+" server "+f.Server+" tools "+strings.Join(f.Tools, ","))
		}
		if f.BindsProposer {
			parts = append(parts, "binds the proposer")
		}
		if len(parts) > 0 {
			out = append(out, f.Code+" "+f.Object+" "+strings.Join(parts, " "))
		}
	}
	return out
}

// TestFindingFields pins the fields each risk and warning carries beside its
// words: the rows of a risk of who gains, the rule, server and tools of
// policy.local-allow and of ready.rule-names-nothing, and binds_proposer on
// a policy.guardrail-changed line of a set that applies to the proposer,
// for a person and for an agent, and on no line whose set does not.
func TestFindingFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		scenario string
		want     []string
	}{
		{"ungated", []string{"access.ungated Role/readers github/delete_repo github/push_files"}},
		{"gated", []string{"access.gated Role/readers github/push_files"}},
		{"denygone", []string{"access.deny-removed Role/dev github/delete_repo", "access.ungated Role/dev github/delete_repo",
			"access.deny-removed Role/engineering github/delete_repo", "access.ungated Role/engineering github/delete_repo"}},
		{"chrome", []string{"policy.local-allow PolicySet/dev-access rule allow-chrome server claude-in-chrome tools navigate,form_input",
			"ready.rule-names-nothing PolicySet/dev-access rule allow-chrome server claude-in-chrome tools navigate,form_input"}},
		{"aliceremove", []string{"policy.guardrail-changed PolicySet/dev-access binds the proposer", "access.deny-removed Role/dev github/delete_repo",
			"access.ungated Role/dev github/delete_repo github/push_files", "access.deny-removed Role/engineering github/delete_repo",
			"access.ungated Role/engineering github/delete_repo github/push_files"}},
		{"carolremove", []string{"access.deny-removed Role/dev github/delete_repo",
			"access.ungated Role/dev github/delete_repo github/push_files", "access.deny-removed Role/engineering github/delete_repo",
			"access.ungated Role/engineering github/delete_repo github/push_files"}},
		{"bobunbound", []string{"policy.guardrail-changed PolicySet/dev-access binds the proposer"}},
		{"agentremove", []string{"policy.guardrail-changed PolicySet/dev-access binds the proposer", "access.deny-removed Role/dev github/delete_repo",
			"access.ungated Role/dev github/delete_repo github/push_files", "access.deny-removed Role/engineering github/delete_repo",
			"access.ungated Role/engineering github/delete_repo github/push_files"}},
	}
	for _, tc := range cases {
		t.Run(tc.scenario, func(t *testing.T) {
			if got := fieldLines(fieldCheck(t, tc.scenario)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("fields\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestFindingFieldsLeaveKeysAlone pins that the fields beside the words
// change no finding's key: each key below is the key the same checks
// answer without the fields.
func TestFindingFieldsLeaveKeysAlone(t *testing.T) {
	t.Parallel()
	want := map[string]map[string]string{
		"ungated": {"access.tools-later Role/readers": "e102d1475c3ddc1fc349676037aab905526dc882ccce404815920c24b4ad6f84",
			"access.ungated Role/readers":      "3ade4b44f533d6fb4ff6c742a4c4233a23d3349bf98d10576a1f34c1ce76ab1b",
			"heuristic.destructive App/github": "fb8c86e115bf6ff1860e5430c6582b50cc007197836d6847b8f2fd595abf6ca8"},
		"gated": {"access.gated Role/readers": "11fb57729e7e503f19187a10d40e2895d0a5c72e812618d42c0ccc408a586968"},
		"denygone": {"policy.guardrail-changed PolicySet/dev-access": "2189fc89271a7df5d06905c9cbd650e1744b17ef61d4f5992fd8c338539dbeb2",
			"access.deny-removed Role/dev":         "902042d938f72286bd9c9a53df61144a63b212e776094a48e6ff5126bb662411",
			"access.ungated Role/dev":              "7dbd3e26c29da4e1229d1371897d482ab4ad876896f855c8730fff64939a3b4b",
			"access.deny-removed Role/engineering": "9df83aabe99af7b04b2e96146b57e0d91f47c5bbd00b185a87d0dd80ada8edf4",
			"access.ungated Role/engineering":      "a03f262ba0c64e1190d47e7c3a0040c89534a45bceb02bcaf828472471a02799",
			"heuristic.destructive App/github":     "fb8c86e115bf6ff1860e5430c6582b50cc007197836d6847b8f2fd595abf6ca8"},
		"chrome": {"policy.local-allow PolicySet/dev-access": "387cc8ddf577276539e55700e6bb1e676a12c0f78963f865c65fda262d6a7ffe",
			"ready.rule-names-nothing PolicySet/dev-access": "5670dcad97aec9723a19abb92f7b38385c328320790c177650aaffef7a3e9e70"},
		"aliceremove": {"policy.guardrail-changed PolicySet/dev-access": "0d7a5d8404f7308a8de7a6ef68703a19e4f224986831e1ea2653cb62c3dc10b4",
			"access.deny-removed Role/dev":         "902042d938f72286bd9c9a53df61144a63b212e776094a48e6ff5126bb662411",
			"access.ungated Role/dev":              "277e3047f117fa3a437086b4db2e7a181ada4fb5e21205cfa184765617e2950d",
			"access.deny-removed Role/engineering": "9df83aabe99af7b04b2e96146b57e0d91f47c5bbd00b185a87d0dd80ada8edf4",
			"access.ungated Role/engineering":      "18227df36bdd2252261521564a9cc85ed592921fa86dbf0c3185e0797593d3a7",
			"heuristic.destructive App/github":     "fb8c86e115bf6ff1860e5430c6582b50cc007197836d6847b8f2fd595abf6ca8"},
		"bobunbound": {"policy.guardrail-changed PolicySet/dev-access": "7c97c2ae4f1ee229f816f2fe751f5a87ece0360ed12a905d91ab72bab5b022fc",
			"narrow.role-loses Role/engineering": "bde4a93aa1ffc5d79c6dc79ffef59576df12409a64bb10c5d2987191a6c7393f"},
	}
	for scenario, keys := range want {
		v := fieldCheck(t, scenario)
		got := map[string]string{}
		for _, f := range append(append([]Finding{}, v.Risks...), v.Warnings...) {
			got[f.Code+" "+f.Object] = f.Key()
			bare := f
			bare.Rows, bare.Rule, bare.Server, bare.Tools, bare.BindsProposer = nil, "", "", nil, false
			if bare.Key() != f.Key() {
				t.Errorf("%s %s %s: the fields beside the words move the key", scenario, f.Code, f.Object)
			}
		}
		if !reflect.DeepEqual(got, keys) {
			t.Errorf("%s: keys\n got %v\nwant %v", scenario, got, keys)
		}
	}
}

// TestFoldRisksJoinsTheFields pins how foldRisks joins the fields of the
// risks it folds: the rows as one list in the order of the verdict's
// gains, each pair once, and the rule, server and tools kept only where
// every part names the same.
func TestFoldRisksJoinsTheFields(t *testing.T) {
	t.Parallel()
	rows := func(f Finding, pairs ...string) Finding {
		for _, p := range pairs {
			s, tool, _ := strings.Cut(p, "/")
			f.Rows = append(f.Rows, ToolRow{Server: s, Tool: tool})
		}
		return f
	}
	named := func(f Finding, rule, server string, tools ...string) Finding {
		f.Rule, f.Server, f.Tools = rule, server, tools
		return f
	}
	allow := func(rule, server string) Finding {
		return named(risk(codeLocalAllow, "PolicySet/s", "", "Rule "+rule+" allows "+server+".", "", rule+": "+server), rule, server, "a", "b")
	}
	parts := []Finding{
		rows(risk(codeUngated, "Role/dev", "dev", "b on jira will run.", "jira/b", "jira/b"), "jira/b"),
		rows(risk(codeUngated, "Role/dev", "dev", "b and c on github will run.", "github/b, github/c", "github/b, github/c"), "github/b", "github/c"),
		rows(risk(codeUngated, "Role/dev", "dev", "a and c on github will run.", "github/a, github/c", "github/a, github/c"), "github/a", "github/c"),
		allow("r", "x"), allow("r", "x"),
		named(risk(codeLocalAllow, "PolicySet/t", "", "Rule r allows x.", "", "r: x"), "r", "x"),
		named(risk(codeLocalAllow, "PolicySet/t", "", "Rule q allows y.", "", "q: y"), "q", "y"),
	}
	got := foldRisks(parts)
	if len(got) != 3 {
		t.Fatalf("foldRisks folded into %d findings, want 3: %q", len(got), riskLines(got))
	}
	if want := []ToolRow{{"github", "a"}, {"github", "b"}, {"github", "c"}, {"jira", "b"}}; !reflect.DeepEqual(got[0].Rows, want) {
		t.Errorf("rows %v, want %v", got[0].Rows, want)
	}
	if got[1].Rule != "r" || got[1].Server != "x" || !reflect.DeepEqual(got[1].Tools, []string{"a", "b"}) {
		t.Errorf("one rule twice: rule %q server %q tools %v, want r, x and a, b", got[1].Rule, got[1].Server, got[1].Tools)
	}
	if got[2].Rule != "" || got[2].Server != "" || got[2].Tools != nil {
		t.Errorf("two rules: rule %q server %q tools %v, want none", got[2].Rule, got[2].Server, got[2].Tools)
	}
}

// TestVerdictFieldsOnTheWire pins the JSON of a gain whose gate changes on
// both sides and of the risk that covers it, as a check writes them.
func TestVerdictFieldsOnTheWire(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	v := Check(w, stamped(w, devAccessWith("timeoutSeconds: 120", "class: ticket, deciders: [sponsor]")), CheckInput{Now: checkNow})
	var gain *Gain
	for i, g := range v.Gains {
		if g.Role == "dev" && g.Tool == "push_files" {
			gain = &v.Gains[i]
		}
	}
	var looser *Finding
	for i, f := range v.Risks {
		if f.Code == codeGateLooser && f.Object == "Role/dev" {
			looser = &v.Risks[i]
		}
	}
	if gain == nil || looser == nil {
		t.Fatalf("no gain of dev on push_files or no access.gate-looser of dev: %q %q", gainLines(v.Gains), riskLines(v.Risks))
	}
	gain.Holders = nil
	for _, tc := range []struct {
		name string
		v    any
		want string
	}{
		{"gain", gain, `{"role":"dev","server":"github","tool":"push_files","holders_count":3,"before":"needs-approval","after":"needs-approval",` +
			`"before_words":"a hold, up to 2 minutes, decided by sec-approvers","after_words":"a ticket good for 1 day, decided by sec-approvers or the agent's sponsor",` +
			`"before_gate":{"rule":"hold-push","set":"dev-access","class":"hold","seconds":120,"roles":["sec-approvers"],"sponsor":false,"requester":false,"confirm":false,` +
			`"within_seconds":0,"any_arguments":false,"classifier":false,"server_check":false},` +
			`"after_gate":{"rule":"hold-push","set":"dev-access","class":"ticket","seconds":86400,"roles":["sec-approvers"],"sponsor":true,"requester":false,"confirm":false,` +
			`"within_seconds":0,"any_arguments":false,"classifier":false,"server_check":false}}`},
		{"risk", looser, `{"code":"access.gate-looser","class":"risk","ack":"tick","object":"Role/dev",` +
			`"sentence":"push_files on github will be gated by a ticket good for 1 day, decided by sec-approvers or the agent's sponsor instead of a hold, up to 2 minutes, decided by sec-approvers.",` +
			`"before":"github/push_files=a hold, up to 2 minutes, decided by sec-approvers","after":"github/push_files=a ticket good for 1 day, decided by sec-approvers or the agent's sponsor",` +
			`"group":"access.gate-looser","rows":[{"server":"github","tool":"push_files"}]}`},
	} {
		body, err := json.Marshal(tc.v)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != tc.want {
			t.Errorf("%s\n got %s\nwant %s", tc.name, body, tc.want)
		}
	}
}

// TestRuleOfALiveSetKeepsItsToolsToItself pins that a line about a rule of a
// live set the draft does not write names the rule and the server, as its
// sentence does, and never the tool names the rule allows, which no word of
// the line says: an agent that drafts a server's removal would otherwise
// read the allow list of every live rule that names that server.
func TestRuleOfALiveSetKeepsItsToolsToItself(t *testing.T) {
	t.Parallel()
	w := withSet(gainWorld(), "payroll-access", gainSet("payroll-access", "{ roles: [readers] }",
		"    - id: pay-jira\n      tools: [mcp.call]\n      apps: [jira]\n      toolNames: { allow: [get_salary, approve_payroll] }\n      effect: allow\n"))
	v := Check(w, agentDraftOf(w, Item{Kind: KindApp, Name: "jira", Op: OpRemove}), CheckInput{Proposer: ciBot, Now: checkNow})
	var seen bool
	for _, f := range v.ForAgent().Warnings {
		if f.Code != codeRuleNothing || f.Object != "PolicySet/payroll-access" {
			continue
		}
		seen = true
		if f.Rule != "pay-jira" || f.Server != "jira" || len(f.Tools) != 0 {
			t.Errorf("the line carries rule %q, server %q and tools %v, want pay-jira, jira and no tools", f.Rule, f.Server, f.Tools)
		}
	}
	if !seen {
		t.Fatalf("no ready.rule-names-nothing line on payroll-access: refused %q, warnings %q", findingLines(v.Refused), findingLines(v.Warnings))
	}
}
