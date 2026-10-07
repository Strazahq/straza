package agentguard

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/redact"
)

// Conversation capture, client half:
// prompts are captured at prompt.submit (the hook payload carries the text),
// replies at session.end by reading the delta of the harness transcript file
// since the last capture. Everything rides the existing audit spool: capture
// is observational and MUST never block or fail a session.

// captureMaxBytes caps one captured turn's stored content. The full-content
// hash is computed BEFORE the cap, so the chain witnesses what was cut.
const captureMaxBytes = 64 << 10

// redactedMark replaces matched secrets in redact mode (kept in step with the
// shared battery's mark).
const redactedMark = redact.Mark

// redactSecrets masks recognizable credential patterns (capture mode
// `redact`, spec/policyset §6) via the shared battery (internal/redact).
func redactSecrets(s string) string {
	return redact.Redact(s)
}

// captureContent prepares one turn for the spool: hash the FULL original,
// then apply the mode, then the size cap.
func captureContent(content, mode string) (stored string, truncated bool, hash string) {
	sum := sha256.Sum256([]byte(content))
	hash = "sha256:" + hex.EncodeToString(sum[:])
	if mode == policy.CaptureModeRedact {
		content = redactSecrets(content)
	}
	if len(content) > captureMaxBytes {
		content = content[:captureMaxBytes]
		truncated = true
	}
	return content, truncated, hash
}

// Capture returns the session's conversation-capture directive from the
// snapshot the PDP already holds.
func (p *LocalPDP) Capture() policy.CaptureDirective { return p.engine.Capture(p.subject) }

// maxTranscriptLine bounds one transcript record.
const maxTranscriptLine = 8 << 20

// watermarkPath is the per-transcript watermark file. The key is a wide hash
// of the path: a short one lets two unrelated transcripts share a watermark
// and cross-contaminate each other's deltas.
func watermarkPath(store *Store, path string) string {
	sum := sha256.Sum256([]byte(path))
	return store.statePath("capture-" + hex.EncodeToString(sum[:16]) + ".offset")
}

// legacyWatermarkPath is the 4-byte key older clients wrote. It is adopted,
// and removed, on first contact so the one turn that straddles a binary
// upgrade reads its delta from the old position instead of slurping from byte 0.
func legacyWatermarkPath(store *Store, path string) string {
	sum := sha256.Sum256([]byte(path))
	return store.statePath("capture-" + hex.EncodeToString(sum[:4]) + ".offset")
}

// governedMark, when present after the offset, records that the watermark was
// last written under an active capture directive. That flag is what lets the
// next prompt tell "a governed reply escaped capture because session.end never
// fired" (recover it) apart from "this text predates capture" (never touch).
const governedMark = "capturing"

