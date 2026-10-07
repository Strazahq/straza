package agentguard

import (
	"context"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// codexWant renders the managed block exactly as it must appear on disk. The
// marker and key text is spelled out here rather than reused from the
// constants: this is the contract with an operator's file (and with every
// block already written), so a change to it has to break a test.
func codexWant(command string) string {
	return "# BEGIN straza-managed (straza install writes this block; do not edit inside)\n" +
		"[mcp_servers.straza]\n" +
		"command = " + command + "\n" +
		`args = ["mcp", "--harness", "codex"]` + "\n" +
		"startup_timeout_sec = 60\n" +
		"# END straza-managed\n"
}

func readCodex(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func writeCodex(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCodexMCPConfigPath(t *testing.T) {
	t.Run("honors CODEX_HOME, beside the hook settings", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("CODEX_HOME", dir)
		got, err := CodexMCPConfigPath()
		if err != nil || got != filepath.Join(dir, "config.toml") {
			t.Fatalf("CodexMCPConfigPath = (%q, %v), want %s", got, err, filepath.Join(dir, "config.toml"))
		}
		settings, err := SettingsPath("codex")
		if err != nil || filepath.Dir(settings) != filepath.Dir(got) {
			t.Errorf("config.toml %q must sit beside settings %q (%v)", got, settings, err)
		}
	})
	t.Run("defaults to ~/.codex/config.toml", func(t *testing.T) {
		t.Setenv("CODEX_HOME", "")
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("no home directory on this machine")
		}
		got, err := CodexMCPConfigPath()
		if err != nil || got != filepath.Join(home, ".codex", "config.toml") {
			t.Errorf("CodexMCPConfigPath = (%q, %v), want %s", got, err, filepath.Join(home, ".codex", "config.toml"))
		}
	})
}

// TestCodexMCPConfigPathForInvoker pins the managed-mode resolution: codex has
// no system-scope MCP config, so `install --managed` (running as root) has to
// land in the config.toml of the user who invoked sudo, never root's own.
func TestCodexMCPConfigPathForInvoker(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip("no current user on this machine")
	}
	t.Run("CODEX_HOME still wins", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("CODEX_HOME", dir)
		t.Setenv("SUDO_USER", me.Username)
		got, err := CodexMCPConfigPathForInvoker()
		if err != nil || got != filepath.Join(dir, "config.toml") {
			t.Errorf("= (%q, %v), want the explicit CODEX_HOME %s", got, err, dir)
		}
	})
	t.Run("sudo resolves the invoking user's home", func(t *testing.T) {
		t.Setenv("CODEX_HOME", "")
		t.Setenv("SUDO_USER", me.Username)
		got, err := CodexMCPConfigPathForInvoker()
		want := filepath.Join(me.HomeDir, ".codex", "config.toml")
		if err != nil || got != want {
			t.Errorf("= (%q, %v), want %s", got, err, want)
		}
	})
	t.Run("no sudo, root, or an unresolvable user falls back", func(t *testing.T) {
		t.Setenv("CODEX_HOME", "")
		fallback, err := CodexMCPConfigPath()
		if err != nil {
			t.Skip("no home directory on this machine")
		}
		for _, sudoUser := range []string{"", "root", "no-such-user-6f2b"} {
			t.Setenv("SUDO_USER", sudoUser)
			got, err := CodexMCPConfigPathForInvoker()
			if err != nil || got != fallback {
				t.Errorf("SUDO_USER=%q → (%q, %v), want the current user's %s", sudoUser, got, err, fallback)
			}
		}
	})
}

// TestInstallCodexMCPServerFresh: install must WRITE the registration (file
// and directory included), not print it.
func TestInstallCodexMCPServerFresh(t *testing.T) {
	setOSName(t, "linux")
	path := filepath.Join(t.TempDir(), "nested", "config.toml")

	state, changed, err := InstallCodexMCPServer(path, "/opt/straza")
	if err != nil || state != CodexMCPManaged || !changed {
		t.Fatalf("install = (%v, %v, %v), want (managed, true, nil)", state, changed, err)
	}
	if got, want := readCodex(t, path), codexWant("'/opt/straza'"); got != want {
		t.Fatalf("fresh file =\n%q\nwant\n%q", got, want)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("fresh config perms = %v (%v), want 0600 like the hook settings", fi.Mode().Perm(), err)
		}
	}

	// Idempotent: a second install with the same path rewrites nothing.
	before := readCodex(t, path)
	state, changed, err = InstallCodexMCPServer(path, "/opt/straza")
	if err != nil || state != CodexMCPManaged || changed {
		t.Fatalf("re-install = (%v, %v, %v), want (managed, false, nil)", state, changed, err)
	}
	if after := readCodex(t, path); after != before {
		t.Errorf("re-install rewrote the file:\n%q", after)
	}

	got, command, err := ReadCodexMCPRegistration(path)
	if err != nil || got != CodexMCPManaged || command != "/opt/straza" {
		t.Errorf("read back = (%v, %q, %v), want (managed, /opt/straza, nil)", got, command, err)
	}
}

