package agentguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestInstallClaudeCodeMergeSafe(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")

	// Pre-existing settings with an unrelated hook and a user preference.
	pre := `{
  "model": "opus",
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type":"command","command":"echo existing"}]}
    ]
  }
}`
	if err := os.WriteFile(settings, []byte(pre), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := InstallHooks("claude-code", settings, "/usr/local/bin/straza"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := readSettings(t, settings)

	// User preference preserved.
	if got["model"] != "opus" {
		t.Errorf("model preference lost: %v", got["model"])
	}
	hooks := got["hooks"].(map[string]any)
	// The pre-existing PreToolUse hook survives, Straza's is appended.
	pre2 := hooks["PreToolUse"].([]any)
	if len(pre2) != 2 {
		t.Fatalf("PreToolUse hooks = %d, want 2 (existing + straza)", len(pre2))
	}
	// All seven Straza events wired: session start, pre-tool, the three
	// conversation-capture registrations (prompt submit, per-TURN Stop, the
	// reply-delta capture point; without it replies land only on a clean
	// session exit, which killed terminals and long-lived chats never do;
	// and session end, the final delta + spool drain) and the delegation pair
	// (SubagentStop is the delegate's capture point; its reply lives in its
	// own transcript, which no other event ever names).
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd", "SubagentStart", "SubagentStop"} {
		if hooks[ev] == nil {
			t.Errorf("%s hook not installed", ev)
		}
	}

	// Idempotent: installing again does not duplicate.
	if err := InstallHooks("claude-code", settings, "/usr/local/bin/straza"); err != nil {
		t.Fatal(err)
	}
	got = readSettings(t, settings)
	hooks = got["hooks"].(map[string]any)
	if n := len(hooks["PreToolUse"].([]any)); n != 2 {
		t.Errorf("re-install duplicated hooks: PreToolUse = %d", n)
	}

	// Uninstall removes only Straza's entries.
	if err := UninstallHooks("claude-code", settings, "/usr/local/bin/straza"); err != nil {
		t.Fatal(err)
	}
	got = readSettings(t, settings)
	hooks = got["hooks"].(map[string]any)
	if n := len(hooks["PreToolUse"].([]any)); n != 1 {
		t.Errorf("uninstall left %d PreToolUse hooks, want 1 (the pre-existing one)", n)
	}
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd", "SubagentStart", "SubagentStop"} {
		if hooks[ev] != nil {
			t.Errorf("uninstall did not remove %s", ev)
		}
	}
}

