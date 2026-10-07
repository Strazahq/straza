package agentguard

import (
	"path/filepath"
	"strings"
	"testing"
)

// The managed block ends at a COMMENT ("# END straza-managed"), and a comment
// is not a table boundary: every line an operator adds below the END marker,
// up to the next [table] header, is still inside [mcp_servers.straza]. An
// operator who respects "do not edit inside" therefore adds their key just
// BELOW the block, which is where an operator would hand-add
// startup_timeout_sec to work around a "not initialized" symptom.
// Once install writes that key itself, the naive splice produces the key
// twice in one table: TOML 1.0 forbids defining a key twice, so codex refuses
// the WHOLE config, a worse failure than the one the key was added to fix.
//
// Install therefore strips, from that tail region only, assignments of keys
// the rendered block already defines. Genuinely foreign keys are the user's
// and survive untouched.

// codexTOMLKeyOrder is the test's own miniature TOML reader: it walks the
// document the way a parser does, tracking the current table and the keys
// defined in it, so an assertion can ask a structural question instead of
// comparing bytes. Deliberately strict: a line that is neither a header, a
// comment, nor an assignment means the writer left a dangling value behind,
// and that is a failure, not something to skip past. (stdlib only: pulling in
// a TOML library to test the writer that exists BECAUSE we refuse to depend on
// one would be a poor trade.)
func codexTOMLKeyOrder(t *testing.T, content string) map[string][]string {
	t.Helper()
	keys := map[string][]string{}
	table := ""
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			end := strings.Index(line, "]")
			if end < 0 {
				t.Fatalf("unterminated table header %q in:\n%s", line, content)
			}
			table = strings.TrimSpace(strings.TrimPrefix(line[:end], "["))
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("dangling line %q (neither header, comment, nor assignment) in:\n%s", line, content)
		}
		keys[table] = append(keys[table], strings.TrimSpace(key))
	}
	return keys
}

// assertCodexTOMLValid fails on the one thing this class of bug produces: the
// same key defined twice in the same table, which makes codex reject the file.
func assertCodexTOMLValid(t *testing.T, content string) map[string][]string {
	t.Helper()
	keys := codexTOMLKeyOrder(t, content)
	for table, ks := range keys {
		seen := make(map[string]bool, len(ks))
		for _, k := range ks {
			if seen[k] {
				t.Fatalf("key %q defined twice in table [%s] (TOML forbids it and codex refuses the whole file):\n%s", k, table, content)
			}
			seen[k] = true
		}
	}
	return keys
}

