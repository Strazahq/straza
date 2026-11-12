package drafts

import (
	"cmp"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/policy"
)

// policyRisks answers the risks of the sets the draft changes: recording
// turned on or off, a Rego module, a require predicate loosened, a
// hook-lane allow where the deployment denies by default, a set that
// selects by user or identity, and every loosening of a gate, typed
// where the loosening is or where its set applies to the proposer. On an
// agent's draft it also answers agent.guardrail for each set that binds
// the agent and that the draft loosens.
func (c *check) policyRisks() []Finding {
	var out []Finding
	ops := setOps(c.d)
	subjs := c.proposerSubjects()
	for _, name := range sortedKeys(c.changedSets()) {
		b, hadB := c.docsW[name]
		a, hasA := c.docsA[name]
		object := "PolicySet/" + name
		rb, ra := recordMode(b.Spec.Capture), recordMode(a.Spec.Capture)
		wider := rb != "" && ra != "" && matchWidens(b.Spec.Match, a.Spec.Match)
		switch {
		case ra != "" && (rb == "" || (rb == policy.CaptureModeRedact && ra == policy.CaptureModeVerbatim) || wider):
			how := "verbatim"
			if ra == policy.CaptureModeRedact {
				how = "with secrets masked"
			}
			before, after := cmp.Or(rb, "off"), ra
			if wider {
				before, after = before+" "+matchWords(b.Spec.Match), after+" "+matchWords(a.Spec.Match)
			}
			out = append(out, risk(codeRecordingOn, object, name, fmt.Sprintf("Conversations of the sessions %s matches will be recorded %s.", name, how), before, after))
		case rb != "" && ra == "":
			out = append(out, risk(codeRecordingOff, object, "", fmt.Sprintf("Conversations of the sessions %s matches will stop being recorded.", name), rb, "off"))
		}
		if a.Spec.Escape != nil && (b.Spec.Escape == nil || b.Spec.Escape.Rego != a.Spec.Escape.Rego) {
			before := ""
			if b.Spec.Escape != nil {
				before = sha256Hex(b.Spec.Escape.Rego)
			}
			out = append(out, risk(codeRego, object, name, fmt.Sprintf(
				"The policy set %s carries a Rego module, which runs on every decision in strazad and on every managed machine.", name), before, sha256Hex(a.Spec.Escape.Rego)))
		}
		if hadB {
			out = append(out, requireLooser(name, b, a, hasA)...)
		}
		if hasA && c.w.LocalToolDefault == policy.EffectDeny {
			out = append(out, localAllow(name, b, hadB, a)...)
		}
		if hasA {
			out = append(out, serverAllow(name, b, hadB, a, c.registered())...)
		}
		out = append(out, c.identityScoped(name, ops[name])...)
	}
	_, loosened, guard := c.guardrail()
	if agentDraft(c.d, c.in.Proposer) {
		out = append(out, loosened...)
	}
	out = append(out, guard...)
	for _, l := range c.loosenings() {
		f := risk(codeGuardrail, "PolicySet/"+l.object, "", l.sentence, l.before, l.after)
		if l.typed || slices.ContainsFunc(subjs, func(subj [2]policy.Subject) bool { return c.binds(l.changed, subj) }) {
			f.Ack, f.Typed = AckTyped, l.object
		}
		out = append(out, f)
	}
	return append(out, c.addedGateRisks()...)
}

// binds reports whether the set name applies to the subject pair subj, in
// live state or after the draft.
func (c *check) binds(name string, subj [2]policy.Subject) bool {
	b, hadB := c.docsW[name]
	a, hasA := c.docsA[name]
	return (hadB && appliesTo(b.Spec.Match, subj[0])) || (hasA && appliesTo(a.Spec.Match, subj[1]))
}

// requireLooser answers policy.require-looser for each require predicate
// of a live set's rule that the draft drops or loosens while it keeps the
// rule. A rule or a set the draft removes is a loosening of its own.
func requireLooser(name string, b, a policy.Document, hasA bool) []Finding {
	var out []Finding
	for _, rb := range b.Spec.Rules {
		i := ruleIndex(a, rb.ID)
		if rb.Require == nil || !hasA || i < 0 {
			continue
		}
		for _, d := range droppedPredicates(rb.Require, a.Spec.Rules[i].Require) {
			out = append(out, risk(codeRequireLooser, "PolicySet/"+name, "", fmt.Sprintf("Rule %s of %s will no longer require %s.", rb.ID, name, d[0]),
				rb.ID+": "+d[0], rb.ID+": "+d[1]))
		}
	}
	return out
}

