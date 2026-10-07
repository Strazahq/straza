package drafts

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/redact"
)

// The limits of one draft: its items and note together, its items, its
// note, and the open drafts one proposer may keep.
const (
	maxDraftBytes = 1 << 20
	maxDraftItems = 1000
	maxNoteBytes  = 2000
	maxOpenDrafts = 10
)

// The codes of the intake refusals that are not about reading a document.
const (
	codeDraftEmpty        = "draft.empty"
	codeDraftSize         = "draft.size"
	codeDraftOpenLimit    = "draft.open-limit"
	codeNoteSize          = "draft.note-size"
	codeNoteCharacters    = "draft.note-characters"
	codeAppParse          = "app.parse"
	codeAgentSponsor      = "agent.sponsor"
	codeAgentRuntime      = "agent.runtime"
	codeAgentStrazaRole   = "agent.straza-role"
	codeAgentRego         = "agent.rego"
	codeAgentCapture      = "agent.capture"
	codeAgentSelfApproval = "agent.self-approval"
	codeAgentASCIIName    = "agent.ascii-name"
	codeAgentASCIIHost    = "agent.ascii-host"
	codeBundleMasked      = "bundle.masked"
)

// noteObject is the Object of a finding about the draft's note.
const noteObject = "Note"

// invisibleNames are the characters a note may not hold beside the control
// characters, the set the decider reason rule refuses: the direction
// embeddings, overrides, isolates and marks, which can reorder the words on
// every surface that shows them, and two invisible spaces.
var invisibleNames = map[rune]string{
	0x202A: "left-to-right embedding", 0x202B: "right-to-left embedding", 0x202C: "pop directional formatting",
	0x202D: "left-to-right override", 0x202E: "right-to-left override",
	0x2066: "left-to-right isolate", 0x2067: "right-to-left isolate", 0x2068: "first strong isolate", 0x2069: "pop directional isolate",
	0x200E: "left-to-right mark", 0x200F: "right-to-left mark", 0x061C: "Arabic letter mark",
	0x200B: "zero width space", 0xFEFF: "zero width no-break space",
}

// invisibleInNames are the characters a name may not hold: those of
// invisibleNames and four a note keeps, the joiners and the soft hyphen,
// because the decider reason rule keeps them legal for emoji sequences and
// for Persian and Indic text.
var invisibleInNames = func() map[rune]string {
	out := map[rune]string{0x200C: "zero width non-joiner", 0x200D: "zero width joiner", 0x2060: "word joiner", 0x00AD: "soft hyphen"}
	maps.Copy(out, invisibleNames)
	return out
}()

// appHead is what the agent rules read of an App document: its runtime kind
// and a remote server's address. The manifest parser reads the rest.
type appHead struct {
	Straza struct {
		Runtime struct {
			Kind   string `yaml:"kind"`
			Remote struct {
				URL string `yaml:"url"`
			} `yaml:"remote"`
		} `yaml:"runtime"`
	} `yaml:"straza"`
}

// Intake answers the refusals a draft meets before it is stored: the size
// of the draft, every item's kind, name, op and document counting toward
// it, its number of items, the size and the characters of its note, the
// rules of each item and of reading its document, the secret scan of every
// item and of the note, and the agent rules that need no live state. The agent rules
// hold for the straza-app door and for a proposer who is not a person, and
// agent.sponsor for a proposer who is not a person alone. It answers the
// draft's findings first, then each item's in item order. The server adds
// app.parse, with AppParse, for the App items the manifest parser refuses,
// and draft.open-limit, with OpenLimit, on the doors that count.
func Intake(d Draft, proposer Principal) []Finding {
	var out []Finding
	if len(d.Items) == 0 {
		out = append(out, refusal(codeDraftEmpty, "", "The draft holds no document.", "Add at least one App, Role, PolicySet or Removal document."))
	}
	size := len(d.Note)
	for _, it := range d.Items {
		size += len(it.Kind) + len(it.Name) + len(it.Op) + len(it.Doc)
	}
	if size > maxDraftBytes {
		out = append(out, refusal(codeDraftSize, "",
			fmt.Sprintf("The draft holds %s of documents and note, and a draft holds at most 1 MiB.", bytesWords(size)), "Split it into smaller drafts."))
	}
	if len(d.Items) > maxDraftItems {
		out = append(out, refusal(codeDraftSize, "",
			fmt.Sprintf("The draft holds %s items, and a draft holds at most %s.", thousands(len(d.Items)), thousands(maxDraftItems)), "Split it into smaller drafts."))
	}
	if proposer.Agent && proposer.SponsorID == "" {
		out = append(out, sponsorRefusal(proposer.Username))
	}
	out = append(out, noteFindings(d.Note)...)
	agent := d.Door == DoorAgent || proposer.Agent
	seen := map[string]int{}
	for i, it := range d.Items {
		out = append(out, itemIntake(i+1, it, seen)...)
		if agent {
			out = append(out, agentItem(it, nil)...)
		}
		out = append(out, refusedOnly(ScanSecrets(it))...)
		out = append(out, maskedFindings(it, d.Door)...)
	}
	return out
}

