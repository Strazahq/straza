package trace

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTailReadsRotatedFirstAndLimits(t *testing.T) {
	dir := stateDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(dir, FileName)
	rotated := live + ".1"
	mk := func(i int) string {
		return `{"ts":"2026-08-22T10:00:0` + string(rune('0'+i)) + `Z","level":"INFO","msg":"decision","v":1,"tool":"t` + string(rune('0'+i)) + `"}`
	}
	if err := os.WriteFile(rotated, []byte(mk(0)+"\n"+mk(1)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte(mk(2)+"\n{torn\n"+mk(3)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	recs, unreadable := Tail(dir, 0)
	if unreadable != 1 {
		t.Fatalf("unreadable = %d, want 1", unreadable)
	}
	if len(recs) != 4 {
		t.Fatalf("records = %d, want 4", len(recs))
	}
	for i, r := range recs {
		if r.Attrs["tool"] != "t"+string(rune('0'+i)) {
			t.Errorf("record %d out of order: %+v", i, r)
		}
		if _, leaked := r.Attrs["ts"]; leaked {
			t.Error("ts must not appear in Attrs")
		}
		if _, leaked := r.Attrs["msg"]; leaked {
			t.Error("msg must not appear in Attrs")
		}
		if r.Msg != "decision" || r.Level != "INFO" {
			t.Errorf("record %d mangled: %+v", i, r)
		}
	}
	if recs[0].TS.Location() != time.UTC || recs[0].TS.Second() != 0 {
		t.Errorf("ts not parsed UTC: %v", recs[0].TS)
	}
	// The last two lines are the torn one and t3: a tail of 2 keeps t3 and
	// counts the torn line.
	recs, unreadable = Tail(dir, 2)
	if len(recs) != 1 || unreadable != 1 || recs[0].Attrs["tool"] != "t3" {
		t.Fatalf("Tail(2) = %d records (%v), unreadable %d", len(recs), recs, unreadable)
	}
	recs, _ = Tail(dir, 3)
	if len(recs) != 2 || recs[0].Attrs["tool"] != "t2" {
		t.Fatalf("Tail(3) = %v", recs)
	}
	// Absent files: empty, no error.
	if recs, unreadable := Tail(filepath.Join(t.TempDir(), "nope"), 0); len(recs) != 0 || unreadable != 0 {
		t.Fatalf("absent dir: %v %d", recs, unreadable)
	}
}

func TestRenderRecordExact(t *testing.T) {
	ts := time.Date(2026, 8, 22, 10, 41, 2, 123000000, time.UTC)
	r := Record{TS: ts, Level: "INFO", Msg: "decision", Attrs: map[string]any{
		"v":           json.Number("1"),
		"tool":        "shell.exec",
		"effect":      "deny",
		"rule":        "no rm rf",
		"duration_ms": json.Number("12.5"),
		"fail_closed": true,
		"empty":       "",
	}}
	want := `2026-08-22T10:41:02Z INFO decision duration_ms=12.5 effect=deny empty="" fail_closed=true rule="no rm rf" tool=shell.exec v=1`
	if got := Render(r); got != want {
		t.Fatalf("Render =\n%s\nwant\n%s", got, want)
	}
	if got := Render(Record{}); got != "- - -" {
		t.Fatalf("zero record = %q", got)
	}
}

func TestSummarize(t *testing.T) {
	dir := stateDir(t)
	if s := Summarize(dir); s.Records != 0 || s.Last != nil || s.Size != 0 {
		t.Fatalf("empty summary = %+v", s)
	}
	l := New(dir, "", time.Now())
	l.Journal("decision", slog.String("tool", "a"))
	l.Journal("decision", slog.String("tool", "b"))
	if err := os.WriteFile(filepath.Join(dir, FileName+".1"), []byte("{torn\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Summarize(dir)
	if s.Records != 2 || s.Unreadable != 1 || s.Last == nil || s.Last.Attrs["tool"] != "b" {
		t.Fatalf("summary = %+v", s)
	}
	fi, _ := os.Stat(filepath.Join(dir, FileName))
	if s.Size != fi.Size()+int64(len("{torn\n")) {
		t.Fatalf("size = %d", s.Size)
	}
	if !strings.Contains(Render(*s.Last), "tool=b") {
		t.Fatalf("last record render = %q", Render(*s.Last))
	}
	// A trailing debug line never displaces the newest journal record: the
	// doctor and `trace status` name the last decision, not the last breadcrumb.
	dl := New(dir, "debug", time.Now())
	dl.Debug("escalate", slog.String("branch", "approve"))
	s = Summarize(dir)
	if s.Records != 3 || s.Last == nil || s.Last.Msg != "decision" || s.Last.Attrs["tool"] != "b" {
		t.Fatalf("summary after a debug line = %+v (last %+v)", s, s.Last)
	}
	// With no journal record at all, the newest record of any level stands in.
	only := stateDir(t)
	New(only, "debug", time.Now()).Debug("adapter", slog.String("harness", "codex"))
	if s := Summarize(only); s.Records != 1 || s.Last == nil || s.Last.Msg != "adapter" {
		t.Fatalf("debug-only summary = %+v", s)
	}
}
