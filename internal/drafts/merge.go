package drafts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Pick is the value Check again keeps for a field that the draft and live
// state both changed, to different values, since the item's base.
type Pick string

// The picks: the draft's value or live state's.
const (
	PickDraft Pick = "draft"
	PickLive  Pick = "live"
)

// FieldState is the field of every kind that says whether the object
// exists: present or absent for a server or a role, and on, off or absent
// for a policy set.
const FieldState = "state"

// The values of the state field.
const (
	statePresent = "present"
	stateAbsent  = "absent"
	stateOn      = "on"
	stateOff     = "off"
)

// Version is one side of a merge: the object's state in the words of an
// item's op, put, off or remove, and its document. A server's document is
// its manifest as JSON, a role's its Role document, and a set's its text.
type Version struct {
	Op  Op
	Doc string
}

// Conflict is a field that the draft and live state both changed, to
// different values, since the item's base. Each value is the field's text:
// a string as itself, any other value as compact JSON, and "" where the
// field is unset.
type Conflict struct {
	Object string `json:"object"`
	Field  string `json:"field"`
	Base   string `json:"base"`
	Draft  string `json:"draft"`
	Live   string `json:"live"`
}

// Merged is one item after Check again. Op and Doc are its new state and
// document, a server's as manifest JSON, and Changed is false when both
// equal the draft's own, so the caller keeps the item's text as written.
// Conflicts are the fields no pick settled, Picked counts the picks that
// settled one, and Drop says the item leaves the draft with the object that
// live state removed.
type Merged struct {
	Op        Op
	Doc       string
	Changed   bool
	Conflicts []Conflict
	Picked    int
	Drop      bool
}

// LiveSecretError is a merge that would take live's value at a field where
// live holds a secret, which a draft never keeps.
type LiveSecretError struct{ Object, Field string }

func (e *LiveSecretError) Error() string {
	return fmt.Sprintf("drafts: %s holds a secret at %s on live state", e.Object, e.Field)
}

// UnreadError is a merge of a document that cannot be read field by field.
// Side names it: base, draft or live.
type UnreadError struct {
	Object, Side string
	Err          error
}

func (e *UnreadError) Error() string {
	return fmt.Sprintf("drafts: the %s document of %s cannot be read field by field: %v", e.Side, e.Object, e.Err)
}

func (e *UnreadError) Unwrap() error { return e.Err }

// field is one merge field: raw is its value as its side wrote it, nil when
// unset, and key its canonical form, so values that differ only in spelling
// read as one.
type field struct {
	raw json.RawMessage
	key string
}

// fieldSet is a document read as its merge fields, by name.
type fieldSet map[string]field

