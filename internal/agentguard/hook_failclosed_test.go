package agentguard

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The fail-closed error paths (OpenStore/LoadAdapters/ReadAll/ParsePayload/
// DetectHarness/no-adapter/Normalize + session.start checkin failure) must
// block in the harness's OWN dialect. Gemini blocks via a strict-JSON
// {"decision":"deny","reason":…} document on STDOUT and IGNORES the process
// exit code (adapters/gemini.yaml, pinned to gemini-cli v0.50.0 hooks/types.ts;
// the same contract encodeDeny already uses for a normal gemini deny, and the
// conformance judge enforces for gemini). A fail path that emits only exit 2
// therefore fails OPEN on gemini. These tests pin the dialect-correct block.

// denyCode extracts the process exit code carried by a fail-closed deny error.
func denyCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	if c, ok := err.(interface{ Code() int }); ok {
		return c.Code()
	}
	t.Fatalf("unexpected non-deny error: %v", err)
	return 0
}

// assertGeminiDeny asserts a dialect-correct gemini fail-closed block: a valid
// JSON object on stdout with decision=deny and a non-empty reason, plus the
// defense-in-depth stderr line and non-zero exit (harmless to gemini).
func assertGeminiDeny(t *testing.T, stdout string, code int, stderr string) {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &resp); err != nil {
		t.Fatalf("gemini fail path must write a strict-JSON block on stdout, got %q: %v", stdout, err)
	}
	if resp["decision"] != "deny" {
		t.Errorf("gemini fail decision = %v, want deny (stdout %q)", resp["decision"], stdout)
	}
	reason, _ := resp["reason"].(string)
	if strings.TrimSpace(reason) == "" {
		t.Errorf("gemini fail must carry a non-empty reason (the model cannot adapt without one), got %q", stdout)
	}
	if code != 2 {
		t.Errorf("gemini fail should also exit 2 as defense-in-depth, got %d", code)
	}
	if !strings.Contains(stderr, reason) {
		t.Errorf("gemini fail reason should also reach stderr, got stderr=%q reason=%q", stderr, reason)
	}
}

// TestFailClosedGeminiUnparseablePayload: a gemini session (env-detected) whose
// payload cannot be parsed must still emit the gemini stdout block. Stderr and
// exit 2 alone leave stdout empty, which fails open on gemini.
func TestFailClosedGeminiUnparseablePayload(t *testing.T) {
	stdout, stderr, code := runHookTest(t, allowAll{},
		[]string{"GEMINI_CLI=1"},
		`{ this is not valid json`)
	assertGeminiDeny(t, stdout, code, stderr)
}

// TestFailClosedGeminiNormalizeError: Normalize's only error mode is an event
// name absent from the dialect map; a gemini call that fails to normalize must
// still emit the gemini block.
func TestFailClosedGeminiNormalizeError(t *testing.T) {
	stdout, stderr, code := runHookTest(t, allowAll{},
		[]string{"GEMINI_CLI=1"},
		`{"hook_event_name":"NotARealGeminiEvent","tool_name":"run_shell_command","tool_input":{"command":"rm -rf /"}}`)
	assertGeminiDeny(t, stdout, code, stderr)
}

// TestFailClosedGeminiHarnessOverride: the explicit --harness override must make
// even a pre-parse failure dialect-aware, before any payload detection runs.
// allowAll is irrelevant here; this never decides.
func TestFailClosedGeminiHarnessOverride(t *testing.T) {
	var out, errb bytes.Buffer
	err := runHookWith(HookIO{
		Harness: "gemini",
		Stdin:   strings.NewReader(`not json at all`),
		Stdout:  &out,
		Stderr:  &errb,
		Environ: func() []string { return nil },
	}, allowAll{})
	assertGeminiDeny(t, out.String(), denyCode(t, err), errb.String())
}

