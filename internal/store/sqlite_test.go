package store

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSQLiteFilesOwnerOnly pins the database's mode: a new database and the
// WAL and SHM files sqlite makes beside it are 0600, and OwnerOnly narrows
// an existing 0644 database and its WAL to 0600 with one log line each.
func TestSQLiteFilesOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps its ACLs")
	}
	modes := func(t *testing.T, path string) {
		t.Helper()
		for _, p := range []string{path, path + "-wal", path + "-shm"} {
			fi, err := os.Stat(p)
			if err != nil {
				t.Fatalf("%s: %v, want sqlite in WAL mode to have made it", filepath.Base(p), err)
			}
			if got := fi.Mode().Perm(); got != 0o600 {
				t.Errorf("%s mode = %v, want -rw-------", filepath.Base(p), got)
			}
		}
	}
	path := filepath.Join(t.TempDir(), "straza.db")
	st, err := openSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.(*sqlStore).db.Exec(`CREATE TABLE mode_probe (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	modes(t, path)

	for _, p := range []string{path, path + "-wal"} {
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var logs bytes.Buffer
	if err := OwnerOnly(path, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
		t.Fatal(err)
	}
	modes(t, path)
	if n := strings.Count(logs.String(), `msg="store: narrowed a database file to owner only"`); n != 2 || !strings.Contains(logs.String(), "was=-rw-r--r--") {
		t.Errorf("log = %q, want one narrowing line for the database and one for its WAL, with the old mode", logs.String())
	}
}