// attestationRank orders the attestation levels a rule may require, an
// unset level lowest.
var attestationRank = map[string]int{"": -1, "none": 0, "advisory": 1, "managed": 2}

// droppedPredicates answers each predicate of b that a no longer requires
// as strictly, with what a requires in its place or "none".
func droppedPredicates(b, a *policy.Require) [][2]string {
	if a == nil {
		a = &policy.Require{}
	}
	var out [][2]string
	if b.Attestation != "" && attestationRank[a.Attestation] < attestationRank[b.Attestation] {
		left := "none"
		if a.Attestation != "" {
			left = "attestation " + a.Attestation
		}
		out = append(out, [2]string{"attestation " + b.Attestation, left})
	}
	if b.DeviceCert && !a.DeviceCert {
		out = append(out, [2]string{"a device certificate", "none"})
	}
	if len(b.Harness) > 0 && (len(a.Harness) == 0 || slices.ContainsFunc(a.Harness, func(h string) bool { return !slices.Contains(b.Harness, h) })) {
		left := "none"
		if len(a.Harness) > 0 {
			left = "harness " + listWords(a.Harness, "or")
		}
		out = append(out, [2]string{"harness " + listWords(b.Harness, "or"), left})
	}
	return out
}

// localAllow answers policy.local-allow for each rule of a set the draft
// writes that allows a tool of a managed machine where the deployment
// denies by default, unless the live set holds the same rule and the match
// selects no session it did not. A gate is left to addedGateRisks and
// hookGateRisks, which name how it lets a call through.
func localAllow(name string, b policy.Document, hadB bool, a policy.Document) []Finding {
	var out []Finding
	wider := hadB && matchWidens(b.Spec.Match, a.Spec.Match)
	for _, ra := range a.Spec.Rules {
		kinds := localKinds(ra)
		if gateMode(ra) || !canAllow(ra) || !blocks(ra) || len(kinds) == 0 {
			continue
		}
		if i := ruleIndex(b, ra.ID); hadB && !wider && i >= 0 && sameRule(b.Spec.Rules[i], ra) {
			continue
		}
		what := localWhat(ra, kinds, "every tool kind")
		out = append(out, risk(codeLocalAllow, "PolicySet/"+name, "", fmt.Sprintf("Rule %s of %s allows %s on managed machines, where this deployment denies it by default.", ra.ID, name, what),
			"", ra.ID+": "+strings.Join(kinds, ", ")))
	}
	return out
}

// localKinds answers the tool kinds of a managed machine that r governs,
// sorted.
func localKinds(r policy.Rule) []string {
	kinds := toolKinds(r)
	if kinds == nil {
		return localTools
	}
	return slices.DeleteFunc(kinds, func(k string) bool { return !slices.Contains(localTools, k) })
}

// guardrail answers guardrailOf for each subject of the proposer, so the
// guardrail holds at every instant the proposer's roles change, each
// finding once.
func (c *check) guardrail() (refused, loosened, risks []Finding) {
	for _, subj := range c.proposerSubjects() {
		r, l, k := c.guardrailOf(subj)
		refused, loosened, risks = append(refused, r...), append(loosened, l...), append(risks, k...)
	}
	return dedupe(refused), dedupe(loosened), dedupe(risks)
}

