package drafts

import (
	"fmt"
	"slices"

	"github.com/strazahq/straza/internal/policy"
)

// closureLoosenings answers the loosenings a change of role closures makes.
// For each role whose closure changes, a live set
// that applied to its holders through the closure and no longer does stops
// every gate and module in it for them, which is typed. An approval of a
// live set that starts to apply through the closure may take over a
// stricter approval of a set that applies to them throughout. A set's
// match is read as it stands after the draft, so only the closure change
// counts, and a set the draft creates is left to the set-level pass. A
// role the draft adds has no holders yet.
func (c *check) closureLoosenings() []loosening {
	var out []loosening
	for _, r := range roleNames(c.w, c.after) {
		cw, ca := c.closureW(r), c.closureA(r)
		if cw == nil || slices.Equal(cw, ca) {
			continue
		}
		var starting, throughout []string
		for _, name := range sortedKeys(c.docsA) {
			b, had := c.docsW[name]
			roles := c.docsA[name].Spec.Match.Roles
			inW, inA := reaches(cw, roles), reaches(ca, roles)
			switch {
			case !had:
			case inW && !inA:
				out = append(out, c.closureStops(r, name, b, roles, cw, ca)...)
			case inA && !inW:
				starting = append(starting, name)
			case inW:
				throughout = append(throughout, name)
			}
		}
		for _, t := range c.startTakeovers(starting, throughout, false) {
			gained := slices.DeleteFunc(slices.Clone(t.x.match.Roles), func(x string) bool { return !slices.Contains(ca, x) || slices.Contains(cw, x) })
			out = append(out, loosening{changed: t.x.set, object: t.x.set, lane: r, typed: t.self, before: ruleRef(t.y.rule), after: ruleRef(t.x.rule) + " for " + r,
				sentence: fmt.Sprintf("Rule %s of %s will gate %s in place of rule %s of %s for holders of %s, who will also hold %s, with %s where today it is %s.",
					t.x.rule.ID, t.x.set, sharedKinds(t.x.rule, t.y.rule), t.y.rule.ID, t.y.set, r, listWords(sortedCopy(gained), "and"), gateWords(t.x.rule), gateWords(t.y.rule))})
		}
	}
	return out
}

// reaches reports whether a match that selects roles reaches a session
// holding closure: a match with no role list selects whatever roles a
// session holds.
func reaches(closure, roles []string) bool {
	return len(roles) == 0 || slices.ContainsFunc(roles, func(r string) bool { return slices.Contains(closure, r) })
}

// closureStops answers the gates and module of the live set name, whose
// live document is b, that stop applying to the holders of role r because
// the roles of the set's match, roles, leave r's closure, cw before and ca
// after the draft.
func (c *check) closureStops(r, name string, b policy.Document, roles, cw, ca []string) []loosening {
	lost := slices.DeleteFunc(slices.Clone(roles), func(x string) bool { return !slices.Contains(cw, x) || slices.Contains(ca, x) })
	cause := "removes " + r
	if _, ok := c.after.Roles[r]; ok {
		cause = "removes " + listWords(sortedCopy(lost), "and")
		if slices.ContainsFunc(lost, func(x string) bool { _, kept := c.after.Roles[x]; return kept }) {
			cause = "takes " + listWords(sortedCopy(lost), "and") + " out of what " + r + " implies"
		}
	}
	var out []loosening
	for _, g := range gatesOf(b) {
		out = append(out, loosening{changed: name, object: name, lane: r, typed: true, sentence: stopWords(name, g, r, cause), before: ruleRef(g), after: "unbound for " + r})
	}
	if b.Spec.Escape != nil {
		out = append(out, loosening{changed: name, object: name, lane: r, typed: true, sentence: moduleWords(name, r, cause), before: moduleRef(b), after: "unbound for " + r})
	}
	return out
}

// startTakeover is an approval x of a set that starts to apply, which the
// engine takes before the stricter approval y of a set that applies before
// and after the draft; self marks an x the requester may approve.
type startTakeover struct {
	x, y approval
	self bool
}

// startTakeovers answers each approval of a set in starting that the
// engine takes before an approval of a set in throughout, on a call both
// can fire on, where the first is looser or lets the requester approve.
// local keeps the calls of the tool kinds of a managed machine alone.
func (c *check) startTakeovers(starting, throughout []string, local bool) []startTakeover {
	if len(starting) == 0 || len(throughout) == 0 {
		return nil
	}
	var ys []approval
	after := map[approvalKey]approval{}
	for _, name := range throughout {
		ys = append(ys, approvalsOf(name, c.docsW[name])...)
		for _, y := range approvalsOf(name, c.docsA[name]) {
			after[approvalKey{y.set, y.rule.ID}] = y
		}
	}
	var out []startTakeover
	for _, name := range starting {
		for _, x := range approvalsOf(name, c.docsA[name]) {
			for _, y := range ys {
				ya, ok := after[approvalKey{y.set, y.rule.ID}]
				if !ok || !x.outranks(ya) || !mayMeet(x, ya) || (local && !sharesLocal(x.rule, ya.rule)) {
					continue
				}
				was, now := gateProbe(y.rule), gateProbe(x.rule)
				if self := selfApproves(now) && !selfApproves(was); self || looser(was, now) {
					out = append(out, startTakeover{x, y, self})
				}
			}
		}
	}
	return out
}

// sharesLocal reports whether two rules both govern a tool kind of a
// managed machine.
func sharesLocal(x, y policy.Rule) bool {
	kinds := shared(toolKinds(x), toolKinds(y))
	return kinds == nil || slices.ContainsFunc(kinds, func(k string) bool { return slices.Contains(localTools, k) })
}

// subjectTakeovers answers the takeovers the subject pair subj meets on
// the tool kinds of a managed machine: an approval of a live set that
// starts to apply to it, taking over a stricter approval of a set that
// applies to it before and after the draft. Own reach reads
// mcp.call for the same subject.
func (c *check) subjectTakeovers(subj [2]policy.Subject) []startTakeover {
	var starting, throughout []string
	for _, name := range sortedKeys(c.docsA) {
		b, had := c.docsW[name]
		if !had {
			continue
		}
		appB, appA := appliesTo(b.Spec.Match, subj[0]), appliesTo(c.docsA[name].Spec.Match, subj[1])
		switch {
		case appA && !appB:
			starting = append(starting, name)
		case appA:
			throughout = append(throughout, name)
		}
	}
	return c.startTakeovers(starting, throughout, true)
}
