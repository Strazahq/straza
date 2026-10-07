package spool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// shrinkChunks makes the upload bounds small enough to exercise with
// kilobytes instead of megabytes; restored on cleanup.
func shrinkChunks(t *testing.T, chunkBytes, chunkRecords int, spoolBytes int64) {
	t.Helper()
	ob, or, os_ := drainChunkBytes, drainChunkRecords, maxSpoolBytes
	drainChunkBytes, drainChunkRecords, maxSpoolBytes = chunkBytes, chunkRecords, spoolBytes
	t.Cleanup(func() { drainChunkBytes, drainChunkRecords, maxSpoolBytes = ob, or, os_ })
}

// captureBatchServer stands in for the server's /v1/audit/batch behind the
// Uploader seam: it records per-upload body sizes (the JSON envelope the
// client would send) and every CE id seen. fail(reqNum) can refuse an upload.
type captureBatchServer struct {
	mu        sync.Mutex
	requests  int
	bodySizes []int
	ids       map[string]bool
	fail      func(req int) bool
}

func (s *captureBatchServer) AuditBatch(_ context.Context, _ string, events []json.RawMessage) (int, error) {
	s.mu.Lock()
	s.requests++
	req := s.requests
	s.mu.Unlock()
	if s.fail != nil && s.fail(req) {
		return 0, errors.New("boom")
	}
	raw, _ := json.Marshal(map[string]any{"events": events})
	s.mu.Lock()
	s.bodySizes = append(s.bodySizes, len(raw))
	for _, ev := range events {
		var env struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(ev, &env)
		s.ids[env.ID] = true
	}
	s.mu.Unlock()
	return len(events), nil
}

// toolRecord is the data map spoolAppend (agentguard) builds for a
// claude-code shell.exec decision: same keys and sizes, no Normalized here.
func toolRecord(cmd, effect, session, snapshot string) map[string]any {
	return map[string]any{
		"session":  session,
		"harness":  "claude-code/2.1",
		"event":    "tool.pre",
		"tool":     "shell.exec",
		"app":      "",
		"command":  cmd,
		"effect":   effect,
		"ruleId":   "",
		"reason":   "",
		"snapshot": snapshot,
	}
}