// TestInstallCodexMCPServerRewritesMovedBinary is the re-install case that made
// a marker block necessary: the exe path changes (staged build, new bin dir),
// and only the block may move with it.
func TestInstallCodexMCPServerRewritesMovedBinary(t *testing.T) {
	setOSName(t, "linux")
	path := filepath.Join(t.TempDir(), "config.toml")
	head := "model = \"gpt-5-codex\"\n\n[mcp_servers.other]\ncommand = \"/usr/bin/other\"\n"
	writeCodex(t, path, head)

	if _, _, err := InstallCodexMCPServer(path, `C:\Users\alice\straza.exe`); err != nil {
		t.Fatal(err)
	}
	first := readCodex(t, path)
	if want := head + "\n" + codexWant(`'C:\Users\alice\straza.exe'`); first != want {
		t.Fatalf("after install =\n%q\nwant\n%q", first, want)
	}

	state, changed, err := InstallCodexMCPServer(path, `D:\tools\straza.exe`)
	if err != nil || state != CodexMCPManaged || !changed {
		t.Fatalf("moved-binary install = (%v, %v, %v)", state, changed, err)
	}
	got := readCodex(t, path)
	if want := head + "\n" + codexWant(`'D:\tools\straza.exe'`); got != want {
		t.Fatalf("rewrite =\n%q\nwant\n%q", got, want)
	}
	if strings.Count(got, "# BEGIN straza-managed") != 1 {
		t.Errorf("re-install must rewrite the block, not add one:\n%s", got)
	}
	if !strings.HasPrefix(got, head) {
		t.Errorf("content before the block must survive byte-for-byte:\n%s", got)
	}
}

// TestCodexMCPWindowsPathRoundTrip pins the reason the command is a TOML
// LITERAL string: a Windows path with backslashes goes in and comes back out
// verbatim, with nothing escaped and nothing lost.
func TestCodexMCPWindowsPathRoundTrip(t *testing.T) {
	setOSName(t, "linux")
	tests := []struct {
		name    string
		exe     string
		want    string // the rendered TOML value
		wantErr bool
	}{
		{"windows exe", `C:\Users\alice\straza.exe`, `'C:\Users\alice\straza.exe'`, false},
		{"unix path", "/usr/local/bin/straza", "'/usr/local/bin/straza'", false},
		{"space in path", `C:\Program Files\straza\straza.exe`, `'C:\Program Files\straza\straza.exe'`, false},
		// A literal string cannot hold a single quote and TOML gives it no
		// escape, so an O'Brien-shaped Windows path falls back to a basic
		// string, where the backslashes DO have to be doubled.
		{"apostrophe in path", `C:\Users\O'Brien\straza.exe`, `"C:\\Users\\O'Brien\\straza.exe"`, false},
		{"control character is refused", "/opt/stra\nza", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			_, _, err := InstallCodexMCPServer(path, tc.exe)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want an error, got none")
				}
				if _, statErr := os.Stat(path); statErr == nil {
					t.Error("a refused path must not leave a file behind")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, want := readCodex(t, path), codexWant(tc.want); got != want {
				t.Fatalf("file =\n%q\nwant\n%q", got, want)
			}
			state, command, err := ReadCodexMCPRegistration(path)
			if err != nil || state != CodexMCPManaged || command != tc.exe {
				t.Errorf("round trip = (%v, %q, %v), want (managed, %q, nil)", state, command, err, tc.exe)
			}
		})
	}
}

