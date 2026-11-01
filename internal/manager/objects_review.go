package manager

import (
	"fmt"
	"maps"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ReviewObjects reads the pinned straza.objects block of server against the
// tools its running server lists, before the exposure cut. refusals are
// operator sentences, one per rule the block breaks against those tools.
// undecided names, sorted, the tools the block does not name, the listing
// tool aside, because it lists every object by nature. unknown names,
// sorted, the tools the block names that the server does not list.
func ReviewObjects(server string, pinned ObjectsSpec, tools []*mcp.Tool) (refusals, undecided, unknown []string) {
	live := make(map[string]*mcp.Tool, len(tools))
	for _, t := range tools {
		if t != nil {
			live[t.Name] = t
		}
	}
	plural := pinned.PluralWord()
	switch lister, ok := live[pinned.List.Tool]; {
	case !ok:
		refusals = append(refusals, fmt.Sprintf("%s lists no tool named %s, so Straza cannot list its %s with it. Name the server's read-only listing tool under straza.objects.list.tool.",
			server, pinned.List.Tool, plural))
	case lister.Annotations == nil || !lister.Annotations.ReadOnlyHint:
		refusals = append(refusals, fmt.Sprintf("%s is not marked read-only, so Straza will not call it to list %s. The server's author must set readOnlyHint on it, or name another listing tool under straza.objects.list.tool.",
			pinned.List.Tool, plural))
	}
	for _, name := range slices.Sorted(maps.Keys(live)) {
		t := live[name]
		r, named := pinned.Tools[name]
		if !named {
			if name != pinned.List.Tool {
				undecided = append(undecided, name)
			}
			continue
		}
		if r.Reach == ReachArgument && !requiredString(t.InputSchema, r.Argument) {
			refusals = append(refusals, fmt.Sprintf("%s has no argument %s that is a required top-level string in its input schema, so Straza cannot read the %s a call names. Name the argument that carries the %s, or pin the reach all.",
				name, r.Argument, pinned.Type, pinned.Type))
		}
		// The tool marked as the lister lists every object, so its reach is
		// all even when the block names another tool under list.tool.
		if _, lists := t.Meta[markListsObjects]; lists {
			if r.Reach != ReachAll {
				refusals = append(refusals, fmt.Sprintf("%s marks %s as its listing tool, so its reach is all, and the pinned reach %s is looser. Pin all, or leave %s out of objects.tools.",
					server, name, reachWords(r), name))
			}
		} else if mark, ok := liveReach(t); ok && !r.AtLeastAsStrict(mark) {
			refusals = append(refusals, fmt.Sprintf("%s marks %s with the reach %s, and the pinned reach %s is looser. Pin the server's own reach or all.",
				server, name, reachWords(mark), reachWords(r)))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(pinned.Tools)) {
		if _, ok := live[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	return refusals, undecided, unknown
}

// requiredString reports whether the input schema of a tool has a
// top-level property arg of type string that it lists in required.
func requiredString(schema any, arg string) bool {
	var s struct {
		Properties map[string]struct {
			Type any `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if !decodeMark(schema, &s) {
		return false
	}
	p, ok := s.Properties[arg]
	return ok && p.Type == "string" && slices.Contains(s.Required, arg)
}

// liveReach answers the reach the server's own ai.straza/object mark gives
// t, and false when t carries no mark that reads.
func liveReach(t *mcp.Tool) (ToolReach, bool) {
	raw, ok := t.Meta[markObject]
	if !ok {
		return ToolReach{}, false
	}
	r, _, fault := readObjectMark(t.Name, raw)
	return r, fault == ""
}

// reachWords is a reach as a sentence names it: all, none, credential or
// argument and its name.
func reachWords(r ToolReach) string {
	if r.Reach == ReachArgument {
		return "argument " + r.Argument
	}
	return r.Reach
}