// Merge is Check again for one item whose object moved on live state since
// its base: the fields the draft changed from base keep the draft's
// value, every other field takes live's, and a field both changed to
// different values is a conflict until picks, keyed "Kind/Name field",
// settles it. A server's base is its stored manifest masked by
// WithoutSecrets, as the stamp keeps it, so live is compared masked the
// same way: a place that still holds a secret reads unchanged, and taking
// live's value where live holds a secret answers LiveSecretError. A
// document that does not read field by field answers UnreadError.
func Merge(kind Kind, object string, base, draft, live Version, picks map[string]Pick) (Merged, error) {
	sides := [3]Version{base, draft, live}
	var sets [3]fieldSet
	var states [3]string
	for i, name := range []string{"base", "draft", "live"} {
		fs, err := fieldsOf(kind, sides[i])
		if err != nil {
			return Merged{}, &UnreadError{Object: object, Side: name, Err: err}
		}
		sets[i], states[i] = fs, stateOf(kind, sides[i].Op)
	}
	fb, fd, fl := sets[0], sets[1], sets[2]
	sb, sd, sl := states[0], states[1], states[2]
	switch {
	case draft.Op == OpRemove && sl == stateAbsent:
		return Merged{Drop: true}, nil
	case draft.Op == OpRemove:
		return Merged{Op: OpRemove}, nil
	case sl == stateAbsent && sb != stateAbsent:
		return mergeGone(object, draft, sb, sd, fb, fd, picks), nil
	}
	flm := fl
	if kind == KindApp && live.Op != OpRemove {
		masked, _ := WithoutSecrets(live.Doc)
		var err error
		if flm, err = appFields(masked); err != nil {
			return Merged{}, &UnreadError{Object: object, Side: "live", Err: err}
		}
	}
	m := Merged{}
	state, c, picked, err := mergeField(object, FieldState, stateField(sb), stateField(sd), stateField(sl), stateField(sl), picks)
	if err != nil {
		return Merged{}, err
	}
	m.Picked += picked
	if c != nil {
		m.Conflicts = append(m.Conflicts, *c)
	}
	out := fieldSet{}
	for _, name := range fieldNames(fb, fd, fl) {
		f, c, picked, err := mergeField(object, name, fb[name], fd[name], fl[name], flm[name], picks)
		if err != nil {
			return Merged{}, err
		}
		m.Picked += picked
		if c != nil {
			m.Conflicts = append(m.Conflicts, *c)
		}
		if f.raw != nil {
			out[name] = f
		}
	}
	if len(m.Conflicts) > 0 {
		return Merged{Conflicts: m.Conflicts, Picked: m.Picked}, nil
	}
	m.Op = opOf(state.key)
	m.Changed = m.Op != draft.Op || !sameFields(out, fd)
	m.Doc = draft.Doc
	if m.Changed {
		doc, err := buildDoc(kind, draft.Doc, out)
		if err != nil {
			return Merged{}, &UnreadError{Object: object, Side: "draft", Err: err}
		}
		m.Doc = doc
	}
	return m, nil
}

// mergeGone merges a put of an object that live state removed since the
// base: an item that changed nothing leaves the draft with it, and a change
// is a conflict on the state field, because a mechanical merge must never
// turn a change into a removal. A pick of the draft keeps the item whole,
// which now creates the object, and a pick of live takes it out.
func mergeGone(object string, draft Version, sb, sd string, fb, fd fieldSet, picks map[string]Pick) Merged {
	if sb == sd && sameFields(fb, fd) {
		return Merged{Drop: true}
	}
	switch picks[object+" "+FieldState] {
	case PickDraft:
		return Merged{Op: draft.Op, Doc: draft.Doc, Picked: 1}
	case PickLive:
		return Merged{Drop: true, Picked: 1}
	}
	return Merged{Conflicts: []Conflict{{Object: object, Field: FieldState, Base: sb, Draft: sd, Live: stateAbsent}}}
}

// mergeField merges one field: b, d and l are its base, draft and live
// values, and lm live's as a draft may read it, masked for a server. It
// answers the value kept, or the conflict when both sides changed it
// apart and no pick settled it, and 1 when a pick did.
func mergeField(object, name string, b, d, l, lm field, picks map[string]Pick) (field, *Conflict, int, error) {
	takeLive := func() (field, *Conflict, int, error) {
		if l.key != lm.key {
			return field{}, nil, 0, &LiveSecretError{Object: object, Field: name}
		}
		return l, nil, 0, nil
	}
	draftChanged, liveChanged := d.key != b.key, lm.key != b.key
	switch {
	case !liveChanged:
		return d, nil, 0, nil
	case !draftChanged:
		return takeLive()
	case d.key == l.key:
		return d, nil, 0, nil
	}
	switch picks[object+" "+name] {
	case PickDraft:
		return d, nil, 1, nil
	case PickLive:
		f, c, _, err := takeLive()
		return f, c, 1, err
	}
	return field{}, &Conflict{Object: object, Field: name, Base: fieldText(b.raw), Draft: fieldText(d.raw), Live: fieldText(l.raw)}, 0, nil
}

