package drafts

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/strazahq/straza/internal/policy"
)

// The codes of the risks, and of the two agent refusals that read who
// gains and the sets that bind the agent.
const (
	codeRunsCode       = "server.runs-code"
	codeCredentialHost = "server.credential-host" // #nosec G101 -- a finding code, not a credential
	codeUnusualHost    = "server.unusual-host"
	codeAgentsCred     = "server.agents-credential"
	codeRemoval        = "server.removal"
	codeNewHost        = "server.new-host"
	codePlainHTTP      = "server.plain-http"
	codeExposureWider  = "server.exposure-wider"
	codeScopesWider    = "server.scopes-wider"
	codeSharedAccount  = "server.shared-account"
	codeStrazaReach    = "role.straza-reach"
	codeUngated        = "access.ungated"
	codeSelfApproval   = "access.self-approval"
	codeToolsLater     = "access.tools-later"
	codeGated          = "access.gated"
	codeDenyRemoved    = "access.deny-removed"
	codeGateLooser     = "access.gate-looser"
	codeImplication    = "access.implication"
	codeRecordingOn    = "policy.recording-on"
	codeRecordingOff   = "policy.recording-off"
	codeRego           = "policy.rego"
	codeGuardrail      = "policy.guardrail-changed"
	codeRequireLooser  = "policy.require-looser"
	codeNativeOpened   = "policy.native-opened"
	codeLocalAllow     = "policy.local-allow"
	codeIdentityScoped = "policy.identity-scoped-change"
	codeAgentGuardrail = "agent.guardrail"
	codeAgentOwnReach  = "agent.own-reach"
)

// typedRisks are the risks a publisher acknowledges by typing the risk's
// Typed text, because republishing the old state cannot undo them: code
// on the Straza host, a credential sent to a host the server has not
// used, calls sent over plain http where the server used https, a host
// strazad should not reach, a credential shared with agents, wider OAuth
// scopes, whose tokens stay valid when the scopes narrow again, reach into
// a Straza role, a held role gaining a tool that runs with no gate or that
// its requester may approve, recording turned on, a Rego module and a
// server removal. Every other risk is a tick, and policy.guardrail-changed
// is typed where its set applies to the proposer.
var typedRisks = map[string]bool{
	codeRunsCode: true, codeCredentialHost: true, codePlainHTTP: true, codeUnusualHost: true, codeAgentsCred: true, codeScopesWider: true,
	codeStrazaReach: true, codeUngated: true, codeSelfApproval: true, codeRecordingOn: true, codeRego: true, codeRemoval: true,
}

// agentsSponsor is the agents value that lets an agent without a
// credential of its own use its sponsor's connection.
const agentsSponsor = "sponsor"

// risk is a risk of code about object, acknowledged by typing typed when
// the code is typed and by a tick otherwise. Every text it carries is
// spelled with visible, because names, tools and addresses come from
// documents and from servers.
func risk(code, object, typed, sentence, before, after string) Finding {
	f := Finding{Code: code, Class: ClassRisk, Ack: AckTick, Object: visible(object), Sentence: visible(sentence), Before: visible(before), After: visible(after)}
	if typedRisks[code] {
		f.Ack, f.Typed = AckTyped, visible(typed)
	}
	return f
}

// risks answers step 6: the risks of who gains, of the roles, servers and
// sets the draft changes, folded by code and object.
func (c *check) risks(rows []gainRow) []Finding {
	out := gainRisks(rows)
	out = append(out, c.roleRisks()...)
	out = append(out, c.serverRisks()...)
	out = append(out, c.policyRisks()...)
	return foldRisks(out)
}

