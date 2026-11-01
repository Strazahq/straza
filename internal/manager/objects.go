package manager

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

// Reach values of a tool in an objects declaration.
const (
	ReachArgument   = "argument"
	ReachCredential = "credential"
	ReachAll        = "all"
	ReachNone       = "none"
)

// ObjectsSpec is the straza.objects block of app-manifest revision 7.
type ObjectsSpec struct {
	Type   string               `yaml:"type" json:"type"`
	Plural string               `yaml:"plural,omitempty" json:"plural,omitempty"`
	List   ObjectListSpec       `yaml:"list" json:"list"`
	Tools  map[string]ToolReach `yaml:"tools,omitempty" json:"tools,omitempty"`
}

// ObjectListSpec names the listing tool and the attributes its items carry.
type ObjectListSpec struct {
	Tool       string            `yaml:"tool" json:"tool"`
	Attributes []ObjectAttribute `yaml:"attributes,omitempty" json:"attributes,omitempty"`
}

// ObjectAttribute is one attribute a listing returns and who may set it in the source.
type ObjectAttribute struct {
	Name  string `yaml:"name" json:"name"`
	SetBy string `yaml:"setBy,omitempty" json:"setBy,omitempty"` // org or object, and empty reads as object
}

// ToolReach is how one tool reaches objects. Its YAML and JSON form is the
// scalar "all", "none" or "credential", or the mapping {argument: <name>}.
// The mapping form refuses any key but argument.
type ToolReach struct {
	Reach    string
	Argument string // set exactly when Reach == ReachArgument
}

// PluralWord answers Plural, or Type plus "s" when Plural is empty.
func (o ObjectsSpec) PluralWord() string {
	if o.Plural != "" {
		return o.Plural
	}
	return o.Type + "s"
}

// ReachOf answers the pinned reach of tool, ReachAll for a tool the block
// does not name.
func (o ObjectsSpec) ReachOf(tool string) ToolReach {
	if r, ok := o.Tools[tool]; ok {
		return r
	}
	return ToolReach{Reach: ReachAll}
}

// maxObjectAttributes caps the attributes a listing may carry per object.
const maxObjectAttributes = 16

var (
	objectTypeRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	objectPluralRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	attributeRe    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)
	argumentRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)
	// mcpToolNameRe is the tool name rule of the MCP specification:
	// 1 to 128 ASCII letters, digits, underscores, hyphens and dots.
	mcpToolNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
)

