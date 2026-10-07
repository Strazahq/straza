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

	"github.com/strazahq/straza/internal/store"
)

// TestGatewayNoDBOnRequestPath proves the no-DB-reads-on-a-request-path rule
// the gateway.go header names. After warm-up (identity seeded, app installed
// and probed into the catalog, snapshot compiled, session checked in) it fires
// the real request paths and asserts that no forbidden repo of the 18 is touched:
// gateway tools/list (cold catalog build, then the cached serve), tools/call on
// both the allow path and the deny path, POST /v1/decide allow and deny, the
// DECISION lane of an approve-gated tools/call, and the drafting tools.
//
// Two phases are documented exceptions: the approve phase's escalation lane
// reads and writes `approvals`, and the drafting phase's tools read and write
// their own rows of `drafts` by id. Subject, roles, catalog, credentials,
// snapshot and denylist must still come from memory on both, so each phase
// pins that its one repo may move and the other 17 must not. Warm-up traffic
// is NOT asserted (check-in mints a session row, and control-plane seeding
// is DB by definition); the window opens once those caches are warm.
func TestGatewayNoDBOnRequestPath(t *testing.T) {
	t.Parallel()
	app, base, cs := testAppCounting(t)
	up := startNoDBUpstream(t)
	ctx := context.Background()

	// ---- warm-up: control plane (DB access expected, not asserted) ----
	seedGatewayUser(t, app, "kim", "dev")
	seedGatewayUser(t, app, "lee", "intern")

	manifest := fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: echoapp}
