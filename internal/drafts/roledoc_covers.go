package drafts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Scope is the covers line of a binding: which objects of the server's
// pinned type it covers. Every covers each object, the ones listed later
// included. Otherwise Objects names the covered objects as written, and an
// ObjectRef's ID stays empty until the server resolves its value.
type Scope struct {
	Every   bool
	Objects []ObjectRef
}

// ObjectRef is one named object: the value a role file writes and the id it
// resolves to.
type ObjectRef struct{ Value, ID string }

// Key answers the scope key every binding comparison reads: "" for a nil
// scope, "every", or "objects:" and the sorted ids joined by NUL. Ids
// alone count, so a renamed object is not a change.
func (s *Scope) Key() string {
	switch {
	case s == nil:
		return ""
	case s.Every:
		return "every"
	}
	ids := make([]string, len(s.Objects))
	for i, o := range s.Objects {
		ids[i] = o.ID
	}
	slices.Sort(ids)
	return "objects:" + strings.Join(ids, "\x00")
}

// coversShape is the refusal of a covers line of neither form.
const coversShape = "covers must be every or a mapping with one key, objects, that lists names"

// MarshalYAML writes the covers line of a role file: the word every, or
// objects with the values sorted. Ids never reach a file.
func (s *Scope) MarshalYAML() (any, error) {
	if s.Every {
		return "every", nil
	}
	values := make([]string, len(s.Objects))
	for i, o := range s.Objects {
		values[i] = o.Value
	}
	slices.Sort(values)
	return struct {
		Objects []string `yaml:"objects"`
	}{values}, nil
}

// UnmarshalYAML reads the covers line of a role file strictly: the plain
// word every, or a mapping whose one key, objects, lists strings. It reads
// the node by hand, because a decoder an Unmarshaler opens does not refuse
// unknown keys. An empty list and a repeated value read, and the draft
// check refuses each in the words of the server's type.
func (s *Scope) UnmarshalYAML(n *yaml.Node) error {
	switch {
	case n.Kind == yaml.ScalarNode && n.ShortTag() == "!!str" && n.Value == "every":
		*s = Scope{Every: true}
		return nil
	case n.Kind != yaml.MappingNode || len(n.Content) == 0:
		return fmt.Errorf("line %d: %s", n.Line, coversShape)
	}
	var list *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i]
		switch {
		case key.Value != "objects":
			return fmt.Errorf("line %d: a covers mapping takes only the key objects, and this one carries %s. Write covers: every for every object, or covers: {objects: [<name>, ...]} for some",
				key.Line, visible(key.Value))
		case list != nil:
			return fmt.Errorf("line %d: covers carries objects twice", key.Line)
		}
		list = n.Content[i+1]
	}
	if list.Kind != yaml.SequenceNode {
		return fmt.Errorf("line %d: covers.objects must be a list of names", list.Line)
	}
	out := Scope{Objects: make([]ObjectRef, 0, len(list.Content))}
	for _, v := range list.Content {
		switch {
		case v.Kind != yaml.ScalarNode || v.Anchor != "":
			return fmt.Errorf("line %d: covers.objects lists an entry that is not a name", v.Line)
		case v.ShortTag() != "!!str":
			return fmt.Errorf("line %d: covers.objects lists %s, which is not a string. Quote it", v.Line, visible(v.Value))
		}
		out.Objects = append(out.Objects, ObjectRef{Value: v.Value})
	}
	*s = out
	return nil
}

// nullCovers refuses a covers key of the Role document text doc, which d
// reads, that holds no value: null, ~ or nothing at all. The decoder skips
// UnmarshalYAML for a null and leaves the line unset, which would read as
// no covers line, so the text is walked again for one.
func nullCovers(doc string, d RoleDoc) error {
	if !slices.ContainsFunc(d.Spec.Bindings, func(b RoleBinding) bool { return b.Covers == nil }) {
		return nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(doc), &root); err != nil || len(root.Content) == 0 {
		return nil
	}
	for _, b := range mappingValue(mappingValue(root.Content[0], "spec"), "bindings").Content {
		v := mappingValue(b, "covers")
		line := v.Line
		if v.Kind == yaml.AliasNode && v.Alias != nil {
			v = v.Alias
		}
		if v.Kind == yaml.ScalarNode && v.ShortTag() == "!!null" {
			return fmt.Errorf("line %d: %s", line, coversShape)
		}
	}
	return nil
}

