package manager

import "github.com/modelcontextprotocol/go-sdk/mcp"

// ObjectsCall is what the gateway's object check reads of a server that
// pins an object type, for one tool.
type ObjectsCall struct {
	AppID, Server, Type, Plural string
	Reach                       ToolReach
	Drifted                     bool
}

// ObjectsFor answers the pinned declaration of server for tool, read under
// the instance's lock from the block the instance serves now, never from
// the manifest it started with. Drifted is set when the live tool leaves
// the pinned reach behind (drifted). ok is false when no instance carries
// the name or it pins no type. In-memory only.
func (m *Manager) ObjectsFor(server, tool string) (ObjectsCall, bool) {
	m.mu.RLock()
	inst, ok := m.byName[server]
	m.mu.RUnlock()
	if !ok {
		return ObjectsCall{}, false
	}
	inst.mu.Lock()
	if inst.objects == nil {
		inst.mu.Unlock()
		return ObjectsCall{}, false
	}
	pinned := inst.objects.ReachOf(tool)
	c := ObjectsCall{AppID: inst.app.ID, Server: inst.app.Name, Type: inst.objects.Type,
		Plural: inst.objects.PluralWord(), Reach: pinned}
	var live *mcp.Tool
	for _, t := range inst.inventory {
		if t != nil && t.Name == tool {
			live = t
			break
		}
	}
	inst.mu.Unlock()
	// A new inventory replaces its tools and never changes one, so the
	// schema is read off the lock that every call of the server takes.
	if live != nil {
		c.Drifted = drifted(pinned, c.Type, live)
	}
	return c, true
}

// drifted reports whether the live tool t leaves pinned behind: a pinned
// argument that is not a required top-level string of t's input schema,
// or a missing schema, as ReviewObjects refuses at publish, or a mark that
// does not parse, names another type, or is stricter than pinned. A tool
// marked ai.straza/lists-objects lists every object, so its mark reads as
// the reach all, whatever else it carries. A tool without either mark
// whose schema keeps the argument has not drifted.
func drifted(pinned ToolReach, typ string, t *mcp.Tool) bool {
	if pinned.Reach == ReachArgument && !requiredString(t.InputSchema, pinned.Argument) {
		return true
	}
	if raw, ok := t.Meta[markListsObjects]; ok {
		var m listsObjectsMark
		return !decodeMark(raw, &m) || m.Type != typ || pinned.Reach != ReachAll
	}
	raw, ok := t.Meta[markObject]
	if !ok {
		return false
	}
	live, liveType, fault := readObjectMark(t.Name, raw)
	return fault != "" || liveType != typ || !pinned.AtLeastAsStrict(live)
}
