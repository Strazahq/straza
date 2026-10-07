package agentguard

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// codexHooksWant renders hooks.json EXACTLY as it must appear on disk for a
// fresh install. It is spelled out rather than generated from the code under
// test on purpose: a test that asserts the bytes straza wrote into a filename
// straza chose stays green even when the harness reads none of it. This
// literal is the contract
// with codex's parser: top-level `description` + `hooks` and nothing else
// (HooksFile is deny_unknown_fields: an extra key makes codex skip the whole
// file), the nested {hooks:[{type,command}]} entry shape, "type":"command" (the
// serde tag), NO `matcher` (codex matchers are regexes; "*" is not one, and an
// absent matcher matches every tool), and the explicit SessionEnd timeout
// (codex defaults SessionEnd to 1s, max 3, and that is where the spool drains).
func codexHooksWant(command string) string {
	entry := func(event string, extra string) string {
		return `    "` + event + `": [
      {
        "hooks": [
          {
            "command": "` + command + `",` + extra + `
            "type": "command"
          }
        ]
      }
    ]`
	}
	events := []string{
		entry("PreToolUse", ""),
		entry("SessionEnd", "\n            \"timeout\": 3,"),
		entry("SessionStart", ""),
		entry("Stop", ""),
		entry("SubagentStart", ""),
		entry("SubagentStop", ""),
		entry("UserPromptSubmit", ""),
	}
	return `{
  "description": "Straza governance hooks (straza install codex)",
  "hooks": {
` + strings.Join(events, ",\n") + `
  }
}
`
}

const testStraza = "/usr/local/bin/straza"

func readFileString(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestInstallCodexHooksExactBytes pins the whole file, byte for byte.
func TestInstallCodexHooksExactBytes(t *testing.T) {
	setOSName(t, "linux")
	path := filepath.Join(t.TempDir(), codexHooksFile)
	changed, err := InstallCodexHooks(path, testStraza)
	if err != nil || !changed {
		t.Fatalf("InstallCodexHooks = (%v, %v), want (true, nil)", changed, err)
	}
	want := codexHooksWant(testStraza + " hook --harness codex")
	if got := readFileString(t, path); got != want {
		t.Errorf("hooks.json =\n%s\nwant\n%s", got, want)
	}
}

// TestInstallCodexHooksNoMatcherKey is the same fact stated so it cannot be
// lost in a diff of the big literal: writing claude-code's "*" glob into a
// codex matcher would hand the harness an invalid regex on the ONE event that
// blocks.
func TestInstallCodexHooksNoMatcherKey(t *testing.T) {
	setOSName(t, "linux")
	path := filepath.Join(t.TempDir(), codexHooksFile)
	if _, err := InstallCodexHooks(path, testStraza); err != nil {
		t.Fatal(err)
	}
	if got := readFileString(t, path); strings.Contains(got, "matcher") {
		t.Errorf("hooks.json carries a matcher key; codex matchers are regexes, not globs:\n%s", got)
	}
}

// TestCodexUserHookCommandPolicy pins the user-lane command policy (codex
// 0.146 kills a double-quoted exe head at spawn):
// space-free paths are UNQUOTED on every OS; a path with whitespace keeps the
// pre-0.146 quoted form (the only spelling older tokenizers accepted, since
// >= 0.146 has no working string form for it at all) and carries a warning
// the installer surfaces.
func TestCodexUserHookCommandPolicy(t *testing.T) {
	cases := []struct {
		name, path, wantCmd string
		wantWarn            bool
	}{
		{"unix", "/usr/local/bin/straza", "/usr/local/bin/straza hook --harness codex", false},
		{"windows", `C:\Users\alice\straza.exe`, `C:\Users\alice\straza.exe hook --harness codex`, false},
		{"spaced", `C:\Program Files\straza\straza.exe`, `"C:\Program Files\straza\straza.exe" hook --harness codex`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, warning := codexHookCommand(tc.path)
			if cmd != tc.wantCmd {
				t.Errorf("codexHookCommand(%q) = %q, want %q", tc.path, cmd, tc.wantCmd)
			}
			if (warning != "") != tc.wantWarn {
				t.Errorf("warning = %q, wantWarn = %v", warning, tc.wantWarn)
			}
			if tc.wantWarn && !strings.Contains(warning, "0.146") {
				t.Errorf("the warning must name the codex version that broke quoting: %q", warning)
			}
		})
	}
}

