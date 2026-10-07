package server

import (
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/redact"
)

// maskedRunner is the command server runner with a plain word after a flag
// naming a secret, a plain env value and a value under a name that marks a
// secret, as a manifest stored before the scan may hold them, with the
// given description. Every route answers it with the word and both env
// values masked.
func maskedRunner(description string) string {
	return "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: runner\n  description: " + description + "\nserver:\n  name: io.x/runner\n  version: 1.0.0\n" +
		"straza:\n  runtime:\n    kind: command\n    command:\n      exec: /bin/sh\n      args: [-c, \"exec sleep 30\", --auth, oauth]\n" +
		"      env:\n        - {name: LOG_LEVEL, value: debug}\n        - {name: API_TOKEN, value: hunter2hunter2}\n"
}

// manifestJSON is doc as the store keeps a manifest.
func manifestJSON(t *testing.T, doc string) string {
	t.Helper()
	mf, err := manager.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := mf.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestKeptMasks pins the rule a mask sent back is judged by: a mask at a
// place where the live manifest as a route answers it, or as the stamp
// keeps it, holds the same mask stands for the live value, which the
// restored text holds in its place, and a mask anywhere else is answered
// by its path. An argument is matched under the argument before it, an env
// entry under its name, an address whole, a place written twice in either
// document never matches, and a document that holds an anchor, an alias or
// a merge key keeps no mask, because either could copy a restored value to
// a place no route masks.
func TestKeptMasks(t *testing.T) {
	t.Parallel()
	source := maskedRunner("The runner.")
	live := manifestJSON(t, source)
	answer := maskedManifest(live)
	if strings.Count(answer, redact.Mark) != 3 {
		t.Fatalf("the answered runner holds %d masks, want 3:\n%s", strings.Count(answer, redact.Mark), answer)
	}
	benign := manifestJSON(t, strings.Replace(source, "--auth, oauth]", "--auth, none]", 1))
	address := "https://bot:Hunter2Hunter2@linear.example/mcp?token=abc123abc123"
	remote := manifestJSON(t, draftApp("linear", address, "Linear."))
	remoteAnswer := maskedManifest(remote)
	undo, _ := drafts.WithoutSecrets(live)
	query := manifestJSON(t, draftApp("qonly", "https://qonly.example/mcp?token=abc123abc123", "Q."))
	queryUndo, _ := drafts.WithoutSecrets(query)
	remoteUndo, _ := drafts.WithoutSecrets(remote)
	mask := func(doc, value string) string {
		return strings.Replace(doc, "value: "+value, "value: '"+redact.Mark+"'", 1)
	}
	head := "apiVersion: straza.dev/v1beta1\nkind: App\n"
	runtime := "straza:\n  runtime:\n    kind: command\n    command:\n      exec: /bin/sh\n      args: [-c, \"exec sleep 30\", --auth, oauth]\n      env:\n        - {name: LOG_LEVEL, value: debug}\n"
	aliased := head + runtime + "        - {name: API_TOKEN, value: &tok '" + redact.Mark + "'}\nmetadata:\n  name: runner\n  description: *tok\nserver:\n  name: io.x/runner\n  version: 1.0.0\n"
	merged := mask(source, "hunter2hunter2")
	merged = strings.Replace(merged, "        - {name: API_TOKEN,", "        - &e {name: API_TOKEN,", 1) + "        - {<<: *e, name: LEAKED}\n"
	listed := head + strings.Replace(runtime, "      env:\n", "      env: &envs\n", 1) + "        - {name: API_TOKEN, value: '" + redact.Mark + "'}\n" +
		"metadata:\n  name: runner\nserver:\n  name: io.x/runner\n  version: 1.0.0\n  notes: *envs\n"
	cases := []struct {
		name, doc, live, at string
		kept                bool
		holds               []string
	}{
		{"the answered document sent back", answer, live, "straza.runtime.command.args[3]", true, []string{"oauth", "debug", "hunter2hunter2"}},
		{"a new description beside the masks", strings.Replace(answer, "The runner.", "Changed.", 1), live, "straza.runtime.command.args[3]", true, []string{"Changed.", "oauth", "debug", "hunter2hunter2"}},
		{"a plain env value the answer masked", mask(source, "debug"), live, "straza.runtime.command.env[0].value", true, []string{"debug"}},
		{"two masks in one document", mask(mask(source, "debug"), "hunter2hunter2"), live, "straza.runtime.command.env[0].value", true, []string{"debug", "hunter2hunter2"}},
		{"a mask at an env entry live does not hold", source + "        - {name: EXTRA, value: '" + redact.Mark + "'}\n", live, "straza.runtime.command.env[2].value", false, nil},
		{"a mask at an argument live holds plain", strings.Replace(source, "--auth, oauth]", "--auth, '"+redact.Mark+"']", 1), benign, "straza.runtime.command.args[3]", false, nil},
		{"an argument mask after the same flag at another index", strings.Replace(source, "[-c, \"exec sleep 30\", --auth, oauth]", "[-v, -c, \"exec sleep 30\", --auth, '"+redact.Mark+"']", 1),
			live, "straza.runtime.command.args[4]", true, []string{"--auth, 'oauth'"}},
		{"an env entry masked and moved", strings.Replace(mask(source, "hunter2hunter2"), "        - {name: LOG_LEVEL, value: debug}\n", "", 1) + "        - {name: LOG_LEVEL, value: debug}\n",
			live, "straza.runtime.command.env[0].value", true, []string{"hunter2hunter2"}},
		{"a mask on a server that is not live", mask(source, "debug"), "", "straza.runtime.command.env[0].value", false, nil},
		{"a document with no mask", source, live, "", true, nil},
		{"an env name written twice with a mask", strings.Replace(mask(source, "debug"), "name: API_TOKEN", "name: LOG_LEVEL", 1), live, "straza.runtime.command.env[0].value", false, nil},
		{"a masked key", strings.Replace(source, "  version: 1.0.0\n", "  version: 1.0.0\n  '"+redact.Mark+"': x\n", 1), live, "server." + redact.Mark, false, nil},
		{"the undo's form", undo, live, "straza.runtime.command.args[3]", true, []string{"oauth", "hunter2hunter2"}},
		{"a masked address sent back", remoteAnswer, remote, "server.remotes[0].headers[0].value", true, []string{"team-a", "Hunter2Hunter2", "token=abc123abc123"}},
		{"a masked address whose path changed", strings.ReplaceAll(remoteAnswer, "linear.example/mcp", "linear.example/v2"), remote, "server.remotes[0].url", false, nil},
		{"the undo's form of an address with a query parameter", queryUndo, query, "server.remotes[0].url", true, []string{"token=abc123abc123"}},
		{"the undo's form of an address with user information and a query", remoteUndo, remote, "server.remotes[0].url", true, []string{"Hunter2Hunter2", "token=abc123abc123"}},
		{"an anchored mask aliased into the description", aliased, live, "straza.runtime.command.env[1].value", false, nil},
		{"a merge key that copies a masked entry", merged, live, "straza.runtime.command.env[1].value", false, nil},
		{"an anchored env list aliased elsewhere", listed, live, "straza.runtime.command.env[1].value", false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restored, at := keptMasks(tc.doc, tc.live)
			if at != tc.at {
				t.Errorf("at = %q, want %q", at, tc.at)
			}
			if !tc.kept {
				if restored != "" {
					t.Errorf("restored = %q, want nothing", restored)
				}
				return
			}
			if restored == "" {
				t.Fatal("restored nothing")
			}
			if tc.at == "" && restored != tc.doc {
				t.Errorf("a document with no mask came back changed:\n%s", restored)
			}
			for _, mark := range []string{redact.Mark, "%5BREDACTED%5D", "?\u2026"} {
				if strings.Contains(restored, mark) {
					t.Errorf("restored still holds %s:\n%s", mark, restored)
				}
			}
			for _, v := range tc.holds {
				if !strings.Contains(restored, v) {
					t.Errorf("restored lacks %q:\n%s", v, restored)
				}
			}
			if _, err := manager.Parse([]byte(restored)); err != nil {
				t.Errorf("restored does not parse: %v\n%s", err, restored)
			}
		})
	}
}

