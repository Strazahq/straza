package approval

import (
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// --- v2 ---

func mustKeyV2(t *testing.T, ev policy.Event, binding string) string {
	t.Helper()
	k, err := EventKeyV2(ev, binding)
	if err != nil {
		t.Fatalf("EventKeyV2: %v", err)
	}
	return k
}

// TestEventKeyV2Format pins the tagged key shape: the version and effective
// scope ride inside the stored string, so every grant records its version by
// construction and binding_scope derives from the key itself (no migration).
func TestEventKeyV2Format(t *testing.T) {
	mcp := policy.Event{Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user", Args: []byte(`{"user":"bob"}`)}
	k := mustKeyV2(t, mcp, policy.ApproveBindingCall)
	if !strings.HasPrefix(k, "v2:call:sha256:") || len(k) != len("v2:call:sha256:")+64 {
		t.Fatalf("call key format = %q", k)
	}
	kt := mustKeyV2(t, mcp, policy.ApproveBindingTool)
	if !strings.HasPrefix(kt, "v2:tool:sha256:") || len(kt) != len("v2:tool:sha256:")+64 {
		t.Fatalf("tool key format = %q", kt)
	}
	if k == kt {
		t.Fatal("call and tool scopes hashed identically")
	}
}

// TestEventKeyV2MCPBinding pins the mcp.call semantics: under binding call the
// canonicalized args fold into the key (wire noise collapses, real differences
// split); under binding tool (or when the args were never observed) the key
// covers tool identity only and says so in its tag.
func TestEventKeyV2MCPBinding(t *testing.T) {
	base := policy.Event{Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user", Args: []byte(`{"force":true,"user":"bob"}`)}
	baseKey := mustKeyV2(t, base, policy.ApproveBindingCall)

	tests := []struct {
		name    string
		ev      policy.Event
		binding string
		same    bool
	}{
		{"identical", policy.Event{Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user", Args: []byte(`{"force":true,"user":"bob"}`)}, policy.ApproveBindingCall, true},
		{"key order noise", policy.Event{Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user", Args: []byte(`{"user":"bob","force":true}`)}, policy.ApproveBindingCall, true},
		{"whitespace noise", policy.Event{Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user", Args: []byte("{ \"force\" : true ,\n\"user\" : \"bob\" }")}, policy.ApproveBindingCall, true},
		{"justification noise", policy.Event{Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user", Args: []byte(`{"_straza_justification":"cleanup","force":true,"user":"bob"}`)}, policy.ApproveBindingCall, true},
		{"different args", policy.Event{Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user", Args: []byte(`{"force":true,"user":"ceo"}`)}, policy.ApproveBindingCall, false},
		{"different tool", policy.Event{Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "enable_user", Args: []byte(`{"force":true,"user":"bob"}`)}, policy.ApproveBindingCall, false},
		{"tool binding ignores args", policy.Event{Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user", Args: []byte(`{"force":true,"user":"bob"}`)}, policy.ApproveBindingTool, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mustKeyV2(t, tc.ev, tc.binding)
			if (got == baseKey) != tc.same {
				t.Errorf("key same=%v (got %q vs base %q)", got == baseKey, got, baseKey)
			}
		})
	}

	// Tool scope collapses arg differences: any args, no args, one key.
	toolA := mustKeyV2(t, policy.Event{Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user", Args: []byte(`{"user":"bob"}`)}, policy.ApproveBindingTool)
	toolB := mustKeyV2(t, policy.Event{Tool: policy.ToolMCPCall, App: "midpoint", ToolName: "disable_user", Args: []byte(`{"user":"ceo"}`)}, policy.ApproveBindingTool)
	if toolA != toolB {
		t.Error("binding tool split on args")
	}
}

// TestEventKeyV2UnobservedArgsDegrade: an mcp.call whose args were never
// observed (nil: old client, argless dialect) cannot bind what the server
// never saw. Under binding call it honestly degrades to the SAME tool-scoped
// key an explicit binding:tool rule mints; a tool-scoped grant authorizes
// any-args by explicit human decision, so the alias is within what was
// approved. An OBSERVED empty object is the exact no-argument call and stays
// call-scoped.
func TestEventKeyV2UnobservedArgsDegrade(t *testing.T) {
	identity := policy.Event{Tool: policy.ToolMCPCall, App: "github", ToolName: "list_repos"}
	degraded := mustKeyV2(t, identity, policy.ApproveBindingCall) // Args nil
	explicit := mustKeyV2(t, identity, policy.ApproveBindingTool)
	if degraded != explicit {
		t.Errorf("degraded key %q != explicit tool key %q", degraded, explicit)
	}
	if !strings.HasPrefix(degraded, "v2:tool:") {
		t.Errorf("degraded key not tool-tagged: %q", degraded)
	}
	observed := policy.Event{Tool: policy.ToolMCPCall, App: "github", ToolName: "list_repos", Args: []byte(`{}`)}
	k := mustKeyV2(t, observed, policy.ApproveBindingCall)
	if !strings.HasPrefix(k, "v2:call:") {
		t.Errorf("observed-empty args degraded: %q", k)
	}
}

// TestEventKeyV2Shell: non-mcp lanes always bind the concrete call; the
// binding knob is consulted for mcp.call only.
func TestEventKeyV2Shell(t *testing.T) {
	sh := policy.Event{Tool: policy.ToolShellExec, Command: "deploy prod"}
	k := mustKeyV2(t, sh, policy.ApproveBindingTool)
	if !strings.HasPrefix(k, "v2:call:") {
		t.Errorf("shell key = %q, want call scope regardless of binding", k)
	}
	other := mustKeyV2(t, policy.Event{Tool: policy.ToolShellExec, Command: "deploy staging"}, policy.ApproveBindingCall)
	if k == other {
		t.Error("different commands hashed identically")
	}
}

// TestEventKeyV2MalformedArgs: ambiguous args (dup keys, bad JSON) error so
// the PEP denies fail-closed, never a part-read hash.
func TestEventKeyV2MalformedArgs(t *testing.T) {
	for _, raw := range []string{`{"a":1,"a":2}`, `{"a":`, `[1]`} {
		ev := policy.Event{Tool: policy.ToolMCPCall, App: "x", ToolName: "y", Args: []byte(raw)}
		if _, err := EventKeyV2(ev, policy.ApproveBindingCall); err == nil {
			t.Errorf("args %q: want error", raw)
		}
	}
}

func TestBindingScopeForKey(t *testing.T) {
	tests := []struct{ key, want string }{
		{"v2:call:sha256:" + strings.Repeat("a", 64), BindingCall},
		{"v2:tool:sha256:" + strings.Repeat("a", 64), BindingToolIdentity},
		{"sha256:" + strings.Repeat("a", 64), ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := BindingScopeForKey(tc.key); got != tc.want {
			t.Errorf("BindingScopeForKey(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// TestHashPrefixV2: the visible 12-hex tag parses from both key formats.
func TestHashPrefixV2(t *testing.T) {
	hexs := strings.Repeat("ab", 32)
	for _, key := range []string{"sha256:" + hexs, "v2:call:sha256:" + hexs, "v2:tool:sha256:" + hexs} {
		if got := HashPrefix(key); got != hexs[:12] {
			t.Errorf("HashPrefix(%q) = %q, want %q", key, got, hexs[:12])
		}
	}
	if got := HashPrefix("v2:call:sha256:short"); got != "" {
		t.Errorf("short digest prefix = %q, want empty", got)
	}
}