// validateObjects checks the straza.objects block against the rules of
// SPEC.md section 7. The JSON Schema mirrors every rule but the listing
// tool's reach, since a schema cannot tie a key of tools to list.tool.
func validateObjects(o *ObjectsSpec, fail func(string, ...any)) {
	if !objectTypeRe.MatchString(o.Type) {
		fail("objects.type %q is not a valid object type. Write one lowercase word of up to 32 letters, digits or hyphens that starts with a letter, such as repo", o.Type)
	}
	if o.Plural != "" && !objectPluralRe.MatchString(o.Plural) {
		fail("objects.plural %q is not a valid plural. Write one lowercase word of up to 40 letters, digits or hyphens that starts with a letter, such as repos, or leave plural out to use the type plus s", o.Plural)
	}
	switch {
	case o.List.Tool == "":
		fail("objects.list.tool is empty, so Straza does not know which tool lists the objects. Name the server's read-only listing tool, such as repo_list")
	case !mcpToolNameRe.MatchString(o.List.Tool):
		fail("objects.list.tool %q is not a valid MCP tool name. Write the tool's name as the server lists it: 1 to 128 letters, digits, underscores, hyphens or dots", o.List.Tool)
	}
	if n := len(o.List.Attributes); n > maxObjectAttributes {
		fail("objects.list.attributes names %d attributes, and the limit is %d. Keep the attributes roles match on and remove the rest", n, maxObjectAttributes)
	}
	seen := map[string]bool{}
	for _, a := range o.List.Attributes {
		switch {
		case !attributeRe.MatchString(a.Name):
			fail("objects.list.attributes name %q is not a valid attribute name. Write up to 64 letters, digits, underscores, dots or hyphens that start with a letter, such as team", a.Name)
		case seen[a.Name]:
			fail("objects.list.attributes names %q twice. Keep one entry for it", a.Name)
		}
		seen[a.Name] = true
		if a.SetBy != "" && a.SetBy != "org" && a.SetBy != "object" {
			fail("objects.list.attributes %q has setBy %q. Write org when only the organization's admins can set it, or object when the object's own admins can set it too", a.Name, a.SetBy)
		}
	}
	names := make([]string, 0, len(o.Tools))
	for name := range o.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		r := o.Tools[name]
		if !mcpToolNameRe.MatchString(name) {
			fail("objects.tools names %q, which is not a valid MCP tool name. Write the tool's name as the server lists it: 1 to 128 letters, digits, underscores, hyphens or dots", name)
		}
		switch r.Reach {
		case ReachAll, ReachNone, ReachCredential:
		case ReachArgument:
			if r.Argument == "" {
				fail("objects.tools.%s says argument without naming one. Write {argument: <name>} with the name of the tool's argument that carries the object", name)
				continue
			}
			if !argumentRe.MatchString(r.Argument) {
				fail("objects.tools.%s names the argument %q, which is not a valid argument name. Write up to 64 letters, digits, underscores or hyphens that start with a letter or underscore", name, r.Argument)
				continue
			}
		default:
			fail("objects.tools.%s has the reach %q. %s", name, r.Reach, reachForms)
			continue
		}
		// The listing tool returns every object, whatever its pin says, so a
		// reach other than all would let a role that binds it read objects
		// the role does not cover. A malformed reach or name has its own
		// fault above.
		if name == o.List.Tool && r.Reach != ReachAll && mcpToolNameRe.MatchString(name) {
			fail("objects.tools.%s gives the listing tool the reach %s. The listing tool returns every %s, so its reach is all. Write all, or leave %s out of objects.tools", name, reachWords(r), o.Type, name)
		}
	}
}

// reachForms is the fix every reach refusal names.
const reachForms = "Write all, none, credential, or {argument: <name>}"

// MarshalJSON writes the scalar form, or {"argument": name} for ReachArgument.
func (t ToolReach) MarshalJSON() ([]byte, error) {
	if t.Reach == ReachArgument {
		return json.Marshal(map[string]string{"argument": t.Argument})
	}
	return json.Marshal(t.Reach)
}

// UnmarshalJSON reads the scalar form or the {"argument": name} mapping and
// refuses any other key.
func (t *ToolReach) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*t = ToolReach{Reach: s}
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("objects.tools: a tool's reach is neither a word nor a mapping. %s", reachForms)
	}
	raw, ok := m["argument"]
	if len(m) != 1 || !ok {
		return fmt.Errorf("objects.tools: a tool's mapping takes only the key argument. %s", reachForms)
	}
	var arg string
	if err := json.Unmarshal(raw, &arg); err != nil {
		return fmt.Errorf("objects.tools: argument must be the name of a string argument. %s", reachForms)
	}
	*t = ToolReach{Reach: ReachArgument, Argument: arg}
	return nil
}

// MarshalYAML writes the scalar form, or {argument: name} for ReachArgument.
func (t ToolReach) MarshalYAML() (any, error) {
	if t.Reach == ReachArgument {
		return map[string]string{"argument": t.Argument}, nil
	}
	return t.Reach, nil
}

// UnmarshalYAML reads the scalar form or the {argument: name} mapping and
// refuses any other key, a second form or a nested value. validate checks
// the scalar's word.
func (t *ToolReach) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*t = ToolReach{Reach: n.Value}
		return nil
	case yaml.MappingNode:
		if len(n.Content) != 2 || n.Content[0].Value != "argument" {
			return fmt.Errorf("line %d: objects.tools: a tool's mapping takes only the key argument, so it cannot carry a second form. %s", n.Line, reachForms)
		}
		if v := n.Content[1]; v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
			return fmt.Errorf("line %d: objects.tools: argument must be the name of a string argument. %s", n.Line, reachForms)
		}
		*t = ToolReach{Reach: ReachArgument, Argument: n.Content[1].Value}
		return nil
	default:
		return fmt.Errorf("line %d: objects.tools: a tool's reach is neither a word nor a mapping. %s", n.Line, reachForms)
	}
}
