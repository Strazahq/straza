package agentguard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testErrorStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("STRAZA_HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestErrorLogAppendAndRead(t *testing.T) {
	store := testErrorStore(t)

	if errs, unreadable := ClientErrors(store); len(errs) != 0 || unreadable != 0 {
		t.Fatalf("fresh home: got %d errors, %d unreadable", len(errs), unreadable)
	}

	store.logClientError(ClientError{Kind: "hook", Harness: "codex", Event: "Stop", Err: "boom", Exit: 2})
	store.logClientError(ClientError{Kind: "drain", Err: "server unreachable"})

	errs, unreadable := ClientErrors(store)
	if unreadable != 0 {
		t.Fatalf("unreadable = %d, want 0", unreadable)
	}
	if len(errs) != 2 {
		t.Fatalf("got %d records, want 2", len(errs))
	}
	first := errs[0]
	if first.V != 1 || first.Kind != "hook" || first.Harness != "codex" ||
		first.Event != "Stop" || first.Err != "boom" || first.Exit != 2 {
		t.Errorf("record round-trip mangled: %+v", first)
	}
	if first.TS.IsZero() || first.TS.Location() != time.UTC {
		t.Errorf("timestamp not stamped UTC: %v", first.TS)
	}
	if first.PID == 0 || first.Bin == "" {
		t.Errorf("bin/pid not stamped: %+v", first)
	}
	if errs[1].Kind != "drain" {
		t.Errorf("order not oldest-first: %+v", errs)
	}
}

func TestErrorLogRotationBounds(t *testing.T) {
	store := testErrorStore(t)
	defer func(old int64) { errorLogMaxBytes = old }(errorLogMaxBytes)
	errorLogMaxBytes = 2 << 10 // 2 KiB cap so a handful of records rotate

	for i := 0; i < 100; i++ {
		store.logClientError(ClientError{Kind: "hook", Err: fmt.Sprintf("failure %03d", i)})
	}

	live, rotated := store.ErrorLogPath(), store.ErrorLogPath()+".1"
	for _, p := range []string{live, rotated} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("expected %s to exist after 100 appends: %v", p, err)
		}
		// One in-flight record may land past the cap (the check precedes the
		// append); anything more means rotation is not bounding the file.
		if fi.Size() > errorLogMaxBytes+1024 {
			t.Errorf("%s is %d bytes, cap %d", p, fi.Size(), errorLogMaxBytes)
		}
	}
	if extra, _ := filepath.Glob(live + ".*"); len(extra) != 1 {
		t.Errorf("want exactly one rotated generation, got %v", extra)
	}

	// Newest record survives; the oldest was rotated away then dropped.
	errs, _ := ClientErrors(store)
	if len(errs) == 0 || errs[len(errs)-1].Err != "failure 099" {
		t.Fatalf("newest record missing after rotation: %d records", len(errs))
	}
	if errs[0].Err == "failure 000" {
		t.Error("oldest record survived 100 appends under a 2 KiB cap. Rotation never dropped it")
	}
}

func TestErrorLogConcurrentAppends(t *testing.T) {
	store := testErrorStore(t)
	const goroutines, each = 8, 25

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				store.logClientError(ClientError{Kind: "hook", Err: fmt.Sprintf("g%d-%d", g, i)})
			}
		}(g)
	}
	wg.Wait()

	// Every line must parse. O_APPEND single-write records may interleave in
	// order but never tear into invalid JSON.
	raw, err := os.ReadFile(store.ErrorLogPath())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != goroutines*each {
		t.Fatalf("got %d lines, want %d", len(lines), goroutines*each)
	}
	for i, line := range lines {
		var e ClientError
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %d torn: %v (%q)", i, err, line)
		}
	}
}

func TestErrorLogTruncatesLongErrors(t *testing.T) {
	store := testErrorStore(t)
	store.logClientError(ClientError{Kind: "hook", Err: strings.Repeat("x", 10*errorLogMaxErrBytes)})
	errs, _ := ClientErrors(store)
	if len(errs) != 1 {
		t.Fatalf("got %d records, want 1", len(errs))
	}
	if n := len(errs[0].Err); n > errorLogMaxErrBytes+len(errorLogTruncSuffix) {
		t.Errorf("err survived at %d bytes, cap %d", n, errorLogMaxErrBytes)
	}
	if !strings.HasSuffix(errs[0].Err, errorLogTruncSuffix) {
		t.Error("truncated err does not say it was truncated")
	}
}