// TestCodexMCPForeignRegistration is the never-clobber contract: any straza
// registration straza did not write is left exactly as it is, in every
// spelling TOML allows for it. A second [mcp_servers.straza] beside the user's
// would be a duplicate table; codex would refuse to load the whole file.
func TestCodexMCPForeignRegistration(t *testing.T) {
	setOSName(t, "linux")
	tests := []struct {
		name    string
		content string
		want    CodexMCPState
	}{
		{"plain table header", "[mcp_servers.straza]\ncommand = \"/old/straza\"\n", CodexMCPUnmanaged},
		{"quoted key", "[mcp_servers.\"straza\"]\ncommand = \"/old/straza\"\n", CodexMCPUnmanaged},
		{"spaces and a trailing comment", "[ mcp_servers . straza ] # mine\ncommand = \"/old\"\n", CodexMCPUnmanaged},
		{"sub-table only", "[mcp_servers.straza.env]\nFOO = \"bar\"\n", CodexMCPUnmanaged},
		{"key under [mcp_servers]", "[mcp_servers]\nstraza = { command = \"/old\" }\n", CodexMCPUnmanaged},
		{"dotted key at root", "mcp_servers.straza.command = \"/old\"\n", CodexMCPUnmanaged},
		{"inline table naming straza", "mcp_servers = { straza = { command = \"/old\" } }\n", CodexMCPUnmanaged},
		{"another server is not ours", "[mcp_servers.other]\ncommand = \"/other\"\n", CodexMCPMissing},
		{"another key under mcp_servers", "[mcp_servers]\nother = { command = \"/other\" }\n", CodexMCPMissing},
		{"straza under an unrelated table", "[profiles]\nstraza = \"yes\"\n", CodexMCPMissing},
		{"a commented-out registration is not a registration", "#[mcp_servers.straza]\n#command = \"/old\"\n", CodexMCPMissing},
		{"our own marker text in prose", "# straza-managed blocks go at the end\n", CodexMCPMissing},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			writeCodex(t, path, tc.content)

			state, _, err := ReadCodexMCPRegistration(path)
			if err != nil || state != tc.want {
				t.Fatalf("read = (%v, %v), want %v", state, err, tc.want)
			}

			state, changed, err := InstallCodexMCPServer(path, "/opt/straza")
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == CodexMCPUnmanaged {
				if state != CodexMCPUnmanaged || changed {
					t.Errorf("install = (%v, %v), want (unmanaged, false): user config is untouchable", state, changed)
				}
				if got := readCodex(t, path); got != tc.content {
					t.Errorf("file was modified:\n%q\nwant\n%q", got, tc.content)
				}
				// Uninstall must not take the user's registration either.
				state, changed, err = UninstallCodexMCPServer(path)
				if err != nil || state != CodexMCPUnmanaged || changed {
					t.Errorf("uninstall = (%v, %v, %v), want (unmanaged, false, nil)", state, changed, err)
				}
				if got := readCodex(t, path); got != tc.content {
					t.Errorf("uninstall modified the file:\n%q", got)
				}
				return
			}
			if state != CodexMCPManaged || !changed {
				t.Errorf("install = (%v, %v), want (managed, true)", state, changed)
			}
			if got, want := readCodex(t, path), tc.content+"\n"+codexWant("'/opt/straza'"); got != want {
				t.Errorf("file =\n%q\nwant\n%q", got, want)
			}
		})
	}
}

// TestUninstallCodexMCPServer: uninstall takes back exactly what install put
// there (markers, block, and the one blank line it added) and nothing else.
func TestUninstallCodexMCPServer(t *testing.T) {
	tests := []struct {
		name string
		pre  string
	}{
		{"config with comments and settings", "# my codex config\nmodel = \"gpt-5-codex\"\n\n[tui]\ntheme = \"dark\"\n"},
		{"config without a trailing newline", "model = \"gpt-5-codex\""},
		{"empty file", ""},
		{"other mcp servers", "[mcp_servers.other]\ncommand = \"/other\"\nargs = []\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			writeCodex(t, path, tc.pre)

			if _, _, err := InstallCodexMCPServer(path, "/opt/straza"); err != nil {
				t.Fatal(err)
			}
			state, changed, err := UninstallCodexMCPServer(path)
			if err != nil || state != CodexMCPMissing || !changed {
				t.Fatalf("uninstall = (%v, %v, %v), want (missing, true, nil)", state, changed, err)
			}
			// The install added a newline to a file that lacked one; that is
			// the only byte uninstall is allowed to leave behind.
			want := tc.pre
			if want != "" && !strings.HasSuffix(want, "\n") {
				want += "\n"
			}
			if got := readCodex(t, path); got != want {
				t.Fatalf("after uninstall =\n%q\nwant\n%q", got, want)
			}
			// Uninstalling twice is a no-op, not an error.
			state, changed, err = UninstallCodexMCPServer(path)
			if err != nil || state != CodexMCPMissing || changed {
				t.Errorf("second uninstall = (%v, %v, %v), want (missing, false, nil)", state, changed, err)
			}
		})
	}

	t.Run("missing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "absent.toml")
		state, changed, err := UninstallCodexMCPServer(path)
		if err != nil || state != CodexMCPMissing || changed {
			t.Errorf("= (%v, %v, %v), want (missing, false, nil)", state, changed, err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Error("uninstall must not create the file")
		}
	})

	t.Run("keeps the file when the block was all of it", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		if _, _, err := InstallCodexMCPServer(path, "/opt/straza"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := UninstallCodexMCPServer(path); err != nil {
			t.Fatal(err)
		}
		if got := readCodex(t, path); got != "" {
			t.Errorf("file = %q, want empty (and still present; deleting a user's config is not ours to do)", got)
		}
	})
}

