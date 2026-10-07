package server

import (
	"fmt"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/config"
)

// The gateway speaks a policy deny to the model as a tool error whose text
// starts with "Straza: ": the session banner promises agents that prefix marks
// a policy decision to relay, never retry. Rule authors and the console's
// Block flow write the prefix themselves, and the engine synthesizes
// "Straza: blocked by policy rule <set>/<rule>" for a reason-less deny, so the
// gateway adds the prefix only when it is missing, so a reason-less MCP
// deny never reads "Straza: Straza: blocked ...".
func TestGatewayDenyReasonPrefixedOnce(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, reason, want string
	}{
		{"author-prefixed reason stays single", `reason: "Straza: env reads are off for role dev"`, "Straza: env reads are off for role dev"},
		{"bare reason gains the prefix", `reason: "env reads are off for role dev"`, "Straza: env reads are off for role dev"},
		{"reason-less deny keeps the engine's prefix", "", "Straza: blocked by policy rule mcp-dev/deny-env"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, base, _ := testAppCounting(t, func(c *config.Config) { c.Apps.Catalog.PolicyFilter = false })
			up := startGatewayUpstream(t)
			seedGatewayUser(t, app, "kim", "dev")
			catInstallEcho(t, app, up, "echoapp", []string{"*"})
			catActivate(t, app, "mcp-dev", fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: mcp-dev}
spec:
  match: {roles: [dev]}
  rules:
    - id: deny-env
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {deny: ["env"]}
      effect: deny
      %s
    - id: allow-echo
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {allow: ["echo"]}
      effect: allow
`, tc.reason))
			tok := sessionToken(t, base, "kim")
			_, res, raw := mcpCall(t, base, tok, "tools/call", map[string]any{"name": "echoapp__env"})
			result, _ := res["result"].(map[string]any)
			if result == nil || result["isError"] != true {
				t.Fatalf("deny must be a tool result with isError true, got %s", raw)
			}
			content, _ := result["content"].([]any)
			if len(content) != 1 {
				t.Fatalf("deny content = %v, want one text block", content)
			}
			first, _ := content[0].(map[string]any)
			text, _ := first["text"].(string)
			if text != tc.want {
				t.Fatalf("deny text = %q, want %q", text, tc.want)
			}
			if n := strings.Count(text, "Straza:"); n != 1 {
				t.Fatalf("deny text carries the prefix %d times, want exactly once: %q", n, text)
			}
		})
	}
}
