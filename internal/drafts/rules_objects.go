package drafts

import (
	"fmt"
	"slices"
)

// ObjectsReview is what the server read of an App item's straza.objects
// block against the tools its running server lists. Running is false when
// the server holds no tool list for it, and the lists are then empty.
// Refusals are sentences, one per rule the block breaks. Undecided names
// the tools the block does not name, the listing tool aside, and Unknown
// the tools it names that the server does not list, both sorted.
type ObjectsReview struct {
	Type      string
	Plural    string
	Running   bool
	Refusals  []string
	Undecided []string
	Unknown   []string
}

// The codes of the objects check: a rule the block breaks against the
// running server, a type change while a role names objects of the old
// type, tools it leaves undecided, tools it names that the server does not
// list, a server that is not running to compare with, the first pin, a
// pinned server Straza cannot list, an unpin, and a tool a role on some
// objects can no longer call.
const (
	codeAppObjects       = "app.objects"
	codeObjectsType      = "app.objects-type"
	codeObjectsUndecided = "ready.objects-undecided"
	codeObjectsUnknown   = "info.objects-unknown"
	codeUncheckedObjects = "unchecked.objects"
	codeObjectsFirstPin  = "info.objects-first-pin"
	codeObjectsListing   = "ready.objects-listing"
	codeObjectsUnpin     = "info.objects-unpin"
	codeObjectsReach     = "ready.objects-reach"
)

// objectsRefusals answers app.objects for each rule the straza.objects
// block of the App put it breaks against its running server, and
// app.objects-type when the pinned type changes while a role on the
// server, as the draft leaves it, covers objects of the old type by name.
func objectsRefusals(it Item, w, after World) []Finding {
	app := after.Apps[it.Name]
	var out []Finding
	if app.Objects != nil {
		for _, s := range app.Objects.Refusals {
			out = append(out, refusal(codeAppObjects, it.Object(), s, ""))
		}
	}
	p, live := app.Pinned, w.Apps[it.Name].Pinned
	if p == nil || live == nil || p.LiveType == "" || p.LiveType == p.Type {
		return out
	}
	if roles := rolesOn(after, it.Name, (*Scope).partial); len(roles) > 0 {
		verb := "names"
		if len(roles) > 1 {
			verb = "name"
		}
		out = append(out, refusal(codeObjectsType, it.Object(), fmt.Sprintf(
			"%s's pinned type changes from %s to %s while %s %s %s. Make those roles cover every %s, or remove their access rows on %s, in this draft.",
			it.Name, p.LiveType, p.Type, listWords(roles, "and"), verb, live.Plural, p.Type, it.Name), ""))
	}
	return out
}

