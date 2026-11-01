//go:build darwin

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// TestInstallerIsHidden pins that the installer group and its uninstall
// step are registered on macOS and that neither is an available command,
// the test both help and the CLI reference writer apply.
func TestInstallerIsHidden(t *testing.T) {
	root := rootCmd()
	for _, path := range [][]string{{"installer"}, {"installer", "uninstall"}} {
		c, _, err := root.Find(path)
		if err != nil || c.CommandPath() != "straza "+strings.Join(path, " ") {
			t.Fatalf("%v is not registered: %v", path, err)
		}
		if c.IsAvailableCommand() {
			t.Errorf("%s shows in help and in the CLI reference", c.CommandPath())
		}
	}
}

// TestPkgLayoutMatchesThePackage pins the paths the uninstall removes to
// the ones the files in deploy/macos install and run, and the binary to the
// one the managed wiring names, so the Go side and the package cannot drift
// apart.
func TestPkgLayoutMatchesThePackage(t *testing.T) {
	t.Setenv("STRAZA_MANAGED_BIN_DIR", "")
	if got := agentguard.ManagedBinaryPath(""); got != pkgLayout.binary {
		t.Errorf("the managed wiring runs %s, and the uninstall removes %s", got, pkgLayout.binary)
	}
	account := "<string>" + localapi.ServiceAccount + "</string>"
	files := map[string][]string{
		filepath.Base(pkgLayout.plist): {"<string>" + launchdLabel + "</string>", "<string>" + pkgLayout.binary + "</string>", account,
			"<string>" + filepath.Join(pkgLayout.logDir, "service.log") + "</string>"},
		filepath.Base(pkgLayout.newsyslog): {filepath.Join(pkgLayout.logDir, "service.log") + " ", localapi.ServiceAccount + ":" + localapi.ServiceAccount},
		"scripts/preinstall":               {"account=" + localapi.ServiceAccount},
		"scripts/postinstall": {"account=" + localapi.ServiceAccount, "label=" + launchdLabel,
			"state=" + pkgLayout.stateDir, "run=" + pkgLayout.runDir, "logs=" + pkgLayout.logDir, `own_folder "$logs" 0755`, "bin=" + pkgLayout.binary,
			"pathdir=" + filepath.Dir(pkgLayout.links[0]), "for name in " + filepath.Base(pkgLayout.links[0]) + " " + filepath.Base(pkgLayout.links[1])},
		"distribution.xml": {`<pkg-ref id="` + pkgIdentifier + `"`},
		"build-pkg.sh": {"identifier=" + pkgIdentifier, "newsyslog.d", "$root" + filepath.Dir(pkgLayout.binary),
			"$root" + pkgLayout.docDir + `"`},
	}
	// postinstall makes the PATH links, so the payload holds nothing under
	// /usr/local, which the Installer could otherwise take from a person.
	if raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "macos", "build-pkg.sh")); err != nil || strings.Contains(string(raw), `"$root/usr/local`) {
		t.Errorf("deploy/macos/build-pkg.sh stages a file under /usr/local, or cannot be read: %v", err)
	}
	for name, wants := range files {
		raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "macos", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(raw), want) {
				t.Errorf("deploy/macos/%s does not hold %q", name, want)
			}
		}
	}
}

// errGone is what a fake tool answers for a job, record or receipt that is
// not there.
var errGone = errors.New("not found")