// TestCodexMCPPreservesCRLF: a config.toml edited on Windows is CRLF, and a
// block written with bare LF into it reads as one mangled line in Notepad.
func TestCodexMCPPreservesCRLF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	pre := "# windows config\r\nmodel = \"gpt-5-codex\"\r\n"
	writeCodex(t, path, pre)

	if _, _, err := InstallCodexMCPServer(path, `C:\Users\alice\straza.exe`); err != nil {
		t.Fatal(err)
	}
	got := readCodex(t, path)
	if !strings.HasPrefix(got, pre) {
		t.Fatalf("existing bytes changed:\n%q", got)
	}
	if strings.Contains(strings.TrimPrefix(got, pre), "\n") && !strings.Contains(strings.TrimPrefix(got, pre), "\r\n") {
		t.Errorf("block was written with LF into a CRLF file:\n%q", got)
	}
	if n := strings.Count(got, "\r\n"); n != strings.Count(got, "\n") {
		t.Errorf("mixed line endings (%d CRLF of %d LF):\n%q", n, strings.Count(got, "\n"), got)
	}

	// A rewrite keeps them, and uninstall restores the file exactly.
	if _, _, err := InstallCodexMCPServer(path, `D:\straza.exe`); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(readCodex(t, path), "\r\n"); n != strings.Count(readCodex(t, path), "\n") {
		t.Errorf("rewrite broke the line endings:\n%q", readCodex(t, path))
	}
	if _, _, err := UninstallCodexMCPServer(path); err != nil {
		t.Fatal(err)
	}
	if got := readCodex(t, path); got != pre {
		t.Errorf("after uninstall = %q, want %q", got, pre)
	}
}

// TestCodexMCPUnterminatedBlock: a half-deleted block is a human's mistake to
// fix, not a reason to write a second one straza would then never rewrite.
func TestCodexMCPUnterminatedBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	pre := "# BEGIN straza-managed (straza install writes this block; do not edit inside)\n[mcp_servers.straza]\n"
	writeCodex(t, path, pre)

	if _, _, err := InstallCodexMCPServer(path, "/opt/straza"); err == nil {
		t.Error("install must refuse an unterminated block")
	}
	if _, _, err := UninstallCodexMCPServer(path); err == nil {
		t.Error("uninstall must refuse an unterminated block")
	}
	if _, _, err := ReadCodexMCPRegistration(path); err == nil {
		t.Error("read must report an unterminated block")
	}
	if got := readCodex(t, path); got != pre {
		t.Errorf("the file must be left alone:\n%q", got)
	}
}

// TestCodexMCPStartupTimeout pins startup_timeout_sec and the failure it
// exists to prevent: codex's default MCP startup budget is shorter than a COLD
// straza start (config load plus the first gateway checkin), so the harness
// reports the straza server as "not initialized" while a manual
// `straza mcp --harness codex` serves stdio perfectly well. 60 s is the
// budget we give it.
func TestCodexMCPStartupTimeout(t *testing.T) {
	setOSName(t, "linux")
	path := filepath.Join(t.TempDir(), "config.toml")
	if _, _, err := InstallCodexMCPServer(path, "/opt/straza"); err != nil {
		t.Fatal(err)
	}
	if got := readCodex(t, path); !strings.Contains(got, "\nstartup_timeout_sec = 60\n") {
		t.Fatalf("the registration must budget the cold start:\n%s", got)
	}

	// The block is rewritten WHOLE, never appended into, so an operator who
	// hand-added the key, in any spelling, converges on
	// the next install instead of accumulating keys beside it.
	hand := strings.Replace(codexWant("'/opt/straza'"), "startup_timeout_sec = 60\n",
		"startup_timeout_ms = 30000\nstartup_timeout_sec = 45\n", 1)
	writeCodex(t, path, hand)
	state, changed, err := InstallCodexMCPServer(path, "/opt/straza")
	if err != nil || state != CodexMCPManaged || !changed {
		t.Fatalf("re-install over a hand-edited block = (%v, %v, %v), want (managed, true, nil)", state, changed, err)
	}
	if got, want := readCodex(t, path), codexWant("'/opt/straza'"); got != want {
		t.Fatalf("hand-edited block did not converge:\n%q\nwant\n%q", got, want)
	}
}

