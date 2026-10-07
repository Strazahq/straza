package drafts

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/policy"
)

// The codes of the refusals that reading a bundle or an item meets.
const (
	codeBundleYAML      = "bundle.yaml"
	codeBundleSplit     = "bundle.split"
	codeBundleKind      = "bundle.kind"
	codeBundleUnnamed   = "bundle.unnamed"
	codeBundleDuplicate = "bundle.duplicate"
	codeBundleName      = "bundle.name"
	codeBundleOff       = "bundle.off"
	codeBundleOp        = "bundle.op"
	codeBundleRemoval   = "bundle.removal-doc"
	codeBundleNameChars = "bundle.name-characters"
	codeRoleParse       = "role.parse"
	codeRoleKindMissing = "role.kind-missing"
	codeRoleBindings    = "role.bindings"
	codePolicyParse     = "policy.parse"
	codeRemovalKind     = "removal.kind"
	codeRemovalParse    = "removal.parse"
)

// removalKind is the kind of the document that removes an object.
const removalKind = "Removal"

// docHead is what every document of a bundle says about itself, its kind
// and its name, read before the document's own reader reads it strictly.
type docHead struct{ Kind, Name string }

// The ways a document's kind or name cannot be read as plain text, which
// headOf answers.
var (
	errNoKind     = errors.New("the document names no kind")
	errNoName     = errors.New("the document has no name")
	errHiddenHead = errors.New("a YAML tag, alias or merge key writes the kind or the name")
)

// removalDoc is a Removal document of spec/objects revision 5.
type removalDoc struct {
	APIVersion string      `yaml:"apiVersion"`
	Kind       string      `yaml:"kind"`
	Metadata   RoleDocMeta `yaml:"metadata"`
	Spec       removalSpec `yaml:"spec"`
}

// removalSpec names the kind of the object a Removal removes.
type removalSpec struct {
	Kind string `yaml:"kind"`
}

// ParseBundle reads documents, each a YAML stream of App, Role, PolicySet
// and Removal documents, into items with their canonical documents, and
// answers the bundle refusals. A stream is split at the lines that hold
// exactly ---, and the split must agree with a YAML decoder on the number of
// documents, or the whole stream is refused rather than guessed. Documents
// are numbered from 1 across the whole bundle, and each item and each
// refusal carries its document's number. An App and a PolicySet keep
// their bytes as sent, a Role is read strictly and kept in the export's
// form, and a Removal becomes a remove item. A document that is refused
// becomes no item, and the server reads each App item with the manifest
// parser, which this package does not import.
func ParseBundle(documents []string) ([]Item, []Finding) {
	items, out, _ := readBundle(documents)
	return items, out
}

// Place is where a numbered document of a bundle sits: the index of its
// text among the texts sent, from 0, its place in that text, from 1, or 0
// when that text does not split cleanly, and its kind and name as visible
// text when they read as plain text.
type Place struct {
	Text, Doc  int
	Kind, Name string
}

// Places answers where each document of documents sits, keyed by the
// number ParseBundle gives it and a refusal carries in Document, for a
// client that sent the texts and knows what each one is, such as the file
// it read.
func Places(documents []string) map[int]Place {
	_, _, docs := readBundle(documents)
	out := make(map[int]Place, len(docs))
	for i, d := range docs {
		out[i+1] = placeOf(d)
	}
	return out
}

// bundleDoc is where one numbered document of a bundle sits: the index of
// its text, its place in that text, 0 when the text does not split
// cleanly, and its own text when it has one.
type bundleDoc struct {
	text, doc int
	part      string
}

