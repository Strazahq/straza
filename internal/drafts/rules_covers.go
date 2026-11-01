package drafts

import (
	"fmt"
	"slices"
	"strings"
)

// The codes of the covers rules of a Role put: a binding on a pinned server
// with no covers line, a value the object list does not hold, a tool
// nothing keeps to a role's objects, a covers line on a server that pins
// no type, a value named twice, and a list that names none.
const (
	codeCoversMissing  = "access.covers-missing"
	codeCoversUnknown  = "access.covers-unknown"
	codeCoversReach    = "access.covers-reach"
	codeCoversUnpinned = "access.covers-unpinned"
	codeCoversTwice    = "access.covers-twice"
	codeCoversEmpty    = "access.covers-empty"
)

// rowKept reports whether the binding b of the role name is the access row
// live state w holds for it: the same server, the same tools, each once
// and in any order, and the same covers line by id. b's covers line must
// carry the ids it resolves to (RowCovers). A live row whose covers line
// cannot be read is never kept, so a put that restates it meets the row
// rules again.
func rowKept(w World, name string, b RoleBinding) bool {
	acc, ok := w.Access[name]
	return ok && acc.Fault == "" && acc.Server == b.App && slices.Equal(slices.Compact(sortedCopy(acc.Tools)), slices.Compact(sortedCopy(b.Tools))) &&
		acc.Covers.Key() == b.Covers.Key()
}

// coversRefusals answers the refusals of the covers line of b, the binding
// of the Role put of the role name, over after, the world after the draft.
// b's covers line carries its resolved ids. A covers line on a server that
// pins no type is refused whatever the row, and the other rules judge only
// a row the put adds or changes, which kept says it does not.
func coversRefusals(after World, name string, b RoleBinding, kept bool) []Finding {
	app, ok := after.Apps[b.App]
	object, role, server := string(KindRole)+"/"+name, visible(name), visible(b.App)
	switch p := app.Pinned; {
	case !ok:
		return nil
	case p == nil && b.Covers != nil:
		return []Finding{refusal(codeCoversUnpinned, object, fmt.Sprintf(
			"%s has a covers line on %s, which pins no object type. Remove covers from that binding, or pin %s's objects first.", role, server, server), "")}
	case p == nil || kept:
		return nil
	case b.Covers == nil:
		return []Finding{refusal(codeCoversMissing, object, fmt.Sprintf(
			"%s binds tools that work on %s. Say which %s it covers: every %s, or these %s by name.", role, p.Plural, p.Plural, p.Type, p.Plural),
			fmt.Sprintf("In the role file, write covers: every, or covers: {objects: [<%s>, ...]}.", p.Type))}
	case b.Covers.Every:
		return nil
	case len(b.Covers.Objects) == 0:
		return []Finding{refusal(codeCoversEmpty, object, fmt.Sprintf(
			"%s's covers line names no %s. Name at least one, or write covers: every.", role, p.Plural), "")}
	}
	p := app.Pinned
	var out []Finding
	values := make([]string, len(b.Covers.Objects))
	for i, o := range b.Covers.Objects {
		values[i] = o.Value
	}
	if v := repeated(values); v != "" {
		out = append(out, refusal(codeCoversTwice, object, fmt.Sprintf(
			"%s names %s twice under covers. Name each %s once.", role, visible(v), p.Type), ""))
	}
	named := map[string]string{}
	for _, o := range b.Covers.Objects {
		if first, ok := named[o.ID]; ok && o.ID != "" && first != o.Value {
			out = append(out, refusal(codeCoversTwice, object, fmt.Sprintf(
				"%s names %s and %s, which are the same %s. Name it once.", role, visible(first), visible(o.Value), p.Type), ""))
			continue
		}
		named[o.ID] = o.Value
	}
	unknown := map[string]bool{}
	for _, o := range b.Covers.Objects {
		if o.ID == "" && !unknown[o.Value] {
			unknown[o.Value] = true
			out = append(out, refusal(codeCoversUnknown, object, fmt.Sprintf(
				"%s is not in %s's %s list. Compare the name with %s's %s tab or strazactl apps objects %s. If the %s is new, refresh the list with strazactl apps objects refresh %s, then try again.",
				visible(o.Value), server, p.Type, server, title(p.Plural), server, p.Type, server), ""))
		}
	}
	for _, tool := range boundTools(app, b.Tools) {
		switch p.reachOf(tool) {
		case reachAll:
			out = append(out, refusal(codeCoversReach, object, fmt.Sprintf(
				"%s cannot be in a role on some %s: nothing keeps it to them. Make a role on every %s for it.", visible(tool), p.Plural, p.Type), ""))
		case reachCredential:
			out = append(out, refusal(codeCoversReach, object, fmt.Sprintf(
				"%s cannot be in a role on some %s: its calls do not name a %s, and Straza does not check a role's own login yet. Make a role on every %s for it.",
				visible(tool), p.Plural, p.Type, p.Type), ""))
		}
	}
	return out
}

// The reach words a pinned declaration gives a tool, as manager reads them.
const (
	reachArgument   = "argument"
	reachCredential = "credential"
	reachAll        = "all"
	reachNone       = "none"
)

// reachOf answers the pinned reach of tool, all for a tool the declaration
// does not name, the listing tool included.
func (p *PinnedObjects) reachOf(tool string) string {
	if r, ok := p.Reach[tool]; ok {
		return r
	}
	return reachAll
}

// boundTools answers, sorted and each once, the tools the matchers bind on
// app: each name a matcher spells, and each tool app offers that a glob
// matches, so a glob that admits a tool answers that tool's name. While
// nobody has read the server's tools, a glob answers itself, a name no
// declaration gives a reach.
func boundTools(app App, matchers []string) []string {
	offered, known := toolsOf(app)
	var out []string
	for _, m := range matchers {
		if !strings.Contains(m, "*") || !known {
			out = append(out, m)
			continue
		}
		for _, t := range offered {
			if matchGlob(m, t) {
				out = append(out, t)
			}
		}
	}
	return slices.Compact(sortedCopy(out))
}

// title answers a plural, which starts with a letter a to z, with that
// letter upper case, as a console tab names it.
func title(plural string) string {
	if plural == "" {
		return ""
	}
	return strings.ToUpper(plural[:1]) + plural[1:]
}
