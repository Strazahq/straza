package agentguard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/agentguard/trace"
)

// The hook lane's decision journal: every decision RunHook makes
// lands as one content-free "decision" record in state/trace.jsonl, the
// record carries the journal's schema v1 facts and nothing of the
// payload, and the journal never changes a byte of what the harness sees.

const (
	journalAllowPayload = `{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/work","tool_name":"Bash","tool_input":{"command":"git status"}}`
	journalDenyPayload  = `{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/work","tool_name":"Bash","tool_input":{"command":"rm -rf /tmp/CANARY-trace-7f3a9"}}`
	journalGeminiAllow  = `{"event":"BeforeTool","toolName":"run_shell_command","args":{"command":"git status"}}`
	journalGeminiDeny   = `{"event":"BeforeTool","toolName":"run_shell_command","args":{"command":"rm -rf /tmp/CANARY-trace-7f3a9"}}`
)

// journalHookStore seeds a fully enrolled-looking client under a fresh
// STRAZA_HOME (config with pinned keys, a verified snapshot of
// offlinePolicyDoc, a live session) so RunHook decides locally: `git status`
// reads as the default allow, `rm -rf` as the no-rm rule deny.
func journalHookStore(t *testing.T) (*Store, string) {
	t.Helper()
	priv, keys := testSnapshotKey(t)
	signed, id := testSignedPolicy(t, priv, offlinePolicyDoc)
	return reacquireStore(t, "http://127.0.0.1:9", keys, signed, id, time.Hour, false), id
}

// runJournalHook pipes one payload through RunHook under the current
// STRAZA_HOME and returns the streams and the error (a deny carries Code()).
func runJournalHook(t *testing.T, harness, payload string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errb bytes.Buffer
	err = RunHook(HookIO{
		Harness: harness, Stdin: strings.NewReader(payload),
		Stdout: &out, Stderr: &errb, Environ: func() []string { return nil },
	})
	return out.String(), errb.String(), err
}

func journalRecords(t *testing.T, store *Store) []trace.Record {
	t.Helper()
	recs, unreadable := trace.Tail(store.stateDir(), 0)
	if unreadable != 0 {
		t.Fatalf("%d unreadable trace line(s)", unreadable)
	}
	return recs
}

func recordsNamed(recs []trace.Record, msg string) []trace.Record {
	var out []trace.Record
	for _, r := range recs {
		if r.Msg == msg {
			out = append(out, r)
		}
	}
	return out
}

// attrNumber normalizes the reader's json.Number values to float64 so tests
// can state numbers plainly.
func attrNumber(v any) any {
	if n, ok := v.(json.Number); ok {
		if f, err := n.Float64(); err == nil {
			return f
		}
	}
	return v
}

// assertAttrs checks every wanted key/value and that none of the absent keys
// is carried (omitempty is part of the schema).
func assertAttrs(t *testing.T, label string, r trace.Record, want map[string]any, absent ...string) {
	t.Helper()
	for k, v := range want {
		if got := attrNumber(r.Attrs[k]); got != v {
			t.Errorf("%s: %s = %v (%T), want %v (%T)", label, k, got, got, v, v)
		}
	}
	for _, k := range absent {
		if _, ok := r.Attrs[k]; ok {
			t.Errorf("%s: carries %s = %v, want it omitted", label, k, r.Attrs[k])
		}
	}
}

// assertDuration checks the timing every journal-level record carries.
func assertDuration(t *testing.T, label string, r trace.Record) {
	t.Helper()
	if d, ok := attrNumber(r.Attrs["duration_ms"]).(float64); !ok || d < 0 {
		t.Errorf("%s: duration_ms = %v, want a non-negative number", label, r.Attrs["duration_ms"])
	}
}