// foldRisks folds the risks of one code and object into one finding, in
// the order each pair first comes: its sentences, each once, joined by a
// space, and its Before and After words, each sorted and de-duplicated
// and joined with "; ". A typed part makes the whole typed.
func foldRisks(parts []Finding) []Finding {
	var out []Finding
	at := map[string]int{}
	var before, after [][]string
	for _, p := range parts {
		key := p.Code + "\n" + p.Object
		i, ok := at[key]
		if !ok {
			i = len(out)
			at[key] = i
			out, before, after = append(out, p), append(before, nil), append(after, nil)
		} else if !slices.Contains(strings.Split(out[i].Sentence, "\x00"), p.Sentence) {
			out[i].Sentence += "\x00" + p.Sentence
		}
		before[i], after[i] = append(before[i], p.Before), append(after[i], p.After)
		if p.Ack == AckTyped {
			out[i].Ack, out[i].Typed = AckTyped, p.Typed
		}
	}
	for i := range out {
		out[i].Sentence = strings.ReplaceAll(out[i].Sentence, "\x00", " ")
		out[i].Before, out[i].After = joinParts(before[i]), joinParts(after[i])
	}
	return out
}

// joinParts joins the non-empty words of parts, sorted and each once, with
// "; ". A part that already joins several words counts as each of them.
func joinParts(parts []string) string {
	var words []string
	for _, p := range parts {
		if p != "" {
			words = append(words, strings.Split(p, "; ")...)
		}
	}
	sort.Strings(words)
	return strings.Join(slices.Compact(words), "; ")
}

// toolKey collects the tools of one risk sentence: its code, role and
// server, and the gate words the sentence names.
type toolKey struct{ code, role, server, words string }

// gainRisks answers the risks who gains shows, for each row that describes
// a holder now: a tool that will run with no gate, a newly gated tool, a
// looser gate, a deny gone, a tool its requester may approve, and a tool
// of the built-in straza app opened.
func gainRisks(rows []gainRow) []Finding {
	var keys []toolKey
	tools := map[toolKey][]string{}
	add := func(k toolKey, tool string) {
		if _, ok := tools[k]; !ok {
			keys = append(keys, k)
		}
		if !slices.Contains(tools[k], tool) {
			tools[k] = append(tools[k], tool)
		}
	}
	for _, row := range rows {
		b, a := row.b, row.a
		reached := a.outcome == OutcomeRuns || a.outcome == OutcomeApproval
		switch {
		case row.HolderCount == 0:
		case row.Server == nativeApp:
			if outcomeRank[b.outcome] == 0 && reached {
				add(toolKey{code: codeNativeOpened, role: row.Role, server: nativeApp}, row.Tool)
			}
		default:
			switch {
			case a.outcome == OutcomeRuns && b.outcome != OutcomeRuns:
				add(toolKey{codeUngated, row.Role, row.Server, b.words}, row.Tool)
			case a.outcome == OutcomeApproval && (b.outcome == OutcomeNotReachable || b.outcome == OutcomeUnknown):
				add(toolKey{codeGated, row.Role, row.Server, a.words}, row.Tool)
			case a.outcome == OutcomeApproval && b.outcome == OutcomeApproval && looser(b, a):
				add(toolKey{codeGateLooser, row.Role, row.Server, b.words + "\x00" + a.words}, row.Tool)
			}
			if b.outcome == OutcomeDenied && reached {
				add(toolKey{codeDenyRemoved, row.Role, row.Server, b.words + "\x00" + string(a.outcome) + "\x00" + a.words}, row.Tool)
			}
			if a.outcome == OutcomeApproval && selfApproves(a) && !selfApproves(b) {
				add(toolKey{codeSelfApproval, row.Role, row.Server, ""}, row.Tool)
			}
		}
	}
	var out []Finding
	for _, k := range keys {
		ts := tools[k]
		object, names := "Role/"+k.role, listWords(ts, "and")
		switch k.code {
		case codeUngated:
			today := ""
			if k.words != "" {
				today = ", where today " + k.words
			}
			out = append(out, risk(k.code, object, k.role, fmt.Sprintf("%s on %s will run for holders of %s with no rule gating it%s.", names, k.server, k.role, today),
				toolList(k.server, ts, k.words), toolList(k.server, ts, "")))
		case codeGated:
			out = append(out, risk(k.code, object, "", fmt.Sprintf("%s will reach %s on %s, each call %s.", k.role, names, k.server, k.words), "", toolList(k.server, ts, k.words)))
		case codeGateLooser:
			bw, aw, _ := strings.Cut(k.words, "\x00")
			out = append(out, risk(k.code, object, "", fmt.Sprintf("%s on %s will be gated by %s instead of %s.", names, k.server, aw, bw), toolList(k.server, ts, bw), toolList(k.server, ts, aw)))
		case codeDenyRemoved:
			parts := strings.SplitN(k.words, "\x00", 3)
			will := "need approval, " + parts[2]
			if Outcome(parts[1]) == OutcomeRuns {
				will = strings.TrimSpace("run " + parts[2])
			}
			out = append(out, risk(k.code, object, "", fmt.Sprintf("No rule will refuse %s on %s for %s any more. It will %s.", names, k.server, k.role, will),
				toolList(k.server, ts, parts[0]), toolList(k.server, ts, parts[2])))
		case codeSelfApproval:
			out = append(out, risk(k.code, object, k.role, fmt.Sprintf("A person who asks to run %s on %s may approve it themself.", listWords(ts, "or"), k.server), "", toolList(k.server, ts, "")))
		case codeNativeOpened:
			out = append(out, risk(k.code, object, "", fmt.Sprintf("Holders of %s will see %s of the built-in straza MCP server.", k.role, names), "", toolList(nativeApp, ts, "")))
		}
	}
	return out
}

