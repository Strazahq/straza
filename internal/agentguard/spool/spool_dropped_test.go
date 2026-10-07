package spool

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestParseDroppedMarker pins the audit-dropped marker grammar: every line
// noteDropped ever wrote (plain and compacted) parses back into totals, and a
// foreign line degrades to "one drop event, unknown size" instead of being
// silently ignored: the marker exists to make loss visible, so an
// unparseable line must still count as loss.
func TestParseDroppedMarker(t *testing.T) {
	ts1 := "2026-07-29T10:00:00Z"
	ts2 := "2026-07-30T04:05:06Z"
	cases := []struct {
		name     string
		raw      string
		events   int
		records  int
		files    int
		last     string // RFC3339, "" = zero time
		lastLine string
	}{
		{"empty", "", 0, 0, 0, "", ""},
		{"blank lines only", "\n\n", 0, 0, 0, "", ""},
		{"oversize records",
			ts1 + " dropped 5 oversize record(s)\n",
			1, 5, 0, ts1, ts1 + " dropped 5 oversize record(s)"},
		{"parked files with cap bytes not miscounted",
			ts1 + " dropped 2 parked file(s) over the 30720-byte spool cap\n",
			1, 0, 2, ts1, ts1 + " dropped 2 parked file(s) over the 30720-byte spool cap"},
		{"mixed lines sum and the last line wins",
			ts1 + " dropped 2 parked file(s) over the 30720-byte spool cap\n" +
				ts2 + " dropped 1 oversize record(s)\n",
			2, 1, 2, ts2, ts2 + " dropped 1 oversize record(s)"},
		{"compacted summary line carries its event span",
			ts2 + " dropped 7 oversize record(s) and 3 parked file(s) spanning 9 drop event(s) [compacted]\n",
			9, 7, 3, ts2, ts2 + " dropped 7 oversize record(s) and 3 parked file(s) spanning 9 drop event(s) [compacted]"},
		{"compacted summary plus later plain lines",
			ts1 + " dropped 7 oversize record(s) and 3 parked file(s) spanning 9 drop event(s) [compacted]\n" +
				ts2 + " dropped 4 parked file(s) over the 33554432-byte spool cap\n",
			10, 7, 7, ts2, ts2 + " dropped 4 parked file(s) over the 33554432-byte spool cap"},
		{"foreign line still counts as one drop event",
			"something ate the spool\n",
			1, 0, 0, "", "something ate the spool"},
		{"out-of-order times keep the newest",
			ts2 + " dropped 1 oversize record(s)\n" +
				ts1 + " dropped 1 oversize record(s)\n",
			2, 2, 0, ts2, ts1 + " dropped 1 oversize record(s)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := ParseDroppedMarker([]byte(tc.raw))
			if st.Events != tc.events || st.Records != tc.records || st.Files != tc.files {
				t.Fatalf("events/records/files = %d/%d/%d, want %d/%d/%d",
					st.Events, st.Records, st.Files, tc.events, tc.records, tc.files)
			}
			wantLast := time.Time{}
			if tc.last != "" {
				wantLast, _ = time.Parse(time.RFC3339, tc.last)
			}
			if !st.Last.Equal(wantLast) {
				t.Fatalf("last = %v, want %v", st.Last, wantLast)
			}
			if st.LastLine != tc.lastLine {
				t.Fatalf("lastLine = %q, want %q", st.LastLine, tc.lastLine)
			}
		})
	}
}

