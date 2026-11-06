package spool

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// reportLoss sends the loss ledger in dir to the server as one loss record
// and removes the ledger only once the server accepted it. A refused upload
// or a crash keeps the report claim, and the next drain sends it again,
// unchanged and under the same id, so the server dedupes a record it
// already took. Drops noted meanwhile wait in the live ledger, which stays
// compacted, for the report after it. It holds the ledger's lock to claim,
// read and remove, never during the upload, so a note never waits on the
// network. A client can forge a loss record only about its own uploads, so
// a forged one is noise and hides nothing.
func reportLoss(ctx context.Context, client Uploader, sessionToken, dir string) error {
	raw, id, claims := claimLoss(dir)
	if st := ParseDroppedMarker(raw); st.Events > 0 {
		rec, err := encodeCE(id, "straza.audit.tool", lossData(st))
		if err != nil {
			return err
		}
		if _, err := client.AuditBatch(ctx, sessionToken, []json.RawMessage{rec}); err != nil {
			return err
		}
	}
	mu := ledgerLock(dir)
	mu.Lock()
	defer mu.Unlock()
	for _, c := range claims {
		_ = os.Remove(c)
	}
	return nil
}

// claimLoss renames the live ledger in dir to a report claim when no
// report claim waits, and returns the text of the report claims it read,
// the loss record's id over their names and text, and the claims, all
// under the ledger's lock.
func claimLoss(dir string) (raw []byte, id string, claims []string) {
	mu := ledgerLock(dir)
	mu.Lock()
	defer mu.Unlock()
	path := filepath.Join(dir, DroppedMarkerName)
	found, _ := filepath.Glob(path + DroppedReportingSuffix + "*")
	if len(found) == 0 {
		foldClaims(path)
		if os.Rename(path, path+DroppedReportingSuffix+uuid.NewString()) != nil {
			return nil, "", nil // no ledger, or a Windows handle on it until the next drain
		}
		found, _ = filepath.Glob(path + DroppedReportingSuffix + "*")
	}
	sort.Strings(found)
	h := sha256.New()
	for _, c := range found {
		b, err := os.ReadFile(c) // #nosec G304 -- our own ledger
		if err != nil {
			continue // kept for the next drain, and Dropped still counts it
		}
		h.Write([]byte(filepath.Base(c) + "\n"))
		h.Write(b)
		raw = append(append(raw, b...), '\n')
		claims = append(claims, c)
	}
	return raw, uuid.NewSHA1(uuid.NameSpaceOID, h.Sum(nil)).String(), claims
}

// lossData is the data of the loss record of st. It names no session, so
// the server files it under the uploading session and its user.
func lossData(st DroppedStats) map[string]any {
	data := map[string]any{
		"event":           LossEvent,
		"droppedRecords":  st.Lost(),
		"oversizeRecords": st.Records,
		"evictedFiles":    st.Files,
		"evictedRecords":  st.FileRecords,
		"refusedCalls":    st.Refused,
		"dropEvents":      st.Events,
		"reason":          lossReason(st),
	}
	if !st.First.IsZero() {
		data["firstDrop"] = st.First.UTC().Format(time.RFC3339)
		data["lastDrop"] = st.Last.UTC().Format(time.RFC3339)
	}
	return data
}

// lossReason words the loss record of st for the people who read the audit.
func lossReason(st DroppedStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The Straza background service on this machine lost %s of this user before they reached strazad",
		count(st.Lost(), "audit record"))
	switch {
	case st.First.IsZero():
	case st.First.Equal(st.Last):
		fmt.Fprintf(&b, ", at %s", st.First.UTC().Format(time.RFC3339))
	default:
		fmt.Fprintf(&b, ", between %s and %s", st.First.UTC().Format(time.RFC3339), st.Last.UTC().Format(time.RFC3339))
	}
	b.WriteString(".")
	switch {
	case st.Records == 1:
		b.WriteString(" 1 was larger than one upload can carry.")
	case st.Records > 1:
		fmt.Fprintf(&b, " %d were each larger than one upload can carry.", st.Records)
	}
	if st.Files > 0 {
		fmt.Fprintf(&b, " It discarded %s holding %s because uploads failed while the unsent backlog passed the spool cap.",
			count(st.Files, "unsent spool file"), count(st.FileRecords, "record"))
	}
	if st.Refused > 0 {
		fmt.Fprintf(&b, " It refused %s because this user already had as many decide and capture calls running as it takes at once, or because the call was malformed or too large to read. "+
			"A refused tool call was denied with no record, and a refused captured turn was never recorded.",
			count(st.Refused, "decide or capture call"))
	}
	b.WriteString(" That audit is lost. Look for the gap in this user's other records, " +
		"and check the Straza service log on that machine for failed uploads.")
	return b.String()
}

// count words n of noun, with the plural s when n is not 1.
func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// NoteRefused counts in the loss ledger one call the service refused before
// it could record it, so the next loss record names it. call names the
// call, decide or capture.
func (s *Spool) NoteRefused(call string) {
	noteDropped(filepath.Dir(s.path), "1 refused "+call+" call(s)")
}

// ledgerLocks holds one mutex per spool directory. Every write of a loss
// ledger, and every read that reports or counts it, holds the mutex of its
// directory, because the service notes refused calls on its request
// goroutines while a drain folds, claims and reports the same ledger.
var ledgerLocks sync.Map

