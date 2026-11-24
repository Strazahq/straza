package drafts

import (
	"slices"

	"github.com/strazahq/straza/internal/policy"
)

// grouped sets the Group of each risk of fs, marks BindsProposer on each
// policy.guardrail-changed line of a set that applies to the proposer
// before or after the draft, and answers fs. A risk's group
// is its code, so a publisher confirms the risks of one code together,
// except a server's removal, an agent.guardrail line and an
// agent.own-reach line, each confirmed on its own by its name. On an
// agent's draft the policy.guardrail-changed
// lines of a set that binds the agent carry no group either, because they
// are typed by the set's name beside the agent.guardrail line of that set,
// so the name is typed once.
func (c *check) grouped(fs []Finding) []Finding {
	bound := map[string]bool{}
	subjs := c.proposerSubjects()
	for _, name := range sortedKeys(c.docsW, c.docsA) {
		if slices.ContainsFunc(subjs, func(subj [2]policy.Subject) bool { return c.binds(name, subj) }) {
			bound[visible("PolicySet/"+name)] = true
		}
	}
	agent := agentDraft(c.d, c.in.Proposer)
	for i, f := range fs {
		fs[i].BindsProposer = f.Code == codeGuardrail && bound[f.Object]
		switch {
		case f.Code == codeRemoval, f.Code == codeAgentGuardrail, f.Code == codeAgentOwnReach:
		case agent && fs[i].BindsProposer:
		default:
			fs[i].Group = f.Code
		}
	}
	return fs
}