func appendCaptureRecords(t *testing.T, spool *Spool, n, contentBytes int) {
	t.Helper()
	content := strings.Repeat("x", contentBytes)
	for i := 0; i < n; i++ {
		if err := spool.AppendCE("straza.audit.prompt", map[string]any{
			"session":     "s1",
			"harness":     "claude-code/2.1",
			"content":     content,
			"mode":        "verbatim",
			"truncated":   false,
			"contentHash": "sha256:test",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSpoolDrainChunksLargeBacklog pins the poison-pill guard: a backlog
// larger than one server-accepted body drains in bounded chunks instead of
// one refused mega-batch retried forever.
func TestSpoolDrainChunksLargeBacklog(t *testing.T) {
	shrinkChunks(t, 40<<10, 1000, 32<<20)
	spool := NewSpool(filepath.Join(t.TempDir(), "spool.jsonl"))
	appendCaptureRecords(t, spool, 20, 10<<10) // ~205 KiB across a 40 KiB chunk cap

	srv := &captureBatchServer{ids: map[string]bool{}}

	n, err := spool.Drain(context.Background(), srv, "tok")
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if n != 20 || len(srv.ids) != 20 {
		t.Fatalf("drained %d, server saw %d unique, want 20/20", n, len(srv.ids))
	}
	if srv.requests < 5 {
		t.Fatalf("requests = %d, want >= 5 (chunked)", srv.requests)
	}
	for i, size := range srv.bodySizes {
		if size > 40<<10+2048 {
			t.Fatalf("request %d body = %d bytes, exceeds the chunk cap", i, size)
		}
	}
	if p, _ := spool.Pending(); p != 0 {
		t.Fatalf("spool not empty after chunked drain: %d", p)
	}
}

// TestSpoolDrainPartialFailureKeepsRemainder pins mid-drain failure: chunks
// already uploaded may re-send (the server dedupes by CE id); nothing is
// lost, and a healed server receives every record.
func TestSpoolDrainPartialFailureKeepsRemainder(t *testing.T) {
	shrinkChunks(t, 30<<10, 1000, 32<<20)
	spool := NewSpool(filepath.Join(t.TempDir(), "spool.jsonl"))
	appendCaptureRecords(t, spool, 10, 10<<10) // ~4 chunks

	srv := &captureBatchServer{ids: map[string]bool{}, fail: func(req int) bool { return req == 2 }}

	if _, err := spool.Drain(context.Background(), srv, "tok"); err == nil {
		t.Fatal("drain with a failing chunk should error")
	}
	if p, _ := spool.Pending(); p != 10 {
		t.Fatalf("pending after partial failure = %d, want 10 (file kept whole)", p)
	}

	srv.fail = nil
	n, err := spool.Drain(context.Background(), srv, "tok")
	if err != nil {
		t.Fatalf("drain after heal: %v", err)
	}
	if n != 10 || len(srv.ids) != 10 {
		t.Fatalf("healed drain = %d, unique ids = %d, want 10/10", n, len(srv.ids))
	}
	if p, _ := spool.Pending(); p != 0 {
		t.Fatalf("spool not empty after healed drain: %d", p)
	}
}

// TestSpoolCapDropsOldestParkedFiles pins the backlog bound: over the cap,
// the OLDEST parked files are dropped (counted in the audit-dropped marker)
// and the newest survive and upload.
func TestSpoolCapDropsOldestParkedFiles(t *testing.T) {
	shrinkChunks(t, 3<<20, 1000, 30<<10) // 30 KiB spool cap
	dir := t.TempDir()
	spool := NewSpool(filepath.Join(dir, "spool.jsonl"))

	// Three parked files of ~20 KiB each, oldest first by mtime.
	now := time.Now()
	var newestIDs []string
	for i, name := range []string{"audit-pending-aaa.jsonl", "audit-pending-bbb.jsonl", "audit-pending-ccc.jsonl"} {
		var lines []string
		for j := 0; j < 10; j++ {
			id := name + "-" + strconv.Itoa(j)
			lines = append(lines, `{"id":"`+id+`","data":{"content":"`+strings.Repeat("x", 2<<10)+`"}}`)
			if i == 2 {
				newestIDs = append(newestIDs, id)
			}
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(time.Duration(i-3) * time.Hour)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}

	srv := &captureBatchServer{ids: map[string]bool{}}

	n, err := spool.Drain(context.Background(), srv, "tok")
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if n != 10 {
		t.Fatalf("drained %d, want 10 (only the newest file survives the cap)", n)
	}
	for _, id := range newestIDs {
		if !srv.ids[id] {
			t.Fatalf("newest file's record %s was not uploaded", id)
		}
	}
	marker, err := os.ReadFile(filepath.Join(dir, DroppedMarkerName))
	if err != nil {
		t.Fatalf("dropped marker missing: %v", err)
	}
	if !strings.Contains(string(marker), "parked file(s)") {
		t.Fatalf("dropped marker content = %q", marker)
	}
	// The marker a real cap-drop writes must parse back into the totals the
	// doctor check reports (two of the three files fit under no cap here).
	if st := ParseDroppedMarker(marker); st.Files != 2 || st.Events != 1 || st.Last.IsZero() {
		t.Fatalf("parsed marker = %+v, want 2 files / 1 event with a drop time", st)
	}
}

// spoolLineOfLen returns a valid one-line JSON record of exactly n bytes,
// carrying id so the capture server can account for it.
func spoolLineOfLen(t *testing.T, id string, n int) string {
	t.Helper()
	skeleton := `{"id":"` + id + `","p":""}`
	if n < len(skeleton) {
		t.Fatalf("record length %d smaller than the %d-byte skeleton", n, len(skeleton))
	}
	return `{"id":"` + id + `","p":"` + strings.Repeat("x", n-len(skeleton)) + `"}`
}

// TestSpoolDrainMidsizeRecordNeverWedges pins the wedge case at production
// sizes: a record over a 1 MiB scanner cap but under the 3 MiB chunk cap
// would make ReadRecords fail with bufio.ErrTooLong, so EVERY drain would
// abort at that file forever: no upload, no drop, no marker line, and audit
// delivery for the machine silently halted while doctor blamed the server
// check. Under the size invariant (read cap >= the largest chunk-carryable
// record, see spoolReadCap) such a record is carryable and simply uploads.
func TestSpoolDrainMidsizeRecordNeverWedges(t *testing.T) {
	dir := t.TempDir()
	spool := NewSpool(filepath.Join(dir, "spool.jsonl"))
	appendCaptureRecords(t, spool, 1, 2<<20) // 2 MiB: over a 1 MiB scanner cap, under the chunk cap
	appendCaptureRecords(t, spool, 2, 16)    // and normal traffic behind it

	srv := &captureBatchServer{ids: map[string]bool{}}

	n, err := spool.Drain(context.Background(), srv, "tok")
	if err != nil {
		t.Fatalf("Drain wedged on a mid-size record: %v", err)
	}
	if n != 3 || len(srv.ids) != 3 {
		t.Fatalf("drained %d, server saw %d unique, want 3/3 (a 2 MiB record is carryable and must upload)", n, len(srv.ids))
	}
	if p, _ := spool.Pending(); p != 0 {
		t.Fatalf("pending after drain = %d, want 0", p)
	}
	if _, err := os.Stat(filepath.Join(dir, DroppedMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("carryable record was counted dropped (marker stat err = %v)", err)
	}
}

// TestSpoolRecordSizeInvariant pins the ordering that keeps Drain wedge-free
// for EVERY record size: read cap >= largest chunk-carryable record
// (drainChunkBytes-1, dropOversize's keep bound). Every record lands in
// exactly one of two outcomes (upload, or COUNTED drop), and no size is ever
// a read error; records around an oversize line still upload.
func TestSpoolRecordSizeInvariant(t *testing.T) {
	const chunk = 8 << 10
	cases := []struct {
		name       string
		recLen     int
		terminated bool
		uploads    bool // does the sized record itself reach the server?
	}{
		{"largest carryable record uploads", chunk - 1, true, true},
		{"one past carryable is a counted drop", chunk, true, false},
		{"just past the read cap is a counted drop", chunk + 1, true, false},
		{"line past the reader page is a counted drop", 100 << 10, true, false},
		{"giant unterminated tail is a counted drop", (100 << 10) + 7, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shrinkChunks(t, chunk, 1000, 32<<20)
			dir := t.TempDir()
			spool := NewSpool(filepath.Join(dir, "spool.jsonl"))

			big := spoolLineOfLen(t, "big", tc.recLen)
			follower := spoolLineOfLen(t, "after", 64)
			content := big + "\n" + follower + "\n"
			if !tc.terminated { // an unterminated line is by nature the file's last
				content = follower + "\n" + big
			}
			if err := os.WriteFile(filepath.Join(dir, "audit-pending-inv.jsonl"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}

			srv := &captureBatchServer{ids: map[string]bool{}}

			n, err := spool.Drain(context.Background(), srv, "tok")
			if err != nil {
				t.Fatalf("record size must never be a drain error: %v", err)
			}
			wantN := 1
			if tc.uploads {
				wantN = 2
			}
			if n != wantN || !srv.ids["after"] || srv.ids["big"] != tc.uploads {
				t.Fatalf("drained %d (big=%v after=%v), want %d (big=%v after=true)",
					n, srv.ids["big"], srv.ids["after"], wantN, tc.uploads)
			}
			marker, err := os.ReadFile(filepath.Join(dir, DroppedMarkerName))
			if tc.uploads {
				if !os.IsNotExist(err) {
					t.Fatalf("carryable record counted dropped: %v %q", err, marker)
				}
			} else {
				if err != nil {
					t.Fatalf("dropped marker missing: %v", err)
				}
				if st := ParseDroppedMarker(marker); st.Records != 1 || st.Events != 1 {
					t.Fatalf("parsed marker = %+v, want exactly 1 record / 1 event", st)
				}
			}
			if p, _ := spool.Pending(); p != 0 {
				t.Fatalf("pending after drain = %d, want 0", p)
			}
		})
	}
}

// TestSpoolReadCapDominatesCarryableMax pins the size ordering at its
// definition: the read cap is DERIVED from drainChunkBytes (spoolReadCap), so
// every chunk-carryable record (len+1 <= drainChunkBytes) is readable, and
// everything past the cap is by the same inequality oversize: a counted
// drop, never a read error. Two independent constants could cross, and if
// this relation ever breaks, a carryable record becomes unreadable and Drain
// aborts at its file forever.
func TestSpoolReadCapDominatesCarryableMax(t *testing.T) {
	if spoolReadCap() < drainChunkBytes-1 {
		t.Fatalf("spoolReadCap() = %d < drainChunkBytes-1 = %d: a carryable record could fail to read and wedge the drain",
			spoolReadCap(), drainChunkBytes-1)
	}
	// And under a retuned chunk size the derivation must follow.
	shrinkChunks(t, 512, 1000, 32<<20)
	if spoolReadCap() < drainChunkBytes-1 {
		t.Fatalf("spoolReadCap() = %d does not follow a retuned drainChunkBytes = %d", spoolReadCap(), drainChunkBytes)
	}
}

// TestSpoolOversizeCountedOnDiscardNotOnRead: the marker line for an oversize
// record is written when its file is actually REMOVED, not when the drain
// read it; a failed upload keeps the file whole, and counting at read time
// re-counts the same record on every retry (doctor turns marker totals into
// a FAIL an operator must chase).
func TestSpoolOversizeCountedOnDiscardNotOnRead(t *testing.T) {
	shrinkChunks(t, 4<<10, 1000, 32<<20)
	dir := t.TempDir()
	spool := NewSpool(filepath.Join(dir, "spool.jsonl"))
	appendCaptureRecords(t, spool, 1, 8<<10) // oversize for a 4 KiB chunk
	appendCaptureRecords(t, spool, 2, 16)    // plus uploadable records

	srv := &captureBatchServer{ids: map[string]bool{}, fail: func(int) bool { return true }}
	if _, err := spool.Drain(context.Background(), srv, "tok"); err == nil {
		t.Fatal("drain against a refusing server should fail")
	}
	if _, err := os.Stat(filepath.Join(dir, DroppedMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("oversize record counted dropped while its file is still on disk (stat err = %v)", err)
	}

	srv.fail = nil
	if _, err := spool.Drain(context.Background(), srv, "tok"); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(dir, DroppedMarkerName))
	if err != nil {
		t.Fatalf("dropped marker missing after the record's file was removed: %v", err)
	}
	if st := ParseDroppedMarker(marker); st.Records != 1 || st.Events != 1 {
		t.Fatalf("parsed marker = %+v, want exactly 1 record / 1 event across both drains", st)
	}
}

// TestSpoolDropsOversizeRecord pins the unuploadable-record guard: a record
// larger than a whole chunk is dropped (marker counted) instead of wedging
// its file forever.
func TestSpoolDropsOversizeRecord(t *testing.T) {
	shrinkChunks(t, 4<<10, 1000, 32<<20)
	dir := t.TempDir()
	spool := NewSpool(filepath.Join(dir, "spool.jsonl"))

	appendCaptureRecords(t, spool, 1, 8<<10) // record > 4 KiB chunk: unuploadable
	if err := spool.AppendCE("straza.audit.tool", toolRecord("ok-1", "allow", "s1", "snap")); err != nil {
		t.Fatal(err)
	}
	if err := spool.AppendCE("straza.audit.tool", toolRecord("ok-2", "allow", "s1", "snap")); err != nil {
		t.Fatal(err)
	}

	srv := &captureBatchServer{ids: map[string]bool{}}

	n, err := spool.Drain(context.Background(), srv, "tok")
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if n != 2 || len(srv.ids) != 2 {
		t.Fatalf("drained %d, server saw %d, want 2/2 (oversize dropped)", n, len(srv.ids))
	}
	if p, _ := spool.Pending(); p != 0 {
		t.Fatalf("spool not empty: %d (oversize record must not wedge the file)", p)
	}
	marker, err := os.ReadFile(filepath.Join(dir, DroppedMarkerName))
	if err != nil {
		t.Fatalf("dropped marker missing: %v", err)
	}
	if !strings.Contains(string(marker), "oversize record") {
		t.Fatalf("dropped marker content = %q", marker)
	}
	if st := ParseDroppedMarker(marker); st.Records != 1 || st.Events != 1 {
		t.Fatalf("parsed marker = %+v, want 1 record / 1 event", st)
	}
}
