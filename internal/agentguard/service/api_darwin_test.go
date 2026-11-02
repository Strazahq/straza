//go:build darwin

package service

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// runDirSentence is the refusal of a run folder the service cannot use.
const runDirSentence = "`straza service` cannot use /opt/straza/run, because it is missing, does not belong to _straza-service, or others may change it. " +
	"Install the Straza package again, which creates it, then start the service with `sudo launchctl kickstart system/ai.straza.service`."

// TestListenRunFolder pins the system service's check on the folder of its
// socket, which only the pkg can make: a folder of the service's own uid
// that only it may write is served, and a missing folder, a link to a
// folder, a folder group or others may write, and a folder of another uid
// each stop the service with the run-folder sentence and leave nothing
// behind. A user-mode service makes its own folder, as on Linux.
func TestListenRunFolder(t *testing.T) {
	mkdir := func(mode fs.FileMode) func(*testing.T, string) {
		return func(t *testing.T, dir string) {
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatal(err)
			}
		}
	}
	link := func(t *testing.T, dir string) {
		target := dir + "-target"
		mkdir(0o755)(t, target)
		if err := os.Symlink(target, dir); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		name     string
		make     func(*testing.T, string)
		dir      string // a fixed folder instead of one the row makes
		userMode bool
		refused  bool
	}{
		{name: "a folder of its own", make: mkdir(0o755)},
		{name: "missing", make: func(*testing.T, string) {}, refused: true},
		{name: "a link to a folder of its own", make: link, refused: true},
		{name: "a folder group may write", make: mkdir(0o775), refused: true},
		{name: "a folder others may write", make: mkdir(0o757), refused: true},
		{name: "a folder of another uid", dir: "/usr", refused: true},
		{name: "user mode makes its own folder", make: func(*testing.T, string) {}, userMode: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			dir := row.dir
			if dir == "" {
				dir = filepath.Join(shortTempDir(t), "run")
				row.make(t, dir)
			} else if os.Getuid() == 0 {
				t.Skip("root owns " + dir)
			}
			before, beforeErr := os.Lstat(dir)
			socket := filepath.Join(dir, "service.sock")
			ln, err := listen(Options{Socket: socket, UserMode: row.userMode})
			if !row.refused {
				if err != nil {
					t.Fatalf("listen = %v, want it served", err)
				}
				_ = ln.Close()
				return
			}
			if err == nil {
				_ = ln.Close()
				t.Fatal("listen served, want the run-folder sentence")
			}
			if err.Error() != runDirSentence {
				t.Errorf("listen = %q, want %q", err, runDirSentence)
			}
			after, afterErr := os.Lstat(dir)
			if (beforeErr == nil) != (afterErr == nil) || (afterErr == nil && after.Mode() != before.Mode()) {
				t.Errorf("the folder changed from %v (%v) to %v (%v)", before, beforeErr, after, afterErr)
			}
			if _, err := os.Lstat(socket); err == nil {
				t.Errorf("a refused service left %s", socket)
			}
		})
	}
}
