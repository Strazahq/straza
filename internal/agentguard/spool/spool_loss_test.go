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

// orderedServer stands in for /v1/audit/batch and keeps every upload it
// accepted in order. refuse(n) refuses the n-th upload, counted from 1, and
// tried records every upload, the refused ones included.
type orderedServer struct {
	mu       sync.Mutex
	n        int
	refuse   func(n int) bool
	accepted [][]json.RawMessage
	tried    [][]json.RawMessage
}

func (s *orderedServer) AuditBatch(_ context.Context, _ string, events []json.RawMessage) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	batch := append([]json.RawMessage(nil), events...)
	s.tried = append(s.tried, batch)
	if s.refuse != nil && s.refuse(s.n) {
		return 0, errors.New("HTTP 429: audit ingest rate limited; the spool retries")
	}
	s.accepted = append(s.accepted, batch)
	return len(events), nil
}

// lossCE is the part of a loss record the tests read.
type lossCE struct {
	ID   string         `json:"id"`
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// lossOf decodes rec and reports whether it is a loss record.
func lossOf(t *testing.T, rec json.RawMessage) (lossCE, bool) {
	t.Helper()
	var ce lossCE
	if err := json.Unmarshal(rec, &ce); err != nil {
		t.Fatalf("an uploaded record is not JSON: %v", err)
	}
	return ce, ce.Data["event"] == LossEvent
}

// sentAny reports whether any accepted upload holds a record that contains s.
func (s *orderedServer) sentAny(s2 string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.accepted {
		for _, rec := range b {
			if strings.Contains(string(rec), s2) {
				return true
			}
		}
	}
	return false
}

// ledgerFiles returns the live loss ledger and every claim of it in dir.
func ledgerFiles(dir string) []string {
	path := filepath.Join(dir, DroppedMarkerName)
	files, _ := filepath.Glob(path + DroppedCompactingSuffix + "*")
	reporting, _ := filepath.Glob(path + DroppedReportingSuffix + "*")
	files = append(files, reporting...)
	if _, err := os.Stat(path); err == nil {
		files = append(files, path)
	}
	return files
}

// TestSpoolDrainKeepsARealRecordNextToJunk pins the drain against a flood in
// the live file: one real tool record followed by more junk than the spool
// cap holds, with a server that takes every upload. The cap bounds what
// failed uploads leave parked, so it must not throw away a file the drain
// has not tried to upload. Junk a chunk can carry uploads with the real
// record. Junk no chunk can carry is dropped one record at a time and
// counted, and the real record still uploads.
func TestSpoolDrainKeepsARealRecordNextToJunk(t *testing.T) {
	rows := []struct {
		name      string
		junkBytes int
		junk      int
		uploads   int
		oversize  int
	}{
		{"junk a chunk carries uploads with it", 4 << 10, 10, 11, 0},
		{"junk no chunk carries is counted one by one", 9 << 10, 4, 1, 4},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			shrinkChunks(t, 8<<10, 1000, 30<<10)
			dir := t.TempDir()
			sp := NewSpool(filepath.Join(dir, "spool.jsonl"))
			if err := sp.AppendCE("straza.audit.tool", toolRecord("REAL-rm -rf /srv/prod", "allow", "s1", "snap")); err != nil {
				t.Fatal(err)
			}
			for range row.junk {
				if err := sp.AppendCE("straza.audit.tool", toolRecord(strings.Repeat("A", row.junkBytes), "allow", "s1", "snap")); err != nil {
					t.Fatal(err)
				}
			}
			srv := &orderedServer{}
			n, err := sp.Drain(context.Background(), srv, "tok")
			if err != nil {
				t.Fatalf("Drain: %v", err)
			}
			if n != row.uploads || !srv.sentAny("REAL-rm -rf /srv/prod") {
				t.Fatalf("drained %d, real record sent = %v; want %d and the real record", n, srv.sentAny("REAL-rm"), row.uploads)
			}
			st := Dropped(dir)
			if st.Records != row.oversize || st.Files != 0 {
				t.Fatalf("ledger = %+v, want %d oversize record(s) and no evicted file", st, row.oversize)
			}
		})
	}
}

