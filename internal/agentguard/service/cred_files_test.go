package service

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestCredLockOneServicePerStateDir pins that a second service on the same
// state directory stops with the second-service sentence, and that the
// directory is free again once the first one stops.
func TestCredLockOneServicePerStateDir(t *testing.T) {
	r := newCredRig(t)
	_, err := newCredentials(context.Background(), Options{StateDir: r.dir})
	if err == nil || err.Error() != secondServiceSentence {
		t.Fatalf("second service on the same directory: got %v, want the second-service sentence", err)
	}
	r.restart()
	if r.s == nil {
		t.Fatal("the directory stayed locked after the first service stopped")
	}
}

// TestCredFileModes pins the modes of everything the service keeps:
// directories 0700, and the key, the record, the snapshot, the saved
// sessions, the machine identity and the lock 0600.
func TestCredFileModes(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Unix modes guard the state directory only on Linux and macOS; TestCredStateFolderDACL is the Windows twin")
	}
	r := newCredRig(t)
	machineFrom(t, filepath.Join(t.TempDir(), "absent"))
	r.restart()
	r.enroll(alice)
	if _, err := r.start(alice, "w1"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := r.s.Record(alice, "straza.audit.tool", map[string]any{"session": "s"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	r.waitDrained(t, alice)
	user := filepath.Join(r.dir, "u", alice.UID)
	cases := []struct {
		path string
		mode fs.FileMode
	}{
		{r.dir, 0o700 | fs.ModeDir},
		{filepath.Join(r.dir, "u"), 0o700 | fs.ModeDir},
		{user, 0o700 | fs.ModeDir},
		{filepath.Join(r.dir, "lock"), 0o600},
		{filepath.Join(r.dir, "machine-id"), 0o600},
		{filepath.Join(user, keyFile), 0o600},
		{filepath.Join(user, recordFile), 0o600},
		{filepath.Join(user, snapshotFile), 0o600},
		{filepath.Join(user, sessionsFile), 0o600},
	}
	for _, tc := range cases {
		fi, err := os.Stat(tc.path)
		if err != nil {
			t.Fatalf("stat %s: %v", tc.path, err)
		}
		if fi.Mode() != tc.mode {
			t.Errorf("%s: mode %v, want %v", tc.path, fi.Mode(), tc.mode)
		}
	}
	leftovers, _ := filepath.Glob(filepath.Join(user, ".*.tmp-*"))
	if len(leftovers) != 0 {
		t.Errorf("atomic writes left temp files: %v", leftovers)
	}
}

// TestCredMachineBinding pins the machine binding: the same machine keeps
// using the key across a restart, another machine identity refuses every
// use with the machine-changed sentence, which names the enrolled server,
// and a system without /etc/machine-id keeps one random identity in the
// state directory.
func TestCredMachineBinding(t *testing.T) {
	cases := []struct {
		name    string
		machine string // "" removes the machine id file
		changed bool   // false means the session starts
	}{
		{"same machine", "machine-one\n", false},
		{"cloned machine", "machine-two\n", true},
		{"machine id gone", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCredRig(t)
			r.enroll(alice)
			if tc.machine == "" {
				if err := os.Remove(r.machine); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(r.machine, []byte(tc.machine), 0o600); err != nil {
				t.Fatal(err)
			}
			r.restart()
			_, err := r.start(alice, "w1")
			var refused *Refused
			want := personChangedFor(r.srv.serverURL())
			switch {
			case !tc.changed && err != nil:
				t.Fatalf("start on the same machine: %v", err)
			case tc.changed && (!errors.As(err, &refused) || refused.Sentence != want || refused.Status != http.StatusForbidden):
				t.Fatalf("start after the machine changed: got %v, want 403 %q", err, want)
			}
			st := r.s.Status(alice)
			if st.Enrollment == nil || st.Enrollment.MachineChanged != tc.changed {
				t.Fatalf("status enrollment %+v, want machine_changed %v", st.Enrollment, tc.changed)
			}
		})
	}
}

// TestCredRandomMachineIdentityIsStable pins that a system without
// /etc/machine-id gets one random identity, kept across restarts.
func TestCredRandomMachineIdentityIsStable(t *testing.T) {
	dir := t.TempDir()
	machineFrom(t, filepath.Join(dir, "absent"))
	first, err := machineIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := machineIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatalf("random machine identity changed across reads: %q then %q", first, second)
	}
}

// TestWriteFileAtomicReplaces pins that a write over an existing file
// replaces it whole and leaves no temp file behind, which every counter
// write depends on, on Linux and on Windows alike.
func TestWriteFileAtomicReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), recordFile)
	for _, body := range []string{"first", "second, which is longer than the first"} {
		if err := writeFileAtomic(path, []byte(body)); err != nil {
			t.Fatalf("writeFileAtomic(%q): %v", body, err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != body {
			t.Fatalf("after writing %q the file holds %q, %v", body, got, err)
		}
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".*.tmp-*")); len(leftovers) != 0 {
		t.Errorf("atomic writes left temp files: %v", leftovers)
	}
}
