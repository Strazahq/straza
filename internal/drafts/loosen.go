package drafts

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/strazahq/straza/internal/policy"
)

// loosening is one way the draft loosens a gating rule, read from the
// rules of the sets and the role graph alone, never from who holds what.
// changed is the set whose change loosens, which an agent may not
// make while that set binds it, and object the set the finding names.
// clause is the words of agent.guardrail, and sentence, before and after
// those of policy.guardrail-changed. typed marks a loosening a publisher
// types. lane names the role whose closure change loosens, empty for a
// set change; the agent's own roles are judged on its subject instead.
type loosening struct {
	changed, object  string
	clause, sentence string
	before, after    string
	typed            bool
	lane             string
}

// loosenings answers every loosening the draft makes, read once per check:
// for each set it changes, the gates and the Rego module that stop
// applying or gate more loosely, every approval that takes over a
// stricter one, and what a change of role closures stops or lets take
// over.
func (c *check) loosenings() []loosening {
	if c.looseDone {
		return c.loose
	}
	changed := c.changedSets()
	ops := setOps(c.d)
	for _, name := range sortedKeys(changed) {
		c.loose = append(c.loose, c.setLoosenings(name, ops[name])...)
	}
	c.loose = append(c.loose, c.takeovers(changed)...)
	c.loose, c.looseDone = append(c.loose, c.closureLoosenings()...), true
	return c.loose
}

// setLoosenings answers the loosenings of the live set name, which the
// draft changes with op. Every gate and the module stop applying when the
// set goes or its match narrows, a gate loosens when the draft removes it
// or changes it, and the module when the draft strips or rewrites it. A
// set text that does not parse is left to its parse refusal.
func (c *check) setLoosenings(name string, op Op) []loosening {
	b, hadB := c.docsW[name]
	a, hasA := c.docsA[name]
	if _, stored := c.after.Policies[name]; !hadB || (stored && !hasA) {
		return nil
	}
	var out []loosening
	stop := func(cause, clause, after string) {
		for _, g := range gatesOf(b) {
			out = append(out, loosening{name, name, clause, stopWords(name, g, "", cause), ruleRef(g), cmp.Or(after, ruleRef(g)), true, ""})
		}
		if b.Spec.Escape != nil {
			out = append(out, loosening{name, name, clause, moduleWords(name, "", cause), moduleRef(b), cmp.Or(after, moduleRef(b)), true, ""})
		}
	}
	switch {
	case !hasA && op == OpOff:
		stop("turns it off", "turns it off", "off")
		return out
	case !hasA:
		stop("removes the set", "removes it", "removed")
		return out
	case matchNarrows(b.Spec.Match, a.Spec.Match):
		stop("changes its match or priority", "changes its match or priority", "")
	}
	for _, r := range b.Spec.Rules {
		out = append(out, ruleLoosening(name, r, a)...)
	}
	if e := b.Spec.Escape; e != nil && (a.Spec.Escape == nil || a.Spec.Escape.Rego != e.Rego) {
		cause, clause, after := "removes the module", "removes its Rego module", "rego@removed"
		if a.Spec.Escape != nil {
			cause, clause, after = "changes the module", "changes its Rego module", moduleRef(a)
		}
		out = append(out, loosening{name, name, clause, moduleWords(name, "", cause), moduleRef(b), after, true, ""})
	}
	return out
}