// TestSpoolCapEvictsOnlyAfterAFailedUpload pins the cap's place in a drain:
// while the server refuses, the oldest parked files past the cap go and the
// ledger counts the files and the records in them. Once the server takes
// uploads again, the loss record goes first and the surviving file follows.
func TestSpoolCapEvictsOnlyAfterAFailedUpload(t *testing.T) {
	shrinkChunks(t, 3<<20, 1000, 30<<10)
	dir := t.TempDir()
	sp := NewSpool(filepath.Join(dir, "spool.jsonl"))
	now := time.Now()
	var newest []string
	for i, name := range []string{"audit-pending-aaa.jsonl", "audit-pending-bbb.jsonl", "audit-pending-ccc.jsonl"} {
		var b strings.Builder
		for j := range 10 {
			id := name + "-" + strconv.Itoa(j)
			b.WriteString(spoolLineOfLen(t, id, 2<<10) + "\n")
			if i == 2 {
				newest = append(newest, id)
			}
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(time.Duration(i-3) * time.Hour)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}

	srv := &orderedServer{refuse: func(int) bool { return true }}
	if _, err := sp.Drain(context.Background(), srv, "tok"); err == nil {
		t.Fatal("a drain against a refusing server succeeded")
	}
	if st := Dropped(dir); st.Files != 2 || st.FileRecords != 20 || st.Events != 1 || st.Last.IsZero() {
		t.Fatalf("ledger after the refused drain = %+v, want 2 files holding 20 records in 1 event", st)
	}

	srv.refuse = nil
	n, err := sp.Drain(context.Background(), srv, "tok")
	if err != nil || n != 10 {
		t.Fatalf("healed drain = %d, %v; want the 10 records of the newest file", n, err)
	}
	if len(srv.accepted) != 2 || len(srv.accepted[0]) != 1 {
		t.Fatalf("accepted uploads = %d, want the loss record alone and then the newest file", len(srv.accepted))
	}
	ce, ok := lossOf(t, srv.accepted[0][0])
	if !ok || ce.Data["evictedFiles"] != float64(2) || ce.Data["evictedRecords"] != float64(20) || ce.Data["droppedRecords"] != float64(20) {
		t.Fatalf("first upload = %s, want the loss record of 2 files holding 20 records", srv.accepted[0][0])
	}
	for _, id := range newest {
		if !srv.sentAny(id) {
			t.Fatalf("the newest file's record %s was not uploaded", id)
		}
	}
	if left := ledgerFiles(dir); len(left) != 0 {
		t.Fatalf("the ledger stayed after strazad took the loss record: %v", left)
	}
}

// TestSpoolLossRecordLeavesOnlyOnAccept pins the loss record's delivery: it
// is the first upload of a drain, it names the counts, the time range and
// the reasons, and the ledger leaves the disk only once the server took it.
// A refused upload keeps every count and sends nothing after it, and the
// retry carries the same id, so a record sent twice dedupes.
func TestSpoolLossRecordLeavesOnlyOnAccept(t *testing.T) {
	ts1, ts2 := "2026-11-04T10:00:00Z", "2026-11-04T10:05:00Z"
	ledger := ts1 + " dropped 3 oversize record(s)\n" +
		ts2 + " dropped 2 parked file(s) holding 9 record(s) over the 33554432-byte spool cap\n"
	rows := []struct {
		name    string
		refuses int // how many uploads the server refuses first
	}{
		{"accepted at once", 0},
		{"refused once, then accepted", 1},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			dir := t.TempDir()
			sp := NewSpool(filepath.Join(dir, "spool.jsonl"))
			if err := os.WriteFile(filepath.Join(dir, DroppedMarkerName), []byte(ledger), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := sp.AppendCE("straza.audit.tool", toolRecord("ls", "allow", "s1", "snap")); err != nil {
				t.Fatal(err)
			}
			srv := &orderedServer{refuse: func(n int) bool { return n <= row.refuses }}
			for range row.refuses {
				if _, err := sp.Drain(context.Background(), srv, "tok"); err == nil {
					t.Fatal("a drain whose loss record was refused succeeded")
				}
				if st := Dropped(dir); st.Lost() != 12 || st.Events != 2 {
					t.Fatalf("ledger after a refused loss record = %+v, want every count kept", st)
				}
				if len(srv.tried) != 1 || len(srv.tried[0]) != 1 {
					t.Fatalf("uploads after a refused loss record = %d, want none past it", len(srv.tried))
				}
			}
			n, err := sp.Drain(context.Background(), srv, "tok")
			if err != nil || n != 1 {
				t.Fatalf("Drain = %d, %v; want the one spooled record", n, err)
			}
			first := srv.accepted[0]
			ce, ok := lossOf(t, first[0])
			if len(first) != 1 || !ok || ce.Type != "straza.audit.tool" {
				t.Fatalf("first accepted upload = %s, want the loss record alone", first)
			}
			want := map[string]any{
				"droppedRecords": float64(12), "oversizeRecords": float64(3), "evictedFiles": float64(2),
				"evictedRecords": float64(9), "dropEvents": float64(2), "firstDrop": ts1, "lastDrop": ts2,
			}
			for k, v := range want {
				if ce.Data[k] != v {
					t.Errorf("loss record %s = %v, want %v", k, ce.Data[k], v)
				}
			}
			reason, _ := ce.Data["reason"].(string)
			for _, s := range []string{"lost 12 audit records", ts1, ts2, "3 were each larger", "2 unsent spool files holding 9 records"} {
				if !strings.Contains(reason, s) {
					t.Errorf("reason %q does not say %q", reason, s)
				}
			}
			if _, has := ce.Data["session"]; has {
				t.Error("the loss record names a session, but strazad binds it to the uploader's")
			}
			if row.refuses > 0 {
				refused, _ := lossOf(t, srv.tried[0][0])
				if refused.ID != ce.ID {
					t.Errorf("retried loss record id %s, want the refused one's %s so the server dedupes", ce.ID, refused.ID)
				}
			}
			if left := ledgerFiles(dir); len(left) != 0 {
				t.Fatalf("the ledger stayed after strazad took the loss record: %v", left)
			}
			if _, err := sp.Drain(context.Background(), srv, "tok"); err != nil {
				t.Fatal(err)
			}
			for _, b := range srv.accepted[1:] {
				for _, rec := range b {
					if _, again := lossOf(t, rec); again {
						t.Fatal("a later drain sent the loss record again")
					}
				}
			}
		})
	}
}