// TestInstallCodexHooksConvergesOldQuotedEntry: an entry written by an older
// installer (quoted exe) reads as ours via the stable command tail and is
// REPLACED with the new form; the trust hash changes, which is why install
// always prints the /hooks step.
func TestInstallCodexHooksConvergesOldQuotedEntry(t *testing.T) {
	setOSName(t, "linux")
	path := filepath.Join(t.TempDir(), codexHooksFile)
	pre := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"\"C:\\old\\straza.exe\" hook --harness codex"}]}]}}`
	if err := os.WriteFile(path, []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := InstallCodexHooks(path, testStraza)
	if err != nil || !changed {
		t.Fatalf("InstallCodexHooks = (%v, %v), want (true, nil)", changed, err)
	}
	body := readFileString(t, path)
	if strings.Contains(body, "straza.exe") {
		t.Errorf("the old quoted entry survived:\n%s", body)
	}
	if got := strings.Count(body, testStraza+" hook --harness codex"); got != 7 {
		t.Errorf("want 7 new-form entries, got %d:\n%s", got, body)
	}
}

// TestInstallCodexHooksIdempotent: a second install must not duplicate entries
// and must not rewrite the file at all. Codex keys hook trust to the
// definition's hash, so needless churn is not cosmetic: it re-gates a hook
// the operator already trusted.
func TestInstallCodexHooksIdempotent(t *testing.T) {
	setOSName(t, "linux")
	path := filepath.Join(t.TempDir(), codexHooksFile)
	if _, err := InstallCodexHooks(path, testStraza); err != nil {
		t.Fatal(err)
	}
	first := readFileString(t, path)
	changed, err := InstallCodexHooks(path, testStraza)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("re-install reported a change; identical bytes must not be rewritten (trust is hashed)")
	}
	if got := readFileString(t, path); got != first {
		t.Errorf("re-install changed the file:\n%s", got)
	}
}

// TestInstallCodexHooksForeignContent: the file is the user's. Their hooks,
// their description, their other keys all survive, and a straza entry left by
// an install whose binary has since moved is REPLACED, not accumulated.
func TestInstallCodexHooksForeignContent(t *testing.T) {
	setOSName(t, "linux")
	path := filepath.Join(t.TempDir(), codexHooksFile)
	pre := `{
  "description": "my workspace hooks",
  "hooks": {
    "PreToolUse": [
      {"matcher": "^Bash$", "hooks": [{"type": "command", "command": "python3 ~/audit.py"}]},
      {"hooks": [{"type": "command", "command": "/opt/old/straza hook --harness codex"}]}
    ],
    "PostToolUse": [
      {"hooks": [{"type": "command", "command": "python3 ~/after.py"}]}
    ]
  }
}`
	if err := os.WriteFile(path, []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCodexHooks(path, testStraza); err != nil {
		t.Fatal(err)
	}
	got := readSettings(t, path)

	if got["description"] != "my workspace hooks" {
		t.Errorf("the user's description was overwritten: %v", got["description"])
	}
	hooks := got["hooks"].(map[string]any)
	if hooks["PostToolUse"] == nil {
		t.Error("an unrelated event's hooks were dropped")
	}
	pre2 := hooks["PreToolUse"].([]any)
	if len(pre2) != 2 {
		t.Fatalf("PreToolUse = %d entries, want 2 (the user's + exactly one straza)", len(pre2))
	}
	if !strings.Contains(readFileString(t, path), "python3 ~/audit.py") {
		t.Error("the user's own PreToolUse hook is gone")
	}
	if strings.Contains(readFileString(t, path), "/opt/old/straza") {
		t.Error("a straza entry naming a moved binary must be replaced, not kept beside the new one")
	}
}

// TestUninstallCodexHooks covers both halves of the reversal: our entries go,
// everything else stays, and a file that held nothing but our wiring is
// deleted rather than left as an empty husk.
func TestUninstallCodexHooks(t *testing.T) {
	setOSName(t, "linux")
	t.Run("removes only straza, keeps the file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), codexHooksFile)
		pre := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"python3 ~/audit.py"}]}]}}`
		if err := os.WriteFile(path, []byte(pre), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := InstallCodexHooks(path, testStraza); err != nil {
			t.Fatal(err)
		}
		changed, err := UninstallCodexHooks(path)
		if err != nil || !changed {
			t.Fatalf("UninstallCodexHooks = (%v, %v), want (true, nil)", changed, err)
		}
		body := readFileString(t, path)
		if strings.Contains(body, "straza") {
			t.Errorf("straza wiring survived uninstall:\n%s", body)
		}
		if !strings.Contains(body, "python3 ~/audit.py") {
			t.Errorf("the user's hook was removed too:\n%s", body)
		}
	})

	t.Run("deletes a file that was only ours", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), codexHooksFile)
		if _, err := InstallCodexHooks(path, testStraza); err != nil {
			t.Fatal(err)
		}
		if _, err := UninstallCodexHooks(path); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("hooks.json survived an uninstall that emptied it (%v)", err)
		}
	})

	t.Run("a moved binary is still removable", func(t *testing.T) {
		// The stale-entry case that made this a marker match rather than an
		// exact-command match: uninstall runs from a straza that is no longer
		// at the path the entry names.
		path := filepath.Join(t.TempDir(), codexHooksFile)
		if _, err := InstallCodexHooks(path, "/opt/old/straza"); err != nil {
			t.Fatal(err)
		}
		if _, err := UninstallCodexHooks(path); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Error("wiring written by a since-moved straza was left behind")
		}
	})

	t.Run("absent file is a no-op", func(t *testing.T) {
		changed, err := UninstallCodexHooks(filepath.Join(t.TempDir(), codexHooksFile))
		if changed || err != nil {
			t.Errorf("= (%v, %v), want (false, nil)", changed, err)
		}
	})
}

