package drafts

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestForAgentKeepsWhatAnAgentMayRead pins what an agent reads: the
// refusals without draft.stale's publisher, the risks read from the rules,
// the warnings that tell it which names exist, and the unchecked lines,
// and never the risks and warnings that fire only for a role someone holds
// or tell of holders, devices and push, the passed and info lines, who
// gains, the needs or the digest.
func TestForAgentKeepsWhatAnAgentMayRead(t *testing.T) {
	t.Parallel()
	f := func(code string, class Class, sentence string) Finding {
		return Finding{Code: code, Class: class, Object: "Role/dev", Sentence: sentence}
	}
	v := Verdict{Draft: "41", Revision: 3,
		Refused: []Finding{f(codeStale, ClassRefused, "The role dev changed after this draft was checked, when bob published draft 40 at 2026-09-24 07:05 UTC."),
			f(codeImplyMissing, ClassRefused, "dev implies nosuch, which is not a role in Straza and is not created by this draft.")},
		Risks: []Finding{f(codeGuardrail, ClassRisk, "dev-access will stop denying mcp.call as rule no-delete does today, because this draft changes the rule."),
			f(codeUngated, ClassRisk, "get_me on github will run with no approval for holders of dev."), f(codeGated, ClassRisk, "gated"), f(codeGateLooser, ClassRisk, "looser"),
			f(codeDenyRemoved, ClassRisk, "deny"), f(codeSelfApproval, ClassRisk, "self"), f(codeNativeOpened, ClassRisk, "native"), f(codeImplication, ClassRisk, "implies"),
			f(codeToolsLater, ClassRisk, "later"), f("role.covers-wider", ClassRisk, "covers")},
		Warnings: []Finding{f(codeToolUnknown, ClassWarning, "dev names get_you, which github does not offer."), f(codeRuleNothing, ClassWarning, "Rule d of s names gitlab, which is not a registered server."),
			f(codePoolEmpty, ClassWarning, "pool"), f(codeDeciderDevice, ClassWarning, "device"), f(codeNoPush, ClassWarning, "push"),
			f(codeHoldShort, ClassWarning, "short"), f(codeNobodyConnected, ClassWarning, "connected"), f(codeRoleLoses, ClassWarning, "loses")},
		Unchecked: []Finding{f(codeUncheckedTools, ClassUnchecked, "unknown")},
		Passed:    []Finding{f(codePassedCompile, ClassPassed, "compiles")},
		Info:      []Finding{f(codeNobodyHolds, ClassInfo, "nobody")},
		Gains:     []Gain{{Role: "dev", Server: "github", Tool: "get_me", Holders: []string{"alice"}, HolderCount: 1}},
		Needs:     []Need{{Object: "Role/dev", Standing: needIdentity}}, RiskDigest: "abc"}
	got := v.ForAgent()
	want := AgentVerdict{Draft: "41", Revision: 3, Checked: true, Publishable: false,
		Refused:   []Finding{f(codeStale, ClassRefused, "The role dev changed after this draft was checked."), v.Refused[1]},
		Risks:     v.Risks[:1],
		Warnings:  v.Warnings[:2],
		Unchecked: v.Unchecked}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ForAgent\n got %+v\nwant %+v", got, want)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"bob", "alice", "gains", "needs", "passed", "info", "risk_digest"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("the agent's view holds %q: %s", leak, b)
		}
	}
}