func TestErrorLogBestEffort(t *testing.T) {
	// state exists as a regular FILE: MkdirAll fails, so the writer must
	// silently no-op: never panic, never influence the caller.
	t.Setenv("STRAZA_HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := Home()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.logClientError(ClientError{Kind: "hook", Err: "boom"}) // must not panic
	if errs, _ := ClientErrors(store); len(errs) != 0 {
		t.Fatalf("impossible read-back: %+v", errs)
	}
}

func TestClientErrorsSkipsGarbageLines(t *testing.T) {
	store := testErrorStore(t)
	store.logClientError(ClientError{Kind: "hook", Err: "real"})
	f, err := os.OpenFile(store.ErrorLogPath(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{torn half-record\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	store.logClientError(ClientError{Kind: "hook", Err: "after"})

	errs, unreadable := ClientErrors(store)
	if len(errs) != 2 || unreadable != 1 {
		t.Fatalf("got %d records + %d unreadable, want 2 + 1", len(errs), unreadable)
	}
}

func TestRenderClientError(t *testing.T) {
	ts := time.Date(2026, 7, 30, 21, 14, 9, 0, time.UTC)
	tests := []struct {
		name string
		in   ClientError
		want string
	}{
		{"full", ClientError{TS: ts, Kind: "hook", Harness: "codex", Event: "Stop", Err: "boom", Exit: 2},
			"2026-07-30T21:14:09Z hook codex Stop: boom (exit 2)"},
		{"no harness/event/exit", ClientError{TS: ts, Kind: "drain", Err: "unreachable"},
			"2026-07-30T21:14:09Z drain - -: unreachable"},
	}
	for _, tc := range tests {
		if got := RenderClientError(tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestClientErrorsCheck(t *testing.T) {
	tests := []struct {
		name       string
		records    []ClientError
		wantCheck  bool
		wantStatus string
		wantDetail string
	}{
		{"no log ever written: no line", nil, false, "", ""},
		{"recent error: warn", []ClientError{
			{TS: time.Now().UTC().Add(-time.Hour), Kind: "hook", Harness: "codex", Event: "Stop", Err: "boom", Exit: 2}},
			true, checkWarn, "1 client error(s) in the last 24h"},
		{"stale errors only: ok", []ClientError{
			{TS: time.Now().UTC().Add(-48 * time.Hour), Kind: "hook", Err: "old"}},
			true, checkOK, "none in the last 24h"},
		{"mixed picks the recent count and last line", []ClientError{
			{TS: time.Now().UTC().Add(-48 * time.Hour), Kind: "hook", Err: "old"},
			{TS: time.Now().UTC().Add(-time.Minute), Kind: "spool", Err: "disk full"},
			{TS: time.Now().UTC().Add(-time.Second), Kind: "hook", Harness: "codex", Event: "Stop", Err: "newest"}},
			true, checkWarn, "2 client error(s) in the last 24h"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := testErrorStore(t)
			for _, r := range tc.records {
				store.logClientError(r)
			}
			got := clientErrorsCheck(store, time.Now())
			if !tc.wantCheck {
				if got != nil {
					t.Fatalf("want no check, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("want a check, got none")
			}
			if got.Name != "client-errors" {
				t.Errorf("name = %q", got.Name)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q (detail %q)", got.Status, tc.wantStatus, got.Detail)
			}
			if !strings.Contains(got.Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to mention %q", got.Detail, tc.wantDetail)
			}
			if got.Status == checkWarn {
				if !strings.Contains(got.Detail, "newest") && !strings.Contains(got.Detail, "boom") {
					t.Errorf("warn detail does not show the last error: %q", got.Detail)
				}
				if !strings.Contains(got.Hint, "straza logs") {
					t.Errorf("hint does not point at `straza logs`: %q", got.Hint)
				}
			}
		})
	}
}

// TestDoctorSurfacesClientErrorsUnenrolled pins the early-return gap: an
// un-enrolled box (wiped config, live wiring) is exactly where hook failures
// pile up, and doctor must still show them there.
func TestDoctorSurfacesClientErrorsUnenrolled(t *testing.T) {
	store := testErrorStore(t)
	store.logClientError(ClientError{Kind: "hook", Harness: "codex", Err: "boom", Exit: 2})
	checks := Doctor(context.Background(), store)
	found := false
	for _, c := range checks {
		if c.Name == "client-errors" && c.Status == checkWarn {
			found = true
		}
	}
	if !found {
		t.Fatalf("un-enrolled doctor run hid the client errors: %+v", checks)
	}
}

func TestLogSpoolError(t *testing.T) {
	store := testErrorStore(t)
	n := Normalized{HarnessName: "codex", NativeEvent: "PreToolUse"}
	store.logSpoolError(n, nil) // nil error must be a no-op
	if errs, _ := ClientErrors(store); len(errs) != 0 {
		t.Fatalf("nil error logged: %+v", errs)
	}
	store.logSpoolError(n, fmt.Errorf("disk full"))
	errs, _ := ClientErrors(store)
	if len(errs) != 1 || errs[0].Kind != "spool" || errs[0].Harness != "codex" ||
		errs[0].Event != "PreToolUse" || errs[0].Err != "disk full" {
		t.Fatalf("spool error record wrong: %+v", errs)
	}
}