// TestHookJournalDecisionRecord: an allow and a deny through the real hook
// path each leave exactly one "decision" record with the schema v1 fields,
// and the command never reaches the file (the privacy stance as a test).
func TestHookJournalDecisionRecord(t *testing.T) {
	store, snapID := journalHookStore(t)

	if _, _, err := runJournalHook(t, "claude-code", journalAllowPayload); err != nil {
		t.Fatalf("allow: %v", err)
	}
	if _, _, err := runJournalHook(t, "claude-code", journalDenyPayload); denyCode(t, err) != 2 {
		t.Fatalf("deny: want exit 2, got %v", err)
	}

	recs := recordsNamed(journalRecords(t, store), "decision")
	if len(recs) != 2 {
		t.Fatalf("decision records = %d, want 2: %+v", len(recs), recs)
	}
	assertAttrs(t, "allow", recs[0], map[string]any{
		"v": float64(1), "lane": "hook", "harness": "claude-code", "event": "tool.pre",
		"tool": "shell.exec", "effect": "allow", "escalation": "none", "snapshot": snapID,
		"spool": "ok", "default": true,
	}, "rule", "status", "correlation", "fail_closed", "tool_name", "app")
	assertAttrs(t, "deny", recs[1], map[string]any{
		"v": float64(1), "lane": "hook", "harness": "claude-code", "event": "tool.pre",
		"tool": "shell.exec", "effect": "deny", "rule": "no-rm", "set": "offline-gate-test",
		"escalation": "none", "snapshot": snapID, "spool": "ok",
	}, "status", "correlation", "fail_closed", "default")
	for _, r := range recs {
		assertDuration(t, r.Msg, r)
		if r.Level != "INFO" {
			t.Errorf("journal record level = %q, want INFO", r.Level)
		}
	}

	raw, err := os.ReadFile(store.TracePath())
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"CANARY", "rm -rf", `"command"`, `"argv"`, `"paths"`, `"reason"`} {
		if bytes.Contains(raw, []byte(canary)) {
			t.Fatalf("payload content %q leaked into the journal:\n%s", canary, raw)
		}
	}
}

// TestHookJournalFailClosedNoSession: a box with no session fails closed,
// and the journal says so (fail_closed, no escalation, no server facts).
func TestHookJournalFailClosedNoSession(t *testing.T) {
	store := daemonStore(t)
	_, _, err := runJournalHook(t, "claude-code", journalAllowPayload)
	if denyCode(t, err) != 2 {
		t.Fatalf("want the fail-closed exit 2, got %v", err)
	}
	recs := recordsNamed(journalRecords(t, store), "decision")
	if len(recs) != 1 {
		t.Fatalf("decision records = %d, want 1", len(recs))
	}
	assertAttrs(t, "no-session", recs[0], map[string]any{
		"lane": "hook", "harness": "claude-code", "event": "tool.pre", "tool": "shell.exec",
		"effect": "deny", "escalation": "none", "fail_closed": true,
	}, "rule", "status", "correlation", "snapshot", "spool", "default")
	assertDuration(t, "no-session", recs[0])
}

