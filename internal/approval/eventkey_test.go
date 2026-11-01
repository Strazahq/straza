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

// TestEventKeyGolden pins today's key bytes: a key without an object, from
// EventKeyV2 or from EventKeyObject with a zero Object, never changes, so every
// stored grant keeps matching after the object joins the preimage.
func TestEventKeyGolden(t *testing.T) {
	pr := policy.Event{Tool: policy.ToolMCPCall, App: "ghapp", ToolName: "pr_create", Args: []byte(`{"repo":"Strazahq/pay-api","title":"x"}`)}
	tests := []struct {
		name    string
		ev      policy.Event
		binding string
		want    string
	}{
		{"mcp call scope", pr, policy.ApproveBindingCall, "v2:call:sha256:7d41176bdd23be741183dd8f0e898f513a07071709d28dde9e0d38b60250c731"},
		{"mcp tool scope", pr, policy.ApproveBindingTool, "v2:tool:sha256:07ba99c0dd68fba3e2f692252777c5c99f9635077efdbd9d2d758a1aefe1c100"},
		{"mcp unobserved args", policy.Event{Tool: policy.ToolMCPCall, App: "ghapp", ToolName: "pr_create"}, policy.ApproveBindingCall, "v2:tool:sha256:07ba99c0dd68fba3e2f692252777c5c99f9635077efdbd9d2d758a1aefe1c100"},
		{"shell", policy.Event{Tool: policy.ToolShellExec, Command: "deploy prod", Argv: []string{"deploy", "prod"}}, policy.ApproveBindingCall, "v2:call:sha256:76b1e41b4388e821ea7772227199f0f723d294997d2a7897d29eda038c6d7caf"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustKeyV2(t, tc.ev, tc.binding); got != tc.want {
				t.Errorf("EventKeyV2 = %q, want %q", got, tc.want)
			}
			got, err := EventKeyObject(tc.ev, tc.binding, Object{})
			if err != nil {
				t.Fatalf("EventKeyObject: %v", err)
			}
			if got != tc.want {
				t.Errorf("EventKeyObject zero = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEventKeyObject pins the object half of the key: in both scopes the
// object's value splits the key from the object-less one and from another
// value or type, the same object keys alike however the arguments spell
// the rest, and a zero Object gives exactly EventKeyV2's key.
func TestEventKeyObject(t *testing.T) {
	ev := policy.Event{Tool: policy.ToolMCPCall, App: "ghapp", ToolName: "pr_create", Args: []byte(`{"repo":"Strazahq/pay-api"}`)}
	for _, binding := range []string{policy.ApproveBindingCall, policy.ApproveBindingTool} {
		key := func(ev policy.Event, o Object) string {
			t.Helper()
			k, err := EventKeyObject(ev, binding, o)
			if err != nil {
				t.Fatalf("EventKeyObject(%+v): %v", o, err)
			}
			return k
		}
		bare := key(ev, Object{})
		payAPI := key(ev, Object{Type: "repo", Value: "Strazahq/pay-api"})
		reordered := policy.Event{Tool: policy.ToolMCPCall, App: "ghapp", ToolName: "pr_create", Args: []byte("{ \"repo\" : \"Strazahq/pay-api\" }")}
		tests := []struct {
			name string
			ev   policy.Event
			obj  Object
			same string
			diff []string
		}{
			{"the same repo keys alike", reordered, Object{Type: "repo", Value: "Strazahq/pay-api"}, payAPI, []string{bare}},
			{"another repo splits", ev, Object{Type: "repo", Value: "Strazahq/site"}, "", []string{bare, payAPI}},
			{"another type splits", ev, Object{Type: "project", Value: "Strazahq/pay-api"}, "", []string{bare, payAPI}},
		}
		for _, tc := range tests {
			t.Run(binding+"/"+tc.name, func(t *testing.T) {
				got := key(tc.ev, tc.obj)
				if tc.same != "" && got != tc.same {
					t.Errorf("key = %q, want %q", got, tc.same)
				}
				for _, d := range tc.diff {
					if got == d {
						t.Errorf("key %q equals a key it must differ from", got)
					}
				}
				if !strings.HasPrefix(got, "v2:"+binding+":sha256:") {
					t.Errorf("key %q lost its scope tag", got)
				}
			})
		}
		if v2 := mustKeyV2(t, ev, binding); bare != v2 {
			t.Errorf("%s: zero Object key %q, want EventKeyV2's %q", binding, bare, v2)
		}
	}
	if _, err := EventKeyObject(policy.Event{Tool: policy.ToolMCPCall, App: "x", ToolName: "y", Args: []byte(`{"a":1,"a":2}`)}, policy.ApproveBindingCall, Object{Type: "repo", Value: "a"}); err == nil {
		t.Error("ambiguous args with an object: want error")
	}
}
