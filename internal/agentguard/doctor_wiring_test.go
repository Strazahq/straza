package agentguard

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeSettings renders a harness settings file wiring the Straza hook command
// to exactly the named events.
func writeSettings(t *testing.T, harness string, events ...string) string {
	t.Helper()
	body := `{"hooks":{`
	for i, ev := range events {
		if i > 0 {
			body += ","
		}
		body += `"` + ev + `":[{"hooks":[{"type":"command","command":"/usr/local/bin/straza hook --harness ` + harness + `"}]}]`
	}
	body += `}}`
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestMissingHookEvents pins the partial-roster case: a settings file written
// by an older installer mentions straza, so the roster calls it wired, while
// the events added to the roster later never fire. Conversation capture rides
// exactly those events.
func TestMissingHookEvents(t *testing.T) {
	full := []string{"SessionStart", "PreToolUse", "UserPromptSubmit", "Stop", "SessionEnd", "SubagentStart", "SubagentStop"}

	tests := []struct {
		name    string
		harness string
		wired   []string
		want    []string
	}{
		{
			name:    "current installer wires everything",
			harness: "claude-code",
			wired:   full,
			want:    nil,
		},
		{
			name:    "pre-capture install: governance on, capture silently dead",
			harness: "claude-code",
			wired:   []string{"SessionStart", "PreToolUse"},
			want:    []string{"UserPromptSubmit", "Stop", "SessionEnd", "SubagentStart", "SubagentStop"},
		},
		{
			name:    "an older install: replies only on clean exit",
			harness: "claude-code",
			wired:   []string{"SessionStart", "PreToolUse", "UserPromptSubmit", "SessionEnd", "SubagentStart", "SubagentStop"},
			want:    []string{"Stop"},
		},
		{
			// An install from before the roster gained the delegation pair
			// keeps working but never hears a delegate finish, so subagent
			// replies are silently uncaptured, exactly the partial state this
			// check names.
			name:    "pre-subagent-capture install is partial",
			harness: "claude-code",
			wired:   []string{"SessionStart", "PreToolUse", "UserPromptSubmit", "Stop", "SessionEnd"},
			want:    []string{"SubagentStart", "SubagentStop"},
		},
		{
			// Codex gained SessionEnd in a later release, so an install that
			// predates it in the roster is correctly reported partial.
			name:    "pre-SessionEnd codex install is partial",
			harness: "codex",
			wired:   []string{"SessionStart", "PreToolUse", "UserPromptSubmit", "Stop", "SubagentStart", "SubagentStop"},
			want:    []string{"SessionEnd"},
		},
		{
			name:    "current codex installer wires everything",
			harness: "codex",
			wired:   []string{"SessionStart", "PreToolUse", "UserPromptSubmit", "Stop", "SessionEnd", "SubagentStart", "SubagentStop"},
			want:    nil,
		},
		{
			name:    "nothing wired at all",
			harness: "claude-code",
			wired:   nil,
			want:    full,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, verified := missingHookEvents(writeSettings(t, tc.harness, tc.wired...), tc.harness)
			if !verified {
				t.Fatal("a readable JSON settings file must yield a verified answer")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("missing = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("missing[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestCodexManagedEventCoverage pins event coverage on the codex managed lane
// (requirements.toml): an install predating an event's addition must not read
// as COMPLETE. The gap check reads the same narrow TOML reader the hook-binary
// check uses, with the opposite fail-safe: where the reader refuses to manufacture a registration
// from a line it cannot parse, the gap check refuses to manufacture a clean
// bill from a file it cannot read.
func TestCodexManagedEventCoverage(t *testing.T) {
	seed := func(t *testing.T) string {
		t.Helper()
		t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", t.TempDir())
		t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
		t.Setenv("CODEX_HOME", t.TempDir())
		t.Setenv("STRAZA_GEMINI_CONFIG_DIR", t.TempDir())
		path, err := ManagedSettingsPath("codex")
		if err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("complete roster: no warning", func(t *testing.T) {
		path := seed(t)
		// The real installer writes the block, so this case tracks whatever
		// the roster grows into rather than a hand-copied snapshot of it.
		if _, err := InstallCodexManagedHooks(path, fakeStraza(t, t.TempDir(), "straza")); err != nil {
			t.Fatal(err)
		}
		gaps, verified := missingHookEvents(path, "codex")
		if !verified || gaps != nil {
			t.Fatalf("full managed roster: gaps = %v, verified = %v; want none, true", gaps, verified)
		}
		if c := wiringCheck(); c.Status != checkOK || !strings.Contains(c.Detail, "codex (managed)") {
			t.Errorf("wiring = %+v, want ok naming codex (managed)", c)
		}
	})

	t.Run("missing event: named warning with the managed reinstall fix", func(t *testing.T) {
		path := seed(t)
		if _, err := InstallCodexManagedHooks(path, fakeStraza(t, t.TempDir(), "straza")); err != nil {
			t.Fatal(err)
		}
		// Simulate an install that predates SessionEnd by renaming its tables
		// out of the roster; everything else the installer wrote stays.
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path,
			[]byte(strings.ReplaceAll(string(raw), "SessionEnd", "SomeRetiredEvent")), 0o600); err != nil {
			t.Fatal(err)
		}
		gaps, verified := missingHookEvents(path, "codex")
		if !verified || len(gaps) != 1 || gaps[0] != "SessionEnd" {
			t.Fatalf("gaps = %v, verified = %v; want exactly [SessionEnd], true", gaps, verified)
		}
		c := wiringCheck()
		if c.Status != checkWarn || !strings.Contains(c.Detail, "codex (managed) is missing SessionEnd") {
			t.Fatalf("wiring = %+v, want a warn naming the codex managed gap", c)
		}
		if !strings.Contains(c.Hint, "`sudo straza install --managed --server <server-url> codex`") {
			t.Errorf("hint = %q, want the managed reinstall fix", c.Hint)
		}
	})

	t.Run("mangled block: cannot-verify warning, never a clean bill", func(t *testing.T) {
		path := seed(t)
		// Mentions straza (so the roster reads it as wired) but registers its
		// command as a basic double-quoted string, the shape the narrow
		// reader refuses to half-unescape, standing in for every block it
		// cannot parse.
		body := "[features]\nhooks = true\n\n[[hooks.PreToolUse]]\n[[hooks.PreToolUse.hooks]]\n" +
			"type = \"command\"\ncommand = \"C:\\\\straza\\\\straza.exe hook --harness codex\"\n"
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		gaps, verified := missingHookEvents(path, "codex")
		if verified || gaps != nil {
			t.Fatalf("gaps = %v, verified = %v; a block the reader cannot parse must verify nothing", gaps, verified)
		}
		c := wiringCheck()
		if c.Status != checkWarn || !strings.Contains(c.Detail, "cannot verify") {
			t.Fatalf("wiring = %+v, want a cannot-verify warn", c)
		}
		if !strings.Contains(c.Hint, "`sudo straza install --managed --server <server-url> codex`") {
			t.Errorf("hint = %q, want the managed reinstall fix", c.Hint)
		}
		if strings.Contains(c.Detail, "is missing") {
			t.Errorf("detail = %q: an unparseable file must not claim specific gaps", c.Detail)
		}
	})

	t.Run("unreadable file: coverage unknown, never complete", func(t *testing.T) {
		gaps, verified := missingHookEvents(filepath.Join(t.TempDir(), "requirements.toml"), "codex")
		if verified || gaps != nil {
			t.Fatalf("gaps = %v, verified = %v; an unreadable file must verify nothing", gaps, verified)
		}
	})
}

// TestRegisteredHookEventsMatching pins what counts as registered: the harness
// tail, not the binary path (a moved straza is still wired), and never another
// harness's command sharing the same settings file.
func TestRegisteredHookEventsMatching(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	body := `{"hooks":{
	  "SessionStart":[{"hooks":[{"type":"command","command":"D:\\tools\\straza.exe hook --harness claude-code"}]}],
	  "PreToolUse":[{"hooks":[{"type":"command","command":"/opt/straza hook --harness codex"}]}],
	  "Stop":[{"hooks":[{"type":"command","command":"/usr/bin/some-other-tool --quiet"}]}]
	}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got := registeredHookEvents(path, "claude-code")
	if !got["SessionStart"] {
		t.Error("a relocated straza binary should still count as registered")
	}
	if got["PreToolUse"] {
		t.Error("another harness's command must not count for claude-code")
	}
	if got["Stop"] {
		t.Error("an unrelated tool's hook must not count as Straza wiring")
	}
}

// TestRegisteredHookEventsUnreadable pins the fail-soft contract: doctor
// reports, it never panics on a missing or malformed settings file.
func TestRegisteredHookEventsUnreadable(t *testing.T) {
	dir := t.TempDir()
	if got := registeredHookEvents(filepath.Join(dir, "absent.json"), "claude-code"); len(got) != 0 {
		t.Errorf("missing file = %v, want empty", got)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"hooks": not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := registeredHookEvents(bad, "claude-code"); len(got) != 0 {
		t.Errorf("malformed file = %v, want empty", got)
	}
	if got, _ := missingHookEvents(bad, "no-such-harness"); got != nil {
		t.Errorf("unknown harness = %v, want nil", got)
	}
}

// fakeStraza writes an executable stand-in for the straza binary under name
// and returns its path.
func fakeStraza(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeHookCommand wires one Straza hook command into a harness hook file, in
// the file's own dialect (JSON for every user-scope file and the claude-code /
// gemini managed files; TOML for codex's managed requirements.toml).
func writeHookCommand(t *testing.T, path, harness, command string) {
	t.Helper()
	body := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":` + strconv.Quote(command) + `}]}]}}`
	if filepath.Ext(path) == ".toml" {
		body = "[features]\nhooks = true\n\n[[hooks.SessionStart]]\n[[hooks.SessionStart.hooks]]\n" +
			"type = \"command\"\ncommand = '" + command + "'\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestHookBinaryCheck pins the moved-binary ghost check. Hook registrations
// carry an ABSOLUTE path, and nothing rewrites it when straza is moved or
// renamed after install. The harness then runs nothing, or errors, on every
// governed event while every other check reads healthy, because the
// registration is right there in the file and names the right events.
// Doctor resolves the command each registration points at.
func TestHookBinaryCheck(t *testing.T) {
	tests := []struct {
		name string
		// wire returns the harness, the file to write, and the command; bin is
		// resolved against a per-case temp dir.
		wire       func(t *testing.T, dir string) (path, command string)
		wantNil    bool
		wantStatus string
		wantDetail string
		wantHint   string
	}{
		{
			name: "a live binary is verified",
			wire: func(t *testing.T, dir string) (string, string) {
				p, err := SettingsPath("claude-code")
				if err != nil {
					t.Fatal(err)
				}
				return p, fakeStraza(t, dir, "straza") + " hook --harness claude-code"
			},
			wantStatus: checkOK,
			wantDetail: "claude-code (user)",
		},
		{
			name: "a moved binary is a loud fail with the reinstall fix",
			wire: func(t *testing.T, dir string) (string, string) {
				p, err := SettingsPath("claude-code")
				if err != nil {
					t.Fatal(err)
				}
				return p, filepath.Join(dir, "gone", "straza") + " hook --harness claude-code"
			},
			wantStatus: checkFail,
			wantDetail: "not there",
			wantHint:   "`straza install claude-code`",
		},
		{
			name: "a managed ghost names the managed fix",
			wire: func(t *testing.T, dir string) (string, string) {
				p, err := ManagedSettingsPath("gemini")
				if err != nil {
					t.Fatal(err)
				}
				return p, filepath.Join(dir, "gone", "straza") + " hook --harness gemini"
			},
			wantStatus: checkFail,
			wantDetail: "gemini (managed)",
			wantHint:   "`sudo straza install --managed --server <server-url> gemini`",
		},
		{
			// codex's managed lane is requirements.toml (TOML), which no JSON
			// reader in doctor can see into. A ghost there is just as dead.
			name: "codex's managed requirements.toml is read too",
			wire: func(t *testing.T, dir string) (string, string) {
				p, err := ManagedSettingsPath("codex")
				if err != nil {
					t.Fatal(err)
				}
				return p, filepath.Join(dir, "gone", "straza") + " hook --harness codex"
			},
			wantStatus: checkFail,
			wantDetail: "codex (managed)",
			wantHint:   "`sudo straza install --managed --server <server-url> codex`",
		},
		{
			// A bare program name is resolved by the HARNESS against ITS PATH,
			// which doctor cannot see. Not found here is "cannot verify", not
			// "broken": guessing either way would be the lie this check exists
			// to remove.
			name: "a bare name off this PATH cannot be verified",
			wire: func(t *testing.T, _ string) (string, string) {
				p, err := SettingsPath("claude-code")
				if err != nil {
					t.Fatal(err)
				}
				return p, "straza hook --harness claude-code"
			},
			wantStatus: checkWarn,
			wantDetail: "PATH",
		},
		{
			// A wrapper's first token is not straza and its path-with-a-space
			// twin is indistinguishable from here. Reporting either as a dead
			// install would be a fabricated failure, so the ambiguous split
			// warns and says what it could not read.
			name: "a wrapper command is not called a ghost",
			wire: func(t *testing.T, dir string) (string, string) {
				p, err := SettingsPath("claude-code")
				if err != nil {
					t.Fatal(err)
				}
				return p, "/bin/sh -c " + fakeStraza(t, dir, "straza") + " hook --harness claude-code"
			},
			wantStatus: checkWarn,
			wantDetail: "cannot read as a single path",
		},
		{
			name: "an existing file that is not straza is not verification",
			wire: func(t *testing.T, dir string) (string, string) {
				p, err := SettingsPath("claude-code")
				if err != nil {
					t.Fatal(err)
				}
				return p, fakeStraza(t, dir, "corp-wrapper.sh") + " hook --harness claude-code"
			},
			wantStatus: checkWarn,
			wantDetail: "not a straza binary",
		},
		{
			name:    "nothing wired: no line at all",
			wire:    func(*testing.T, string) (string, string) { return "", "" },
			wantNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", filepath.Join(dir, "empty-path")) // no straza anywhere
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			t.Setenv("CODEX_HOME", t.TempDir())
			t.Setenv("STRAZA_GEMINI_CONFIG_DIR", t.TempDir())
			t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", t.TempDir())

			if path, command := tc.wire(t, dir); path != "" {
				writeHookCommand(t, path, harnessOf(t, command), command)
			}
			got := hookBinaryCheck()
			if tc.wantNil {
				if got != nil {
					t.Fatalf("want no check, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("want a check, got none")
			}
			if got.Name != "hook-binary" {
				t.Errorf("name = %q, want hook-binary", got.Name)
			}
			if got.Status != tc.wantStatus || !strings.Contains(got.Detail, tc.wantDetail) {
				t.Fatalf("check = %+v, want %s mentioning %q", got, tc.wantStatus, tc.wantDetail)
			}
			if got.Status != checkOK && got.Hint == "" {
				t.Error("a non-ok check without a next step is noise")
			}
			if tc.wantHint != "" && !strings.Contains(got.Hint, tc.wantHint) {
				t.Errorf("hint = %q, want it to name %s", got.Hint, tc.wantHint)
			}
			for _, cmd := range installCommandsIn(got.Hint) {
				if !namesAHarness(cmd) {
					t.Errorf("hint prints `%s`, which no longer parses", cmd)
				}
			}
		})
	}
}

// harnessOf pulls the harness name out of a Straza hook command.
func harnessOf(t *testing.T, command string) string {
	t.Helper()
	_, harness, ok := strings.Cut(command, " hook --harness ")
	if !ok {
		t.Fatalf("not a straza hook command: %q", command)
	}
	return harness
}

// TestHookBinaryCheckKeepsWiringRoster: the ghost is reported on its own
// line, so the roster line keeps saying which harnesses are registered. An
// operator needs both facts, and losing the roster to a path failure would
// trade one blind spot for another.
func TestHookBinaryCheckKeepsWiringRoster(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("STRAZA_GEMINI_CONFIG_DIR", t.TempDir())
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", t.TempDir())

	settings, err := SettingsPath("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	ghost := filepath.Join(dir, "gone", "straza")
	if err := InstallHooks("claude-code", settings, ghost); err != nil {
		t.Fatal(err)
	}
	if c := wiringCheck(); c.Status != checkOK || !strings.Contains(c.Detail, "claude-code (user)") {
		t.Errorf("wiring check = %+v, want the roster line unchanged", c)
	}
	c := hookBinaryCheck()
	if c == nil || c.Status != checkFail || !strings.Contains(c.Detail, ghost) {
		t.Fatalf("hook-binary check = %+v, want a fail naming %s", c, ghost)
	}
	// One line per distinct binary, not one per event: seven registrations
	// pointing at the same dead path are one fact.
	if n := strings.Count(c.Detail, ghost); n != 1 {
		t.Errorf("detail names the binary %d times, want 1:\n%s", n, c.Detail)
	}
}

// TestWiredLayerReadsWhatInstallWrites pins the path convergence between
// install and doctor: doctor resolves codex's managed file through
// CodexManagedRequirementsPath, the SAME function install writes through, so
// the two cannot disagree. On Windows that function honors a relocated
// %ProgramData%, which ManagedSettingsPath does not, so a second resolver
// would hide the only live lane.
func TestWiredLayerReadsWhatInstallWrites(t *testing.T) {
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("STRAZA_GEMINI_CONFIG_DIR", t.TempDir())

	path, err := CodexManagedRequirementsPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCodexManagedHooks(path, fakeStraza(t, t.TempDir(), "straza")); err != nil {
		t.Fatal(err)
	}
	layer, got := wiredLayer("codex")
	if layer != "managed" || got != path {
		t.Fatalf("wiredLayer(codex) = (%q, %q), want (managed, %q): doctor must read the file install writes", layer, got, path)
	}
}

// TestCodexBothLanesVisible pins the union view: codex runs hooks from BOTH its
// managed and user sources, so a dead user lane beside a healthy managed one is
// a real finding: a first-match reader would let managed win and leave the
// user lane's ghost unreported. Coverage runs the other way: an event is dead
// only when NO lane registers it, so a user file predating an event stays
// silent while the managed lane carries the roster.
func TestCodexBothLanesVisible(t *testing.T) {
	full := []string{"SessionStart", "PreToolUse", "UserPromptSubmit", "Stop", "SessionEnd", "SubagentStart", "SubagentStop"}
	writeUserHooks := func(t *testing.T, path, command string, events ...string) {
		t.Helper()
		body := `{"hooks":{`
		for i, ev := range events {
			if i > 0 {
				body += ","
			}
			body += `"` + ev + `":[{"hooks":[{"type":"command","command":` + strconv.Quote(command) + `}]}]`
		}
		body += `}}`
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("STRAZA_GEMINI_CONFIG_DIR", t.TempDir())

	managedPath, err := CodexManagedRequirementsPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCodexManagedHooks(managedPath, fakeStraza(t, t.TempDir(), "straza")); err != nil {
		t.Fatal(err)
	}
	userPath, err := SettingsPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	ghost := filepath.Join(t.TempDir(), "gone", "straza")
	writeUserHooks(t, userPath, ghost+" hook --harness codex", full...)

	c := wiringCheck()
	if c.Status != checkOK ||
		!strings.Contains(c.Detail, "codex (managed)") ||
		!strings.Contains(c.Detail, "codex (user; trust gate, see codex-hooks)") {
		t.Fatalf("wiring = %+v, want ok listing both codex lanes", c)
	}

	hb := hookBinaryCheck()
	if hb == nil || hb.Status != checkFail ||
		!strings.Contains(hb.Detail, "codex (user) invokes "+ghost) {
		t.Fatalf("hook-binary = %+v, want a fail naming the user-lane ghost %s", hb, ghost)
	}
	if !strings.Contains(hb.Hint, "`straza install codex`") {
		t.Errorf("hint = %q, want the user-scope reinstall fix", hb.Hint)
	}

	// A user file predating most of the roster is NOT a coverage gap while the
	// managed lane carries every event; warning here would tell an operator
	// to "fix" a box whose features all work.
	writeUserHooks(t, userPath, ghost+" hook --harness codex", "SessionStart")
	if c := wiringCheck(); c.Status != checkOK {
		t.Fatalf("partial user lane beside a complete managed lane = %+v, want ok (union coverage)", c)
	}
}

// installCommandsIn returns every backtick-quoted `straza install …` command
// embedded in a user-facing string (an optional `sudo ` prefix is stripped).
func installCommandsIn(s string) []string {
	var out []string
	// Odd-indexed segments of a backtick split are the quoted commands.
	parts := strings.Split(s, "`")
	for i := 1; i < len(parts); i += 2 {
		cmd := strings.TrimPrefix(parts[i], "sudo ")
		if strings.HasPrefix(cmd, "straza install") {
			out = append(out, cmd)
		}
	}
	return out
}

// namesAHarness reports whether cmd carries the operand `straza install`
// requires: a positional harness name (or a documented placeholder), with
// flags allowed anywhere. It mirrors harnessArgs' contract, not its code.
func namesAHarness(cmd string) bool {
	takesValue := false
	for _, f := range strings.Fields(cmd)[2:] { // past "straza install"
		if takesValue {
			takesValue = false
			continue // the value of --server or --bin-dir
		}
		if strings.HasPrefix(f, "-") {
			takesValue = f == "--server" || f == "--bin-dir"
			continue // a flag, e.g. --managed
		}
		if f == "<harness>" || f == "<name>" {
			return true
		}
		_, known := installs[f]
		return known
	}
	return false
}

// TestInstallHintsUsePositionalSyntax pins that every hint telling someone to
// run `straza install` names a harness operand, because a hint in a form that
// does not parse hands a blocked user a command that errors. The rule is
// checked, not the wording, so the NEXT syntax change breaks a test here
// instead of in a customer's terminal.
func TestInstallHintsUsePositionalSyntax(t *testing.T) {
	t.Run("deny-hint catalog", func(t *testing.T) {
		checked := 0
		for _, h := range denyHints {
			for _, cmd := range installCommandsIn(h.Hint) {
				checked++
				if !namesAHarness(cmd) {
					t.Errorf("hint for %q prints `%s`, which no longer parses: install takes a positional harness", h.Reason, cmd)
				}
			}
		}
		if checked == 0 {
			t.Fatal("no `straza install …` command found in denyHints: the tripwire is not watching anything")
		}
		// The attestation row is the one a blocked user actually meets (the
		// server's 403 reason routes here); pin it exactly.
		const want = "reinstall with `straza install --managed --server <server-url> <harness>` so the wiring matches what the server published"
		if got := HintFor(`attestation level "advisory" is below the required level "managed"`); got != want {
			t.Errorf("attestation hint = %q, want %q", got, want)
		}
	})

	t.Run("wiring check", func(t *testing.T) {
		managed := t.TempDir()
		t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", managed)
		t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
		t.Setenv("CODEX_HOME", t.TempDir())
		t.Setenv("STRAZA_GEMINI_CONFIG_DIR", t.TempDir())

		// Nothing wired anywhere.
		got := wiringCheck()
		for _, cmd := range installCommandsIn(got.Hint) {
			if !namesAHarness(cmd) {
				t.Errorf("unwired hint prints `%s`, which no longer parses", cmd)
			}
		}
		if !strings.Contains(got.Hint, "`straza install --managed --server <server-url> claude-code`") || strings.Contains(got.Hint, "<harness>") {
			t.Errorf("unwired hint = %q, want the enterprise line to name a real harness", got.Hint)
		}

		// Straza-authored wiring left at two harnesses' RETIRED managed paths:
		// the hint must name both, which is also the multi-operand form the
		// CLI accepts (`straza install --managed --server <server-url> claude-code codex`).
		for _, harness := range []string{"claude-code", "codex"} {
			legacy := LegacyManagedSettingsPath(harness)
			if err := os.MkdirAll(filepath.Dir(legacy), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(legacy, []byte(`{"hooks":{"PreToolUse":[{"command":"straza hook"}]}}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		got = wiringCheck()
		if !strings.Contains(got.Detail, "retired managed wiring") {
			t.Fatalf("stale wiring not detected: %+v", got)
		}
		for _, cmd := range installCommandsIn(got.Hint) {
			if !namesAHarness(cmd) {
				t.Errorf("stale-wiring hint prints `%s`, which no longer parses", cmd)
			}
		}
		if !strings.Contains(got.Hint, "`straza install --managed --server <server-url> claude-code codex`") {
			t.Errorf("stale-wiring hint = %q, want it to name both stale harnesses as operands", got.Hint)
		}
	})
}
