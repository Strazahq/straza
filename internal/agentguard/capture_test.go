package agentguard

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/spool"
	"github.com/strazahq/straza/internal/policy"
)

func TestRedactSecrets(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		wantGone string // substring that must NOT survive
		wantKept string // substring that must survive
	}{
		{"aws key", "creds: AKIAIOSFODNN7EXAMPLE ok", "AKIAIOSFODNN7EXAMPLE", "creds:"},
		{"github pat", "use ghp_abcdefghijklmnopqrstuvwxyz0123456789 here", "ghp_abcdef", "use"},
		{"bearer header", "Authorization: Bearer eyJhbGciOiJFUzI1NiJ9.payload.sig", "eyJhbGci", "Authorization:"},
		{"pem block", "key:\n-----BEGIN PRIVATE KEY-----\nMIIEvg==\n-----END PRIVATE KEY-----\ndone", "MIIEvg", "done"},
		{"straza api token", "token wat_0123456789abcdefghijklmnopqrstuvwxyzABCDEF used", "wat_0123", "used"},
		{"plain text untouched", "deploy the staging branch", "", "deploy the staging branch"},
	} {
		got := redactSecrets(tc.in)
		if tc.wantGone != "" && strings.Contains(got, tc.wantGone) {
			t.Errorf("%s: %q still contains %q", tc.name, got, tc.wantGone)
		}
		if !strings.Contains(got, tc.wantKept) {
			t.Errorf("%s: %q lost surrounding text %q", tc.name, got, tc.wantKept)
		}
		if tc.wantGone != "" && !strings.Contains(got, redactedMark) {
			t.Errorf("%s: no redaction marker in %q", tc.name, got)
		}
	}
}

func TestCaptureContent(t *testing.T) {
	// Hash is of the FULL ORIGINAL, mode and cap applied after.
	original := strings.Repeat("a", captureMaxBytes+50)
	sum := sha256.Sum256([]byte(original))
	wantHash := "sha256:" + hex.EncodeToString(sum[:])

	stored, truncated, hash := captureContent(original, "verbatim")
	if !truncated || len(stored) != captureMaxBytes || hash != wantHash {
		t.Errorf("verbatim oversized: truncated=%v len=%d hash ok=%v", truncated, len(stored), hash == wantHash)
	}

	small := "the AWS key is AKIAIOSFODNN7EXAMPLE"
	stored, truncated, _ = captureContent(small, "redact")
	if truncated {
		t.Error("small content must not truncate")
	}
	if strings.Contains(stored, "AKIAIOSFODNN7EXAMPLE") {
		t.Error("redact mode stored the secret verbatim")
	}
	// The hash still witnesses the ORIGINAL (leak hunt by value works even
	// when the stored content is masked).
	_, _, hash = captureContent(small, "redact")
	sum = sha256.Sum256([]byte(small))
	if hash != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Error("redact mode must hash the pre-redaction content")
	}
}

func TestTranscriptDelta(t *testing.T) {
	dir := t.TempDir()
	store := &Store{root: dir}
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "transcript.jsonl")

	write := func(lines ...string) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		for _, l := range lines {
			if _, err := f.WriteString(l + "\n"); err != nil {
				t.Fatal(err)
			}
		}
	}

	write(
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hello, deploying now."}]}}`,
		`not json at all`,
	)
	delta, err := transcriptDelta(store, path)
	if err != nil {
		t.Fatal(err)
	}
	if delta != "Hello, deploying now." {
		t.Errorf("first delta = %q", delta)
	}

	// Nothing new → empty delta, no event.
	if delta, _ := transcriptDelta(store, path); delta != "" {
		t.Errorf("no-change delta = %q", delta)
	}

	// Only the appended assistant text comes back; multiple text parts join.
	write(`{"type":"assistant","message":{"content":[{"type":"text","text":"Done."},{"type":"text","text":"All green."}]}}`)
	delta, _ = transcriptDelta(store, path)
	if delta != "Done.\nAll green." {
		t.Errorf("second delta = %q", delta)
	}

	// A rotated/replaced (smaller) transcript resets the offset instead of
	// silently capturing nothing forever.
	if err := os.WriteFile(path, []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"fresh"}]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if delta, _ := transcriptDelta(store, path); delta != "fresh" {
		t.Errorf("post-rotation delta = %q", delta)
	}
}

