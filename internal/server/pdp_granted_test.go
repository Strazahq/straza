package server

import (
	"strings"
	"testing"
)

// TestDecideZeroesGranted pins the /v1/decide ingress rule of spec/policyset
// revision 17: a client that sends granted: true on an mcp.call still reads
// the ungranted default deny, because only the gateway's binding lookup may
// set the fact.
func TestDecideZeroesGranted(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	seedIdentity(t, app)
	tok, _ := checkinToken(t, app, base)

	code, res := decide(t, base, tok, map[string]any{
		"kind": "tool.pre", "tool": "mcp.call", "app": "demo-tools", "toolName": "echo", "granted": true,
	})
	if code != 200 || res["effect"] != "deny" {
		t.Fatalf("decide with a client-claimed grant = %d %v, want the default deny", code, res)
	}
	reason, _ := res["reason"].(string)
	if !strings.Contains(reason, "no role of yours has access to MCP tool demo-tools/echo") {
		t.Errorf("reason = %q, want the ungranted default sentence", reason)
	}
}
