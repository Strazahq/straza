package drafts

import (
	"slices"
	"strings"
)

// objectGranted answers the granted fact a probe of tool on server carries
// for roles in w, as the gateway's object gate decides it once the call
// passes: always on a server that pins no type and for a tool whose reach
// is none, through a row on every object for any tool, and through a row
// on some objects for a tool whose calls name the object. A row with no
// covers line covers nothing on a pinned server, except on a server the
// draft pins first, where the publish makes it cover every object. A
// check does not know whether a tool's mark drifted, so the probe reads
// the pinned reach alone.
func objectGranted(w World, roles []string, server, tool string) bool {
	p := w.Apps[server].Pinned
	if p == nil {
		return true
	}
	reach := p.reachOf(tool)
	if reach == reachNone {
		return true
	}
	for _, r := range roles {
		acc, ok := w.Access[r]
		if !ok || acc.Server != server || !matchAnyGlob(acc.Tools, tool) {
			continue
		}
		covers := acc.Covers
		if covers == nil && p.LiveType == "" {
			covers = &Scope{Every: true}
		}
		if covers != nil && (covers.Every || (reach == reachArgument && len(covers.Objects) > 0)) {
			return true
		}
	}
	return false
}

// objectReach answers which objects of server roles in w reach through
// tool, read as objectGranted reads a row: every object when a row binds
// the tool on a server that pins no type, for a tool whose reach is none,
// or through a row on every object. A row on some objects reaches every
// object as well when tool is an argument tool whose argument differs from
// the one live names, live being the pinned type the gate reads today,
// because the gate would read the object from an argument the covers line
// was not written for. Otherwise ids holds, sorted and each once, the
// resolved ids the rows on some objects cover for a tool whose calls name
// the object, and nothing for any other tool. Roles with no row that binds
// the tool reach no object.
func objectReach(w World, live *PinnedObjects, roles []string, server, tool string) (every bool, ids []string) {
	p, reach := w.Apps[server].Pinned, reachNone
	if p != nil {
		reach = p.reachOf(tool)
	}
	moved := reach == reachArgument && live != nil && live.reachOf(tool) == reachArgument && live.Argument[tool] != p.Argument[tool]
	for _, r := range roles {
		acc, ok := w.Access[r]
		if !ok || acc.Server != server || !matchAnyGlob(acc.Tools, tool) {
			continue
		}
		covers := acc.Covers
		if reach == reachNone || (covers == nil && p.LiveType == "") {
			covers = &Scope{Every: true}
		}
		switch {
		case covers == nil:
		case covers.Every, moved:
			return true, nil
		case reach == reachArgument:
			for _, o := range covers.Objects {
				if o.ID != "" {
					ids = append(ids, o.ID)
				}
			}
		}
	}
	slices.Sort(ids)
	return false, slices.Compact(ids)
}

// reachKey answers the pinned type and the reach of each tool p names in
// one string, with the argument of each argument tool, so two declarations
// compare equal exactly when every tool reads the same reach from the same
// argument, and "" for nil.
func (p *PinnedObjects) reachKey() string {
	if p == nil {
		return ""
	}
	parts := []string{p.Type}
	for tool, reach := range p.Reach {
		if reach == reachArgument {
			reach += "\x01" + p.Argument[tool]
		}
		parts = append(parts, tool+"="+reach)
	}
	slices.Sort(parts[1:])
	return strings.Join(parts, "\x00")
}
