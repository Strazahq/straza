package drafts

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/strazahq/straza/internal/policy"
)

// The approval windows a rule gets when it names none, as the policy
// engine fills them in: a hold's retry window, a ticket's life and its
// grant window. Gate words leave a window out while it is the default.
const (
	defaultRetrySeconds  = 60
	defaultTicketSeconds = 86400
	defaultGrantSeconds  = 3600
)

// probe is what one call gives a subject: the outcome, the gate in words,
// the approval behind a needs-approval outcome, and whether a classifier
// or the server must allow the call.
type probe struct {
	outcome               Outcome
	words                 string
	approve               *policy.ApproveSpec
	confirm               bool
	classify, serverCheck bool
}

// probeKey names one probe, so the roles and the proposer that share a
// subject share its probes.
type probeKey struct {
	after     bool
	subject   int
	app, tool string
	granted   bool
}

// gainer probes calls for one check. subjects numbers each distinct
// subject, engines holds the narrow engines of each world, and memo every
// probe made.
type gainer struct {
	c        *check
	subjects map[string]int
	engines  [2]*narrowEngines
	memo     map[probeKey]probe
}

// prober answers the check's gainer, made on first use.
func (c *check) prober() *gainer {
	if c.g == nil {
		c.g = &gainer{c: c, subjects: map[string]int{}, memo: map[probeKey]probe{},
			engines: [2]*narrowEngines{newNarrowEngines(c.docsW, c.w.LocalToolDefault), newNarrowEngines(c.docsA, c.w.LocalToolDefault)}}
	}
	return c.g
}

// subjectID numbers each distinct subject.
func (g *gainer) subjectID(sub policy.Subject) int {
	key := sub.User + "\x00" + sub.UserType + "\x00" + sub.AgencyMode + "\x00" + sub.SwarmID + "\x00" + strings.Join(sub.Roles, "\x00")
	id, ok := g.subjects[key]
	if !ok {
		id = len(g.subjects)
		g.subjects[key] = id
	}
	return id
}

// eval probes an mcp.call of tool on app for sub with the narrow engine of
// one world, once per subject and call.
func (g *gainer) eval(after bool, sub policy.Subject, app, tool string, granted bool) probe {
	side := 0
	if after {
		side = 1
	}
	id := g.subjectID(sub)
	key := probeKey{after: after, subject: id, app: app, tool: tool, granted: granted}
	if p, ok := g.memo[key]; ok {
		return p
	}
	p := probeOf(g.engines[side].of(id, sub).Evaluate(policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolMCPCall, App: app, ToolName: tool, Granted: granted}, sub))
	g.memo[key] = p
	return p
}

// read answers, sorted, the sets of either world that applied to a probed
// subject and carried a require predicate, and those that carried a Rego
// module, which the probes did not run.
func (g *gainer) read() (require, rego []string) {
	for _, n := range g.engines {
		for name := range n.read {
			if n.require[name] {
				require = append(require, name)
			}
			if n.rego[name] {
				rego = append(rego, name)
			}
		}
	}
	slices.Sort(require)
	slices.Sort(rego)
	return slices.Compact(require), slices.Compact(rego)
}

// narrowEngines compiles, for a subject, an engine of only the sets of one
// world that apply to it, and shares it among the subjects to whom the
// same sets apply. A set that does not apply to a subject takes no part in
// the engine's decision for it, so the narrow engine decides as an engine
// of every set would, while it reads a handful of sets.
//
// The sets are compiled with every require block and every Rego module
// taken out. A rule with require then answers as it does for a session
// that meets every predicate, the widest reach a holder can have, and no
// module runs during a check, which only refuses calls anyway. require and
// rego mark the sets that carried either, and read the ones that applied to
// a probed subject.
type narrowEngines struct {
	docs    map[string]policy.Document
	names   []string
	local   string
	require map[string]bool
	rego    map[string]bool
	read    map[string]bool
	bySet   map[string]*policy.Engine
	byID    map[int]*policy.Engine
}

// newNarrowEngines answers the narrow engines of the sets docs with the
// local tool default local.
func newNarrowEngines(docs map[string]policy.Document, local string) *narrowEngines {
	n := &narrowEngines{docs: map[string]policy.Document{}, names: sortedKeys(docs), local: local,
		require: map[string]bool{}, rego: map[string]bool{}, read: map[string]bool{}, bySet: map[string]*policy.Engine{}, byID: map[int]*policy.Engine{}}
	for name, doc := range docs {
		n.rego[name] = doc.Spec.Escape != nil
		doc.Spec.Escape = nil
		rules := make([]policy.Rule, len(doc.Spec.Rules))
		for i, r := range doc.Spec.Rules {
			n.require[name] = n.require[name] || r.Require != nil
			r.Require = nil
			rules[i] = r
		}
		doc.Spec.Rules = rules
		n.docs[name] = doc
	}
	return n
}

