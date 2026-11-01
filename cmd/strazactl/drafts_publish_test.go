package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestDraftsPublishIsRetired pins drafts publish: whatever the old verb
// took, it sends nothing, needs no token or server, says that a person
// publishes every draft in the console, and exits 2 as a retired verb does.
func TestDraftsPublishIsRetired(t *testing.T) {
	const want = "strazactl no longer publishes drafts, because it runs on an admin API token, which is not a person, " +
		"and a person publishes every draft. Publish it in the console under Drafts"
	for _, args := range [][]string{
		{"drafts", "publish", "41"},
		{"drafts", "publish", "41", "--yes", "--ack", "legacy-desk", "--confirm", "policy.guardrail-changed=3", "--json"},
		{"drafts", "publish"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Setenv("STRAZA_SERVER", "")
			t.Setenv(apiTokenEnv, "")
			root := newRootCmd(noCreds(t))
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(args)
			cmd, err := root.ExecuteC()
			if err == nil || err.Error() != want {
				t.Fatalf("err\n got %v\nwant %s", err, want)
			}
			if code := exitCode(cmd, err); code != 2 || out.Len() != 0 {
				t.Errorf("exit %d, printed %q, want 2 and nothing printed", code, out.String())
			}
		})
	}
}