// ledgerLock is the mutex of the loss ledger in dir.
func ledgerLock(dir string) *sync.Mutex {
	mu, _ := ledgerLocks.LoadOrStore(filepath.Clean(dir), &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// noteDropped appends one line to the audit-dropped marker, best-effort
// (compacting it first when it outgrew markerCompactBytes), under the
// ledger's lock.
func noteDropped(dir, what string) {
	mu := ledgerLock(dir)
	mu.Lock()
	defer mu.Unlock()
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
// ParseDroppedMarker round-trips it: totals and the first and last drop
// times survive any number of compactions. Best-effort with a fail-safe
// order: the claim is removed only after its summary landed, so any failure
// keeps the evidence (in a file doctor also reads) rather than the
// directory clean.
func foldOneClaim(path, claim string) {
	raw, err := os.ReadFile(claim) // #nosec G304 -- our own marker
	if err != nil {
		return
	}
	st := ParseDroppedMarker(raw)
	if st.Events == 0 {
		return // nothing parseable to fold; leave it for doctor to warn about
	}
	first, last := st.First, st.Last
	if last.IsZero() {
		first, last = time.Now().UTC(), time.Now().UTC()
	}
	line := fmt.Sprintf("%s dropped %d oversize record(s) and %d parked file(s) holding %d record(s) and %d refused call(s) spanning %d drop event(s) since %s [compacted]\n",
		last.UTC().Format(time.RFC3339), st.Records, st.Files, st.FileRecords, st.Refused, st.Events, first.UTC().Format(time.RFC3339))
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
// machine discarded, over how many drop events, and when.
type DroppedStats struct {
	Events      int       // drop events recorded (compacted lines carry their span)
	Records     int       // oversize records dropped by uploadFile
	Files       int       // parked files dropped by enforceSpoolCap
	FileRecords int       // records in those files, a file whose line does not count them as one
	Refused     int       // calls the service refused before it could record them (NoteRefused)
	First       time.Time // oldest drop time seen on any line, zero when none carried one
	Last        time.Time // newest drop time seen on any line, zero when none carried one
	LastLine    string    // final line, verbatim
}

// Lost is how many records the ledger says never reached the server:
// those discarded and those of refused calls.
func (st DroppedStats) Lost() int { return st.Records + st.FileRecords + st.Refused }

// ParseDroppedMarker sums the marker's lines. Grammar per line:
// `<RFC3339> dropped N oversize record(s)` / `... N parked file(s) holding
// R record(s) over the C-byte spool cap`, where older lines lack the
// holding part / `... N refused <call> call(s)` / the compacted summary
// carrying `spanning N drop event(s)` and `since <RFC3339>`, its first drop.
// A line it cannot read still counts as one drop event, and a negative
// count is not read, so no line counts as less: the marker exists to make
// loss visible, so unknown loss is loss, not nothing.
func ParseDroppedMarker(raw []byte) DroppedStats {
	var st DroppedStats
	seen := func(ts time.Time) {
		if st.First.IsZero() || ts.Before(st.First) {
			st.First = ts
		}
		if ts.After(st.Last) {
			st.Last = ts
		}
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		st.LastLine = line
		fields := strings.Fields(line)
		// The oldest and newest times win, not the line order: Dropped
		// concatenates the live marker with parked claims, and those
		// sources are not ordered.
		if ts, err := time.Parse(time.RFC3339, fields[0]); err == nil {
			seen(ts)
		}
		lineEvents, files, fileRecords, counted := 1, 0, 0, false
		for i, f := range fields[:len(fields)-1] {
			next := fields[i+1]
			if f == "since" {
				if ts, err := time.Parse(time.RFC3339, next); err == nil {
					seen(ts)
				}
				continue
			}
			n, err := strconv.Atoi(f)
			if err != nil || n < 0 {
				continue
			}
			switch {
			case strings.HasPrefix(next, "oversize"):
				st.Records += n
			case strings.HasPrefix(next, "parked"):
				files += n
			case strings.HasPrefix(next, "record"):
				fileRecords, counted = fileRecords+n, true
			case strings.HasPrefix(next, "refused"):
				st.Refused += n
			case strings.HasPrefix(next, "drop"):
				lineEvents = max(n, 1)
			}
		}
		if !counted {
			fileRecords = files
		}
		st.Events += lineEvents
		st.Files += files
		st.FileRecords += fileRecords
	}
	return st
}

// Dropped sums the loss ledger in dir: the live marker, its compaction
// claims and its report claims, so loss still on its way to the server
// counts too.
func Dropped(dir string) DroppedStats {
	mu := ledgerLock(dir)
	mu.Lock()
	defer mu.Unlock()
	path := filepath.Join(dir, DroppedMarkerName)
	compacting, _ := filepath.Glob(path + DroppedCompactingSuffix + "*")
	reporting, _ := filepath.Glob(path + DroppedReportingSuffix + "*")
	var raw []byte
	for _, p := range append(append([]string{path}, compacting...), reporting...) {
		if b, err := os.ReadFile(p); err == nil { // #nosec G304 -- our own ledger
			raw = append(append(raw, b...), '\n')
		}
	}
	return ParseDroppedMarker(raw)
}