// of answers the engine for sub, whose subject number is id.
func (n *narrowEngines) of(id int, sub policy.Subject) *policy.Engine {
	if eng, ok := n.byID[id]; ok {
		return eng
	}
	var applying []string
	for _, name := range n.names {
		if appliesTo(n.docs[name].Spec.Match, sub) {
			applying = append(applying, name)
			n.read[name] = true
		}
	}
	set := strings.Join(applying, "\x00")
	eng, ok := n.bySet[set]
	if !ok {
		list := make([]policy.Document, len(applying))
		for i, name := range applying {
			list[i] = n.docs[name]
		}
		// The sets compiled whole in step 4, and taking require blocks and
		// modules out leaves nothing that can fail to compile.
		eng, _ = policy.NewEngine(list, n.local)
		n.bySet[set] = eng
	}
	n.byID[id] = eng
	return eng
}

// probeOf reads a decision as an outcome and its whole gate: a default
// deny reaches nothing, a rule's deny is denied, an allow with an approval
// needs approval, and any other allow runs, each allow with the checks a
// classifier or the server makes on it.
func probeOf(d policy.Decision) probe {
	switch {
	case d.Effect != policy.EffectAllow && d.Default:
		return probe{outcome: OutcomeNotReachable}
	case d.Effect != policy.EffectAllow:
		return probe{outcome: OutcomeDenied, words: fmt.Sprintf("denied by rule %s of %s", d.RuleID, d.SetName)}
	}
	p := probe{outcome: OutcomeRuns, classify: d.Classify, serverCheck: d.ServerCheck}
	var words []string
	if d.Approve != nil {
		p.outcome, p.approve, p.confirm = OutcomeApproval, d.Approve, d.Confirm
		words = append(words, approveWords(d.Approve, d.Confirm))
	}
	switch {
	case d.Classify && d.ServerCheck:
		words = append(words, "after a classifier and the server allow it")
	case d.Classify:
		words = append(words, "after a classifier allows it")
	case d.ServerCheck:
		words = append(words, "after the server allows it")
	}
	p.words = strings.Join(words, ", ")
	return p
}

// approveWords spells an approval gate, as "a hold, up to 2 minutes,
// decided by sec-approvers" or "a ticket good for 1 day, decided by the
// person behind the agent", naming every setting that can make it looser.
func approveWords(a *policy.ApproveSpec, confirm bool) string {
	var b strings.Builder
	if a.Class == policy.ClassTicket {
		b.WriteString("a ticket good for " + duration(a.TicketTTLSeconds))
	} else {
		b.WriteString("a hold, up to " + duration(a.TimeoutSeconds))
	}
	if confirm {
		b.WriteString(", confirmed by the requester")
	} else {
		deciders := pool(probe{approve: a})
		if a.SelfApproval {
			deciders = append(deciders, "the requester")
		}
		b.WriteString(", decided by " + listWords(deciders, "or"))
	}
	switch {
	case a.Class == policy.ClassTicket && a.GrantTTLSeconds != defaultGrantSeconds:
		b.WriteString(", used within " + duration(a.GrantTTLSeconds))
	case a.Class != policy.ClassTicket && a.RetryTTLSeconds != defaultRetrySeconds:
		b.WriteString(", retried within " + duration(a.RetryTTLSeconds))
	}
	if a.Binding == policy.ApproveBindingTool {
		b.WriteString(", for any arguments")
	}
	return b.String()
}

// normalApprove answers the approval r asks for with every window the rule
// leaves out filled in, as the policy engine fills them.
func normalApprove(r policy.Rule) *policy.ApproveSpec {
	a := policy.ApproveSpec{}
	if r.Approve != nil {
		a = *r.Approve
	}
	a.Class = cmp.Or(a.Class, policy.ClassHold)
	a.Binding = cmp.Or(a.Binding, policy.ApproveBindingCall)
	if a.Class == policy.ClassTicket {
		a.TicketTTLSeconds = cmp.Or(a.TicketTTLSeconds, defaultTicketSeconds)
		a.GrantTTLSeconds = cmp.Or(a.GrantTTLSeconds, defaultGrantSeconds)
		a.Bind = cmp.Or(a.Bind, policy.BindFingerprint)
		return &a
	}
	a.TimeoutSeconds = cmp.Or(a.TimeoutSeconds, defaultHoldSeconds)
	a.RetryTTLSeconds = cmp.Or(a.RetryTTLSeconds, defaultRetrySeconds)
	return &a
}

// pool answers who may decide the approval of p, the requester's own
// approval left out: the approver roles, sorted, then the person behind the
// agent when the rule names the sponsor or names nobody, in the words of the
// console. A confirmation is the requester's.
func pool(p probe) []string {
	if p.confirm {
		return []string{"the requester"}
	}
	a := p.approve
	out := sortedCopy(a.Roles)
	if slices.Contains(a.Deciders, policy.DeciderSponsor) || (len(a.Roles) == 0 && len(a.Deciders) == 0 && !a.SelfApproval) {
		out = append(out, "the person behind the agent")
	}
	return out
}

// duration words a number of seconds in the largest whole unit.
func duration(seconds int) string {
	n, unit := seconds, "second"
	switch {
	case seconds >= 86400 && seconds%86400 == 0:
		n, unit = seconds/86400, "day"
	case seconds >= 3600 && seconds%3600 == 0:
		n, unit = seconds/3600, "hour"
	case seconds >= 60 && seconds%60 == 0:
		n, unit = seconds/60, "minute"
	}
	if n != 1 {
		unit += "s"
	}
	return fmt.Sprintf("%d %s", n, unit)
}