// fieldsOf reads the fields of one side of kind, none for an absent object.
func fieldsOf(kind Kind, v Version) (fieldSet, error) {
	if v.Op == OpRemove || v.Op == "" {
		return fieldSet{}, nil
	}
	switch kind {
	case KindApp:
		return appFields(v.Doc)
	case KindRole:
		return roleFields(v.Doc)
	}
	return fieldSet{"text": textField(v.Doc)}, nil
}

// stateOf is the state field's value for an item op of kind.
func stateOf(kind Kind, op Op) string {
	switch {
	case op == OpRemove || op == "":
		return stateAbsent
	case kind != KindPolicySet:
		return statePresent
	case op == OpOff:
		return stateOff
	}
	return stateOn
}

// opOf is the item op a merged state gives.
func opOf(state string) Op {
	if state == stateOff {
		return OpOff
	}
	return OpPut
}

// stateField is a state as a field, whose key is its word.
func stateField(state string) field {
	raw, _ := json.Marshal(state)
	return field{raw: raw, key: state}
}

// textField is a string as a field, unset when empty.
func textField(s string) field {
	if s == "" {
		return field{}
	}
	raw, _ := json.Marshal(s)
	return field{raw: raw, key: string(raw)}
}

// fieldNames lists every field name of the sets, sorted.
func fieldNames(sets ...fieldSet) []string {
	var out []string
	for _, fs := range sets {
		for name := range fs {
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// sameFields reports whether a and b hold the same fields with the same
// values.
func sameFields(a, b fieldSet) bool {
	if len(a) != len(b) {
		return false
	}
	for name, f := range a {
		if g, ok := b[name]; !ok || g.key != f.key {
			return false
		}
	}
	return true
}

// fieldText is a field's value as a conflict shows it: a string as itself,
// any other value as compact JSON, and "" when unset.
func fieldText(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}
	return b.String()
}

// appFields reads a manifest as JSON into its leaves, named as
// manager.ChangedPaths names them: an object is walked key by key, an
// array or a scalar is one leaf, the verbatim server block is one leaf, and
// an empty block keeps its own path. A key that holds a dot or a space
// cannot name a field and refuses the read.
func appFields(doc string) (fieldSet, error) {
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.UseNumber()
	var top map[string]any
	if err := dec.Decode(&top); err != nil {
		return nil, fmt.Errorf("the manifest is not a JSON object: %w", err)
	}
	if top == nil {
		return nil, errors.New("the manifest is not a JSON object")
	}
	out := fieldSet{}
	return out, walkLeaves("", top, out)
}

// walkLeaves adds the leaves of obj under prefix to out.
func walkLeaves(prefix string, obj map[string]any, out fieldSet) error {
	for k, v := range obj {
		if strings.ContainsAny(k, ". ") {
			return fmt.Errorf("the key %q cannot name a field", k)
		}
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok && len(sub) > 0 && path != "server" {
			if err := walkLeaves(path, sub, out); err != nil {
				return err
			}
			continue
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		out[path] = field{raw: raw, key: canonicalKey(v)}
	}
	return nil
}

// buildApp writes the manifest whose leaves are fs as JSON. Names sort a
// block before the leaves under it, and a leaf that is an empty block gives
// way to them. Any other value under a leaf's path refuses the build.
func buildApp(fs fieldSet) (string, error) {
	top := map[string]any{}
	for _, name := range fieldNames(fs) {
		parts := strings.Split(name, ".")
		node := top
		for _, p := range parts[:len(parts)-1] {
			switch next := node[p].(type) {
			case map[string]any:
				node = next
				continue
			case json.RawMessage:
				if string(next) != "{}" {
					return "", fmt.Errorf("the field %s lies under a value", name)
				}
			}
			block := map[string]any{}
			node[p], node = block, block
		}
		node[parts[len(parts)-1]] = fs[name].raw
	}
	raw, err := json.Marshal(top)
	return string(raw), err
}

// AppFieldText answers the text of every field of a server's manifest,
// given as JSON, as a conflict shows it, so a caller can show the values of
// a manifest it masked first.
func AppFieldText(doc string) (map[string]string, error) {
	fs, err := appFields(doc)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(fs))
	for name, f := range fs {
		out[name] = fieldText(f.raw)
	}
	return out, nil
}

// roleFields reads a Role document into its three fields: spec.description,
// spec.bindings and spec.implies, each unset when empty.
func roleFields(doc string) (fieldSet, error) {
	d, err := ParseRole(doc)
	if err != nil {
		return nil, err
	}
	out := fieldSet{}
	if f := textField(d.Spec.Description); f.raw != nil {
		out["spec.description"] = f
	}
	if len(d.Spec.Bindings) > 0 {
		list := make([]map[string]any, len(d.Spec.Bindings))
		for i, b := range d.Spec.Bindings {
			list[i] = map[string]any{"app": b.App, "tools": sortedCopy(b.Tools)}
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i]["app"].(string) < list[j]["app"].(string) })
		raw, _ := json.Marshal(list)
		out["spec.bindings"] = field{raw: raw, key: string(raw)}
	}
	if len(d.Spec.Implies) > 0 {
		raw, _ := json.Marshal(sortedCopy(d.Spec.Implies))
		out["spec.implies"] = field{raw: raw, key: string(raw)}
	}
	return out, nil
}

