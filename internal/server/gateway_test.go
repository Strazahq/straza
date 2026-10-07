package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

func managerParse(t *testing.T, doc string) (manager.Manifest, error) {
	t.Helper()
	return manager.Parse([]byte(doc))
}

// The request-path counting harness (countingStore, storeRepoLanes,
// controlPlaneRepos, assertNoRepoAccess, testAppCounting) lives in
// store_counting_test.go; the
// proof it exists for is TestGatewayNoDBOnRequestPath in gateway_nodb_test.go.

// gatewayUpstream is an MCP server with two tools (echo, env) that records
// the Authorization headers it receives.
type gatewayUpstream struct {
	*httptest.Server
	mu      sync.Mutex
	headers []string
}

func startGatewayUpstream(t *testing.T) *gatewayUpstream {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "gw-upstream", Version: "1.0.0"}, nil)
	type echoArgs struct {
		Text string `json:"text"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "echo"},
		func(_ context.Context, _ *mcp.CallToolRequest, a echoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + a.Text}}}, nil, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "env", Description: "sensitive"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "top secret env"}}}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	u := &gatewayUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.headers = append(u.headers, r.Header.Get("Authorization"))
		u.mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *gatewayUpstream) sawAuth(v string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, h := range u.headers {
		if h == v {
			return true
		}
	}
	return false
}

// seedGatewayUser creates a local user with a role assignment.
func seedGatewayUser(t *testing.T, app *App, username, roleName string) store.User {
	t.Helper()
	ctx := context.Background()
	hash, err := testPasswordHash("hunter2!")
	if err != nil {
		t.Fatal(err)
	}
	u, err := app.store.Users().Create(ctx, store.User{Username: username, PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	role, err := app.store.Roles().GetByName(ctx, roleName)
	if err != nil {
		role, err = app.store.Roles().Create(ctx, store.Role{Name: roleName})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.store.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: u.ID, RoleID: role.ID,
	}); err != nil {
		t.Fatal(err)
	}
	app.resolver.Bump()
	return u
}

// sessionToken checks a user in (as straza would) and returns the
// session token; the checkin also populates the gateway's subject cache.
func sessionToken(t *testing.T, base, username string) string {
	t.Helper()
	idToken := loginDeviceFlow(t, base, username, "hunter2!")
	code, body := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "x"}},
	})
	if code != http.StatusOK {
		t.Fatalf("checkin = %d: %v", code, body)
	}
	tok, _ := body["session_token"].(string)
	if tok == "" {
		t.Fatalf("no session token: %v", body)
	}
	return tok
}

// mcpCall posts one JSON-RPC message to the gateway and returns status +
// decoded body + raw bytes.
func mcpCall(t *testing.T, base, token, method string, params any) (int, map[string]any, []byte) {
	t.Helper()
	return mcpPost(t, base+"/mcp", token, method, params)
}

// mcpPost posts one JSON-RPC message to the gateway endpoint url and returns
// status + decoded body + raw bytes.
func mcpPost(t *testing.T, url, token, method string, params any) (int, map[string]any, []byte) {
	t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		msg["params"] = params
	}
	raw, _ := json.Marshal(msg)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	return resp.StatusCode, decoded, body
}

func toolNamesOf(t *testing.T, res map[string]any) []string {
	t.Helper()
	result, _ := res["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	var names []string
	for _, tl := range tools {
		m, _ := tl.(map[string]any)
		names = append(names, m["name"].(string))
	}
	return names
}

// TestGatewayPEP pins the gateway PEP: two users with different roles get
// provably different catalogs; an unauthorized call is denied with the rule
// reason; a non-bound user cannot even see (or call) the tools; the
// injected secret reaches upstream and never appears client-side; and no
// control-plane repo is touched during gateway traffic.
func TestGatewayPEP(t *testing.T) {
	t.Parallel()
	app, base, cs := testAppCounting(t)
	up := startGatewayUpstream(t)
	ctx := context.Background()

	// Identity: kim holds dev, lee holds intern.
	kim := seedGatewayUser(t, app, "kim", "dev")
	_ = kim
	seedGatewayUser(t, app, "lee", "intern")

	// App: remote echo upstream with a static header credential.
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
	const secretValue = "sk-live-8827-NEVER-LEAK"
	if _, err := app.broker.Set(ctx, row.ID, devRole.ID, secretValue); err != nil {
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

	// Policy: dev may call echo; env is denied with a reason; default deny.
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: "mcp-dev", Status: "active", YAMLSource: `
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
      reason: "env reads are blocked for role dev"
    - id: allow-echo
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {allow: ["echo"]}
      effect: allow
`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}

	// Wait for the app to be probed into the catalog.
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

	kimTok := sessionToken(t, base, "kim")
	leeTok := sessionToken(t, base, "lee")

	// ---- Request-path phase: snapshot control-plane repo counters. ----
	before := cs.snapshot()

	// initialize handshake.
	code, res, _ := mcpCall(t, base, kimTok, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"clientInfo":      map[string]string{"name": "claude-code", "version": "2.1.0"},
		"capabilities":    map[string]any{},
	})
	if code != http.StatusOK {
		t.Fatalf("initialize = %d", code)
	}
	if result, _ := res["result"].(map[string]any); result["protocolVersion"] != "2025-06-18" {
		t.Errorf("initialize result = %v", res)
	}

	// Catalogs: kim sees both tools, lee sees nothing.
	_, kimList, _ := mcpCall(t, base, kimTok, "tools/list", nil)
	kimTools := toolNamesOf(t, kimList)
	if len(kimTools) != 2 || kimTools[0] != "echoapp__echo" || kimTools[1] != "echoapp__env" {
		t.Errorf("kim catalog = %v", kimTools)
	}
	_, leeList, _ := mcpCall(t, base, leeTok, "tools/list", nil)
	if n := len(toolNamesOf(t, leeList)); n != 0 {
		t.Errorf("lee catalog has %d tools, want 0", n)
	}

	// Allowed call round-trips; the injected credential reaches upstream and
	// never appears in any client-visible byte.
	_, callRes, callRaw := mcpCall(t, base, kimTok, "tools/call", map[string]any{
		"name": "echoapp__echo", "arguments": map[string]string{"text": "hi"},
	})
	if !strings.Contains(string(callRaw), "echo: hi") {
		t.Errorf("call result = %v", callRes)
	}
	if !up.sawAuth("Bearer " + secretValue) {
		t.Error("upstream did not receive the injected credential")
	}
	if bytes.Contains(callRaw, []byte(secretValue)) {
		t.Fatal("SECRET LEAKED into a client-visible response")
	}

	// Policy deny carries the rule reason as a tool error.
	_, denyRes, denyRaw := mcpCall(t, base, kimTok, "tools/call", map[string]any{"name": "echoapp__env"})
	if !strings.Contains(string(denyRaw), "env reads are blocked for role dev") {
		t.Errorf("deny result = %v", denyRes)
	}
	if strings.Contains(string(denyRaw), "top secret env") {
		t.Error("denied call reached upstream")
	}

	// Invisible tools are unknown tools for unbound roles.
	_, leeCall, _ := mcpCall(t, base, leeTok, "tools/call", map[string]any{"name": "echoapp__echo"})
	if errObj, _ := leeCall["error"].(map[string]any); errObj == nil || !strings.Contains(errObj["message"].(string), "unknown tool") {
		t.Errorf("lee call = %v", leeCall)
	}

	// Unknown method and auth failures.
	_, unk, _ := mcpCall(t, base, kimTok, "resources/list", nil)
	if errObj, _ := unk["error"].(map[string]any); errObj == nil {
		t.Errorf("unknown method = %v", unk)
	}
	if code, _, _ := mcpCall(t, base, "", "tools/list", nil); code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", code)
	}

	// ---- No DB on the request path. The dedicated proof is
	// TestGatewayNoDBOnRequestPath; this test folds into the same assertion so
	// both cover all 18 forbidden repos. ----
	assertNoRepoAccess(t, cs, "TestGatewayPEP request phase", before)

	// Async audit: straza.audit.mcp events land in the outbox/audit chain:
	// the deny with its rule id, and the allowed call with the verbatim
	// tools/call arguments (the MCP analog of the exact shell command).
	auditDeadline := time.Now().Add(10 * time.Second)
	for {
		recs, err := app.store.Audit().List(ctx, 0, 1000)
		if err == nil {
			foundDeny, foundArgs := false, false
			for _, r := range recs {
				if !strings.Contains(r.CE, "straza.audit.mcp") {
					continue
				}
				// Revision 20: the CE names the deciding SET alongside the rule.
				if strings.Contains(r.CE, "deny-env") && strings.Contains(r.CE, `"setName":"mcp-dev"`) {
					foundDeny = true
				}
				if strings.Contains(r.CE, "arguments") && strings.Contains(r.CE, `\"text\":\"hi\"`) {
					foundArgs = true
				}
			}
			if foundDeny && foundArgs {
				break
			}
		}
		if time.Now().After(auditDeadline) {
			t.Fatal("audit chain is missing the mcp deny and/or the allowed call's arguments")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Revoked session: denied at the gateway door.
	if code := adminSessionRevoke(t, app, base, "kim"); code != 0 {
		t.Logf("revoke helper status %d", code)
	}
	if code, _, _ := mcpCall(t, base, kimTok, "tools/list", nil); code != http.StatusForbidden {
		t.Errorf("revoked session = %d, want 403", code)
	}
}

