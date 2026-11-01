package manager

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The _meta keys a server sends to mark its tools.
const (
	markPrefix       = "ai.straza/"
	markObject       = markPrefix + "object"
	markListsObjects = markPrefix + "lists-objects"
)

// objectMark is the ai.straza/object mark. Unknown keys are ignored: a mark
// only pre-fills a draft, and the draft passes the strict manifest parse.
type objectMark struct {
	Type     string  `json:"type"`
	Argument *string `json:"argument"`
	Reach    *string `json:"reach"`
}

// listsObjectsMark is the ai.straza/lists-objects mark on the listing tool.
type listsObjectsMark struct {
	Type       string            `json:"type"`
	Attributes []ObjectAttribute `json:"attributes"`
}

// DeclaredObjects builds the straza.objects block the marks on tools
// declare. ok is false when no tool carries a mark. faults are operator
// sentences, one per broken rule (two types, no listing tool or two, a mark
// that does not parse); the block is unusable when faults is non-empty.
func DeclaredObjects(server string, tools []*mcp.Tool) (spec ObjectsSpec, ok bool, faults []string) {
	sorted := slices.DeleteFunc(slices.Clone(tools), func(t *mcp.Tool) bool { return t == nil })
	slices.SortFunc(sorted, func(a, b *mcp.Tool) int { return strings.Compare(a.Name, b.Name) })
	types := map[string]bool{}
	var listers []string
	listMarks := 0
	for _, t := range sorted {
		if raw, has := t.Meta[markObject]; has {
			ok = true
			reach, typ, fault := readObjectMark(t.Name, raw)
			if fault != "" {
				faults = append(faults, fault)
			} else {
				types[typ] = true
				if spec.Tools == nil {
					spec.Tools = map[string]ToolReach{}
				}
				spec.Tools[t.Name] = reach
			}
		}
		if raw, has := t.Meta[markListsObjects]; has {
			ok, listMarks = true, listMarks+1
			var m listsObjectsMark
			if !decodeMark(raw, &m) || m.Type == "" {
				faults = append(faults, fmt.Sprintf("The %s mark on %s does not parse. It needs a type, and each attribute it lists needs a name and may carry setBy.", markListsObjects, t.Name))
				continue
			}
			types[m.Type] = true
			listers = append(listers, t.Name)
			if spec.List.Tool == "" {
				spec.List = ObjectListSpec{Tool: t.Name, Attributes: m.Attributes}
			}
		}
	}
	if !ok {
		return ObjectsSpec{}, false, nil
	}
	if names := slices.Sorted(maps.Keys(types)); len(names) == 1 {
		spec.Type = names[0]
	} else if len(names) > 1 {
		faults = append(faults, fmt.Sprintf("%s's tools mark %d object types: %s. A server has one object type, so its author must mark every tool with the same type.", server, len(names), strings.Join(names, ", ")))
	}
	switch {
	case listMarks == 0:
		faults = append(faults, fmt.Sprintf("%s marks objects, but no tool carries an %s mark, so Straza has no tool to list them with.", server, markListsObjects))
	case len(listers) > 1:
		faults = append(faults, fmt.Sprintf("%s marks %d listing tools: %s. A server has one listing tool, so its author must keep the %s mark on one of them.", server, len(listers), strings.Join(listers, ", "), markListsObjects))
	}
	if len(faults) == 0 {
		validateObjects(&spec, func(format string, args ...any) {
			faults = append(faults, fmt.Sprintf("%s's marks break a manifest rule: %s.", server, fmt.Sprintf(format, args...)))
		})
	}
	return spec, true, faults
}

// readObjectMark reads one ai.straza/object mark, or answers the fault
// sentence that says why it cannot.
func readObjectMark(tool string, raw any) (ToolReach, string, string) {
	var m objectMark
	switch {
	case !decodeMark(raw, &m) || m.Type == "" || (m.Argument == nil && m.Reach == nil):
		return ToolReach{}, "", fmt.Sprintf("The %s mark on %s does not parse. It needs a type and either an argument or a reach.", markObject, tool)
	case m.Argument != nil && m.Reach != nil:
		return ToolReach{}, "", fmt.Sprintf("The %s mark on %s carries both an argument and a reach. It takes one of them.", markObject, tool)
	case m.Argument != nil:
		return ToolReach{Reach: ReachArgument, Argument: *m.Argument}, m.Type, ""
	}
	switch *m.Reach {
	case ReachAll, ReachNone, ReachCredential:
		return ToolReach{Reach: *m.Reach}, m.Type, ""
	}
	return ToolReach{}, "", fmt.Sprintf("The %s mark on %s has the reach %q. The reach is all, none or credential, or the mark names an argument instead.", markObject, tool, *m.Reach)
}

// decodeMark reads a _meta value into v through its JSON form, so a mark
// decoded from the wire and one built in Go read the same.
func decodeMark(raw any, v any) bool {
	b, err := json.Marshal(raw)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

// AtLeastAsStrict reports whether t may stand where the server marks live:
// the same reach, all over any mark, or any mark over none. A pinned mark
// that fails this is looser than the server's own and cannot be published.
func (t ToolReach) AtLeastAsStrict(live ToolReach) bool {
	return t == live || t.Reach == ReachAll || live.Reach == ReachNone
}
