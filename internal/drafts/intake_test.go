package drafts

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// intakeRole is the item of a Role document named name with the given spec
// lines.
func intakeRole(name, spec string) Item {
	return Item{Kind: KindRole, Name: name, Op: OpPut,
		Doc: "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: " + name + "\nspec:\n" + spec}
}

// intakeSet is the item of a PolicySet named name whose one rule is given.
func intakeSet(name, rule string) Item {
	return Item{Kind: KindPolicySet, Name: name, Op: OpPut, Doc: "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata:\n  name: " + name +
		"\nspec:\n  priority: 10\n  match: { roles: [dev] }\n  rules:\n" + rule}
}

// intakeServer is the item of an App named github with the given runtime
// block.
func intakeServer(runtime string) Item {
	return Item{Kind: KindApp, Name: "github", Op: OpPut,
		Doc: "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: github\nserver:\n  name: io.github/github\n  version: 1.0.0\nstraza:\n  runtime:\n" + runtime}
}

// manyRemovals is n removals of roles that do not exist.
func manyRemovals(n int) []Item {
	items := make([]Item, n)
	for i := range items {
		items[i] = Item{Kind: KindRole, Name: fmt.Sprintf("r%05d", i), Op: OpRemove}
	}
	return items
}

// codes lists the code and object of each finding.
func codes(fs []Finding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Code+" "+f.Object)
	}
	return out
}

var (
	person = Principal{UserID: "u1", Username: "alice", Via: "session", Client: "console"}
	agent  = Principal{UserID: "u2", Username: "joe-agent", Agent: true, Via: "session", Client: "claude-code", SponsorID: "u1", SponsorName: "alice"}
)

