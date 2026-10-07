// Package spool is the client kit's audit buffer: governed decisions and
// captured conversation turns are appended as JSONL CloudEvents and drained
// to the server in bounded chunks with zero loss (rotate aside, upload,
// remove only on success), a bounded parked backlog, and a loss ledger the
// doctor reads. It knows nothing about harness events or the HTTP client:
// agentguard shapes the records (spoolwrite.go) and supplies the Uploader.
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
	"strconv"
	"strings"
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
	mu   sync.Mutex
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
	ce := map[string]any{
		"specversion": "1.0",
		"id":          uuid.NewString(),
		"type":        ceType,
		"source":      "straza",
		"time":        time.Now().UTC().Format(time.RFC3339Nano),
		"data":        data,
	}
	line, err := json.Marshal(ce)
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

// spoolReadCap is the largest single record ScanRecords will buffer, DERIVED
// from drainChunkBytes so the two bounds can never drift apart.
//
// Size invariant: read cap >= the largest chunk-carryable record, which is
// drainChunkBytes-1 (dropOversize keeps a record iff len+1 <= drainChunkBytes,
// so uploadChunked always has a chunk for it). Three outcomes, none an error:
//
//	len < drainChunkBytes         -> buffered and uploaded
//	drainChunkBytes <= len <= cap -> buffered, counted by dropOversize
//	len > cap                     -> skip-streamed, counted the same way
//
// An independent scanner cap under the chunk size breaks that first relation:
// a carryable but unreadable record makes ReadRecords return bufio.ErrTooLong
// and Drain abort at that file every pass, halting audit delivery. Pinned by
// TestSpoolReadCapDominatesCarryableMax and TestSpoolRecordSizeInvariant.
func spoolReadCap() int { return drainChunkBytes }

// DroppedMarkerName records audit loss: one `<RFC3339> dropped <what>` line
// per event the spool cap or the oversize-record guard discarded records on.
// It is the ONLY trace those records ever existed, and `straza doctor` is its
// reader (droppedAuditCheck: a populated marker is a loud FAIL; deleting the
// file is the acknowledgement). Doctor is deliberately the only surface:
// drops happen inside Drain, which runs from hook-invoked flows where stderr
// is harness-interpreted (claude-code/codex show it as the block reason on
// exit 2), so hooks must not write to stderr mid-session.
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
// is unfolded loss evidence: noteDropped folds claims back in before
// appending, and doctor reads them.
const DroppedCompactingSuffix = ".compacting-"

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
func (s *Spool) Drain(ctx context.Context, client Uploader, sessionToken string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(s.path)
	if fi, err := os.Stat(s.path); err == nil && fi.Size() > 0 {
		_ = os.Rename(s.path, filepath.Join(dir, "audit-pending-"+uuid.NewString()+".jsonl"))
	}
	pending, err := filepath.Glob(filepath.Join(dir, "audit-pending-*.jsonl"))
	if err != nil {
		return 0, err
	}
	sort.Strings(pending)
	pending = enforceSpoolCap(dir, pending)

	total := 0
	for _, p := range pending {
		recs, skipped, err := ScanRecords(p)
		if err != nil {
			return total, err
		}
		recs, oversize := dropOversize(recs)
		n, err := uploadChunked(ctx, client, sessionToken, recs)
		total += n
		if err != nil {
			return total, fmt.Errorf("audit drain failed (uploaded %d, kept the rest): %w", total, err)
		}
		removeDrained(dir, p, skipped+oversize)
	}
	s.markDrainedIfFlushed()
	return total, nil
}

// removeDrained removes a fully-uploaded pending file and, only when THIS
// process's removal is what discarded it, writes the marker line for the
// oversize records that went with it (scan-skipped + dropOversize-filtered).
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

// uploadChunked sends recs in server-safe chunks, returning how many landed.
func uploadChunked(ctx context.Context, client Uploader, sessionToken string, recs []json.RawMessage) (int, error) {
	sent := 0
	for len(recs) > 0 {
		size, n := 0, 0
		for n < len(recs) && n < drainChunkRecords {
			if rl := len(recs[n]) + 1; size+rl > drainChunkBytes && n > 0 {
				break
			} else {
				size += rl
			}
			n++
		}
		if _, err := client.AuditBatch(ctx, sessionToken, recs[:n]); err != nil {
			return sent, err
		}
		sent += n
		recs = recs[n:]
	}
	return sent, nil
}

