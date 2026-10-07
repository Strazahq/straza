package agentguard

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// geminiSessionStateLines are VERBATIM lines from a real gemini-cli
// session-state file, the file gemini hands hooks as `transcript_path`. Only
// the workdir/home PATH VALUES inside the session_context text are sanitized
// to placeholders; every shape and key is as gemini wrote it.
//
// The file is gemini's own session state, not a reply transcript: a metadata
// header, whole-message appends whose type is "user" or "gemini", and
// {"$set":{...}} deltas. Nothing in it matches either shape assistantText
// probes (the JSONL {"type":"assistant","message":{"content":...}} form, or
// the codex rollout response_item), so the transcript fallback in captureReply
// is a harmless no-op for gemini: replies arrive in the AfterAgent payload
// (prompt_response), which is gemini's ONLY reply lane.
var geminiSessionStateLines = []struct {
	name string
	line string
}{
	{"header (session metadata; no type key)", `{"sessionId":"d90558c4-862b-4799-946b-4186a8294321","projectHash":"d810e39dead20de26e08cf08cace63c54638116b2f33e47811191edbffe48216","startTime":"2026-07-31T12:50:23.324Z","lastUpdated":"2026-07-31T12:50:23.324Z","kind":"main"}`},
	{"$set messages (session_context user message)", `{"$set":{"messages":[{"id":"d04923d38bb0f6017037e74183378ef4","timestamp":"2026-07-31T12:50:23.324Z","type":"user","content":[{"text":"<session_context>\nThis is the Gemini CLI. We are setting up the context for our chat.\nToday's date is Friday, July 31, 2026 (formatted according to the user's locale).\nMy operating system is: linux\nThe project's temporary directory is: /home/u/.gemini/tmp/work\n- **Workspace Directories:**\n  - /work/proj\n- **Directory Structure:**\n\nShowing up to 200 items (files + folders).\n\n/work/proj/\n\n\n\n</session_context>"}]}],"lastUpdated":"2026-07-31T12:50:23.324Z"}}`},
	{"user prompt message (top-level type/content)", `{"id":"5b4eb006-25b9-45b7-a086-28a59984fd0f","timestamp":"2026-07-31T12:50:23.445Z","type":"user","content":[{"text":"say hi"}]}`},
	{"$set lastUpdated", `{"$set":{"lastUpdated":"2026-07-31T12:50:23.445Z"}}`},
	{"MODEL REPLY message (type gemini, content string)", `{"id":"1c0ed97a-ccfe-475b-9b12-231100785aa0","timestamp":"2026-07-31T12:50:23.468Z","type":"gemini","content":"Hello from the fake gateway. This is the model reply text.","thoughts":[],"tokens":{"input":7,"output":9,"cached":0,"thoughts":0,"tool":0,"total":16},"model":"gemini-3.1-pro-preview"}`},
	{"$set lastUpdated after the reply", `{"$set":{"lastUpdated":"2026-07-31T12:50:23.468Z"}}`},
}

// TestGeminiSessionStateLinesAreNotReplies pins the harmless-no-op verdict: no
// line of gemini's session-state file may be mistaken for an assistant reply.
// The model reply DOES live in this file (the "MODEL REPLY message" row), so
// the assertion is not vacuous: it says our reader deliberately does not read
// gemini's dialect, and must never half-read it either. Each fixture is also
// re-parsed as JSON: a line that stopped being valid JSON would pass the
// no-reply assertion for the wrong reason (unmarshal failure), which is
// exactly the fixture-blindness this test exists to prevent.
func TestGeminiSessionStateLinesAreNotReplies(t *testing.T) {
	for _, tc := range geminiSessionStateLines {
		var probe any
		if err := json.Unmarshal([]byte(tc.line), &probe); err != nil {
			t.Errorf("fixture %q is not valid JSON (corrupted transcription): %v", tc.name, err)
			continue
		}
		if got := assistantText([]byte(tc.line)); got != nil {
			t.Errorf("gemini session-state line %q captured as a reply: %q", tc.name, got)
		}
	}
}

// TestTranscriptDeltaGeminiSessionState runs the real reader over the real
// file: the delta is empty and error-free (never garbage, never a wedge), and
// the watermark still advances to the end so the next turn starts clean. The
// positive control at the end proves the reader itself is alive.
func TestTranscriptDeltaGeminiSessionState(t *testing.T) {
	store, path := captureFixture(t)
	var b strings.Builder
	for _, tc := range geminiSessionStateLines {
		b.WriteString(tc.line)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	delta, err := transcriptDelta(store, path)
	if err != nil {
		t.Fatalf("gemini session-state file errored the reader: %v", err)
	}
	if delta != "" {
		t.Errorf("gemini session-state delta = %q, want empty", delta)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	off, governed := readWatermark(store, path)
	if off != fi.Size() || !governed {
		t.Errorf("watermark = (%d, %v), want (%d, true)", off, governed, fi.Size())
	}

	// Positive control: same reader, same store, one claude-jsonl assistant
	// line appended. If this stops being captured, the assertions above are
	// pinning a dead reader rather than a dialect gap.
	appendTranscript(t, path, `{"type":"assistant","message":{"content":[{"type":"text","text":"control reply"}]}}`+"\n")
	if delta, err := transcriptDelta(store, path); err != nil || delta != "control reply" {
		t.Errorf("positive control: delta = %q, err = %v", delta, err)
	}
}
