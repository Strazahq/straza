package drafts

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The documents of the bundle tests: a remote server, a role it owns with
// its lists out of order, a set with a comment, and a removal.
const (
	bundleApp = `apiVersion: straza.dev/v1beta1
kind: App
metadata:
  name: github
server:
  name: io.github/github
  version: 1.0.0
straza:
  runtime:
    kind: remote
    remote:
      url: https://api.githubcopilot.com/mcp/
`
	bundleRole = `apiVersion: straza.dev/v1beta1
kind: Role
metadata:
  name: github-readers
spec:
  kind: application
  server: github
  bindings:
    - app: github
      tools: [search_issues, get_me]
`
	bundleRoleCanonical = `apiVersion: straza.dev/v1beta1
kind: Role
metadata:
    name: github-readers
spec:
    kind: application
    server: github
    bindings:
        - app: github
          tools:
            - get_me
            - search_issues
`
	bundlePolicy = `# Hold every write for a person.
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: github-readers-access
spec:
  priority: 100
  match: { roles: [github-readers] }
  rules:
    - id: hold-writes
      tools: [mcp.call]
      apps: [github]
      effect: allow
      mode: approve
      approve: { roles: [sec-approvers] }
      reason: "Straza: a person approves each write"
`
	bundleRemoval = `apiVersion: straza.dev/v1beta1
kind: Removal
metadata:
  name: old-server
spec:
  kind: App
`
)

// findingLines spells each finding as its code, object and sentence, for a
// test to compare.
func findingLines(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Code+" "+f.Object+" "+f.Sentence)
	}
	return out
}

// hiddenLine is the finding line of document n, whose kind or name a tag,
// an alias or a merge key writes.
func hiddenLine(n int) string {
	return fmt.Sprintf("bundle.unnamed  Document %d writes its kind or name with a YAML tag, alias or merge key, so the name Straza reads can differ from the text a reviewer reads.", n)
}

// TestParseBundleNamesAnAppAsTheManifestParserDoes pins that the item of an
// App is named by the name the decoder reads, quoted or not, so it is the
// name the manifest parser will read.
func TestParseBundleNamesAnAppAsTheManifestParserDoes(t *testing.T) {
	t.Parallel()
	items, fs := ParseBundle([]string{strings.Replace(bundleApp, "name: github", `name: "github"`, 1)})
	if len(fs) > 0 || len(items) != 1 || items[0].Name != "github" {
		t.Errorf("ParseBundle = %+v, %v, want the App github", items, findingLines(fs))
	}
}

func TestParseBundleReadsEveryKind(t *testing.T) {
	t.Parallel()
	stream := "# The onboarding bundle.\n---\n" + bundleApp + "---\n" + bundleRole + "---\n" + bundlePolicy + "---\n" + bundleRemoval + "---\n"
	items, fs := ParseBundle([]string{stream})
	if len(fs) > 0 {
		t.Fatalf("ParseBundle refused: %v", findingLines(fs))
	}
	want := []Item{
		{Kind: KindApp, Name: "github", Op: OpPut, Doc: bundleApp, Number: 1},
		{Kind: KindRole, Name: "github-readers", Op: OpPut, Doc: bundleRoleCanonical, Number: 2},
		{Kind: KindPolicySet, Name: "github-readers-access", Op: OpPut, Doc: bundlePolicy, Number: 3},
		{Kind: KindApp, Name: "old-server", Op: OpRemove, Number: 4},
	}
	if !reflect.DeepEqual(items, want) {
		t.Errorf("ParseBundle items:\n got %+v\nwant %+v", items, want)
	}
}

// TestParseBundleKeepsWindowsLineEnds pins that a separator line ending in
// CR LF splits, and the documents keep their bytes.
func TestParseBundleKeepsWindowsLineEnds(t *testing.T) {
	t.Parallel()
	app := strings.ReplaceAll(bundleApp, "\n", "\r\n")
	items, fs := ParseBundle([]string{app + "---\r\n" + bundleRemoval})
	if len(fs) > 0 || len(items) != 2 || items[0].Doc != app {
		t.Errorf("ParseBundle = %+v, %v, want the App with its CR LF bytes and the removal", items, findingLines(fs))
	}
}