// adminSessionRevoke revokes all of a user's sessions directly (denylist is
// fed by the admin path; here we poke the internals like admin revoke does).
func adminSessionRevoke(t *testing.T, app *App, _ string, username string) int {
	t.Helper()
	ctx := context.Background()
	u, err := app.store.Users().GetByUsername(ctx, username)
	if err != nil {
		t.Fatal(err)
	}
	app.denylist.revokeUser(u.ID)
	return 0
}

// TestGatewayListChanged: a binding change invalidates catalogs, notifies
// live SSE streams, and the next tools/list reflects it.
func TestGatewayListChanged(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t)
	up := startGatewayUpstream(t)
	ctx := context.Background()

	seedGatewayUser(t, app, "kim", "dev")
	manifest := fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: openapp}
server: {name: straza.test/open, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
`, up.URL)
	mf, err := managerParse(t, manifest)
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.manager.Install(ctx, mf, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if v, ok := app.manager.View("openapp"); ok && v.Status == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("openapp not running")
		}
		time.Sleep(20 * time.Millisecond)
	}

	tok := sessionToken(t, base, "kim")
	if n := len(toolNamesOf(t, second(mcpCall(t, base, tok, "tools/list", nil)))); n != 0 {
		t.Fatalf("catalog should start empty, got %d tools", n)
	}

	// Open the SSE stream, then bind the app to dev.
	req, _ := http.NewRequest(http.MethodGet, base+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE stream = %d", resp.StatusCode)
	}

	devRole, err := app.store.Roles().GetByName(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{
		RoleID: devRole.ID, AppID: row.ID, ToolMatcher: `["echo"]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}

	// The stream delivers notifications/tools/list_changed.
	notified := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				notified <- line
				return
			}
		}
	}()
	select {
	case line := <-notified:
		if !strings.Contains(line, "notifications/tools/list_changed") {
			t.Errorf("SSE line = %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no list_changed notification on the SSE stream")
	}

	names := toolNamesOf(t, second(mcpCall(t, base, tok, "tools/list", nil)))
	if len(names) != 1 || names[0] != "openapp__echo" {
		t.Errorf("catalog after bind = %v", names)
	}
}

