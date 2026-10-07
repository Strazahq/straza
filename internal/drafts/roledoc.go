package drafts

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// RoleAPIVersion is the apiVersion of every canonical object document.
const RoleAPIVersion = "straza.dev/v1beta1"

// RoleDoc is the canonical Role document of spec/objects: name-keyed and
// id-free, with every list sorted, so the export of a role and the Role
// item of a draft are the same bytes. One serializer, Marshal, serves the
// export route, strazactl, the console's Download button and every draft.
type RoleDoc struct {
	APIVersion string      `yaml:"apiVersion"`
	Kind       string      `yaml:"kind"`
	Metadata   RoleDocMeta `yaml:"metadata"`
	Spec       RoleDocSpec `yaml:"spec"`
}

// RoleDocMeta names the role.
type RoleDocMeta struct {
	Name string `yaml:"name"`
}

// RoleDocSpec is the role itself. Kind is the wire kind, straza for a role on
// the control plane. Server names the server that owns an owned role.
// Bindings holds the role's one access row, and Packs is read and never
// applied, because knowledge packs are bound directly.
type RoleDocSpec struct {
	Kind        string        `yaml:"kind"`
	Description string        `yaml:"description,omitempty"`
	Server      string        `yaml:"server,omitempty"`
	Implies     []string      `yaml:"implies,omitempty"`
	Bindings    []RoleBinding `yaml:"bindings,omitempty"`
	Packs       []string      `yaml:"packs,omitempty"`
}

// RoleBinding is an access row: the server by name and the tool matchers,
// where ["*"] is every tool and an empty list grants nothing.
type RoleBinding struct {
	App   string   `yaml:"app"`
	Tools []string `yaml:"tools"`
}

// docTypeWords names the Go types of the documents this package decodes
// strictly in the words of their YAML paths, so a decoder's refusal reads in
// the document's own terms.
var docTypeWords = strings.NewReplacer(
	"drafts.RoleDocSpec", "spec",
	"drafts.RoleDocMeta", "metadata",
	"drafts.RoleBinding", "a binding",
	"drafts.RoleDoc", "the document",
	"drafts.removalSpec", "spec",
	"drafts.removalDoc", "the document",
)

// decodeWords is a strict decoder's refusal on one line, in the document's
// own terms.
func decodeWords(err error) string {
	s := strings.TrimPrefix(err.Error(), "yaml: unmarshal errors:\n  ")
	return docTypeWords.Replace(strings.ReplaceAll(s, "\n  ", "; "))
}

// Marshal answers d in canonical form: its lists sorted, and the YAML
// encoder's defaults, four-space indent included. It sorts a copy and
// leaves d as it is.
func (d RoleDoc) Marshal() ([]byte, error) {
	d.Spec.Implies = sortedCopy(d.Spec.Implies)
	d.Spec.Packs = sortedCopy(d.Spec.Packs)
	bindings := make([]RoleBinding, len(d.Spec.Bindings))
	for i, b := range d.Spec.Bindings {
		bindings[i] = RoleBinding{App: b.App, Tools: sortedCopy(b.Tools)}
	}
	sort.SliceStable(bindings, func(i, j int) bool { return bindings[i].App < bindings[j].App })
	if len(bindings) > 0 {
		d.Spec.Bindings = bindings
	}
	return yaml.Marshal(d)
}

// ParseRole reads one Role document strictly: every field must be one the
// document defines, the apiVersion and kind must be the canonical ones, the
// role must be named, a spec.kind it gives must be one the admin API knows,
// each binding must name its server, and no implication or tool of a
// binding may be listed twice, since each is one row in the store. A
// missing spec.kind and a second binding are left for the caller, which
// refuses each with its own code.
func ParseRole(doc string) (RoleDoc, error) {
	dec := yaml.NewDecoder(strings.NewReader(doc))
	dec.KnownFields(true)
	var d RoleDoc
	if err := dec.Decode(&d); errors.Is(err, io.EOF) {
		return RoleDoc{}, errors.New("the text holds no document")
	} else if err != nil {
		return RoleDoc{}, errors.New(decodeWords(err))
	}
	for {
		var more yaml.Node
		err := dec.Decode(&more)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || !emptyDocument(&more) {
			return RoleDoc{}, errors.New("the text holds more than one document")
		}
	}
	switch {
	case d.APIVersion != RoleAPIVersion:
		return RoleDoc{}, fmt.Errorf("apiVersion must be %s, and it is %q", RoleAPIVersion, d.APIVersion)
	case d.Kind != string(KindRole):
		return RoleDoc{}, fmt.Errorf("kind must be Role, and it is %q", d.Kind)
	case d.Metadata.Name == "":
		return RoleDoc{}, errors.New("metadata.name is empty")
	case d.Spec.Kind != "" && !roleKindValid(d.Spec.Kind):
		return RoleDoc{}, fmt.Errorf("spec.kind must be business, application, approver or straza, and it is %q", d.Spec.Kind)
	}
	if name := repeated(d.Spec.Implies); name != "" {
		return RoleDoc{}, fmt.Errorf("spec.implies lists %s twice", visible(name))
	}
	for i, b := range d.Spec.Bindings {
		if b.App == "" {
			return RoleDoc{}, fmt.Errorf("spec.bindings[%d].app is empty", i)
		}
		if tool := repeated(b.Tools); tool != "" {
			return RoleDoc{}, fmt.Errorf("spec.bindings[%d].tools lists %s twice", i, visible(tool))
		}
	}
	return d, nil
}

// repeated answers the first entry of list that an earlier entry repeats,
// or "".
func repeated(list []string) string {
	seen := make(map[string]bool, len(list))
	for _, s := range list {
		if seen[s] {
			return s
		}
		seen[s] = true
	}
	return ""
}

// RoleDocOf is the canonical document of the role name as w holds it, and
// false when w holds no such role. Its binding is the role's access row,
// each tool once, and a row whose server is gone is left out, as the export
// leaves it out.
func RoleDocOf(w World, name string) (RoleDoc, bool) {
	ro, ok := w.Roles[name]
	if !ok {
		return RoleDoc{}, false
	}
	d := RoleDoc{
		APIVersion: RoleAPIVersion,
		Kind:       string(KindRole),
		Metadata:   RoleDocMeta{Name: name},
		Spec: RoleDocSpec{Kind: wireKind(ro), Description: ro.Description, Server: ro.Owner,
			Implies: sortedCopy(w.Implies[name]), Packs: sortedCopy(ro.Packs)},
	}
	// A row stored before the document parser refused a repeated tool keeps
	// each tool once here, so the document of every live role parses.
	if acc, ok := w.Access[name]; ok && acc.Server != "" {
		d.Spec.Bindings = []RoleBinding{{App: acc.Server, Tools: slices.Compact(sortedCopy(acc.Tools))}}
	}
	return d, true
}

// roleText is the canonical text of d. The encoder cannot fail on a
// document of strings and string lists, so a failure leaves the text empty,
// which no parser reads as a role.
func roleText(d RoleDoc) string {
	out, err := d.Marshal()
	if err != nil {
		return ""
	}
	return string(out)
}

// sortedCopy answers a sorted copy of list, nil for an empty one.
func sortedCopy(list []string) []string {
	if len(list) == 0 {
		return nil
	}
	out := slices.Clone(list)
	sort.Strings(out)
	return out
}