// TestMigrateCodexStaleHooks pins the cleanup of $CODEX_HOME/settings.json,
// which older straza versions wrote and no codex release reads.
func TestMigrateCodexStaleHooks(t *testing.T) {
	tests := []struct {
		name       string
		content    string // "" = do not create the file
		want       CodexStaleAction
		wantExists bool
		wantKeep   string // substring that must survive
	}{
		{
			name: "our wiring and nothing else: the file goes",
			content: `{"hooks":{"SessionStart":[{"hooks":[{"type":"command",` +
				`"command":"/usr/local/bin/straza hook --harness codex"}]}]}}`,
			want: CodexStaleRemoved,
		},
		{
			name: "our wiring written by a since-moved binary still goes",
			content: `{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command",` +
				`"command":"D:\\tools\\straza.exe hook --harness codex"}]}]}}`,
			want: CodexStaleRemoved,
		},
		{
			name: "our wiring beside the user's content: only ours goes",
			content: `{"model":"gpt-5","hooks":{"SessionStart":[{"hooks":[{"type":"command",` +
				`"command":"/usr/local/bin/straza hook --harness codex"}]}],` +
				`"PostToolUse":[{"hooks":[{"type":"command","command":"python3 ~/after.py"}]}]}}`,
			want:       CodexStaleCleaned,
			wantExists: true,
			wantKeep:   "python3 ~/after.py",
		},
		{
			name:       "somebody else's settings.json is never touched",
			content:    `{"model":"gpt-5","hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"python3 ~/audit.py"}]}]}}`,
			want:       CodexStaleNone,
			wantExists: true,
			wantKeep:   "python3 ~/audit.py",
		},
		{
			// Mentions straza (a model name, a comment, their own tooling)
			// but holds no wiring of ours. Rewriting it would reformat a file
			// we do not own and report a migration that never happened.
			name:       "mentions straza but has none of our wiring",
			content:    `{"model":"straza-tuned","hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"python3 ~/audit.py"}]}]}}`,
			want:       CodexStaleNone,
			wantExists: true,
			wantKeep:   "straza-tuned",
		},
		{
			name:    "no stale file at all",
			content: "",
			want:    CodexStaleNone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), codexStaleHooksFile)
			if tc.content != "" {
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := MigrateCodexStaleHooks(path)
			if err != nil {
				t.Fatalf("MigrateCodexStaleHooks: %v", err)
			}
			if got != tc.want {
				t.Errorf("action = %v, want %v", got, tc.want)
			}
			_, statErr := os.Stat(path)
			if exists := statErr == nil; exists != tc.wantExists {
				t.Errorf("file exists = %v, want %v", exists, tc.wantExists)
			}
			if tc.want == CodexStaleNone && tc.content != "" {
				if got := readFileString(t, path); got != tc.content {
					t.Errorf("a file with nothing of ours in it was rewritten:\n%s", got)
				}
			}
			if tc.wantKeep != "" {
				if body := readFileString(t, path); !strings.Contains(body, tc.wantKeep) {
					t.Errorf("lost content that was not ours: %q not in\n%s", tc.wantKeep, body)
				}
				if tc.want == CodexStaleCleaned && strings.Contains(readFileString(t, path), "straza") {
					t.Error("straza wiring survived the migration")
				}
			}
		})
	}

	t.Run("our file but unreadable: reported, not silently rewritten", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), codexStaleHooksFile)
		body := `{"hooks": straza but not json`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := MigrateCodexStaleHooks(path); err == nil {
			t.Error("want an error naming the file")
		}
		if got := readFileString(t, path); got != body {
			t.Errorf("the file was modified: %s", got)
		}
	})
}

