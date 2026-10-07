package drafts

import (
	"path/filepath"
	"strconv"
	"strings"
)

// titleVerbs are the verbs of a title in the order its phrases come.
var titleVerbs = []string{"add", "change", "turn off", "remove"}

// titleNouns names each kind in a title, one and several.
var titleNouns = map[Kind][2]string{
	KindApp:       {"server", "servers"},
	KindRole:      {"role", "roles"},
	KindPolicySet: {"approval set", "approval sets"},
}

// Title names what d does in one line, from its items and never from its
// note: one phrase per verb in the order add, change, turn off and remove,
// joined by " and then ", as "Add server github, 2 roles and 1 approval set
// and then change role developer". Within a phrase the kinds come in the
// order server, role, approval set, and when the first kind has one object
// the phrase names it and counts the rest, else it counts every kind. A put
// adds an object its item's base says did not exist, and changes one that
// did, so an item not checked yet reads as an add. An undo reads "Undo draft
// {n}", a draft whose file did not read reads "Does not read", and a draft
// of the apps directory puts its file's name and a colon in front.
func Title(d Draft) string {
	title := ""
	switch {
	case d.Reverts != "":
		title = "Undo draft " + d.Reverts
	case len(d.Items) == 0 && d.Refusal != "":
		title = "Does not read"
	case len(d.Items) == 0:
		title = "No change"
	default:
		byVerb := map[string][]Item{}
		for _, it := range d.Items {
			verb := "change"
			switch {
			case it.Op == OpRemove:
				verb = "remove"
			case it.Op == OpOff:
				verb = "turn off"
			case it.Base == "":
				verb = "add"
			}
			byVerb[verb] = append(byVerb[verb], it)
		}
		var phrases []string
		for _, verb := range titleVerbs {
			if items := byVerb[verb]; len(items) > 0 {
				phrases = append(phrases, verb+" "+titleObjects(items))
			}
		}
		title = strings.Join(phrases, " and then ")
		title = strings.ToUpper(title[:1]) + title[1:]
	}
	if d.Door == DoorAppsDir && d.Source != "" {
		title = visible(filepath.Base(d.Source)) + ": " + title
	}
	return title
}

// titleObjects words the objects of one phrase, such as "server github, 2
// roles and 1 approval set".
func titleObjects(items []Item) string {
	byKind := map[Kind][]string{}
	for _, it := range items {
		byKind[it.Kind] = append(byKind[it.Kind], it.Name)
	}
	var parts []string
	for _, k := range []Kind{KindApp, KindRole, KindPolicySet} {
		names := byKind[k]
		if len(names) == 0 {
			continue
		}
		noun := titleNouns[k]
		switch {
		case len(parts) == 0 && len(names) == 1:
			parts = append(parts, noun[0]+" "+visible(names[0]))
		case len(names) == 1:
			parts = append(parts, "1 "+noun[0])
		default:
			parts = append(parts, strconv.Itoa(len(names))+" "+noun[1])
		}
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}
