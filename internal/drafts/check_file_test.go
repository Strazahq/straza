package drafts

import (
	"reflect"
	"testing"
	"time"
)

// TestCheckRefusesAFileThatDoesNotRead pins file.refused: a draft of the
// apps directory whose file became no items answers that one refusal, with
// the file's name and the sentence its door stored, and "Fix the file."
// before the fix stored on the line after it, and never publishes. A draft
// of another door, or a file draft with items, never meets it.
func TestCheckRefusesAFileThatDoesNotRead(t *testing.T) {
	t.Parallel()
	const fix = "Fix the file. Straza proposes it again once it is saved."
	cases := []struct {
		name string
		d    Draft
		want []Finding
	}{
		{"a file that does not parse",
			Draft{Door: DoorAppsDir, Source: "/etc/straza/apps/bad.yaml", Refusal: "manifest: parse: yaml: line 3: did not find expected key"},
			[]Finding{{Code: "file.refused", Class: ClassRefused,
				Sentence: "bad.yaml does not read as an MCP server manifest: manifest: parse: yaml: line 3: did not find expected key.", Fix: fix}}},
		{"a sentence that ends in a period keeps one",
			Draft{Door: DoorAppsDir, Source: "/etc/straza/apps/oauth.yaml", Refusal: "credential.oauth.provider \"github\" is not configured on this server."},
			[]Finding{{Code: "file.refused", Class: ClassRefused,
				Sentence: "oauth.yaml does not read as an MCP server manifest: credential.oauth.provider \"github\" is not configured on this server.", Fix: fix}}},
		{"a file name that prints nothing is spelled out",
			Draft{Door: DoorAppsDir, Source: "/apps/b\u202ead.yaml", Refusal: "manifest: parse"},
			[]Finding{{Code: "file.refused", Class: ClassRefused,
				Sentence: "bU+202Ead.yaml does not read as an MCP server manifest: manifest: parse.", Fix: fix}}},
		{"a finding's fix follows the file's own",
			Draft{Door: DoorAppsDir, Source: "/etc/straza/apps/secret.yaml", Refusal: "In the server github, env.TOKEN holds a token.\nRemove the value."},
			[]Finding{{Code: "file.refused", Class: ClassRefused,
				Sentence: "secret.yaml does not read as an MCP server manifest: In the server github, env.TOKEN holds a token.",
				Fix:      "Fix the file. Remove the value. Straza proposes it again once it is saved."}}},
		{"another door", Draft{Door: DoorConsole, Refusal: "manifest: parse"}, []Finding{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Check(checkWorld(), tc.d, CheckInput{Now: time.Now()})
			if !reflect.DeepEqual(v.Refused, tc.want) {
				t.Errorf("refused = %+v, want %+v", v.Refused, tc.want)
			}
		})
	}
}

// TestCheckLeavesAFileDraftWithItemsAlone pins that a draft of the apps
// directory whose file read is checked like any other.
func TestCheckLeavesAFileDraftWithItemsAlone(t *testing.T) {
	t.Parallel()
	w := checkWorld()
	d := stamped(w, appPut("github"))
	d.Door, d.Source = DoorAppsDir, "/etc/straza/apps/github.yaml"
	for _, f := range Check(w, d, CheckInput{Now: time.Now()}).Refused {
		if f.Code == "file.refused" {
			t.Fatalf("a file draft with items met %+v", f)
		}
	}
}