// toolList spells tools of server as `server/tool` pairs, sorted and
// joined by ", ", each followed by =words when words is set.
func toolList(server string, tools []string, words string) string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = server + "/" + t
		if words != "" {
			out[i] += "=" + words
		}
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// looser reports whether the approval of a is looser than that of b: the
// requester decides where someone else did, a ticket where there was a
// hold, a longer window, an approval that covers any arguments, or a pool
// that gains a decider.
func looser(b, a probe) bool {
	if b.approve == nil || a.approve == nil {
		return false
	}
	x, y := b.approve, a.approve
	ticket := func(s *policy.ApproveSpec) bool { return s.Class == policy.ClassTicket }
	switch {
	case a.confirm && !b.confirm, !ticket(x) && ticket(y):
		return true
	case ticket(x) && ticket(y) && (y.TicketTTLSeconds > x.TicketTTLSeconds || y.GrantTTLSeconds > x.GrantTTLSeconds):
		return true
	case !ticket(x) && !ticket(y) && (y.TimeoutSeconds > x.TimeoutSeconds || y.RetryTTLSeconds > x.RetryTTLSeconds):
		return true
	case x.Binding != policy.ApproveBindingTool && y.Binding == policy.ApproveBindingTool:
		return true
	}
	before := pool(b)
	return slices.ContainsFunc(pool(a), func(d string) bool { return !slices.Contains(before, d) })
}

// selfApproves reports whether the approval behind p lets the requester
// approve their own call.
func selfApproves(p probe) bool {
	return p.approve != nil && p.approve.SelfApproval && !p.confirm
}

// wider reports whether a reaches more than b (agent.own-reach), weighing
// the whole gate and not the outcome alone: a higher outcome, a classifier
// or server check dropped, or an approval that is looser or that its
// requester may give.
func wider(b, a probe) bool {
	if rb, ra := outcomeRank[b.outcome], outcomeRank[a.outcome]; rb != ra {
		return ra > rb
	}
	return checkDropped(b, a) || (a.outcome == OutcomeApproval && b.outcome == OutcomeApproval && (looser(b, a) || (selfApproves(a) && !selfApproves(b))))
}

// checkDropped reports whether b's call waits for a classifier or the
// server and a's does not.
func checkDropped(b, a probe) bool {
	return (b.classify && !a.classify) || (b.serverCheck && !a.serverCheck)
}

