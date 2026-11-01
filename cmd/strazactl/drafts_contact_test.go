package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestDraftsContactIsRetired pins drafts contact: whatever the old verb
// took, it sends nothing, needs no token or server, says that a person
// contacts a proposed server in the console, and exits 2 as a retired verb
// does.
func TestDraftsContactIsRetired(t *testing.T) {
	const want = "strazactl no longer contacts proposed servers, because it runs on an admin API token, which is not a person, " +
		"and a person contacts a proposed server. Contact it in the console under Drafts"
	for _, args := range [][]string{
		{"drafts", "contact", "41", "github"},
		{"drafts", "contact", "41", "github", "--json"},
		{"drafts", "contact", "41"},
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
