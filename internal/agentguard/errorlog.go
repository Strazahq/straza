package agentguard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/version"
)

// The client error log records hook, spool and drain failures locally: event
// name, error string, timestamp, and never payload content. Prompts, commands,
// file contents and tool arguments do not reach this file.
//
// Writes are best-effort by construction: a hook that fails must fail exactly
// as it did before this log existed (same stdout bytes, same exit code), so
// every write error is swallowed, there is no fsync (a torn last line costs a
// diagnostic record, not evidence), and each append is one small O_APPEND
// write, which the kernel keeps atomic across concurrent hook processes.
//
// Bound: size cap plus one rename-based rotation, about 2x errorLogMaxBytes on
// disk. Concurrent rotators race harmlessly, both landing their append in the
// fresh live file. This is a diagnostic ring whose oldest lines are the right
// thing to lose, so it must NOT reuse the audit-dropped marker's compaction,
// which preserves running TOTALS forever as loss evidence.
const errorLogName = "errors.jsonl"

// errorLogMaxBytes caps one generation of the log; live + one rotation bound
// the total at ~1 MiB. Var, not const, so tests can shrink it.
var errorLogMaxBytes int64 = 512 << 10

// errorLogMaxErrBytes caps a single record's error string, keeping every line
// far under the size where an O_APPEND write could tear (records stay <4 KiB).
const errorLogMaxErrBytes = 2048

// errorLogTruncSuffix marks a truncated error string so nobody mistakes it
// for the whole message.
const errorLogTruncSuffix = "…[truncated]"

// errorLogRecent is the doctor surfacing window: hook failures are debugged
// close to when they happen, and a warning that never ages out would train
// operators to skim past doctor. Unlike audit-dropped (unrecoverable loss,
// FAIL until acknowledged), an old error here is history `straza logs` keeps,
// not a live condition.
const errorLogRecent = 24 * time.Hour

// ClientError is one record of the client error log (~/.straza/state/
// errors.jsonl, JSON Lines). The shape is versioned (V) so a future fleet
// drain can ship these records without a format break. It never carries
// payload content: event NAME and error string only.
type ClientError struct {
	// V is the record format version (currently 1).
	V int `json:"v"`
	// TS is the failure time, UTC.
	TS time.Time `json:"ts"`
	// Kind names the failing surface: "hook" (the governed hook path),
	// "spool" (audit spool append), "drain" (audit upload), "mcp" (the
	// stdio proxy's gateway lane, e.g. session revival breadcrumbs),
	// "identity" (persisting a renewed device credential).
	Kind string `json:"kind"`
	// Harness is the dialect in play, when known (claude-code|codex|gemini).
	Harness string `json:"harness,omitempty"`
	// Event is the harness-native event name, when the payload got far enough
	// to name one (e.g. "Stop").
	Event string `json:"event,omitempty"`
	// Err is the error string, truncated at errorLogMaxErrBytes.
	Err string `json:"err"`
	// Exit is the process exit code the failure produced, when meaningful
	// (2 = fail-closed deny encoding, 1 = bare error).
	Exit int `json:"exit,omitempty"`
	// Bin is the straza version that wrote the record; a fleet log survives
	// binary re-stages, and "which build failed" is the first triage question.
	Bin string `json:"bin,omitempty"`
	// PID distinguishes interleaved concurrent hook processes.
	PID int `json:"pid,omitempty"`
}

// ErrorLogPath returns the live client error log file (state/errors.jsonl;
// the single rotated generation lives beside it with a ".1" suffix).
func (s *Store) ErrorLogPath() string { return s.statePath(errorLogName) }

