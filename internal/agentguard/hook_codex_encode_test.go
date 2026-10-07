package agentguard

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// This file pins the codex hook RESPONSE contract: codex strict-parses hook
// stdout per event against its own output schemas (serde deny_unknown_fields),
// and the {"hookSpecificOutput":...} echo is valid JSON of the WRONG shape, so
// Stop fails the hook with "hook returned invalid stop hook JSON output". The
// vendor contract is: on exit 0 a hook prints schema-valid JSON or NOTHING, so
// empty stdout is the one success ack valid for EVERY codex event.
//
// The default lane behaves the same: it validates stdout against a
// per-event hookSpecificOutput schema with no SessionEnd variant and rejects
// the echo there. Both dialects ack non-enforceable events with silence,
// each for a reason observed against the shipping binary rather than assumed
// from docs (see the sibling test below).

// codexTurnEvents is the codex non-enforceable roster with minimal payloads in
// codex's own field spellings (adapters/codex.yaml).
var codexTurnEvents = []struct{ name, payload string }{
	{"UserPromptSubmit", `{"hook_event_name":"UserPromptSubmit","session_id":"s","cwd":"/w","prompt":"hi"}`},
	{"PostToolUse", `{"hook_event_name":"PostToolUse","session_id":"s","cwd":"/w","tool_name":"Bash","tool_input":{"command":"ls"}}`},
	{"Stop", `{"hook_event_name":"Stop","session_id":"s","cwd":"/w"}`},
	{"SessionEnd", `{"hook_event_name":"SessionEnd","session_id":"s","cwd":"/w"}`},
	{"SubagentStart", `{"hook_event_name":"SubagentStart","session_id":"s","cwd":"/w","agent_id":"thr1","agent_type":"worker"}`},
	{"SubagentStop", `{"hook_event_name":"SubagentStop","session_id":"s","cwd":"/w","agent_id":"thr1","agent_type":"worker","last_assistant_message":"done"}`},
}

// TestCodexNonEnforceableSilentAck: every codex non-enforceable event succeeds
// with EMPTY stdout and exit 0, never the claude-shaped echo.
func TestCodexNonEnforceableSilentAck(t *testing.T) {
	for _, tc := range codexTurnEvents {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, code := runHookTest(t, denyShell{}, []string{"CODEX_HARNESS=1"}, tc.payload)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			if stdout != "" {
				t.Errorf("codex %s ack must be empty (codex strict-parses per-event stdout; silence is the only universally valid success), got %q", tc.name, stdout)
			}
		})
	}
}

// TestClaudeNonEnforceableSilentAck: non-enforceable events on this lane ack
// with EMPTY stdout too.
//
// The harness validates hook stdout against a per-event hookSpecificOutput
// schema whose union has no SessionEnd member, so an echo document is
// rejected on every governed session close, printing
// "Hook JSON output validation failed" on the harness's own stderr in front of
// the user. Silence is accepted on every event this lane can fire, so it is
// the ack that cannot drift with the vendor's schema union.
func TestClaudeNonEnforceableSilentAck(t *testing.T) {
	for _, tc := range codexTurnEvents {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, code := runHookTest(t, denyShell{}, []string{"CLAUDECODE=1"}, tc.payload)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			if stdout != "" {
				t.Errorf("claude-code %s ack must be empty (claude validates stdout per event; the echo document is rejected where its schema has no variant), got %q", tc.name, stdout)
			}
		})
	}
}

// TestCodexEnforceableAck pins the codex tool.pre encodings: ALLOW is SILENCE
// (exit 0, empty stdout). Codex rejects `permissionDecision:"allow"` without
// updatedInput as semantically unsupported, "PreToolUse hook returned
// unsupported permissionDecision:allow", matching output_parser.rs at
// rust-v0.146.0. DENY keeps stderr reason + exit 2, because codex reads only
// stderr on exit 2 (source-verified) and the document on stdout is ignored
// there.
func TestCodexEnforceableAck(t *testing.T) {
	t.Run("allow is silence", func(t *testing.T) {
		stdout, _, code := runHookTest(t, denyShell{}, []string{"CODEX_HARNESS=1"},
			`{"hook_event_name":"PreToolUse","session_id":"s","cwd":"/w","tool_name":"Bash","tool_input":{"command":"ls"}}`)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if stdout != "" {
			t.Errorf("codex PreToolUse allow = %q, want empty stdout (exit 0 + no output is codex's one universal success ack)", stdout)
		}
	})
	t.Run("deny", func(t *testing.T) {
		stdout, stderr, code := runHookTest(t, denyShell{}, []string{"CODEX_HARNESS=1"},
			`{"hook_event_name":"PreToolUse","session_id":"s","cwd":"/w","tool_name":"Bash","tool_input":{"command":"rm -rf /"}}`)
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
		var resp claudeResponse
		if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
			t.Fatalf("deny stdout not JSON: %v: %s", err, stdout)
		}
		if resp.HookSpecificOutput.PermissionDecision != "deny" ||
			!strings.Contains(resp.HookSpecificOutput.PermissionDecisionReason, "destructive") {
			t.Errorf("deny response = %+v", resp)
		}
		if !strings.Contains(stderr, "destructive") {
			t.Errorf("deny reason must reach stderr, got %q", stderr)
		}
	})
}

// TestCodexSessionStartEmptyContextSilent: the empty-context session-start ack
// routes through encodeAllow, so on codex it is silence too. The populated
// path, the governance banner, is pinned in encode_test.go.
func TestCodexSessionStartEmptyContextSilent(t *testing.T) {
	var out bytes.Buffer
	if err := encodeSessionStart(HookIO{Stdout: &out}, "codex", "SessionStart", "", ""); err != nil {
		t.Fatalf("encodeSessionStart: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("codex empty-context session-start ack must be empty, got %q", out.String())
	}
}
