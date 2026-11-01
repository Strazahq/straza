package agentguard

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

var wantGitEnv = map[string]any{
	"GIT_CONFIG_COUNT":   "1",
	"GIT_CONFIG_KEY_0":   "include.path",
	"GIT_CONFIG_VALUE_0": "/run/straza-service/git/hosts.conf",
}

// wantGitEnvWindows is the env block of the windows Claude Code wiring.
// git takes GIT_CONFIG_VALUE_0 as it is, so the backslashes reach the
// Windows file API unchanged.
var wantGitEnvWindows = map[string]any{
	"GIT_CONFIG_COUNT":   "1",
	"GIT_CONFIG_KEY_0":   "include.path",
	"GIT_CONFIG_VALUE_0": `C:\ProgramData\straza-service\git\hosts.conf`,
}

// wantGitEnvDarwin is the env block of the darwin Claude Code wiring, whose
// host file sits in the run folder beside the service's socket.
var wantGitEnvDarwin = map[string]any{
	"GIT_CONFIG_COUNT":   "1",
	"GIT_CONFIG_KEY_0":   "include.path",
	"GIT_CONFIG_VALUE_0": "/opt/straza/run/git/hosts.conf",
}

// TestRenderGitEnv pins the env block of the served wiring: the Claude
// Code artifact of every platform carries the one include of its host
// file, and no other harness carries an env table.
func TestRenderGitEnv(t *testing.T) {
	want := map[string]map[string]any{"linux": wantGitEnv, "windows": wantGitEnvWindows, "darwin": wantGitEnvDarwin}
	for _, harness := range []string{"claude-code", "gemini"} {
		for _, goos := range SupportedGOOS {
			t.Run(harness+"/"+goos, func(t *testing.T) {
				arts, err := RenderManagedArtifacts(harness, goos, "")
				if err != nil {
					t.Fatal(err)
				}
				var settings map[string]any
				if err := json.Unmarshal(arts["hooks."+harness], &settings); err != nil {
					t.Fatal(err)
				}
				env, has := settings["env"]
				if block := want[goos]; harness == "claude-code" && block != nil {
					if !reflect.DeepEqual(env, block) {
						t.Fatalf("env %v, want %v", env, block)
					}
					return
				}
				if has {
					t.Fatalf("%s on %s carries an env table: %v", harness, goos, env)
				}
			})
		}
	}
	if GitHostsFile("linux") != "/run/straza-service/git/hosts.conf" ||
		GitHostsFile("windows") != `C:\ProgramData\straza-service\git\hosts.conf` ||
		GitHostsFile("darwin") != "/opt/straza/run/git/hosts.conf" {
		t.Fatal("the host file path per platform changed")
	}
}

// TestGitHostsFileHere pins the host file a local install names: the one
// the server renders on Linux and macOS, and on Windows the one the system
// service writes in its state folder, which is the rendered one only where
// Windows runs from C:.
func TestGitHostsFileHere(t *testing.T) {
	for _, goos := range SupportedGOOS {
		t.Run(goos, func(t *testing.T) {
			setOSName(t, goos)
			want := GitHostsFile(goos)
			if goos == "windows" && runtime.GOOS == "windows" {
				want = filepath.Join(localapi.SystemStateDir, "git", "hosts.conf")
			}
			if got := gitHostsFileHere(); got != want {
				t.Fatalf("gitHostsFileHere() = %q, want %q", got, want)
			}
		})
	}
}