// TestInstallerUninstall pins the order of the uninstall, which stops the
// service before it unwires the harnesses and removes the binary and its
// folders last, and that a rerun skips what is gone, that a failed step
// stops the steps after it, that a folder holding a foreign file stays, and
// that --purge deletes the state folder, the log folder and the
// account but never what a link points at nor a file the archive pattern
// does not name. The transcript holds the tool calls the fake Mac recorded
// and the lines the uninstall printed, in the order they came.
func TestInstallerUninstall(t *testing.T) {
	logs := []string{"logdir", "log0", "neighbor"}
	everything := append([]string{"plist", "binary", "link0", "link1", "run", "newsyslog", "state", "doc", "bindir", "share", "opt"}, logs...)
	stop := []string{
		"$ launchctl print system/ai.straza.service",
		"$ launchctl bootout system/ai.straza.service",
		"stopped the Straza background service system/ai.straza.service",
		"removed /Library/LaunchDaemons/ai.straza.service.plist",
		"$ unwire",
		"removed /opt/straza/run",
		"removed /etc/newsyslog.d/ai.straza.service.conf",
	}
	forget := []string{
		"$ pkgutil --pkg-info ai.straza.straza",
		"$ pkgutil --forget ai.straza.straza",
		"forgot the package receipt ai.straza.straza",
	}
	files := []string{
		"removed /opt/straza/share/doc",
		"removed /opt/straza/bin/straza",
		"removed /opt/straza/bin",
		"removed /opt/straza/share",
		"removed /opt/straza",
	}
	links := []string{"removed /usr/local/bin/straza", "removed /usr/local/bin/git-remote-straza"}
	kept := "kept /var/db/straza-service, with this Mac's key and sessions, and the account _straza-service"
	join := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	tests := []struct {
		name     string
		prepare  func(t *testing.T, root string, l macLayout)
		purge    bool
		args     []string
		euid     int
		answers  map[string][]error
		unwire   error
		want     []string // the transcript, each line by its start
		wantErr  string
		wantLeft []string // what is still there afterwards, by paths' key
	}{
		{name: "an install is removed, and the state folder, the log and the account stay", prepare: installed,
			want:     join(stop, forget, links, files, []string{kept}),
			wantLeft: append([]string{"state"}, logs...)},
		{name: "--purge also deletes the state folder, the log folder, the account and the group", prepare: installed, purge: true,
			want: join(stop, []string{
				"removed /var/db/straza-service",
				"removed /var/log/straza-service",
				"$ dscl . -read /Users/_straza-service RecordName",
				"$ dscl . -delete /Users/_straza-service",
				"deleted the account _straza-service",
				"$ dscl . -read /Groups/_straza-service RecordName",
				"$ dscl . -delete /Groups/_straza-service",
				"deleted the group _straza-service",
			}, forget, links, files),
			wantLeft: []string{"neighbor"}},
		{name: "a rerun with everything gone finishes without changing anything", prepare: func(*testing.T, string, macLayout) {}, purge: true,
			answers: map[string][]error{
				"launchctl print system/ai.straza.service":        {errGone},
				"dscl . -read /Users/_straza-service RecordName":  {errGone},
				"dscl . -read /Groups/_straza-service RecordName": {errGone},
				"pkgutil --pkg-info ai.straza.straza":             {errGone},
			},
			want: []string{
				"$ launchctl print system/ai.straza.service",
				"$ unwire",
				"$ dscl . -read /Users/_straza-service RecordName",
				"$ dscl . -read /Groups/_straza-service RecordName",
				"$ pkgutil --pkg-info ai.straza.straza",
			}},
		{name: "a service that stays loaded after a failed bootout stops the uninstall before the unwire", prepare: installed,
			answers: map[string][]error{"launchctl bootout system/ai.straza.service": {errors.New("Boot-out failed: 5: Input/output error")}},
			want: []string{
				"$ launchctl print system/ai.straza.service",
				"$ launchctl bootout system/ai.straza.service",
				"$ launchctl print system/ai.straza.service",
			},
			wantErr:  "could not stop the Straza background service system/ai.straza.service",
			wantLeft: everything},
		{name: "a failed bootout of a service that is gone anyway goes on", prepare: installed,
			answers: map[string][]error{
				"launchctl print system/ai.straza.service":   {nil, errGone},
				"launchctl bootout system/ai.straza.service": {errors.New("Boot-out failed: 3: No such process")},
			},
			want:     join(stop[:2], []string{"$ launchctl print system/ai.straza.service"}, stop[2:], forget, links, files, []string{kept}),
			wantLeft: append([]string{"state"}, logs...)},
		{name: "a failed unwire keeps the binary for the rerun", prepare: installed,
			unwire:   errors.New("unwire codex: permission denied"),
			want:     stop[:5],
			wantErr:  "could not unwire Claude Code, Codex and Gemini (unwire codex: permission denied), so it stopped before the steps after it. Fix that, then run `sudo /opt/straza/bin/straza installer uninstall` again",
			wantLeft: append([]string{"binary", "link0", "link1", "run", "newsyslog", "state", "doc", "bindir", "share", "opt"}, logs...)},
		{name: "--purge refuses a state folder that is a link and deletes nothing it points at", purge: true,
			prepare: func(t *testing.T, root string, l macLayout) {
				installed(t, root, l)
				if err := os.RemoveAll(l.stateDir); err != nil {
					t.Fatal(err)
				}
				layFile(t, filepath.Join(root, "elsewhere", "key"))
				if err := os.Symlink(filepath.Join(root, "elsewhere"), l.stateDir); err != nil {
					t.Fatal(err)
				}
			},
			want:     stop,
			wantErr:  "did not delete /var/db/straza-service, because it is a link or not a folder",
			wantLeft: append([]string{"binary", "link0", "link1", "state", "elsewhere", "doc", "bindir", "share", "opt"}, logs...)},
		{name: "--purge refuses a log folder that is a link and deletes nothing it points at", purge: true,
			prepare: func(t *testing.T, root string, l macLayout) {
				installed(t, root, l)
				if err := os.RemoveAll(l.logDir); err != nil {
					t.Fatal(err)
				}
				layFile(t, filepath.Join(root, "elsewhere-log", "service.log"))
				if err := os.Symlink(filepath.Join(root, "elsewhere-log"), l.logDir); err != nil {
					t.Fatal(err)
				}
			},
			want:     join(stop, []string{"removed /var/db/straza-service"}),
			wantErr:  "did not delete /var/log/straza-service, because it is a link or not a folder",
			wantLeft: []string{"binary", "link0", "link1", "doc", "bindir", "share", "opt", "logdir", "elsewherelog", "neighbor"}},
		{name: "a PATH link that points elsewhere is kept", prepare: func(t *testing.T, root string, l macLayout) {
			installed(t, root, l)
			if err := os.Remove(l.links[0]); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "usr", "local", "Cellar", "straza"), l.links[0]); err != nil {
				t.Fatal(err)
			}
		},
			want: join(stop, forget, []string{
				"kept /usr/local/bin/straza, because it is not the Straza package's link to /opt/straza/bin/straza",
				"removed /usr/local/bin/git-remote-straza",
			}, files, []string{kept}),
			wantLeft: append([]string{"link0", "state"}, logs...)},
		{name: "a package folder that holds a file of someone else's stays", prepare: func(t *testing.T, root string, l macLayout) {
			installed(t, root, l)
			layFile(t, filepath.Join(l.folders[2], "notes.txt"))
		},
			want:     join(stop, forget, links, files[:4], []string{kept}),
			wantLeft: append([]string{"state", "opt", "foreign"}, logs...)},
		{name: "a package folder that cannot be removed once the binary is gone is reported, and the uninstall succeeds",
			prepare: func(t *testing.T, root string, l macLayout) {
				if os.Geteuid() == 0 {
					t.Skip("root removes a folder from a parent it cannot write")
				}
				installed(t, root, l)
				parent := filepath.Dir(l.folders[2])
				if err := os.Chmod(parent, 0o555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
			},
			want: join(stop, forget, links, files[:4], []string{
				"left /opt/straza in place, because removing it failed (",
				kept,
			}),
			wantLeft: append([]string{"state", "opt"}, logs...)},
		{name: "a run without root is refused before any change", prepare: installed, euid: 501,
			wantErr:  "`straza installer uninstall` changes system folders and must run as root. Run it again with sudo.",
			wantLeft: everything},
		{name: "an operand is refused before any change", prepare: installed, args: []string{"now"},
			wantErr:  "`straza installer uninstall` takes no operands",
			wantLeft: everything},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			l := testLayout(root)
			tt.prepare(t, root, l)
			var log strings.Builder
			sys := fakeMac(&log, tt.euid, tt.answers, tt.unwire)

			err := installerUninstall(sys, l, tt.purge, tt.args, &log)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("uninstall = %v, want nil", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(strings.ReplaceAll(err.Error(), root, ""), tt.wantErr)):
				t.Fatalf("uninstall = %v, want the refusal %q", err, tt.wantErr)
			}
			got := strings.Split(strings.TrimSuffix(strings.ReplaceAll(log.String(), root, ""), "\n"), "\n")
			if log.Len() == 0 {
				got = nil
			}
			if len(got) != len(tt.want) {
				t.Fatalf("transcript has %d lines, want %d:\n%s", len(got), len(tt.want), strings.Join(got, "\n"))
			}
			for i := range got {
				if !strings.HasPrefix(got[i], tt.want[i]) {
					t.Errorf("transcript line %d = %q, want it to start with %q", i+1, got[i], tt.want[i])
				}
			}
			left := map[string]bool{}
			for _, k := range tt.wantLeft {
				left[k] = true
			}
			for k, p := range paths(root, l) {
				if _, err := os.Lstat(p); (err == nil) != left[k] {
					t.Errorf("%s (%s) is there = %v, want %v", k, strings.TrimPrefix(p, root), err == nil, left[k])
				}
			}
		})
	}
}