// maskedFindings refuses an App put whose document holds what a route
// answers in place of a value it does not show, because a document read
// from a route and sent back would store the mask as the value. The apps
// directory keeps no mask, so its fix says to write the value in the file
// or to move one secret into a stored credential, and what cannot move.
func maskedFindings(it Item, door Door) []Finding {
	if it.Kind != KindApp || it.Op != OpPut {
		return nil
	}
	at := maskedAt(it.Doc)
	if at == "" {
		return nil
	}
	key, object, name := it.Object(), visible(it.Object()), visible(it.Name)
	if NameWithheld(it) {
		key = string(it.Kind) + "/(name withheld)"
		object, name = key, "NAME"
	}
	fix := "Write the real value in place of the mask, or leave that place exactly as strazactl apps export prints it to keep the stored value, and send the draft again."
	if door == DoorAppsDir {
		fix = "Write the real value in place of the mask, because Straza publishes a file of the apps directory as it is written. " +
			"An argument or an environment value that the stored manifest holds is kept when the file holds the same value. " +
			"To take one secret out of the file, set credential.inject so Straza adds it as a header or an environment entry, and store it with strazactl apps secret set " +
			name + ", which asks for the value at a hidden prompt. " +
			"Straza injects one secret per server, so a second secret, or one the server takes only as an argument, cannot leave the file yet."
	}
	return []Finding{refusal(codeBundleMasked, key,
		fmt.Sprintf("%s holds a value that Straza masked for display at %s, so publishing it would store the mask in place of the value.", object, visible(at)), fix)}
}

// maskedAt answers where the App document doc holds a mask, or "": a
// string or a key that reads redact.Mark or redact.URL's placeholder whole,
// such as an env value, an argument, a description or a server block field
// that drafts.WithoutSecrets masked, or an address inside any string that
// maskedURL reads as masked. A document that does not decode is left to the
// manifest parser.
func maskedAt(doc string) string {
	var root yaml.Node
	if yaml.Unmarshal([]byte(doc), &root) != nil {
		return ""
	}
	return maskedNode(&root, "")
}

// maskedNode answers the path of the first mask in n, found at path, in
// document order: a mapping's key and value at the key's path, a list's
// items at their index. An alias is not followed, because the text it names
// is read where its anchor is defined.
func maskedNode(n *yaml.Node, path string) string {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			if at := maskedNode(c, path); at != "" {
				return at
			}
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			if at := maskedNode(c, fmt.Sprintf("%s[%d]", path, i)); at != "" {
				return at
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			child := n.Content[i].Value
			if path != "" {
				child = path + "." + child
			}
			for _, c := range n.Content[i : i+2] {
				if at := maskedNode(c, child); at != "" {
					return at
				}
			}
		}
	case yaml.ScalarNode:
		if n.Value == redact.Mark || n.Value == "<unparseable url>" || slices.ContainsFunc(secretURLRe.FindAllString(n.Value, -1), maskedURL) {
			return path
		}
	}
	return ""
}