// guardrailOf applies the agent guardrail, that a draft may not loosen a
// set that binds its proposer, to every set of either world, with the
// proposer's subject subj in each, and answers the cases where the draft
// changes a set that binds the proposer: each as guardFinding answers it,
// an agent.guardrail refusal or a loosening, and the policy.guardrail-changed
// risks the same cases raise for a person. A set with only a Rego module
// counts as a gate. For an agent's draft it also reads each set-level
// loosening whose set binds the agent before or after, and refuses each
// approval that starts to apply to the agent and takes over a stricter one
// on its local tools. A set whose text does not parse is left to its parse
// refusal.
func (c *check) guardrailOf(subj [2]policy.Subject) (refused, loosened, risks []Finding) {
	ops := setOps(c.d)
	agent := agentDraft(c.d, c.in.Proposer)
	guard := func(name, clause, them string, takeover bool) {
		if f, refuse := c.guardFinding(name, clause, them, takeover); refuse {
			refused = append(refused, f)
		} else {
			loosened = append(loosened, f)
		}
	}
	for _, name := range sortedKeys(c.docsW, c.docsA) {
		b, hadB := c.docsW[name]
		a, hasA := c.docsA[name]
		if _, stored := c.after.Policies[name]; stored && !hasA {
			continue
		}
		object := "PolicySet/" + name
		bound := func(clause, them string, parts ...Finding) {
			guard(name, clause, them, false)
			for _, f := range parts {
				f.Ack, f.Typed = AckTyped, name
				risks = append(risks, f)
			}
		}
		add := func(clause string, parts ...Finding) { bound(clause, clause, parts...) }
		gates := gatesOf(b)
		appB, appA := hadB && appliesTo(b.Spec.Match, subj[0]), hasA && appliesTo(a.Spec.Match, subj[1])
		refs := refsOf(gates)
		if b.Spec.Escape != nil {
			refs = joinParts([]string{refs, moduleRef(b)})
		}
		switch {
		case !appB || appA || refs == "":
		case !hasA && ops[name] == OpOff:
			add("turns it off", gateRisks(name, gates, "turns it off", "off")...)
		case !hasA:
			add("removes it", gateRisks(name, gates, "removes the set", "removed")...)
		case appliesTo(a.Spec.Match, subj[0]):
			role := "a role"
			if i := slices.IndexFunc(a.Spec.Match.Roles, func(r string) bool { return slices.Contains(subj[0].Roles, r) && !slices.Contains(subj[1].Roles, r) }); i >= 0 {
				role = a.Spec.Match.Roles[i]
			}
			stops := fmt.Sprintf("%s will stop applying to you, because this draft takes %s out of your roles.", name, role)
			if agent {
				stops = fmt.Sprintf("%s will stop applying to the agent that wrote this draft, because this draft takes %s out of its roles.", name, role)
			}
			bound(fmt.Sprintf("takes %s out of your roles so that the set no longer applies to you", role),
				fmt.Sprintf("takes %s out of the agent's roles, so the set no longer applies to it", role), risk(codeGuardrail, object, "", stops, refs, "unbound"))
		default:
			add("changes its match or priority", gateRisks(name, gates, "changes its match or priority", "unbound")...)
		}
		if appA && (!appB || c.w.Policies[name].Text != c.after.Policies[name].Text) {
			// A set that starts to bind the proposer, whether the draft
			// writes it or the proposer's roles reach it, is judged as a set
			// the draft creates.
			base, hadBase := b, hadB
			if !appB {
				base, hadBase = policy.Document{}, false
			}
			changedWhileBinding(name, base, hadBase, a, c.w.LocalToolDefault, c.registered(), add)
		}
	}
	if !agent {
		return refused, loosened, risks
	}
	for _, l := range c.loosenings() {
		if l.lane == "" && c.binds(l.changed, subj) {
			guard(l.changed, l.clause, l.clause, l.takeover)
		}
	}
	for _, t := range c.subjectTakeovers(subj) {
		refused = append(refused, refusal(codeAgentGuardrail, "PolicySet/"+t.x.set, visible(fmt.Sprintf(
			"%s applies to you, and this draft lets its rule %s take over from the stricter rule %s of %s. An agent's draft may not change what binds the agent.",
			t.x.set, t.x.rule.ID, t.y.rule.ID, t.y.set)), fmt.Sprintf("Leave %s and your roles as they are. A person can change them.", t.x.set)))
	}
	return refused, loosened, risks
}

// widensRule begins the guardrail clause of a rule that a draft adds to a
// set that binds the proposer, or changes so that it allows more, which
// widens the proposer's own reach.
const widensRule = "adds or widens rule "

// guardFinding is the agent.guardrail finding of a draft that changes the
// set name, which binds its proposer, as clause says, and them says of the
// agent in the third person. A clause that adds or widens a rule widens
// the agent's own reach, so it is refused, as is a takeover, and every
// clause on a direct route, which publishes with no review, and on a check
// that reads no risks, where nothing would show it. Otherwise the change
// loosens the set, and refused is false: a risk a person confirms by
// typing the set's name. Its sentence names no agent, because the reader
// is the publisher.
func (c *check) guardFinding(name, clause, them string, takeover bool) (f Finding, refused bool) {
	object := "PolicySet/" + name
	if takeover || strings.HasPrefix(clause, widensRule) || c.in.Direct || c.in.RefusalsOnly {
		return refusal(codeAgentGuardrail, object, visible(fmt.Sprintf("%s applies to you, and this draft %s. An agent's draft may not change what binds the agent.", name, clause)),
			fmt.Sprintf("Leave %s and your roles as they are. A person can change them.", name)), true
	}
	return risk(codeAgentGuardrail, object, name, fmt.Sprintf("%s binds the agent that wrote this draft, and this draft %s.", name, them), "", ""), false
}