func second(_ int, m map[string]any, _ []byte) map[string]any { return m }

// TestGatewayRateLimit pins that a manifest rps cap throttles calls
// per (session, app) with a Straza tool error, and the limit is enforced
// before the upstream is touched.
func TestGatewayRateLimit(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t)
	up := startGatewayUpstream(t)
	ctx := context.Background()

	seedGatewayUser(t, app, "kim", "dev")
	mf, err := managerParse(t, fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: capped}
server: {name: straza.test/capped, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
  limits: {rps: 1}
`, up.URL))
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
	if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{
		RoleID: devRole.ID, AppID: row.ID, ToolMatcher: `["echo"]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: "allow-capped", Status: "active", YAMLSource: `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: allow-capped}
spec:
  match: {roles: [dev]}
  rules:
    - id: allow-echo
      tools: [mcp.call]
      apps: [capped]
      toolNames: {allow: ["echo"]}
      effect: allow
`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if v, ok := app.manager.View("capped"); ok && v.Status == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("capped app not running")
		}
		time.Sleep(20 * time.Millisecond)
	}

	tok := sessionToken(t, base, "kim")
	call := func() (map[string]any, string) {
		_, res, raw := mcpCall(t, base, tok, "tools/call", map[string]any{
			"name": "capped__echo", "arguments": map[string]string{"text": "x"},
		})
		return res, string(raw)
	}

	// burst=1: first passes, a rapid second is throttled with the Straza error.
	if _, raw := call(); !strings.Contains(raw, "echo: x") {
		t.Fatalf("first call not served: %s", raw)
	}
	throttled := false
	for i := 0; i < 5; i++ {
		if _, raw := call(); strings.Contains(raw, "rate limit exceeded") {
			throttled = true
			break
		}
	}
	if !throttled {
		t.Error("rate limit never fired under a burst")
	}
}