// dropOversize filters out records no chunk can ever carry (len+1 over
// drainChunkBytes): unuploadable, they would wedge their file forever. It
// only COUNTS; the marker line is removeDrained's, written when the file
// (and so the record) is actually discarded; noting at read time would
// re-count the same record on every failed-upload retry.
func dropOversize(recs []json.RawMessage) ([]json.RawMessage, int) {
	kept := recs[:0]
	dropped := 0
	for _, rec := range recs {
		if len(rec)+1 > drainChunkBytes {
			dropped++
			continue
		}
		kept = append(kept, rec)
	}
	return kept, dropped
}

// enforceSpoolCap drops the OLDEST parked files (by mtime) until the parked
// backlog fits maxSpoolBytes, returning the surviving list in the original
// upload order. A file counts as dropped ONLY when this process's os.Remove
// actually discarded it: a refused removal (a Windows handle holding
// the file open) means the file SURVIVED (it stays in the drain list and
// uploads normally), and IsNotExist means a concurrent drainer already
// discarded and counted it. The fail-closed "unknown loss is loss" stance
// (ParseDroppedMarker) is about evidence we cannot READ, never about files
// we know survived: counting survivors would make the doctor audit-dropped
// check FAIL over audit that was in fact delivered. Either way the file's bytes
// leave the eviction budget, so one stuck file cannot cascade the eviction
// onto newer files: the byte bound is best-effort until the lock clears,
// and the next drain retries.
func enforceSpoolCap(dir string, pending []string) []string {
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
		return pending
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	droppedFiles := 0
	gone := map[string]bool{}
	for _, f := range files {
		if total <= maxSpoolBytes {
			break
		}
		err := removeSpoolFile(f.path)
		total -= f.size
		switch {
		case err == nil:
			gone[f.path] = true
			droppedFiles++
		case os.IsNotExist(err):
			gone[f.path] = true // the concurrent drainer's disposal, not ours
		}
	}
	if droppedFiles > 0 {
		noteDropped(dir, fmt.Sprintf("%d parked file(s) over the %d-byte spool cap", droppedFiles, maxSpoolBytes))
	}
	kept := pending[:0]
	for _, p := range pending {
		if !gone[p] {
			kept = append(kept, p)
		}
	}
	return kept
}

// noteDropped appends one line to the audit-dropped marker, best-effort
// (compacting it first when it outgrew markerCompactBytes).
func noteDropped(dir, what string) {
	path := filepath.Join(dir, DroppedMarkerName)
	compactDroppedMarker(path)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- our own marker
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "%s dropped %s\n", time.Now().UTC().Format(time.RFC3339), what)
}

// compactDroppedMarker folds a marker past markerCompactBytes into one
// summary line WITHOUT ever truncating a file another process may be
// appending to: Spool.mu is per-process while concurrent drainers across
// processes are a supported state (see Drain), so a read-then-rewrite here
// would erase any line that landed between the read and the write. Instead
// the oversized marker is CLAIMED by an atomic rename to a unique name: an
// appender either beat the rename (its line is inside the claim, counted by
// the summary) or O_CREATEs a fresh live marker whose lines survive beside
// the appended summary; no interleaving loses a line. A rename refused by
// an appender's open handle (Windows) just skips this round. A crash between
// rename and append parks the ledger in the claim file: the next drop folds
// it back in first, and doctor reads claims meanwhile. The one benign race
// left: two processes folding the same claim can append its summary twice;
// totals then OVERcount, never undercount; the contract is that nothing may
// shrink the evidence of loss.
func compactDroppedMarker(path string) {
	foldClaims(path)
	fi, err := os.Stat(path)
	if err != nil || fi.Size() <= markerCompactBytes {
		return
	}
	claim := path + DroppedCompactingSuffix + uuid.NewString()
	if os.Rename(path, claim) != nil {
		return
	}
	foldOneClaim(path, claim)
}

// foldClaims folds every parked compaction claim back into the live marker.
func foldClaims(path string) {
	claims, _ := filepath.Glob(path + DroppedCompactingSuffix + "*")
	for _, claim := range claims {
		foldOneClaim(path, claim)
	}
}