// rolesOn answers, sorted, the roles whose access row in w is on server
// and whose covers line meets keep.
func rolesOn(w World, server string, keep func(*Scope) bool) []string {
	var out []string
	for name, acc := range w.Access {
		if acc.Server == server && keep(acc.Covers) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// pinNotes answers the lines that inform about a change of the pinned type
// of the App put it, from w to after: the first pin, which makes every row
// without a covers line cover every object, a pinned server with no
// listing credential, and an unpin, which takes the roles on some objects
// back to every object.
func pinNotes(it Item, w, after World) []Finding {
	p, live := after.Apps[it.Name].Pinned, w.Apps[it.Name].Pinned
	var out []Finding
	switch {
	case p != nil && p.LiveType == "":
		if p.Credential == "missing" {
			out = append(out, note(codeObjectsListing, ClassWarning, it.Object(), fmt.Sprintf(
				"%s has no listing credential, so Straza cannot list its %s, and only roles on every %s can use its %s tools until it has one. Add one with strazactl apps secret set %s --listing after you publish.",
				it.Name, p.Plural, p.Type, p.Type, it.Name)))
		}
		if _, existed := w.Apps[it.Name]; !existed {
			break
		}
		if roles := rolesOn(after, it.Name, func(s *Scope) bool { return s == nil }); len(roles) > 0 {
			verb, them := "now cover", "them"
			if len(roles) == 1 {
				verb, them = "now covers", "it"
			}
			out = append(out, note(codeObjectsFirstPin, ClassInfo, it.Object(), fmt.Sprintf(
				"Pinning %s on %s keeps today's reach: %s %s every %s. Narrow %s in this draft or later.", p.Plural, it.Name, listWords(roles, "and"), verb, p.Type, them)))
		}
	case p == nil && live != nil:
		if roles := rolesOn(w, it.Name, (*Scope).partial); len(roles) > 0 {
			loses := "loses its list"
			if len(roles) > 1 {
				loses = "lose their lists"
			}
			out = append(out, note(codeObjectsUnpin, ClassInfo, it.Object(), fmt.Sprintf(
				"Removing %s's pinned %s makes every role on %s reach every %s again: %s %s of %s.", it.Name, live.Plural, it.Name, live.Type, listWords(roles, "and"), loses, live.Plural)))
		}
	}
	return out
}

// reachNotes answers a warning for each tool whose reach the App put it
// changes, from w to after under the same pinned type, from one object or
// none to every object or the credential's, while a role on some objects
// in after binds it. The gate refuses such a role every call of the tool,
// so the warning names the roles, and the draft still publishes. A tool
// the block leaves out reaches every object (reachOf).
func reachNotes(it Item, w, after World) []Finding {
	app, live := after.Apps[it.Name], w.Apps[it.Name].Pinned
	p := app.Pinned
	if p == nil || live == nil || p.Type != live.Type {
		return nil
	}
	held := map[string][]string{}
	for _, role := range rolesOn(after, it.Name, (*Scope).partial) {
		for _, tool := range boundTools(app, after.Access[role].Tools) {
			was, now := live.reachOf(tool), p.reachOf(tool)
			if (was == reachArgument || was == reachNone) && (now == reachAll || now == reachCredential) {
				held[tool] = append(held[tool], role)
			}
		}
	}
	var out []Finding
	for _, tool := range sortedKeys(held) {
		roles := held[tool]
		who, those, them := "a role", "that role", "it"
		if len(roles) > 1 {
			who, those, them = "roles", "those roles", "them"
		}
		reach := fmt.Sprintf("%s reaches every %s on %s", tool, p.Type, it.Name)
		if p.reachOf(tool) == reachCredential {
			reach = fmt.Sprintf("the calls of %s on %s name no %s, and Straza does not check a role's own login yet", tool, it.Name, p.Type)
		}
		out = append(out, note(codeObjectsReach, ClassWarning, it.Object(), fmt.Sprintf(
			"After this draft, %s, so %s, %s on some %s, can no longer call it. Take %s out of %s, or make %s cover every %s, in this draft or later.",
			reach, listWords(roles, "and"), who, p.Plural, tool, those, them, p.Type)))
	}
	return out
}

// objectsNotes answers the lines that only inform about the straza.objects
// block of the App put it: the notes of a change of its pinned type, the
// tools a re-pin takes from the roles on some objects, the tools it leaves
// undecided, which read as reaching every object, the tools it names that
// the server does not list, and a server that is not running, so nothing
// was compared.
func objectsNotes(it Item, w, after World) []Finding {
	out := append(pinNotes(it, w, after), reachNotes(it, w, after)...)
	r := after.Apps[it.Name].Objects
	switch {
	case r == nil:
		return out
	case !r.Running:
		return append(out, note(codeUncheckedObjects, ClassUnchecked, it.Object(), fmt.Sprintf(
			"Straza could not compare the pinned %s with the tools of %s, because %s is not running. Check the draft again once it runs.", r.Plural, it.Name, it.Name)))
	}
	if len(r.Undecided) > 0 {
		which := "each is"
		if len(r.Undecided) == 1 {
			which = "it is"
		}
		f := note(codeObjectsUndecided, ClassWarning, it.Object(), fmt.Sprintf(
			"Needs a decision: straza.objects.tools does not name %s, so %s treated as reaching every %s until you decide.", listWords(r.Undecided, "and"), which, r.Type))
		f.Fix = "Name each under straza.objects.tools with all, none, credential or {argument: <name>}."
		out = append(out, f)
	}
	if len(r.Unknown) > 0 {
		out = append(out, note(codeObjectsUnknown, ClassInfo, it.Object(), fmt.Sprintf(
			"straza.objects.tools names %s, which %s does not list. Its reach applies once the server lists it.", listWords(r.Unknown, "and"), it.Name)))
	}
	return out
}
