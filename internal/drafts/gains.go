package drafts

import (
	"slices"
	"sort"

	"github.com/strazahq/straza/internal/policy"
)

// nativeApp is the built-in straza app. Every session is offered its
// approval tools and policy alone decides who sees them. The two drafting
// tools are offered only to a holder of DraftConfigRole, and they count as
// granted for it.
const nativeApp = "straza"

var (
	nativeTools   = []string{"approval_await", "approval_request", "approval_status", "draft_status", "draft_submit"}
	draftingTools = map[string]bool{"draft_status": true, "draft_submit": true}
)

// outcomeRank orders the outcomes from narrowest to widest: not reachable
// and denied, then needs approval, then runs, and unknown above them all,
// because a tool list nobody read may hold anything.
var outcomeRank = map[Outcome]int{OutcomeNotReachable: 0, OutcomeDenied: 0, OutcomeApproval: 1, OutcomeRuns: 2, OutcomeUnknown: 3}

// gainRow is one Gain with the two probes behind it, which the risk and
// warning rules compare.
type gainRow struct {
	Gain
	b, a probe
}

// call is one tool of one server as a subject reaches it in each world:
// the outcome where no probe is needed, empty where the engine decides,
// and the granted fact the probe carries.
type call struct {
	server, tool string
	reach        [2]Outcome
	granted      [2]bool
}

// reached is one call and what a subject gets on it before and after the
// draft.
type reached struct {
	call
	p [2]probe
}

