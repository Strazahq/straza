package server

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/strazahq/straza/internal/redact"
)

// The request-lane args preview. The concrete call arguments exist
// server-side for exactly one moment (the request path), so the preview
// is computed here, redacted + display-safed + bounded by
// internal/redact, and carried on the approval record. A single global
// admin knob (approval.preview.enabled) gates the whole thing: off means
// nothing is computed or stored, and every surface degrades to today's behavior
// via the omit-empty wire fields.

// argsPreview bundles the three preview fields a request-lane call computes.
type argsPreview struct {
	preview   string
	truncated bool
	bytes     int
}

// previewEnabled reports the global approval.preview knob.
func (a *App) previewEnabled() bool { return a.cfg.Approval.Preview.Enabled }

// mcpArgsPreview builds the preview for a PROXIED mcp.call from its actual
// (post-stripJustification) arguments. Absent/empty/null arguments render "{}":
// the call genuinely has no arguments, and an empty preview must always mean
// "no preview available", never "no arguments". Gated by the preview knob.
func (a *App) mcpArgsPreview(callArgs json.RawMessage) argsPreview {
	if !a.previewEnabled() {
		return argsPreview{}
	}
	p, tr, n := redact.Preview(renderMCPArgs(callArgs), redact.PreviewMaxBytes)
	return argsPreview{p, tr, n}
}

// commandArgsPreview builds the preview for a shell / described-command call
// from the verbatim command text. An empty command yields no preview (nothing
// concrete to show, e.g. a described mcp action whose args were never given).
// Gated by the preview knob.
func (a *App) commandArgsPreview(cmd string) argsPreview {
	if !a.previewEnabled() || cmd == "" {
		return argsPreview{}
	}
	p, tr, n := redact.Preview(cmd, redact.PreviewMaxBytes)
	return argsPreview{p, tr, n}
}

// renderMCPArgs pretty-prints tools/call arguments as 2-space-indented JSON for
// the preview; absent/empty/null arguments render "{}". Go's JSON encoder
// already \u-escapes C0 inside strings; redact.Neutralize (inside Preview)
// catches the bidi/zero-width it passes through raw. HTML escaping is OFF:
// the preview is a stored plain-text artifact read on non-HTML surfaces
// (mobile, Slack, CLI), and the encoder default turns printable < > & into
// literal backslash-u003c text on approver phones.
// HTML safety belongs to the console's render layer, not the stored bytes.
func renderMCPArgs(args json.RawMessage) string {
	if len(bytes.TrimSpace(args)) == 0 {
		return "{}"
	}
	var v any
	if err := json.Unmarshal(args, &v); err != nil {
		return string(args) // not JSON; show raw, redact/neutralize still scrub it
	}
	if v == nil {
		return "{}"
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return string(args)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// shellSummaryMax bounds the one-line shell.exec approval summary.
const shellSummaryMax = 160

var shellSummaryWS = regexp.MustCompile(`\s+`)

// clampShellSummary makes a shell command safe and compact for the one-line
// approval summary. Redact secrets, collapse whitespace to single
// spaces, neutralize any remaining display-hostile code points, and cap at 160
// runes with a trailing ellipsis when cut.
func clampShellSummary(cmd string) string {
	s := redact.Redact(cmd)
	s = strings.TrimSpace(shellSummaryWS.ReplaceAllString(s, " "))
	s = redact.Neutralize(s)
	if runes := []rune(s); len(runes) > shellSummaryMax {
		s = string(runes[:shellSummaryMax-1]) + "…"
	}
	return s
}