// readBundle is ParseBundle with where each numbered document sits, the
// document numbered k at index k-1.
func readBundle(documents []string) ([]Item, []Finding, []bundleDoc) {
	var items []Item
	var out []Finding
	var docs []bundleDoc
	n := 0
	for t, text := range documents {
		parts, bad, clean, whole := splitStream(text)
		if len(bad) > 0 {
			for i, part := range parts {
				docs = append(docs, bundleDoc{t, i + 1, part})
				if err, ok := bad[i]; ok {
					out = append(out, numbered(n+i+1, notYAML(n+i+1, "", err.Error()))...)
				}
			}
			n += len(parts)
			continue
		}
		if !clean {
			out = append(out, numbered(n+1, refusal(codeBundleSplit, "",
				fmt.Sprintf("Text %d of the draft does not split cleanly into documents at its lines of ---, so Straza cannot tell where each document starts.", t+1),
				"Put each --- on a line of its own between two documents, or send each document as a text of its own."))...)
			for range max(whole, len(parts)) {
				docs = append(docs, bundleDoc{text: t})
			}
			n += max(whole, len(parts))
			continue
		}
		for i, part := range parts {
			n++
			docs = append(docs, bundleDoc{t, i + 1, part})
			it, fs := readDocument(n, part)
			out = append(out, numbered(n, fs...)...)
			if len(fs) == 0 {
				it.Number = n
				items = append(items, it)
			}
		}
	}
	return items, out, docs
}

// numbered is fs, each finding carrying the document number n.
func numbered(n int, fs ...Finding) []Finding {
	for i := range fs {
		fs[i].Document = n
	}
	return fs
}

// placeOf is the Place of d, with its kind and name when headOf reads them
// as plain text. A document whose kind or name a tag, an alias or a merge
// key writes shows neither, since what it seems to say may not be what
// Straza reads.
func placeOf(d bundleDoc) Place {
	p := Place{Text: d.text, Doc: d.doc}
	if d.doc == 0 {
		return p
	}
	if head, err := headOf(d.part); err == nil || errors.Is(err, errNoName) {
		p.Kind, p.Name = visible(head.Kind), visible(head.Name)
	}
	return p
}

