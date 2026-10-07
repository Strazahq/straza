package server

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/redact"
)

// answerDoc is the document of an object of kind as a route answers it: a
// server's masked, and every other kind's as it is.
func answerDoc(kind, doc string) string {
	if kind == string(drafts.KindApp) && doc != "" {
		return maskedManifest(doc)
	}
	return doc
}

// maskedManifest renders an App document as every route answers it:
// the manifest read from doc, a stored JSON manifest or the app.yaml of an
// item, with every env value of a command or a container replaced by
// redact.Mark, every address in any field masked by maskURL, and every
// value and default of the verbatim server block replaced by redact.Mark,
// in the app.yaml form strazactl apps export prints, and then every other place
// the draft secret scan flags masked by drafts.WithoutSecrets, such as an
// argument, a description or the capability in an address path. A document
// that does not read answers "", so no route answers a manifest unmasked.
// Intake refuses every one of these masks when a document comes back
// (bundle.masked).
func maskedManifest(doc string) string {
	var mf manager.Manifest
	var err error
	if json.Valid([]byte(doc)) {
		mf, err = manager.FromJSON(doc)
	} else {
		mf, err = manager.Parse([]byte(doc))
	}
	if err != nil {
		return ""
	}
	rt := &mf.Straza.Runtime
	if rt.Command != nil {
		maskEnv(rt.Command.Env)
	}
	if rt.OCI != nil {
		maskEnv(rt.OCI.Env)
	}
	if rt.Remote != nil && rt.Remote.URL != "" {
		rt.Remote.URL = maskURL(rt.Remote.URL)
	}
	maskServerBlock("", mf.Server)
	out, err := yaml.Marshal(mf)
	if err != nil {
		return ""
	}
	rendered := maskAddresses(string(out))
	if rendered == "" {
		return ""
	}
	// The scan runs on the rendered form, after maskURL, so an address keeps
	// the marked user name and the hidden query, and no mask reaches
	// the parse above, where a masked inject template would fail validation.
	masked, _ := drafts.WithoutSecrets(rendered)
	return masked
}

// maskAddresses answers the YAML document doc with every address in every
// scalar, a key or a value, written as maskURL writes it, so an address in
// an argument, a description or any field keeps no user information, query
// or fragment, and doc as it was when no address changed. A document that
// does not read, or cannot be written again, answers "".
func maskAddresses(doc string) string {
	var root yaml.Node
	if yaml.Unmarshal([]byte(doc), &root) != nil {
		return ""
	}
	changed := false
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode {
			if v := redact.URLPattern.ReplaceAllStringFunc(n.Value, maskURL); v != n.Value {
				n.Value, changed = v, true
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(&root)
	if !changed {
		return doc
	}
	out, err := yaml.Marshal(&root)
	if err != nil {
		return ""
	}
	return string(out)
}

// maskURL is the address s as every route answers it: as written when it
// carries no user information, query or fragment, and otherwise with the
// user information marked by redact.Mark as the user name, so a reader
// sees the address had some, and the query and the fragment written as
// redact.URL writes them, "?…" and "#…". Intake refuses such an address
// sent back (bundle.masked). A capability in the path is left to the draft
// secret scan, which masks it wherever the address stands, and an address
// that does not parse answers redact.URL's placeholder.
func maskURL(s string) string {
	u, err := url.Parse(s)
	switch {
	case err != nil:
		return redact.URL(s)
	case u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "":
		return s
	}
	if u.User != nil {
		u.User = url.User(redact.Mark)
	}
	tail := ""
	if u.RawQuery != "" || u.ForceQuery {
		u.RawQuery, u.ForceQuery, tail = "", false, "?\u2026"
	}
	if u.Fragment != "" {
		u.Fragment, u.RawFragment, tail = "", "", tail+"#\u2026"
	}
	return u.String() + tail
}

// maskEnv replaces every value of env that is set with redact.Mark.
func maskEnv(env []manager.EnvVar) {
	for i := range env {
		if env[i].Value != "" {
			env[i].Value = redact.Mark
		}
	}
}

// maskServerBlock masks, in place, the value v of the verbatim server block
// found under key: url strings through maskURL, and every value and
// default that is set, a string, a number or a boolean, to redact.Mark. A
// list's items take the key of the list.
func maskServerBlock(key string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = maskServerBlock(k, x)
		}
	case []any:
		for i, x := range t {
			t[i] = maskServerBlock(key, x)
		}
	case string:
		switch {
		case key == "url":
			return maskURL(t)
		case (key == "value" || key == "default") && t != "":
			return redact.Mark
		}
	case nil:
	default:
		// A number or a boolean may be a secret too, such as a PIN.
		if key == "value" || key == "default" {
			return redact.Mark
		}
	}
	return v
}

