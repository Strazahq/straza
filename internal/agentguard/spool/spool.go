// Package spool is the client kit's audit buffer: governed decisions and
// captured conversation turns are appended as JSONL CloudEvents and drained
// to the server in bounded chunks with zero loss (rotate aside, upload,
// remove only on success), a bounded parked backlog, and a loss ledger that
// each drain reports to the server. It knows nothing about harness events
// or the HTTP client: agentguard shapes the records (spoolwrite.go) and
// supplies the Uploader.
package spool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Spool is the client audit buffer: governed decisions are appended as JSONL
// and drained to the server in batches. Records survive a network outage: the
// spool is only truncated after a successful upload, so a kill mid-drain
// loses nothing (at-least-once; the server dedupes by CE id).
type Spool struct {
	path string
	// mu serializes appends with the rotate of the live file and with
	// Pending, and drainMu serializes drains, so an append never waits on
	// an upload.
	mu      sync.Mutex
	drainMu sync.Mutex
}

// NewSpool returns the spool at path.
func NewSpool(path string) *Spool { return &Spool{path: path} }

// LastDrainMarkerName is the marker file (same directory as the spool)
// stamped by every successful drain that left the spool empty. Its mtime is
// the "last successful drain" surface `straza doctor` reports: drained
// pending files are deleted and nothing else records that the upload
// happened, so without the marker "when did audit last flush?" has no honest
// answer. Content is an informational RFC3339 timestamp; readers use mtime.
const LastDrainMarkerName = "audit-last-drain"