// splitStream cuts text at the lines that hold exactly --- and keeps each
// part's bytes, leaving out the parts that hold no document, such as a
// comment above the first ---. bad maps the index of each part the YAML
// decoder refuses to its error. clean is true when every part holds one
// document and the decoder reads the whole text as that many documents,
// which whole counts.
func splitStream(text string) (parts []string, bad map[int]error, clean bool, whole int) {
	var cur strings.Builder
	var raw []string
	for _, line := range strings.SplitAfter(text, "\n") {
		if strings.TrimRight(line, "\r\n") == "---" {
			raw = append(raw, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteString(line)
	}
	raw = append(raw, cur.String())
	bad = map[int]error{}
	clean = true
	for _, p := range raw {
		count, err := yamlDocuments(p)
		if err == nil && count == 0 {
			continue
		}
		if err != nil {
			bad[len(parts)] = err
		}
		clean = clean && count == 1
		parts = append(parts, p)
	}
	whole, err := yamlDocuments(text)
	return parts, bad, clean && err == nil && whole == len(parts), whole
}

// yamlDocuments counts the documents a YAML decoder reads in text, leaving
// out the empty document it reads after a --- that nothing follows.
func yamlDocuments(text string) (int, error) {
	dec := yaml.NewDecoder(strings.NewReader(text))
	n := 0
	for {
		var node yaml.Node
		err := dec.Decode(&node)
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		if !emptyDocument(&node) {
			n++
		}
	}
}

// emptyDocument reports whether node is a document with nothing written in
// it, which a decoder reads as an implicit null.
func emptyDocument(node *yaml.Node) bool {
	if node.Kind != yaml.DocumentNode || len(node.Content) != 1 {
		return false
	}
	c := node.Content[0]
	return c.Kind == yaml.ScalarNode && c.Tag == "!!null" && c.Value == ""
}

// readDocument reads document n of a bundle, one YAML document, into an
// item, or answers why it cannot. The item is named by the name the strict
// parsers read, the manifest parser's included.
func readDocument(n int, text string) (Item, []Finding) {
	head, err := headOf(text)
	switch {
	case errors.Is(err, errHiddenHead):
		return Item{}, []Finding{hiddenHead(n)}
	case errors.Is(err, errNoKind):
		return Item{}, []Finding{refusal(codeBundleKind, "",
			fmt.Sprintf("Document %d names no kind, and a draft holds App, Role, PolicySet and Removal documents only.", n),
			"Set kind to App, Role, PolicySet or Removal.")}
	}
	switch head.Kind {
	case string(KindApp), string(KindRole), string(KindPolicySet), removalKind:
	default:
		return Item{}, []Finding{refusal(codeBundleKind, "",
			fmt.Sprintf("Document %d has kind %s, and a draft holds App, Role, PolicySet and Removal documents only.", n, visible(head.Kind)),
			"Remove it, or make that change with its own command.")}
	}
	switch {
	case errors.Is(err, errNoName):
		return Item{}, []Finding{unnamed(n)}
	case err != nil:
		return Item{}, []Finding{notYAML(n, "", decodeWords(err))}
	}
	name := head.Name
	switch head.Kind {
	case removalKind:
		return readRemoval(n, text, name)
	case string(KindRole):
		doc, fs := readRole(name, text)
		if len(fs) > 0 {
			return Item{}, fs
		}
		return Item{Kind: KindRole, Name: name, Op: OpPut, Doc: roleText(doc)}, nil
	case string(KindPolicySet):
		if _, fs := readPolicy(Item{Kind: KindPolicySet, Name: name}, text); len(fs) > 0 {
			return Item{}, fs
		}
	}
	return Item{Kind: Kind(head.Kind), Name: name, Op: OpPut, Doc: text}, nil
}

// headOf reads the kind and the metadata.name of one document the way the
// strict parsers decode them, yaml.v3 into a string, once it refused each
// way the decoder reads other text than the document shows: a tag, an
// alias or a merge key on the kind, the metadata, the name or a key beside
// them, or on a Removal's spec.kind. It answers errHiddenHead, errNoKind or
// errNoName, or the decoder's own error, such as a key written twice, and
// the kind as written whenever the kind itself reads.
func headOf(text string) (docHead, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return docHead{}, err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return docHead{}, errNoKind
	}
	root := doc.Content[0]
	kind, meta := valueAt(root, "kind"), valueAt(root, "metadata")
	name := valueAt(meta, "name")
	plain := plainKeys(root) && plainNode(kind) && plainNode(meta) && plainKeys(meta) && plainNode(name)
	if plain && kind != nil && kind.Value == removalKind {
		spec := valueAt(root, "spec")
		plain = plainNode(spec) && plainKeys(spec) && plainNode(valueAt(spec, "kind"))
	}
	switch {
	case !plain:
		return docHead{}, errHiddenHead
	case kind == nil || kind.Kind != yaml.ScalarNode || kind.Tag == "!!null" || kind.Value == "":
		return docHead{}, errNoKind
	case name == nil || name.Kind != yaml.ScalarNode || name.Tag == "!!null" || name.Value == "":
		return docHead{Kind: kind.Value}, errNoName
	}
	var head struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
	}
	if err := doc.Decode(&head); err != nil {
		return docHead{Kind: kind.Value}, err
	}
	return docHead{Kind: head.Kind, Name: head.Metadata.Name}, nil
}