// TestInstallHooksRewritesMovedBinary pins the install half of the ghost
// check: straza gets staged, renamed and re-copied between installs, and a
// merge-safe writer that APPENDs the new command beside the old one leaves a
// registration that points at a path with nothing on it. The harness would
// then run a dead command on every governed event, and re-running the
// installer would not clean it up. Straza's own entries are rewritten rather
// than accumulated, as InstallCodexHooks and the codex MCP block do.
func TestInstallHooksRewritesMovedBinary(t *testing.T) {
	setOSName(t, "linux")
	settings := filepath.Join(t.TempDir(), "settings.json")
	pre := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo existing"}]}]}}`
	if err := os.WriteFile(settings, []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallHooks("claude-code", settings, "/opt/old/straza"); err != nil {
		t.Fatal(err)
	}
	if err := InstallHooks("claude-code", settings, "/opt/new/straza"); err != nil {
		t.Fatal(err)
	}

	hooks, ok := readSettings(t, settings)["hooks"].(map[string]any)
	if !ok {
		t.Fatal("hooks table missing")
	}
	for _, event := range installs["claude-code"].allEvents() {
		entries, ok := hooks[event].([]any)
		if !ok {
			t.Fatalf("%s: no entries", event)
		}
		straza := 0
		for _, item := range entries {
			if hookHasCommand(item, "/opt/new/straza hook --harness claude-code") {
				straza++
			}
			if hookHasCommand(item, "/opt/old/straza hook --harness claude-code") {
				t.Errorf("%s still invokes the old binary path", event)
			}
		}
		if straza != 1 {
			t.Errorf("%s has %d straza entries, want exactly 1", event, straza)
		}
	}
	// The user's own hook is not ours to rewrite.
	if n := len(hooks["PreToolUse"].([]any)); n != 2 {
		t.Errorf("PreToolUse = %d entries, want 2 (the user's + exactly one straza)", n)
	}
	if !hookHasCommand(hooks["PreToolUse"].([]any)[0], "echo existing") {
		t.Error("the user's own PreToolUse hook is gone (or reordered)")
	}
}

// handMergedSettings writes a settings file where an operator has put straza's
// hook and a foreign hook in ONE hooks[] group: not a shape our installer
// writes, but a shape a human legitimately hand-merges, and the file is theirs.
func handMergedSettings(t *testing.T, strazaCmd string) string {
	t.Helper()
	settings := filepath.Join(t.TempDir(), "settings.json")
	body := `{"hooks":{` +
		`"SessionStart":[{"hooks":[` +
		`{"type":"command","command":"` + strazaCmd + `"},` +
		`{"type":"command","command":"echo mine"}]}],` +
		`"Stop":[{"hooks":[{"type":"command","command":"` + strazaCmd + `"}]}]}}`
	if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return settings
}

// TestInstallHooksMovedBinarySparesForeignHooks: rewriting a ghost path must
// take back only what is ours. Straza's entries are pruned at the individual
// HOOK level, so a foreign command an operator merged into the same hooks[]
// group survives the rewrite; removing the whole group would delete a
// registration straza never wrote.
func TestInstallHooksMovedBinarySparesForeignHooks(t *testing.T) {
	setOSName(t, "linux")
	settings := handMergedSettings(t, "/opt/old/straza hook --harness claude-code")
	if err := InstallHooks("claude-code", settings, "/opt/new/straza"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(raw), "echo mine"); got != 1 {
		t.Errorf("the operator's own hook appears %d times, want 1:\n%s", got, raw)
	}
	if strings.Contains(string(raw), "/opt/old/straza") {
		t.Errorf("the moved binary's command survived the rewrite:\n%s", raw)
	}
	if got := strings.Count(string(raw), "/opt/new/straza hook --harness claude-code"); got != 7 {
		t.Errorf("straza command wired %d times, want one per roster event (7):\n%s", got, raw)
	}

	hooks, ok := readSettings(t, settings)["hooks"].(map[string]any)
	if !ok {
		t.Fatal("hooks table missing")
	}
	// The hand-merged group is still a group: emptied of ours, keeping theirs.
	shared, ok := hooks["SessionStart"].([]any)[0].(map[string]any)
	if !ok {
		t.Fatal("the hand-merged entry is gone")
	}
	inner, _ := shared["hooks"].([]any)
	if len(inner) != 1 || !hookHasCommand(shared, "echo mine") {
		t.Errorf("hand-merged entry = %v, want the operator's hook alone", shared)
	}
}

// TestUninstallSparesForeignHooksInSharedEntry: same contract on the way out.
// Uninstall removes straza's commands, never the group they were merged into
// when something that is not ours is still in it.
func TestUninstallSparesForeignHooksInSharedEntry(t *testing.T) {
	settings := handMergedSettings(t, "/opt/straza hook --harness claude-code")
	if err := UninstallHooks("claude-code", settings, "/opt/straza"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "straza") {
		t.Errorf("uninstall left straza wiring behind:\n%s", raw)
	}
	if got := strings.Count(string(raw), "echo mine"); got != 1 {
		t.Errorf("the operator's own hook appears %d times, want 1:\n%s", got, raw)
	}
	hooks, ok := readSettings(t, settings)["hooks"].(map[string]any)
	if !ok {
		t.Fatal("hooks table missing")
	}
	if _, still := hooks["Stop"]; still {
		t.Error("an event holding nothing but straza's hook must lose its key")
	}
	entries, ok := hooks["SessionStart"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("SessionStart = %v, want the operator's entry kept", hooks["SessionStart"])
	}
	if !hookHasCommand(entries[0], "echo mine") {
		t.Errorf("the operator's hook is gone: %v", entries[0])
	}
}

// TestUninstallAfterMoveLeavesNoGhost: uninstall matches the harness tail,
// not the exact command of the binary running it, so wiring written by a
// straza that has since moved does not survive as a dead command the harness
// keeps invoking with nothing left to fix it. The same match clears the
// double entry an older install left behind.
func TestUninstallAfterMoveLeavesNoGhost(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	// A file as an older install left it: two straza entries on one event,
	// one of them naming a binary that is long gone.
	pre := `{"hooks":{"SessionStart":[` +
		`{"hooks":[{"type":"command","command":"/opt/old/straza hook --harness claude-code"}]},` +
		`{"hooks":[{"type":"command","command":"/opt/new/straza hook --harness claude-code"}]}],` +
		`"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo existing"}]}]}}`
	if err := os.WriteFile(settings, []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UninstallHooks("claude-code", settings, "/opt/new/straza"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "straza") {
		t.Errorf("uninstall left straza wiring behind:\n%s", raw)
	}
	if !strings.Contains(string(raw), "echo existing") {
		t.Errorf("uninstall removed a hook that is not ours:\n%s", raw)
	}
}

func TestInstallCreatesFileWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "nested", "settings.json")
	if err := InstallHooks("claude-code", settings, "/opt/straza"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := readSettings(t, settings)
	if got["hooks"] == nil {
		t.Error("hooks not written")
	}
}

// TestInstallHookFileModesByScope pins the permissions of freshly created hook
// wiring, per scope. A managed writer with the user-scope 0600/0750 breaks a
// fleet silently in two ways at once: every OTHER user's harness cannot read
// the managed policy wiring (so it runs ungoverned while looking installed),
// and measureAttestation EPERMs on the file it must hash, reporting att=none
// everywhere. Managed artifacts are root-writable, world-readable; user-scope
// files stay private to their owner.
func TestInstallHookFileModesByScope(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	cases := []struct {
		name             string
		install          func(harness, path, self string) error
		wantDir, wantFil os.FileMode
	}{
		{"user scope stays private", InstallHooks, 0o750, 0o600},
		{"managed scope is world-readable", InstallManagedHooks, 0o755, 0o644},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "nested")
			settings := filepath.Join(dir, "settings.json")
			if err := tc.install("claude-code", settings, "/opt/straza"); err != nil {
				t.Fatalf("install: %v", err)
			}
			fi, err := os.Stat(settings)
			if err != nil {
				t.Fatal(err)
			}
			if got := fi.Mode().Perm(); got != tc.wantFil {
				t.Errorf("settings mode = %04o, want %04o", got, tc.wantFil)
			}
			di, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got := di.Mode().Perm(); got != tc.wantDir {
				t.Errorf("dir mode = %04o, want %04o", got, tc.wantDir)
			}
		})
	}
}

// TestInstallHooksPreservesExistingMode: a merge into a file that already
// exists must not restyle its permissions: the operator (or the harness
// vendor) chose them, and both scopes' writers defer to that choice, exactly
// like writeJSONMap.
func TestInstallHooksPreservesExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	settings := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(settings, []byte(`{"model":"opus"}`), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := InstallManagedHooks("claude-code", settings, "/opt/straza"); err != nil {
		t.Fatalf("install: %v", err)
	}
	fi, err := os.Stat(settings)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o640 {
		t.Errorf("mode = %04o, want 0640 (the operator's own choice, kept)", got)
	}
	if readSettings(t, settings)["hooks"] == nil {
		t.Error("hooks not written")
	}
}
