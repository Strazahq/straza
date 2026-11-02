package drafts

import (
	"slices"

	"github.com/strazahq/straza/internal/policy"
)

// grouped sets the Group of each risk of fs and answers fs. A risk's group
// is its code, so a publisher confirms the risks of one code together,
// except a server's removal and an agent.guardrail line, each confirmed on
// its own by its name. On an agent's draft the policy.guardrail-changed
// lines of a set that binds the agent carry no group either, because they
// are typed by the set's name beside the agent.guardrail line of that set,
// so the name is typed once.
func (c *check) grouped(fs []Finding) []Finding {
	bound := map[string]bool{}
	if agentDraft(c.d, c.in.Proposer) {
		subjs := c.proposerSubjects()
		for _, name := range sortedKeys(c.docsW, c.docsA) {
			if slices.ContainsFunc(subjs, func(subj [2]policy.Subject) bool { return c.binds(name, subj) }) {
				bound[visible("PolicySet/"+name)] = true
			}
		}
	}
	for i, f := range fs {
		switch {
		case f.Code == codeRemoval, f.Code == codeAgentGuardrail:
		case f.Code == codeGuardrail && bound[f.Object]:
		default:
			fs[i].Group = f.Code
		}
	}
	return fs
}