// walkToolsList pages through the whole catalog following nextCursor, returning
// the concatenated tool names and the number of pages walked. It fails if a page
// exceeds maxPer or the walk does not terminate.
func walkToolsList(t *testing.T, base, tok string, maxPer int) ([]string, int) {
	t.Helper()
	var got []string
	cursor := ""
	pages := 0
	for {
		var params any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		res := second(mcpCall(t, base, tok, "tools/list", params))
		result, _ := res["result"].(map[string]any)
		if result == nil {
			t.Fatalf("page %d has no result: %v", pages, res)
		}
		names := toolNamesOf(t, res)
		if len(names) > maxPer {
			t.Fatalf("page %d has %d tools, want <= %d", pages, len(names), maxPer)
		}
		got = append(got, names...)
		pages++
		nc, ok := result["nextCursor"].(string)
		if !ok {
			return got, pages
		}
		cursor = nc
		if pages > 50 {
			t.Fatal("pagination did not terminate")
		}
	}
}

// TestGatewayToolsListPagination walks a 6-tool catalog at pageSize 2: three
// pages, stable order across the chain, no nextCursor on the final page, and the
// concatenation equals the full sorted catalog.
func TestGatewayToolsListPagination(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t)
	app.cfg.Apps.Catalog.PageSize = 2
	up := startGatewayUpstream(t)
	seedGatewayUser(t, app, "kim", "dev")
	catInstallEcho(t, app, up, "aapp", []string{"*"})
	catInstallEcho(t, app, up, "bapp", []string{"*"})
	catInstallEcho(t, app, up, "capp", []string{"*"})
	tok := sessionToken(t, base, "kim")

	want := []string{
		"aapp__echo", "aapp__env", "bapp__echo", "bapp__env", "capp__echo", "capp__env",
	}
	got, pages := walkToolsList(t, base, tok, 2)
	if pages != 3 {
		t.Errorf("walked %d pages, want 3", pages)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("paged catalog = %v, want %v", got, want)
	}
}

// TestGatewayToolsListPaginationDisabled: pageSize <= 0 serves the whole catalog
// in one response with no nextCursor key (the historical shape).
func TestGatewayToolsListPaginationDisabled(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t) // testAppCounting leaves PageSize 0
	up := startGatewayUpstream(t)
	seedGatewayUser(t, app, "kim", "dev")
	catInstallEcho(t, app, up, "aapp", []string{"*"})
	catInstallEcho(t, app, up, "bapp", []string{"*"})
	tok := sessionToken(t, base, "kim")

	res := second(mcpCall(t, base, tok, "tools/list", nil))
	result := res["result"].(map[string]any)
	if _, ok := result["nextCursor"]; ok {
		t.Error("pageSize 0 must not emit a nextCursor")
	}
	if n := len(toolNamesOf(t, res)); n != 4 {
		t.Errorf("unpaginated catalog = %d tools, want all 4 in one response", n)
	}
}

// TestGatewayToolsListStaleCursor: a cursor minted against one catalog fails with
// -32602 after an Invalidate rebuilds it (the digest no longer matches).
func TestGatewayToolsListStaleCursor(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t)
	app.cfg.Apps.Catalog.PageSize = 2
	up := startGatewayUpstream(t)
	seedGatewayUser(t, app, "kim", "dev")
	catInstallEcho(t, app, up, "aapp", []string{"*"})
	catInstallEcho(t, app, up, "bapp", []string{"*"})
	tok := sessionToken(t, base, "kim")

	res := second(mcpCall(t, base, tok, "tools/list", nil))
	cursor, ok := res["result"].(map[string]any)["nextCursor"].(string)
	if !ok || cursor == "" {
		t.Fatalf("first page carried no nextCursor: %v", res)
	}

	app.gateway.Invalidate() // rebuilds the catalog under a new epoch/id

	_, call, _ := mcpCall(t, base, tok, "tools/list", map[string]any{"cursor": cursor})
	assertCursorRejected(t, call)
}