// foldOneClaim appends a claim's one-line summary to the live marker name,
// then removes the claim. The summary reuses the marker grammar so
// ParseDroppedMarker round-trips it: totals and last-drop time survive any
// number of compactions. Best-effort with a fail-safe order: the claim is
// removed only after its summary landed, so any failure keeps the evidence
// (in a file doctor also reads) rather than the directory clean.
func foldOneClaim(path, claim string) {
	raw, err := os.ReadFile(claim) // #nosec G304 -- our own marker
	if err != nil {
		return
	}
	st := ParseDroppedMarker(raw)
	if st.Events == 0 {
		return // nothing parseable to fold; leave it for doctor to warn about
	}
	last := st.Last
	if last.IsZero() {
		last = time.Now().UTC()
	}
	line := fmt.Sprintf("%s dropped %d oversize record(s) and %d parked file(s) spanning %d drop event(s) [compacted]\n",
		last.UTC().Format(time.RFC3339), st.Records, st.Files, st.Events)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- our own marker
	if err != nil {
		return
	}
	_, werr := f.WriteString(line)
	_ = f.Close()
	if werr != nil {
		return
	}
	_ = os.Remove(claim)
}

// DroppedStats is the audit-dropped marker, summed: how much audit this
// machine discarded, over how many drop events, and when the last one was.
type DroppedStats struct {
	Events   int       // drop events recorded (compacted lines carry their span)
	Records  int       // oversize records dropped by dropOversize
	Files    int       // parked files dropped by enforceSpoolCap
	Last     time.Time // newest drop time seen on any line, zero when none carried one
	LastLine string    // final line, verbatim
}

// ParseDroppedMarker sums the marker's lines. Grammar per line:
// `<RFC3339> dropped N oversize record(s)` / `... N parked file(s) over the
// C-byte spool cap` / the compacted summary carrying `spanning N drop
// event(s)`. A line it cannot read still counts as one drop event: the
// marker exists to make loss visible, so unknown loss is loss, not nothing.
func ParseDroppedMarker(raw []byte) DroppedStats {
	var st DroppedStats
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		st.LastLine = line
		fields := strings.Fields(line)
		// Newest time wins, not last-line-wins: doctor concatenates the live
		// marker with parked claims, and those sources are not ordered.
		if ts, err := time.Parse(time.RFC3339, fields[0]); err == nil && ts.After(st.Last) {
			st.Last = ts
		}
		lineEvents := 1
		for i, f := range fields[:len(fields)-1] {
			n, err := strconv.Atoi(f)
			if err != nil {
				continue
			}
			switch {
			case strings.HasPrefix(fields[i+1], "oversize"):
				st.Records += n
			case strings.HasPrefix(fields[i+1], "parked"):
				st.Files += n
			case strings.HasPrefix(fields[i+1], "drop"):
				lineEvents = n
			}
		}
		st.Events += lineEvents
	}
	return st
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

// Pending returns the number of spooled records, live + parked (diagnostics).
func (s *Spool) Pending() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files := []string{s.path}
	parked, _ := filepath.Glob(filepath.Join(filepath.Dir(s.path), "audit-pending-*.jsonl"))
	files = append(files, parked...)
	n := 0
	for _, p := range files {
		recs, err := ReadRecords(p)
		if err != nil {
			return 0, err
		}
		n += len(recs)
	}
	return n, nil
}

// ReadRecords is the uploadable view of a spool file (diagnostics callers:
// doctor's backlog count, Pending, tests). Lines past the read cap are
// invisible here; Drain's ScanRecords counts them as oversize drops.
func ReadRecords(path string) ([]json.RawMessage, error) {
	recs, _, err := ScanRecords(path)
	return recs, err
}

// ScanRecords reads one spool file line by line, never buffering more than
// spoolReadCap() plus one reader page per line. A line past the cap is, by
// the size invariant on spoolReadCap, unuploadable anyway: it is skipped in
// a stream and returned in skipped so Drain records it as an oversize drop
// when the file is discarded. Record SIZE must never be a read error (a
// read error aborts Drain at this file on every pass), so
// the error return carries real I/O faults only.
func ScanRecords(path string) (recs []json.RawMessage, skipped int, err error) {
	f, err := os.Open(path) // #nosec G304 -- our own spool files
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
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
			return recs, skipped, rerr
		}
		rec := bytes.TrimSuffix(line, []byte{'\n'})
		rec = bytes.TrimSuffix(rec, []byte{'\r'})
		switch {
		case tooLong || len(rec) > readCap:
			skipped++
		case len(rec) > 0:
			recs = append(recs, append(json.RawMessage(nil), rec...))
		}
		tooLong, line = false, line[:0]
		if rerr == io.EOF {
			return recs, skipped, nil
		}
	}
}
