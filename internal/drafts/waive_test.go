package drafts

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// waiveArgs is the stored manifest of a command server whose arguments
// are args, as the store keeps it.
func waiveArgs(name string, args ...string) string {
	return `{"apiVersion":"straza.dev/v1beta1","kind":"App","metadata":{"name":"` + name + `"},"straza":{"runtime":{"kind":"command","command":{"exec":"npx","args":["` +
		strings.Join(args, `","`) + `"]}}}}`
}

// waiveEnv is the stored manifest of a command server with one env entry.
func waiveEnv(name, entry, value string) string {
	return `{"apiVersion":"straza.dev/v1beta1","kind":"App","metadata":{"name":"` + name + `"},"straza":{"runtime":{"kind":"command","command":{"exec":"npx","env":[{"name":"` +
		entry + `","value":"` + value + `"}]}}}}`
}

// waiveNamedArg is the stored manifest of a remote server whose registry
// record carries one named package argument, and waiveNamedArgDoc the same
// server as an App document.
func waiveNamedArg(name, arg, value string) string {
	return `{"apiVersion":"straza.dev/v1beta1","kind":"App","metadata":{"name":"` + name + `"},"server":{"name":"io.x/` + name + `","version":"1.0.0","packages":[{"registryType":"npm","identifier":"demo","packageArguments":[{"type":"named","name":"` +
		arg + `","value":"` + value + `"}]}]},"straza":{"runtime":{"kind":"remote","remote":{"url":"https://x.example.com/mcp"}}}}`
}

func waiveNamedArgDoc(name, arg, value string) string {
	return "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: " + name + "\nserver:\n  name: io.x/" + name + "\n  version: \"1.0.0\"\n" +
		"  packages:\n    - registryType: npm\n      identifier: demo\n      packageArguments:\n        - type: named\n          name: " + arg + "\n          value: " + value + "\n" +
		"straza:\n  runtime:\n    kind: remote\n    remote:\n      url: https://x.example.com/mcp\n"
}

// waiveRole is the Role document of the business role name, implying
// implies when it is not empty.
func waiveRole(name, implies string) string {
	doc := "apiVersion: straza.dev/v1beta1\nkind: Role\nmetadata:\n    name: " + name + "\nspec:\n    kind: business\n"
	if implies != "" {
		doc += "    implies: [" + implies + "]\n"
	}
	return doc
}

// waiveApp is a command server named name with the given arguments, as a
// draft item.
func waiveApp(name, args string) Item {
	it := testCommandArgs(args)
	it.Name = name
	return Item{Kind: KindApp, Name: name, Op: OpPut, Doc: strings.Replace(it.Doc, "name: github", "name: "+name, 1)}
}

const (
	zwRole    = "dev\u200bops"
	zwServer  = "ops\u200bx"
	zwjRole   = "dev\u200dops"
	mixedRole = "\u0430dmin"
	cfRole    = "dev\u2062ops"
)