server: {name: straza.test/echo, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
  credential:
    kind: static
    inject: {as: header, name: Authorization, template: "Bearer {{secret}}"}
`, up.URL)
	mf, err := managerParse(t, manifest)
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.manager.Install(ctx, mf, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	devRole, err := app.store.Roles().GetByName(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.broker.Set(ctx, row.ID, devRole.ID, "sk-live-nodb-NEVER-LEAK"); err != nil {
		t.Fatal(err)
	}
	app.manager.SecretUpdated(ctx, row.ID)
	if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{
		RoleID: devRole.ID, AppID: row.ID, ToolMatcher: `["*"]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}

	// echo is a plain allow, env is denied with a reason, and gated is the
	// approve-gated tool whose decision lane the last phase measures. The
	// 1-second timeout keeps the blocking Await short: nobody approves, so the
	// gate resolves to the fail-closed timeout deny.
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: "mcp-nodb", Status: "active", YAMLSource: `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: mcp-nodb}
spec:
  match: {roles: [dev]}
  rules:
    - id: deny-env
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {deny: ["env"]}
      effect: deny
      reason: "env reads are blocked for role dev"
    - id: approve-gated
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {allow: ["gated"]}
      effect: allow
      mode: approve
      approve: {roles: [sec-approvers], timeoutSeconds: 1}
    - id: allow-echo
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {allow: ["echo"]}
      effect: allow
    - id: deny-rm
      tools: [shell.exec]
      command: {denyPatterns: ["rm -rf *"]}
      effect: deny
      reason: "Destructive delete blocked"
`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if v, ok := app.manager.View("echoapp"); ok && v.Status == "running" {
			break
		}
		if time.Now().After(deadline) {
			v, _ := app.manager.View("echoapp")
			t.Fatalf("echoapp not running: %+v", v)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Check-in mints the session and fills the subject cache, the warm-up
	// step every asserted path below depends on.
	kimTok := sessionToken(t, base, "kim")
	leeTok := sessionToken(t, base, "lee")

	// ---- phase 1: gateway tools/list ----
	before := cs.snapshot()
	_, kimList, _ := mcpCall(t, base, kimTok, "tools/list", nil)
	if names := toolNamesOf(t, kimList); len(names) == 0 {
		t.Fatalf("kim catalog is empty: %v", kimList)
	}
	_, kimAgain, _ := mcpCall(t, base, kimTok, "tools/list", nil) // cached serve
	if len(toolNamesOf(t, kimAgain)) == 0 {
		t.Fatal("cached tools/list came back empty")
	}
	_, leeList, _ := mcpCall(t, base, leeTok, "tools/list", nil)
	if n := len(toolNamesOf(t, leeList)); n != 0 {
		t.Errorf("lee catalog has %d tools, want 0", n)
	}
	assertNoRepoAccess(t, cs, "gateway tools/list", before)

	// ---- phase 2: gateway tools/call, allow ----
	before = cs.snapshot()
	_, allowRes, allowRaw := mcpCall(t, base, kimTok, "tools/call", map[string]any{
		"name": "echoapp__echo", "arguments": map[string]string{"text": "hi"},
	})
	if !strings.Contains(string(allowRaw), "echo: hi") {
		t.Fatalf("allowed call did not round-trip: %v", allowRes)
	}
	assertNoRepoAccess(t, cs, "gateway tools/call (allow)", before)

	// ---- phase 3: gateway tools/call, deny ----
	before = cs.snapshot()
	_, denyRes, denyRaw := mcpCall(t, base, kimTok, "tools/call", map[string]any{"name": "echoapp__env"})
	if !strings.Contains(string(denyRaw), "env reads are blocked for role dev") {
		t.Fatalf("deny did not carry the rule reason: %v", denyRes)
	}
	// An unbound role's call is an unknown tool: the catalog-visibility deny.
	_, leeCall, _ := mcpCall(t, base, leeTok, "tools/call", map[string]any{"name": "echoapp__echo"})
	if errObj, _ := leeCall["error"].(map[string]any); errObj == nil {
		t.Errorf("lee call should have failed: %v", leeCall)
	}
	assertNoRepoAccess(t, cs, "gateway tools/call (deny)", before)

	// ---- phase 4: POST /v1/decide, allow and deny ----
	before = cs.snapshot()
	if code, dec := decide(t, base, kimTok, map[string]any{
		"kind": "tool.pre", "tool": "shell.exec", "command": "rm -rf /tmp/x",
	}); code != http.StatusOK || dec["effect"] != "deny" {
		t.Fatalf("decide deny = %d %v", code, dec)
	}
	if _, dec := decide(t, base, kimTok, map[string]any{
		"kind": "tool.pre", "tool": "shell.exec", "command": "ls -la",
	}); dec["effect"] != "allow" {
		t.Fatalf("decide allow = %v", dec)
	}
	assertNoRepoAccess(t, cs, "POST /v1/decide", before)

	// ---- phase 5: the DECISION lane of an approve-gated tools/call ----
	//
	// Nobody approves, so the gate blocks for the rule's 1s timeout and then
	// fails closed. What the phase proves: reaching a human costs exactly ONE
	// repo (approvals), and every other input to the decision (subject, roles,
	// catalog visibility, snapshot, credential, denylist) still comes from
	// memory on the slow lane too.
	//
	// The approvals traffic itself: Service.Request first READS the
	// pending-dedupe row (FindPendingByKey) so a retry attaches to the request
	// already in front of a human, then WRITES the record (Insert). Both are
	// the sanctioned escalation lane, which internal/store documents as "not
	// the fast PDP path", and neither is reachable from a decision without an
	// approve marker (TestDecideApproveZeroCost pins that zero cost).
	before = cs.snapshot()
	_, gatedRes, gatedRaw := mcpCall(t, base, kimTok, "tools/call", map[string]any{
		"name":      "echoapp__gated",
		"arguments": map[string]any{"text": "x", justificationField: "shipping the fix"},
	})
	if !strings.Contains(string(gatedRaw), "approval request expired") {
		t.Fatalf("approve gate did not fail closed on timeout: %v", gatedRes)
	}
	assertNoRepoAccess(t, cs, "approve-gated tools/call (decision lane)", before, "approvals")

	// ---- phase 6: the drafting tools of the built-in app ----
	//
	// A submit counts the caller's open drafts and writes its own rows, and a
	// status reads one row by id. The role check, the rates,
	// the proposer and the catalog come from memory, so `drafts` may move and
	// the other 17 must not. A submit wakes the drafts checker, whose read of
	// live state is off the request path but would land in this window, so
	// the phase holds a.configMu, which every checker tick takes first.
	seedAgent(t, app, "joe", "kim", DraftConfigRole)
	joeTok := sessionToken(t, base, "joe")
	func() {
		app.configMu.Lock()
		defer app.configMu.Unlock()
		before = cs.snapshot()
		text, isErr, sc := submit(t, base, joeTok, map[string]any{"documents": []string{draftApp("weather", "https://weather.example/mcp", "The weather.")}})
		id, _ := sc["draft"].(string)
		if isErr || id == "" {
			t.Fatalf("submit = %q", text)
		}
		if text, isErr, _ := status(t, base, joeTok, id); isErr {
			t.Fatalf("status = %q", text)
		}
		assertNoRepoAccess(t, cs, "drafting tools", before, "drafts")
	}()
}

// startNoDBUpstream is TestGatewayNoDBOnRequestPath's own MCP upstream: echo
// (plain allow), env (policy deny), gated (approve-gated). It is separate from
// startGatewayUpstream so the third tool cannot perturb TestGatewayPEP's
// exact-catalog pins.
func startNoDBUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "nodb-upstream", Version: "1.0.0"}, nil)
	type textArgs struct {
		Text string `json:"text"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "echo"},
		func(_ context.Context, _ *mcp.CallToolRequest, a textArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + a.Text}}}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "env", Description: "sensitive"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "top secret env"}}}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "gated", Description: "needs a human"},
		func(_ context.Context, _ *mcp.CallToolRequest, a textArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "gated: " + a.Text}}}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return s
}