// AppendCE appends one CloudEvent of ceType carrying data as a JSONL line,
// fsync'd. The record shapes themselves live with their producers in
// agentguard (spoolwrite.go); this is the only writer of the file.
func (s *Spool) AppendCE(ceType string, data map[string]any) error {
	line, err := encodeCE(uuid.NewString(), ceType, data)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- our own spool
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// encodeCE is the CloudEvent of ceType carrying data under id, as the
// client writes every record it sends.
func encodeCE(id, ceType string, data map[string]any) ([]byte, error) {
	return json.Marshal(map[string]any{
		"specversion": "1.0",
		"id":          id,
		"type":        ceType,
		"source":      "straza",
		"time":        time.Now().UTC().Format(time.RFC3339Nano),
		"data":        data,
	})
}

// Upload and backlog bounds. The server rejects any /v1/audit/batch body
// over 4 MiB, so an unchunked spool that grew past that (64 capture turns at
// the 64 KiB cap) would be a PERMANENT poison pill: every drain would re-send
// the whole oversize batch, be refused, and keep the files forever. Vars, not
// consts, so tests can shrink them.
var (
	// drainChunkBytes caps one upload's record bytes; 1 MiB of headroom
	// under the server's 4 MiB body cap covers the JSON envelope.
	drainChunkBytes = 3 << 20
	// drainChunkRecords caps records per upload independent of size.
	drainChunkRecords = 1000
	// maxSpoolBytes bounds the parked backlog. Beyond it the OLDEST parked
	// files are dropped and counted: on a long-offline box, losing the
	// oldest audit beats growing without bound (the client-side mirror of
	// the standalone drop-with-counter backpressure stance; a client cannot
	// "block" the way the enterprise server spool does).
	maxSpoolBytes int64 = 32 << 20
)

// removeSpoolFile is os.Remove behind a seam so tests can deterministically
// inject the removal outcomes a live fleet produces: a Windows handle
// refusing the delete, or a concurrent drainer winning the remove race.
var removeSpoolFile = os.Remove

// spoolReadCap is the largest single record eachRecord will buffer, DERIVED
// from drainChunkBytes so the two bounds can never drift apart.
//
// Size invariant: read cap >= the largest chunk-carryable record, which is
// drainChunkBytes-1 (uploadFile keeps a record iff len+1 <= drainChunkBytes,
// so it always has a chunk for it). Three outcomes, none an error:
//
//	len < drainChunkBytes         -> buffered and uploaded
//	drainChunkBytes <= len <= cap -> buffered, counted by uploadFile
//	len > cap                     -> skip-streamed, counted the same way
//
// An independent scanner cap under the chunk size breaks that first relation:
// a carryable but unreadable record would make the read fail and Drain abort
// at that file every pass, halting audit delivery. Pinned by
// TestSpoolReadCapDominatesCarryableMax and TestSpoolRecordSizeInvariant.
func spoolReadCap() int { return drainChunkBytes }

// DroppedMarkerName records audit loss: one `<RFC3339> dropped <what>` line
// per event the spool cap or the oversize-record guard discarded records on.
// It is the only trace those records ever existed. The next drain sends it
// to the server as one loss record (reportLoss) and removes it once the
// server took that record. Until then the service's status, and so
// `straza doctor`, count it.
const DroppedMarkerName = "audit-dropped"

// markerCompactBytes bounds the marker itself: a box parked over the spool
// cap appends a line per drain attempt, and the loss ledger must not become
// its own unbounded file. Past the bound it folds into one summary line that
// preserves the running totals. Var, not const, so tests can shrink it.
var markerCompactBytes int64 = 64 << 10

// DroppedCompactingSuffix + a random id names a compaction claim: the
// oversized marker renamed aside while its summary is appended to the live
// name. Unique per claim so no rename can ever land ON an existing claim and
// erase it (os.Rename replaces on Unix). Anything matching marker+suffix+*
// is unfolded loss evidence: noteDropped and reportLoss fold claims back in
// first, and Dropped counts them.
const DroppedCompactingSuffix = ".compacting-"

// DroppedReportingSuffix plus a random id names a report claim: the ledger
// renamed aside while its loss record goes to the server. A drop noted
// meanwhile starts a new live ledger, and compaction never folds a report
// claim, so an accepted report removes only what it sent.
const DroppedReportingSuffix = ".reporting-"

// LossEvent is the event of the loss record, the straza.audit.tool record
// that tells the server how much audit this spool discarded.
const LossEvent = "audit.dropped"

// Uploader is the one server call Drain needs: the audit batch upload.
// *agentguard.Client satisfies it; tests supply fakes.
type Uploader interface {
	AuditBatch(ctx context.Context, sessionToken string, events []json.RawMessage) (int, error)
}

// Drain uploads all spooled records to the server and removes them only on
// success (zero loss). It first ROTATES the live file aside (atomic rename):
// several processes share this spool, hooks appending while a daemon tick, a
// detached post-decision drainer or a session start/end drains, and a
// truncate-after-read scheme would eat a record another process appended
// between the read and the truncate. After rotation, appenders recreate a
// fresh live file and this drain owns the rotated one; a rename refused
// because an appender holds the file open (Windows) just waits for the next
// drain. Concurrent drainers may double-upload (the server dedupes by CE id)
// but can never lose a record. Uploads go file by file in chunks of at most
// drainChunkRecords records and drainChunkBytes bytes, and a file's records
// leave disk only after every one of its chunks landed; a mid-drain failure
// keeps the current and later files and re-sends next drain.
// Only the rotate holds the append lock, so no append waits on the network.
func (s *Spool) Drain(ctx context.Context, client Uploader, sessionToken string) (int, error) {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()

	dir := filepath.Dir(s.path)
	s.mu.Lock()
	if fi, err := os.Stat(s.path); err == nil && fi.Size() > 0 {
		_ = os.Rename(s.path, filepath.Join(dir, "audit-pending-"+uuid.NewString()+".jsonl"))
	}
	s.mu.Unlock()
	pending, err := filepath.Glob(filepath.Join(dir, "audit-pending-*.jsonl"))
	if err != nil {
		return 0, err
	}
	sort.Strings(pending)
	total, err := upload(ctx, client, sessionToken, dir, pending)
	if err != nil {
		// What a drain could not deliver is the backlog the cap bounds, so it
		// applies now, and never to a file the server would have taken.
		enforceSpoolCap(dir, pending)
		return total, err
	}
	s.mu.Lock()
	s.markDrainedIfFlushed()
	s.mu.Unlock()
	return total, nil
}

// upload sends the loss ledger first, so loss reaches the server ahead of
// whatever followed it, then the pending files in order, and removes each
// file once all of its records landed. It stops at the first failure and
// returns how many spooled records landed.
func upload(ctx context.Context, client Uploader, sessionToken, dir string, pending []string) (int, error) {
	if err := reportLoss(ctx, client, sessionToken, dir); err != nil {
		return 0, fmt.Errorf("audit drain failed (the record of discarded audit was not accepted, kept it and the spooled records up to the spool cap): %w", err)
	}
	total := 0
	for _, p := range pending {
		n, oversize, err := uploadFile(ctx, client, sessionToken, p)
		total += n
		if err != nil {
			return total, fmt.Errorf("audit drain failed (uploaded %d, kept the rest up to the spool cap): %w", total, err)
		}
		removeDrained(dir, p, oversize)
	}
	return total, nil
}

// uploadFile uploads the records of the spool file at path in chunks of at
// most drainChunkRecords records and drainChunkBytes bytes, and reads the
// records after a chunk only once it landed, so a file of any size costs
// one chunk of memory. It returns how many records landed and how many no
// chunk can carry, which it skips.
func uploadFile(ctx context.Context, client Uploader, sessionToken, path string) (sent, oversize int, err error) {
	var chunk []json.RawMessage
	size := 0
	flush := func() error {
		if len(chunk) == 0 {
			return nil
		}
		if _, err := client.AuditBatch(ctx, sessionToken, chunk); err != nil {
			return err
		}
		sent += len(chunk)
		chunk, size = nil, 0
		return nil
	}
	skipped, err := eachRecord(path, func(rec []byte) error {
		if len(rec)+1 > drainChunkBytes {
			oversize++
			return nil
		}
		if len(chunk) == drainChunkRecords || size+len(rec)+1 > drainChunkBytes {
			if err := flush(); err != nil {
				return err
			}
		}
		chunk = append(chunk, append(json.RawMessage(nil), rec...))
		size += len(rec) + 1
		return nil
	})
	if err == nil {
		err = flush()
	}
	return sent, oversize + skipped, err
}

// removeDrained removes a fully-uploaded pending file and, only when THIS
// process's removal is what discarded it, writes the marker line for the
// oversize records that went with it, as uploadFile counted them.
// Counting is tied to the discard, never the read: a refused removal (a
// Windows handle) keeps the file WHOLE (nothing is lost yet, the next drain
// re-reads it and re-uploads dedupe by CE id), and IsNotExist means a
// concurrent drainer discarded it, so its drain did the accounting. The
// fail-closed stance elsewhere in this file (unknown loss counts as loss;
// ParseDroppedMarker) is about evidence we cannot READ; a file we know
// survived, or one another process accounted for, is not loss, and counting
// it would be a false alarm the doctor audit-dropped check turns into a hard
// FAIL. A crash before removal re-uploads on the next drain; the server
// dedupes.
func removeDrained(dir, path string, oversize int) {
	if err := removeSpoolFile(path); err == nil {
		if oversize > 0 {
			noteDropped(dir, fmt.Sprintf("%d oversize record(s)", oversize))
		}
	}
}

// enforceSpoolCap drops the OLDEST parked files (by mtime) until the parked
// backlog fits maxSpoolBytes, and notes the files and the records in them.
// A file counts as dropped ONLY when this process's os.Remove
// actually discarded it: a refused removal (a Windows handle holding
// the file open) means the file SURVIVED (it stays parked for the next
// drain), and IsNotExist means a concurrent drainer already
// discarded and counted it. The fail-closed "unknown loss is loss" stance
// (ParseDroppedMarker) is about evidence we cannot READ, never about files
// we know survived: counting survivors would make the doctor audit-dropped
// check FAIL over audit that was in fact delivered. Either way the file's bytes
// leave the eviction budget, so one stuck file cannot cascade the eviction
// onto newer files: the byte bound is best-effort until the lock clears,
// and the next drain retries.
func enforceSpoolCap(dir string, pending []string) {
	type pf struct {
		path string
		size int64
		mod  time.Time
	}
	var files []pf
	var total int64
	for _, p := range pending {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		files = append(files, pf{p, fi.Size(), fi.ModTime()})
		total += fi.Size()
	}
	if total <= maxSpoolBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	droppedFiles, droppedRecords := 0, 0
	for _, f := range files {
		if total <= maxSpoolBytes {
			break
		}
		recs := countRecords(f.path)
		err := removeSpoolFile(f.path)
		total -= f.size
		if err == nil {
			droppedFiles++
			droppedRecords += recs
		}
	}
	if droppedFiles > 0 {
		noteDropped(dir, fmt.Sprintf("%d parked file(s) holding %d record(s) over the %d-byte spool cap",
			droppedFiles, droppedRecords, maxSpoolBytes))
	}
}

// countRecords counts the records of the spool file at path without holding
// them, before an eviction discards them. A file it cannot read whole
// counts at least one record, because unknown loss is loss.
func countRecords(path string) int {
	n := 0
	skipped, _ := eachRecord(path, func([]byte) error { n++; return nil })
	return max(n+skipped, 1)
}

// markDrainedIfFlushed stamps the last-drain marker, but only when this
// drain actually left the live file empty. A rotation refused by a concurrent
// appender's open handle (Windows) leaves live records behind; stamping then
// would tell doctor the spool flushed when it did not. Best-effort: the
// marker is diagnostics, never worth failing a drain over.
func (s *Spool) markDrainedIfFlushed() {
	if fi, err := os.Stat(s.path); err == nil && fi.Size() > 0 {
		return
	}
	_ = os.WriteFile(filepath.Join(filepath.Dir(s.path), LastDrainMarkerName),
		[]byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// Pending returns the number of spooled records, live + parked
// (diagnostics). It counts without holding them, so a large spool costs no
// memory here either.
func (s *Spool) Pending() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files := []string{s.path}
	parked, _ := filepath.Glob(filepath.Join(filepath.Dir(s.path), "audit-pending-*.jsonl"))
	files = append(files, parked...)
	n := 0
	for _, p := range files {
		if _, err := eachRecord(p, func([]byte) error { n++; return nil }); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// eachRecord calls fn with each record of the spool file at path, in order,
// never holding more than spoolReadCap() plus one reader page of it, and
// returns how many lines it skipped. rec is valid only until fn returns. A
// line past the cap is, by the size invariant on spoolReadCap, unuploadable
// anyway: it is skipped in a stream and counted so Drain records it as an
// oversize drop when the file is discarded. Record SIZE must never be a read
// error (a read error aborts Drain at this file on every pass), so the
// error carries real I/O faults and fn's own error only. A missing file has
// no records.
func eachRecord(path string, fn func(rec []byte) error) (skipped int, err error) {
	f, err := os.Open(path) // #nosec G304 -- our own spool files
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	readCap := spoolReadCap()
	r := bufio.NewReaderSize(f, 64<<10)
	var line []byte
	tooLong := false
	for {
		chunk, rerr := r.ReadSlice('\n')
		if !tooLong {
			line = append(line, chunk...)
		}
		if rerr == bufio.ErrBufferFull { // mid-line: keep accumulating (or skipping)
			if len(line) > readCap {
				tooLong, line = true, line[:0]
			}
			continue
		}
		if rerr != nil && rerr != io.EOF {
			return skipped, rerr
		}
		rec := bytes.TrimSuffix(line, []byte{'\n'})
		rec = bytes.TrimSuffix(rec, []byte{'\r'})
		switch {
		case tooLong || len(rec) > readCap:
			skipped++
		case len(rec) > 0:
			if err := fn(rec); err != nil {
				return skipped, err
			}
		}
		tooLong, line = false, line[:0]
		if rerr == io.EOF {
			return skipped, nil
		}
	}
}
