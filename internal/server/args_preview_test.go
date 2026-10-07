package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
)

func previewApp(enabled bool) *App {
	return &App{cfg: config.Config{Approval: config.Approval{Preview: config.ApprovalPreview{Enabled: enabled}}}}
}

// TestMCPPreviewStripsJustification: the injected _straza_justification never
// appears in the preview (it is stripped before the preview is built, exactly as
// the gateway lane does), while the real arguments survive.
func TestMCPPreviewStripsJustification(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"user":"nova","_straza_justification":"offboard the leaver"}`)
	cleaned, just := stripJustification(raw)
	if just != "offboard the leaver" {
		t.Fatalf("stripJustification lost the reason: %q", just)
	}
	p := previewApp(true).mcpArgsPreview(cleaned)
	if strings.Contains(p.preview, "_straza_justification") || strings.Contains(p.preview, "offboard the leaver") {
		t.Errorf("justification leaked into preview: %q", p.preview)
	}
	if !strings.Contains(p.preview, "nova") {
		t.Errorf("real argument dropped from preview: %q", p.preview)
	}
}

// TestMCPPreviewNoHTMLEscapes: the preview is a stored plain-text artifact
// rendered by non-HTML surfaces (mobile Compose Text, Slack mrkdwn); Go's
// encoder default of \u-escaping < > & belongs to HTML embedding, not here,
// and would leak literal backslash-u text onto approver phones. The
// printable trio must survive verbatim; the
// neutralize pass's own escapes (C0, bidi, zero-width) are separate and stay.
func TestMCPPreviewNoHTMLEscapes(t *testing.T) {
	t.Parallel()
	p := previewApp(true).mcpArgsPreview(json.RawMessage(`{"body":"<p>a & b</p>"}`))
	if !strings.Contains(p.preview, `<p>a & b</p>`) {
		t.Errorf("printable < > & must survive verbatim, got %q", p.preview)
	}
	if strings.Contains(p.preview, `\u003c`) || strings.Contains(p.preview, `\u0026`) {
		t.Errorf("encoder HTML escapes leaked into stored preview: %q", p.preview)
	}
}

// TestPreviewKnobOff: with the knob off, no preview is computed for either lane.
func TestPreviewKnobOff(t *testing.T) {
	t.Parallel()
	off := previewApp(false)
	if p := off.mcpArgsPreview(json.RawMessage(`{"user":"nova"}`)); p.preview != "" || p.bytes != 0 || p.truncated {
		t.Errorf("mcp preview computed with knob off: %+v", p)
	}
	if p := off.commandArgsPreview("rm -rf /tmp/x"); p.preview != "" || p.bytes != 0 {
		t.Errorf("command preview computed with knob off: %+v", p)
	}
}

// TestMCPPreviewEmptyArgsIsBraces: an mcp call with absent/empty/null arguments
// stores "{}" (never empty) so absence always means "no preview available",
// never "no arguments".
func TestMCPPreviewEmptyArgsIsBraces(t *testing.T) {
	t.Parallel()
	on := previewApp(true)
	for _, in := range []json.RawMessage{nil, json.RawMessage(""), json.RawMessage("{}"), json.RawMessage("null")} {
		if p := on.mcpArgsPreview(in); p.preview != "{}" {
			t.Errorf("mcpArgsPreview(%q) = %q, want {}", string(in), p.preview)
		}
	}
}

// TestCommandPreviewEmpty: a command lane with no command yields no preview
// (nothing concrete to show, e.g. a described mcp action).
func TestCommandPreviewEmpty(t *testing.T) {
	t.Parallel()
	if p := previewApp(true).commandArgsPreview(""); p.preview != "" {
		t.Errorf("empty command must yield no preview, got %q", p.preview)
	}
}

// TestClampShellSummary: the shell summary is redacted, single-lined, display
// safe, and capped at 160 runes with a trailing ellipsis.
func TestClampShellSummary(t *testing.T) {
	t.Parallel()
	// redaction + whitespace collapse
	got := clampShellSummary("deploy   --token wat_0123456789abcdefghijklmnopqrstuvwx\n--env prod")
	if strings.Contains(got, "wat_0123456789") {
		t.Errorf("secret survived summary: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("summary is not single-line: %q", got)
	}
	// cap at 160 runes
	long := clampShellSummary(strings.Repeat("x", 500))
	if r := []rune(long); len(r) > 160 {
		t.Errorf("summary %d runes, over 160", len(r))
	}
	if !strings.HasSuffix(long, "…") {
		t.Errorf("clamped summary must end with ellipsis: %q", long)
	}
}

// TestApproveSummaryShellClamped: approveSummary runs a shell command through the
// clamp (bounded, redacted) while leaving the mcp summary untouched.
func TestApproveSummaryShellClamped(t *testing.T) {
	t.Parallel()
	shell := approveSummary(policy.Event{Tool: policy.ToolShellExec, Command: "curl -H 'Authorization: Bearer " + strings.Repeat("t", 40) + "'"})
	if !strings.HasPrefix(shell, "shell.exec: ") || strings.Contains(shell, strings.Repeat("t", 40)) {
		t.Errorf("shell summary not clamped/redacted: %q", shell)
	}
	mcp := approveSummary(policy.Event{Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user"})
	if mcp != "mcp.call midpoint:disable_user" {
		t.Errorf("mcp summary changed: %q", mcp)
	}
}

// TestApprovalPayloadPreviewPairing: the admin payload emits argsPreview and its
// companions together, and the companions are ABSENT when there is no preview.
func TestApprovalPayloadPreviewPairing(t *testing.T) {
	t.Parallel()
	withPrev := toApprovalPayload(approval.Record{
		ID: "a1", State: approval.StatePending, Summary: "mcp.call midpoint:disable_user",
		ArgvHash: "sha256:0123456789abcdef", ArgsPreview: "{\n  \"user\": \"nova\"\n}", ArgsBytes: 17,
	})
	if withPrev.ArgsPreview == "" || withPrev.ArgvHashPrefix != "0123456789ab" || withPrev.BindingScope != approval.BindingToolIdentity {
		t.Errorf("preview companions not paired: %+v", withPrev)
	}
	b, _ := json.Marshal(withPrev)
	for _, k := range []string{"argsPreview", "argvHashPrefix", "bindingScope"} {
		if !strings.Contains(string(b), k) {
			t.Errorf("wire missing %q: %s", k, b)
		}
	}
	// No preview -> companions omitted entirely.
	noPrev := toApprovalPayload(approval.Record{ID: "a2", State: approval.StatePending, ArgvHash: "sha256:0123456789abcdef", Summary: "shell.exec: ls"})
	nb, _ := json.Marshal(noPrev)
	for _, k := range []string{"argsPreview", "argvHashPrefix", "bindingScope", "argsBytes"} {
		if strings.Contains(string(nb), k) {
			t.Errorf("empty-preview row leaked %q: %s", k, nb)
		}
	}
}

// TestApproverRowPreviewPairing: the mobile row uses the pinned snake_case names
// and pairs the companions with a present preview; an empty-preview row omits
// all five.
func TestApproverRowPreviewPairing(t *testing.T) {
	t.Parallel()
	pr := approval.PendingRow{
		ID: "a1", Summary: approval.SummaryView{Tool: "shell.exec"},
		ArgsPreview: "rm -rf /tmp/x", ArgsBytes: 13,
		ArgvHashPrefix: "0123456789ab", BindingScope: approval.BindingCall,
	}
	b, _ := json.Marshal(toApproverRow(pr, true))
	for _, k := range []string{`"args_preview"`, `"argv_hash_prefix"`, `"binding_scope"`, `"args_bytes"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("wire missing %s: %s", k, b)
		}
	}
	empty, _ := json.Marshal(toApproverRow(approval.PendingRow{ID: "a2"}, true))
	for _, k := range []string{"args_preview", "argv_hash_prefix", "binding_scope", "args_bytes"} {
		if strings.Contains(string(empty), k) {
			t.Errorf("empty-preview approver row leaked %q: %s", k, empty)
		}
	}
}