func TestParseBundleRefuses(t *testing.T) {
	t.Parallel()
	role := func(spec string) string {
		return "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n  name: dev\nspec:\n" + spec
	}
	cases := []struct {
		name  string
		texts []string
		items int
		want  []string
	}{
		{"a document that is not YAML, numbered across texts", []string{bundleApp, bundleRemoval + "---\nkind: [App\n"}, 1,
			[]string{"bundle.yaml  Document 3 is not valid YAML: yaml: line 1: did not find expected ',' or ']'."}},
		{"a separator line with a comment", []string{bundleApp + "--- # the role\n" + bundleRole}, 0,
			[]string{"bundle.split  Text 1 of the draft does not split cleanly into documents at its lines of ---, so Straza cannot tell where each document starts."}},
		{"a kind a draft does not hold", []string{"apiVersion: straza.dev/v1beta1\nkind: Group\nmetadata:\n  name: g\n"}, 0,
			[]string{"bundle.kind  Document 1 has kind Group, and a draft holds App, Role, PolicySet and Removal documents only."}},
		{"a document with no kind", []string{"- one\n- two\n"}, 0,
			[]string{"bundle.kind  Document 1 names no kind, and a draft holds App, Role, PolicySet and Removal documents only."}},
		{"a document with no name", []string{"apiVersion: straza.dev/v1beta1\nkind: App\nserver: {}\n"}, 0,
			[]string{"bundle.unnamed  Document 1 has no name."}},
		{"a document whose name is a list", []string{"apiVersion: straza.dev/v1beta1\nkind: Removal\nmetadata:\n  name: [a, b]\nspec:\n  kind: App\n"}, 0,
			[]string{"bundle.unnamed  Document 1 has no name."}},
		{"a document whose kind is a list", []string{"kind: [App]\nmetadata:\n  name: a\n"}, 0,
			[]string{"bundle.kind  Document 1 names no kind, and a draft holds App, Role, PolicySet and Removal documents only."}},
		{"a Role with no kind", []string{role("  description: x\n")}, 0,
			[]string{"role.kind-missing Role/dev Role dev names no spec.kind, and a draft never picks a kind for you."}},
		{"a Role with two bindings", []string{role("  kind: application\n  bindings:\n    - {app: a, tools: [x]}\n    - {app: b, tools: [y]}\n")}, 0,
			[]string{"role.bindings Role/dev Role dev lists 2 bindings, and an application role reaches one MCP server."}},
		{"a Role with a field it does not have", []string{role("  kind: business\n  owner: x\n")}, 0,
			[]string{"role.parse Role/dev Role dev does not read as a Role document: line 7: field owner not found in type spec."}},
		{"a PolicySet that does not parse", []string{"apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata:\n  name: p\nspec:\n  rules: []\n"}, 0,
			[]string{`policy.parse PolicySet/p policy: spec.rules must contain at least one rule`}},
		{"a Removal of a kind a draft does not hold", []string{strings.Replace(bundleRemoval, "kind: App", "kind: Pack", 1)}, 0,
			[]string{`removal.kind  The Removal of old-server names spec.kind "Pack".`}},
		{"a Removal with a field it does not have", []string{bundleRemoval + "  reason: x\n"}, 0,
			[]string{"removal.parse  The Removal of old-server, document 1, does not read as a Removal document: line 7: field reason not found in type spec."}},
		{"a Removal of another apiVersion", []string{strings.Replace(bundleRemoval, "v1beta1", "v2", 1)}, 0,
			[]string{`removal.parse  The Removal of old-server, document 1, does not read as a Removal document: apiVersion must be straza.dev/v1beta1, and it is "straza.dev/v2".`}},
		{"an empty text holds nothing", []string{"", "---\n", "# only a comment\n"}, 0, nil},
		{"a name written with a binary tag", []string{strings.Replace(bundleApp, "name: github", "name: !!binary Z2l0aHVi", 1)}, 0, []string{hiddenLine(1)}},
		{"a kind written with a tag", []string{strings.Replace(bundleRemoval, "kind: Removal", "kind: !!str Removal", 1)}, 0, []string{hiddenLine(1)}},
		{"a name merged into metadata", []string{"apiVersion: straza.dev/v1beta1\nkind: Removal\nmetadata:\n  <<: {name: q}\n  name: p\nspec: {kind: App}\n"}, 0,
			[]string{hiddenLine(1)}},
		{"metadata written as an alias", []string{"apiVersion: straza.dev/v1beta1\nkind: Removal\nx: &m {name: evil}\nmetadata: *m\nspec: {kind: App}\n"}, 0,
			[]string{hiddenLine(1)}},
		{"a key written as an alias", []string{"apiVersion: straza.dev/v1beta1\nx: &k kind\n*k : Removal\nmetadata: {name: p}\nspec: {kind: App}\n"}, 0,
			[]string{hiddenLine(1)}},
		{"a removed kind written with a tag", []string{strings.Replace(bundleRemoval, "  kind: App", "  kind: !!str App", 1)}, 0, []string{hiddenLine(1)}},
		{"a key written twice", []string{"apiVersion: straza.dev/v1beta1\nkind: Removal\nmetadata: {name: a}\nmetadata: {name: b}\nspec: {kind: App}\n"}, 0,
			[]string{`bundle.yaml  Document 1 is not valid YAML: line 4: mapping key "metadata" already defined at line 3.`}},
		{"a document after a text that did not split cleanly keeps its number", []string{bundleApp + "--- \n" + bundlePolicy, "kind: [App\n"}, 0, []string{
			"bundle.split  Text 1 of the draft does not split cleanly into documents at its lines of ---, so Straza cannot tell where each document starts.",
			"bundle.yaml  Document 3 is not valid YAML: yaml: line 1: did not find expected ',' or ']'."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, fs := ParseBundle(tc.texts)
			if got := findingLines(fs); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseBundle findings:\n got %q\nwant %q", got, tc.want)
			}
			if len(items) != tc.items {
				t.Errorf("ParseBundle made %d items, want %d", len(items), tc.items)
			}
			for _, f := range fs {
				if f.Class != ClassRefused || f.Fix == "" && f.Code != codePolicyParse {
					t.Errorf("finding %s has class %s and fix %q, want a refusal with a fix", f.Code, f.Class, f.Fix)
				}
			}
		})
	}
}