// ruleLoosening answers the loosening of the gate r of the live set name
// in a, the set after the draft: r removed, or changed so that it fires on
// fewer calls, no longer denies or no longer gates, which is typed, or so
// that it gates more loosely, which is a tick unless the requester may
// now approve. A require predicate is left to requireLooser.
func ruleLoosening(name string, r policy.Rule, a policy.Document) []loosening {
	if !isGate(r) {
		return nil
	}
	l := loosening{changed: name, object: name, clause: "changes rule " + r.ID, before: ruleRef(r)}
	i := ruleIndex(a, r.ID)
	if i < 0 {
		l.sentence, l.after, l.typed = stopWords(name, r, "", "changes the rule"), r.ID+"@removed", true
		return []loosening{l}
	}
	b, n := r, a.Spec.Rules[i]
	l.after = ruleRef(n)
	b.Reason, n.Reason, b.Require, n.Require = "", "", nil, nil
	self := selfApproves(gateProbe(n)) && !selfApproves(gateProbe(b))
	switch {
	case sameRule(b, n):
		return nil
	case firesLess(b, n) || (len(patternsOf(b)) == 0 && b.Effect == policy.EffectDeny && n.Effect != policy.EffectDeny) || (gateMode(b) && !gateMode(n) && canAllow(n)):
		l.sentence, l.typed = stopWords(name, r, "", "changes the rule"), true
	case gateMode(b) && gateMode(n) && (b.Mode != n.Mode || looser(gateProbe(b), gateProbe(n)) || self):
		l.sentence, l.typed = fmt.Sprintf("Rule %s of %s will gate %s with %s where today it is %s.", r.ID, name, kindsWords(n), gateWords(n), gateWords(b)), self
	default:
		return nil
	}
	return []loosening{l}
}

// pattern is one pattern of a rule with its block and side, so a pattern
// moved from one side to the other reads as dropped and as gained.
type pattern struct{ block, side, text string }

// patternsOf answers every pattern of r.
func patternsOf(r policy.Rule) []pattern {
	var out []pattern
	add := func(block string, allow, deny []string) {
		for _, p := range allow {
			out = append(out, pattern{block, "allow", p})
		}
		for _, p := range deny {
			out = append(out, pattern{block, "deny", p})
		}
	}
	if r.Command != nil {
		add("command", r.Command.AllowPatterns, r.Command.DenyPatterns)
	}
	for _, ad := range []struct {
		block string
		lists *policy.AllowDeny
	}{{"paths", r.Paths}, {"toolNames", r.ToolNames}, {"interpreters", r.Interpreters}} {
		if ad.lists != nil {
			add(ad.block, ad.lists.Allow, ad.lists.Deny)
		}
	}
	return out
}

// firesLess reports whether rule b, changed into a, leaves out a call b
// fires on: an event, a tool kind or an app dropped, patterns where there
// were none, or a pattern dropped from either side.
func firesLess(b, a policy.Rule) bool {
	lost := func(was, now []string) bool {
		return len(now) > 0 && (len(was) == 0 || slices.ContainsFunc(was, func(v string) bool { return !slices.Contains(now, v) }))
	}
	was, now := patternsOf(b), patternsOf(a)
	return lost(b.Events, a.Events) || lost(b.Tools, a.Tools) || lost(b.Apps, a.Apps) || (len(was) == 0 && len(now) > 0) ||
		slices.ContainsFunc(was, func(p pattern) bool { return !slices.Contains(now, p) })
}

// firesMore reports whether rule b, changed into a, can hold a call b did
// not: an event, a tool kind or an app gained, no patterns where there
// were some, or an allow-side pattern gained.
func firesMore(b, a policy.Rule) bool {
	gained := func(was, now []string) bool {
		return len(was) > 0 && (len(now) == 0 || slices.ContainsFunc(now, func(v string) bool { return !slices.Contains(was, v) }))
	}
	was, now := patternsOf(b), patternsOf(a)
	return gained(b.Events, a.Events) || gained(b.Tools, a.Tools) || gained(b.Apps, a.Apps) || (len(was) > 0 && len(now) == 0) ||
		slices.ContainsFunc(now, func(p pattern) bool { return p.side == "allow" && !slices.Contains(was, p) })
}

// approval is an approve or confirm rule that can hold a blocking call, as
// the engine ranks it: its set, the set's priority and match, its place in
// the set, and the rule.
type approval struct {
	set      string
	priority int
	match    policy.Match
	index    int
	rule     policy.Rule
}

// outranks reports whether the engine takes x's approval before y's when
// both fire: a higher priority, then the earlier set name, then the
// earlier place in the set, as the policy engine's tie-break reads.
func (x approval) outranks(y approval) bool {
	if x.priority != y.priority {
		return x.priority > y.priority
	}
	if x.set != y.set {
		return x.set < y.set
	}
	return x.index < y.index
}

