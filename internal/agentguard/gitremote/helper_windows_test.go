//go:build windows

package gitremote

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestInvokedWindowsPaths pins that the helper answers to the full path
// Windows starts it with, with either separator, in any letter case and
// with or without .exe, and that straza.exe in the same folder does not.
func TestInvokedWindowsPaths(t *testing.T) {
	cases := map[string]bool{
		`C:\ProgramData\straza\bin\git-remote-straza.exe`:                    true,
		`C:\ProgramData\straza\bin\GIT-REMOTE-STRAZA.EXE`:                    true,
		`C:\ProgramData\straza\bin\git-remote-straza`:                        true,
		`C:/ProgramData/straza/bin/git-remote-straza.exe`:                    true,
		`C:\ProgramData\straza\bin\straza.exe`:                               false,
		`C:\git-remote-straza.exe\straza.exe`:                                false,
		`C:\Program Files\Git\mingw64\libexec\git-core\git-remote-https.exe`: false,
	}
	for argv0, want := range cases {
		if got := Invoked(argv0); got != want {
			t.Errorf("Invoked(%q) = %v, want %v", argv0, got, want)
		}
	}
}

// TestHandoffRunsGitRemoteHTTPS pins the Windows hand-off: git's own https
// transport reads this process's stdin as git wrote it and answers on this
// process's stdout, and its exit code is the helper's, with no sentence of
// the helper's own. The answer to capabilities shows the commands arrived
// untouched, because a changed line is an unknown command.
func TestHandoffRunsGitRemoteHTTPS(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH, so there is no git remote-https to hand off to")
	}
	rows := []struct {
		name, in string
		code     int
		out, err string
	}{
		{"capabilities, then the end", "capabilities\n\n", 0, "\nfetch\n", ""},
		{"an unknown command", "straza-unknown\n", 1, "", "straza-unknown"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "in")
			if err := os.WriteFile(in, []byte(row.in), 0o600); err != nil {
				t.Fatal(err)
			}
			files := map[string]*os.File{}
			for name, flag := range map[string]int{"in": os.O_RDONLY, "out": os.O_CREATE | os.O_WRONLY, "err": os.O_CREATE | os.O_WRONLY} {
				f, err := os.OpenFile(filepath.Join(dir, name), flag, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = f.Close() })
				files[name] = f
			}
			stdin, stdout, stderr := os.Stdin, os.Stdout, os.Stderr
			os.Stdin, os.Stdout, os.Stderr = files["in"], files["out"], files["err"]
			var errw bytes.Buffer
			code := handoff("origin", "https://example.invalid/acme/pay-api.git", &errw)
			os.Stdin, os.Stdout, os.Stderr = stdin, stdout, stderr
			out, _ := os.ReadFile(filepath.Join(dir, "out"))
			gitErr, _ := os.ReadFile(filepath.Join(dir, "err"))
			if code != row.code || !strings.Contains(string(out), row.out) || !strings.Contains(string(gitErr), row.err) || errw.Len() != 0 {
				t.Fatalf("exit %d, stdout %q, git's stderr %q, helper's stderr %q; want exit %d, stdout holding %q, stderr holding %q",
					code, out, gitErr, errw.String(), row.code, row.out, row.err)
			}
		})
	}
}