// TestCodexLaneFileInventory is the in-tree contradiction check. A full
// user-scope codex install must leave EXACTLY the files
// codex reads (hooks.json plus the config.toml straza owns one block of), and
// settings.json must be gone even when an older straza left one behind. A
// future regression that reintroduces the dead file fails here, loudly, rather
// than passing a test that renames itself along with the bug.
func TestCodexLaneFileInventory(t *testing.T) {
	setOSName(t, "linux")
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)

	// An older straza's dead wiring, present before this install runs.
	stale, err := CodexStaleHooksPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallHooks("codex", stale, "/opt/old/straza"); err != nil {
		t.Fatal(err)
	}

	hooks, err := CodexHooksPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(hooks) != "hooks.json" {
		t.Fatalf("the codex user-scope hook file is %q; codex reads hooks.json", filepath.Base(hooks))
	}
	if _, err := InstallCodexHooks(hooks, testStraza); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateCodexStaleHooks(stale); err != nil {
		t.Fatal(err)
	}
	mcp, err := CodexMCPConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := InstallCodexMCPServer(mcp, testStraza); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(got)
	want := []string{"config.toml", "hooks.json"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("codex install left %v, want exactly %v (settings.json is the file no codex release reads)", got, want)
	}

	// And the managed lane's file is the requirements.toml codex reads, not a
	// managed-settings.json invented by analogy with claude-code.
	managed, err := CodexManagedRequirementsPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(managed) != "requirements.toml" {
		t.Errorf("codex managed file = %q, want requirements.toml", managed)
	}
}

// TestCodexHooksWritten pins doctor's input: which events a hooks.json
// registers, matched on the stable command tail so a moved binary still counts.
func TestCodexHooksWritten(t *testing.T) {
	setOSName(t, "linux")
	t.Run("absent file", func(t *testing.T) {
		wired, missing := CodexHooksWritten(filepath.Join(t.TempDir(), codexHooksFile))
		if wired || missing != nil {
			t.Errorf("= (%v, %v), want (false, nil)", wired, missing)
		}
	})
	t.Run("full install", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), codexHooksFile)
		if _, err := InstallCodexHooks(path, testStraza); err != nil {
			t.Fatal(err)
		}
		wired, missing := CodexHooksWritten(path)
		if !wired || len(missing) != 0 {
			t.Errorf("= (%v, %v), want (true, none missing)", wired, missing)
		}
	})
	t.Run("partial install from an older straza", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), codexHooksFile)
		body := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/moved/straza hook --harness codex"}]}]}}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		wired, missing := CodexHooksWritten(path)
		if !wired {
			t.Fatal("a relocated straza binary must still read as wired")
		}
		want := []string{"PreToolUse", "UserPromptSubmit", "Stop", "SessionEnd", "SubagentStart", "SubagentStop"}
		if strings.Join(missing, ",") != strings.Join(want, ",") {
			t.Errorf("missing = %v, want %v", missing, want)
		}
	})
}