// maskedURL reports whether address is what a route answers for an address
// it masked: redact.URL's form of one whose query or fragment it hid or
// that it could not parse, one whose user name reads redact.Mark in place of the user
// information it hid, or one with redact.Mark as a segment of its path,
// written plain or percent-encoded, or as a query value, where
// drafts.WithoutSecrets or a risk's words hid a capability or a secret. An
// address masked whole is a path of one segment.
func maskedURL(address string) bool {
	if strings.HasSuffix(address, "?\u2026") || strings.HasSuffix(address, "#\u2026") || address == "<unparseable url>" {
		return true
	}
	u, err := url.Parse(address)
	if err != nil {
		return false
	}
	if (u.User != nil && u.User.Username() == redact.Mark) || slices.Contains(strings.Split(u.Path, "/"), redact.Mark) {
		return true
	}
	for _, values := range u.Query() {
		if slices.Contains(values, redact.Mark) {
			return true
		}
	}
	return false
}

// OpenLimit refuses a new draft of the proposer named proposer, who already
// keeps open drafts that count against the limit, or answers nil. The
// server counts them with CountOpen, which skips slot drafts, and asks only
// for a new draft on the drafts route without working and on the
// straza-app door.
func OpenLimit(proposer string, open int) []Finding {
	if open < maxOpenDrafts {
		return nil
	}
	return []Finding{refusal(codeDraftOpenLimit, "",
		fmt.Sprintf("%s already has %d open drafts, and a proposer may keep at most %d.", visible(proposer), open, maxOpenDrafts),
		"Publish, discard or wait for one of them, then send this draft again.")}
}

// AppParse is the refusal of the App item named name whose manifest the
// manifest parser refused with err, in the parser's own words.
func AppParse(name string, err error) Finding {
	return refusal(codeAppParse, string(KindApp)+"/"+name, err.Error(), "")
}

// itemIntake answers the refusals of item n that need no live state: its
// kind, name and op, a second item for its object, a document on a
// removal, and the reading of its document, which must be one YAML
// document whose kind and name read as written. seen counts the items of
// each object read so far. The name refusal of an item ParseBundle read
// names and carries its document's number in place of n.
func itemIntake(n int, it Item, seen map[string]int) []Finding {
	switch it.Kind {
	case KindApp, KindRole, KindPolicySet:
	default:
		return []Finding{refusal(codeBundleKind, "",
			fmt.Sprintf("Document %d has kind %s, and a draft holds App, Role, PolicySet and Removal documents only.", n, visible(string(it.Kind))),
			"Remove it, or make that change with its own command.")}
	}
	if it.Name == "" {
		return []Finding{unnamed(n)}
	}
	var out []Finding
	// The name refusal is recorded and the rules after it still run, because
	// the check waives it for a name live state holds, and the item must
	// then meet every other rule as a plain-named item does.
	if words, invisible := nameFault(it.Name); words != "" {
		why := "which can make it pass for another name."
		if invisible {
			why = "which can make it read differently from what is stored."
		}
		out = append(out, numbered(it.Number, refusal(codeBundleNameChars, "",
			fmt.Sprintf("The name of document %d %s, %s", cmp.Or(it.Number, n), words, why),
			"Rename the object and send the draft again."))...)
	}
	object := it.Object()
	if seen[object]++; seen[object] == 2 {
		out = append(out, refusal(codeBundleDuplicate, object, fmt.Sprintf("The draft names %s twice.", visible(object)),
			"Keep one document for each object and send the draft again."))
	}
	switch {
	case it.Op == OpRemove && it.Doc != "":
		return append(out, refusal(codeBundleRemoval, object, fmt.Sprintf("%s is removed, and a removal carries no document.", visible(object)),
			"Leave the document out, or put the object instead."))
	case it.Op == OpRemove:
		return out
	case it.Op == OpOff && it.Kind != KindPolicySet:
		return append(out, refusal(codeBundleOff, object,
			fmt.Sprintf("%s cannot be turned off: only a PolicySet stays stored while it is off.", visible(object)), "Remove it instead, or leave it out."))
	case it.Op != OpPut && it.Op != OpOff:
		return append(out, refusal(codeBundleOp, object,
			fmt.Sprintf("%s has op %q, and an item's op is put, remove or off.", visible(object), it.Op), "Use put, remove or off, and send the draft again."))
	}
	if fs := oneDocument(n, it); len(fs) > 0 {
		return append(out, fs...)
	}
	head, err := headOf(it.Doc)
	switch {
	case errors.Is(err, errHiddenHead):
		return append(out, hiddenHead(n))
	case it.Kind == KindApp && errors.Is(err, errNoName):
		return append(out, unnamed(n))
	}
	named := ""
	switch it.Kind {
	case KindApp:
		named = head.Name
	case KindRole:
		doc, fs := readRole(it.Name, it.Doc)
		out, named = append(out, fs...), doc.Metadata.Name
	case KindPolicySet:
		doc, fs := readPolicy(it, it.Doc)
		out, named = append(out, fs...), doc.Metadata.Name
	}
	if named != "" && named != it.Name {
		out = append(out, refusal(codeBundleName, object,
			fmt.Sprintf("The item names %s and its document names %s.", visible(object), visible(named)), "Make the two agree."))
	}
	return out
}

