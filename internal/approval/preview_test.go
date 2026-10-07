package approval

import (
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// TestBindingScope pins the derivation from Event contents: only an args-free
// mcp.call binds tool identity; shell.exec and described commands bind the call.
func TestBindingScope(t *testing.T) {
	cases := []struct {
		name string
		ev   policy.Event
		want string
	}{
		{"proxied mcp (args-free)", policy.Event{Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user"}, BindingToolIdentity},
		{"shell command", policy.Event{Tool: policy.ToolShellExec, Command: "rm -rf /tmp/x"}, BindingCall},
		{"described command via argv", policy.Event{Tool: policy.ToolShellExec, Argv: []string{"rm", "-rf"}}, BindingCall},
		{"file write", policy.Event{Tool: policy.ToolFileWrite, Paths: []string{"/etc/x"}}, BindingCall},
	}
	for _, tc := range cases {
		if got := BindingScope(tc.ev); got != tc.want {
			t.Errorf("%s: BindingScope = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestBindingScopeForSummary pins the serialization-time derivation from the
// stored summary label (the Event is not persisted).
func TestBindingScopeForSummary(t *testing.T) {
	if got := BindingScopeForSummary("mcp.call midpoint:disable_user"); got != BindingToolIdentity {
		t.Errorf("mcp summary => %q, want tool_identity", got)
	}
	if got := BindingScopeForSummary("shell.exec: rm -rf /tmp/x"); got != BindingCall {
		t.Errorf("shell summary => %q, want call", got)
	}
}

// TestHashPrefix: first 12 hex, no sha256: prefix; empty for short/empty.
func TestHashPrefix(t *testing.T) {
	if got := HashPrefix("sha256:0123456789abcdef0123456789abcdef"); got != "0123456789ab" {
		t.Errorf("HashPrefix = %q, want 0123456789ab", got)
	}
	if got := HashPrefix(""); got != "" {
		t.Errorf("HashPrefix(empty) = %q, want empty", got)
	}
	if got := HashPrefix("sha256:"); got != "" {
		t.Errorf("HashPrefix(no hex) = %q, want empty", got)
	}
}

// TestHonestyLine: the two pinned strings, differing only by the binds clause.
func TestHonestyLine(t *testing.T) {
	if got := HonestyLine(BindingCall, "abc123abc123"); got != "preview only; this approval covers the exact call (sha256:abc123abc123)" {
		t.Errorf("call honesty line = %q", got)
	}
	if got := HonestyLine(BindingToolIdentity, "abc123abc123"); got != "preview only; this approval covers tool identity (sha256:abc123abc123)" {
		t.Errorf("tool_identity honesty line = %q", got)
	}
}