// TestSpoolLossNotedDuringReportStays pins the claim the report works on: a
// drop noted while the loss record is on its way lands in a new ledger,
// which the accepted report does not remove and the next drain sends.
func TestSpoolLossNotedDuringReportStays(t *testing.T) {
	dir := t.TempDir()
	sp := NewSpool(filepath.Join(dir, "spool.jsonl"))
	if err := os.WriteFile(filepath.Join(dir, DroppedMarkerName), []byte("2026-11-04T10:00:00Z dropped 1 oversize record(s)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := &noteDuringUpload{dir: dir}
	if _, err := sp.Drain(context.Background(), srv, "tok"); err != nil {
		t.Fatal(err)
	}
	if st := Dropped(dir); st.Records != 5 || st.Events != 1 {
		t.Fatalf("ledger after the report = %+v, want only the drop noted during it", st)
	}
}

// noteDuringUpload accepts every upload and notes a drop of 5 records in
// dir while the first one is on its way.
type noteDuringUpload struct {
	dir   string
	noted bool
}

func (u *noteDuringUpload) AuditBatch(_ context.Context, _ string, events []json.RawMessage) (int, error) {
	if !u.noted {
		u.noted = true
		noteDropped(u.dir, "5 oversize record(s)")
	}
	return len(events), nil
}

// TestSpoolLossReportStaysOneClaim pins the bound on the ledger while the
// server refuses: every drain retries the one report claim it holds, drops
// noted meanwhile wait in the compacted live ledger, and no drain adds a
// claim. Once the server takes uploads, the held report goes first and the
// waiting drops go in the report after it, so every drop arrives once.
func TestSpoolLossReportStaysOneClaim(t *testing.T) {
	dir := t.TempDir()
	sp := NewSpool(filepath.Join(dir, "spool.jsonl"))
	path := filepath.Join(dir, DroppedMarkerName)
	if err := os.WriteFile(path, []byte("2026-11-04T10:00:00Z dropped 2 oversize record(s)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := &orderedServer{refuse: func(int) bool { return true }}
	for i := range 5 {
		if _, err := sp.Drain(context.Background(), srv, "tok"); err == nil {
			t.Fatalf("drain %d against a refusing server succeeded", i)
		}
		noteDropped(dir, "1 oversize record(s)")
		if claims, _ := filepath.Glob(path + DroppedReportingSuffix + "*"); len(claims) != 1 {
			t.Fatalf("report claims after drain %d = %d, want the one held report", i, len(claims))
		}
	}
	if st := Dropped(dir); st.Lost() != 7 {
		t.Fatalf("ledger while refused = %+v, want all 7 records counted once", st)
	}
	srv.refuse = nil
	for range 2 {
		if _, err := sp.Drain(context.Background(), srv, "tok"); err != nil {
			t.Fatal(err)
		}
	}
	reported := 0
	for _, b := range srv.accepted {
		for _, rec := range b {
			if ce, ok := lossOf(t, rec); ok {
				reported += int(ce.Data["droppedRecords"].(float64))
			}
		}
	}
	if reported != 7 || len(ledgerFiles(dir)) != 0 {
		t.Fatalf("records reported = %d with %v left, want all 7 and an empty ledger", reported, ledgerFiles(dir))
	}
}

// TestCountRecords pins how an eviction counts the records of a parked
// file: one per line, a final line without its newline included, a line
// longer than the reader's page once, and a file it cannot read as one.
func TestCountRecords(t *testing.T) {
	long := strings.Repeat("x", 200<<10)
	rows := []struct {
		name    string
		content *string
		want    int
	}{
		{"three lines", ptr("a\nb\nc\n"), 3},
		{"a final line without its newline", ptr("a\nb"), 2},
		{"lines longer than the reader's page", ptr(long + "\n" + long + "\nc\n"), 3},
		{"a long final line without its newline", ptr("a\n" + long), 2},
		{"a missing file", nil, 1},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "audit-pending-x.jsonl")
			if row.content != nil {
				if err := os.WriteFile(p, []byte(*row.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := countRecords(p); got != row.want {
				t.Fatalf("countRecords = %d, want %d", got, row.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }

// TestSpoolLossRecordTravelsAlone pins the loss record's share of the
// server's per-upload record cap, which matches drainChunkRecords: the
// loss record is an upload of its own, so no upload of a drain carries more
// than drainChunkRecords records, the loss record included.
func TestSpoolLossRecordTravelsAlone(t *testing.T) {
	shrinkChunks(t, 3<<20, 10, 32<<20)
	dir := t.TempDir()
	sp := NewSpool(filepath.Join(dir, "spool.jsonl"))
	if err := os.WriteFile(filepath.Join(dir, DroppedMarkerName), []byte("2026-11-04T10:00:00Z dropped 1 oversize record(s)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := range 25 {
		b.WriteString(spoolLineOfLen(t, "r"+strconv.Itoa(i), 64) + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "audit-pending-a.jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := &orderedServer{}
	if n, err := sp.Drain(context.Background(), srv, "tok"); err != nil || n != 25 {
		t.Fatalf("Drain = %d, %v; want 25", n, err)
	}
	sizes := []int{}
	for _, batch := range srv.accepted {
		sizes = append(sizes, len(batch))
		if len(batch) > drainChunkRecords {
			t.Errorf("an upload carried %d records, over the cap of %d", len(batch), drainChunkRecords)
		}
	}
	if _, ok := lossOf(t, srv.accepted[0][0]); !ok || sizes[0] != 1 {
		t.Fatalf("upload sizes = %v, want the loss record alone first", sizes)
	}
	for _, batch := range srv.accepted[1:] {
		for _, rec := range batch {
			if _, ok := lossOf(t, rec); ok {
				t.Fatal("the loss record shared an upload with spooled records")
			}
		}
	}
}

// appendingServer accepts every upload and appends more records to file
// while the first upload is on its way.
type appendingServer struct {
	orderedServer
	file  string
	extra string
	done  bool
}

func (s *appendingServer) AuditBatch(ctx context.Context, tok string, events []json.RawMessage) (int, error) {
	if !s.done {
		s.done = true
		f, err := os.OpenFile(s.file, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return 0, err
		}
		_, err = f.WriteString(s.extra)
		_ = f.Close()
		if err != nil {
			return 0, err
		}
	}
	return s.orderedServer.AuditBatch(ctx, tok, events)
}

// TestSpoolDrainReadsFileAsItUploads pins that a drain holds one chunk of a
// spool file at a time: it uploads each chunk before it reads the records
// after it, so a file far larger than a chunk never sits in memory whole.
// Records appended to the file while its first chunk is on its way are
// read and uploaded too, which a drain that read the file first would miss.
func TestSpoolDrainReadsFileAsItUploads(t *testing.T) {
	shrinkChunks(t, 200<<10, 1000, 32<<20)
	dir := t.TempDir()
	sp := NewSpool(filepath.Join(dir, "spool.jsonl"))
	lines := func(prefix string, n int) string {
		var b strings.Builder
		for i := range n {
			b.WriteString(spoolLineOfLen(t, prefix+strconv.Itoa(i), 50<<10) + "\n")
		}
		return b.String()
	}
	p := filepath.Join(dir, "audit-pending-a.jsonl")
	if err := os.WriteFile(p, []byte(lines("r", 40)), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := &appendingServer{file: p, extra: lines("late", 5)}
	n, err := sp.Drain(context.Background(), srv, "tok")
	if err != nil || n != 45 || !srv.sentAny(`"late4"`) {
		t.Fatalf("Drain = %d, %v; want all 45 records, the 5 appended during the first upload included", n, err)
	}
	for i, b := range srv.accepted {
		size := 0
		for _, rec := range b {
			size += len(rec) + 1
		}
		if size > drainChunkBytes {
			t.Fatalf("upload %d carried %d bytes, over one chunk", i, size)
		}
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("the file stayed after its last chunk landed: %v", err)
	}
}

// TestSpoolRefusedCallsInTheLedger pins a refused call's note: each one
// counts in the ledger, a compaction keeps the count, and the loss record
// names the refused calls beside the dropped records.
func TestSpoolRefusedCallsInTheLedger(t *testing.T) {
	dir := t.TempDir()
	sp := NewSpool(filepath.Join(dir, "spool.jsonl"))
	sp.NoteRefused("capture")
	sp.NoteRefused("capture")
	sp.NoteRefused("decide")
	if st := Dropped(dir); st.Refused != 3 || st.Lost() != 3 || st.Events != 3 {
		t.Fatalf("ledger = %+v, want 3 refused calls in 3 events", st)
	}
	path := filepath.Join(dir, DroppedMarkerName)
	claim := path + DroppedCompactingSuffix + "test"
	if err := os.Rename(path, claim); err != nil {
		t.Fatal(err)
	}
	foldOneClaim(path, claim)
	if st := Dropped(dir); st.Refused != 3 || st.Events != 3 {
		t.Fatalf("ledger after a compaction = %+v, want the 3 refused calls kept", st)
	}
	srv := &orderedServer{}
	if _, err := sp.Drain(context.Background(), srv, "tok"); err != nil {
		t.Fatal(err)
	}
	ce, ok := lossOf(t, srv.accepted[0][0])
	reason, _ := ce.Data["reason"].(string)
	if !ok || ce.Data["refusedCalls"] != float64(3) || ce.Data["droppedRecords"] != float64(3) ||
		!strings.Contains(reason, "refused 3 decide or capture calls") {
		t.Fatalf("loss record = %s, want the 3 refused calls named", srv.accepted[0][0])
	}
}

// TestSpoolRefusedNotesExactUnderRace pins the ledger's count when refused
// calls are noted from many goroutines while drains report and compaction
// folds the ledger, as the service's request goroutines and its drain loop
// do: what reached the server plus what is left equals what was noted, in
// every round.
func TestSpoolRefusedNotesExactUnderRace(t *testing.T) {
	old := markerCompactBytes
	markerCompactBytes = 2 << 10
	t.Cleanup(func() { markerCompactBytes = old })
	for _, drains := range []bool{false, true} {
		for round := range 5 {
			dir := t.TempDir()
			sp := NewSpool(filepath.Join(dir, "spool.jsonl"))
			srv := &orderedServer{}
			stop := make(chan struct{})
			var dwg sync.WaitGroup
			if drains {
				dwg.Add(1)
				go func() {
					defer dwg.Done()
					for {
						select {
						case <-stop:
							return
						default:
						}
						_, _ = sp.Drain(context.Background(), srv, "tok")
					}
				}()
			}
			const workers, each = 8, 300
			var wg sync.WaitGroup
			for range workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for range each {
						sp.NoteRefused("decide")
					}
				}()
			}
			wg.Wait()
			close(stop)
			dwg.Wait()
			reported := 0
			for _, b := range srv.accepted {
				for _, rec := range b {
					if ce, ok := lossOf(t, rec); ok {
						reported += int(ce.Data["refusedCalls"].(float64))
					}
				}
			}
			if left := Dropped(dir).Refused; reported+left != workers*each {
				t.Fatalf("drains %v, round %d: the server got %d and %d are left, want %d in all", drains, round, reported, left, workers*each)
			}
		}
	}
}

// TestSpoolMalformedLedgerIsSent pins that a ledger line the parser cannot
// read, a negative count included, still counts as one drop event, so the
// drain sends the loss record with the readable lines' counts and removes
// the ledger only after the server took it. Unknown loss is loss.
func TestSpoolMalformedLedgerIsSent(t *testing.T) {
	dir := t.TempDir()
	sp := NewSpool(filepath.Join(dir, "spool.jsonl"))
	ledger := "2026-11-05T00:00:00Z dropped 0 oversize record(s) and 0 parked file(s) holding 0 record(s) and -2389377045467174595 refused call(s) spanning -2389377045467174595 drop event(s) since 2026-11-05T00:00:00Z [compacted]\n" +
		"2026-11-05T00:01:00Z dropped 1 parked file(s) holding 7 record(s) over the 33554432-byte spool cap\n"
	if err := os.WriteFile(filepath.Join(dir, DroppedMarkerName), []byte(ledger), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := Dropped(dir); st.Refused != 0 || st.FileRecords != 7 || st.Events != 2 {
		t.Fatalf("ledger = %+v, want the negative counts ignored and both lines counted as events", st)
	}
	srv := &orderedServer{refuse: func(n int) bool { return n == 1 }}
	if _, err := sp.Drain(context.Background(), srv, "tok"); err == nil {
		t.Fatal("a drain whose loss record was refused succeeded")
	}
	if st := Dropped(dir); st.FileRecords != 7 || st.Events != 2 {
		t.Fatalf("ledger after a refused report = %+v, want it kept", st)
	}
	if _, err := sp.Drain(context.Background(), srv, "tok"); err != nil {
		t.Fatal(err)
	}
	ce, ok := lossOf(t, srv.accepted[0][0])
	if !ok || ce.Data["evictedRecords"] != float64(7) || ce.Data["dropEvents"] != float64(2) || ce.Data["refusedCalls"] != float64(0) {
		t.Fatalf("loss record = %s, want the eviction and both events", srv.accepted[0][0])
	}
	if left := ledgerFiles(dir); len(left) != 0 {
		t.Fatalf("the ledger stayed after strazad took it: %v", left)
	}
}
