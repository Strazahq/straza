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
			v.Info = append(v.Info, note(codeNoChange, ClassInfo, it.Object(), fmt.Sprintf("%s equals live.", objectWords(it.Object()))))
			if f, ok := c.packsNote(it); ok {
				v.Info = append(v.Info, f)
			}
			continue
		}
		switch {
		case it.Kind == KindApp && it.Op == OpPut:
			servers = append(servers, it.Name)
			if f, ok := c.unlistedTools(it.Name); ok {
				v.Unchecked = append(v.Unchecked, f)
			}
			for _, f := range objectsNotes(it, c.w, c.after) {
				switch f.Class {
				case ClassWarning:
					v.Warnings = append(v.Warnings, f)
				case ClassUnchecked:
					v.Unchecked = append(v.Unchecked, f)
				default:
					v.Info = append(v.Info, f)
				}
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
			"Straza read who gains what for a session that meets every require predicate of %s.",
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
// it: a document equal to the live one, its covers line compared by id and
// its knowledge packs ignored, or the removal of an object that does not
// exist.
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
		// Packs bind nothing, so a document that differs only by them
		// changes nothing.
		doc.Spec.Packs, live.Spec.Packs = nil, nil
		return ok && read && roleText(withoutCovers(doc)) == roleText(withoutCovers(live)) &&
			c.w.Access[it.Name].Covers.Key() == c.after.Access[it.Name].Covers.Key()
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
		sentence = fmt.Sprintf("Straza has not contacted %s yet, so the tool names of %s are unknown. It contacts an address in a draft only when a person asks.",
			normHost(next.URL), name)
	}
	return note(codeUncheckedTools, ClassUnchecked, "App/"+name, sentence), true
}

// roleNotes answers the info lines of a role the draft puts: nobody holds
// it, and its document names knowledge packs, which bind nothing.
func (c *check) roleNotes(it Item) []Finding {
	var out []Finding
	if len(c.hA.byRole[it.Name]) == 0 {
		out = append(out, note(codeNobodyHolds, ClassInfo, it.Object(), fmt.Sprintf("Nobody holds %s yet, so it reaches nothing until someone is assigned it.", it.Name)))
	}
	if f, ok := c.packsNote(it); ok {
		out = append(out, f)
	}
	return out
}

// packsNote is info.packs for a role put whose document names knowledge
// packs, which bind nothing, whether or not the document equals live.
func (c *check) packsNote(it Item) (Finding, bool) {
	doc, read := c.after.roleDocs[it.Name]
	if it.Kind != KindRole || it.Op != OpPut || !read || len(doc.Spec.Packs) == 0 {
		return Finding{}, false
	}
	return note(codePacks, ClassInfo, it.Object(), "This Role names knowledge packs, which were removed from Straza. The packs field is ignored."), true
}
