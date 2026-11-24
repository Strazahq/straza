package drafts

import (
	"reflect"
	"slices"
	"testing"
)

// widenReaders is a person's draft that raises gains, risks, warnings and
// notes: readers reaches every tool of github.
func widenReaders(w World) Draft {
	return stamped(w, gainRole("readers", "    kind: application\n    bindings:\n        - app: github\n          tools: ['*']\n"))
}

// TestCheckRefusalsOnlyStopsAfterTheRefusals pins RefusalsOnly: with
// RefusalsOnly a person's draft gets its refusals and needs, and no
// who-gains table, risks, warnings or notes, and its zero value runs every
// step.
func TestCheckRefusalsOnlyStopsAfterTheRefusals(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	full := Check(w, widenReaders(w), CheckInput{Now: checkNow})
	if len(full.Gains) == 0 || len(full.Risks) == 0 || len(full.Warnings) == 0 || len(full.Info)+len(full.Passed) == 0 {
		t.Fatalf("the zero value skipped a step: gains %d, risks %q, warnings %q", len(full.Gains), codesOf(full.Risks), codesOf(full.Warnings))
	}
	agent := agentDraftOf(w, widenReaders(w).Items...)
	for _, short := range []Verdict{Check(w, widenReaders(w), CheckInput{Now: checkNow, RefusalsOnly: true}),
		Check(w, agent, CheckInput{Proposer: ciBot, Now: checkNow, RefusalsOnly: true})} {
		for name, list := range map[string]int{"refused": len(short.Refused), "gains": len(short.Gains), "risks": len(short.Risks),
			"warnings": len(short.Warnings), "unchecked": len(short.Unchecked), "passed": len(short.Passed), "info": len(short.Info)} {
			if list != 0 {
				t.Errorf("RefusalsOnly answered %d %s", list, name)
			}
		}
		if !reflect.DeepEqual(short.Needs, full.Needs) || short.Gains == nil || short.Risks == nil {
			t.Errorf("RefusalsOnly needs %+v, want %+v, and empty lists rather than nil", short.Needs, full.Needs)
		}
	}
	refused := Check(w, stamped(w, gainRole("readers", "    kind: business\n")), CheckInput{Now: checkNow, RefusalsOnly: true})
	if !slices.Contains(codesOf(refused.Refused), codeRoleKindChange) {
		t.Errorf("RefusalsOnly skipped a refusal: %q", findingLines(refused.Refused))
	}
}

// TestCheckRefusedDraftGetsNoWhoGains pins that a refusal from who gains,
// agent.own-reach on a direct route, ends the check with no table and no
// risks, and that the same draft, reviewed, is not refused and reads its
// table beside the agent.own-reach risk.
func TestCheckRefusedDraftGetsNoWhoGains(t *testing.T) {
	t.Parallel()
	w := gainWorld()
	d := agentDraftOf(w, gainRole("dev", "    kind: application\n    bindings:\n        - app: github\n          tools: ['*']\n"),
		gainRole("engineering", "    kind: business\n    implies: [dev, jira-tickets]\n"),
		gainRole("jira-tickets", "    kind: application\n    server: jira\n    bindings:\n        - app: jira\n          tools: [create_issue]\n"))
	v := Check(w, d, CheckInput{Proposer: ciBot, Now: checkNow, Direct: true})
	if !slices.Contains(codesOf(v.Refused), codeAgentOwnReach) || len(v.Gains) != 0 || len(v.Risks) != 0 || len(v.Warnings) != 0 {
		t.Errorf("on a direct route: refused %q, gains %d, risks %q, warnings %q", codesOf(v.Refused), len(v.Gains), codesOf(v.Risks), codesOf(v.Warnings))
	}
	v = Check(w, d, CheckInput{Proposer: ciBot, Now: checkNow})
	if len(v.Refused) != 0 || !slices.Contains(codesOf(v.Risks), codeAgentOwnReach) || len(v.Gains) == 0 {
		t.Errorf("a reviewed draft: refused %q, gains %d, risks %q", codesOf(v.Refused), len(v.Gains), codesOf(v.Risks))
	}
}