// changedWhileBinding calls add for each change of a set that applies to
// the proposer after the draft, other than added deny rules, added gates
// that let through no call the deployment denies by default, and reason
// text. A set the draft creates counts as changed, so an allow rule
// in it is added too. A gate kept by its id widens when it comes to let
// through calls it held or refused: it allows where it could not, fires
// on more calls or stops gating, unless it can only tighten the set.
func changedWhileBinding(name string, b policy.Document, hadB bool, a policy.Document, localDefault string, apps map[string]App, add func(string, ...Finding)) {
	if hadB && !sameMatch(b, a) {
		var parts []Finding
		if matchNarrows(b.Spec.Match, a.Spec.Match) || b.Spec.Priority != a.Spec.Priority {
			parts = gateRisks(name, gatesOf(b), "changes its match or priority", "")
		}
		add("changes its match or priority", parts...)
	}
	for _, rb := range b.Spec.Rules {
		i := ruleIndex(a, rb.ID)
		switch {
		case i < 0 && isGate(rb):
			add("changes rule "+rb.ID, gateRisk(name, rb, "changes the rule", rb.ID+"@removed"))
		case i < 0:
			add("changes rule " + rb.ID)
		case sameRule(rb, a.Spec.Rules[i]):
		case isGate(rb):
			clause, ra := "changes rule "+rb.ID, a.Spec.Rules[i]
			if !tightens(ra, localDefault, apps) && (!canAllow(rb) || firesMore(rb, ra) || (gateMode(rb) && !gateMode(ra))) {
				clause = widensRule + rb.ID
			}
			add(clause, gateRisk(name, rb, "changes the rule", ruleRef(ra)))
		case tightens(a.Spec.Rules[i], localDefault, apps):
			add("changes rule " + rb.ID)
		default:
			add(widensRule + rb.ID)
		}
	}
	for _, ra := range a.Spec.Rules {
		if ruleIndex(b, ra.ID) < 0 && !tightens(ra, localDefault, apps) {
			add(widensRule + ra.ID)
		}
	}
}

// onceWords says what lets a held or checked call run, by the mode of its
// gate.
var onceWords = map[string]string{policy.ModeApprove: "a person approves it", policy.ModeConfirm: "the requester confirms it",
	policy.ModeClassify: "a classifier allows it", policy.ModeServerCheck: "the server allows it"}

// addedGateRisks answers policy.guardrail-changed, with a tick, for each
// gate any set gains that turns a local call the deployment denies by
// default into a held or checked one, so a call refused today can run.
func (c *check) addedGateRisks() []Finding {
	var out []Finding
	for _, name := range sortedKeys(c.changedSets()) {
		a, ok := c.docsA[name]
		if !ok {
			continue
		}
		for _, r := range a.Spec.Rules {
			if ruleIndex(c.docsW[name], r.ID) >= 0 || !opensDefault(r, c.w.LocalToolDefault, c.registered()) {
				continue
			}
			if kinds := deniedKinds(r, c.w.LocalToolDefault); len(kinds) > 0 {
				out = append(out, risk(codeGuardrail, "PolicySet/"+name, "", fmt.Sprintf(
					"Rule %s of %s lets %s run on managed machines once %s, where this deployment denies it by default.",
					r.ID, name, localWhat(r, kinds, "every local tool kind"), onceWords[r.Mode]), "", ruleRef(r)))
			}
			for _, s := range gateServers(r, c.registered()) {
				out = append(out, risk(codeGuardrail, "PolicySet/"+name, "", unregisteredWords(name, r, s, onceWords[r.Mode]), "", ruleRef(r)+" on "+cmp.Or(s, "every MCP server")))
			}
		}
	}
	return out
}

// gateProbe is the approval a gate rule asks for, as a probe would read it.
func gateProbe(r policy.Rule) probe {
	return probe{outcome: OutcomeApproval, approve: normalApprove(r), confirm: r.Mode == policy.ModeConfirm}
}

// gateRisks answers gateRisk for each of gates, each with after as its
// after words, or its own reference when after is empty.
func gateRisks(set string, gates []policy.Rule, cause, after string) []Finding {
	out := make([]Finding, 0, len(gates))
	for _, g := range gates {
		out = append(out, gateRisk(set, g, cause, cmp.Or(after, ruleRef(g))))
	}
	return out
}

// gateRisk is the policy.guardrail-changed risk that the gate g of set
// stops gating as it does today, because the draft does what cause says.
func gateRisk(set string, g policy.Rule, cause, after string) Finding {
	return risk(codeGuardrail, "PolicySet/"+set, "", stopWords(set, g, "", cause), ruleRef(g), after)
}

// gatesOf answers the gates of doc, in rule order.
func gatesOf(doc policy.Document) []policy.Rule {
	var out []policy.Rule
	for _, r := range doc.Spec.Rules {
		if isGate(r) {
			out = append(out, r)
		}
	}
	return out
}