// buildDoc writes the merged document of kind: a server's leaves as
// manifest JSON, a role's fields over the draft's Role document, and a
// set's text.
func buildDoc(kind Kind, draftDoc string, fs fieldSet) (string, error) {
	switch kind {
	case KindApp:
		return buildApp(fs)
	case KindPolicySet:
		var text string
		if f, ok := fs["text"]; ok {
			if err := json.Unmarshal(f.raw, &text); err != nil {
				return "", err
			}
		}
		return text, nil
	}
	d, err := ParseRole(draftDoc)
	if err != nil {
		return "", err
	}
	d.Spec.Description, d.Spec.Bindings, d.Spec.Implies = "", nil, nil
	if f, ok := fs["spec.description"]; ok {
		if err := json.Unmarshal(f.raw, &d.Spec.Description); err != nil {
			return "", err
		}
	}
	if f, ok := fs["spec.bindings"]; ok {
		var list []struct {
			App   string   `json:"app"`
			Tools []string `json:"tools"`
		}
		if err := json.Unmarshal(f.raw, &list); err != nil {
			return "", err
		}
		for _, b := range list {
			d.Spec.Bindings = append(d.Spec.Bindings, RoleBinding{App: b.App, Tools: b.Tools})
		}
	}
	if f, ok := fs["spec.implies"]; ok {
		if err := json.Unmarshal(f.raw, &d.Spec.Implies); err != nil {
			return "", err
		}
	}
	raw, err := d.Marshal()
	return string(raw), err
}

// canonicalKey writes v in one spelling: object keys sorted, and every
// number as its exact value, so 1.50, 1.5 and 15e-1 read as one. A number
// too long to read cheaply keeps its literal.
func canonicalKey(v any) string {
	var b strings.Builder
	writeKey(&b, v)
	return b.String()
}

func writeKey(b *strings.Builder, v any) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.Quote(k) + ":")
			writeKey(b, t[k])
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			writeKey(b, x)
		}
		b.WriteByte(']')
	case json.Number:
		b.WriteString("#" + numberKey(string(t)))
	case string:
		b.WriteString(strconv.Quote(t))
	default:
		raw, _ := json.Marshal(t)
		b.Write(raw)
	}
}

// numberKey is a JSON number literal as its exact value, or the literal
// itself when it is too long, or its exponent too large, to read cheaply.
func numberKey(n string) string {
	if len(n) > 64 {
		return n
	}
	if i := strings.IndexAny(n, "eE"); i >= 0 {
		if exp, err := strconv.Atoi(n[i+1:]); err != nil || exp > 400 || exp < -400 {
			return n
		}
	}
	r, ok := new(big.Rat).SetString(n)
	if !ok {
		return n
	}
	return r.RatString()
}