// maskedValue reports whether the scalar v is what a route answers in
// place of a value it does not show, as intake reads it for bundle.masked:
// redact.Mark or redact.URL's placeholder whole, or an address that ends
// in the query or the fragment mark redact.URL leaves, names redact.Mark
// as its user, plain or percent-encoded, or holds it as a segment of its
// path or as a query value.
func maskedValue(v string) bool {
	if v == redact.Mark || v == "<unparseable url>" {
		return true
	}
	// The addresses are found as the draft secret scan finds them, so a
	// mask inside an address is read where the scan put it.
	for _, address := range redact.URLPattern.FindAllString(v, -1) {
		if strings.HasSuffix(address, "?…") || strings.HasSuffix(address, "#…") {
			return true
		}
		u, err := url.Parse(address)
		if err != nil {
			continue
		}
		if (u.User != nil && u.User.Username() == redact.Mark) || slices.Contains(strings.Split(u.Path, "/"), redact.Mark) {
			return true
		}
		for _, values := range u.Query() {
			if slices.Contains(values, redact.Mark) {
				return true
			}
		}
	}
	return false
}

// scalarAt is one scalar of a document: its node, its path as intake names
// a place, its place as keptMasks matches one, and whether it is a
// mapping key.
type scalarAt struct {
	node        *yaml.Node
	path, place string
	key         bool
}

// scalarsOf lists the scalars of the document under n in document order,
// each with its path, mapping keys joined by dots and list items by index,
// and its place: the same path, except that an item of an env or header
// list is under the entry's name and an argument under the argument before
// it, so an entry added before a value or an argument put in front of a
// flag leaves the value at its place. A list's items take the key of the
// list. An alias is not followed, because the text it names is read where
// its anchor is defined.
func scalarsOf(n *yaml.Node, path, place, key string) []scalarAt {
	var out []scalarAt
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			out = append(out, scalarsOf(c, path, place, key)...)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			childPath, childPlace := k.Value, k.Value
			if path != "" {
				childPath, childPlace = path+"."+k.Value, place+"."+k.Value
			}
			if k.Kind == yaml.ScalarNode {
				out = append(out, scalarAt{node: k, path: childPath, place: childPlace, key: true})
			}
			out = append(out, scalarsOf(v, childPath, childPlace, k.Value)...)
		}
	case yaml.SequenceNode:
		kind := listKinds[key]
		for i, c := range n.Content {
			itemPlace := fmt.Sprintf("%s[%d]", place, i)
			switch {
			case (kind == "env" || kind == "header") && c.Kind == yaml.MappingNode:
				if name := fieldOf(c, "name"); name != "" {
					itemPlace = place + "[" + name + "]"
				}
			case kind == "arg" && i > 0 && n.Content[i-1].Kind == yaml.ScalarNode:
				itemPlace = place + "[after " + n.Content[i-1].Value + "]"
			}
			out = append(out, scalarsOf(c, fmt.Sprintf("%s[%d]", path, i), itemPlace, key)...)
		}
	case yaml.ScalarNode:
		out = append(out, scalarAt{node: n, path: path, place: place})
	}
	return out
}

// listKinds maps the keys whose lists carry values into a server's
// environment, its request headers or its command line, in the straza block
// and in the registry record the server block copies, to the kind of list,
// as the draft secret scan reads them.
var listKinds = map[string]string{
	"env": "env", "environmentVariables": "env",
	"headers": "header",
	"args":    "arg", "packageArguments": "arg", "runtimeArguments": "arg",
}

// fieldOf answers the text of the scalar under key in the mapping n, or "".
func fieldOf(n *yaml.Node, key string) string {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if v := n.Content[i+1]; n.Content[i].Value == key && v.Kind == yaml.ScalarNode {
			return v.Value
		}
	}
	return ""
}

// livePlace is what the live manifest holds at one place: its value as a
// route answers it, its value as the stamp keeps it (drafts.WithoutSecrets),
// its stored value with the tag and style it is written in, so a restored
// number or boolean stays one, and how many scalars the place holds.
type livePlace struct {
	masked, stamped, plain string
	tag                    string
	style                  yaml.Style
	n                      int
}