// captureFixture is an isolated store plus the path of a transcript that does
// not exist yet.
func captureFixture(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	return &Store{root: dir}, filepath.Join(dir, "transcript.jsonl")
}

// appendTranscript writes raw bytes verbatim. No newline is added, so tests
// can reproduce a half-written record.
func appendTranscript(t *testing.T, path, raw string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(raw); err != nil {
		t.Fatal(err)
	}
}

func assistantLine(text string) string {
	return `{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
}

// TestTranscriptDeltaTornLine pins the concurrency contract: the harness
// appends while hooks read, so the tail is routinely a half-written record.
// A torn line must not be consumed: advancing the watermark past it loses the
// finished turn forever.
func TestTranscriptDeltaTornLine(t *testing.T) {
	store, path := captureFixture(t)
	appendTranscript(t, path, assistantLine("one"))
	appendTranscript(t, path, `{"type":"assistant","message":{"content":[{"type":"text","text":"two"`)

	got, err := transcriptDelta(store, path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "one" {
		t.Fatalf("delta over a torn tail = %q, want %q", got, "one")
	}

	appendTranscript(t, path, `}]}}`+"\n") // the harness finishes the record
	got, err = transcriptDelta(store, path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "two" {
		t.Errorf("completed turn = %q, want %q", got, "two")
	}
}

// TestTranscriptDeltaUnterminatedTail covers the other trigger of the same
// failure: a final record with no newline at all. Charging a byte for the
// missing newline would start the next read mid-record.
func TestTranscriptDeltaUnterminatedTail(t *testing.T) {
	store, path := captureFixture(t)
	appendTranscript(t, path, strings.TrimSuffix(assistantLine("only"), "\n"))

	if got, err := transcriptDelta(store, path); err != nil || got != "" {
		t.Fatalf("delta of an unterminated-only transcript = %q (err %v), want empty", got, err)
	}
	appendTranscript(t, path, "\n")
	if got, _ := transcriptDelta(store, path); got != "only" {
		t.Errorf("delta once terminated = %q, want %q", got, "only")
	}
}

// TestPinTranscriptWatermark pins the capture-boundary contract: enabling
// capture mid-session must never reach back
// into text produced while the policy forbade capture, and a session captured
// from its start must still record its very first turn.
func TestPinTranscriptWatermark(t *testing.T) {
	t.Run("mid-session enable leaves the backlog unreachable", func(t *testing.T) {
		store, path := captureFixture(t)
		for i := 0; i < 50; i++ {
			appendTranscript(t, path, assistantLine("ungoverned"))
		}
		pinTranscriptWatermark(store, path, false) // prompt arrives; capture still off
		appendTranscript(t, path, assistantLine("ungoverned"))
		if late := pinTranscriptWatermark(store, path, true); late != "" { // next prompt; capture now on
			t.Errorf("flip-on recovered pre-capture text: %.120q", late)
		}
		appendTranscript(t, path, assistantLine("governed"))

		got, err := transcriptDelta(store, path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, "ungoverned") {
			t.Errorf("captured pre-capture backlog: %.120q", got)
		}
		if got != "governed" {
			t.Errorf("delta = %q, want %q", got, "governed")
		}
	})

	t.Run("captured from session start keeps the first turn", func(t *testing.T) {
		store, path := captureFixture(t)
		appendTranscript(t, path, `{"type":"user","message":{"content":"hi"}}`+"\n")
		pinTranscriptWatermark(store, path, true) // the reply does not exist yet
		appendTranscript(t, path, assistantLine("first reply"))

		if got, _ := transcriptDelta(store, path); got != "first reply" {
			t.Errorf("first turn = %q, want %q", got, "first reply")
		}
	})

	t.Run("re-pinning after a captured turn is a no-op", func(t *testing.T) {
		store, path := captureFixture(t)
		appendTranscript(t, path, assistantLine("captured"))
		if got, _ := transcriptDelta(store, path); got != "captured" {
			t.Fatalf("setup delta = %q", got)
		}
		if late := pinTranscriptWatermark(store, path, true); late != "" { // nothing written since session.end
			t.Errorf("re-pin recovered text that was already captured: %q", late)
		}
		appendTranscript(t, path, assistantLine("next"))
		if got, _ := transcriptDelta(store, path); got != "next" {
			t.Errorf("delta after re-pin = %q, want %q", got, "next")
		}
	})

	t.Run("a missing transcript is survivable", func(t *testing.T) {
		store, path := captureFixture(t)
		pinTranscriptWatermark(store, "", true)
		pinTranscriptWatermark(store, path, true) // never created
	})
}

// TestMissedStopRecovery pins the resume-after-kill contract: a turn whose
// reply was fully written but whose session.end never
// fired (killed terminal, crash) is RECOVERED at the next prompt, but only
// when both that turn and the present were governed. The flag in
// the watermark file is what distinguishes "a governed reply escaped" from
// "this text predates capture", which must stay permanently unreachable.
func TestMissedStopRecovery(t *testing.T) {
	t.Run("killed turn's governed reply is recovered at the next prompt", func(t *testing.T) {
		store, path := captureFixture(t)
		pinTranscriptWatermark(store, path, true) // governed prompt
		appendTranscript(t, path, assistantLine("orphaned reply"))
		// Stop never fires (terminal killed). Session resumes:
		if late := pinTranscriptWatermark(store, path, true); late != "orphaned reply" {
			t.Errorf("late reply = %q, want %q", late, "orphaned reply")
		}
		// And it is not captured twice.
		appendTranscript(t, path, assistantLine("next reply"))
		if got, _ := transcriptDelta(store, path); got != "next reply" {
			t.Errorf("post-recovery delta = %q, want %q", got, "next reply")
		}
	})

	t.Run("normal turns recover nothing", func(t *testing.T) {
		store, path := captureFixture(t)
		pinTranscriptWatermark(store, path, true)
		appendTranscript(t, path, assistantLine("reply"))
		if got, _ := transcriptDelta(store, path); got != "reply" { // Stop fired normally
			t.Fatalf("setup delta = %q", got)
		}
		appendTranscript(t, path, `{"type":"user","message":{"content":"next ask"}}`+"\n")
		if late := pinTranscriptWatermark(store, path, true); late != "" {
			t.Errorf("recovered %q after a clean Stop", late)
		}
	})

	t.Run("ungoverned text is never recovered at flip-on", func(t *testing.T) {
		store, path := captureFixture(t)
		pinTranscriptWatermark(store, path, false) // capture OFF at this prompt
		appendTranscript(t, path, assistantLine("forbidden"))
		// Capture flips on before the next prompt:
		if late := pinTranscriptWatermark(store, path, true); late != "" {
			t.Errorf("privacy boundary crossed: recovered %q", late)
		}
	})

	t.Run("no recovery when capture is currently off", func(t *testing.T) {
		store, path := captureFixture(t)
		pinTranscriptWatermark(store, path, true)
		appendTranscript(t, path, assistantLine("was governed"))
		// Stop missed AND capture flipped off before the next prompt:
		if late := pinTranscriptWatermark(store, path, false); late != "" {
			t.Errorf("captured %q while the directive says off", late)
		}
	})
}

// TestLegacyWatermarkMigration pins the 4→16-byte key migration: a
// watermark written by an older binary under the short key is
// adopted, not ignored, so the one turn that straddles the upgrade reads
// its delta from the old position instead of slurping from byte 0, and the
// legacy file is removed once the new key is written.
func TestLegacyWatermarkMigration(t *testing.T) {
	store, path := captureFixture(t)
	appendTranscript(t, path, assistantLine("pre-upgrade history"))
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// The old binary left its watermark under the short key, plain format.
	legacy := legacyWatermarkPath(store, path)
	if err := os.WriteFile(legacy, []byte(strconv.FormatInt(fi.Size(), 10)), 0o600); err != nil {
		t.Fatal(err)
	}
	appendTranscript(t, path, assistantLine("post-upgrade reply"))

	// First read under the new binary: Stop fires before any prompt.submit.
	got, err := transcriptDelta(store, path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "pre-upgrade history") {
		t.Errorf("one-shot slurp: legacy watermark ignored, delta = %.120q", got)
	}
	if got != "post-upgrade reply" {
		t.Errorf("delta = %q, want %q", got, "post-upgrade reply")
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("legacy watermark file survived the migration")
	}
	if _, err := os.Stat(watermarkPath(store, path)); err != nil {
		t.Error("migrated watermark missing under the new key")
	}
}

// TestTranscriptDeltaCodexRollout pins the codex rollout-*.jsonl shape
// (verified live; vendor calls the format unstable, so the reader is a
// tolerant probe: non-assistant and unknown lines are simply not replies).
// TestAssistantTextSkipsErrorBanners pins an over-capture guard: harness
// error banners ("API Error…", rate-limit notices) are written into the
// transcript as assistant-type records with isApiErrorMessage=true: the
// HARNESS talking, not the model. Capturing them as replies pollutes the
// conversation record.
func TestAssistantTextSkipsErrorBanners(t *testing.T) {
	banner := `{"type":"assistant","isApiErrorMessage":true,"message":{"content":[{"type":"text","text":"API Error: rate limited"}]}}`
	if got := assistantText([]byte(banner)); got != nil {
		t.Errorf("error banner captured as a reply: %v", got)
	}
	real := `{"type":"assistant","message":{"content":[{"type":"text","text":"actual reply"}]}}`
	if got := assistantText([]byte(real)); len(got) != 1 || got[0] != "actual reply" {
		t.Errorf("real assistant line lost: %v", got)
	}
}

func TestTranscriptDeltaCodexRollout(t *testing.T) {
	dir := t.TempDir()
	store := &Store{root: dir}
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-07-16.jsonl")
	lines := []string{
		`{"timestamp":"2026-07-16T10:00:00Z","type":"session_meta","payload":{"cli_version":"0.130.0"}}`,
		`{"timestamp":"2026-07-16T10:00:01Z","type":"event_msg","payload":{"type":"task_started"}}`,
		`{"timestamp":"2026-07-16T10:00:02Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"system stuff"}]}}`,
		`{"timestamp":"2026-07-16T10:00:03Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Deploying now."}]}}`,
		`{"timestamp":"2026-07-16T10:00:04Z","type":"response_item","payload":{"type":"function_call","name":"shell"}}`,
		`{"timestamp":"2026-07-16T10:00:05Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."},{"type":"output_text","text":"All green."}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	delta, err := transcriptDelta(store, path)
	if err != nil {
		t.Fatal(err)
	}
	if delta != "Deploying now.\nDone.\nAll green." {
		t.Errorf("codex delta = %q", delta)
	}
}

// TestCaptureFlowPolicyGated proves the hook-flow gating: a capture-enabled
// snapshot spools prompt/reply CEs; a plain snapshot spools nothing.
func TestCaptureFlowPolicyGated(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	captureOn, onID := testSignedPolicy(t, priv, `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: cap-on}
spec:
  match: {roles: [dev]}
  capture: {conversations: true, mode: redact}
  rules: [{id: r1, effect: deny, tools: [shell.exec], command: {denyPatterns: ["rm -rf *"]}}]
`)
	plain, plainID := testSignedPolicy(t, priv, `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: plain}