// ownReach refuses an agent's draft that widens what the agent itself
// reaches (agent.own-reach), read over its whole subject in each world:
// the roles it holds directly, each closed over that world's implications,
// its username and its typology. It answers one refusal for each server
// and way the reach widens. A server nobody has listed on either side
// widens when the agent's rows on it come to admit more names.
func (c *check) ownReach() []Finding {
	sub := c.proposerSubjects()
	var keys []toolKey
	tools := map[toolKey][]string{}
	add := func(server, how, tool string) {
		k := toolKey{server: server, words: how}
		if _, ok := tools[k]; !ok {
			keys = append(keys, k)
		}
		tools[k] = append(tools[k], tool)
	}
	for _, rc := range c.prober().reach(sub) {
		if !wider(rc.p[0], rc.p[1]) {
			continue
		}
		how := "looser"
		switch {
		case rc.p[1].outcome == OutcomeUnknown:
			how = string(rc.p[1].outcome)
		case outcomeRank[rc.p[0].outcome] == outcomeRank[rc.p[1].outcome] && checkDropped(rc.p[0], rc.p[1]):
			how = "check"
		case rc.p[1].outcome == OutcomeRuns:
			how = string(rc.p[1].outcome)
		case outcomeRank[rc.p[0].outcome] == 0:
			how = "approval"
		}
		add(rc.server, how, rc.tool)
	}
	mw, ma := matchersOf(c.w, sub[0].Roles), matchersOf(c.after, sub[1].Roles)
	for _, s := range sortedKeys(ma) {
		_, bKnown := toolsOf(c.w.Apps[s])
		_, aKnown := toolsOf(c.after.Apps[s])
		if !bKnown && !aKnown && len(mw[s]) > 0 && slices.ContainsFunc(ma[s], func(m string) bool { return !slices.Contains(mw[s], m) }) {
			add(s, string(OutcomeUnknown), "*")
		}
	}
	var out []Finding
	for _, k := range keys {
		names, where, object := listWords(tools[k], "and"), "on "+k.server, "App/"+k.server
		if k.server == nativeApp {
			where, object = "of the built-in straza MCP server", ""
		}
		words := fmt.Sprintf("reach %s %s under a looser approval", names, where)
		switch k.words {
		case string(OutcomeUnknown):
			words = "reach tools of " + k.server + " that nobody has listed yet"
		case string(OutcomeRuns):
			words = fmt.Sprintf("run %s %s with no approval", names, where)
		case "approval":
			words = fmt.Sprintf("reach %s %s with an approval", names, where)
		case "check":
			words = fmt.Sprintf("reach %s %s without a check a classifier or the server makes today", names, where)
		}
		out = append(out, refusal(codeAgentOwnReach, visible(object), visible("This draft would let you "+words+", which widens your own reach. An agent's draft may not."),
			"Leave that change out. A person can make it."))
	}
	return out
}

// roleRisks answers the risks of the roles after the draft: a Straza role
// newly reached, whether or not anyone holds the role yet, because the
// identity manager can assign it at any time, and for a role someone holds,
// an implication added, a row that newly reaches every tool of a server,
// and a row on a server nobody has listed that is new there or whose
// matchers come to admit more names, where who gains cannot name the tools.
func (c *check) roleRisks() []Finding {
	var out []Finding
	for _, r := range sortedKeys(c.after.Roles) {
		object, cw := "Role/"+r, c.closureW(r)
		for _, x := range c.closureA(r) {
			if ro := c.after.Roles[x]; x != r && ro.Plane == PlaneControl && !slices.Contains(cw, x) {
				out = append(out, risk(codeStrazaReach, object, r, strazaReachSentence(r, ro), "", x))
			}
		}
		if len(c.hA.byRole[r]) == 0 {
			continue
		}
		for _, x := range c.after.Implies[r] {
			if !slices.Contains(c.w.Implies[r], x) && c.after.Roles[x].Plane != PlaneControl {
				out = append(out, risk(codeImplication, object, "", fmt.Sprintf("Holders of %s will also hold %s and reach what it reaches.", r, x), "", x))
			}
		}
		acc, ok := c.after.Access[r]
		old, had := c.w.Access[r]
		switch {
		case ok && acc.Server != "" && everyTool(acc.Tools) && (!had || old.Server != acc.Server || !everyTool(old.Tools)):
			out = append(out, risk(codeToolsLater, object, "", fmt.Sprintf("%s will reach every tool on %s, including tools the server adds later.", r, acc.Server),
				strings.Join(sortedCopy(old.Tools), ", "), strings.Join(sortedCopy(acc.Tools), ", ")))
		case ok && had && acc.Server != "" && old.Server == acc.Server && c.unlisted(acc.Server) && admitsMore(old.Tools, acc.Tools):
			out = append(out, risk(codeToolsLater, object, "", fmt.Sprintf("%s will reach more tools on %s, which nobody has listed yet, so Straza cannot name them.", r, acc.Server),
				strings.Join(sortedCopy(old.Tools), ", "), strings.Join(sortedCopy(acc.Tools), ", ")))
		case ok && acc.Server != "" && (!had || old.Server != acc.Server) && c.unlisted(acc.Server):
			out = append(out, risk(codeToolsLater, object, "", fmt.Sprintf("%s will reach tools on %s, which nobody has listed yet, so Straza cannot name them.", r, acc.Server),
				strings.Join(sortedCopy(old.Tools), ", "), strings.Join(sortedCopy(acc.Tools), ", ")))
		}
	}
	return append(out, c.todaysToolsRisks()...)
}