// TestGatewayToolsListBadCursor: malformed cursors all fail closed with -32602
// and the exact invalidCursorMessage.
func TestGatewayToolsListBadCursor(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t)
	app.cfg.Apps.Catalog.PageSize = 2
	up := startGatewayUpstream(t)
	seedGatewayUser(t, app, "kim", "dev")
	catInstallEcho(t, app, up, "aapp", []string{"*"})
	catInstallEcho(t, app, up, "bapp", []string{"*"})
	tok := sessionToken(t, base, "kim")

	for _, bad := range []string{
		"!!! not base64 !!!", // decode failure
		"YWJjZGVm",           // valid base64, wrong structure
		"MgB4ADA",            // version "2", not "1"
	} {
		_, call, _ := mcpCall(t, base, tok, "tools/list", map[string]any{"cursor": bad})
		assertCursorRejected(t, call)
	}
}

func assertCursorRejected(t *testing.T, call map[string]any) {
	t.Helper()
	errObj, _ := call["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("bad cursor was accepted: %v", call)
	}
	if code, _ := errObj["code"].(float64); int(code) != -32602 {
		t.Errorf("bad cursor error code = %v, want -32602", errObj["code"])
	}
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "invalid or stale cursor: re-issue tools/list from the start") {
		t.Errorf("bad cursor message = %q, want the contract message", errObj["message"])
	}
}

// TestGatewayToolsListPaginationHidesDenied: policy hiding is applied to the full
// catalog BEFORE pagination, so a denied tool never appears on any page and the
// page boundaries reflect the filtered length.
func TestGatewayToolsListPaginationHidesDenied(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t, func(c *config.Config) {
		c.Apps.Catalog.PolicyFilter = true
		c.Apps.Catalog.PageSize = 2
	})
	up := startGatewayUpstream(t)
	seedGatewayUser(t, app, "kim", "dev")
	catInstallEcho(t, app, up, "aapp", []string{"*"})
	catInstallEcho(t, app, up, "bapp", []string{"*"})
	catActivate(t, app, "deny-benv", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: deny-benv}
spec:
  match: {roles: [dev]}
  rules:
    - id: deny-bapp-env
      tools: [mcp.call]
      apps: [bapp]
      toolNames: {deny: ["env"]}
      effect: deny
      reason: "bapp env blocked"
    - id: allow-rest
      tools: [mcp.call]
      apps: [aapp, bapp]
      toolNames: {allow: ["echo", "env"]}
      effect: allow