spec:
  rules: [{id: r1, effect: deny, tools: [shell.exec], command: {denyPatterns: ["rm -rf *"]}}]
`)

	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(transcript,
		[]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"the key is AKIAIOSFODNN7EXAMPLE"}]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	setup := func(t *testing.T, signed []byte, id string) (*Store, liveDecider) {
		t.Setenv("STRAZA_HOME", t.TempDir())
		store, err := OpenStore()
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveConfig(Config{ServerURL: "http://127.0.0.1:0", SnapshotKeys: keys}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveSnapshot(signed); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveSession(Session{
			SessionID: "s-cap", SessionToken: "tok", SnapshotID: id,
			User: "bob", Roles: []string{"dev"}, Harness: "claude-code/2.1",
			IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		return store, liveDecider{store: store}
	}

	spooled := func(store *Store) []string {
		recs, err := spool.ReadRecords(store.SpoolPath())
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(recs))
		for i, r := range recs {
			out[i] = string(r)
		}
		return out
	}

	// Capture on: prompt.submit spools a redacted prompt CE.
	store, d := setup(t, captureOn, onID)
	prompt := Normalized{Event: policy.Event{Kind: policy.EventPromptSubmit},
		HarnessName: "claude-code", HarnessVersion: "2.1",
		Prompt: "use AKIAIOSFODNN7EXAMPLE for the deploy"}
	if dec := d.Decide(prompt); dec.Effect != policy.EffectAllow {
		t.Fatalf("prompt.submit decision = %+v", dec)
	}
	recs := spooled(store)
	if len(recs) != 1 || !strings.Contains(recs[0], "straza.audit.prompt") {
		t.Fatalf("prompt spool = %v", recs)
	}
	if strings.Contains(recs[0], "AKIAIOSFODNN7EXAMPLE") || !strings.Contains(recs[0], redactedMark) {
		t.Errorf("redact mode leaked the secret into the spool: %s", recs[0])
	}

	// Reply capture at session.end reads the transcript delta (redacted too).
	d.captureReply(mustSession(t, store), Normalized{
		Event:       policy.Event{Kind: policy.EventSessionEnd},
		HarnessName: "claude-code", HarnessVersion: "2.1", TranscriptPath: transcript})
	recs = spooled(store)
	if len(recs) != 2 || !strings.Contains(recs[1], "straza.audit.reply") {
		t.Fatalf("reply spool = %d records", len(recs))
	}
	if strings.Contains(recs[1], "AKIAIOSFODNN7EXAMPLE") {
		t.Error("reply capture leaked the secret in redact mode")
	}

	// Capture off: the same prompt spools nothing.
	store, d = setup(t, plain, plainID)
	if dec := d.Decide(prompt); dec.Effect != policy.EffectAllow {
		t.Fatalf("plain decision = %+v", dec)
	}
	if recs := spooled(store); len(recs) != 0 {
		t.Fatalf("capture-off spooled %d records: %v", len(recs), recs)
	}
}

func mustSession(t *testing.T, store *Store) Session {
	t.Helper()
	ses, err := store.LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	return ses
}

// TestNormalizeSubagentFields pins the delegate-attribution extraction: the
// child's transcript path comes ONLY from agent_transcript_path, never from
// transcript_path, which names the PARENT's file on every claude-code
// subagent payload and FLIPS to the child's at SubagentStart on codex (the
// polarity divergence pinned in spec/hook-profile). agent_id/agent_type ride
// along wherever the harness flattens them (delegated tool calls included).
func TestNormalizeSubagentFields(t *testing.T) {
	adapters, err := LoadAdapters()
	if err != nil {
		t.Fatal(err)
	}
	stop := map[string]any{
		"hook_event_name":        "SubagentStop",
		"session_id":             "s-1",
		"transcript_path":        "/parent.jsonl",
		"agent_transcript_path":  "/parent/subagents/agent-1.jsonl",
		"agent_id":               "a-1",
		"agent_type":             "researcher",
		"last_assistant_message": "done",
	}
	for _, h := range []string{"claude-code", "codex"} {
		n, err := Normalize(adapters[h], stop)
		if err != nil {
			t.Fatalf("%s: %v", h, err)
		}
		if n.Event.Kind != policy.EventSubagentStop {
			t.Errorf("%s kind = %v, want subagent.stop", h, n.Event.Kind)
		}
		if n.AgentTranscriptPath != "/parent/subagents/agent-1.jsonl" {
			t.Errorf("%s agent transcript = %q", h, n.AgentTranscriptPath)
		}
		if n.AgentType != "researcher" || n.AgentID != "a-1" || n.AgentReply != "done" {
			t.Errorf("%s attribution = type %q id %q reply %q", h, n.AgentType, n.AgentID, n.AgentReply)
		}
		if n.TranscriptPath != "/parent.jsonl" {
			t.Errorf("%s parent transcript = %q (the two lanes must stay separate)", h, n.TranscriptPath)
		}
	}

	// claude-code SubagentStart carries the PARENT's transcript_path and no
	// agent_transcript_path at all (verified live). The child path must stay
	// empty, not be guessed from transcript_path.
	start, err := Normalize(adapters["claude-code"], map[string]any{
		"hook_event_name": "SubagentStart", "session_id": "s-1",
		"transcript_path": "/parent.jsonl",
	})
	if err != nil {
		t.Fatal(err)
	}
	if start.AgentTranscriptPath != "" {
		t.Errorf("SubagentStart agent transcript = %q, want empty", start.AgentTranscriptPath)
	}

	// A delegated tool call carries agent_id/agent_type (claude-code verified
	// live; codex vendor-documented). Audit attribution rides them.
	pre, err := Normalize(adapters["claude-code"], map[string]any{
		"hook_event_name": "PreToolUse", "session_id": "s-1",
		"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"},
		"agent_id": "a-1", "agent_type": "researcher",
	})
	if err != nil {
		t.Fatal(err)
	}
	if pre.AgentType != "researcher" || pre.AgentID != "a-1" {
		t.Errorf("delegated tool.pre attribution = type %q id %q", pre.AgentType, pre.AgentID)
	}
}

// TestSubagentStopCapture pins the delegate capture lane: capture, tagged.
// A delegate's reply is read as the delta of
// ITS OWN transcript at subagent.stop and spooled tagged with agent_type;
// the parent's transcript and watermark are never touched. The payload-borne
// last_assistant_message is the fallback when the child transcript is
// unavailable (codex: agent_transcript_path is nullable). When capture is
// OFF, the stop still seals the child watermark so a later flip-ON can never
// reach backwards (the subagent analog of the prompt.submit pin).
func TestSubagentStopCapture(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	captureOn, onID := testSignedPolicy(t, priv, `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: cap-on}
spec:
  match: {roles: [dev]}
  capture: {conversations: true}
  rules: [{id: r1, effect: deny, tools: [shell.exec], command: {denyPatterns: ["rm -rf *"]}}]