func TestSplitStream(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		text  string
		parts int
		clean bool
	}{
		{"one document", "a: 1\n", 1, true},
		{"a comment above the first separator", "# c\n---\na: 1\n", 1, true},
		{"a trailing separator", "a: 1\n---\n", 1, true},
		{"two separators in a row", "a: 1\n---\n---\nb: 2\n", 2, true},
		{"a separator with trailing space is not one", "a: 1\n--- \nb: 2\n", 1, false},
		{"a document end marker", "a: 1\n...\n---\nb: 2\n", 2, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parts, bad, clean, _ := splitStream(tc.text)
			if len(parts) != tc.parts || clean != tc.clean || len(bad) > 0 {
				t.Errorf("splitStream = %d parts, clean %v, bad %v, want %d parts, clean %v", len(parts), clean, bad, tc.parts, tc.clean)
			}
		})
	}
}

// TestFindingForChange pins the words a direct route answers for an intake
// refusal of the one object it changes: the object named as the caller
// knows it, a fix that makes the change again, and no draft and no
// document number. A finding with neither reads as intake wrote it.
func TestFindingForChange(t *testing.T) {
	t.Parallel()
	server := func(doc string) Item { return Item{Kind: KindApp, Name: "github", Op: OpPut, Doc: doc} }
	masked := strings.Replace(bundleApp, "    kind: remote\n    remote:\n      url: https://api.githubcopilot.com/mcp/\n",
		"    kind: command\n    command:\n      exec: /usr/bin/gh-mcp\n      env:\n        - {name: GH_HOST, value: \"[REDACTED]\"}\n", 1)
	const why = "A change cannot carry a secret, because Straza keeps the text of every change in its history, where administrators read it."
	cases := []struct {
		name string
		it   Item
		code string
		want Finding
	}{
		{"a manifest followed by a broken document", server(bundleApp + "---\n{{{\n"), "bundle.yaml", Finding{
			Sentence: "The manifest of the server github is not valid YAML: yaml: line 14: did not find expected node content.",
			Fix:      "Fix the document and make the change again."}},
		{"a manifest of two documents", server(bundleApp + "---\n" + bundleRemoval), "bundle.split", Finding{
			Sentence: "The manifest of the server github holds 2 documents, and a change takes one.",
			Fix:      "Send each document as a change of its own."}},
		{"a manifest that writes its name with a tag", server(strings.Replace(bundleApp, "name: github", "name: !!str github", 1)), "bundle.unnamed", Finding{
			Sentence: "The manifest of the server github writes its kind or name with a YAML tag, alias or merge key, so the name Straza reads can differ from the text a reviewer reads.",
			Fix:      "Write kind and metadata.name as plain text, and make the change again."}},
		{"a role name with an invisible character", intakeRole("dev\u200b", "    kind: business\n"), "bundle.name-characters", Finding{
			Sentence: "The name of the role devU+200B holds an invisible character, zero width space (U+200B), which can make it read differently from what is stored.",
			Fix:      "Rename the object and make the change again."}},
		{"a role name with letters from two scripts", intakeRole("\u0430dmin", "    kind: business\n"), "bundle.name-characters", Finding{
			Sentence: "The name of the role \u0430dmin holds letters from both Cyrillic and Latin, which can make it pass for another name.",
			Fix:      "Rename the object and make the change again."}},
		{"a manifest that carries a mask", server(masked), "bundle.masked", Finding{
			Sentence: "The manifest of the server github holds a value that Straza masked for display at straza.runtime.command.env[0].value, so publishing it would store the mask in place of the value.",
			Fix:      "Write the real value in place of the mask, or leave that place exactly as strazactl apps export prints it to keep the stored value, and make the change again."}},
		{"a role document with a field it does not have", intakeRole("dev", "    kind: business\n    owner: x\n"), "role.parse", Finding{
			Sentence: "Role dev does not read as a Role document: line 7: field owner not found in type spec.",
			Fix:      "Compare it with strazactl roles export dev and make the change again."}},
		{"a role description that holds a secret", intakeRole("dev", "    kind: business\n    description: key wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\n"), "secret.value", Finding{
			Sentence: "In the role dev, spec.description holds a value after a word that names a secret. " + why,
			Fix:      "Remove the value. A role never needs a secret."}},
		{"an address that carries a password", server(strings.Replace(bundleApp, "https://api.githubcopilot.com", "https://bot:Hunter2Hunter2@api.githubcopilot.com", 1)), "secret.userinfo", Finding{
			Sentence: "In the server github, straza.runtime.remote.url holds an address that carries a password or a credential before its host. " + why,
			Fix: "Remove the user name and password from the address. If the server needs the secret, make the change without it, " +
				"then store it with strazactl apps secret set github, which asks for the value at a hidden prompt, and set credential.inject so Straza adds it for you."}},
		{"an env value that holds a token", intakeServer("    kind: command\n    command:\n      exec: /usr/bin/gh-mcp\n      env:\n        - {name: GH_TOKEN, value: ghp_0123456789abcdefghijklmnopqrstuvwxyzAB}\n"),
			"secret.shape", Finding{
				Sentence: "In the server github, straza.runtime.command.env[GH_TOKEN].value holds what looks like a GitHub token. " + why,
				Fix: "Remove it. If the server needs the secret, set credential.kind: static and credential.inject with as: env, name: GH_TOKEN and template: {{secret}}, " +
					"make the change, then store the secret with strazactl apps secret set github, which asks for the value at a hidden prompt."}},
		{"a policy comment that holds a secret", intakeSet("p", "    - id: r1\n      tools: [mcp.call]\n      effect: allow\n      # token ghp_0123456789abcdefghijklmnopqrstuvwxyzAB\n"), "secret.shape", Finding{
			Sentence: "In the policy set p, line 12 holds what looks like a GitHub token. " + why + " Once published, its text, comments included, is served to anyone who asks, without a sign-in.",
			Fix:      "Remove it. A policy set never needs a secret, in a rule or in a comment."}},
		{"the removal of a role whose name looks like a token", Item{Kind: KindRole, Name: "ghp_0123456789abcdefghijklmnopqrstuvwxyzAB", Op: OpRemove}, "secret.shape", Finding{
			Sentence: "The name of the role you asked to remove holds what looks like a GitHub token. " + why,
			Fix:      "Straza does not change or remove a role whose name looks like a secret, so this one stays. Treat the secret in its name as exposed and rotate it."}},
		{"the removal of a server whose name looks like a token", Item{Kind: KindApp, Name: "ghp_0123456789abcdefghijklmnopqrstuvwxyzAB", Op: OpRemove}, "secret.shape", Finding{
			Sentence: "The name of the server you asked to remove holds what looks like a GitHub token. " + why,
			Fix:      "Straza does not change or remove a server whose name looks like a secret, so this one stays. Treat the secret in its name as exposed and rotate it."}},
		{"the removal of a policy set whose name looks like a token", Item{Kind: KindPolicySet, Name: "ghp_0123456789abcdefghijklmnopqrstuvwxyzAB", Op: OpRemove}, "secret.shape", Finding{
			Sentence: "The name of the policy set you asked to remove holds what looks like a GitHub token. " + why,
			Fix:      "Straza does not change or remove a policy set whose name looks like a secret, so this one stays. Treat the secret in its name as exposed and rotate it."}},
		{"a manifest of 1 MiB", server(bundleApp + "#" + strings.Repeat("a", 1<<20-len(bundleApp)-2) + "\n"), "draft.size", Finding{
			Sentence: "The change to the server github holds 1,048,588 bytes, and a change holds at most 1 MiB.",
			Fix:      "Make the manifest of the server github smaller and make the change again."}},
		{"a finding with no draft words", intakeServer("    kind: oci\n    oci:\n      image: ghcr.io/x/y\n"), "agent.runtime", Finding{
			Sentence: "An agent cannot propose a command or container server, because such a server runs code on the Straza host as Straza's own user, with its data directory in reach.",
			Fix:      "Propose a remote server, or ask a holder of straza-global-mcp-admin to add this one."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var f Finding
			for _, g := range Intake(Draft{Door: DoorAPI, Items: []Item{tc.it}}, agent) {
				if g.Code == tc.code {
					f = g
					break
				}
			}
			if f.Code == "" {
				t.Fatalf("Intake = %q, want a finding %s", codes(Intake(Draft{Door: DoorAPI, Items: []Item{tc.it}}, agent)), tc.code)
			}
			got := f.ForChange(tc.it)
			if got.Sentence != tc.want.Sentence || got.Fix != tc.want.Fix {
				t.Errorf("ForChange of %s\n got %q %q\nwant %q %q", f.Code, got.Sentence, got.Fix, tc.want.Sentence, tc.want.Fix)
			}
			if got.Code != f.Code || got.Class != f.Class || got.Object != f.Object {
				t.Errorf("ForChange changed the code, class or object: %+v from %+v", got, f)
			}
		})
	}
}

