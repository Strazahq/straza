package drafts

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/redact"
)

// The codes of the secret findings. Every one is a refusal except
// codeSecretEntropy, which is a warning, and codeSecretMore, which counts the
// places a capped answer leaves out and is a refusal when any of them is.
const (
	codeSecretShape    = "secret.shape"
	codeSecretUserinfo = "secret.userinfo"
	codeSecretQuery    = "secret.query"
	codeSecretPath     = "secret.path"
	codeSecretValue    = "secret.value"
	codeSecretTemplate = "secret.template"
	codeSecretEntropy  = "secret.entropy"
	codeSecretMore     = "secret.more"
)

// secretMaxFindings caps the places one item's answer names, so a document
// full of tokens still gets an answer a person can read.
const secretMaxFindings = 20

// secretLists maps the keys whose lists carry values into a server's
// environment, its request headers or its command line, in the straza block
// and in the registry record the server block copies, to the kind of list.
var secretLists = map[string]string{
	"env": "env", "environmentVariables": "env",
	"headers": "header",
	"args":    "arg", "packageArguments": "arg", "runtimeArguments": "arg",
}

// injectTemplatePath is the injection template, the one field where the
// secret's own placeholder belongs.
const injectTemplatePath = "straza.credential.inject.template"

// secretKindWords names each kind the way the console does.
var secretKindWords = map[Kind]string{KindApp: "server", KindRole: "role", KindPolicySet: "policy set"}

// scanWhy is why no finding's value may stay in a draft.
const scanWhy = "A draft cannot carry a secret, because every reviewer reads the draft and its history keeps it."

// ScanSecrets returns the secret findings of one draft item: a refusal for
// each place that holds a secret or the shape of one, and a warning for a
// long random-looking value in an env entry, an argument or a header. It
// reads only the item and contacts nothing, so intake runs it on every door
// before a draft is stored or audited. A finding names the field path or the
// line, never the value, and withholds a name that holds a secret. An App
// document is read as YAML or JSON, keys and comments included, with the
// manifest rules on its lists and template. A Role is read the same way
// without them. A PolicySet is read as raw text, comments included, because
// the snapshot serves it without a sign-in. Text a document does not decode,
// or a decoder keeps nowhere, is read raw as well. An item with no document,
// a removal, is read by its name, which is refused when it holds a secret.
func ScanSecrets(it Item) []Finding {
	s, set, word := itemScan(it)
	if strings.TrimSpace(it.Doc) == "" {
		// A removal carries no document, so the draft keeps nothing of the
		// object but its name, and a name that holds a secret refuses here.
		var out []Finding
		for _, r := range secretTextRules(it.Name, set) {
			out = append(out, Finding{Code: r.code, Class: ClassRefused, Object: s.object,
				Sentence: fmt.Sprintf("The name of a %s in this draft %s. %s", word, r.what, scanWhy),
				Fix:      "Leave that item out of the draft. If a live object carries the name, treat the secret in it as exposed and rotate it."})
		}
		return out
	}
	s.read(set)
	return s.findings()
}

// itemScan is the scan of it before any text is read: its words, the rule
// set its kind gets, and the word for its kind.
func itemScan(it Item) (*secretScan, secretRuleSet, string) {
	word := cmp.Or(secretKindWords[it.Kind], string(it.Kind))
	set := secretShapesOnly
	if it.Kind == KindApp || it.Kind == KindRole {
		set = secretAddresses
	}
	s := &secretScan{doc: it.Doc, object: it.Object(), noun: "the " + word + " " + it.Name, why: scanWhy, seen: map[string]bool{}, app: it.Kind == KindApp}
	name := it.Name
	// A name that holds a secret would travel in every finding, so it is
	// withheld. The document's own copy of the name is what refuses it.
	if NameWithheld(it) {
		name = "NAME"
		s.object, s.noun = string(it.Kind)+"/(name withheld)", "a "+word+" whose name looks like a secret"
	}
	switch it.Kind {
	case KindApp:
		s.server = name
		s.tail = " If the server needs the secret, publish the draft without it, then store it with strazactl apps secret set " + name +
			", which asks for the value at a hidden prompt, and set credential.inject so Straza adds it for you."
	case KindRole:
		s.tail = " A role never needs a secret."
	case KindPolicySet:
		s.why += " Once published, its text, comments included, is served to anyone who asks, without a sign-in."
		s.tail = " A policy set never needs a secret, in a rule or in a comment."
	}
	return s, set, word
}

