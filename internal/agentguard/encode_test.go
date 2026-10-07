package agentguard

import (
	"bytes"
	"encoding/json"
	"testing"
)

// encodeSSResult decodes the claude-family hookSpecificOutput ack.
type encodeSSResult struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
	SystemMessage string `json:"systemMessage"`
}

// TestEncodeSessionStartEchoesNativeEvent pins that the session.start ack
// echoes the harness-native event name it was invoked with (n.NativeEvent),
// NOT a hardcoded "SessionStart" constant. It exercises a non-claude-code
// dialect (codex, which uses the claude-family default branch that echoes
// hookEventName) and passes a native name that DIFFERS from the literal
// constant, so a re-introduced hardcode fails this test. Both encoder paths
// (empty context → encodeAllow ack; populated context → additionalContext)
// are covered.
func TestEncodeSessionStartEchoesNativeEvent(t *testing.T) {
	// A hypothetical renamed session-start event: not a real dialect name,
	// but exactly the kind of vendor rename the encoder must survive.
	const renamed = "SessionBegin"

	t.Run("populated-context-path", func(t *testing.T) {
		var out bytes.Buffer
		if err := encodeSessionStart(HookIO{Stdout: &out}, "codex", renamed, "ctx", "userline"); err != nil {
			t.Fatalf("encodeSessionStart: %v", err)
		}
		var r encodeSSResult
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatalf("stdout not JSON: %v: %s", err, out.String())
		}
		if r.HookSpecificOutput.HookEventName != renamed {
			t.Errorf("hookEventName = %q, want %q (must echo n.NativeEvent, not a constant)",
				r.HookSpecificOutput.HookEventName, renamed)
		}
		if r.HookSpecificOutput.AdditionalContext != "ctx" {
			t.Errorf("additionalContext = %q, want %q", r.HookSpecificOutput.AdditionalContext, "ctx")
		}
	})

	// The empty-context ack is SILENCE on both dialects that route through
	// encodeAllow's default branch: codex, and claude-code because its schema
	// union has no SessionEnd variant and it rejects the echo there (verified
	// against the harness-matrix claude-output lane). A
	// session-start with nothing to inject therefore prints nothing at all.
	t.Run("empty-context-path", func(t *testing.T) {
		var out bytes.Buffer
		if err := encodeSessionStart(HookIO{Stdout: &out}, "claude-code", renamed, "", ""); err != nil {
			t.Fatalf("encodeSessionStart: %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("empty-context ack = %q, want silence", out.String())
		}
	})

	// The echo-not-hardcode proof lives on the surviving document path: an
	// ENFORCEABLE allow still carries hookSpecificOutput, and its event name
	// must be the one the hook was invoked for.
	t.Run("enforceable-allow-echoes", func(t *testing.T) {
		var out bytes.Buffer
		if err := encodeAllow(HookIO{Stdout: &out}, "claude-code", renamed, true); err != nil {
			t.Fatalf("encodeAllow: %v", err)
		}
		var r encodeSSResult
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatalf("stdout not JSON: %v: %s", err, out.String())
		}
		if r.HookSpecificOutput.HookEventName != renamed {
			t.Errorf("hookEventName = %q, want %q (the allow ack must echo the native event, not a constant)",
				r.HookSpecificOutput.HookEventName, renamed)
		}
	})
}

// TestEncodeSessionStartClaudeUnchanged pins the claude-code wire output: its
// native session-start event IS "SessionStart", so
// the ack still carries exactly that, with the human-facing systemMessage.
func TestEncodeSessionStartClaudeUnchanged(t *testing.T) {
	var out bytes.Buffer
	if err := encodeSessionStart(HookIO{Stdout: &out}, "claude-code", "SessionStart", "ctx", "userline"); err != nil {
		t.Fatalf("encodeSessionStart: %v", err)
	}
	var r encodeSSResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("stdout not JSON: %v: %s", err, out.String())
	}
	if r.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Errorf("hookEventName = %q, want SessionStart", r.HookSpecificOutput.HookEventName)
	}
	if r.HookSpecificOutput.AdditionalContext != "ctx" {
		t.Errorf("additionalContext = %q, want ctx", r.HookSpecificOutput.AdditionalContext)
	}
	if r.SystemMessage != "userline" {
		t.Errorf("systemMessage = %q, want userline", r.SystemMessage)
	}
}