// refsOf spells gates as their references joined by "; ".
func refsOf(gates []policy.Rule) string {
	refs := make([]string, len(gates))
	for i, g := range gates {
		refs[i] = ruleRef(g)
	}
	return strings.Join(refs, "; ")
}

// identityScoped answers policy.identity-scoped-change for the set name,
// which the draft adds, changes, turns off or removes with op, when its
// match selects by username or identity before or after. Who gains reads
// role lanes, which such a match never selects, so a person must read the
// match. The words are the sha12 of each side's text, "off" or "removed",
// so they change with the set and never list a user.
func (c *check) identityScoped(name string, op Op) []Finding {
	b, hadB := c.docsW[name]
	a, hasA := c.docsA[name]
	selects := func(m policy.Match) [4]bool {
		id := identityLists(m)
		return [4]bool{len(m.Users) > 0, len(id[0]) > 0, len(id[1]) > 0, len(id[2]) > 0}
	}
	sb, sa := selects(b.Spec.Match), selects(a.Spec.Match)
	var kinds []string
	for i, word := range []string{"username", "user type", "agency mode", "swarm"} {
		if (hadB && sb[i]) || (hasA && sa[i]) {
			kinds = append(kinds, word)
		}
	}
	if len(kinds) == 0 {
		return nil
	}
	before, after := "", "removed"
	if hadB {
		before = sha256Hex(c.w.Policies[name].Text)[:12]
	}
	switch {
	case hasA:
		after = sha256Hex(c.after.Policies[name].Text)[:12]
	case op == OpOff:
		after = "off"
	}
	return []Finding{risk(codeIdentityScoped, "PolicySet/"+name, "", fmt.Sprintf(
		"Straza cannot show who gains through %s, because its match selects by %s, so check its match by hand.", name, listWords(kinds, "and")), before, after)}
}

// setOps maps the name of each set d writes to the op of its item.
func setOps(d Draft) map[string]Op {
	out := map[string]Op{}
	for _, it := range d.Items {
		if it.Kind == KindPolicySet {
			out[it.Name] = it.Op
		}
	}
	return out
}

// ruleIndex answers the index of the rule of doc with the id, or -1.
func ruleIndex(doc policy.Document, id string) int {
	return slices.IndexFunc(doc.Spec.Rules, func(r policy.Rule) bool { return r.ID == id })
}

// proposerSubjects answers the proposer as the policy engine sees them in
// live state and after the draft, once for each set of roles
// proposerRoleSets answers: the roles of the set, each closed over its
// world's implications, and their typology. On the after side a role the
// draft removes drops out.
func (c *check) proposerSubjects() [][2]policy.Subject {
	p := c.in.Proposer
	var out [][2]policy.Subject
	for _, set := range c.proposerRoleSets() {
		kept := slices.DeleteFunc(slices.Clone(set), func(r string) bool {
			_, live := c.w.Roles[r]
			_, stays := c.after.Roles[r]
			return live && !stays
		})
		out = append(out, [2]policy.Subject{subjectOf(p, c.clW.union(set)), subjectOf(p, c.clA.union(kept))})
	}
	return out
}

// proposerRoleSets answers each distinct set of roles the proposer holds
// directly, sorted, at in.Now and at every later instant one of its
// windows starts or ends, in time order. A role Holders lists the proposer
// under with no window is in every set. A proposer with no username holds
// no role.
func (c *check) proposerRoleSets() [][]string {
	username := c.in.Proposer.Username
	if username == "" {
		return [][]string{nil}
	}
	windows := c.w.Windows[username]
	bounded := map[string]bool{}
	instants := []time.Time{c.in.Now}
	for _, r := range windows {
		bounded[r.Role] = true
		for _, b := range []*time.Time{r.From, r.To} {
			if b != nil && b.After(c.in.Now) {
				instants = append(instants, *b)
			}
		}
	}
	slices.SortFunc(instants, time.Time.Compare)
	var always []string
	for role, hs := range c.w.Holders {
		if !bounded[role] && slices.ContainsFunc(hs, func(h Holder) bool { return h.Username == username }) {
			always = append(always, role)
		}
	}
	var out [][]string
	seen := map[string]bool{}
	for _, t := range instants {
		set := slices.Clone(always)
		for _, r := range windows {
			if r.holds(t) && !slices.Contains(set, r.Role) {
				set = append(set, r.Role)
			}
		}
		sort.Strings(set)
		if key := strings.Join(set, "\x00"); !seen[key] {
			seen[key] = true
			out = append(out, set)
		}
	}
	return out
}