// TestHookSessionStartJournalCarriesCheckinCorrelation: the session.start
// decision record names the check-in's HTTP status and the X-Request-Id the
// server answered with, the id that joins this laptop line to the strazad log.
func TestHookSessionStartJournalCarriesCheckinCorrelation(t *testing.T) {
	priv, keys := testSnapshotKey(t)
	signed, id := testSignedPolicy(t, priv, offlinePolicyDoc)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-Id", "corr-checkin-7")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_id": "s-new", "session_token": "tok-new", "expires_in": 300,
			"user": "kim", "roles": []string{"dev"}, "snapshot_id": id, "attestation": "advisory",
		})
	})
	mux.HandleFunc("GET /v1/snapshot", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Straza-Snapshot-Id", id)
		_, _ = w.Write(signed)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	store := daemonStore(t)
	if err := store.SaveConfig(Config{ServerURL: srv.URL, SnapshotKeys: keys}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveIdentity(Identity{DeviceID: "d1", Username: "kim", DeviceToken: "dev-tok"}); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runJournalHook(t, "claude-code",
		`{"hook_event_name":"SessionStart","session_id":"s1","cwd":"/work"}`)
	if err != nil {
		t.Fatalf("session.start: %v (stdout %q)", err, stdout)
	}
	recs := recordsNamed(journalRecords(t, store), "decision")
	if len(recs) != 1 {
		t.Fatalf("decision records = %d, want 1", len(recs))
	}
	assertAttrs(t, "session.start", recs[0], map[string]any{
		"lane": "hook", "harness": "claude-code", "event": "session.start", "effect": "allow",
		"status": float64(200), "correlation": "corr-checkin-7", "snapshot": id, "escalation": "none",
	}, "fail_closed", "spool", "rule")
	assertDuration(t, "session.start", recs[0])
}

// TestHookStdoutPureWithTraceDebug: with the debug window open, every dialect
// answers byte-for-byte what it answers with the journal switched off: same
// stdout, same stderr, same exit code. The trace goes to its file only.
func TestHookStdoutPureWithTraceDebug(t *testing.T) {
	store, _ := journalHookStore(t)
	cases := []struct{ harness, payload string }{
		{"claude-code", journalAllowPayload}, {"claude-code", journalDenyPayload},
		{"codex", journalAllowPayload}, {"codex", journalDenyPayload},
		{"gemini", journalGeminiAllow}, {"gemini", journalGeminiDeny},
	}
	type answer struct {
		stdout, stderr string
		code           int
	}
	run := func() []answer {
		var out []answer
		for _, c := range cases {
			stdout, stderr, err := runJournalHook(t, c.harness, c.payload)
			out = append(out, answer{stdout, stderr, denyCode(t, err)})
		}
		return out
	}

	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Trace = "off"
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	want := run()
	if recs := journalRecords(t, store); len(recs) != 0 {
		t.Fatalf("trace: off still wrote %d record(s)", len(recs))
	}

	cfg.Trace = ""
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := trace.WriteToggle(store.stateDir(), trace.Toggle{Level: "debug", Until: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	got := run()
	for i, c := range cases {
		if got[i] != want[i] {
			t.Errorf("%s %s: answer changed with the debug window open:\n got %+v\nwant %+v", c.harness, c.payload, got[i], want[i])
		}
	}
	recs := journalRecords(t, store)
	var info, debug int
	for _, r := range recs {
		switch r.Level {
		case "INFO":
			info++
		case "DEBUG":
			debug++
		}
	}
	if info != len(cases) || debug == 0 {
		t.Errorf("debug window wrote %d INFO + %d DEBUG records, want %d INFO and some DEBUG", info, debug, len(cases))
	}
}

// TestHookTraceBestEffortIsolation: an unwritable trace location (a directory
// squatting on the file, with the debug window open so every emit path runs)
// must not change the hook's observable behaviour at all.
func TestHookTraceBestEffortIsolation(t *testing.T) {
	store, _ := journalHookStore(t)
	if err := trace.WriteToggle(store.stateDir(), trace.Toggle{Level: "debug", Until: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	wantOut, wantErr, wantE := runJournalHook(t, "claude-code", journalDenyPayload)
	if recs := journalRecords(t, store); len(recs) == 0 {
		t.Fatal("healthy run wrote no trace records; the test would prove nothing")
	}

	if err := os.Remove(store.TracePath()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.TracePath(), 0o755); err != nil {
		t.Fatal(err)
	}
	gotOut, gotErr, gotE := runJournalHook(t, "claude-code", journalDenyPayload)
	if gotOut != wantOut || gotErr != wantErr || denyCode(t, gotE) != denyCode(t, wantE) {
		t.Errorf("answer changed when the trace file was unwritable:\n got %q %q %d\nwant %q %q %d",
			gotOut, gotErr, denyCode(t, gotE), wantOut, wantErr, denyCode(t, wantE))
	}
}