func TestIntakeRefuses(t *testing.T) {
	t.Parallel()
	remote := "    kind: remote\n    remote:\n      url: https://api.example.com/mcp\n"
	holdRule := "    - id: r1\n      tools: [mcp.call]\n      effect: allow\n"
	cases := []struct {
		name string
		d    Draft
		want []string
	}{
		{"a draft with no item", Draft{}, []string{"draft.empty "}},
		{"a draft over 1 MiB", Draft{Items: []Item{{Kind: KindPolicySet, Name: "p", Op: OpPut, Doc: intakeSet("p", holdRule).Doc + "# " + strings.Repeat("x", maxDraftBytes) + "\n"}}},
			[]string{"draft.size "}},
		{"a note over 2,000 bytes", Draft{Items: []Item{intakeServer(remote)}, Note: strings.Repeat("n", 2001)}, []string{"draft.note-size Note"}},
		{"a note with a direction override", Draft{Items: []Item{intakeServer(remote)}, Note: "ok \u202e evil"}, []string{"draft.note-characters Note"}},
		{"a note with a secret", Draft{Items: []Item{intakeServer(remote)}, Note: "use https://bob:" + fakePass + "@git.example.com"}, []string{"secret.userinfo Note"}},
		{"an object named three times is refused once", Draft{Items: []Item{{Kind: KindApp, Name: "a", Op: OpRemove}, {Kind: KindApp, Name: "a", Op: OpRemove}, {Kind: KindApp, Name: "a", Op: OpRemove}}},
			[]string{"bundle.duplicate App/a"}},
		{"an App turned off", Draft{Items: []Item{{Kind: KindApp, Name: "a", Op: OpOff, Doc: "x"}}}, []string{"bundle.off App/a"}},
		{"an op an item does not have", Draft{Items: []Item{{Kind: KindRole, Name: "a", Op: "delete"}}}, []string{"bundle.op Role/a"}},
		{"a kind a draft does not hold", Draft{Items: []Item{{Kind: "Pack", Name: "a", Op: OpPut}}}, []string{"bundle.kind "}},
		{"an item with no name", Draft{Items: []Item{{Kind: KindRole, Op: OpRemove}}}, []string{"bundle.unnamed "}},
		{"an item holding two documents", Draft{Items: []Item{{Kind: KindPolicySet, Name: "p", Op: OpOff, Doc: "a: 1\n---\nb: 2\n"}}}, []string{"bundle.split PolicySet/p"}},
		{"a Role item whose document names another role", Draft{Items: []Item{{Kind: KindRole, Name: "dev", Op: OpPut, Doc: intakeRole("ops", "    kind: business\n").Doc}}},
			[]string{"bundle.name Role/dev"}},
		{"an App item whose document names another server", Draft{Items: []Item{{Kind: KindApp, Name: "gitlab", Op: OpPut, Doc: intakeServer(remote).Doc}}},
			[]string{"bundle.name App/gitlab"}},
		{"a set turned off whose document names another set", Draft{Items: []Item{{Kind: KindPolicySet, Name: "q", Op: OpOff, Doc: intakeSet("p", holdRule).Doc}}},
			[]string{"bundle.name PolicySet/q"}},
		{"a Role item that does not read", Draft{Items: []Item{intakeRole("dev", "    kind: business\n    owner: x\n")}}, []string{"role.parse Role/dev"}},
		{"a Role item with no kind", Draft{Items: []Item{intakeRole("dev", "    description: x\n")}}, []string{"role.kind-missing Role/dev"}},
		{"a set that does not parse", Draft{Items: []Item{{Kind: KindPolicySet, Name: "p", Op: OpPut, Doc: "kind: PolicySet\n"}}}, []string{"policy.parse PolicySet/p"}},
		{"a private key in a server's arguments", Draft{Items: []Item{intakeServer("    kind: command\n    command:\n      exec: npx\n      args: ['" + fakeKey + "']\n")}},
			[]string{"secret.shape App/github"}},
		{"a long random value draws no intake refusal", Draft{Items: []Item{intakeServer("    kind: command\n    command:\n      exec: npx\n      args: ['a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0']\n")}},
			[]string{}},
		{"a person's command server on the console", Draft{Door: DoorConsole, Items: []Item{intakeServer("    kind: command\n    command:\n      exec: npx\n")}}, []string{}},
		{"an App item followed by a broken document", Draft{Items: []Item{{Kind: KindApp, Name: "github", Op: OpPut, Doc: intakeServer(remote).Doc + "---\nkind: Role\nnote: [unclosed\n"}}},
			[]string{"bundle.yaml App/github"}},
		{"a set followed by a broken document", Draft{Items: []Item{{Kind: KindPolicySet, Name: "p", Op: OpPut, Doc: intakeSet("p", holdRule).Doc + "---\n{{{\n"}}},
			[]string{"bundle.yaml PolicySet/p"}},
		{"a set followed by an end marker and broken text", Draft{Items: []Item{{Kind: KindPolicySet, Name: "p", Op: OpPut, Doc: intakeSet("p", holdRule).Doc + "...\nnot: [yaml\n"}}},
			[]string{"bundle.yaml PolicySet/p"}},
		{"a set followed by a second document", Draft{Items: []Item{{Kind: KindPolicySet, Name: "p", Op: OpPut, Doc: intakeSet("p", holdRule).Doc + "---\nkind: PolicySet\n"}}},
			[]string{"bundle.split PolicySet/p"}},
		{"a put with no document", Draft{Items: []Item{{Kind: KindRole, Name: "dev", Op: OpPut}}}, []string{"bundle.split Role/dev"}},
		{"an App item whose metadata is an alias", Draft{Items: []Item{{Kind: KindApp, Name: "github", Op: OpPut,
			Doc: "apiVersion: straza.dev/v1beta1\nkind: App\nserver: {name: io.github/github, version: 1.0.0, meta: &m {name: evil}}\nmetadata: *m\nstraza:\n  runtime:\n" + remote}}},
			[]string{"bundle.unnamed "}},
		{"an App item whose name is written with a tag", Draft{Items: []Item{{Kind: KindApp, Name: "github", Op: OpPut,
			Doc: strings.Replace(intakeServer(remote).Doc, "name: github", "name: !!binary Z2l0aHVi", 1)}}},
			[]string{"bundle.unnamed "}},
		{"an App item whose document names no server", Draft{Items: []Item{{Kind: KindApp, Name: "github", Op: OpPut,
			Doc: strings.Replace(intakeServer(remote).Doc, "  name: github\n", "  description: x\n", 1)}}},
			[]string{"bundle.unnamed "}},
		{"a removal that carries a document", Draft{Items: []Item{{Kind: KindApp, Name: "old", Op: OpRemove,
			Doc: "straza:\n  runtime:\n    kind: command\n    command: {exec: /bin/sh}\n# approved by security\n"}}},
			[]string{"bundle.removal-doc App/old"}},
		{"a note that is not UTF-8", Draft{Items: []Item{{Kind: KindApp, Name: "old", Op: OpRemove}}, Note: "ok \xff\xfe bytes"}, []string{"draft.note-characters Note"}},
		{"a name that is not UTF-8", Draft{Items: []Item{{Kind: KindRole, Name: "bad\xffname", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with a line break", Draft{Items: []Item{{Kind: KindRole, Name: "line\nbreak", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with a direction override", Draft{Items: []Item{{Kind: KindRole, Name: "dev\u202e", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with a zero width non-joiner", Draft{Items: []Item{{Kind: KindRole, Name: "dev\u200cops", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with a zero width joiner", Draft{Items: []Item{{Kind: KindRole, Name: "dev\u200dops", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with a word joiner", Draft{Items: []Item{{Kind: KindRole, Name: "dev\u2060ops", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with a soft hyphen", Draft{Items: []Item{{Kind: KindRole, Name: "dev\u00adops", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a note with a zero width joiner is not refused", Draft{Items: []Item{intakeServer(remote)}, Note: "For the \U0001F469\u200d\U0001F4BB team."}, []string{}},
		{"a name with letters from two scripts", Draft{Items: []Item{{Kind: KindRole, Name: "\u0430dmin", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name that starts with a space", Draft{Items: []Item{{Kind: KindRole, Name: " dev", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name that ends with a space", Draft{Items: []Item{{Kind: KindRole, Name: "dev ", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with two spaces in a row", Draft{Items: []Item{{Kind: KindRole, Name: "dev  ops", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with a no-break space", Draft{Items: []Item{{Kind: KindRole, Name: "dev\u00a0ops", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with a colon, a hyphen, a digit and one space passes", Draft{Items: []Item{{Kind: KindRole, Name: "AR:Zahlung-Ops 2", Op: OpRemove}}}, []string{}},
		{"a name written in one script other than Latin passes", Draft{Items: []Item{{Kind: KindRole, Name: "\u0440\u0430\u0437\u0440\u0430\u0431\u043e\u0442\u043a\u0430", Op: OpRemove}}}, []string{}},
		{"a name with a Latin letter beyond ASCII passes", Draft{Items: []Item{{Kind: KindRole, Name: "\u00e9quipe-dev", Op: OpRemove}}}, []string{}},
		{"a Japanese name mixing Han with Hiragana and Katakana passes", Draft{Items: []Item{{Kind: KindRole, Name: "\u958b\u767a\u306e\u30c1\u30fc\u30e0", Op: OpRemove}}}, []string{}},
		{"a Korean name mixing Han with Hangul passes", Draft{Items: []Item{{Kind: KindRole, Name: "\u958b\u767a\ud300", Op: OpRemove}}}, []string{}},
		{"a Chinese name mixing Han with Bopomofo passes", Draft{Items: []Item{{Kind: KindRole, Name: "\u958b\u767c\u310a\u3127\u3122", Op: OpRemove}}}, []string{}},
		{"a name mixing Hiragana with Hangul is refused", Draft{Items: []Item{{Kind: KindRole, Name: "\u306e\ud300", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a Korean name with Latin letters passes", Draft{Items: []Item{{Kind: KindRole, Name: "HR\ud300", Op: OpRemove}}}, []string{}},
		{"a Japanese name with Latin letters passes", Draft{Items: []Item{{Kind: KindRole, Name: "SAP\u7d4c\u7406", Op: OpRemove}}}, []string{}},
		{"a name mixing Latin and Hangul with Hiragana is refused", Draft{Items: []Item{{Kind: KindRole, Name: "HR\ud300\u306e", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with a combining mark and a symbol passes", Draft{Items: []Item{{Kind: KindRole, Name: "e\u0301quipe-\u2605", Op: OpRemove}}}, []string{}},
		{"a name with a format character outside the named set", Draft{Items: []Item{{Kind: KindRole, Name: "admin\u2062", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with the Mongolian vowel separator", Draft{Items: []Item{{Kind: KindRole, Name: "admin\u180e", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with a tag character", Draft{Items: []Item{{Kind: KindRole, Name: "admin\U000e0061", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with a variation selector", Draft{Items: []Item{{Kind: KindRole, Name: "admin\ufe0f", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with the combining grapheme joiner", Draft{Items: []Item{{Kind: KindRole, Name: "adm\u034fin", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with the braille blank", Draft{Items: []Item{{Kind: KindRole, Name: "admin\u2800", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with a private use character", Draft{Items: []Item{{Kind: KindRole, Name: "admin\ue000", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with an unassigned code point", Draft{Items: []Item{{Kind: KindRole, Name: "admin\u0378", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with the Hangul filler", Draft{Items: []Item{{Kind: KindRole, Name: "admin\u3164", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with the Hangul choseong filler", Draft{Items: []Item{{Kind: KindRole, Name: "admin\u115f", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with the Hangul jungseong filler", Draft{Items: []Item{{Kind: KindRole, Name: "admin\u1160", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with the halfwidth Hangul filler", Draft{Items: []Item{{Kind: KindRole, Name: "admin\uffa0", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with the Khmer vowel inherent aq", Draft{Items: []Item{{Kind: KindRole, Name: "admin\u17b4", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"a name with the Khmer vowel inherent aa", Draft{Items: []Item{{Kind: KindRole, Name: "admin\u17b5", Op: OpRemove}}}, []string{"bundle.name-characters "}},
		{"removals count toward the size by their names", Draft{Items: []Item{{Kind: KindRole, Name: strings.Repeat("r", maxDraftBytes), Op: OpRemove}}},
			[]string{"draft.size "}},
		{"more than 1,000 items", Draft{Items: manyRemovals(maxDraftItems + 1)}, []string{"draft.size "}},
		{"1,000 items", Draft{Items: manyRemovals(maxDraftItems)}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := codes(Intake(tc.d, person))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Intake = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestIntakeSentences pins the words of each intake refusal this package
// writes.
func TestIntakeSentences(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		d        Draft
		proposer Principal
		want     Finding
	}{
		{"draft.empty", Draft{}, person, Finding{Code: "draft.empty", Sentence: "The draft holds no document.",
			Fix: "Add at least one App, Role, PolicySet or Removal document."}},
		{"draft.size", Draft{Items: []Item{{Kind: KindRole, Name: strings.Repeat("r", maxDraftBytes), Op: OpRemove}}, Note: "x"}, person,
			Finding{Code: "draft.size", Sentence: "The draft holds 1,048,587 bytes of documents and note, and a draft holds at most 1 MiB.", Fix: "Split it into smaller drafts."}},
		{"draft.note-size", Draft{Items: []Item{{Kind: KindApp, Name: "a", Op: OpRemove}}, Note: strings.Repeat("n", 2345)}, person,
			Finding{Code: "draft.note-size", Object: "Note", Sentence: "The note holds 2,345 bytes, and a note holds at most 2,000 bytes.", Fix: "Shorten it and send the draft again."}},
		{"draft.note-characters", Draft{Items: []Item{{Kind: KindApp, Name: "a", Op: OpRemove}}, Note: "line one\r\nline two"}, person,
			Finding{Code: "draft.note-characters", Object: "Note",
				Sentence: "The note holds an invisible character, carriage return (U+000D), that can make the text read differently from what is stored.",
				Fix:      "Remove it and send the draft again."}},
		{"bundle.duplicate", Draft{Items: []Item{{Kind: KindApp, Name: "a", Op: OpRemove}, {Kind: KindApp, Name: "a", Op: OpRemove}}}, person,
			Finding{Code: "bundle.duplicate", Object: "App/a", Sentence: "The draft names App/a twice.", Fix: "Keep one document for each object and send the draft again."}},
		{"bundle.off", Draft{Items: []Item{{Kind: KindRole, Name: "dev", Op: OpOff}}}, person,
			Finding{Code: "bundle.off", Object: "Role/dev", Sentence: "Role/dev cannot be turned off: only a PolicySet stays stored while it is off.", Fix: "Remove it instead, or leave it out."}},
		{"bundle.op", Draft{Items: []Item{{Kind: KindRole, Name: "dev", Op: "delete"}}}, person,
			Finding{Code: "bundle.op", Object: "Role/dev", Sentence: `Role/dev has op "delete", and an item's op is put, remove or off.`, Fix: "Use put, remove or off, and send the draft again."}},
		{"bundle.split on an item", Draft{Items: []Item{{Kind: KindPolicySet, Name: "p", Op: OpOff, Doc: "a: 1\n---\nb: 2\n"}}}, person,
			Finding{Code: "bundle.split", Object: "PolicySet/p", Sentence: "PolicySet/p holds 2 documents, and an item holds one.",
				Fix: "Send each document as an item of its own, or send them together as documents."}},
		{"bundle.name", Draft{Items: []Item{{Kind: KindRole, Name: "dev", Op: OpPut, Doc: intakeRole("ops", "    kind: business\n").Doc}}}, person,
			Finding{Code: "bundle.name", Object: "Role/dev", Sentence: "The item names Role/dev and its document names ops.", Fix: "Make the two agree."}},
		{"agent.sponsor", Draft{Door: DoorAgent, Items: []Item{{Kind: KindApp, Name: "a", Op: OpRemove}}}, Principal{Username: "joe-agent", Agent: true}, Finding{
			Code: "agent.sponsor", Sentence: "joe-agent has no active sponsor, and an agent's draft needs a person who answers for it.",
			Fix: "Ask an administrator to set its sponsor in your identity manager, then submit again."}},
		{"agent.runtime", Draft{Door: DoorAgent, Items: []Item{intakeServer("    kind: oci\n    oci:\n      image: ghcr.io/x/y\n")}}, agent, Finding{
			Code: "agent.runtime", Object: "App/github",
			Sentence: "An agent cannot propose a command or container server, because such a server runs code on the Straza host as Straza's own user, with its data directory in reach.",
			Fix:      "Propose a remote server, or ask a holder of straza-global-mcp-admin to add this one."}},
		{"agent.ascii-host", Draft{Door: DoorAgent, Items: []Item{intakeServer("    kind: remote\n    remote:\n      url: https://g\u0456thub.com/mcp\n")}}, agent, Finding{
			Code: "agent.ascii-host", Object: "App/github",
			Sentence: "The host g\u0456thub.com holds a letter from another alphabet, \u0456 (U+0456), which can pass for a familiar name.",
			Fix:      "Write the host in plain ASCII, or in its xn-- form, and propose again."}},
		{"agent.straza-role", Draft{Door: DoorAgent, Items: []Item{intakeRole("auditors", "    kind: straza\n")}}, agent, Finding{
			Code: "agent.straza-role", Object: "Role/auditors", Sentence: "auditors is a Straza role, which governs Straza itself, and an agent cannot propose one.",
			Fix: "A person who may change roles creates it."}},
		{"agent.rego", Draft{Door: DoorAgent, Items: []Item{{Kind: KindPolicySet, Name: "p", Op: OpPut, Doc: intakeSet("p", "    - id: r1\n      tools: [mcp.call]\n      effect: allow\n").Doc +
			"  escape:\n    rego: |\n      package straza.ext\n      deny contains \"no\" if { false }\n"}}}, agent, Finding{
			Code: "agent.rego", Object: "PolicySet/p", Sentence: "An agent cannot propose a Rego module, because the module runs on every decision on the server and on every workstation.",
			Fix: "A person who may change policy sets adds it."}},
		{"agent.self-approval", Draft{Door: DoorAgent, Items: []Item{intakeSet("p", "    - id: r1\n      tools: [mcp.call]\n      effect: allow\n      mode: approve\n      approve: { roles: [sec], selfApproval: true }\n")}}, agent, Finding{
			Code: "agent.self-approval", Object: "PolicySet/p",
			Sentence: "Rule r1 of p sets selfApproval: true, which lets a requester approve their own calls, and an agent cannot propose that.",
			Fix:      "Leave it out. A person can add it."}},
		{"bundle.yaml on an item", Draft{Items: []Item{{Kind: KindPolicySet, Name: "p", Op: OpPut, Doc: "a: 1\n---\n{{{\n"}}}, person, Finding{
			Code: "bundle.yaml", Object: "PolicySet/p", Sentence: "Document 1 is not valid YAML: yaml: line 3: did not find expected node content.",
			Fix: "Fix the document and send the draft again."}},
		{"bundle.unnamed for a name behind a tag", Draft{Items: []Item{{Kind: KindApp, Name: "github", Op: OpPut,
			Doc: "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: !!binary Z2l0aHVi\n"}}}, person, Finding{
			Code: "bundle.unnamed", Sentence: "Document 1 writes its kind or name with a YAML tag, alias or merge key, so the name Straza reads can differ from the text a reviewer reads.",
			Fix: "Write kind and metadata.name as plain text, and send the draft again."}},
		{"bundle.removal-doc", Draft{Items: []Item{{Kind: KindApp, Name: "old", Op: OpRemove, Doc: "kind: App\n"}}}, person, Finding{
			Code: "bundle.removal-doc", Object: "App/old", Sentence: "App/old is removed, and a removal carries no document.",
			Fix: "Leave the document out, or put the object instead."}},
		{"bundle.name-characters", Draft{Items: []Item{{Kind: KindRole, Name: "line\nbreak", Op: OpRemove}}}, person, Finding{
			Code:     "bundle.name-characters",
			Sentence: "The name of document 1 holds an invisible character, line feed (U+000A), which can make it read differently from what is stored.",
			Fix:      "Rename the object and send the draft again."}},
		{"bundle.name-characters for a zero width non-joiner", Draft{Items: []Item{{Kind: KindRole, Name: "dev\u200cops", Op: OpRemove}}}, person, Finding{
			Code:     "bundle.name-characters",
			Sentence: "The name of document 1 holds an invisible character, zero width non-joiner (U+200C), which can make it read differently from what is stored.",
			Fix:      "Rename the object and send the draft again."}},
		{"bundle.name-characters for a zero width joiner", Draft{Items: []Item{{Kind: KindRole, Name: "dev\u200dops", Op: OpRemove}}}, person, Finding{
			Code:     "bundle.name-characters",
			Sentence: "The name of document 1 holds an invisible character, zero width joiner (U+200D), which can make it read differently from what is stored.",
			Fix:      "Rename the object and send the draft again."}},
		{"bundle.name-characters for a word joiner", Draft{Items: []Item{{Kind: KindRole, Name: "dev\u2060ops", Op: OpRemove}}}, person, Finding{
			Code:     "bundle.name-characters",
			Sentence: "The name of document 1 holds an invisible character, word joiner (U+2060), which can make it read differently from what is stored.",
			Fix:      "Rename the object and send the draft again."}},
		{"bundle.name-characters for a soft hyphen", Draft{Items: []Item{{Kind: KindRole, Name: "dev\u00adops", Op: OpRemove}}}, person, Finding{
			Code:     "bundle.name-characters",
			Sentence: "The name of document 1 holds an invisible character, soft hyphen (U+00AD), which can make it read differently from what is stored.",
			Fix:      "Rename the object and send the draft again."}},
		{"bundle.name-characters for letters from two scripts", Draft{Items: []Item{{Kind: KindRole, Name: "\u0430dmin", Op: OpRemove}}}, person, Finding{
			Code:     "bundle.name-characters",
			Sentence: "The name of document 1 holds letters from both Cyrillic and Latin, which can make it pass for another name.",
			Fix:      "Rename the object and send the draft again."}},
		{"bundle.name-characters for a leading space", Draft{Items: []Item{{Kind: KindRole, Name: " dev", Op: OpRemove}}}, person, Finding{
			Code:     "bundle.name-characters",
			Sentence: "The name of document 1 starts with a space, which can make it pass for another name.",
			Fix:      "Rename the object and send the draft again."}},
		{"bundle.name-characters for a trailing space", Draft{Items: []Item{{Kind: KindRole, Name: "dev ", Op: OpRemove}}}, person, Finding{
			Code:     "bundle.name-characters",
			Sentence: "The name of document 1 ends with a space, which can make it pass for another name.",
			Fix:      "Rename the object and send the draft again."}},
		{"bundle.name-characters for two spaces in a row", Draft{Items: []Item{{Kind: KindRole, Name: "dev  ops", Op: OpRemove}}}, person, Finding{
			Code:     "bundle.name-characters",
			Sentence: "The name of document 1 holds two spaces in a row, which can make it pass for another name.",
			Fix:      "Rename the object and send the draft again."}},
		{"bundle.name-characters for a no-break space", Draft{Items: []Item{{Kind: KindRole, Name: "dev\u00a0ops", Op: OpRemove}}}, person, Finding{
			Code:     "bundle.name-characters",
			Sentence: "The name of document 1 holds a whitespace character other than a plain space, U+00A0, which can make it pass for another name.",
			Fix:      "Rename the object and send the draft again."}},
		{"bundle.name-characters for an invisible character outside the named set", Draft{Items: []Item{{Kind: KindRole, Name: "admin\u2062", Op: OpRemove}}}, person, Finding{
			Code:     "bundle.name-characters",
			Sentence: "The name of document 1 holds an invisible character, U+2062, which can make it read differently from what is stored.",
			Fix:      "Rename the object and send the draft again."}},
		{"bundle.name-characters for three scripts names the pair that conflicts", Draft{Items: []Item{{Kind: KindRole, Name: "HR\ud300\u306e", Op: OpRemove}}}, person, Finding{
			Code:     "bundle.name-characters",
			Sentence: "The name of document 1 holds letters from both Hangul and Hiragana, which can make it pass for another name.",
			Fix:      "Rename the object and send the draft again."}},
		{"draft.note-characters for a byte that is not UTF-8", Draft{Items: []Item{{Kind: KindApp, Name: "a", Op: OpRemove}}, Note: "ok \xff\xfe bytes"}, person, Finding{
			Code: "draft.note-characters", Object: "Note",
			Sentence: "The note holds a byte that is not UTF-8 text, 0xFF, that can make the text read differently from what is stored.",
			Fix:      "Remove it and send the draft again."}},
		{"draft.size for the item count", Draft{Items: manyRemovals(1001)}, person, Finding{
			Code: "draft.size", Sentence: "The draft holds 1,001 items, and a draft holds at most 1,000.", Fix: "Split it into smaller drafts."}},
		{"agent.ascii-name", Draft{Door: DoorAgent, Items: []Item{{Kind: KindRole, Name: "d\u0435v", Op: OpRemove}}}, agent, Finding{
			Code: "agent.ascii-name", Object: "Role/d\u0435v",
			Sentence: "The name d\u0435v holds a character outside a-z, A-Z, 0-9, hyphen, dot and underscore, and an agent names objects in plain ASCII so that none can pass for another.",
			Fix:      "Rename it and submit again."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.want.Class = ClassRefused
			fs := Intake(tc.d, tc.proposer)
			for _, f := range fs {
				if f.Code == tc.want.Code {
					if f != tc.want {
						t.Errorf("Intake finding\n got %+v\nwant %+v", f, tc.want)
					}
					return
				}
			}
			t.Errorf("Intake = %q, want a finding %s", codes(fs), tc.want.Code)
		})
	}
}

// TestIntakeNumbersANameByItsDocument pins that the name refusal of an item
// the bundle reader read names and carries the number of that item's
// document, so a document refused before it does not shift the number.
func TestIntakeNumbersANameByItsDocument(t *testing.T) {
	t.Parallel()
	items, bundle := ParseBundle([]string{"kind: [App\n", bundleApp + "---\n" + intakeRole("dev\u200b", "    kind: business\n").Doc})
	if len(bundle) != 1 || len(items) != 2 {
		t.Fatalf("ParseBundle = %d items, %v, want 2 items and the refusal of document 1", len(items), findingLines(bundle))
	}
	want := Finding{Code: "bundle.name-characters", Class: ClassRefused, Document: 3,
		Sentence: "The name of document 3 holds an invisible character, zero width space (U+200B), which can make it read differently from what is stored.",
		Fix:      "Rename the object and send the draft again."}
	fs := Intake(Draft{Items: items}, person)
	if i := slices.IndexFunc(fs, func(f Finding) bool { return f.Code == want.Code }); i < 0 || fs[i] != want {
		t.Errorf("Intake = %+v, want %+v among them", fs, want)
	}
}

// TestIntakeAgentRulesByDoorAndProposer pins where the agent rules hold: for
// the straza-app door whoever writes, and for a proposer who is not a person
// on any door, and the sponsor rule for a proposer who is not a person.
func TestIntakeAgentRulesByDoorAndProposer(t *testing.T) {
	t.Parallel()
	command := intakeServer("    kind: command\n    command:\n      exec: npx\n")
	unsponsored := Principal{Username: "joe-agent", Agent: true}
	cases := []struct {
		name     string
		door     Door
		proposer Principal
		want     []string
	}{
		{"a person on the console", DoorConsole, person, []string{}},
		{"a person on the straza-app door", DoorAgent, person, []string{"agent.runtime App/github"}},
		{"a sponsored agent on strazactl", DoorStrazactl, agent, []string{"agent.runtime App/github"}},
		{"an agent with no sponsor on the admin API", DoorAPI, unsponsored, []string{"agent.sponsor ", "agent.runtime App/github"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := codes(Intake(Draft{Door: tc.door, Items: []Item{command}}, tc.proposer))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Intake = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestIntakeAgentNamesTheObjectsItRefersTo pins that an agent's document
// names every role and server it refers to in plain ASCII, each name once.
func TestIntakeAgentNamesTheObjectsItRefersTo(t *testing.T) {
	t.Parallel()
	d := Draft{Door: DoorAgent, Items: []Item{
		intakeRole("dev", "    kind: business\n    server: r\u0435aders\n    implies: [r\u0435aders]\n"),
		intakeSet("p", "    - id: r1\n      tools: [mcp.call]\n      effect: allow\n      mode: approve\n      approve: { roles: [s\u0435c] }\n"),
	}}
	var names []string
	for _, f := range Intake(d, agent) {
		if f.Code == codeAgentASCIIName {
			names = append(names, f.Object+" "+strings.Fields(f.Sentence)[2])
		}
	}
	want := []string{"Role/dev r\u0435aders", "PolicySet/p s\u0435c"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("agent.ascii-name findings = %q, want %q", names, want)
	}
}

func TestIntakePassesAPlainDraft(t *testing.T) {
	t.Parallel()
	items, fs := ParseBundle([]string{bundleApp + "---\n" + bundleRole + "---\n" + bundlePolicy + "---\n" + bundleRemoval})
	if len(fs) > 0 {
		t.Fatalf("ParseBundle refused: %v", findingLines(fs))
	}
	d := Draft{Door: DoorAgent, Note: "The platform team asked for read access to GitHub.\n\tThanks.", Items: items}
	if got := Intake(d, agent); len(got) > 0 {
		t.Errorf("Intake refused a plain draft: %v", findingLines(got))
	}
}

// TestIntakeRefusesAMaskedDocument pins bundle.masked: an App document
// that holds what a route put in place of a value it would not show, an
// env value, an argument or a server block value reading redact.Mark, an
// address ending in redact.URL's query mark or reading its placeholder, or
// an address with redact.Mark as a segment of its path or as the whole
// address, is refused on every door. So is any other string or key that
// reads redact.Mark whole, and an address inside any string that carries
// one of the address marks. The same text inside a longer string passes.
func TestIntakeRefusesAMaskedDocument(t *testing.T) {
	t.Parallel()
	app := func(server, runtime string) Item {
		return Item{Kind: KindApp, Name: "github", Op: OpPut,
			Doc: "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: github\n  description: \"Values read [REDACTED] in its logs.\"\n" +
				"server:\n  name: io.github/github\n  version: 1.0.0\n" + server + "straza:\n  runtime:\n" + runtime}
	}
	remote := func(url string) string { return "    kind: remote\n    remote:\n      url: " + url + "\n" }
	described := func(description string) Item {
		it := app("", remote("https://api.example.com/mcp"))
		it.Doc = strings.Replace(it.Doc, `"Values read [REDACTED] in its logs."`, description, 1)
		return it
	}
	command := "    kind: command\n    command:\n      exec: /usr/bin/gh-mcp\n      env:\n        - {name: LOG, value: debug}\n        - {name: GH_HOST, value: \"[REDACTED]\"}\n"
	oci := "    kind: oci\n    oci:\n      image: ghcr.io/github/github-mcp-server:1\n      env:\n        - {name: GH_HOST, value: \"[REDACTED]\"}\n"
	header := "  remotes:\n    - url: https://api.example.com/mcp\n      headers:\n        - {name: X-Team, value: \"[REDACTED]\"}\n"
	cases := []struct {
		name  string
		it    Item
		where string
	}{
		{"a command's env value", app("", command), "straza.runtime.command.env[1].value"},
		{"a container's env value", app("", oci), "straza.runtime.oci.env[0].value"},
		{"an address whose query was masked", app("", remote("https://api.example.com/mcp?\u2026")), "straza.runtime.remote.url"},
		{"an address whose user information was masked", app("", remote("https://%5BREDACTED%5D@api.example.com/mcp")), "straza.runtime.remote.url"},
		{"a server block address whose user information was masked", app("  remotes:\n    - url: \"https://%5BREDACTED%5D@api.example.com/mcp\"\n", remote("https://api.example.com/mcp")),
			"server.remotes[0].url"},
		{"a server block value", app(header, remote("https://api.example.com/mcp")), "server.remotes[0].headers[0].value"},
		{"a server block default", app("  packages:\n    - registryType: npm\n      identifier: acme-mcp\n      environmentVariables:\n        - {name: X_TEAM, default: \"[REDACTED]\"}\n",
			remote("https://api.example.com/mcp")), "server.packages[0].environmentVariables[0].default"},
		{"a server block address that did not parse", app("  remotes:\n    - url: \"<unparseable url>\"\n", remote("https://api.example.com/mcp")),
			"server.remotes[0].url"},
		{"a command's argument", app("", "    kind: command\n    command:\n      exec: /usr/bin/gh-mcp\n      args: [--seed, \"[REDACTED]\"]\n"), "straza.runtime.command.args[1]"},
		{"an address with a masked segment in its path", app("", remote("https://hooks.example.com/services/T0AB12CD3/[REDACTED]")), "straza.runtime.remote.url"},
		{"an address with a percent-encoded masked segment in its path", app("", remote("https://hooks.example.com/services/%5BREDACTED%5D/mcp")), "straza.runtime.remote.url"},
		{"an address masked whole", app("", remote("\"[REDACTED]\"")), "straza.runtime.remote.url"},
		{"a server block address with a masked segment in its path", app("  remotes:\n    - url: \"https://hooks.example.com/[REDACTED]/mcp\"\n", remote("https://api.example.com/mcp")),
			"server.remotes[0].url"},
		{"a description masked whole", described("'[REDACTED]'"), "metadata.description"},
		{"a server block field masked whole", app("  websiteUrl: '[REDACTED]'\n", remote("https://api.example.com/mcp")), "server.websiteUrl"},
		{"a server block field whose address has a masked path segment", app("  websiteUrl: https://docs.example.com/s/T0AB12CD3/[REDACTED]\n", remote("https://api.example.com/mcp")),
			"server.websiteUrl"},
		{"a server block key masked whole", app("  notes:\n    '[REDACTED]': seen\n", remote("https://api.example.com/mcp")), "server.notes.[REDACTED]"},
		{"an inject template masked whole", app("", remote("https://api.example.com/mcp")+"  credential:\n    kind: static\n    inject: {as: header, name: X-Auth, template: '[REDACTED]'}\n"),
			"straza.credential.inject.template"},
		{"an argument whose address has its user information masked", app("", "    kind: command\n    command:\n      exec: npx\n      args: [mcp-remote, \"https://%5BREDACTED%5D@relay.example.com/sse\"]\n"),
			"straza.runtime.command.args[1]"},
		{"the mark in a description only", app("", remote("https://api.example.com/mcp")), ""},
		{"the mark inside a longer value", app("", "    kind: command\n    command:\n      exec: /usr/bin/gh-mcp\n      env:\n        - {name: A, value: \"x[REDACTED]\"}\n"), ""},
		{"the mark inside a longer argument or path segment", app("", "    kind: command\n    command:\n      exec: /usr/bin/gh-mcp\n      args: [\"--label=x[REDACTED]\", \"https://h.example.com/x[REDACTED]/mcp\"]\n"), ""},
		{"a removal", Item{Kind: KindApp, Name: "github", Op: OpRemove}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, door := range []Door{DoorConsole, DoorAPI, DoorAgent, DoorAppsDir} {
				var got []Finding
				for _, f := range Intake(Draft{Door: door, Items: []Item{tc.it}}, person) {
					if f.Code == "bundle.masked" {
						got = append(got, f)
					}
				}
				if tc.where == "" {
					if len(got) > 0 {
						t.Errorf("door %s: refused %v, want no bundle.masked", door, findingLines(got))
					}
					continue
				}
				fix := "Write the real value in place of the mask, or leave that place exactly as strazactl apps export prints it to keep the stored value, and send the draft again."
				if door == DoorAppsDir {
					fix = "Write the real value in place of the mask, because Straza publishes a file of the apps directory as it is written. " +
						"An argument or an environment value that the stored manifest holds is kept when the file holds the same value. " +
						"To take one secret out of the file, set credential.inject so Straza adds it as a header or an environment entry, and store it with strazactl apps secret set github, " +
						"which asks for the value at a hidden prompt. " +
						"Straza injects one secret per server, so a second secret, or one the server takes only as an argument, cannot leave the file yet."
				}
				want := []Finding{{Code: "bundle.masked", Class: ClassRefused, Object: "App/github",
					Sentence: "App/github holds a value that Straza masked for display at " + tc.where +
						", so publishing it would store the mask in place of the value.",
					Fix: fix}}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("door %s:\n got %+v\nwant %+v", door, got, want)
				}
			}
		})
	}
}

func TestOpenLimit(t *testing.T) {
	t.Parallel()
	if got := OpenLimit("alice", 9); got != nil {
		t.Errorf("OpenLimit at 9 = %v, want none", got)
	}
	want := []Finding{{Code: "draft.open-limit", Class: ClassRefused, Sentence: "alice already has 10 open drafts, and a proposer may keep at most 10.",
		Fix: "Publish, discard or wait for one of them, then send this draft again."}}
	if got := OpenLimit("alice", 10); !reflect.DeepEqual(got, want) {
		t.Errorf("OpenLimit at 10 = %+v, want %+v", got, want)
	}
}

func TestAppParse(t *testing.T) {
	t.Parallel()
	err := errors.New(`manifest: invalid App "github": server block is required (verbatim registry server.json)`)
	want := Finding{Code: "app.parse", Class: ClassRefused, Object: "App/github", Sentence: err.Error()}
	if got := AppParse("github", err); got != want {
		t.Errorf("AppParse = %+v, want %+v", got, want)
	}
}

func TestBytesWords(t *testing.T) {
	t.Parallel()
	for n, want := range map[int]string{1: "1 byte", 999: "999 bytes", 2345: "2,345 bytes", 1048577: "1,048,577 bytes"} {
		if got := bytesWords(n); got != want {
			t.Errorf("bytesWords(%d) = %q, want %q", n, got, want)
		}
	}
}
