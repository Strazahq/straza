package trace

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestWriterRotatesAtCap(t *testing.T) {
	dir := stateDir(t)
	defer func(old int64) { MaxBytes = old }(MaxBytes)
	MaxBytes = 2 << 10 // 2 KiB cap so a handful of records rotate

	l := New(dir, "", time.Now())
	for i := 0; i < 100; i++ {
		l.Journal("decision", slog.String("tool", fmt.Sprintf("tool-%03d", i)))
	}
	live := filepath.Join(dir, FileName)
	rotated := live + ".1"
	wantMode := os.FileMode(0o600)
	if runtime.GOOS == "windows" {
		// Windows reports writable files as 0666; FileMode does not expose ACLs.
		wantMode = 0o666
	}
	for _, p := range []string{live, rotated} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("expected %s after 100 records: %v", p, err)
		}
		if fi.Mode().Perm() != wantMode {
			t.Errorf("%s mode = %o, want %04o", p, fi.Mode().Perm(), wantMode)
		}
		// One in-flight record may land past the cap (the stat precedes the
		// append); anything more means rotation is not bounding the file.
		if fi.Size() > MaxBytes+1024 {
			t.Errorf("%s is %d bytes, cap %d", p, fi.Size(), MaxBytes)
		}
	}
	if extra, _ := filepath.Glob(live + ".*"); len(extra) != 1 {
		t.Errorf("want exactly one rotated generation, got %v", extra)
	}
	recs, unreadable := Tail(dir, 0)
	if unreadable != 0 {
		t.Fatalf("unreadable = %d", unreadable)
	}
	if len(recs) == 0 || recs[len(recs)-1].Attrs["tool"] != "tool-099" {
		t.Fatalf("newest record missing after rotation: %d records", len(recs))
	}
	if recs[0].Attrs["tool"] == "tool-000" {
		t.Error("oldest record survived 100 appends under a 2 KiB cap; rotation never dropped it")
	}
}

func TestWriterConcurrentAppendsNeverTear(t *testing.T) {
	dir := stateDir(t)
	l := New(dir, "", time.Now())
	const goroutines, each = 8, 25
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				l.Journal("decision", slog.String("tool", fmt.Sprintf("g%d-%d", g, i)))
			}
		}(g)
	}
	wg.Wait()
	lines := readLinesT(t, dir)
	if len(lines) != goroutines*each {
		t.Fatalf("got %d lines, want %d", len(lines), goroutines*each)
	}
	for i, ln := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(ln), &rec); err != nil {
			t.Fatalf("line %d torn: %v (%q)", i, err, ln)
		}
	}
}

func TestWriterBestEffortWhenStateIsAFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	if err := os.WriteFile(dir, []byte("squatter"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := New(dir, "", time.Now())
	// Nothing below may panic or surface an error: tracing is best-effort.
	l.Journal("decision", slog.String("tool", "shell.exec"))
	l.Debug("adapter")
	if l.Level() != Journal {
		t.Fatalf("level = %s", l.Level())
	}
	raw, err := os.ReadFile(dir)
	if err != nil || string(raw) != "squatter" {
		t.Fatalf("the squatting file was altered: %q %v", raw, err)
	}
	w := &appendWriter{path: filepath.Join(dir, FileName)}
	n, err := w.Write([]byte("x\n"))
	if n != 2 || err != nil {
		t.Fatalf("appendWriter must always report success: n=%d err=%v", n, err)
	}
}
