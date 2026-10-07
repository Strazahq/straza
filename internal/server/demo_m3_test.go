package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// githubMock imitates the GitHub MCP server: real MCP over streamable HTTP
// with GitHub-shaped tools, recording every Authorization header it receives.
type githubMock struct {
	*httptest.Server
	mu      sync.Mutex
	headers []string
}

func startGithubMock(t *testing.T) *githubMock {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "github-mock", Version: "0.17.1"}, nil)
	type issueArgs struct {
		Number int `json:"number"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "get_issue", Description: "Get an issue"},
		func(_ context.Context, _ *mcp.CallToolRequest, a issueArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("issue #%d: gateway PEP ships in M3", a.Number)}}}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "list_issues", Description: "List issues"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "#1 gateway PEP"}}}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "create_issue", Description: "Create an issue"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "created #2"}}}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "delete_repository", Description: "Delete a repository"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "gone"}}}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	g := &githubMock{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.headers = append(g.headers, r.Header.Get("Authorization"))
		g.mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(g.Close)
	return g
}

// waitFileDraft waits up to within for an open draft that the apps
// directory file path proposed.
func waitFileDraft(t *testing.T, app *App, path string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		rows, err := app.store.Drafts().BySource(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.State == "open" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no open draft of %s within %s", path, within)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (g *githubMock) sawAuth(v string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, h := range g.headers {
		if h == v {
			return true
		}
	}
	return false
}

// TestM3Demo drives the GitOps demo end to end:
// drop `github.app.yaml` into the watched apps/ dir and publish the draft
// it becomes → a user with role dev sees GitHub tools through the gateway
// (as Claude Code would over MCP); a user without the role sees nothing;
// the token never appears client-side.
// The manifest is the spec example valid-github-remote.yaml verbatim, with
// only the remote URL pointed at a hermetic GitHub mock.
func TestM3Demo(t *testing.T) {
	// Serial: its 2 and 3 second budgets flake under the load of parallel servers.
	gh := startGithubMock(t)
	const ghToken = "ghp_DEMO-1f2e3d4c-NEVER-CLIENT-SIDE"

	app, base := testApp(t, func(cfg *config.Config) {
		cfg.Apps.PollInterval = 100 * time.Millisecond
		cfg.Apps.HealthInterval = time.Hour
	})
	ctx := context.Background()

	// Every byte the "client side" receives in this demo accumulates here;
	// the final assertion greps it for the GitHub token.
	var clientBytes bytes.Buffer

	// -- Identity: alice holds dev; bob holds nothing; kim publishes. --
	seedGatewayUser(t, app, "alice", "dev")
	kim := mkHuman(t, app, "kim")
	grantAdmin(t, app, kim.ID)
	publisher := &draftsFixture{app: app, base: base, kim: kim, root: loginDeviceFlow(t, base, "kim", "hunter2!")}
	hash, err := testPasswordHash("hunter2!")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Users().Create(ctx, store.User{Username: "bob", PasswordHash: hash}); err != nil {
		t.Fatal(err)
	}

	// -- The manifest: spec example VERBATIM, URL swapped to the mock. --
	raw, err := os.ReadFile(filepath.Join("..", "..", "spec", "app-manifest", "examples", "valid-github-remote.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	mf, err := manager.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if mf.Straza.Runtime.Remote.URL != "https://api.githubcopilot.com/mcp/" {
		t.Fatalf("spec example drifted: url = %s", mf.Straza.Runtime.Remote.URL)
	}
	mf.Straza.Runtime.Remote.URL = gh.URL // the only change: hermetic upstream
	manifestYAML, err := yaml.Marshal(mf)
	if err != nil {
		t.Fatal(err)
	}

	// -- Operator setup: secret, binding, policy (as strazactl would). --
	// The app row must exist for secret/binding rows; publishing the draft
	// the GitOps drop below proposes changes it by name.
	appRow, err := app.store.Apps().Create(ctx, store.App{Name: "github", RuntimeKind: "remote", Manifest: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	devRole, err := app.store.Roles().GetByName(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.broker.Set(ctx, appRow.ID, devRole.ID, ghToken); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{
		RoleID: devRole.ID, AppID: appRow.ID,
		ToolMatcher: `["get_*","list_*","search_*","create_issue"]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: "github-readmostly", Status: "active", YAMLSource: `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: github-readmostly}
spec:
  match: {roles: [dev]}
  rules:
    - id: github-readmostly
      tools: [mcp.call]
      apps: [github]
      toolNames: {allow: ["get_*", "list_*", "search_*", "create_issue"]}
      effect: allow
`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}

	// -- Drop github.app.yaml into apps/ and publish the draft it becomes. --
	dropped := time.Now()
	path := filepath.Join(app.cfg.AppsDir(), "github.app.yaml")
	if err := os.WriteFile(path, manifestYAML, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFileDraft(t, app, path, 3*time.Second)
	publishOpenOf(t, publisher, path)
	for {
		if v, ok := app.manager.View("github"); ok && v.Status == "running" {
			break
		}
		if time.Since(dropped) > 10*time.Second {
			v, _ := app.manager.View("github")
			t.Fatalf("github app not running after drop: %+v", v)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if elapsed := time.Since(dropped); elapsed > 3*time.Second {
		t.Errorf("drop-to-running took %s (> 3s)", elapsed)
	}

	// -- Alice (role dev): the gateway serves the GitHub catalog. --
	aliceTok := sessionToken(t, base, "alice")
	if code, res, rawInit := mcpCall(t, base, aliceTok, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]string{"name": "claude-code", "version": "2.1.0"},
	}); code != http.StatusOK {
		t.Fatalf("initialize = %d %v", code, res)
	} else {
		clientBytes.Write(rawInit)
	}

	_, aliceList, rawList := mcpCall(t, base, aliceTok, "tools/list", nil)
	clientBytes.Write(rawList)
	aliceTools := toolNamesOf(t, aliceList)
	want := []string{"github__create_issue", "github__get_issue", "github__list_issues"}
	if strings.Join(aliceTools, ",") != strings.Join(want, ",") {
		t.Errorf("alice sees %v, want %v (delete_repository must NOT be visible)", aliceTools, want)
	}

	_, callRes, rawCall := mcpCall(t, base, aliceTok, "tools/call", map[string]any{
		"name": "github__get_issue", "arguments": map[string]int{"number": 1},
	})
	clientBytes.Write(rawCall)
	if !strings.Contains(string(rawCall), "issue #1") {
		t.Errorf("get_issue = %v", callRes)
	}
	if !gh.sawAuth("Bearer " + ghToken) {
		t.Error("upstream never received the injected GitHub token")
	}

	// delete_repository is invisible: calling it is indistinguishable from
	// calling a tool that does not exist.
	_, delRes, rawDel := mcpCall(t, base, aliceTok, "tools/call", map[string]any{"name": "github__delete_repository"})
	clientBytes.Write(rawDel)
	if !strings.Contains(string(rawDel), "unknown tool") {
		t.Errorf("delete_repository call = %v", delRes)
	}

	// -- Bob (no role): sees nothing at all. --
	bobTok := sessionToken(t, base, "bob")
	_, bobList, rawBob := mcpCall(t, base, bobTok, "tools/list", nil)
	clientBytes.Write(rawBob)
	if n := len(toolNamesOf(t, bobList)); n != 0 {
		t.Errorf("bob sees %d tools, want 0", n)
	}

	// -- The token never appears client-side. --
	if bytes.Contains(clientBytes.Bytes(), []byte(ghToken)) {
		t.Fatal("GITHUB TOKEN LEAKED into client-visible bytes")
	}

	// -- Remove the file and publish the removal it proposes: the app stops
	// and the catalog empties. --
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	waitFileDraft(t, app, path, 3*time.Second)
	publishOpenOf(t, publisher, path)
	removeDeadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := app.manager.View("github"); !ok {
			break
		}
		if time.Now().After(removeDeadline) {
			t.Fatal("github app still managed after file removal")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Catalog invalidation (manager OnChange → epoch bump) trails the
	// instance removal by a hair; poll briefly instead of racing the first
	// read. Calls in that window still fail closed (ErrNotReady); only
	// tools/list visibility is momentarily stale.
	catalogDeadline := time.Now().Add(2 * time.Second)
	for {
		_, afterList, _ := mcpCall(t, base, aliceTok, "tools/list", nil)
		n := len(toolNamesOf(t, afterList))
		if n == 0 {
			break
		}
		if time.Now().After(catalogDeadline) {
			t.Errorf("catalog after removal has %d tools, want 0", n)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}
