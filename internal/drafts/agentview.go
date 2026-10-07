package drafts

// AgentVerdict is a verdict as the agent that proposed the draft may read
// it: the findings that let it fix its draft, and no person, count or
// approval pool behind them. The checker stores it as JSON in
// drafts.agent_verdict, and straza__draft_status answers it with the
// draft's state and next step.
type AgentVerdict struct {
	Draft       string    `json:"draft"`
	Revision    int       `json:"revision"`
	Checked     bool      `json:"checked"`
	Publishable bool      `json:"publishable"`
	Refused     []Finding `json:"refused"`
	Risks       []Finding `json:"risks"`
	Warnings    []Finding `json:"warnings"`
	Unchecked   []Finding `json:"unchecked"`
}

// agentHidden are the risks and warnings an agent does not read, because
// they tell who holds a role, who has a device, how a request reaches a
// person, or whether anyone holds a role at all: the risks of who gains, of
// an implication and of a row that reaches every tool, and the lines of the
// tools a role loses, fire only for a role someone holds.
// ready.tool-unknown, ready.rule-names-nothing and imply.missing stay,
// although they tell which servers, tools and roles exist, because an agent
// that drafts config must be able to fix a name.
var agentHidden = map[string]bool{codePoolEmpty: true, codeDeciderDevice: true, codeNoPush: true, codeHoldShort: true, codeNobodyConnected: true,
	codeRoleLoses: true, codeUngated: true, codeGated: true, codeGateLooser: true, codeDenyRemoved: true, codeSelfApproval: true, codeNativeOpened: true,
	codeImplication: true, codeToolsLater: true}

// ForAgent is v as the agent that proposed the draft may read it: its
// refusals, with the publisher clause of draft.stale dropped, the risks
// and warnings agentHidden leaves, and its unchecked lines. It drops the
// passed and info lines, who gains, the needs and the risk digest. Every
// list is empty rather than nil.
func (v Verdict) ForAgent() AgentVerdict {
	out := AgentVerdict{Draft: v.Draft, Revision: v.Revision, Checked: true, Publishable: len(v.Refused) == 0,
		Refused: []Finding{}, Risks: []Finding{}, Warnings: []Finding{}, Unchecked: append([]Finding{}, v.Unchecked...)}
	for _, f := range v.Refused {
		if f.Code == codeStale {
			f.Sentence = visible(f.Object) + " changed after this draft was checked."
		}
		out.Refused = append(out.Refused, f)
	}
	for _, list := range [][2]*[]Finding{{&v.Risks, &out.Risks}, {&v.Warnings, &out.Warnings}} {
		for _, f := range *list[0] {
			if !agentHidden[f.Code] {
				*list[1] = append(*list[1], f)
			}
		}
	}
	return out
}