// TestFailClosedGeminiSessionStartCheckinFails: a gemini session.start whose
// checkin fails must emit the gemini block, not just exit 2. An empty store has
// no config/identity, so SessionStart fails deterministically with no network.
func TestFailClosedGeminiSessionStartCheckinFails(t *testing.T) {
	store := &Store{root: t.TempDir()}
	var out, errb bytes.Buffer
	err := runHook(HookIO{
		Stdin:   strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"s","cwd":"/w","timestamp":"2026-07-16T12:00:00.000Z","source":"startup"}`),
		Stdout:  &out,
		Stderr:  &errb,
		Environ: func() []string { return []string{"GEMINI_CLI=1"} },
	}, store, allowAll{})
	assertGeminiDeny(t, out.String(), denyCode(t, err), errb.String())
}

// TestFailClosedClaudeUnchanged pins the claude-code fail path byte for byte:
// reason on stderr, exit 2, and NO decision document on stdout (exit 2 alone
// is their contract, and hookSpecificOutput needs the native event name an
// infra error may lack).
func TestFailClosedClaudeUnchanged(t *testing.T) {
	stdout, stderr, code := runHookTest(t, allowAll{},
		[]string{"CLAUDECODE=1"},
		`{ not valid json`)
	if code != 2 {
		t.Fatalf("claude infra error must exit 2, got %d", code)
	}
	if !strings.Contains(stderr, "Straza") {
		t.Errorf("claude fail must carry the reason on stderr, got %q", stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("claude fail path must not write stdout (exit 2 is the contract), got %q", stdout)
	}
}

// TestFailClosedCodexUnchanged: same regression guard for codex.
func TestFailClosedCodexUnchanged(t *testing.T) {
	stdout, stderr, code := runHookTest(t, allowAll{},
		[]string{"CODEX_HARNESS=1"},
		`{"hook_event_name":"NotAnEvent"}`)
	if code != 2 {
		t.Fatalf("codex infra error must exit 2, got %d", code)
	}
	if !strings.Contains(stderr, "Straza") {
		t.Errorf("codex fail must carry the reason on stderr, got %q", stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("codex fail path must not write stdout, got %q", stdout)
	}
}

// TestFailClosedUnknownHarnessUniversal: with no override, no env marker, and an
// unparseable payload the dialect cannot be determined, so the hook fails
// closed UNIVERSALLY: a gemini strict-JSON block on stdout (covers an
// undetected gemini) AND stderr + exit 2 (covers claude/codex, which block on
// exit 2 and ignore a top-level `decision` field).
func TestFailClosedUnknownHarnessUniversal(t *testing.T) {
	stdout, stderr, code := runHookTest(t, allowAll{}, nil, `not json`)
	if code != 2 {
		t.Fatalf("unknown-harness fail must exit 2 (blocks claude/codex), got %d", code)
	}
	if !strings.Contains(stderr, "Straza") {
		t.Errorf("stderr must carry the reason, got %q", stderr)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &resp); err != nil {
		t.Fatalf("unknown-harness fail must also write a gemini block on stdout, got %q: %v", stdout, err)
	}
	if resp["decision"] != "deny" {
		t.Errorf("universal fail decision = %v, want deny", resp["decision"])
	}
	if r, _ := resp["reason"].(string); strings.TrimSpace(r) == "" {
		t.Errorf("universal fail must carry a reason, got %q", stdout)
	}
}

// TestFailClosedGeminiValidEventStillBlocksOnDetect: a well-formed gemini
// tool.pre that a denying decider blocks goes through encodeDeny, NOT the
// fail path, so the normal gemini deny stays strict JSON with no exit code.
func TestFailClosedGeminiValidEventStillBlocksOnDetect(t *testing.T) {
	stdout, _, code := runHookTest(t, denyShell{},
		[]string{"GEMINI_CLI=1"},
		`{"hook_event_name":"BeforeTool","session_id":"s","cwd":"/w","timestamp":"t","tool_name":"run_shell_command","tool_input":{"command":"rm -rf /"}}`)
	if code != 0 {
		t.Errorf("normal gemini deny must NOT use exit code (encodeDeny path), got %d", code)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &resp); err != nil {
		t.Fatalf("normal gemini deny stdout must be strict JSON: %v: %q", err, stdout)
	}
	if resp["decision"] != "deny" {
		t.Errorf("normal gemini deny decision = %v", resp["decision"])
	}
}