// valueAt answers the value under key in mapping n, or nil.
func valueAt(n *yaml.Node, key string) *yaml.Node {
	for i := 0; n != nil && n.Kind == yaml.MappingNode && i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// plainNode reports whether n, when present, reads as written: it is no
// alias and carries no tag.
func plainNode(n *yaml.Node) bool {
	return n == nil || n.Kind != yaml.AliasNode && n.Style&yaml.TaggedStyle == 0
}

// plainKeys reports whether every key of mapping n, when present, is a
// plain scalar and no merge key, so no alias or merge supplies a field.
func plainKeys(n *yaml.Node) bool {
	for i := 0; n != nil && n.Kind == yaml.MappingNode && i+1 < len(n.Content); i += 2 {
		if k := n.Content[i]; k.Kind != yaml.ScalarNode || !plainNode(k) || k.Value == "<<" {
			return false
		}
	}
	return true
}

// oneDocument refuses item n unless its text is exactly one well-formed
// YAML document. The manifest and policy parsers read only a text's first
// document, so a second one, broken or whole, would be stored and shown
// and never read, which the direct policy route refuses for the same
// reason.
func oneDocument(n int, it Item) []Finding {
	count, err := yamlDocuments(it.Doc)
	switch {
	case err != nil:
		return []Finding{notYAML(n, it.Object(), err.Error())}
	case count != 1:
		return []Finding{refusal(codeBundleSplit, it.Object(), fmt.Sprintf("%s holds %d documents, and an item holds one.", visible(it.Object()), count),
			"Send each document as an item of its own, or send them together as documents.")}
	}
	return nil
}

// notYAML refuses document n, which the YAML decoder refuses with why.
func notYAML(n int, object, why string) Finding {
	return refusal(codeBundleYAML, object, fmt.Sprintf("Document %d is not valid YAML: %s.", n, why), "Fix the document and send the draft again.")
}

// hiddenHead refuses document n, whose kind or name a tag, an alias or a
// merge key writes.
func hiddenHead(n int) Finding {
	return refusal(codeBundleUnnamed, "",
		fmt.Sprintf("Document %d writes its kind or name with a YAML tag, alias or merge key, so the name Straza reads can differ from the text a reviewer reads.", n),
		"Write kind and metadata.name as plain text, and send the draft again.")
}

// readRemoval reads a Removal document named name strictly into a remove
// item.
func readRemoval(n int, text, name string) (Item, []Finding) {
	dec := yaml.NewDecoder(strings.NewReader(text))
	dec.KnownFields(true)
	var doc removalDoc
	why := ""
	switch err := dec.Decode(&doc); {
	case err != nil:
		why = decodeWords(err)
	case doc.APIVersion != RoleAPIVersion:
		why = fmt.Sprintf("apiVersion must be %s, and it is %q", RoleAPIVersion, doc.APIVersion)
	}
	if why != "" {
		return Item{}, []Finding{refusal(codeRemovalParse, "",
			fmt.Sprintf("The Removal of %s, document %d, does not read as a Removal document: %s.", visible(name), n, why),
			"Write apiVersion, kind, metadata.name and spec.kind only, and send the draft again.")}
	}
	switch k := Kind(doc.Spec.Kind); k {
	case KindApp, KindRole, KindPolicySet:
		return Item{Kind: k, Name: name, Op: OpRemove}, nil
	}
	return Item{}, []Finding{refusal(codeRemovalKind, "",
		fmt.Sprintf("The Removal of %s names spec.kind %q.", visible(name), doc.Spec.Kind), "Write App, Role or PolicySet.")}
}

// readRole reads the Role document text of the role name strictly and
// answers the refusals of reading it: a document that does not read, and
// the refusals of roleShape.
func readRole(name, text string) (RoleDoc, []Finding) {
	doc, err := ParseRole(text)
	if err != nil {
		return RoleDoc{}, []Finding{refusal(codeRoleParse, string(KindRole)+"/"+name,
			fmt.Sprintf("Role %s does not read as a Role document: %v.", visible(name), err),
			fmt.Sprintf("Compare it with strazactl roles export %s and send the draft again.", visible(name)))}
	}
	return doc, roleShape(name, doc)
}

// roleShape answers the refusals of the Role document doc of the role
// name, which reads: a missing spec.kind, and more than one binding.
func roleShape(name string, doc RoleDoc) []Finding {
	object := string(KindRole) + "/" + name
	var out []Finding
	if doc.Spec.Kind == "" {
		out = append(out, refusal(codeRoleKindMissing, object,
			fmt.Sprintf("Role %s names no spec.kind, and a draft never picks a kind for you.", visible(name)),
			"Set spec.kind to business, application, approver or straza."))
	}
	if len(doc.Spec.Bindings) > 1 {
		out = append(out, refusal(codeRoleBindings, object,
			fmt.Sprintf("Role %s lists %d bindings, and an application role reaches one MCP server.", visible(name), len(doc.Spec.Bindings)),
			"Make a role for each server and compose them from a business role."))
	}
	return out
}

// readPolicy reads the PolicySet text of it with the policy parser and
// answers the parser's sentence as its refusal.
func readPolicy(it Item, text string) (policy.Document, []Finding) {
	doc, err := policy.Parse([]byte(text))
	if err != nil {
		return policy.Document{}, []Finding{refusal(codePolicyParse, it.Object(), err.Error(), "")}
	}
	return doc, nil
}

// unnamed refuses document n, which names no object.
func unnamed(n int) Finding {
	return refusal(codeBundleUnnamed, "", fmt.Sprintf("Document %d has no name.", n),
		"Name the object in metadata.name and send the draft again.")
}

// refusal is a refused finding.
func refusal(code, object, sentence, fix string) Finding {
	return Finding{Code: code, Class: ClassRefused, Object: object, Sentence: sentence, Fix: fix}
}

// The words ForChange puts in place of a draft's: the step that makes the
// change again, why a change cannot carry a secret, and the fix of a text
// that holds more than one document.
const (
	changeAgain    = "make the change again"
	changeWhy      = "A change cannot carry a secret, because Straza keeps the text of every change in its history, where administrators read it."
	changeSplitFix = "Send each document as a change of its own."
)

// ForChange is f, a refusal intake answers for it, worded for a caller that
// changes that one object with one request and sends no draft, such as a
// direct admin route. Where f names the object's document by its number,
// or starts with the object's key, it names the object as that caller
// knows it, such as the manifest of the server github or the role dev. A
// fix that sends the draft again makes the change again, and the secret
// scan's reason and steps speak of the change where they speak of a draft.
// The size refusal counts the change, and a removal refused for a name
// that looks like a secret says the object stays, which it does, since no
// door changes or removes such an object. Every other word reads as intake
// wrote it, so the agent door and the drafts routes keep theirs.
func (f Finding) ForChange(it Item) Finding {
	word := cmp.Or(secretKindWords[it.Kind], string(it.Kind))
	object := "the " + word + " " + visible(it.Name)
	document := object
	if it.Kind == KindApp {
		document = "the manifest of " + object
	}
	lead := strings.ToUpper(document[:1]) + document[1:]
	switch s := f.Sentence; {
	case f.Code == codeDraftSize:
		if size := len(it.Kind) + len(it.Name) + len(it.Op) + len(it.Doc); size > maxDraftBytes {
			f.Sentence = fmt.Sprintf("The change to %s holds %s, and a change holds at most 1 MiB.", object, bytesWords(size))
			f.Fix = "Make " + document + " smaller and " + changeAgain + "."
		}
	case it.Op == OpRemove && strings.HasPrefix(f.Code, "secret."):
		// The scan withholds the name, so the sentence keeps its words for
		// the name and says which object it is by the request alone.
		f.Sentence = strings.Replace(s, "The name of a "+word+" in this draft ", "The name of the "+word+" you asked to remove ", 1)
		f.Fix = fmt.Sprintf("Straza does not change or remove a %s whose name looks like a secret, so this one stays. Treat the secret in its name as exposed and rotate it.", word)
	case f.Code == codeBundleSplit:
		// The draft door's words speak of items, so the sentence is made
		// again from the document count.
		if count, err := yamlDocuments(it.Doc); err == nil {
			f.Sentence, f.Fix = fmt.Sprintf("%s holds %d documents, and a change takes one.", lead, count), changeSplitFix
		}
	case strings.HasPrefix(s, "Document 1 "):
		f.Sentence = lead + strings.TrimPrefix(s, "Document 1")
	case strings.HasPrefix(s, visible(it.Object())+" "):
		f.Sentence = lead + strings.TrimPrefix(s, visible(it.Object()))
	}
	f.Sentence = strings.Replace(strings.Replace(f.Sentence, "document 1 ", object+" ", 1), scanWhy, changeWhy, 1)
	f.Fix = strings.Replace(strings.Replace(f.Fix, "send the draft again", changeAgain, 1), "publish the draft", "make the change", 1)
	return f
}