// testLayout returns the package's layout under root.
func testLayout(root string) macLayout {
	under := func(p string) string { return filepath.Join(root, p) }
	var folders []string
	for _, f := range pkgLayout.folders {
		folders = append(folders, under(f))
	}
	return macLayout{
		plist:     under(pkgLayout.plist),
		binary:    under(pkgLayout.binary),
		links:     []string{under(pkgLayout.links[0]), under(pkgLayout.links[1])},
		runDir:    under(pkgLayout.runDir),
		newsyslog: under(pkgLayout.newsyslog),
		stateDir:  under(pkgLayout.stateDir),
		docDir:    under(pkgLayout.docDir),
		folders:   folders,
		logDir:    under(pkgLayout.logDir),
	}
}

// paths names every path of l that a test checks afterwards, the folders
// tests link the state and log folders to, a log beside the service's log
// folder that no step may touch, and a file someone else put in /opt/straza.
func paths(root string, l macLayout) map[string]string {
	return map[string]string{
		"plist": l.plist, "binary": l.binary, "link0": l.links[0], "link1": l.links[1],
		"run": l.runDir, "newsyslog": l.newsyslog, "state": l.stateDir,
		"doc": l.docDir, "bindir": l.folders[0], "share": l.folders[1], "opt": l.folders[2],
		"logdir": l.logDir, "log0": filepath.Join(l.logDir, "service.log.0"), "neighbor": filepath.Join(filepath.Dir(l.logDir), "install.log"),
		"elsewherelog": filepath.Join(root, "elsewhere-log", "service.log"),
		"elsewhere":    filepath.Join(root, "elsewhere", "key"),
		"foreign":      filepath.Join(l.folders[2], "notes.txt"),
	}
}