// TestWaive pins the two refusals live state waives and everything it
// leaves alone: an argument or env value equal to the one stored at the
// same place of the same server, and a name equal to a live object's of
// the same kind, each as a warning in the product's words, while any
// difference in the value, place, server or kind keeps the refusal.
func TestWaive(t *testing.T) {
	w := World{
		Apps: map[string]App{
			"github":   {Name: "github", Manifest: waiveArgs("github", "--auth", "oauth")},
			"crm":      {Name: "crm", Manifest: waiveEnv("crm", "CRM_API_KEY", "name")},
			"runner":   {Name: "runner", Manifest: waiveArgs("runner")},
			"vault":    {Name: "vault", Manifest: waiveArgs("vault", "--token", fakeGitHub)},
			"verbose":  {Name: "verbose", Manifest: waiveArgs("verbose", "--verbose", "oauth")},
			"reg":      {Name: "reg", Manifest: waiveNamedArg("reg", "--token", "oauth")},
			fakeGitHub: {Name: fakeGitHub, Manifest: waiveArgs(fakeGitHub, "--auth", "oauth")},
			zwServer:   {Name: zwServer, Manifest: waiveArgs(zwServer)},
		},
		Roles: map[string]Role{zwRole: {Name: zwRole}, zwjRole: {Name: zwjRole}, mixedRole: {Name: mixedRole}, cfRole: {Name: cfRole}, "équipe": {Name: "équipe"}},
	}
	agent := Principal{UserID: "u-bot", Username: "bot", Agent: true, SponsorID: "u-kim"}
	person := Principal{UserID: "u-kim", Username: "kim"}
	argFix := "If it is a secret, remove the flag and its value. If the server needs the secret, publish the draft without it, then store it with strazactl apps secret set github, " +
		"which asks for the value at a hidden prompt, and set credential.inject so Straza adds it for you. If it is not a secret, nothing needs to change."
	nameFix := "Nothing needs to change. A plain name takes a removal and a new object under the new name."
	tests := []struct {
		name     string
		d        Draft
		proposer Principal
		waived   []Finding
	}{
		{"an argument equal to the stored one after a flag naming a secret is waived",
			Draft{Items: []Item{waiveApp("github", "[--auth, oauth]")}}, person,
			[]Finding{{Code: "secret.value", Class: ClassWarning, Object: "App/github",
				Sentence: "In the server github, straza.runtime.command.args[1] holds a value after a word that names a secret. " +
					"The server's stored manifest already holds the same value there, so the draft exposes nothing new.",
				Fix: argFix}}},
		{"the same argument after another flag naming a secret is refused",
			Draft{Items: []Item{waiveApp("github", "[--token, oauth]")}}, person, nil},
		{"the same argument where the stored one follows a plain flag is refused",
			Draft{Items: []Item{waiveApp("verbose", "[--auth, oauth]")}}, person, nil},
		{"a named registry argument under another name marking a secret is refused",
			Draft{Items: []Item{{Kind: KindApp, Name: "reg", Op: OpPut, Doc: waiveNamedArgDoc("reg", "--password", "oauth")}}}, person, nil},
		{"a waived value names no server whose name the scan withholds",
			Draft{Items: []Item{waiveApp(fakeGitHub, "[--auth, oauth]")}}, person,
			[]Finding{{Code: "secret.value", Class: ClassWarning, Object: "App/(name withheld)",
				Sentence: "In a server whose name looks like a secret, straza.runtime.command.args[1] holds a value after a word that names a secret. " +
					"The server's stored manifest already holds the same value there, so the draft exposes nothing new.",
				Fix: strings.Replace(argFix, "secret set github", "secret set NAME", 1)}}},
		{"an argument one byte off the stored one is refused",
			Draft{Items: []Item{waiveApp("github", "[--auth, oautH]")}}, person, nil},
		{"the same argument on a server that does not hold it is refused",
			Draft{Items: []Item{waiveApp("runner", "[--auth, oauth]")}}, person, nil},
		{"the same argument on a server that does not exist is refused",
			Draft{Items: []Item{waiveApp("fresh", "[--auth, oauth]")}}, person, nil},
		{"the same argument at another position is refused",
			Draft{Items: []Item{waiveApp("github", "[--verbose, --auth, oauth]")}}, person, nil},
		{"an env value equal to the stored one under a name marking a secret is waived",
			Draft{Items: []Item{{Kind: KindApp, Name: "crm", Op: OpPut, Doc: strings.Replace(testCommandEnv("        - name: CRM_API_KEY\n          value: name\n").Doc, "name: github", "name: crm", 1)}}}, person,
			[]Finding{{Code: "secret.value", Class: ClassWarning, Object: "App/crm",
				Sentence: "In the server crm, straza.runtime.command.env[CRM_API_KEY].value holds a value, and its name marks it as a secret. " +
					"The server's stored manifest already holds the same value there, so the draft exposes nothing new.",
				Fix: "If it is a secret, remove the entry. If the server needs the secret, set credential.kind: static and credential.inject with as: env, name: CRM_API_KEY and template: {{secret}}, " +
					"publish the draft, then store the secret with strazactl apps secret set crm, which asks for the value at a hidden prompt. If it is not a secret, nothing needs to change."}}},
		{"an env entry named twice is refused, because its place is not one value",
			Draft{Items: []Item{{Kind: KindApp, Name: "crm", Op: OpPut, Doc: strings.Replace(testCommandEnv("        - name: CRM_API_KEY\n          value: name\n        - name: CRM_API_KEY\n          value: other\n").Doc, "name: github", "name: crm", 1)}}}, person, nil},
		{"a credential shape equal to the stored one stays refused",
			Draft{Items: []Item{waiveApp("vault", "[--token, "+fakeGitHub+"]")}}, person, nil},
		{"a live role name with an invisible character is waived",
			Draft{Items: []Item{{Kind: KindRole, Name: zwRole, Op: OpPut, Doc: waiveRole(zwRole, "")}}}, person,
			[]Finding{{Code: "bundle.name-characters", Class: ClassWarning,
				Sentence: "The name of document 1 holds an invisible character, zero width space (U+200B), and the stored name of that role already holds it, so the change goes ahead under the stored name.",
				Fix:      nameFix}}},
		{"the removal of a live role name with an invisible character is waived",
			Draft{Items: []Item{{Kind: KindRole, Name: zwRole, Op: OpRemove}}}, person,
			[]Finding{{Code: "bundle.name-characters", Class: ClassWarning,
				Sentence: "The name of document 1 holds an invisible character, zero width space (U+200B), and the stored name of that role already holds it, so the change goes ahead under the stored name.",
				Fix:      nameFix}}},
		{"a live odd name whose document names another role keeps that refusal",
			Draft{Items: []Item{{Kind: KindRole, Name: zwRole, Op: OpPut, Doc: waiveRole("target", "")}}}, person,
			[]Finding{{Code: "bundle.name-characters", Class: ClassWarning,
				Sentence: "The name of document 1 holds an invisible character, zero width space (U+200B), and the stored name of that role already holds it, so the change goes ahead under the stored name.",
				Fix:      nameFix}}},
		{"a live odd name with an op that is not put, remove or off keeps that refusal",
			Draft{Items: []Item{{Kind: KindRole, Name: zwRole, Op: "bogus", Doc: waiveRole(zwRole, "")}}}, person,
			[]Finding{{Code: "bundle.name-characters", Class: ClassWarning,
				Sentence: "The name of document 1 holds an invisible character, zero width space (U+200B), and the stored name of that role already holds it, so the change goes ahead under the stored name.",
				Fix:      nameFix}}},
		{"a live odd name turned off keeps that refusal",
			Draft{Items: []Item{{Kind: KindRole, Name: zwRole, Op: OpOff}}}, person,
			[]Finding{{Code: "bundle.name-characters", Class: ClassWarning,
				Sentence: "The name of document 1 holds an invisible character, zero width space (U+200B), and the stored name of that role already holds it, so the change goes ahead under the stored name.",
				Fix:      nameFix}}},
		{"a live odd name removed with a document keeps that refusal",
			Draft{Items: []Item{{Kind: KindRole, Name: zwRole, Op: OpRemove, Doc: waiveRole(zwRole, "")}}}, person,
			[]Finding{{Code: "bundle.name-characters", Class: ClassWarning,
				Sentence: "The name of document 1 holds an invisible character, zero width space (U+200B), and the stored name of that role already holds it, so the change goes ahead under the stored name.",
				Fix:      nameFix}}},
		{"a live odd name named twice keeps the duplicate refusal",
			Draft{Items: []Item{{Kind: KindRole, Name: zwRole, Op: OpPut, Doc: waiveRole(zwRole, "")}, {Kind: KindRole, Name: zwRole, Op: OpRemove}}}, person,
			[]Finding{{Code: "bundle.name-characters", Class: ClassWarning,
				Sentence: "The name of document 1 holds an invisible character, zero width space (U+200B), and the stored name of that role already holds it, so the change goes ahead under the stored name.",
				Fix:      nameFix}, {Code: "bundle.name-characters", Class: ClassWarning,
				Sentence: "The name of document 2 holds an invisible character, zero width space (U+200B), and the stored name of that role already holds it, so the change goes ahead under the stored name.",
				Fix:      nameFix}}},
		{"a live odd name from a bundle is waived under its document number",
			Draft{Items: []Item{{Kind: KindRole, Name: zwRole, Op: OpPut, Doc: waiveRole(zwRole, ""), Number: 3}}}, person,
			[]Finding{{Code: "bundle.name-characters", Class: ClassWarning, Document: 3,
				Sentence: "The name of document 3 holds an invisible character, zero width space (U+200B), and the stored name of that role already holds it, so the change goes ahead under the stored name.",
				Fix:      nameFix}}},
		{"a new name with the same character is refused",
			Draft{Items: []Item{{Kind: KindRole, Name: "qa\u200bteam", Op: OpPut, Doc: waiveRole("qa\u200bteam", "")}}}, person, nil},
		{"a live role name with a zero width joiner is waived",
			Draft{Items: []Item{{Kind: KindRole, Name: zwjRole, Op: OpPut, Doc: waiveRole(zwjRole, "")}}}, person,
			[]Finding{{Code: "bundle.name-characters", Class: ClassWarning,
				Sentence: "The name of document 1 holds an invisible character, zero width joiner (U+200D), and the stored name of that role already holds it, so the change goes ahead under the stored name.",
				Fix:      nameFix}}},
		{"a live role name with letters from two scripts is waived",
			Draft{Items: []Item{{Kind: KindRole, Name: mixedRole, Op: OpPut, Doc: waiveRole(mixedRole, "")}}}, person,
			[]Finding{{Code: "bundle.name-characters", Class: ClassWarning,
				Sentence: "The name of document 1 holds letters from both Cyrillic and Latin, as the stored name of that role does, so the change goes ahead under the stored name.",
				Fix:      nameFix}}},
		{"a live role name with an invisible character outside the named set is waived",
			Draft{Items: []Item{{Kind: KindRole, Name: cfRole, Op: OpRemove}}}, person,
			[]Finding{{Code: "bundle.name-characters", Class: ClassWarning,
				Sentence: "The name of document 1 holds an invisible character, U+2062, and the stored name of that role already holds it, so the change goes ahead under the stored name.",
				Fix:      nameFix}}},
		{"a new name with letters from two scripts is refused",
			Draft{Items: []Item{{Kind: KindRole, Name: "\u043eps", Op: OpPut, Doc: waiveRole("\u043eps", "")}}}, person, nil},
		{"a role name equal to a live server name is not waived",
			Draft{Items: []Item{{Kind: KindRole, Name: zwServer, Op: OpPut, Doc: waiveRole(zwServer, "")}}}, person, nil},
		{"a server name equal to a live role name is not waived",
			Draft{Items: []Item{waiveApp(zwRole, "[]")}}, person, nil},
		{"an agent's live role name outside ASCII is waived and a name its document introduces is not",
			Draft{Door: DoorAgent, Items: []Item{{Kind: KindRole, Name: "équipe", Op: OpPut, Doc: waiveRole("équipe", "résumé")}}}, agent,
			[]Finding{{Code: "agent.ascii-name", Class: ClassWarning, Object: "Role/équipe",
				Sentence: "The name équipe holds a character outside a-z, A-Z, 0-9, hyphen, dot and underscore, and the stored name of that role already holds it, so the change goes ahead under the stored name.",
				Fix:      nameFix}}},
	}
	kept := map[string]string{
		"a live odd name whose document names another role keeps that refusal":         "bundle.name",
		"a live odd name with an op that is not put, remove or off keeps that refusal": "bundle.op",
		"a live odd name turned off keeps that refusal":                                "bundle.off",
		"a live odd name removed with a document keeps that refusal":                   "bundle.removal-doc",
		"a live odd name named twice keeps the duplicate refusal":                      "bundle.duplicate",
		"a waived value names no server whose name the scan withholds":                 "secret.shape",
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := Intake(tc.d, tc.proposer)
			if len(fs) == 0 {
				t.Fatal("intake refused nothing, so the case tests no waiver")
			}
			got := Waive(w, tc.d, fs)
			if code := kept[tc.name]; code != "" && !slices.ContainsFunc(got, func(f Finding) bool { return f.Code == code && f.Class == ClassRefused }) {
				t.Errorf("the refusal %s is gone after the waiver:\n%s", code, testDescribe(got))
			}
			if len(got) != len(fs) {
				t.Fatalf("Waive answered %d findings for %d:\n%s", len(got), len(fs), testDescribe(got))
			}
			var warned []Finding
			for i, f := range got {
				if f.Class == ClassWarning {
					warned = append(warned, f)
				} else if f != fs[i] {
					t.Errorf("finding %d changed from %+v to %+v", i, fs[i], f)
				}
			}
			if !reflect.DeepEqual(warned, tc.waived) {
				t.Errorf("waived\n%s\nwant\n%s", testDescribe(warned), testDescribe(tc.waived))
				for _, f := range warned {
					t.Logf("fix: %q", f.Fix)
				}
			}
			for _, f := range got {
				if strings.Contains(f.Sentence+f.Fix, "oauth") || strings.Contains(f.Sentence+f.Fix, fakeGitHub) {
					t.Errorf("finding %s carries a value: %q", f.Code, f.Sentence)
				}
			}
		})
	}
}

// TestWaivable pins what a door may defer to live state: only findings
// Waive can downgrade.
func TestWaivable(t *testing.T) {
	tests := []struct {
		name  string
		codes []string
		want  bool
	}{
		{"no findings", nil, true},
		{"a secret value", []string{"secret.value"}, true},
		{"both name rules", []string{"bundle.name-characters", "agent.ascii-name"}, true},
		{"a masked document", []string{"bundle.masked"}, true},
		{"a secret shape beside a secret value", []string{"secret.value", "secret.shape"}, false},
		{"the draft's size", []string{"draft.size"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var fs []Finding
			for _, c := range tc.codes {
				fs = append(fs, Finding{Code: c, Class: ClassRefused})
			}
			if got := Waivable(fs); got != tc.want {
				t.Errorf("Waivable = %v, want %v", got, tc.want)
			}
		})
	}
}