// TestNoteDroppedCompactionBoundsMarker pins the marker's own bound: a box
// that stays over the spool cap appends one line per drain, and the marker
// must not become its own unbounded file. Past markerCompactBytes it folds
// into one summary line that PRESERVES the running totals; bounding the file
// must never shrink the evidence of how much audit was lost.
func TestNoteDroppedCompactionBoundsMarker(t *testing.T) {
	old := markerCompactBytes
	markerCompactBytes = 512
	t.Cleanup(func() { markerCompactBytes = old })

	dir := t.TempDir()
	for i := 0; i < 100; i++ {
		noteDropped(dir, "1 oversize record(s)")
	}
	raw, err := os.ReadFile(filepath.Join(dir, DroppedMarkerName))
	if err != nil {
		t.Fatalf("marker missing after drops: %v", err)
	}
	if int64(len(raw)) > 2*markerCompactBytes {
		t.Fatalf("marker grew to %d bytes despite the %d-byte compaction bound", len(raw), markerCompactBytes)
	}
	st := ParseDroppedMarker(raw)
	if st.Records != 100 || st.Events != 100 {
		t.Fatalf("totals after compaction = %d record(s) / %d event(s), want 100/100 (compaction lost evidence)", st.Records, st.Events)
	}
	if st.Last.IsZero() {
		t.Fatal("last-drop time lost in compaction")
	}
	if !strings.Contains(string(raw), "[compacted]") {
		t.Fatalf("marker over the bound never compacted: %q", raw)
	}
	claims, _ := filepath.Glob(filepath.Join(dir, DroppedMarkerName) + DroppedCompactingSuffix + "*")
	if len(claims) != 0 {
		t.Fatalf("compaction left claim files behind: %v", claims)
	}
}