// unlisted reports whether nobody has listed the tools of server, before
// the draft or after it.
func (c *check) unlisted(server string) bool {
	_, before := toolsOf(c.w.Apps[server])
	_, after := toolsOf(c.after.Apps[server])
	return !before && !after
}

// strazaReachSentence says that holders of role will also hold the Straza
// role ro.
func strazaReachSentence(role string, ro Role) string {
	switch {
	case ro.Name == AdminRole:
		return fmt.Sprintf("Holders of %s will also hold %s, which is full control of Straza.", role, ro.Name)
	case ro.Description == "":
		return fmt.Sprintf("Holders of %s will also hold %s, a Straza role, which governs Straza itself.", role, ro.Name)
	}
	desc := ro.Description
	if !strings.HasSuffix(desc, ".") {
		return fmt.Sprintf("Holders of %s will also hold %s, a Straza role described as \"%s\".", role, ro.Name, desc)
	}
	return fmt.Sprintf("Holders of %s will also hold %s, a Straza role described as \"%s\"", role, ro.Name, desc)
}

// todaysToolsRisks answers access.tools-later for a held role whose row
// reaches every tool of a server while a gate the draft writes lists that
// server's tools of today by name, so a tool the server adds later runs
// without the gate.
func (c *check) todaysToolsRisks() []Finding {
	var out []Finding
	for _, name := range sortedKeys(c.changedSets()) {
		a, ok := c.docsA[name]
		if !ok {
			continue
		}
		for _, rule := range a.Spec.Rules {
			if !gateMode(rule) || len(rule.Apps) != 1 || rule.ToolNames == nil {
				continue
			}
			s := rule.Apps[0]
			tools, known := toolsOf(c.after.Apps[s])
			if !known || len(tools) == 0 || !namesEvery(rule.ToolNames.Allow, tools) {
				continue
			}
			if b, had := c.docsW[name]; had {
				if i := ruleIndex(b, rule.ID); i >= 0 && sameRule(b.Spec.Rules[i], rule) {
					continue
				}
			}
			for _, r := range sortedKeys(c.after.Access) {
				acc := c.after.Access[r]
				if acc.Server != s || !everyTool(acc.Tools) || len(c.hA.byRole[r]) == 0 || !appliesTo(a.Spec.Match, policy.Subject{Roles: c.closureA(r)}) {
					continue
				}
				words := strings.Join(sortedCopy(acc.Tools), ", ")
				out = append(out, risk(codeToolsLater, "Role/"+r, "", fmt.Sprintf("%s will reach every tool on %s, including tools the server adds later.", r, s), words, words))
			}
		}
	}
	return out
}

// namesEvery reports whether patterns name tools by literal name only and
// name every one of tools.
func namesEvery(patterns, tools []string) bool {
	for _, p := range patterns {
		if strings.Contains(p, "*") || strings.HasPrefix(p, "re:") {
			return false
		}
	}
	return !slices.ContainsFunc(tools, func(t string) bool { return !slices.Contains(patterns, t) })
}