// installed lays out l as a Mac holds it after an install, a first start of
// the service and a log rotation, with another program's log beside it.
func installed(t *testing.T, _ string, l macLayout) {
	t.Helper()
	for _, f := range []string{l.plist, l.binary, l.newsyslog, filepath.Join(l.runDir, "git", "hosts.conf"),
		filepath.Join(l.stateDir, "key"), filepath.Join(l.docDir, "LICENSE"),
		filepath.Join(l.logDir, "service.log"), filepath.Join(l.logDir, "service.log.0"),
		filepath.Join(filepath.Dir(l.logDir), "install.log")} {
		layFile(t, f)
	}
	for _, link := range l.links {
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(l.binary, link); err != nil {
			t.Fatal(err)
		}
	}
}

// fakeMac is a macSystem that answers as root unless euid says otherwise,
// and writes each tool call and the unwire to log. answers holds, per
// command line, the errors its next calls return in turn. A command line
// with no answer left succeeds.
func fakeMac(log io.Writer, euid int, answers map[string][]error, unwire error) macSystem {
	return macSystem{
		euid: func() int { return euid },
		run: func(tool string, args ...string) error {
			line := filepath.Base(tool) + " " + strings.Join(args, " ")
			fmt.Fprintf(log, "$ %s\n", line)
			if q := answers[line]; len(q) > 0 {
				answers[line] = q[1:]
				return q[0]
			}
			return nil
		},
		unwire: func(w io.Writer) error {
			fmt.Fprintln(w, "$ unwire")
			return unwire
		},
	}
}

// layFile writes an empty file at path, creating its folders.
func layFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}