// NameWithheld reports whether the scan withholds the name of it from every
// finding, because the name holds a credential shape. A door keeps such an
// item away from the manifest parser and the check, whose words quote the
// name.
func NameWithheld(it Item) bool {
	set := secretShapesOnly
	if it.Kind == KindApp || it.Kind == KindRole {
		set = secretAddresses
	}
	return len(secretTextRules(it.Name, set)) > 0
}

// read reads the document with the rules of set. A YAML decoder keeps no
// node for some text, such as a document that holds only comments, so a
// document that raised no refusal is also read raw. The draft is refused
// either way when any text holds a shape.
func (s *secretScan) read(set secretRuleSet) {
	if set != secretAddresses || !s.document() || !s.anyRefused {
		s.raw(set)
	}
}

// ScanNote returns the secret findings of a draft's note, the proposer's own
// words, which every reviewer is shown and the draft stores. It reads the
// note line by line for the credential shapes and for an address that
// carries a password, and contacts nothing. Each finding's Object is Note.
func ScanNote(note string) []Finding {
	s := &secretScan{doc: note, object: "Note", noun: "the draft's note", why: scanWhy, seen: map[string]bool{},
		tail: " If a server needs the secret, a person stores it after publish with strazactl apps secret set and the server's name, which asks for the value at a hidden prompt."}
	s.raw(secretPasswords)
	return s.findings()
}

// secretScan collects the findings of one item or note. Each hit keeps the
// place it names, which keys the dedupe and never carries a value. flagged
// holds the string node behind every hit that has one, for WithoutSecrets,
// and whole the nodes a rule other than an address rule flagged.
// envTails holds, by place, the fix's tail for an env value of the server's
// runtime, which a stored secret reaches through credential.inject. places
// holds, when the scan is asked to keep them, the values of the argument
// and env places, by place, for Waive to compare with a stored manifest.
type secretScan struct {
	doc          string
	object, noun string
	why, tail    string
	server       string
	app          bool
	anyRefused   bool
	hits         []secretHit
	seen         map[string]bool
	commentLines map[string]int
	flagged      []*yaml.Node
	whole        map[*yaml.Node]bool
	envTails     map[string]string
	places       map[string][]string
}

// secretHit is one finding at the place where and what its rule found
// there, in the rule's words.
type secretHit struct {
	where, what string
	f           Finding
}

// secretRule is one rule's reading of a string: the finding code, what the
// rule found in words, and the first step of the fix.
type secretRule struct{ code, what, step string }

// document walks every YAML document in the item. It reports false when the
// text does not decode, and nothing is walked then.
func (s *secretScan) document() bool {
	var docs []*yaml.Node
	for dec := yaml.NewDecoder(strings.NewReader(s.doc)); ; {
		n := new(yaml.Node)
		if err := dec.Decode(n); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return false
		}
		docs = append(docs, n)
	}
	for _, n := range docs {
		s.node(n, "", "")
	}
	return true
}

// node walks n, found at path under key. Every comment, key and scalar is
// read with the text rules. An alias is read where its anchor is defined,
// so a document cannot multiply the work by repeating one.
func (s *secretScan) node(n *yaml.Node, path, key string) {
	s.comments(n)
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			s.node(c, path, key)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			s.node(c, secretItemPath(path, key, i, c), "")
		}
	case yaml.MappingNode:
		where, prefix := "a top-level key", ""
		if path != "" {
			where, prefix = "a key under "+path, path+"."
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			child := prefix + secretSegment(k)
			if k.Kind == yaml.ScalarNode {
				s.comments(k)
				s.text(where, k)
			} else {
				s.node(k, path, "")
			}
			if s.app {
				s.lists(k.Value, v, child)
			}
			s.node(v, child, k.Value)
		}
	case yaml.ScalarNode:
		if s.app && path == injectTemplatePath {
			s.template(n)
		} else {
			s.text(path, n)
		}
	}
}