// TestKeptMasksReadsAMaskAsIntakeDoes pins that keptMasks finds a mask in
// a document exactly when intake refuses it as bundle.masked, so no mask
// intake refuses is left standing and no plain document is held back.
func TestKeptMasksReadsAMaskAsIntakeDoes(t *testing.T) {
	t.Parallel()
	source := maskedRunner("The runner.")
	live := manifestJSON(t, source)
	undo, _ := drafts.WithoutSecrets(live)
	remote := manifestJSON(t, draftApp("linear", "https://bot:Hunter2Hunter2@linear.example/mcp?token=abc123abc123", "Linear."))
	for _, tc := range []struct{ name, doc string }{
		{"the answered runner", maskedManifest(live)},
		{"the undo's form", undo},
		{"the source", source},
		{"a plain env value masked", strings.Replace(source, "value: debug", "value: '"+redact.Mark+"'", 1)},
		{"the answered remote", maskedManifest(remote)},
		{"a masked address in a description", strings.Replace(source, "The runner.", "See https://%5BREDACTED%5D@wiki.example/runner.", 1)},
		{"a hidden query in a description", strings.Replace(source, "The runner.", "See https://wiki.example/runner?\u2026 for more.", 1)},
		{"the mark inside a longer string", strings.Replace(source, "The runner.", "See "+redact.Mark+" for more.", 1)},
		{"an anchored mask", strings.Replace(source, "value: hunter2hunter2", "value: &tok '"+redact.Mark+"'", 1) + "      workdir: *tok\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			it := drafts.Item{Kind: drafts.KindApp, Name: "runner", Op: drafts.OpPut, Doc: tc.doc}
			refused := false
			for _, f := range drafts.Intake(drafts.Draft{Items: []drafts.Item{it}}, drafts.Principal{}) {
				refused = refused || f.Code == "bundle.masked"
			}
			if _, at := keptMasks(tc.doc, ""); (at != "") != refused {
				t.Errorf("keptMasks reads a mask at %q, and intake refuses bundle.masked: %v", at, refused)
			}
		})
	}
}