`)
	plain, plainID := testSignedPolicy(t, priv, `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: plain}
spec:
  rules: [{id: r1, effect: deny, tools: [shell.exec], command: {denyPatterns: ["rm -rf *"]}}]
`)

	setup := func(t *testing.T, signed []byte, id string) (*Store, liveDecider) {
		t.Setenv("STRAZA_HOME", t.TempDir())
		store, err := OpenStore()
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveConfig(Config{ServerURL: "http://127.0.0.1:0", SnapshotKeys: keys}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveSnapshot(signed); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveSession(Session{
			SessionID: "s-cap", SessionToken: "tok", SnapshotID: id,
			User: "bob", Roles: []string{"dev"}, Harness: "claude-code/2.1",
			IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		return store, liveDecider{store: store}
	}
	spooled := func(t *testing.T, store *Store) []string {
		recs, err := spool.ReadRecords(store.SpoolPath())
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(recs))
		for i, r := range recs {
			out[i] = string(r)
		}
		return out
	}
	childFile := func(t *testing.T, text string) string {
		p := filepath.Join(t.TempDir(), "agent-1.jsonl")
		line := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
		if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	stopEvent := func(childPath, reply string) Normalized {
		return Normalized{
			Event:       policy.Event{Kind: policy.EventSubagentStop},
			HarnessName: "claude-code", HarnessVersion: "2.1",
			TranscriptPath:      "/nonexistent-parent.jsonl",
			AgentTranscriptPath: childPath,
			AgentID:             "a-1", AgentType: "researcher", AgentReply: reply,
		}
	}

	t.Run("child delta captured, tagged, parent untouched", func(t *testing.T) {
		store, d := setup(t, captureOn, onID)
		child := childFile(t, "delegate findings")
		if dec := d.Decide(stopEvent(child, "done")); dec.Effect != policy.EffectAllow {
			t.Fatalf("subagent.stop decision = %+v", dec)
		}
		recs := spooled(t, store)
		if len(recs) != 1 || !strings.Contains(recs[0], "straza.audit.reply") {
			t.Fatalf("spool = %v", recs)
		}
		if !strings.Contains(recs[0], "delegate findings") {
			t.Errorf("child transcript text missing from capture: %s", recs[0])
		}
		if !strings.Contains(recs[0], `"agentType":"researcher"`) || !strings.Contains(recs[0], `"agentId":"a-1"`) {
			t.Errorf("capture not tagged with delegate attribution: %s", recs[0])
		}
		// The delta lane wins over the payload lane: last_assistant_message
		// is only the FINAL message; the transcript holds the whole output.
		if strings.Contains(recs[0], `"content":"done"`) {
			t.Errorf("payload reply won over the transcript delta: %s", recs[0])
		}
		// The parent's watermark must not move: its delta belongs to the
		// session.end lane, and advancing it here would swallow the parent's
		// own reply.
		if _, err := os.Stat(watermarkPath(store, "/nonexistent-parent.jsonl")); !os.IsNotExist(err) {
			t.Error("subagent.stop touched the parent transcript watermark")
		}

		// A second stop on the same child captures only what was appended.
		f, err := os.OpenFile(child, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(`{"type":"assistant","message":{"content":[{"type":"text","text":"second round"}]}}` + "\n"); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		if dec := d.Decide(stopEvent(child, "")); dec.Effect != policy.EffectAllow {
			t.Fatal("second stop denied")
		}
		recs = spooled(t, store)
		if len(recs) != 2 {
			t.Fatalf("second stop: spool = %d records", len(recs))
		}
		if !strings.Contains(recs[1], "second round") || strings.Contains(recs[1], "delegate findings") {
			t.Errorf("second stop is not a pure delta: %s", recs[1])
		}
	})

	t.Run("payload reply is the fallback when the child transcript is unavailable", func(t *testing.T) {
		store, d := setup(t, captureOn, onID)
		if dec := d.Decide(stopEvent("", "handoff summary")); dec.Effect != policy.EffectAllow {
			t.Fatal("stop denied")
		}
		recs := spooled(t, store)
		if len(recs) != 1 || !strings.Contains(recs[0], "handoff summary") {
			t.Fatalf("payload fallback spool = %v", recs)
		}
	})

	t.Run("an unattributed subagent stop captures nothing", func(t *testing.T) {
		// The harness runs internal sidechains (claude-code's input
		// autosuggest generator) whose SubagentStop carries NO agent_id or
		// agent_type. Captured, their ghost suggestion text would land in
		// the transcript as reply rows. "Capture, tagged" means attribution
		// is the entry ticket: no identity, no capture.
		store, d := setup(t, captureOn, onID)
		child := childFile(t, "suggestion machinery output")
		anon := Normalized{
			Event:       policy.Event{Kind: policy.EventSubagentStop},
			HarnessName: "claude-code", HarnessVersion: "2.1",
			AgentTranscriptPath: child, AgentReply: "test another mcp tool",
		}
		if dec := d.Decide(anon); dec.Effect != policy.EffectAllow {
			t.Fatal("stop denied")
		}
		if recs := spooled(t, store); len(recs) != 0 {
			t.Fatalf("unattributed sidechain was captured: %v", recs)
		}
	})

	t.Run("an id-only subagent stop captures nothing", func(t *testing.T) {
		// Since claude-code 2.1.220 the autosuggest sidechain carries an
		// agent_id but still no agent_type, so an id alone does not prove a
		// user-visible delegate. The class tag is the entry ticket: no
		// agent_type, no capture.
		store, d := setup(t, captureOn, onID)
		child := childFile(t, "try get-tiny-image")
		idOnly := Normalized{
			Event:       policy.Event{Kind: policy.EventSubagentStop},
			HarnessName: "claude-code", HarnessVersion: "2.1",
			AgentTranscriptPath: child, AgentID: "a04d32a97a4df1e2e",
			AgentReply: "try get-tiny-image",
		}
		if dec := d.Decide(idOnly); dec.Effect != policy.EffectAllow {
			t.Fatal("stop denied")
		}
		if recs := spooled(t, store); len(recs) != 0 {
			t.Fatalf("id-only sidechain was captured: %v", recs)
		}
	})

	t.Run("nothing to capture spools nothing", func(t *testing.T) {
		store, d := setup(t, captureOn, onID)
		if dec := d.Decide(stopEvent("", "")); dec.Effect != policy.EffectAllow {
			t.Fatal("stop denied")
		}
		if recs := spooled(t, store); len(recs) != 0 {
			t.Fatalf("spool = %v", recs)
		}
	})

	t.Run("capture off seals the child watermark so flip-on never reaches back", func(t *testing.T) {
		store, d := setup(t, plain, plainID)
		child := childFile(t, "produced while capture was OFF")
		if dec := d.Decide(stopEvent(child, "off-reply")); dec.Effect != policy.EffectAllow {
			t.Fatal("stop denied")
		}
		if recs := spooled(t, store); len(recs) != 0 {
			t.Fatalf("capture-off spooled: %v", recs)
		}
		// Flip capture ON mid-session, delegate produces more, stops again:
		// only the post-flip text may be captured.
		if err := store.SaveSnapshot(captureOn); err != nil {
			t.Fatal(err)
		}
		ses := mustSession(t, store)
		ses.SnapshotID = onID
		if err := store.SaveSession(ses); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(child, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(`{"type":"assistant","message":{"content":[{"type":"text","text":"post-flip text"}]}}` + "\n"); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		if dec := d.Decide(stopEvent(child, "")); dec.Effect != policy.EffectAllow {
			t.Fatal("post-flip stop denied")
		}
		recs := spooled(t, store)
		if len(recs) != 1 {
			t.Fatalf("post-flip spool = %d records", len(recs))
		}
		if strings.Contains(recs[0], "produced while capture was OFF") {
			t.Errorf("capture reached back past the flip: %s", recs[0])
		}
		if !strings.Contains(recs[0], "post-flip text") {
			t.Errorf("post-flip text not captured: %s", recs[0])
		}
	})

	t.Run("delegated tool decision is spooled with attribution", func(t *testing.T) {
		store, d := setup(t, captureOn, onID)
		n := Normalized{
			Event:       policy.Event{Kind: policy.EventToolPre, Tool: policy.ToolShellExec, Command: "ls"},
			HarnessName: "claude-code", HarnessVersion: "2.1",
			AgentID: "a-1", AgentType: "researcher",
		}
		d.Decide(n)
		recs := spooled(t, store)
		if len(recs) != 1 || !strings.Contains(recs[0], "straza.audit.tool") {
			t.Fatalf("tool spool = %v", recs)
		}
		if !strings.Contains(recs[0], `"agentType":"researcher"`) || !strings.Contains(recs[0], `"agentId":"a-1"`) {
			t.Errorf("delegated tool audit lost attribution: %s", recs[0])
		}
	})
}

func TestNormalizeCaptureFields(t *testing.T) {
	adapters, err := LoadAdapters()
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"hook_event_name": "UserPromptSubmit",
		"session_id":      "s-1",
		"prompt":          "deploy staging",
		"transcript_path": "/tmp/t.jsonl",
	}
	n, err := Normalize(adapters["claude-code"], payload)
	if err != nil {
		t.Fatal(err)
	}
	if n.Prompt != "deploy staging" || n.TranscriptPath != "/tmp/t.jsonl" {
		t.Errorf("capture fields: prompt=%q transcript=%q", n.Prompt, n.TranscriptPath)
	}

	// gemini: BeforeAgent carries the prompt; AfterAgent carries the reply IN
	// the payload (prompt_response), so the payload lane beats transcript
	// reading (both keys vendor-verified).
	gp, err := Normalize(adapters["gemini"], map[string]any{
		"event": "BeforeAgent", "sessionId": "s-2", "prompt": "list users",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gp.Prompt != "list users" {
		t.Errorf("gemini prompt = %q", gp.Prompt)
	}
	gr, err := Normalize(adapters["gemini"], map[string]any{
		"event": "AfterAgent", "sessionId": "s-2", "prompt": "list users",
		"prompt_response": "Here are the users: kim, bob.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gr.Reply != "Here are the users: kim, bob." {
		t.Errorf("gemini payload reply = %q", gr.Reply)
	}
	if gr.Event.Kind != policy.EventSessionEnd {
		t.Errorf("AfterAgent kind = %v, want session.end", gr.Event.Kind)
	}

	// python-sdk (straza-agentkit): canonical tool names pass through, the
	// command is extracted, and SessionEnd carries the payload-borne reply.
	// SDK agents have no transcript file (adapters/python-sdk.yaml).
	py, err := Normalize(adapters["python-sdk"], map[string]any{
		"hook_event_name": "PreToolUse", "session_id": "py-1",
		"tool_name": "shell.exec", "tool_input": map[string]any{"command": "rm -rf /tmp/x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if py.Event.Kind != policy.EventToolPre || py.Event.Tool != policy.ToolShellExec || py.Event.Command != "rm -rf /tmp/x" {
		t.Errorf("python-sdk tool.pre = %+v", py.Event)
	}
	pyEnd, err := Normalize(adapters["python-sdk"], map[string]any{
		"hook_event_name": "SessionEnd", "session_id": "py-1",
		"prompt_response": "kim and bob",
	})
	if err != nil {
		t.Fatal(err)
	}
	if pyEnd.Reply != "kim and bob" || pyEnd.Event.Kind != policy.EventSessionEnd {
		t.Errorf("python-sdk SessionEnd reply = %q kind = %v", pyEnd.Reply, pyEnd.Event.Kind)
	}
}