// approvals answers the approve and confirm rules of docs that can hold a
// blocking call, by set name and then place.
func approvals(docs map[string]policy.Document) []approval {
	var out []approval
	for _, name := range sortedKeys(docs) {
		out = append(out, approvalsOf(name, docs[name])...)
	}
	return out
}

// approvalsOf answers the approve and confirm rules of the set name, whose
// document is doc, that can hold a blocking call, in place order.
func approvalsOf(name string, doc policy.Document) []approval {
	var out []approval
	for i, r := range doc.Spec.Rules {
		if (r.Mode == policy.ModeApprove || r.Mode == policy.ModeConfirm) && canAllow(r) && blocks(r) {
			out = append(out, approval{set: name, priority: doc.Spec.Priority, match: doc.Spec.Match, index: i, rule: r})
		}
	}
	return out
}

// approvalKey names an approval by its set and rule id.
type approvalKey struct{ set, id string }

// indexOf answers list by set and rule id.
func indexOf(list []approval) map[approvalKey]approval {
	out := make(map[approvalKey]approval, len(list))
	for _, x := range list {
		out[approvalKey{x.set, x.rule.ID}] = x
	}
	return out
}

// takeovers answers the approvals the draft lets take over a stricter one.
// Among the approvals that fire on a call,
// the engine takes the one that outranks the others, whatever holds the
// session, so for every approval x after the draft and every live approval
// y, one of them in a set the draft changes, x takes over y when x
// outranks y after the draft, both can fire on one call for one session,
// and live state did not already give x those calls; it loosens when its
// gate is looser or lets the requester approve. A third approval that
// outranks both is not looked for, so a takeover it hides still counts.
// Only pairs with a changed side are read, each lookup by index.
func (c *check) takeovers(changed map[string]bool) []loosening {
	before, after := approvals(c.docsW), approvals(c.docsA)
	bi, ai := indexOf(before), indexOf(after)
	// Each live approval y is paired with its version after the draft
	// once, for the pairs to read without a lookup.
	type live struct{ y, ya approval }
	var all, changedLive []live
	for _, y := range before {
		if ya, ok := ai[approvalKey{y.set, y.rule.ID}]; ok {
			all = append(all, live{y, ya})
			if changed[y.set] {
				changedLive = append(changedLive, live{y, ya})
			}
		}
	}
	var out []loosening
	for _, x := range after {
		ys := changedLive
		if changed[x.set] {
			ys = all
		}
		xb, had := bi[approvalKey{x.set, x.rule.ID}]
		widened := !had || firesMore(xb.rule, x.rule) || matchWidens(xb.match, x.match)
		for _, p := range ys {
			y, ya := p.y, p.ya
			if !x.outranks(ya) || (!widened && xb.outranks(y)) || (x.set == y.set && x.rule.ID == y.rule.ID) || !mayMeet(x, ya) {
				continue
			}
			was, now := gateProbe(y.rule), gateProbe(x.rule)
			self := selfApproves(now) && !selfApproves(was)
			if !self && !looser(was, now) {
				continue
			}
			l := loosening{x.set, x.set, "adds or widens rule " + x.rule.ID, fmt.Sprintf("Rule %s of %s will gate %s in place of rule %s of %s, with %s where today it is %s.",
				x.rule.ID, x.set, sharedKinds(x.rule, y.rule), y.rule.ID, y.set, gateWords(x.rule), gateWords(y.rule)), ruleRef(y.rule), ruleRef(x.rule), self, ""}
			if !changed[x.set] {
				l.changed, l.object, l.clause = y.set, y.set, "changes its match or priority"
			}
			out = append(out, l)
		}
	}
	return out
}