// mappingValue answers the value of key in the mapping node n, or an empty
// node when n is no mapping or lacks key.
func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n != nil && n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				return n.Content[i+1]
			}
		}
	}
	return &yaml.Node{}
}

// scopeJSON is the object form of the covers line on the admin API.
type scopeJSON struct {
	Every   bool             `json:"every,omitempty"`
	Objects *[]objectRefJSON `json:"objects,omitempty"`
}

// objectRefJSON is one object of the covers line on the admin API.
type objectRefJSON struct {
	Value string `json:"value"`
	ID    string `json:"id"`
}

// coversJSONForms is the two forms of a covers object, which every refusal
// of one names.
const coversJSONForms = `{"every": true} or {"objects": [{"value": "<name>"}]}`

// CoversError is the refusal of a covers object an admin API body sends,
// so a route answers its words rather than a decoder's.
type CoversError struct{ Reason string }

// Error answers the reason, which names the forms to send.
func (e *CoversError) Error() string { return e.Reason }

// MarshalJSON writes the covers line as the admin API carries it:
// {"every":true}, or objects with each value and id in stored order.
func (s *Scope) MarshalJSON() ([]byte, error) {
	if s.Every {
		return json.Marshal(scopeJSON{Every: true})
	}
	list := make([]objectRefJSON, len(s.Objects))
	for i, o := range s.Objects {
		list[i] = objectRefJSON(o)
	}
	return json.Marshal(scopeJSON{Objects: &list})
}

// UnmarshalJSON reads the covers object of an admin API body strictly: an
// unknown key and both forms at once are refused with a *CoversError. An
// id the body sends is kept here and replaced when the server resolves
// the value.
func (s *Scope) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var w scopeJSON
	if err := dec.Decode(&w); err != nil {
		if field, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
			return &CoversError{fmt.Sprintf("covers carries %s, and it takes only every or objects. Send %s", field, coversJSONForms)}
		}
		return &CoversError{"covers must be " + coversJSONForms}
	}
	if w.Every && w.Objects != nil {
		return &CoversError{"covers takes every or objects, not both. Send " + coversJSONForms}
	}
	out := Scope{Every: w.Every}
	if w.Objects != nil {
		out.Objects = make([]ObjectRef, len(*w.Objects))
		for i, o := range *w.Objects {
			out.Objects[i] = ObjectRef(o)
		}
	}
	*s = out
	return nil
}

// clone answers a copy of s that shares nothing with it, nil for nil.
func (s *Scope) clone() *Scope {
	if s == nil {
		return nil
	}
	return &Scope{Every: s.Every, Objects: slices.Clone(s.Objects)}
}

// resolved answers a copy of s with the id of each object filled. A value
// live names, the covers line live state holds for the same role on the
// same server, keeps the id live holds for it, so restating a role never
// moves its grant to an object created later under that name. Any other
// value takes its id from ids, the value-to-id map of the object list, and
// a value neither holds keeps an empty id. nil answers nil.
func (s *Scope) resolved(ids map[string]string, live *Scope) *Scope {
	out := s.clone()
	if out == nil {
		return nil
	}
	held := map[string]string{}
	if live != nil {
		for _, o := range live.Objects {
			held[o.Value] = o.ID
		}
	}
	for i, o := range out.Objects {
		id, ok := held[o.Value]
		if !ok {
			id = ids[o.Value]
		}
		out.Objects[i].ID = id
	}
	return out
}

// partial reports whether s covers some objects rather than every object.
func (s *Scope) partial() bool {
	return s != nil && !s.Every
}

// RowCovers is the covers line of the binding b of the role name with each
// object's id resolved over w: the role's live row on the same server
// first, then the object list ids of b's server that w holds. Every
// comparison of a binding a draft writes with a live row reads this.
func RowCovers(w World, name string, b RoleBinding) *Scope {
	var live *Scope
	if acc, ok := w.Access[name]; ok && acc.Server == b.App {
		live = acc.Covers
	}
	var ids map[string]string
	if p := w.Apps[b.App].Pinned; p != nil {
		ids = p.IDs
	}
	return b.Covers.resolved(ids, live)
}

// withoutCovers answers d with no covers line on its bindings, for a
// comparison that reads the covers line by id on its own.
func withoutCovers(d RoleDoc) RoleDoc {
	bindings := slices.Clone(d.Spec.Bindings)
	for i := range bindings {
		bindings[i].Covers = nil
	}
	d.Spec.Bindings = bindings
	return d
}