// TestCompactionClaimPreservesRacingAppend pins the no-truncation contract
// under the cross-process interleave that would otherwise erase evidence: compaction
// CLAIMS the oversized marker by atomic rename, so an appender either landed
// before the rename (inside the claim, counted by its summary) or O_CREATEs
// a fresh live marker whose line survives beside the appended summary. No
// interleaving may lose a line; compaction never truncates the live path.
func TestCompactionClaimPreservesRacingAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DroppedMarkerName)
	if err := os.WriteFile(path,
		[]byte("2026-07-29T10:00:00Z dropped 2 oversize record(s)\n"+
			"2026-07-29T11:00:00Z dropped 3 parked file(s) over the 512-byte spool cap\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	claim := path + DroppedCompactingSuffix + "test"
	if err := os.Rename(path, claim); err != nil { // the compactor's claim
		t.Fatal(err)
	}
	// A racing appender in another process lands AFTER the claim: O_APPEND|
	// O_CREATE recreates the live marker (the raw write noteDropped does).
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	racing := "2026-07-30T04:05:06Z dropped 4 oversize record(s)\n"
	if _, err := f.WriteString(racing); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	foldOneClaim(path, claim)

	if _, err := os.Stat(claim); !os.IsNotExist(err) {
		t.Fatalf("claim not removed after fold: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), strings.TrimSuffix(racing, "\n")) {
		t.Fatalf("racing append lost by the fold: %q", raw)
	}
	st := ParseDroppedMarker(raw)
	if st.Records != 6 || st.Files != 3 || st.Events != 3 {
		t.Fatalf("fold totals = %d records / %d files / %d events, want 6/3/3 (claimed + racing)", st.Records, st.Files, st.Events)
	}
}

// writeParkedFiles writes parked spool files of size bytes each into dir,
// mtimes oldest-first in slice order, returning their paths.
func writeParkedFiles(t *testing.T, dir string, names []string, size int) []string {
	t.Helper()
	now := time.Now()
	var paths []string
	for i, name := range names {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Repeat("x", size-1)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(time.Duration(i-len(names)) * time.Hour)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}

// TestEnforceSpoolCapCountsOnlyRealRemovals pins the accounting rule: a
// "dropped" parked file is one THIS process's os.Remove actually discarded.
// A refused removal (a Windows handle holding the file) means the file
// SURVIVED: it stays in the drain list, uploads normally, and is never
// counted; IsNotExist means a concurrent drainer already discarded (and
// counted) it: gone, but not this process's drop. The marker exists to make
// real loss visible, and counting survivors would turn the doctor
// audit-dropped check into false FAILs.
func TestEnforceSpoolCapCountsOnlyRealRemovals(t *testing.T) {
	names := []string{"audit-pending-aaa.jsonl", "audit-pending-bbb.jsonl", "audit-pending-ccc.jsonl"}
	lockErr := func(p string) error {
		return &fs.PathError{Op: "remove", Path: p, Err: errors.New("sharing violation")}
	}
	cases := []struct {
		name      string
		remove    func(oldest string) func(string) error // nil = real os.Remove
		wantFiles int                                    // marker total: files actually dropped
		wantKept  []string                               // surviving drain list, in order
	}{
		{"every removal real: both evictions counted", nil, 2,
			[]string{"audit-pending-ccc.jsonl"}},
		{"locked file survives: uncounted and still drained",
			func(oldest string) func(string) error {
				return func(p string) error {
					if p == oldest {
						return lockErr(p)
					}
					return os.Remove(p)
				}
			}, 1,
			[]string{"audit-pending-aaa.jsonl", "audit-pending-ccc.jsonl"}},
		{"already removed by a concurrent drainer: not this process's drop",
			func(oldest string) func(string) error {
				return func(p string) error {
					if p == oldest {
						_ = os.Remove(p)
						return &fs.PathError{Op: "remove", Path: p, Err: fs.ErrNotExist}
					}
					return os.Remove(p)
				}
			}, 1,
			[]string{"audit-pending-ccc.jsonl"}},
		{"no removal succeeds: no loss recorded at all",
			func(string) func(string) error {
				return func(p string) error { return lockErr(p) }
			}, 0,
			names},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldCap := maxSpoolBytes
			maxSpoolBytes = 30 << 10
			t.Cleanup(func() { maxSpoolBytes = oldCap })
			dir := t.TempDir()
			paths := writeParkedFiles(t, dir, names, 20<<10) // 60 KiB over a 30 KiB cap
			if tc.remove != nil {
				oldRm := removeSpoolFile
				removeSpoolFile = tc.remove(paths[0])
				t.Cleanup(func() { removeSpoolFile = oldRm })
			}

			kept := enforceSpoolCap(dir, paths)

			var keptNames []string
			for _, p := range kept {
				keptNames = append(keptNames, filepath.Base(p))
			}
			if strings.Join(keptNames, " ") != strings.Join(tc.wantKept, " ") {
				t.Fatalf("kept = %v, want %v", keptNames, tc.wantKept)
			}
			marker, err := os.ReadFile(filepath.Join(dir, DroppedMarkerName))
			if tc.wantFiles == 0 {
				if !os.IsNotExist(err) {
					t.Fatalf("no file was discarded, yet loss was recorded: %v %q", err, marker)
				}
				return
			}
			if err != nil {
				t.Fatalf("dropped marker missing: %v", err)
			}
			if st := ParseDroppedMarker(marker); st.Files != tc.wantFiles || st.Events != 1 {
				t.Fatalf("parsed marker = %+v, want %d file(s) / 1 event", st, tc.wantFiles)
			}
		})
	}
}

// TestSpoolLockedFileNotCountedAndDrainsNextPass pins the end-to-end case of
// a parked file whose removal fails (Windows: an open handle survives
// os.Remove) while its records are still on disk and upload fine. Nothing
// about it is counted: it stays in the drain list, its records reach the server, and the
// file simply retries removal on a later pass (re-uploads dedupe by CE id):
// zero loss AND zero false loss reports.
func TestSpoolLockedFileNotCountedAndDrainsNextPass(t *testing.T) {
	shrinkChunks(t, 3<<20, 1000, 30<<10)
	dir := t.TempDir()
	spool := NewSpool(filepath.Join(dir, "spool.jsonl"))

	// Two 20 KiB parked files over the 30 KiB cap; eviction wants the oldest.
	names := []string{"audit-pending-old.jsonl", "audit-pending-new.jsonl"}
	now := time.Now()
	for i, name := range names {
		var b strings.Builder
		for j := 0; j < 10; j++ {
			b.WriteString(spoolLineOfLen(t, name+"-"+strconv.Itoa(j), 2<<10))
			b.WriteString("\n")
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(time.Duration(i-2) * time.Hour)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	locked := filepath.Join(dir, names[0])
	oldRm := removeSpoolFile
	removeSpoolFile = func(p string) error {
		if p == locked {
			return &fs.PathError{Op: "remove", Path: p, Err: errors.New("sharing violation")}
		}
		return os.Remove(p)
	}
	t.Cleanup(func() { removeSpoolFile = oldRm })

	srv := &captureBatchServer{ids: map[string]bool{}}

	n, err := spool.Drain(context.Background(), srv, "tok")
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if n != 20 || len(srv.ids) != 20 {
		t.Fatalf("drained %d, server saw %d unique, want 20/20 (the locked file must still upload)", n, len(srv.ids))
	}
	if _, err := os.Stat(locked); err != nil {
		t.Fatalf("locked file should have survived its refused removal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, DroppedMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("a file that survived was recorded as dropped (marker stat err = %v)", err)
	}
	if p, _ := spool.Pending(); p != 10 {
		t.Fatalf("pending = %d, want 10 (the surviving file's records)", p)
	}

	// The lock clears; the next pass delivers and removes it, still no drop.
	removeSpoolFile = oldRm
	n, err = spool.Drain(context.Background(), srv, "tok")
	if err != nil || n != 10 {
		t.Fatalf("next-pass drain = %d, %v; want 10", n, err)
	}
	if len(srv.ids) != 20 {
		t.Fatalf("server unique ids = %d, want 20 (re-upload dedupes)", len(srv.ids))
	}
	if p, _ := spool.Pending(); p != 0 {
		t.Fatalf("pending after next pass = %d, want 0", p)
	}
	if _, err := os.Stat(filepath.Join(dir, DroppedMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("delivered audit ended up recorded as dropped (marker stat err = %v)", err)
	}
}

// TestSpoolConcurrentDrainerRemovalNotDoubleCounted: two drainers can
// legally drain the same parked file (Drain's documented model). The loser's
// os.Remove sees IsNotExist: the winner's pass uploaded the same records and
// its removal did the oversize accounting. The loser must record NOTHING, or
// every oversize drop near a drain race is counted twice.
func TestSpoolConcurrentDrainerRemovalNotDoubleCounted(t *testing.T) {
	shrinkChunks(t, 4<<10, 1000, 32<<20)
	dir := t.TempDir()
	spool := NewSpool(filepath.Join(dir, "spool.jsonl"))
	target := filepath.Join(dir, "audit-pending-race.jsonl")
	content := spoolLineOfLen(t, "big", 8<<10) + "\n" + // oversize for a 4 KiB chunk
		spoolLineOfLen(t, "g1", 64) + "\n" + spoolLineOfLen(t, "g2", 64) + "\n"
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	oldRm := removeSpoolFile
	removeSpoolFile = func(p string) error {
		if p == target { // the winning drainer got here between our upload and our remove
			_ = os.Remove(p)
			return &fs.PathError{Op: "remove", Path: p, Err: fs.ErrNotExist}
		}
		return os.Remove(p)
	}
	t.Cleanup(func() { removeSpoolFile = oldRm })

	srv := &captureBatchServer{ids: map[string]bool{}}

	n, err := spool.Drain(context.Background(), srv, "tok")
	if err != nil || n != 2 {
		t.Fatalf("Drain = %d, %v; want 2 uploads and no error", n, err)
	}
	if _, err := os.Stat(filepath.Join(dir, DroppedMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("losing drainer recorded the winner's drop too (marker stat err = %v)", err)
	}
	if p, _ := spool.Pending(); p != 0 {
		t.Fatalf("pending = %d, want 0", p)
	}
}

// TestStaleClaimFoldedOnNextDrop pins crash resilience: a claim orphaned by
// a crash between the rename and the summary append is folded back into the
// live marker on the next drop: the ledger reappears instead of rotting in
// a file only doctor reads.
func TestStaleClaimFoldedOnNextDrop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DroppedMarkerName)
	claim := path + DroppedCompactingSuffix + "crashed"
	if err := os.WriteFile(claim,
		[]byte("2026-07-29T10:00:00Z dropped 1 oversize record(s)\n"+
			"2026-07-29T11:00:00Z dropped 2 parked file(s) over the 512-byte spool cap\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	noteDropped(dir, "5 oversize record(s)")

	claims, _ := filepath.Glob(path + DroppedCompactingSuffix + "*")
	if len(claims) != 0 {
		t.Fatalf("stale claim not folded: %v", claims)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	st := ParseDroppedMarker(raw)
	if st.Records != 6 || st.Files != 2 || st.Events != 3 {
		t.Fatalf("totals after recovery = %d records / %d files / %d events, want 6/2/3", st.Records, st.Files, st.Events)
	}
}