// TestCodexMCPWindowsSystemRootEnv: on Windows the block carries the
// SystemRoot env line, defensive for the upstream codex 0.146 stdio
// spawn-env failures, and what keeps a re-run install from stripping an
// operator's hand-added line (the block is rewritten whole). Unix blocks
// must NOT grow it.
func TestCodexMCPWindowsSystemRootEnv(t *testing.T) {
	t.Run("windows", func(t *testing.T) {
		setOSName(t, "windows")
		t.Setenv("SystemRoot", `C:\Windows`)
		path := filepath.Join(t.TempDir(), codexConfigFile)
		if _, _, err := InstallCodexMCPServer(path, "/opt/straza"); err != nil {
			t.Fatal(err)
		}
		body := readCodex(t, path)
		if want := `env = { SystemRoot = 'C:\Windows' }`; !strings.Contains(body, want) {
			t.Errorf("windows block must carry %q:\n%s", want, body)
		}
		// Idempotent with the env line in place.
		_, changed, err := InstallCodexMCPServer(path, "/opt/straza")
		if err != nil || changed {
			t.Errorf("re-install = (changed %v, %v), want (false, nil)", changed, err)
		}
	})
	t.Run("windows-no-envvar", func(t *testing.T) {
		setOSName(t, "windows")
		t.Setenv("SystemRoot", "")
		path := filepath.Join(t.TempDir(), codexConfigFile)
		if _, _, err := InstallCodexMCPServer(path, "/opt/straza"); err != nil {
			t.Fatal(err)
		}
		if body := readCodex(t, path); !strings.Contains(body, `env = { SystemRoot = 'C:\Windows' }`) {
			t.Errorf("an empty SystemRoot must fall back to C:\\Windows:\n%s", body)
		}
	})
	t.Run("unix-absent", func(t *testing.T) {
		setOSName(t, "linux")
		path := filepath.Join(t.TempDir(), codexConfigFile)
		if _, _, err := InstallCodexMCPServer(path, "/opt/straza"); err != nil {
			t.Fatal(err)
		}
		if body := readCodex(t, path); strings.Contains(body, "SystemRoot") {
			t.Errorf("unix block must not carry a SystemRoot env line:\n%s", body)
		}
	})
}

// TestCodexMCPStateString pins the words install output and doctor print.
func TestCodexMCPStateString(t *testing.T) {
	for state, want := range map[CodexMCPState]string{
		CodexMCPMissing: "missing", CodexMCPManaged: "managed", CodexMCPUnmanaged: "unmanaged",
	} {
		if got := state.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", state, got, want)
		}
	}
}