// TestForAgentOfNothingRefused pins Publishable and the empty lists.
func TestForAgentOfNothingRefused(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(Verdict{Draft: "7", Revision: 1}.ForAgent())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"draft":"7","revision":1,"checked":true,"publishable":true,"refused":[],"risks":[],"warnings":[],"unchecked":[]}`
	if string(b) != want {
		t.Errorf("ForAgent = %s, want %s", b, want)
	}
}

// TestForAgentReadsItsOwnAccessRisks pins that the agent reads the risks of
// a draft that changes its own access, and reads the draft as publishable,
// because a person accepts those risks when publishing.
func TestForAgentReadsItsOwnAccessRisks(t *testing.T) {
	t.Parallel()
	w := guardWorld()
	got := Check(w, agentDraftOf(w, Item{Kind: KindPolicySet, Name: "dev-access", Op: OpRemove}), CheckInput{Proposer: ciBot, Now: checkNow}).ForAgent()
	if !got.Publishable || len(got.Refused) > 0 {
		t.Errorf("publishable %v, refused %q, want a draft a person may publish", got.Publishable, findingLines(got.Refused))
	}
	for _, code := range []string{codeAgentOwnReach, codeAgentGuardrail} {
		if !slices.Contains(codesOf(got.Risks), code) {
			t.Errorf("the agent reads the risks %q, want %s among them", codesOf(got.Risks), code)
		}
	}
}

// TestFindingsNameNoPersonAndNoCount pins over a real check that no finding
// sentence of a draft that raises risks, warnings and notes names a holder,
// and that adding holders changes who gains and not one sentence.
func TestFindingsNameNoPersonAndNoCount(t *testing.T) {
	t.Parallel()
	items := func(w World) Draft {
		return stamped(w, gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: ['*', get_you]\n"),
			gainRole("engineering", "    kind: business\n    implies: [dev, readers]\n"),
			Item{Kind: KindPolicySet, Name: "dev-access", Op: OpPut, Doc: strings.Replace(devAccess, "timeoutSeconds: 120", "class: ticket, deciders: [sponsor]", 1)},
			Item{Kind: KindPolicySet, Name: "readers-access", Op: OpPut, Doc: gainSet("readers-access", "{ roles: [readers] }",
				"    - id: hold\n      tools: [mcp.call]\n      toolNames: { allow: [push_files] }\n      mode: approve\n      approve: { roles: [sec-approvers] }\n")})
	}
	sentences := func(v Verdict) []string {
		var out []string
		for _, list := range [][]Finding{v.Refused, v.Risks, v.Warnings, v.Unchecked, v.Passed, v.Info} {
			for _, f := range list {
				out = append(out, f.Sentence, f.Fix, f.Before, f.After, f.Typed)
			}
		}
		return out
	}
	w1, w2 := gainWorld(), gainWorld()
	w1.Push = false
	w2.Push = false
	w2.Holders["readers"] = append(w2.Holders["readers"], Holder{Username: "zed", UserType: "human"}, Holder{Username: "yan", Agent: true, UserType: "agent"})
	v1, v2 := Check(w1, items(w1), CheckInput{Now: checkNow}), Check(w2, items(w2), CheckInput{Now: checkNow.Add(time.Minute)})
	if len(v1.Refused) > 0 || len(v1.Risks) < 3 || len(v1.Warnings) < 3 || len(v1.Gains) == 0 {
		t.Fatalf("the draft raised too little to pin anything: %q %q %q", findingLines(v1.Refused), codesOf(v1.Risks), codesOf(v1.Warnings))
	}
	for _, s := range sentences(v1) {
		for _, person := range []string{"alice", "bob", "carol", "ci-bot", "dana"} {
			if strings.Contains(s, person) {
				t.Errorf("a finding names %s: %q", person, s)
			}
		}
	}
	if !reflect.DeepEqual(sentences(v1), sentences(v2)) {
		t.Errorf("findings moved with the holders:\n%q\n%q", sentences(v1), sentences(v2))
	}
}

// TestForAgentKeepsTheFieldsBesideTheWords pins which fields beside a
// line's words reach the agent. The rows of who gains never do, because
// every risk that carries them fires only for a role someone holds. The
// rule, server and tools of a rule the agent's draft writes and the mark
// of a set that binds the agent do, because they name no person, count or
// pool, and the words of the same lines already say them.
func TestForAgentKeepsTheFieldsBesideTheWords(t *testing.T) {
	t.Parallel()
	named := func(code string, class Class) Finding {
		return Finding{Code: code, Class: class, Object: "PolicySet/dev-access", Sentence: code, Rule: "allow-chrome", Server: "claude-in-chrome", Tools: []string{"navigate"}}
	}
	rows := Finding{Code: codeUngated, Class: ClassRisk, Object: "Role/dev", Sentence: "rows", Rows: []ToolRow{{Server: "github", Tool: "get_me"}}}
	bound := Finding{Code: codeGuardrail, Class: ClassRisk, Object: "PolicySet/dev-access", Sentence: "bound", BindsProposer: true}
	v := Verdict{Risks: []Finding{rows, named(codeLocalAllow, ClassRisk), bound}, Warnings: []Finding{named(codeRuleNothing, ClassWarning)}}
	got := v.ForAgent()
	want := []Finding{named(codeLocalAllow, ClassRisk), bound}
	if !reflect.DeepEqual(got.Risks, want) || !reflect.DeepEqual(got.Warnings, v.Warnings) {
		t.Errorf("ForAgent reads\n%+v\n%+v\nwant\n%+v\n%+v", got.Risks, got.Warnings, want, v.Warnings)
	}
	w := guardWorld()
	real := Check(w, agentDraftOf(w, Item{Kind: KindPolicySet, Name: "dev-access", Op: OpRemove}), CheckInput{Proposer: ciBot, Now: checkNow})
	b, err := json.Marshal(real.ForAgent())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"rows"`) || !strings.Contains(string(b), `"binds_proposer":true`) || !strings.Contains(strings.Join(fieldLines(real), "\n"), "github/") {
		t.Errorf("the agent reads rows, or no set that binds it, or the check made no rows to leave out: %s", b)
	}
}
