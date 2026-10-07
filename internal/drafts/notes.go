package drafts

import (
	"fmt"
	"slices"

	"github.com/strazahq/straza/internal/policy"
)

// The codes of the lines that only inform: what Straza could not check
// without contacting or starting something, what passed, and facts.
const (
	codeUncheckedTools   = "unchecked.tools"
	codeUncheckedRego    = "unchecked.rego"
	codeUncheckedRequire = "unchecked.require"
	codePassedSecrets    = "passed.secrets"
	codePassedPools      = "passed.pools"
	codePassedCompile    = "passed.compile"
	codeNobodyHolds      = "info.nobody-holds"
	codePacks            = "info.packs"
	codeServerDown       = "info.server-down"
	codeNoChange         = "info.no-change"
)

// note is a finding of class that only informs, its texts spelled with
// visible.
func note(code string, class Class, object, sentence string) Finding {
	return Finding{Code: code, Class: class, Object: visible(object), Sentence: visible(sentence)}
}

// notes adds the unchecked, passed and info lines of step 7 to the
// verdict. clean says the secret scan found nothing.
func (c *check) notes(clean bool) {
	v := &c.v
	var servers []string
	for _, it := range c.d.Items {
		if c.unchanged(it) {
			v.Info = append(v.Info, note(codeNoChange, ClassInfo, it.Object(), fmt.Sprintf("%s equals live, so publishing changes nothing for it.", it.Object())))
			continue
		}
		switch {
		case it.Kind == KindApp && it.Op == OpPut:
			servers = append(servers, it.Name)
			if f, ok := c.unlistedTools(it.Name); ok {
				v.Unchecked = append(v.Unchecked, f)
			}
		case it.Kind == KindRole && it.Op == OpPut:
			servers = append(servers, c.after.Access[it.Name].Server)
			v.Info = append(v.Info, c.roleNotes(it)...)
		}
	}
	pools := false
	for _, name := range sortedKeys(c.changedSets()) {
		for _, r := range c.docsA[name].Spec.Rules {
			pools = pools || (r.Mode == policy.ModeApprove && r.Approve != nil && len(r.Approve.Roles) > 0)
		}
	}
	require, rego := c.prober().read()
	if len(require) > 0 {
		v.Unchecked = append(v.Unchecked, note(codeUncheckedRequire, ClassUnchecked, "", fmt.Sprintf(
			"Straza read who gains what for a session that meets every require predicate of %s, the widest reach a holder can have, so a session that does not meet them reaches less.",
			listWords(require, "and"))))
	}
	if rego = unionSorted(rego, c.newModules()); len(rego) > 0 {
		v.Unchecked = append(v.Unchecked, note(codeUncheckedRego, ClassUnchecked, "", fmt.Sprintf(
			"Straza read who gains what without running the Rego modules of %s, which only refuse calls, so a holder may reach less than it shows.", listWords(rego, "and"))))
	}
	slices.Sort(servers)
	for _, s := range slices.Compact(servers) {
		live, ok := c.w.Apps[s]
		if !ok || live.Status == "" || live.Status == "running" {
			continue
		}
		sentence := fmt.Sprintf("%s reads %s now.", s, live.Status)
		if live.Detail != "" {
			sentence = fmt.Sprintf("%s reads %s now: %s.", s, live.Status, live.Detail)
		}
		v.Info = append(v.Info, note(codeServerDown, ClassInfo, "App/"+s, sentence))
	}
	if clean {
		v.Passed = append(v.Passed, note(codePassedSecrets, ClassPassed, "", "No document holds a secret or the shape of one."))
	}
	if pools {
		v.Passed = append(v.Passed, note(codePassedPools, ClassPassed, "", "Every approver role the draft's rules name exists and may decide."))
	}
	if slices.ContainsFunc(c.d.Items, func(it Item) bool { return it.Kind == KindPolicySet }) {
		v.Passed = append(v.Passed, note(codePassedCompile, ClassPassed, "", "The policy compiles with this draft."))
	}
}

// unchanged reports whether item it leaves its object as live state holds
// it: a document equal to the live one, or the removal of an object that
// does not exist.
func (c *check) unchanged(it Item) bool {
	switch {
	case it.Op == OpRemove && it.Kind == KindRole:
		_, ok := c.w.Roles[it.Name]
		return !ok
	case it.Op == OpRemove && it.Kind == KindPolicySet:
		_, ok := c.w.Policies[it.Name]
		return !ok
	case it.Op != OpPut:
	case it.Kind == KindApp:
		live, ok := c.w.Apps[it.Name]
		return ok && c.in.Apps[it.Name].Manifest == live.Manifest
	case it.Kind == KindRole:
		live, ok := RoleDocOf(c.w, it.Name)
		doc, read := c.after.roleDocs[it.Name]
		return ok && read && roleText(doc) == roleText(live)
	case it.Kind == KindPolicySet:
		live, ok := c.w.Policies[it.Name]
		return ok && live.Text == it.Doc
	}
	return false
}

// unlistedTools is unchecked.tools for the server name when the draft adds
// it or changes its runtime and nobody read its tools.
func (c *check) unlistedTools(name string) (Finding, bool) {
	next := c.after.Apps[name]
	if live, had := c.w.Apps[name]; next.Offered != nil || (had && sameRuntime(live, next)) {
		return Finding{}, false
	}
	sentence := fmt.Sprintf("Straza has not started %s, so its tool names are unknown until it is published.", name)
	if next.Runtime == runtimeRemote {
		sentence = fmt.Sprintf("Straza has not contacted %s, because an address in a draft is contacted only when a person asks, so the tool names of %s are unknown.",
			normHost(next.URL), name)
	}
	return note(codeUncheckedTools, ClassUnchecked, "App/"+name, sentence), true
}

// roleNotes answers the info lines of a role the draft puts: nobody holds
// it, and its knowledge packs differ from live, which a draft leaves as
// they are.
func (c *check) roleNotes(it Item) []Finding {
	var out []Finding
	if len(c.hA.byRole[it.Name]) == 0 {
		out = append(out, note(codeNobodyHolds, ClassInfo, it.Object(), fmt.Sprintf("Nobody holds %s yet, so it reaches nothing until someone is assigned it.", it.Name)))
	}
	if doc, read := c.after.roleDocs[it.Name]; read && !slices.Equal(sortedCopy(doc.Spec.Packs), sortedCopy(c.w.Roles[it.Name].Packs)) {
		out = append(out, note(codePacks, ClassInfo, it.Object(), fmt.Sprintf(
			"The knowledge packs of %s are bound directly, not through drafts, so this draft leaves them as they are.", it.Name)))
	}
	return out
}