// logClientError appends one record to the client error log, best-effort:
// every failure is swallowed, because this log must never alter the behavior
// of the failing path it observes. Zero-value TS/V/Bin/PID are stamped here;
// a caller-set TS is honored (tests, replayed records).
func (s *Store) logClientError(e ClientError) {
	if e.V == 0 {
		e.V = 1
	}
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	if e.Bin == "" {
		e.Bin = version.Get().Version
	}
	if e.PID == 0 {
		e.PID = os.Getpid()
	}
	if len(e.Err) > errorLogMaxErrBytes {
		e.Err = e.Err[:errorLogMaxErrBytes] + errorLogTruncSuffix
	}
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	path := s.ErrorLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	// Rotate BEFORE the append so the live file stays under the cap (+ at most
	// the one record whose pre-append stat raced another writer). A failed
	// rename means a concurrent rotator won, or the platform refused (an open
	// handle on Windows); either way appending to the live name stays correct.
	if fi, statErr := os.Stat(path); statErr == nil && fi.Size() >= errorLogMaxBytes {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- our own state file
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

// logSpoolError records a failed audit-spool append for the event it was
// carrying. nil is a no-op, so call sites stay one line.
func (s *Store) logSpoolError(n Normalized, err error) {
	if err == nil {
		return
	}
	s.logClientError(ClientError{Kind: "spool", Harness: n.HarnessName, Event: n.NativeEvent, Err: err.Error()})
}

// logDrainError records a failed audit drain (upload attempted and failed;
// callers gate out the no-session/no-config states, which are not errors).
func (s *Store) logDrainError(err error) {
	if err == nil {
		return
	}
	s.logClientError(ClientError{Kind: "drain", Err: err.Error()})
}

// RecordHookFailure is the cmd-layer safety net for the exit-1 class: errors
// `straza hook` returns BARE (encode/stdout failures, the codex Stop shape
// seen live), which never pass the fail-closed choke point that logs
// exit-2 denials. Deny-coded errors are skipped here precisely because that
// choke point already recorded them, and a policy deny is not an error at all.
// Best-effort: a machine whose home cannot even be resolved has nowhere to log.
func RecordHookFailure(harness string, err error) {
	if err == nil {
		return
	}
	if _, isDeny := err.(interface{ Code() int }); isDeny {
		return
	}
	store, openErr := OpenStore()
	if openErr != nil {
		return
	}
	store.logClientError(ClientError{Kind: "hook", Harness: harness, Err: err.Error(), Exit: 1})
}

// RecordHookArgvFailure is the outermost safety net, above RecordHookFailure:
// argv that does not PARSE is rejected by the CLI layer before the hook
// command's code (and its logging) ever runs. That is the shape seen live on
// Windows, where a codex-cli 0.146 quoting change mangled the wired command
// line, straza exited 1, stderr was harness-swallowed, and the fresh error
// log stayed blind at exactly the failing layer. The record carries argv
// verbatim: it is the wiring's own command line (the hook payload rides
// stdin, which this path never reads; regression-pinned), and "what did the
// harness actually execute" is the one fact a parse failure needs.
func RecordHookArgvFailure(args []string, err error) {
	if err == nil {
		return
	}
	store, openErr := OpenStore()
	if openErr != nil {
		return
	}
	store.logClientError(ClientError{Kind: "hook",
		Err: fmt.Sprintf("argv did not parse: %v; argv=%q", err, args), Exit: 1})
}

// ClientErrors reads the whole client error log, oldest first (the rotated
// generation, then the live file), skipping lines that do not parse and
// counting them: a torn or hand-mangled line must not hide the rest.
// Bounded by construction: rotation caps what can ever be read here.
func ClientErrors(store *Store) (errs []ClientError, unreadable int) {
	live := store.ErrorLogPath()
	for _, path := range []string{live + ".1", live} {
		raw, err := os.ReadFile(path) // #nosec G304 -- our own state file
		if err != nil {
			continue // absent generation: nothing recorded there
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var e ClientError
			if json.Unmarshal([]byte(line), &e) != nil {
				unreadable++
				continue
			}
			errs = append(errs, e)
		}
	}
	return errs, unreadable
}

// RenderClientError renders one record as the single greppable line `straza
// logs` prints: RFC3339 timestamp, kind, harness, event ("-" where unknown),
// the error, and the exit code when one applies.
func RenderClientError(e ClientError) string {
	harness, event := e.Harness, e.Event
	if harness == "" {
		harness = "-"
	}
	if event == "" {
		event = "-"
	}
	line := fmt.Sprintf("%s %s %s %s: %s", e.TS.UTC().Format(time.RFC3339), e.Kind, harness, event, e.Err)
	if e.Exit != 0 {
		line += fmt.Sprintf(" (exit %d)", e.Exit)
	}
	return line
}

// clientErrorsCheck surfaces recent client errors in doctor. No log at all =
// no line (nothing ever failed on this box; a routine "no errors" row would
// train operators to skim). Errors inside the last errorLogRecent window WARN
// and never FAIL: the failing path already failed closed at the time, and
// doctor's job here is pointing the human at the record, not re-alarming a
// handled condition. An older log reports ok so the operator knows the
// surface exists and where the history lives.
func clientErrorsCheck(store *Store, now time.Time) *Check {
	errs, unreadable := ClientErrors(store)
	if len(errs) == 0 && unreadable == 0 {
		return nil
	}
	recent := 0
	var last ClientError
	for _, e := range errs {
		if now.Sub(e.TS) <= errorLogRecent {
			recent++
			last = e
		}
	}
	if recent == 0 {
		detail := fmt.Sprintf("none in the last 24h (%d older on record: `straza logs`)", len(errs))
		if unreadable > 0 {
			detail += fmt.Sprintf("; %d unreadable line(s)", unreadable)
		}
		return &Check{"client-errors", checkOK, detail, ""}
	}
	return &Check{"client-errors", checkWarn,
		fmt.Sprintf("%d client error(s) in the last 24h, last: %s", recent, RenderClientError(last)),
		"read `straza logs` for the full record. Each entry names the failing surface and event. Hook failures failed closed at the time; a repeating one means a broken path (a stale binary, an unmapped event) that needs fixing, not just re-running"}
}