// TestCodexMCPCheck is the doctor half of the loop: install writes the
// registration and doctor proves it, including a block naming a binary that
// has since moved and codex wired with no registration at all, which older
// installs that only printed the registration leave behind.
func TestCodexMCPCheck(t *testing.T) {
	binDir := t.TempDir()
	exe := filepath.Join(binDir, "straza")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		config     string // "" = no config.toml at all
		wireHooks  bool
		wantCheck  bool
		wantStatus string
		wantDetail string
	}{
		{
			name:       "managed block naming a binary that is there",
			config:     codexWant("'" + exe + "'"),
			wireHooks:  true,
			wantCheck:  true,
			wantStatus: checkOK,
			wantDetail: "straza-managed block",
		},
		{
			name:       "managed block naming a binary that moved",
			config:     codexWant("'/gone/straza'"),
			wireHooks:  true,
			wantCheck:  true,
			wantStatus: checkWarn,
			wantDetail: "not there",
		},
		{
			name:       "the user's own registration is left alone and said so",
			config:     "[mcp_servers.straza]\ncommand = \"/my/straza\"\n",
			wireHooks:  true,
			wantCheck:  true,
			wantStatus: checkOK,
			wantDetail: "straza did not write",
		},
		{
			name:       "codex wired but never registered",
			config:     "model = \"gpt-5-codex\"\n",
			wireHooks:  true,
			wantCheck:  true,
			wantStatus: checkWarn,
			wantDetail: "no straza MCP server registered",
		},
		{
			name:      "codex not in use: no line at all",
			config:    "",
			wireHooks: false,
			wantCheck: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", t.TempDir())
			if tc.config != "" {
				writeCodex(t, filepath.Join(home, "config.toml"), tc.config)
			}
			if tc.wireHooks {
				// hooks.json, the file codex reads. settings.json is a
				// dead path, and wiredLayer does not look at it.
				if _, err := InstallCodexHooks(filepath.Join(home, codexHooksFile), exe); err != nil {
					t.Fatal(err)
				}
			}
			got := codexMCPCheck()
			if !tc.wantCheck {
				if got != nil {
					t.Fatalf("want no check, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("want a check, got none")
			}
			if got.Name != "codex-mcp" || got.Status != tc.wantStatus {
				t.Errorf("check = %+v, want name codex-mcp status %s", *got, tc.wantStatus)
			}
			if !strings.Contains(got.Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to mention %q", got.Detail, tc.wantDetail)
			}
			if !strings.Contains(got.Detail, filepath.Join(home, "config.toml")) {
				t.Errorf("detail = %q, want the config path in it", got.Detail)
			}
			if got.Status != checkOK && got.Hint == "" {
				t.Error("a non-ok check without a next step is noise")
			}
		})
	}
}

// TestInstallManagedCodexMCP: --managed has no system-scope MCP file to write
// for codex, so it must still write the invoking user's config.toml, pointed
// at the MANAGED binary, so the harness runs the artifact the server verifies.
// No path through install ends in a hand-step.
func TestInstallManagedCodexMCP(t *testing.T) {
	setOSName(t, "linux")
	sysDir := t.TempDir()
	t.Setenv("STRAZA_SYSTEM", sysDir)
	t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", filepath.Join(sysDir, "harness"))
	t.Setenv("STRAZA_MANAGED_BIN_DIR", filepath.Join(sysDir, "bin"))
	t.Setenv("STRAZA_HOME", t.TempDir())
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

	install := ManagedInstallOptions{Harnesses: []string{"codex"},
		ServerURL: snapshotKeyServer(t, map[string]string{"k1": "AAAA"})}
	var out strings.Builder
	err := InstallManaged(context.Background(), install, &out)
	if err != nil {
		t.Fatalf("InstallManaged: %v", err)
	}

	binPath := ManagedBinaryPath("")
	config := filepath.Join(codexHome, "config.toml")
	if got, want := readCodex(t, config), codexWant("'"+binPath+"'"); got != want {
		t.Fatalf("managed install wrote\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(out.String(), config) {
		t.Errorf("install output must name the file it wrote:\n%s", out.String())
	}
	if strings.Contains(strings.ToLower(out.String()), "by hand") {
		t.Errorf("no install path may end in a hand-step:\n%s", out.String())
	}
	// config.toml is user-writable, so measuring it would turn every ordinary
	// user edit into att=none: self + config + hooks.codex, and nothing else.
	att := measureAttestation("codex")
	if len(att.Hashes) != 3 {
		t.Errorf("measured %d artifacts, want 3 (self, config, hooks.codex): %+v", len(att.Hashes), att.Hashes)
	}
	for artifact := range att.Hashes {
		if strings.Contains(artifact, "mcp") {
			t.Errorf("the codex MCP registration must not be a measured artifact: %s", artifact)
		}
	}

	// Re-running is idempotent, and the managed uninstall takes the block back.
	if err := InstallManaged(context.Background(), install, io.Discard); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if strings.Count(readCodex(t, config), "# BEGIN straza-managed") != 1 {
		t.Errorf("re-run duplicated the block:\n%s", readCodex(t, config))
	}
	if err := UninstallManaged([]string{"codex"}, "", io.Discard); err != nil {
		t.Fatalf("UninstallManaged: %v", err)
	}
	if got := readCodex(t, config); strings.Contains(got, "straza-managed") {
		t.Errorf("managed uninstall left the block behind:\n%s", got)
	}
}