// agentItem answers the agent rules of one item that need no live state:
// plain ASCII names, no command or container server, no host in another
// alphabet, no Straza role, no Rego module and no self-approval. roleDocs
// holds the Role documents a check already read, by name, and a Role
// document it lacks is read here.
func agentItem(it Item, roleDocs map[string]RoleDoc) []Finding {
	object := it.Object()
	var out []Finding
	names := []string{it.Name}
	straza := strings.HasPrefix(strings.ToLower(it.Name), "straza-")
	if it.Op != OpRemove {
		switch it.Kind {
		case KindApp:
			out = append(out, agentApp(object, it.Doc)...)
		case KindRole:
			doc, read := roleDocs[it.Name]
			if !read {
				var err error
				doc, err = ParseRole(it.Doc)
				read = err == nil
			}
			if read {
				straza = straza || doc.Spec.Kind == RoleKindStraza
				names = append(append(names, doc.Spec.Server), doc.Spec.Implies...)
				for _, b := range doc.Spec.Bindings {
					names = append(names, b.App)
				}
			}
		case KindPolicySet:
			if doc, err := policy.Parse([]byte(it.Doc)); err == nil {
				out = append(out, agentPolicy(object, doc)...)
				names = append(names, doc.Spec.Match.Roles...)
				for _, r := range doc.Spec.Rules {
					if r.Approve != nil {
						names = append(names, r.Approve.Roles...)
					}
				}
			}
		}
	}
	if it.Kind == KindRole && straza {
		out = append(out, strazaRoleRefusal(object, it.Name))
	}
	reported := map[string]bool{}
	for _, name := range names {
		if name == "" || reported[name] || plainName(name) {
			continue
		}
		reported[name] = true
		out = append(out, refusal(codeAgentASCIIName, object,
			fmt.Sprintf("The name %s holds a character outside a-z, A-Z, 0-9, hyphen, dot and underscore, and an agent names objects in plain ASCII so that none can pass for another.", visible(name)),
			"Rename it and submit again."))
	}
	return out
}

// agentApp answers the agent rules of an App document: a remote server only,
// at a host written in plain ASCII. A document that does not decode is left
// to the manifest parser.
func agentApp(object, doc string) []Finding {
	var head appHead
	if yaml.Unmarshal([]byte(doc), &head) != nil {
		return nil
	}
	rt := head.Straza.Runtime
	if rt.Kind == "command" || rt.Kind == "oci" {
		return []Finding{refusal(codeAgentRuntime, object,
			"An agent cannot propose a command or container server, because such a server runs code on the Straza host as Straza's own user, with its data directory in reach.",
			"Propose a remote server, or ask a holder of "+MCPAdminRole+" to add this one.")}
	}
	host := hostOf(rt.Remote.URL)
	if r, ok := foreignLetter(host); ok {
		return []Finding{refusal(codeAgentASCIIHost, object,
			fmt.Sprintf("The host %s holds a letter from another alphabet, %s, which can pass for a familiar name.", visible(host), runeWords(r)),
			"Write the host in plain ASCII, or in its xn-- form, and propose again.")}
	}
	return nil
}