`)
	tok := sessionToken(t, base, "kim")

	// bapp__env is hidden: 3 visible tools over 2 pages, env never listed.
	want := []string{"aapp__echo", "aapp__env", "bapp__echo"}
	got, pages := walkToolsList(t, base, tok, 2)
	if pages != 2 {
		t.Errorf("walked %d pages, want 2", pages)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("paged filtered catalog = %v, want %v (bapp__env hidden)", got, want)
	}
	for _, n := range got {
		if n == "bapp__env" {
			t.Error("policy-denied bapp__env leaked into a page")
		}
	}
}

// TestGatewaySetBindingsTargetedInvalidation: a binding change scoped to one role
// drops only that role's cached catalog; an unrelated role's catalog survives.
// An empty diff (identical bindings) falls back to a full flush.
func TestGatewaySetBindingsTargetedInvalidation(t *testing.T) {
	t.Parallel()
	app, _, _ := testAppCounting(t)
	g := app.gateway

	g.SetBindings([]gwBinding{
		{App: "x", Role: "A", Matchers: []string{"*"}},
		{App: "y", Role: "B", Matchers: []string{"*"}},
	})
	a1 := app.catalogFor([]string{"A"})
	b1 := app.catalogFor([]string{"B"})

	// Change only role A's matchers.
	g.SetBindings([]gwBinding{
		{App: "x", Role: "A", Matchers: []string{"echo"}},
		{App: "y", Role: "B", Matchers: []string{"*"}},
	})
	a2 := app.catalogFor([]string{"A"})
	b2 := app.catalogFor([]string{"B"})
	if a2.id == a1.id {
		t.Errorf("role A catalog survived its own binding change (id still %d)", a1.id)
	}
	if b2.id != b1.id {
		t.Errorf("role B catalog was dropped by an unrelated role's change (%d → %d)", b1.id, b2.id)
	}

	// Identical bindings → empty diff → full flush drops even the untouched role.
	before := app.catalogFor([]string{"B"}).id
	g.SetBindings([]gwBinding{
		{App: "x", Role: "A", Matchers: []string{"echo"}},
		{App: "y", Role: "B", Matchers: []string{"*"}},
	})
	if after := app.catalogFor([]string{"B"}).id; after == before {
		t.Errorf("empty-diff SetBindings did not full-flush (role B id unchanged at %d)", before)
	}
}

// TestGatewayOverlayRebuiltOnTier1Change: folding the tier-1 build id into the
// overlay key means a rebuilt tier-1 forces a fresh overlay even when the
// subject, epoch, and snapshot are unchanged; the tier-1 id is the only key
// component that differs here.
func TestGatewayOverlayRebuiltOnTier1Change(t *testing.T) {
	t.Parallel()
	app, _, _ := testAppCounting(t)
	g := app.gateway
	g.SetBindings([]gwBinding{{App: "echoapp", Role: "dev", Matchers: []string{"*"}}})

	sub := policy.Subject{User: "kim", Roles: []string{"dev"}}
	const sid = "sess-1"

	t1a := app.catalogFor(sub.Roles)
	ov1 := app.overlayFor(sid, sub, t1a)

	// Drop role dev's tier-1 (targeted, no epoch/snapshot change) so the next
	// build gets a fresh id.
	g.SetBindings([]gwBinding{{App: "echoapp", Role: "dev", Matchers: []string{"echo"}}})
	t1b := app.catalogFor(sub.Roles)
	if t1b.id == t1a.id {
		t.Fatal("tier-1 catalog was not rebuilt")
	}
	ov2 := app.overlayFor(sid, sub, t1b)
	if ov2 == ov1 {
		t.Error("overlay was reused across a tier-1 rebuild (stale-probe risk)")
	}
	if ov2.key == ov1.key {
		t.Errorf("overlay key unchanged across a tier-1 rebuild: %q", ov1.key)
	}
}

// TestGatewayNotifyCoalesces: three invalidations inside one debounce window
// deliver exactly one list_changed on a live SSE stream.
func TestGatewayNotifyCoalesces(t *testing.T) {
	// Serial: its 150 ms debounce window flakes under the load of parallel servers.
	app, base, _ := testAppCounting(t)
	seedGatewayUser(t, app, "kim", "dev")
	tok := sessionToken(t, base, "kim")

	// testAppCounting makes notifications synchronous; re-arm a debounce window.
	app.gateway.notify.SetDelay(150 * time.Millisecond)

	ch, resp := catOpenStream(t, base, tok)
	defer func() { _ = resp.Body.Close() }()

	app.gateway.Invalidate()
	app.gateway.Invalidate()
	app.gateway.Invalidate()

	count := 0
	deadline := time.After(700 * time.Millisecond)
	for done := false; !done; {
		select {
		case line := <-ch:
			if strings.Contains(line, "notifications/tools/list_changed") {
				count++
			}
		case <-deadline:
			done = true
		}
	}
	if count != 1 {
		t.Errorf("coalesced broadcasts delivered %d list_changed, want 1", count)
	}
}

// TestGatewayAuthAnswersActionablyOnLostSubject pins the recovery contract
// the stdio proxy's revival rides: when the cached subject for a
// still-valid token is gone (strazad restart, idle eviction), BOTH gateway
// lanes answer 401 with the check-in-again reason: the POST lane and the SSE
// GET lane the streamable client reconnects on. The proxy's auth transport
// re-checks in on that 401 and the revived session continues; an ambiguous
// answer here would strand every idle governed session on a deploy.
func TestGatewayAuthAnswersActionablyOnLostSubject(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t)
	seedGatewayUser(t, app, "bob", "dev")
	idToken := loginDeviceFlow(t, base, "bob", "hunter2!")
	code, body := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    idToken,
		"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "x"}},
	})
	if code != http.StatusOK {
		t.Fatalf("checkin = %d: %v", code, body)
	}
	tok, _ := body["session_token"].(string)
	sessionID, _ := body["session_id"].(string)
	if tok == "" || sessionID == "" {
		t.Fatalf("checkin payload missing token or session: %v", body)
	}

	// The restart shape: token still verifies, subject cache holds nothing.
	app.subjects.drop(sessionID)

	// POST lane.
	status, decoded, _ := mcpCall(t, base, tok, "ping", nil)
	if status != http.StatusUnauthorized || !strings.Contains(decoded["error"].(string), "Check in again") {
		t.Errorf("POST after subject loss = %d %v, want 401 check-in-again", status, decoded)
	}

	// SSE GET lane (what the streamable client's reconnect loop sees).
	req, _ := http.NewRequest(http.MethodGet, base+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(raw), "Check in again") {
		t.Errorf("SSE GET after subject loss = %d %q, want 401 check-in-again", resp.StatusCode, raw)
	}
}