// comments reads each line of the comments YAML attached to n. A line
// comment sits on the node's own line. A head or foot comment fills lines of
// its own, which an index of the document's comment lines finds, built once.
func (s *secretScan) comments(n *yaml.Node) {
	for i, c := range []string{n.LineComment, n.HeadComment, n.FootComment} {
		for _, line := range strings.Split(c, "\n") {
			line = strings.TrimSpace(line)
			rules := secretTextRules(line, secretAddresses)
			if len(rules) == 0 {
				continue
			}
			if s.commentLines == nil {
				s.commentLines = map[string]int{}
				for no, raw := range strings.Split(s.doc, "\n") {
					if raw = strings.TrimSpace(raw); strings.HasPrefix(raw, "#") && s.commentLines[raw] == 0 {
						s.commentLines[raw] = no + 1
					}
				}
			}
			no := n.Line
			if i > 0 {
				no = s.commentLines[line]
			}
			where := "a comment"
			if no > 0 {
				where = fmt.Sprintf("a comment on line %d", no)
			}
			for _, r := range rules {
				s.add(where, nil, r)
			}
		}
	}
}

// text reads the string node n of a manifest or a role, found at where.
func (s *secretScan) text(where string, n *yaml.Node) {
	for _, r := range secretTextRules(n.Value, secretAddresses) {
		s.add(where, n, r)
	}
}

// raw reads the text as it is, line by line, with the rules of set.
func (s *secretScan) raw(set secretRuleSet) {
	for i, line := range strings.Split(s.doc, "\n") {
		for _, r := range secretTextRules(line, set) {
			s.add(fmt.Sprintf("line %d", i+1), nil, r)
		}
	}
}

// lists reads the values of an env, header or argument list at path, or of
// an env or header mapping from name to value. An argument that follows a
// flag naming a secret, such as the one after --password, is read as a
// value under that name. An entry written as an alias or a merge key is not
// followed, which keeps the walk linear on any input, and the text it names
// is read where its anchor is defined.
func (s *secretScan) lists(key string, v *yaml.Node, path string) {
	runtime := path == "straza.runtime.command.env" || path == "straza.runtime.oci.env"
	kind := secretLists[key]
	switch {
	case kind == "":
	case v.Kind == yaml.MappingNode:
		for i := 0; i+1 < len(v.Content); i += 2 {
			k := v.Content[i]
			where := path + "." + secretSegment(k)
			if runtime {
				s.envTail(where, k.Value)
			}
			s.place(kind, where, k.Value, v.Content[i+1])
			s.value(where, secretEntryNamed(kind, k.Value), false, v.Content[i+1])
		}
	case v.Kind == yaml.SequenceNode:
		for i, item := range v.Content {
			p := secretItemPath(path, key, i, item)
			if item.Kind == yaml.ScalarNode {
				flag := ""
				if i > 0 && v.Content[i-1].Kind == yaml.ScalarNode {
					flag = v.Content[i-1].Value
				}
				s.place(kind, p, flag, item)
				s.value(p, false, false, item)
				if kind == "arg" && i > 0 && secretFlagValue(v.Content[i-1], item) {
					s.add(p, item, secretRule{codeSecretValue, secretAfterWord.what, secretArgStep})
				}
				continue
			}
			_, name := secretField(item, "name")
			_, marked := secretField(item, "isSecret")
			if runtime {
				s.envTail(p+".value", name)
			}
			for _, f := range []string{"value", "default"} {
				val, _ := secretField(item, f)
				s.place(kind, p+"."+f, name, val)
				s.value(p+"."+f, secretEntryNamed(kind, name), strings.EqualFold(marked, "true"), val)
			}
		}
	}
}

// place keeps the value of the scalar v at where when the scan keeps
// places and where is an argument or an env value, together with name, the
// argument before a positional argument, the entry's name or the mapping
// key, so the same value after another flag reads as another place. A
// place written twice keeps both values, so Waive never reads one value
// for a place that holds two.
func (s *secretScan) place(kind, where, name string, v *yaml.Node) {
	if s.places != nil && (kind == "arg" || kind == "env") && v != nil && v.Kind == yaml.ScalarNode {
		s.places[where] = append(s.places[where], name+"\x00"+v.Value)
	}
}

// secretPlaces answers the values of every argument and env place of the
// App document doc, by place, as place keeps them. A document that does
// not decode has none.
func secretPlaces(doc string) map[string][]string {
	s := &secretScan{doc: doc, app: true, seen: map[string]bool{}, places: map[string][]string{}}
	s.document()
	return s.places
}

// secretEntryNamed reports whether the name of an entry of a list of kind
// marks its value as a secret: a secret's name, or, for a header, Cookie,
// whose value carries a session.
func secretEntryNamed(kind, name string) bool {
	return secretNamed(name) || (kind == "header" && strings.EqualFold(name, "cookie"))
}

