package approval

import (
	"strings"

	"github.com/strazahq/straza/internal/policy"
)

// Args-preview wire helpers. binding_scope and argv_hash_prefix are NOT
// stored (they are derived at serialization from the stored fingerprint and
// summary), and the honesty line is composed on the server from them. The
// five preview wire fields (args_preview, args_truncated, args_bytes,
// argv_hash_prefix, binding_scope) travel together or not at all: a surface
// renders the companions only when args_preview is present.

// Binding scope values for the honesty line, folded by fingerprint v2.
// Per-rule truth: a v2 key self-describes its coverage in its tag; v1 keys
// (drain) fall back to the pre-v2 summary derivation.
const (
	// BindingCall: the fingerprint binds the concrete call: canonicalized
	// mcp.call arguments, shell.exec Command/Argv, described actions.
	BindingCall = "call"
	// BindingToolIdentity: the fingerprint covers App+ToolName only: an
	// explicit `binding: tool` rule, an mcp.call whose args the server never
	// observed, or a legacy v1 mcp key.
	BindingToolIdentity = "tool_identity"
)

// BindingScope reports how tightly the approval fingerprint binds the request,
// derived from the Event contents: an mcp.call with no observed args and no
// Command/Argv binds only the tool identity; everything else binds the
// concrete call. (Key-time scope additionally honors the rule's binding knob:
// EventKeyV2 owns that; this Event-only view backs the v1 summary fallback.)
func BindingScope(ev policy.Event) string {
	if ev.Tool == policy.ToolMCPCall && ev.Command == "" && len(ev.Argv) == 0 && ev.Args == nil {
		return BindingToolIdentity
	}
	return BindingCall
}

// BindingScopeForKey derives the wire binding_scope from a stored v2 key's
// tag. It returns "" for v1/unknown formats; callers fall back to
// BindingScopeForSummary for those rows.
func BindingScopeForKey(argvHash string) string {
	switch {
	case strings.HasPrefix(argvHash, keyPrefixV2Call):
		return BindingCall
	case strings.HasPrefix(argvHash, keyPrefixV2Tool):
		return BindingToolIdentity
	}
	return ""
}

// BindingScopeForRecord is the one derivation every surface uses: the key's
// own tag when it is a v2 key, else the legacy summary derivation (v1 rows,
// draining). Keeping it single-sourced is what stops the console, Slack, and
// the approver API from ever disagreeing about what a grant covers.
func BindingScopeForRecord(argvHash, summary string) string {
	if s := BindingScopeForKey(argvHash); s != "" {
		return s
	}
	return BindingScopeForSummary(summary)
}

// BindingScopeForSummary derives the wire binding_scope from a stored summary
// label. The Event is not persisted, but the summary faithfully encodes the tool
// identity (parseSummary): an "mcp.call " summary came from an args-free mcp
// Event, which is all BindingScope needs. v1 rows only; v2 rows use the key.
func BindingScopeForSummary(summary string) string {
	sv := parseSummary(summary)
	return BindingScope(policy.Event{Tool: sv.Tool, App: sv.App, ToolName: sv.ToolName})
}

// HashPrefix returns the first 12 hex chars of an argv-hash digest (no
// "sha256:" prefix; empty for an empty/short hash), from either key format:
// "sha256:<hex>" (v1) or "v2:<scope>:sha256:<hex>". It is the visible,
// non-binding tag rendered beside the honesty line.
func HashPrefix(argvHash string) string {
	h := argvHash
	if i := strings.LastIndex(argvHash, "sha256:"); i >= 0 {
		h = argvHash[i+len("sha256:"):]
	}
	if len(h) < 12 {
		return ""
	}
	return h[:12]
}

// HonestyLine composes the load-bearing "preview only" caption from a binding
// scope and hash prefix: it states plainly that the preview is not the binding
// artifact and whether the fingerprint binds the exact call or only the tool
// identity.
func HonestyLine(scope, prefix string) string {
	binds := "the exact call"
	if scope == BindingToolIdentity {
		binds = "tool identity"
	}
	return "preview only; this approval covers " + binds + " (sha256:" + prefix + ")"
}