// mayMeet reports whether one call of one session can make both approvals
// fire: some session can meet both matches, and the rules share an event,
// a tool kind and, where they meet only on mcp.call, an app and a tool
// name. A role list never rules a session out, since a session can hold
// roles of both lists, and patterns other than tool names count as
// matching every call.
func mayMeet(x, y approval) bool {
	meet := func(a, b []string) bool {
		return len(a) == 0 || len(b) == 0 || slices.ContainsFunc(a, func(v string) bool { return slices.Contains(b, v) })
	}
	apart := func(a, b []string) bool { return len(a) > 0 && len(b) > 0 && !meet(a, b) }
	xi, yi := identityLists(x.match), identityLists(y.match)
	if apart(x.match.Users, y.match.Users) || apart(xi[0], yi[0]) || apart(xi[1], yi[1]) || apart(xi[2], yi[2]) || !meet(x.rule.Events, y.rule.Events) {
		return false
	}
	kinds := shared(toolKinds(x.rule), toolKinds(y.rule))
	switch {
	case kinds != nil && len(kinds) == 0:
		return false
	case kinds == nil || slices.ContainsFunc(kinds, func(k string) bool { return k != policy.ToolMCPCall }):
		return true
	}
	return meet(x.rule.Apps, y.rule.Apps) && namesMeet(x.rule, y.rule)
}

// shared answers the tool kinds two rules both govern, nil for every kind.
func shared(a, b []string) []string {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	return slices.DeleteFunc(slices.Clone(a), func(k string) bool { return !slices.Contains(b, k) })
}

// sharedKinds words the tool kinds two rules both govern for a sentence.
func sharedKinds(x, y policy.Rule) string {
	kinds := shared(toolKinds(x), toolKinds(y))
	if kinds == nil {
		return "every tool"
	}
	return listWords(kinds, "and")
}

// namesMeet reports whether one tool name can make both rules hold a call:
// a rule that holds by no tool name pattern holds every name, and two
// patterns meet unless neither is a regular expression and their literal
// starts or ends rule a common name out.
func namesMeet(x, y policy.Rule) bool {
	names := func(r policy.Rule) []string {
		if r.ToolNames == nil {
			return nil
		}
		return r.ToolNames.Allow
	}
	xs, ys := names(x), names(y)
	if len(xs) == 0 || len(ys) == 0 {
		return true
	}
	literal := func(p string) (prefix, suffix string, wild bool) {
		i := strings.IndexAny(p, "*?")
		if i < 0 {
			return p, p, false
		}
		return p[:i], p[strings.LastIndexAny(p, "*?")+1:], true
	}
	return slices.ContainsFunc(xs, func(p string) bool {
		return slices.ContainsFunc(ys, func(q string) bool {
			if strings.HasPrefix(p, "re:") || strings.HasPrefix(q, "re:") {
				return true
			}
			pp, ps, pw := literal(p)
			qp, qs, qw := literal(q)
			if !pw && !qw {
				return p == q
			}
			return (strings.HasPrefix(pp, qp) || strings.HasPrefix(qp, pp)) && (strings.HasSuffix(ps, qs) || strings.HasSuffix(qs, ps))
		})
	})
}

// gateWords spells the gate r asks for: its approval, or the check a
// classifier or the server makes.
func gateWords(r policy.Rule) string {
	switch r.Mode {
	case policy.ModeClassify:
		return "a classifier's check"
	case policy.ModeServerCheck:
		return "the server's own check"
	}
	return approveWords(normalApprove(r), r.Mode == policy.ModeConfirm)
}

// stopWords is the sentence of the gate g of set that stops gating as it
// does today, for the holders of the role who names or for everyone when
// who is empty, because the draft does what cause says.
func stopWords(set string, g policy.Rule, who, cause string) string {
	verb := "gating"
	if !gateMode(g) {
		verb = "denying"
	}
	return fmt.Sprintf("%s will stop %s %s as rule %s does today%s, because this draft %s.", set, verb, kindsWords(g), g.ID, forHolders(who), cause)
}

// moduleWords is the sentence of the Rego module of set that stops
// refusing calls as it does today, for the holders of the role who names
// or for everyone when who is empty, because the draft does what cause
// says.
func moduleWords(set, who, cause string) string {
	return fmt.Sprintf("%s will stop refusing calls through its Rego module as it does today%s, because this draft %s.", set, forHolders(who), cause)
}

// forHolders words the holders of role for a sentence, or nothing when
// role is empty.
func forHolders(role string) string {
	if role == "" {
		return ""
	}
	return " for holders of " + role
}

// moduleRef names the Rego module of doc in the words of a risk: rego and
// the first 12 hex of the sha256 of its source.
func moduleRef(doc policy.Document) string {
	return "rego@" + sha256Hex(doc.Spec.Escape.Rego)[:12]
}
