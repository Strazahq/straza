package agentguard

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// TestTranscriptDeltaNeverCapturesToolInputs probes whether a drafting tool's
// input can reach conversation capture. The claude-jsonl rows follow the
// shape the harness writes on disk: an assistant record whose content list
// mixes text blocks with a tool_use block carrying the call's name and input,
// then a user record whose tool_result block carries the answer. The codex
// rows follow the rollout file: a function_call payload and its output beside
// the assistant messages. The reader keeps only text and output_text blocks,
// so the document and the token in it never reach captureContent, whichever
// mode is in force. The last row is the positive control: the same token in a
// text block is stored under verbatim and masked under redact, so a reader
// that started to keep tool inputs would fail this test.
func TestTranscriptDeltaNeverCapturesToolInputs(t *testing.T) {
	const token = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	document := "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: github\nspec:\n  auth:\n    token: " + token + "\n"
	submit := map[string]any{
		"type": "tool_use", "id": "toolu_01", "name": "mcp__straza__straza__draft_submit",
		"input": map[string]any{"documents": []string{document}, "note": "GitHub app for the release lane"},
	}
	for _, tc := range []struct {
		name  string
		lines []any
		want  string // the delta the reader must produce
		leak  bool   // the token sits in a text block: verbatim keeps it, redact masks it
	}{
		{
			name: "claude-jsonl draft_submit call beside the assistant's text",
			lines: []any{
				map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "text", "text": "Submitting the GitHub app as a draft."}, submit}}},
				map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": "toolu_01", "content": "draft d_01 stored: " + document}}}},
				map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "text", "text": "Draft d_01 is stored."}}}},
			},
			want: "Submitting the GitHub app as a draft.\nDraft d_01 is stored.",
		},
		{
			name: "claude-jsonl call with no text block at all",
			lines: []any{
				map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{submit}}},
			},
			want: "",
		},
		{
			name: "codex rollout function_call beside the assistant's messages",
			lines: []any{
				map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant", "content": []any{
					map[string]any{"type": "output_text", "text": "Submitting the GitHub app as a draft."}}}},
				map[string]any{"type": "response_item", "payload": map[string]any{"type": "function_call", "call_id": "call_01",
					"name": "mcp__straza__straza__draft_submit", "arguments": jsonText(t, map[string]any{"documents": []string{document}})}},
				map[string]any{"type": "response_item", "payload": map[string]any{"type": "function_call_output", "call_id": "call_01",
					"output": []any{map[string]any{"type": "input_text", "text": "draft d_01 stored: " + document}}}},
				map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant", "content": []any{
					map[string]any{"type": "output_text", "text": "Draft d_01 is stored."}}}},
			},
			want: "Submitting the GitHub app as a draft.\nDraft d_01 is stored.",
		},
		{
			name: "positive control: the token in a text block reaches capture",
			lines: []any{
				map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "text", "text": "the token is " + token}}}},
			},
			want: "the token is " + token,
			leak: true,
		},
	} {
		store, path := captureFixture(t)
		var b strings.Builder
		for _, rec := range tc.lines {
			b.WriteString(jsonText(t, rec))
			b.WriteString("\n")
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		delta, err := transcriptDelta(store, path)
		if err != nil {
			t.Fatalf("%s: the reader errored: %v", tc.name, err)
		}
		if delta != tc.want {
			t.Errorf("%s: delta = %q, want %q", tc.name, delta, tc.want)
		}
		for _, mode := range []string{policy.CaptureModeVerbatim, policy.CaptureModeRedact} {
			stored, _, _ := captureContent(delta, mode)
			for _, s := range []string{"kind: App", "documents", "draft_submit"} {
				if strings.Contains(stored, s) {
					t.Errorf("%s, mode %s: the stored turn carries %q from the tool input: %q", tc.name, mode, s, stored)
				}
			}
			switch {
			case tc.leak && mode == policy.CaptureModeVerbatim:
				if !strings.Contains(stored, token) {
					t.Errorf("%s, mode %s: the positive control lost the token: %q", tc.name, mode, stored)
				}
			case tc.leak:
				if strings.Contains(stored, token) || !strings.Contains(stored, redactedMark) {
					t.Errorf("%s, mode %s: the token survived redaction: %q", tc.name, mode, stored)
				}
			default:
				if strings.Contains(stored, token) {
					t.Errorf("%s, mode %s: the stored turn carries the token: %q", tc.name, mode, stored)
				}
			}
		}
	}
}

// jsonText marshals one transcript record, so every fixture line is valid
// JSON by construction and a leak can never hide behind a parse failure.
func jsonText(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