// TestCodexMCPTailDuplicateKeys is the adversarial case above, end to end.
func TestCodexMCPTailDuplicateKeys(t *testing.T) {
	setOSName(t, "linux")
	const straza = "/opt/straza"
	canonical := codexWant("'" + straza + "'")

	tests := []struct {
		name string
		tail string // what the operator appended below the END marker
		want string // the whole file after re-install
	}{
		{
			name: "the hand-added workaround key is taken back",
			tail: "startup_timeout_sec = 60\n",
			want: canonical,
		},
		{
			name: "a different value of an owned key converges too",
			tail: "startup_timeout_sec = 45\n",
			want: canonical,
		},
		{
			name: "quoted spelling of an owned key is still that key",
			tail: "\"startup_timeout_sec\" = 45\n",
			want: canonical,
		},
		{
			name: "an owned key whose value wraps is taken whole",
			tail: "args = [\n  \"mcp\",\n  \"--harness\", \"codex\",\n]\n",
			want: canonical,
		},
		{
			name: "a foreign key below the marker is the user's and survives",
			tail: "env = { RUST_LOG = \"debug\" }\ntool_timeout_sec = 30\n",
			want: canonical + "env = { RUST_LOG = \"debug\" }\ntool_timeout_sec = 30\n",
		},
		{
			name: "a foreign multi-line value is not misread line by line",
			tail: "notes = [\n  \"command = mine\",\n  \"args = mine\",\n]\n",
			want: canonical + "notes = [\n  \"command = mine\",\n  \"args = mine\",\n]\n",
		},
		{
			name: "comments and blank lines below the block are kept",
			tail: "\n# why we raised this\nstartup_timeout_sec = 90\n",
			want: canonical + "\n# why we raised this\n",
		},
		{
			name: "the next table owns its own keys, including ones we call ours",
			tail: "startup_timeout_sec = 60\n\n[mcp_servers.other]\ncommand = \"/other\"\nargs = []\nstartup_timeout_sec = 5\n",
			want: canonical + "\n[mcp_servers.other]\ncommand = \"/other\"\nargs = []\nstartup_timeout_sec = 5\n",
		},
		{
			name: "an unterminated value means the file is already broken: keep every byte",
			tail: "args = [\n  \"mcp\",\n",
			want: canonical + "args = [\n  \"mcp\",\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			writeCodex(t, path, canonical+tc.tail)

			state, _, err := InstallCodexMCPServer(path, straza)
			if err != nil || state != CodexMCPManaged {
				t.Fatalf("install = (%v, %v), want (managed, nil)", state, err)
			}
			if got := readCodex(t, path); got != tc.want {
				t.Fatalf("after re-install =\n%q\nwant\n%q", got, tc.want)
			}

			// Re-installing again must be a no-op: convergence, not a file that
			// keeps changing under an idempotent command.
			before := readCodex(t, path)
			if _, changed, err := InstallCodexMCPServer(path, straza); err != nil || changed {
				t.Errorf("second re-install = (%v, %v), want (false, nil)", changed, err)
			}
			if after := readCodex(t, path); after != before {
				t.Errorf("second re-install rewrote the file:\n%q", after)
			}
		})
	}
}

// TestCodexMCPTailStripKeepsOneKeyPerTable proves the point structurally: the
// straza table defines startup_timeout_sec exactly once, and the neighbouring
// table's identically named keys are untouched.
func TestCodexMCPTailStripKeepsOneKeyPerTable(t *testing.T) {
	setOSName(t, "linux")
	path := filepath.Join(t.TempDir(), "config.toml")
	pre := "model = \"gpt-5-codex\"\n\n" + codexWant("'/opt/straza'") +
		"startup_timeout_sec = 60\ncommand = '/hand/edited/straza'\n\n" +
		"[mcp_servers.other]\ncommand = \"/other\"\nstartup_timeout_sec = 5\n"
	writeCodex(t, path, pre)

	if _, _, err := InstallCodexMCPServer(path, "/opt/straza"); err != nil {
		t.Fatal(err)
	}
	got := readCodex(t, path)
	keys := assertCodexTOMLValid(t, got)

	if want := []string{"command", "args", "startup_timeout_sec"}; !equalStrings(keys["mcp_servers.straza"], want) {
		t.Errorf("[mcp_servers.straza] keys = %v, want %v:\n%s", keys["mcp_servers.straza"], want, got)
	}
	if want := []string{"command", "startup_timeout_sec"}; !equalStrings(keys["mcp_servers.other"], want) {
		t.Errorf("the operator's other server was edited: keys = %v, want %v:\n%s", keys["mcp_servers.other"], want, got)
	}
	if !strings.HasPrefix(got, "model = \"gpt-5-codex\"\n\n") {
		t.Errorf("content above the block must survive byte-for-byte:\n%s", got)
	}
	// Uninstall still takes back exactly the block; the strip is an install-time
	// convergence, not a licence to delete on the way out.
	if _, _, err := UninstallCodexMCPServer(path); err != nil {
		t.Fatal(err)
	}
	if left := readCodex(t, path); strings.Contains(left, "straza-managed") ||
		!strings.Contains(left, "[mcp_servers.other]") {
		t.Errorf("after uninstall:\n%s", left)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