// secretArgStep is the first step of the fix of a secret in an argument:
// the flag stays behind otherwise, and takes the next argument as its value.
const secretArgStep = "Remove the flag and its value."

// secretFlagValue reports whether arg, the argument after flag, is a value
// under the name of a secret: flag is a flag whose words name one and that
// carries no value of its own, and arg holds a value that is not benign. An
// argument that starts with a dash is the next flag unless secretDashValue
// reads it as a secret, so a switch such as --no-auth takes no value.
func secretFlagValue(flag, arg *yaml.Node) bool {
	held, set := secretHeld(arg)
	return flag.Kind == yaml.ScalarNode && strings.HasPrefix(flag.Value, "-") && !strings.Contains(flag.Value, "=") && secretNamed(flag.Value) &&
		set && (!strings.HasPrefix(held, "-") || secretDashValue(held)) && !secretBenign(held)
}

// secretHeld answers the text of the scalar v and whether it holds a value:
// it is set, not null, no reference to a secret kept elsewhere, and not,
// whole and untrimmed, the mark a route answers in place of a value it
// does not show, which intake refuses as a mask (bundle.masked) and the
// check waives for the stored value. The mark with anything around it is
// a value.
func secretHeld(v *yaml.Node) (string, bool) {
	held := strings.TrimSpace(v.Value)
	return held, held != "" && v.Value != redact.Mark && v.ShortTag() != "!!null" && !secretRefRe.MatchString(held)
}

// envTail records the fix's tail for the env value at where, the value of
// the runtime's env entry name: the manifest injects a stored secret into
// the environment itself, so no placeholder is offered, because the runtime
// would hand the server a placeholder as written. A name that does not print
// is not named.
func (s *secretScan) envTail(where, name string) {
	as := "the entry's name as name"
	if secretPrintable(name) {
		as = "name: " + name
	}
	if s.envTails == nil {
		s.envTails = map[string]string{}
	}
	s.envTails[where] = " If the server needs the secret, set credential.kind: static and credential.inject with as: env, " + as +
		" and template: {{secret}}, publish the draft, then store the secret with strazactl apps secret set " + s.server + ", which asks for the value at a hidden prompt."
}

// value reads one value of a list at where. A value under a secret's name
// must be benign, and one in an entry marked isSecret must be empty or a
// reference, because the entry says it is a secret. A value that holds a
// value after a word naming a secret is refused as well (secretInValue). A
// long random-looking value draws a warning.
func (s *secretScan) value(where string, named, marked bool, v *yaml.Node) {
	if v == nil || v.Kind != yaml.ScalarNode {
		return
	}
	held, set := secretHeld(v)
	what := ""
	switch {
	case marked && set:
		what = "holds a value, and its entry is marked isSecret"
	case named && set && !secretBenign(held):
		what = "holds a value, and its name marks it as a secret"
	case set && secretInValue(held):
		what = secretAfterWord.what
	}
	if what != "" {
		// A {placeholder} is how a registry record names a value a client
		// fills in. The runtime hands the straza block's values to the server
		// as written, so a placeholder there would reach it as text. Past the
		// runtime env, the straza block's list values are arguments.
		step := "Remove the value, or write a {placeholder} in its place."
		if _, runtime := s.envTails[where]; runtime {
			step = "Remove the entry."
		} else if strings.HasPrefix(where, "straza.") {
			step = secretArgStep
		}
		s.add(where, v, secretRule{codeSecretValue, what, step})
	}
	if secretLooksGenerated(v.Value) {
		s.add(where, v, secretRule{codeSecretEntropy, "holds a long random-looking string, which may be a secret", "If it is a secret, remove it."})
	}
}

// template reads the injection template n. The text around {{secret}}
// goes out with every call as written, so a shape there is a secret in the
// open.
func (s *secretScan) template(n *yaml.Node) {
	fixed := strings.ReplaceAll(n.Value, "{{secret}}", "")
	if labels := secretShapeLabels(fixed); labels != "" {
		s.add(injectTemplatePath, n, secretRule{codeSecretTemplate, "holds what looks like " + labels + " outside {{secret}}", "Keep only fixed words around {{secret}}, such as Bearer {{secret}}."})
	}
	if secretLooksGenerated(fixed) {
		s.add(injectTemplatePath, n, secretRule{codeSecretEntropy, "holds a long random-looking string outside {{secret}}, which may be a secret", "If it is a secret, remove it."})
	}
}