// TestPlaces pins where each document of a bundle sits, by the number the
// bundle reader gives it: the index of its text, its place in that text,
// 0 for every number of a text that did not split cleanly, and the kind
// and name as written when they read as plain text. Each refusal and each
// item carries its document's number, so a document after a refused one
// or after a text that did not split cleanly keeps its own, and two
// refusals that read alike in two files keep theirs.
func TestPlaces(t *testing.T) {
	t.Parallel()
	maybe := func(name string) string {
		return strings.Replace(strings.Replace(bundlePolicy, "github-readers-access", name, 1), "effect: allow", "effect: maybe", 1)
	}
	texts := []string{
		"metadata:\n  name: one\n---\n" + strings.Replace(bundleRemoval, "kind: App", "kind: Pack", 1),
		"kind: [Role\n",
		bundleApp + "--- # the role\n" + bundleRole,
		bundleApp + "---\napiVersion: straza.dev/v1beta1\nkind: Group\nmetadata:\n  name: g\u200b\n---\n" +
			"apiVersion: straza.dev/v1beta1\nkind: Removal\nx: &m {name: evil}\nmetadata: *m\nspec: {kind: App}\n",
		maybe("pone"),
		bundleRemoval + "---\n" + maybe("ptwo"),
	}
	items, fs := ParseBundle(texts)
	var got []int
	for _, f := range fs {
		got = append(got, f.Document)
	}
	if want := []int{1, 2, 3, 4, 7, 8, 9, 11}; !reflect.DeepEqual(got, want) {
		t.Errorf("the refusals carry the documents %v, want %v: %v", got, want, findingLines(fs))
	}
	if fs[6].Sentence != fs[7].Sentence {
		t.Errorf("the two sets' refusals read %q and %q, want them alike", fs[6].Sentence, fs[7].Sentence)
	}
	got = nil
	for _, it := range items {
		got = append(got, it.Number)
	}
	if want := []int{6, 10}; !reflect.DeepEqual(got, want) {
		t.Errorf("the items carry the documents %v, want %v", got, want)
	}
	want := map[int]Place{
		1: {Text: 0, Doc: 1}, 2: {Text: 0, Doc: 2, Kind: "Removal", Name: "old-server"}, 3: {Text: 1, Doc: 1}, 4: {Text: 2}, 5: {Text: 2},
		6: {Text: 3, Doc: 1, Kind: "App", Name: "github"}, 7: {Text: 3, Doc: 2, Kind: "Group", Name: "gU+200B"}, 8: {Text: 3, Doc: 3},
		9: {Text: 4, Doc: 1, Kind: "PolicySet", Name: "pone"}, 10: {Text: 5, Doc: 1, Kind: "Removal", Name: "old-server"},
		11: {Text: 5, Doc: 2, Kind: "PolicySet", Name: "ptwo"},
	}
	if got := Places(texts); !reflect.DeepEqual(got, want) {
		t.Errorf("Places =\n %+v\nwant\n %+v", got, want)
	}
}