// readWatermark returns the transcript's watermark and whether it was written
// under capture. A missing new-key file falls back to the legacy key, which
// never carries the flag (the safe direction: legacy state can only seed, not
// authorize recovery).
func readWatermark(store *Store, path string) (offset int64, governed bool) {
	raw, err := os.ReadFile(watermarkPath(store, path)) // #nosec G304 -- our own state file
	if err != nil {
		if raw, err = os.ReadFile(legacyWatermarkPath(store, path)); err != nil { // #nosec G304
			return 0, false
		}
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, len(fields) > 1 && fields[1] == governedMark
}

// writeWatermark persists the watermark under the current key and retires any
// legacy-key file, completing the migration.
func writeWatermark(store *Store, path string, n int64, governed bool) {
	content := strconv.FormatInt(n, 10)
	if governed {
		content += " " + governedMark
	}
	_ = os.WriteFile(watermarkPath(store, path), []byte(content), 0o600)
	_ = os.Remove(legacyWatermarkPath(store, path))
}

// pinTranscriptWatermark pins the reply watermark to the transcript's current
// end and returns any governed reply a missed session.end left behind. It runs
// at prompt.submit: the one point in a turn where the model has not yet
// written its reply, so everything already on disk is either captured, orphaned
// by a kill (recovered here iff BOTH then and now are governed), or was
// produced while capture was off and stays permanently out of reach.
//
// Pinning unconditionally, rather than only on a first sighting, is what makes
// enabling capture mid-session honest: a stale watermark would turn "capture
// from here on" into "capture the entire backlog as one giant turn". A session
// captured from its start is unaffected: at its first
// prompt the reply does not exist yet, so nothing is skipped.
func pinTranscriptWatermark(store *Store, path string, capturing bool) (lateReply string) {
	if path == "" {
		return ""
	}
	if capturing {
		if _, governed := readWatermark(store, path); governed {
			// The previous turn was governed and its watermark never advanced
			// past its reply: session.end did not fire (killed terminal,
			// crash). Recover the orphan instead of sealing it away.
			if delta, err := transcriptDelta(store, path); err == nil {
				lateReply = delta
			}
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		// A governed FIRST turn whose transcript does not exist yet still
		// deserves a watermark: without one, a kill during that turn would
		// leave its reply unrecoverable (no flag to authorize recovery).
		if capturing && os.IsNotExist(err) {
			writeWatermark(store, path, 0, true)
		}
		return lateReply
	}
	writeWatermark(store, path, fi.Size(), capturing)
	return lateReply
}

// sealTranscript advances a transcript's watermark to the file's current end
// WITHOUT capturing: the capture-off boundary for transcripts that have no
// prompt.submit pin (a delegate's file is only ever named by its stop event).
// The flag is written false: text sealed here was produced under an inactive
// directive and is permanently out of reach, never recoverable.
func sealTranscript(store *Store, path string) {
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	writeWatermark(store, path, fi.Size(), false)
}

// transcriptDelta returns the assistant text appended to a claude-jsonl
// transcript since the last capture, advancing a per-transcript byte-offset
// state file. A shrunken file (rotation/replacement) resets the offset. Every
// error path returns what it safely can; capture never blocks a hook.
//
// The watermark only ever advances to the end of the last COMPLETE line. The
// harness appends to this file concurrently, so its tail is routinely a
// half-written record; consuming one would parse garbage and then skip the
// finished line on the next call, silently losing that turn forever.
func transcriptDelta(store *Store, path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- path comes from the harness's own hook payload
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}

	offset, _ := readWatermark(store, path)
	if offset > fi.Size() {
		offset = 0 // transcript rotated or replaced; start over
	}
	end, err := lastCompleteLineEnd(f, fi.Size())
	if err != nil {
		return "", err
	}
	if end <= offset {
		return "", nil // nothing finished since last time
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}

	var parts []string
	sc := bufio.NewScanner(io.LimitReader(f, end-offset))
	sc.Buffer(make([]byte, 0, 64*1024), maxTranscriptLine)
	for sc.Scan() {
		parts = append(parts, assistantText(sc.Bytes())...)
	}
	if err := sc.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return "", err // transient: leave the watermark, retry this range
	}
	// An over-long record is unparseable anyway; skip past it rather than
	// wedging capture on this transcript forever. transcriptDelta only runs
	// under an active capture directive, so the flag is always set here.
	writeWatermark(store, path, end, true)
	return strings.Join(parts, "\n"), nil
}

// lastCompleteLineEnd returns the offset just past the final newline in the
// file: the end of the last record the harness finished writing. Zero means
// no complete line exists yet. It reads backwards, so a large transcript costs
// one buffer rather than a full scan.
func lastCompleteLineEnd(f *os.File, size int64) (int64, error) {
	const chunk = 64 << 10
	buf := make([]byte, chunk)
	for pos := size; pos > 0; {
		n := int64(chunk)
		if pos < n {
			n = pos
		}
		pos -= n
		if _, err := f.ReadAt(buf[:n], pos); err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if i := bytes.LastIndexByte(buf[:n], '\n'); i >= 0 {
			return pos + int64(i) + 1, nil
		}
	}
	return 0, nil
}

// assistantText extracts the text parts of one transcript line when it is an
// assistant message. Two shapes, tolerant (anything unparseable is simply
// not a reply):
//   - claude-jsonl:  {"type":"assistant","message":{"content":...}}
//   - codex rollout: {"type":"response_item","payload":{"type":"message",
//     "role":"assistant","content":[{"type":"output_text","text":...}]}}
//     (~/.codex/sessions/**/rollout-*.jsonl; the vendor calls the format
//     unstable, hence a shape probe, never a strict schema)
func assistantText(line []byte) []string {
	var rec struct {
		Type string `json:"type"`
		// Harness error banners ("API Error…") are assistant-typed records
		// but are the harness talking, not the model; never a reply.
		IsAPIError bool `json:"isApiErrorMessage"`
		Message    struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Payload struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &rec) != nil {
		return nil
	}
	var content json.RawMessage
	switch {
	case rec.Type == "assistant" && !rec.IsAPIError && len(rec.Message.Content) > 0:
		content = rec.Message.Content
	case rec.Type == "response_item" && rec.Payload.Type == "message" &&
		rec.Payload.Role == "assistant" && len(rec.Payload.Content) > 0:
		content = rec.Payload.Content
	default:
		return nil
	}
	// content is either a plain string or a list of typed parts.
	var s string
	if json.Unmarshal(content, &s) == nil {
		if s == "" {
			return nil
		}
		return []string{s}
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		if (b.Type == "text" || b.Type == "output_text") && b.Text != "" {
			out = append(out, b.Text)
		}
	}
	return out
}
