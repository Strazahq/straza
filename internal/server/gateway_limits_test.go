package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// slowUpstream serves an MCP server whose only tool blocks until the request
// context is cancelled (or a generous ceiling elapses).
func slowUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "slow", Version: "1.0.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "hang", Description: "block until cancelled"},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(30 * time.Second):
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil, nil
			}
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	up := httptest.NewServer(handler)
	t.Cleanup(up.Close)
	return up
}

// deploySlowApp installs a remote app over the slow upstream, binds its tools
// to dev, activates an allow policy, and waits for running.
func deploySlowApp(t *testing.T, app *App, name, url string, timeoutSeconds int) {
	t.Helper()
	ctx := context.Background()
	limits := ""
	if timeoutSeconds > 0 {
		limits = fmt.Sprintf("\n  limits: {timeoutSeconds: %d}", timeoutSeconds)
	}
	mf, err := managerParse(t, fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: %s}
server: {name: straza.test/%s, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}%s
`, name, name, url, limits))
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.manager.Install(ctx, mf, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := app.store.Roles().GetByName(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{
		RoleID: dev.ID, AppID: row.ID, ToolMatcher: `["*"]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: name + "-allow", Status: "active", YAMLSource: fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: %s-allow}
spec:
  match: {roles: [dev]}
  rules:
    - id: %s-allow-all
      tools: [mcp.call]
      apps: [%s]
      toolNames: {allow: ["*"]}
      effect: allow
`, name, name, name),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if v, ok := app.manager.View(name); ok && v.Status == "running" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s not running", name)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// callHang invokes the hanging tool and asserts the gateway cut it off with
// an upstream failure well before `within` elapsed.
func callHang(t *testing.T, base, tok, appName string, within time.Duration) {
	t.Helper()
	start := time.Now()
	code, body, raw := mcpCall(t, base, tok, "tools/call", map[string]any{"name": appName + "__hang"})
	elapsed := time.Since(start)
	if code != http.StatusOK {
		t.Fatalf("tools/call = %d %s", code, raw)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("hanging call did not fail: %s", raw)
	}
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "upstream call failed") {
		t.Fatalf("error = %q, want upstream call failure", msg)
	}
	if elapsed > within {
		t.Errorf("timeout fired after %s, want < %s (wrong ceiling applied)", elapsed, within)
	}
}

// TestUpstreamTimeoutPerApp: straza.limits.timeoutSeconds (spec/app-manifest
// §4, revision 2) caps a single tools/call for that app; the 1 s manifest
// ceiling must fire long before the 30 s server-wide default would.
func TestUpstreamTimeoutPerApp(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	seedGatewayUser(t, app, "kim", "dev")
	deploySlowApp(t, app, "slowapp", slowUpstream(t).URL, 1)
	tok := sessionToken(t, base, "kim")
	callHang(t, base, tok, "slowapp", 10*time.Second)
}

// TestUpstreamTimeoutGlobalFallback: with no per-app limit, the server-wide
// apps.upstreamTimeout bounds the call.
func TestUpstreamTimeoutGlobalFallback(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) { c.Apps.UpstreamTimeout = time.Second })
	seedGatewayUser(t, app, "kim", "dev")
	deploySlowApp(t, app, "globalapp", slowUpstream(t).URL, 0)
	tok := sessionToken(t, base, "kim")
	callHang(t, base, tok, "globalapp", 10*time.Second)
}
