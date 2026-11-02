package service

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestHostFileBody pins the host file of contract 20.12: one url section
// per valid upstream with its own form, plus the two ssh forms only for
// https on a bare host, then the protocol line, and an empty file for no
// upstream. An invalid upstream is left out and reported.
func TestHostFileBody(t *testing.T) {
	gh := "[url \"straza::https://github.com/\"]\n" +
		"\tinsteadOf = https://github.com/\n" +
		"\tinsteadOf = git@github.com:\n" +
		"\tinsteadOf = ssh://git@github.com/\n"
	ghe := "[url \"straza::https://ghe.example.com/\"]\n" +
		"\tinsteadOf = https://ghe.example.com/\n" +
		"\tinsteadOf = git@ghe.example.com:\n" +
		"\tinsteadOf = ssh://git@ghe.example.com/\n"
	port := "[url \"straza::https://ghe.example.com:8443/\"]\n" +
		"\tinsteadOf = https://ghe.example.com:8443/\n"
	path := "[url \"straza::https://ghe.example.com/scm/\"]\n" +
		"\tinsteadOf = https://ghe.example.com/scm/\n"
	lab := "[url \"straza::http://127.0.0.1:9418/\"]\n" +
		"\tinsteadOf = http://127.0.0.1:9418/\n"
	localhost := "[url \"straza::http://localhost/\"]\n" +
		"\tinsteadOf = http://localhost/\n"
	allow := "[protocol \"straza\"]\n\tallow = always\n"
	rows := []struct {
		name      string
		upstreams []string
		want      string
		skipped   int
	}{
		{"zero", nil, "", 0},
		{"one", []string{"https://github.com"}, gh + allow, 0},
		{"two sorted without repeats", []string{"https://github.com", "https://ghe.example.com", "https://github.com"}, ghe + gh + allow, 0},
		{"https with a port has no ssh forms", []string{"https://ghe.example.com:8443"}, port + allow, 0},
		{"https with a path has no ssh forms", []string{"https://ghe.example.com/scm"}, path + allow, 0},
		{"http on a loopback address has no ssh forms", []string{"http://127.0.0.1:9418"}, lab + allow, 0},
		{"http on localhost has no ssh forms", []string{"http://localhost"}, localhost + allow, 0},
		{"only invalid", []string{"http://github.com"}, "", 1},
		{"invalid skipped beside a valid one", []string{
			"http://github.com", "https://tok@github.com", "https://github.com/?x", "https://github.com#f",
			"https://github.com/", "https://GitHub.com", "https://github.com:443", "https://github.com/a/../b",
			"https://git hub.com", "https://github.com\"]\n[core]", "https://github.com;x", "", "github.com",
			"https://github.com",
		}, gh + allow, 13},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got, skipped := hostFileBody(row.upstreams)
			if got != row.want {
				t.Errorf("body =\n%s\nwant\n%s", got, row.want)
			}
			if len(skipped) != row.skipped {
				t.Errorf("skipped %d upstreams (%+v), want %d", len(skipped), skipped, row.skipped)
			}
			for _, sk := range skipped {
				if sk.why == "" {
					t.Errorf("upstream %q was skipped without a reason", sk.raw)
				}
			}
		})
	}
}

// TestWriteHostFile pins the file on disk: the folder made with 0755 when
// missing, the file with 0644, replaced whole on each answer, emptied by
// an answer without upstreams, and a log line for a skipped upstream.
func TestWriteHostFile(t *testing.T) {
	logs := &bytes.Buffer{}
	state := t.TempDir()
	s := &credStore{opts: Options{StateDir: state, UserMode: true}, log: slog.New(slog.NewJSONHandler(logs, nil))}
	path := filepath.Join(state, "git", "hosts.conf")
	if got := hostFilePath(s.opts); got != path {
		t.Fatalf("hostFilePath = %q, want %q under the state folder", got, path)
	}
	s.writeHostFile([]string{"https://github.com", "ftp://x"})
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "[url \"straza::https://github.com/\"]\n") || strings.Contains(string(raw), "ftp") {
		t.Errorf("host file = %q", raw)
	}
	if !strings.Contains(logs.String(), "git upstream that is not valid") || !strings.Contains(logs.String(), "ftp://x") {
		t.Errorf("log has no line for the skipped upstream: %s", logs.String())
	}
	if runtime.GOOS != "windows" {
		for p, want := range map[string]os.FileMode{path: 0o644, filepath.Dir(path): 0o755 | os.ModeDir} {
			if st, err := os.Stat(p); err != nil || st.Mode() != want {
				t.Errorf("%s mode = %v (%v), want %v", p, st.Mode(), err, want)
			}
		}
	}
	s.writeHostFile(nil)
	if raw, err := os.ReadFile(path); err != nil || len(raw) != 0 {
		t.Errorf("after an answer without upstreams the host file is %q (%v), want empty", raw, err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".hosts.conf.tmp-*")); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

// TestWriteHostFileOnlyOnChange pins that the service replaces the host
// file only when its bytes change, so a git that holds the file open on
// Windows cannot fail an update that changes nothing. The same hosts in
// another order render the same bytes.
func TestWriteHostFileOnlyOnChange(t *testing.T) {
	state := t.TempDir()
	s := &credStore{opts: Options{StateDir: state, UserMode: true}, log: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	path := hostFilePath(s.opts)
	rows := []struct {
		name      string
		upstreams []string
		replaced  bool
	}{
		{"the first answer", []string{"https://github.com", "https://ghe.example.com"}, true},
		{"the same answer", []string{"https://github.com", "https://ghe.example.com"}, false},
		{"the same hosts in another order with a repeat", []string{"https://ghe.example.com", "https://github.com", "https://github.com"}, false},
		{"one host less", []string{"https://github.com"}, true},
		{"no hosts", nil, true},
		{"no hosts again", nil, false},
	}
	var before os.FileInfo
	for _, row := range rows {
		s.writeHostFile(row.upstreams)
		after := statOpen(t, path)
		if before != nil && os.SameFile(before, after) == row.replaced {
			t.Errorf("%s: replaced = %v, want %v", row.name, !os.SameFile(before, after), row.replaced)
		}
		before = after
	}
}

// statOpen returns the FileInfo of the file at path read through an open
// handle. On Windows os.Stat reads the file's id only when os.SameFile
// asks, by path, which would name the file there at that later time.
func statOpen(t *testing.T, path string) os.FileInfo {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

// TestHostFilePathSystem pins the system service's path: in its state
// folder on Windows, and the one the rendered wiring names on Linux and
// macOS.
func TestHostFilePathSystem(t *testing.T) {
	state := "/var/lib/straza-service"
	want := map[string]string{"windows": filepath.Join(state, "git", "hosts.conf"),
		"linux": systemHostFile("linux"), "darwin": systemHostFile("darwin")}[runtime.GOOS]
	if got := hostFilePath(Options{StateDir: state}); got != want || got == "" {
		t.Errorf("hostFilePath = %q, want %q, set on linux, darwin and windows", got, want)
	}
}