// add records the finding of rule r at where, once per place and rule, and
// the string node n it read, when it read one.
func (s *secretScan) add(where string, n *yaml.Node, r secretRule) {
	if n != nil {
		s.flagged = append(s.flagged, n)
		if r.code != codeSecretUserinfo && r.code != codeSecretPath && r.code != codeSecretQuery {
			if s.whole == nil {
				s.whole = map[*yaml.Node]bool{}
			}
			s.whole[n] = true
		}
	}
	if key := r.code + "\n" + where + "\n" + r.what; !s.seen[key] {
		s.seen[key] = true
		tail, runtime := s.envTails[where]
		if !runtime {
			tail = s.tail
		}
		class, fix := ClassRefused, r.step+tail
		if r.code == codeSecretEntropy {
			class, fix = ClassWarning, fix+" If it is not a secret, nothing needs to change."
		}
		s.anyRefused = s.anyRefused || class == ClassRefused
		s.hits = append(s.hits, secretHit{where: where, what: r.what, f: Finding{Code: r.code, Class: class, Object: s.object,
			Sentence: fmt.Sprintf("In %s, %s %s. %s", s.noun, where, r.what, s.why), Fix: fix}})
	}
}

// kept returns the hits in document order that the answer keeps. A place
// that holds a shape needs no second line saying its value sits under a
// secret's name, and a place with a refusal needs no warning.
func (s *secretScan) kept() []secretHit {
	refusedAt, shapedAt := map[string]bool{}, map[string]bool{}
	for _, h := range s.hits {
		refusedAt[h.where] = refusedAt[h.where] || h.f.Class == ClassRefused
		shapedAt[h.where] = shapedAt[h.where] || h.f.Code == codeSecretShape || h.f.Code == codeSecretTemplate
	}
	var out []secretHit
	for _, h := range s.hits {
		redundant := (h.f.Class == ClassWarning && refusedAt[h.where]) || (h.f.Code == codeSecretValue && shapedAt[h.where])
		if !redundant {
			out = append(out, h)
		}
	}
	return out
}

// findings returns the findings of the hits kept. Past secretMaxFindings
// places, one last finding counts the rest, and it is a refusal when any
// of them is.
func (s *secretScan) findings() []Finding {
	var out []Finding
	for _, h := range s.kept() {
		out = append(out, h.f)
	}
	if len(out) <= secretMaxFindings {
		return out
	}
	cut := out[secretMaxFindings:]
	class, what := ClassWarning, "a long random-looking string, which may be a secret"
	if slices.ContainsFunc(cut, func(f Finding) bool { return f.Class == ClassRefused }) {
		class, what = ClassRefused, "what looks like a secret"
	}
	places := fmt.Sprintf("%d more places hold", len(cut))
	if len(cut) == 1 {
		places = "1 more place holds"
	}
	return append(out[:secretMaxFindings:secretMaxFindings], Finding{Code: codeSecretMore, Class: class, Object: s.object,
		Sentence: fmt.Sprintf("In %s, %s %s. %s", s.noun, places, what, s.why),
		Fix:      "Fix the places above, then save the draft again, and the check names the rest."})
}

// secretSegment is the path segment of a mapping key.
func secretSegment(k *yaml.Node) string {
	if k.Kind == yaml.ScalarNode && secretPrintable(k.Value) {
		return k.Value
	}
	return "(key hidden)"
}

// secretItemPath names item i of the list under key at path: by the item's
// name for an env or header entry whose name prints, by its index otherwise.
func secretItemPath(path, key string, i int, item *yaml.Node) string {
	if kind := secretLists[key]; kind == "env" || kind == "header" {
		if _, name := secretField(item, "name"); secretPrintable(name) {
			return path + "[" + name + "]"
		}
	}
	return fmt.Sprintf("%s[%d]", path, i)
}

// secretField returns the scalar under key in mapping n and its text, or
// nil and the empty string.
func secretField(n *yaml.Node, key string) (*yaml.Node, string) {
	for i := 0; n.Kind == yaml.MappingNode && i+1 < len(n.Content); i += 2 {
		if v := n.Content[i+1]; n.Content[i].Value == key && v.Kind == yaml.ScalarNode {
			return v, v.Value
		}
	}
	return nil, ""
}