// whoGains answers who gains what for every touched role, sorted by
// role, server, tool and outcomes. Each role is read on its lane, the
// subject that holds its closure and nothing else, and each row names every
// holder of the role, counted in the world after the draft, or in live
// state for a role the draft removes.
func (c *check) whoGains() []gainRow {
	g := c.prober()
	var rows []gainRow
	for _, r := range c.touchedRoles() {
		lane := [2]policy.Subject{{Roles: c.closureW(r)}, {Roles: c.closureA(r)}}
		holders := c.holdersOf(r)
		for _, rc := range g.reach(lane) {
			if pairChanges(rc.p) {
				rows = append(rows, gainRow{Gain: Gain{Role: r, Server: rc.server, Tool: rc.tool, Holders: holders, HolderCount: len(holders),
					Before: rc.p[0].outcome, After: rc.p[1].outcome, BeforeWords: rc.p[0].words, AfterWords: rc.p[1].words}, b: rc.p[0], a: rc.p[1]})
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		x, y := rows[i].Gain, rows[j].Gain
		for _, p := range [][2]string{{x.Role, y.Role}, {x.Server, y.Server}, {x.Tool, y.Tool}, {string(x.Before), string(y.Before)},
			{string(x.After), string(y.After)}, {x.BeforeWords, y.BeforeWords}, {x.AfterWords, y.AfterWords}} {
			if p[0] != p[1] {
				return p[0] < p[1]
			}
		}
		return false
	})
	return rows
}

// touchedRoles answers, sorted, the roles whose reach the draft can change:
// every role whose closure in either world holds a role the
// draft changes, and every role a changed set applies to in either world,
// judged on the role's lane there. A set that starts or stops applying
// through an implication changes a role in the closure, so it is counted.
func (c *check) touchedRoles() []string {
	changed := c.changedRoles()
	sets := sortedKeys(c.changedSets())
	var out []string
	for _, r := range roleNames(c.w, c.after) {
		cw, ca := c.closureW(r), c.closureA(r)
		if meets(cw, changed) || meets(ca, changed) || slices.ContainsFunc(sets, func(name string) bool {
			b, hadB := c.docsW[name]
			a, hasA := c.docsA[name]
			return (hadB && appliesTo(b.Spec.Match, policy.Subject{Roles: cw})) || (hasA && appliesTo(a.Spec.Match, policy.Subject{Roles: ca}))
		}) {
			out = append(out, r)
		}
	}
	return out
}

// changedRoles names the roles the draft changes: a role it adds or
// removes, or whose access row, implications, or the server its row names
// differ between the worlds.
func (c *check) changedRoles() map[string]bool {
	apps := c.changedApps()
	out := map[string]bool{}
	for _, r := range roleNames(c.w, c.after) {
		_, inW := c.w.Roles[r]
		_, inA := c.after.Roles[r]
		if inW != inA || rowChanged(c.w, c.after, r) || !slices.Equal(sortedCopy(c.w.Implies[r]), sortedCopy(c.after.Implies[r])) ||
			apps[c.w.Access[r].Server] || apps[c.after.Access[r].Server] {
			out[r] = true
		}
	}
	return out
}

// rowChanged reports whether role's access row differs between w and
// after: added, removed, or naming another server or other tools.
func rowChanged(w, after World, role string) bool {
	rw, rowW := w.Access[role]
	ra, rowA := after.Access[role]
	return rowW != rowA || rw.Server != ra.Server || !slices.Equal(sortedCopy(rw.Tools), sortedCopy(ra.Tools))
}

// changedSets names the sets whose text differs between live state and
// the world after the draft, a set that exists on one side only included.
func (c *check) changedSets() map[string]bool {
	out := map[string]bool{}
	for name, p := range c.w.Policies {
		if q, ok := c.after.Policies[name]; !ok || q.Text != p.Text {
			out[name] = true
		}
	}
	for name := range c.after.Policies {
		if _, ok := c.w.Policies[name]; !ok {
			out[name] = true
		}
	}
	return out
}

// changedApps names the servers whose existence, runtime, address,
// exposure or tool list differs between the worlds.
func (c *check) changedApps() map[string]bool {
	out := map[string]bool{}
	for _, name := range appNames(c.w, c.after) {
		a, okA := c.w.Apps[name]
		b, okB := c.after.Apps[name]
		if okA != okB || !sameRuntime(a, b) || !slices.Equal(sortedCopy(a.Exposure), sortedCopy(b.Exposure)) || !slices.Equal(a.Offered, b.Offered) {
			out[name] = true
		}
	}
	return out
}

// closureW answers the closure of role in live state, nil when live state
// has no such role.
func (c *check) closureW(role string) []string {
	if _, ok := c.w.Roles[role]; !ok {
		return nil
	}
	return c.clW.of(role)
}

// closureA answers the closure of role after the draft, nil when the draft
// leaves no such role.
func (c *check) closureA(role string) []string {
	if _, ok := c.after.Roles[role]; !ok {
		return nil
	}
	return c.clA.of(role)
}

// holdersOf answers who holds role through any path, after the draft, or
// in live state for a role the draft removes. The list is shared, so the
// caller must not change it.
func (c *check) holdersOf(role string) []string {
	if _, ok := c.after.Roles[role]; ok {
		return c.hA.byRole[role]
	}
	return c.hW.byRole[role]
}

// reach answers what the subject pair sub gets before and after the draft
// on every tool of every server its rows reach in either world, and on the
// built-in app: one pair per call, and for a server nobody has listed after
// the draft, one pair on tool * with after unknown. Where nobody has listed
// the server before either, the pair is there only when the rows come to
// admit more names, both sides unknown and worded by their matchers. A
// server's last listed tools count whether it runs or not, the reach it has
// once it runs.
func (g *gainer) reach(sub [2]policy.Subject) []reached {
	c := g.c
	mw, ma := matchersOf(c.w, sub[0].Roles), matchersOf(c.after, sub[1].Roles)
	var out []reached
	for _, s := range sortedKeys(mw, ma) {
		bList, bKnown := toolsOf(c.w.Apps[s])
		aList, aKnown := toolsOf(c.after.Apps[s])
		if len(ma[s]) > 0 && !aKnown {
			if len(mw[s]) > 0 && !bKnown {
				if admitsMore(mw[s], ma[s]) {
					out = append(out, reached{call{server: s, tool: "*"}, [2]probe{{outcome: OutcomeUnknown, words: matcherWords(mw[s])},
						{outcome: OutcomeUnknown, words: matcherWords(ma[s])}}})
				}
				continue
			}
			best := probe{outcome: OutcomeNotReachable}
			for _, t := range bList {
				if !matchAnyGlob(mw[s], t) {
					continue
				}
				if p := g.eval(false, sub[0], s, t, true); outcomeRank[p.outcome] > outcomeRank[best.outcome] {
					best = p
				}
			}
			out = append(out, reached{call{server: s, tool: "*"}, [2]probe{best, {outcome: OutcomeUnknown}}})
			continue
		}
		for _, t := range unionSorted(bList, aList) {
			cl := call{server: s, tool: t, granted: [2]bool{true, true}}
			cl.reach[0] = reachOf(mw[s], bList, bKnown, t)
			cl.reach[1] = reachOf(ma[s], aList, aKnown, t)
			out = append(out, reached{cl, g.pair(cl, sub)})
		}
	}
	for _, t := range nativeTools {
		cl := call{server: nativeApp, tool: t}
		for i := range 2 {
			holds := slices.Contains(sub[i].Roles, DraftConfigRole)
			cl.granted[i] = draftingTools[t] && holds
			if draftingTools[t] && !holds {
				cl.reach[i] = OutcomeNotReachable
			}
		}
		out = append(out, reached{cl, g.pair(cl, sub)})
	}
	return out
}

// matcherWords names the tools a row's matchers admit on a server nobody
// has listed.
func matcherWords(matchers []string) string {
	return "tools matching " + listWords(slices.Compact(sortedCopy(matchers)), "or")
}

// pair probes cl for the subject pair sub, each side with the engine of
// its world where the call reaches the engine.
func (g *gainer) pair(cl call, sub [2]policy.Subject) [2]probe {
	var p [2]probe
	for i := range 2 {
		if cl.reach[i] != "" {
			p[i] = probe{outcome: cl.reach[i]}
			continue
		}
		p[i] = g.eval(i == 1, sub[i], cl.server, cl.tool, cl.granted[i])
	}
	return p
}

// pairChanges reports whether a pair's outcome or gate words differ.
func pairChanges(p [2]probe) bool {
	return p[0].outcome != p[1].outcome || p[0].words != p[1].words
}

// reachOf answers the outcome of a tool before any probe: not reachable
// when no matcher admits it or the server does not offer it, unknown when
// nobody read the server's list, and empty when the engine decides.
func reachOf(matchers, list []string, known bool, tool string) Outcome {
	switch {
	case !matchAnyGlob(matchers, tool):
		return OutcomeNotReachable
	case !known:
		return OutcomeUnknown
	case !slices.Contains(list, tool):
		return OutcomeNotReachable
	}
	return ""
}

// toolsOf answers the tools app offers its callers, sorted: the tools it
// last listed, cut by its exposure globs, whether it runs now or not, and
// false when nobody read its list.
func toolsOf(app App) ([]string, bool) {
	if app.Offered == nil {
		return nil, false
	}
	exposure := app.Exposure
	if len(exposure) == 0 {
		exposure = []string{"*"}
	}
	var out []string
	for _, t := range app.Offered {
		if matchAnyGlob(exposure, t) {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out, true
}