// livePlaces answers, by place, what live, a stored manifest, holds: the
// masked rendering every route answers and the stored text read side by
// side, scalar by scalar, since both are the one manifest written the same
// way, and then the stamp's form, which an undo sends back, at every place
// the rendering holds. It answers nil for a manifest that does not read.
func livePlaces(live string) map[string]*livePlace {
	mf, err := manager.FromJSON(live)
	if err != nil {
		return nil
	}
	plain, err := yaml.Marshal(mf)
	if err != nil {
		return nil
	}
	plainAt := map[string]*yaml.Node{}
	for _, s := range scalarsOf(parsed(string(plain)), "", "", "") {
		if !s.key {
			plainAt[s.path] = s.node
		}
	}
	out := map[string]*livePlace{}
	for _, s := range scalarsOf(parsed(maskedManifest(live)), "", "", "") {
		p, ok := plainAt[s.path]
		if s.key || !ok {
			continue
		}
		lp := out[s.place]
		if lp == nil {
			lp = &livePlace{masked: s.node.Value, plain: p.Value, tag: p.ShortTag(), style: p.Style}
			out[s.place] = lp
		}
		lp.n++
	}
	stamped, _ := drafts.WithoutSecrets(live)
	for _, s := range scalarsOf(parsed(stamped), "", "", "") {
		if lp := out[s.place]; !s.key && lp != nil {
			lp.stamped = s.node.Value
		}
	}
	return out
}

// parsed is the node tree of doc, an empty node for text that does not
// read as YAML.
func parsed(doc string) *yaml.Node {
	var root yaml.Node
	if yaml.Unmarshal([]byte(doc), &root) != nil {
		return &yaml.Node{}
	}
	return &root
}

// anchored reports whether the document under n holds an anchor, an alias
// or a merge key, through which a value restored at one place would be
// read at another, where no route masks it.
func anchored(n *yaml.Node) bool {
	if n.Anchor != "" || n.Kind == yaml.AliasNode || n.Tag == "!!merge" {
		return true
	}
	for _, c := range n.Content {
		if anchored(c) {
			return true
		}
	}
	return false
}

// holdsAnchor reports anchored over the document doc.
func holdsAnchor(doc string) bool {
	return anchored(parsed(doc))
}

// keptMasks answers doc, an App document, with every mask in it replaced
// by the value that live, the server's stored manifest, holds at the same
// place, when live as every route answers it, or as the stamp keeps it,
// masks a value at every such place with the same mask: the mask then
// stands for the stored value, and a document read from a route and sent
// back changes nothing there. Beside the text it answers the path of the
// document's first mask, as intake names it. It answers "" and the path of
// the first mask that does not fit when live is no manifest, holds no such
// mask at that place, which is a changed or a new place, when the place
// holds two values in either document, or when the mask is a mapping key,
// and "" with the first mask's path when the document holds an anchor, an
// alias or a merge key, which could copy a restored value to another place
// (anchored). A document with no mask comes back as it is, byte for byte,
// and one that does not read as YAML is left to the manifest parser.
func keptMasks(doc, live string) (restored, at string) {
	var root yaml.Node
	if yaml.Unmarshal([]byte(doc), &root) != nil {
		return doc, ""
	}
	var masks []scalarAt
	places := map[string]int{}
	for _, s := range scalarsOf(&root, "", "", "") {
		if !s.key {
			places[s.place]++
		}
		if maskedValue(s.node.Value) {
			masks = append(masks, s)
		}
	}
	if len(masks) == 0 {
		return doc, ""
	}
	if anchored(&root) {
		return "", masks[0].path
	}
	kept := livePlaces(live)
	for _, m := range masks {
		lp := kept[m.place]
		if m.key || lp == nil || lp.n != 1 || places[m.place] != 1 || !lp.masks(m.node.Value) {
			return "", m.path
		}
		m.node.Value = lp.plain
		if lp.tag != "!!str" {
			m.node.Tag, m.node.Style = lp.tag, lp.style
		}
	}
	out, err := yaml.Marshal(&root)
	if err != nil {
		return "", masks[0].path
	}
	return string(out), masks[0].path
}

// masks reports whether v is the mask the live place carries for its
// stored value, in a route's answer or in the stamp's form. A place whose
// stored value is the mark itself masks nothing, so the mark never comes
// back as a value.
func (lp *livePlace) masks(v string) bool {
	return v == lp.masked && lp.masked != lp.plain || v == lp.stamped && lp.stamped != lp.plain
}