// TestInstallGitEnvMergesAndUninstalls checks, for the managed and the
// user scope alike, that the installer merges the block beside an
// operator's own env entries and that uninstall takes back only the
// entries that still hold Straza's values. On macOS, as on Linux, both
// scopes get the block. On Windows only the managed scope gets it, and
// uninstall takes it out of either scope.
func TestInstallGitEnvMergesAndUninstalls(t *testing.T) {
	read := func(t *testing.T, path string) map[string]any {
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
	scopes := []struct {
		name       string
		install    func(harness, path, bin string) error
		windowsEnv any
	}{
		{"managed", InstallManagedHooks, wantGitEnvWindows},
		{"user", InstallHooks, nil},
	}
	for _, sc := range scopes {
		t.Run(sc.name, func(t *testing.T) {
			setOSName(t, "linux")
			dir := t.TempDir()
			install := func(path string) {
				t.Helper()
				if err := sc.install("claude-code", path, "/usr/bin/straza"); err != nil {
					t.Fatal(err)
				}
			}
			uninstall := func(path string) {
				t.Helper()
				if err := UninstallHooks("claude-code", path, "/usr/bin/straza"); err != nil {
					t.Fatal(err)
				}
			}

			merged := filepath.Join(dir, "merged.json")
			if err := os.WriteFile(merged, []byte(`{"env":{"HTTPS_PROXY":"http://proxy:3128"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			install(merged)
			want := map[string]any{"HTTPS_PROXY": "http://proxy:3128"}
			for k, v := range wantGitEnv {
				want[k] = v
			}
			if env := read(t, merged)["env"]; !reflect.DeepEqual(env, want) {
				t.Fatalf("env %v, want %v", env, want)
			}
			uninstall(merged)
			if env := read(t, merged)["env"]; !reflect.DeepEqual(env, map[string]any{"HTTPS_PROXY": "http://proxy:3128"}) {
				t.Fatalf("env after uninstall %v, want the operator's entry alone", env)
			}

			only := filepath.Join(dir, "only-straza.json")
			install(only)
			uninstall(only)
			if env, has := read(t, only)["env"]; has {
				t.Fatalf("uninstall left an env table %v", env)
			}

			changed := filepath.Join(dir, "changed.json")
			install(changed)
			m := read(t, changed)
			m["env"].(map[string]any)["GIT_CONFIG_COUNT"] = "2"
			raw, _ := json.Marshal(m)
			if err := os.WriteFile(changed, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			uninstall(changed)
			if env := read(t, changed)["env"]; !reflect.DeepEqual(env, map[string]any{"GIT_CONFIG_COUNT": "2"}) {
				t.Fatalf("uninstall took an entry an operator changed: %v", env)
			}

			gemini := filepath.Join(dir, "gemini.json")
			if err := sc.install("gemini", gemini, "/usr/bin/straza"); err != nil {
				t.Fatal(err)
			}
			if env, has := read(t, gemini)["env"]; has {
				t.Fatalf("gemini got an env table %v", env)
			}
			setOSName(t, "darwin")
			mac := filepath.Join(dir, "darwin.json")
			install(mac)
			if env := read(t, mac)["env"]; !reflect.DeepEqual(env, wantGitEnvDarwin) {
				t.Fatalf("darwin %s scope env %v, want %v", sc.name, env, wantGitEnvDarwin)
			}
			uninstall(mac)
			if env, has := read(t, mac)["env"]; has {
				t.Fatalf("uninstall left the darwin block in the %s scope: %v", sc.name, env)
			}

			setOSName(t, "windows")
			win := filepath.Join(dir, "windows.json")
			install(win)
			m = read(t, win)
			if !reflect.DeepEqual(m["env"], sc.windowsEnv) {
				t.Fatalf("windows %s scope env %v, want %v", sc.name, m["env"], sc.windowsEnv)
			}
			m["env"] = wantGitEnvWindows
			raw, _ = json.Marshal(m)
			if err := os.WriteFile(win, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			uninstall(win)
			if env, has := read(t, win)["env"]; has {
				t.Fatalf("uninstall left the windows block in the %s scope: %v", sc.name, env)
			}
		})
	}
}

// TestApplyStrazaEnvReplaced pins which entries applyStrazaEnv reports as
// replaced: only the three git entries that held another value, and never
// the old value of GIT_CONFIG_VALUE_0, which can hold a credential.
func TestApplyStrazaEnvReplaced(t *testing.T) {
	cases := []struct {
		name    string
		harness string
		env     map[string]any
		want    []string
	}{
		{"no env table", "claude-code", nil, nil},
		{"Straza's own values", "claude-code", wantGitEnv, nil},
		{"other entries only", "claude-code", map[string]any{"HTTPS_PROXY": "http://proxy:3128"}, nil},
		{"a count of its own", "claude-code", map[string]any{"GIT_CONFIG_COUNT": "2"}, []string{"GIT_CONFIG_COUNT"}},
		{"a count as a JSON number", "claude-code", map[string]any{"GIT_CONFIG_COUNT": 2.0}, []string{"GIT_CONFIG_COUNT"}},
		{"all three of its own", "claude-code",
			map[string]any{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.editor", "GIT_CONFIG_VALUE_0": "vim"},
			[]string{"GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"}},
		{"a value that holds a credential", "claude-code",
			map[string]any{"GIT_CONFIG_KEY_0": "http.extraHeader", "GIT_CONFIG_VALUE_0": "Authorization: Bearer s3cret"},
			[]string{"GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"}},
		{"a key that holds a credential", "claude-code",
			map[string]any{"GIT_CONFIG_KEY_0": "url.https://s3cret@github.com/.insteadOf", "GIT_CONFIG_VALUE_0": "https://github.com/"},
			[]string{"GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"}},
		{"gemini gets no block", "gemini", map[string]any{"GIT_CONFIG_COUNT": "2"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settings := map[string]any{}
			if tc.env != nil {
				env := map[string]any{}
				for k, v := range tc.env {
					env[k] = v
				}
				settings["env"] = env
			}
			got := applyStrazaEnv(settings, tc.harness, GitHostsFile("linux"))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("replaced %q, want %q", got, tc.want)
			}
		})
	}
}

// TestInstallNamesReplacedGitEnv checks that an install over a person's own
// git env entries prints one notice naming the file and the replaced entries
// but none of their values, after the file is written, and that a second
// install prints none.
func TestInstallNamesReplacedGitEnv(t *testing.T) {
	for _, sc := range []struct {
		name    string
		install func(harness, path, bin string) error
	}{{"managed", InstallManagedHooks}, {"user", InstallHooks}} {
		t.Run(sc.name, func(t *testing.T) {
			setOSName(t, "linux")
			path := filepath.Join(t.TempDir(), "settings.json")
			own := `{"env":{"GIT_CONFIG_COUNT":"2","GIT_CONFIG_KEY_0":"core.editor","GIT_CONFIG_VALUE_0":"vim"}}`
			if err := os.WriteFile(path, []byte(own), 0o600); err != nil {
				t.Fatal(err)
			}
			first := stderrOf(t, func() error { return sc.install("claude-code", path, "/usr/bin/straza") })
			want := "NOTE: Straza replaced GIT_CONFIG_COUNT, GIT_CONFIG_KEY_0 and GIT_CONFIG_VALUE_0 " +
				"in the env of " + path + " with its own git settings, and uninstalling Straza does " +
				"not put them back, so if you still need them, restore them from a backup of that file before you rely " +
				"on this install.\n"
			if first != want || strings.Contains(first, "vim") || strings.Contains(first, "core.editor") {
				t.Fatalf("stderr %q\nwant %q", first, want)
			}
			if again := stderrOf(t, func() error { return sc.install("claude-code", path, "/usr/bin/straza") }); again != "" {
				t.Fatalf("a second install printed %q", again)
			}
		})
	}
}

// stderrOf runs fn with os.Stderr captured and returns what it printed.
func stderrOf(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	err = fn()
	os.Stderr = orig
	_ = w.Close()
	out, _ := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestDoctorLocalRenderNamesHostFileHere pins that doctor's local render
// names the host file this machine's install writes, so a managed wiring
// on a machine whose Windows runs from another volume than C: still
// matches it.
func TestDoctorLocalRenderNamesHostFileHere(t *testing.T) {
	for _, row := range []struct{ name, here string }{
		{"Windows on C:", GitHostsFile("windows")},
		{"Windows on D:", `D:\ProgramData\straza-service\git\hosts.conf`},
	} {
		t.Run(row.name, func(t *testing.T) {
			setOSName(t, "windows")
			old := gitHostsFileHere
			gitHostsFileHere = func() string { return row.here }
			t.Cleanup(func() { gitHostsFileHere = old })
			path := filepath.Join(t.TempDir(), "managed-settings.json")
			if err := InstallManagedHooks("claude-code", path, `D:\ProgramData\straza\bin\straza.exe`); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !matchesLocalRender("claude-code", raw) {
				t.Fatalf("doctor's local render does not match the managed wiring that names %s:\n%s", row.here, raw)
			}
		})
	}
}