// agentPolicy answers the agent rules of a PolicySet document: no Rego
// module and no rule that lets a requester approve their own calls.
func agentPolicy(object string, doc policy.Document) []Finding {
	var out []Finding
	if doc.Spec.Escape != nil {
		out = append(out, refusal(codeAgentRego, object,
			"An agent cannot propose a Rego module, because the module runs on every decision on the server and on every workstation.",
			"A person who may change policy sets adds it."))
	}
	for _, r := range doc.Spec.Rules {
		if r.Approve != nil && r.Approve.SelfApproval {
			out = append(out, refusal(codeAgentSelfApproval, object,
				fmt.Sprintf("Rule %s of %s sets selfApproval: true, which lets a requester approve their own calls, and an agent cannot propose that.", visible(r.ID), visible(doc.Metadata.Name)),
				"Leave it out. A person can add it."))
		}
	}
	return out
}

// strazaRoleRefusal refuses an agent's item for the Straza role name.
func strazaRoleRefusal(object, name string) Finding {
	return refusal(codeAgentStrazaRole, object,
		fmt.Sprintf("%s is a Straza role, which governs Straza itself, and an agent cannot propose one.", visible(name)),
		"A person who may change roles creates it.")
}

// sponsorRefusal refuses the draft of the agent named agent, which has no
// active sponsor.
func sponsorRefusal(agent string) Finding {
	return refusal(codeAgentSponsor, "",
		fmt.Sprintf("%s has no active sponsor, and an agent's draft needs a person who answers for it.", visible(agent)),
		"Ask an administrator to set its sponsor in your identity manager, then submit again.")
}

// noteFindings answers the refusals of the draft's note: its size, a
// character that reads differently from what is stored, and the secret
// scan.
func noteFindings(note string) []Finding {
	var out []Finding
	if len(note) > maxNoteBytes {
		out = append(out, refusal(codeNoteSize, noteObject,
			fmt.Sprintf("The note holds %s, and a note holds at most 2,000 bytes.", bytesWords(len(note))), "Shorten it and send the draft again."))
	}
	if words := oddCharacter(note, "\n\t", invisibleNames); words != "" {
		out = append(out, refusal(codeNoteCharacters, noteObject,
			fmt.Sprintf("The note holds %s, that can make the text read differently from what is stored.", words),
			"Remove it and send the draft again."))
	}
	return append(out, refusedOnly(ScanNote(note))...)
}

// oddCharacter words the first character of s that reads differently from
// what is stored, or answers "": a byte that is not UTF-8 text, a control
// character other than those allow holds, or a character of the set
// invisible, which is invisibleNames for a note and invisibleInNames for a
// name.
func oddCharacter(s, allow string, invisible map[rune]string) string {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return fmt.Sprintf("a byte that is not UTF-8 text, 0x%02X", s[i])
		}
		i += size
		name, listed := invisible[r]
		switch {
		case strings.ContainsRune(allow, r):
			continue
		case r == '\r':
			name = "carriage return"
		case r == '\n':
			name = "line feed"
		case r == '\t':
			name = "tab"
		case unicode.IsControl(r):
			name = "control character"
		case !listed:
			continue
		}
		return fmt.Sprintf("an invisible character, %s (U+%04X)", name, r)
	}
	return ""
}

// plainName reports whether name holds only a-z, A-Z, 0-9, hyphen, dot and
// underscore.
func plainName(name string) bool {
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
		default:
			return false
		}
	}
	return true
}

// refusedOnly keeps the refusals of fs.
func refusedOnly(fs []Finding) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Class == ClassRefused {
			out = append(out, f)
		}
	}
	return out
}

// bytesWords words a size in bytes, such as "2,345 bytes".
func bytesWords(n int) string {
	if n == 1 {
		return "1 byte"
	}
	return thousands(n) + " bytes"
}

// thousands writes n with a comma between each group of three digits.
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
