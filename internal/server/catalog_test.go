package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// ---- shared catalog-test helpers ----

// catReach gives roleName reach into row through an application role of the
// app's own, reach-<app>, bound with matcher and implied by roleName. An
// application role reaches one server, so a role that reaches several
// composes one role per server, which is what the fixtures model here.
func catReach(t *testing.T, app *App, roleName string, row store.App, matcher string) store.ToolBinding {
	t.Helper()
	ctx := context.Background()
	role, err := app.store.Roles().GetByName(ctx, roleName)
	if err != nil {
		t.Fatal(err)
	}
	reach, err := app.store.Roles().Create(ctx, store.Role{Name: "reach-" + row.Name, Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.Roles().AddImplication(ctx, role.ID, reach.ID); err != nil {
		t.Fatal(err)
	}
	b, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{
		RoleID: reach.ID, AppID: row.ID, ToolMatcher: matcher,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	return b
}

// catInstallEcho installs a remote app pointing at the shared echo upstream,
// optionally gives role dev reach into it with matchers through a role of the
// app's own (nil = no reach), and waits for it to reach running.
func catInstallEcho(t *testing.T, app *App, up *gatewayUpstream, name string, matchers []string) store.App {
	t.Helper()
	ctx := context.Background()
	mf, err := managerParse(t, fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: %s}
server: {name: straza.test/%s, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
`, name, name, up.URL))
	if err != nil {
		t.Fatal(err)
	}
	row, err := app.manager.Install(ctx, mf, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	if matchers != nil {
		m, _ := json.Marshal(matchers)
		catReach(t, app, "dev", row, string(m))
	}
	catWaitRunning(t, app, name)
	return row
}

func catWaitRunning(t *testing.T, app *App, name string) {
	t.Helper()
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

// catCreatePolicy stores an active PolicySet without recompiling; callers that
// activate several sets recompile once at the end.
func catCreatePolicy(t *testing.T, app *App, name, yamlSrc string) store.PolicySet {
	t.Helper()
	ps, err := app.store.Policies().Create(context.Background(), store.PolicySet{
		Name: name, Status: "active", YAMLSource: yamlSrc,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func catRecompile(t *testing.T, app *App) {
	t.Helper()
	if _, err := app.snapshots.Recompile(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// catActivate stores an active PolicySet and recompiles in one step.
func catActivate(t *testing.T, app *App, name, yamlSrc string) store.PolicySet {
	t.Helper()
	ps := catCreatePolicy(t, app, name, yamlSrc)
	catRecompile(t, app)
	return ps
}

// ---- (a)/(b) policyFilter on/off ----

// setupDenyEnv boots an app whose dev role is bound to echoapp's whole tool set,
// with a policy that denies env and allows echo, and returns a checked-in kim
// token. policyFilter selects tier-2 hiding behavior.
func setupDenyEnv(t *testing.T, policyFilter bool) (*App, string, string) {
	t.Helper()
	app, base, _ := testAppCounting(t, func(c *config.Config) { c.Apps.Catalog.PolicyFilter = policyFilter })
	up := startGatewayUpstream(t)
	seedGatewayUser(t, app, "kim", "dev")
	catInstallEcho(t, app, up, "echoapp", []string{"*"})
	catActivate(t, app, "mcp-dev", `
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
`)
	return app, base, sessionToken(t, base, "kim")
}

// TestCatalogPolicyFilterOn: with policyFilter enabled a policy-denied tool is
// absent from tools/list AND tools/call answers -32602 unknown tool (hidden =
// nonexistent, no information leak).
func TestCatalogPolicyFilterOn(t *testing.T) {
	t.Parallel()
	app, base, tok := setupDenyEnv(t, true)
	_ = app

	names := toolNamesOf(t, second(mcpCall(t, base, tok, "tools/list", nil)))
	if len(names) != 1 || names[0] != "echoapp__echo" {
		t.Fatalf("filtered catalog = %v, want [echoapp__echo] only (env hidden by policy)", names)
	}
	_, call, _ := mcpCall(t, base, tok, "tools/call", map[string]any{"name": "echoapp__env"})
	errObj, _ := call["error"].(map[string]any)
	if errObj == nil || !strings.Contains(errObj["message"].(string), "unknown tool") {
		t.Errorf("hidden tool call = %v, want -32602 unknown tool", call)
	}
	// The allowed tool still works.
	if _, _, raw := mcpCall(t, base, tok, "tools/call", map[string]any{
		"name": "echoapp__echo", "arguments": map[string]string{"text": "hi"},
	}); !strings.Contains(string(raw), "echo: hi") {
		t.Errorf("allowed tool not served: %s", raw)
	}
}

// TestCatalogPolicyFilterOff: with policyFilter disabled the same denied tool is
// LISTED and the call returns the Straza deny reason. Justification
// injection is independent of this knob.
func TestCatalogPolicyFilterOff(t *testing.T) {
	t.Parallel()
	app, base, tok := setupDenyEnv(t, false)
	_ = app

	names := toolNamesOf(t, second(mcpCall(t, base, tok, "tools/list", nil)))
	if len(names) != 2 {
		t.Fatalf("unfiltered catalog = %v, want both echo and env listed", names)
	}
	_, _, raw := mcpCall(t, base, tok, "tools/call", map[string]any{"name": "echoapp__env"})
	if !strings.Contains(string(raw), "env reads are blocked for role dev") {
		t.Errorf("deny call = %s, want the Straza rule reason", raw)
	}
	if strings.Contains(string(raw), "unknown tool") {
		t.Error("policyFilter off must surface the deny reason, not hide the tool")
	}
}

// TestOverlayUserScopedApprove pins user-scoped approve gating and the
// plain allow. A mode:approve rule scoped by match.users to the session's
// user gates echo, so tools/list carries the required _straza_justification
// field; a plain allow rule on env leaves its schema untouched.
//
// A roles-only probe (Subject{Roles: roles} with an empty User) would never
// apply a match.users set, so the tier-2 overlay probes with the session's
// FULL subject (User = "kim").
func TestOverlayUserScopedApprove(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t)
	up := startGatewayUpstream(t)
	seedGatewayUser(t, app, "kim", "dev")
	catInstallEcho(t, app, up, "echoapp", []string{"*"})
	catActivate(t, app, "mcp-user-approve", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: mcp-user-approve}
spec:
  match: {users: [kim]}
  rules:
    - id: approve-echo
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {allow: ["echo"]}
      effect: allow
      mode: approve
      approve: {roles: [sec-approvers], timeoutSeconds: 60}
    - id: allow-env
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {allow: ["env"]}
      effect: allow
`)

	tok := sessionToken(t, base, "kim")
	schemas := schemasByName(t, second(mcpCall(t, base, tok, "tools/list", nil)))

	echo := schemas["echoapp__echo"]
	if echo == nil {
		t.Fatalf("echo missing: %v", schemas)
	}
	if props, _ := echo["properties"].(map[string]any); props[justificationField] == nil {
		t.Errorf("user-scoped approve did not inject %s (T18b regression): %v", justificationField, echo)
	}
	if !hasRequired(echo, justificationField) {
		t.Errorf("user-scoped approve schema does not require %s: %v", justificationField, echo["required"])
	}

	env := schemas["echoapp__env"]
	if env == nil {
		t.Fatalf("env missing: %v", schemas)
	}
	if props, _ := env["properties"].(map[string]any); props[justificationField] != nil {
		t.Errorf("plain-allow env schema was injected: %v", env)
	}
	if hasRequired(env, justificationField) {
		t.Errorf("plain-allow env schema requires %s: %v", justificationField, env["required"])
	}
}

// TestOverlayInvalidation: activating a mode:approve policy makes gating
// APPEAR on a live session's next tools/list, and deleting it makes gating
// DISAPPEAR: the tier-2 overlay is keyed on the active snapshot id, so a
// recompile rebuilds it.
func TestOverlayInvalidation(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t)
	up := startGatewayUpstream(t)
	seedGatewayUser(t, app, "kim", "dev")
	catInstallEcho(t, app, up, "echoapp", []string{"*"})
	tok := sessionToken(t, base, "kim")

	// No policy yet: default allow, no gate → raw schema.
	echo := schemasByName(t, second(mcpCall(t, base, tok, "tools/list", nil)))["echoapp__echo"]
	if hasRequired(echo, justificationField) {
		t.Fatalf("echo gated before any approve policy: %v", echo)
	}

	ps := catActivate(t, app, "mcp-approve", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: mcp-approve}
spec:
  match: {roles: [dev]}
  rules:
    - id: approve-echo
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {allow: ["echo"]}
      effect: allow
      mode: approve
      approve: {roles: [sec-approvers], timeoutSeconds: 60}
`)

	echo = schemasByName(t, second(mcpCall(t, base, tok, "tools/list", nil)))["echoapp__echo"]
	if !hasRequired(echo, justificationField) {
		t.Errorf("approve gating did not appear after activation: %v", echo)
	}

	// Remove the approve set and recompile: gating disappears.
	if err := app.store.Policies().Delete(context.Background(), ps.ID); err != nil {
		t.Fatal(err)
	}
	catRecompile(t, app)
	echo = schemasByName(t, second(mcpCall(t, base, tok, "tools/list", nil)))["echoapp__echo"]
	if hasRequired(echo, justificationField) {
		t.Errorf("approve gating did not disappear after deactivation: %v", echo)
	}
}

// TestCatalogOversizeWarning: a small WarnSize trips the oversize counter once
// per role build (builds are cached, so it is naturally rate-limited).
func TestCatalogOversizeWarning(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t)
	app.cfg.Apps.Catalog.WarnSize = 1 // echoapp exposes 2 tools → over the limit
	up := startGatewayUpstream(t)
	seedGatewayUser(t, app, "kim", "dev")
	catInstallEcho(t, app, up, "echoapp", []string{"*"})
	tok := sessionToken(t, base, "kim")

	const metric = "straza_gateway_catalog_oversize_total"
	before := catMetric(t, base, metric)
	if n := len(toolNamesOf(t, second(mcpCall(t, base, tok, "tools/list", nil)))); n != 2 {
		t.Fatalf("catalog = %d tools, want 2", n)
	}
	// A second identical list hits the tier-1 cache: no rebuild, no re-warn.
	mcpCall(t, base, tok, "tools/list", nil)
	if got := catMetric(t, base, metric) - before; got != 1 {
		t.Errorf("oversize counter delta = %v, want exactly 1 (cached builds must not re-warn)", got)
	}
}

// catMetric scrapes one unlabeled counter from the /metrics endpoint.
func catMetric(t *testing.T, base, name string) float64 {
	t.Helper()
	resp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, name+" ") {
			var v float64
			if _, err := fmt.Sscanf(strings.TrimPrefix(line, name+" "), "%g", &v); err == nil {
				return v
			}
		}
	}
	return 0
}

// TestOverlayRoleChangeNudge: a refresh check-in that re-resolves DIFFERENT
// roles nudges exactly that session's live SSE stream (list_changed); an
// unrelated session's stream receives nothing; a subsequent unchanged check-in
// nudges no one.
func TestOverlayRoleChangeNudge(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t)
	up := startGatewayUpstream(t)
	kim := seedGatewayUser(t, app, "kim", "dev")
	seedGatewayUser(t, app, "lee", "dev")
	catInstallEcho(t, app, up, "echoapp", []string{"*"})

	kimTok := sessionToken(t, base, "kim")
	leeTok := sessionToken(t, base, "lee")

	kimCh, kimResp := catOpenStream(t, base, kimTok)
	defer func() { _ = kimResp.Body.Close() }()
	leeCh, leeResp := catOpenStream(t, base, leeTok)
	defer func() { _ = leeResp.Body.Close() }()

	// Roles change under kim's session, then a refresh re-resolves them.
	catAssignRole(t, app, kim.ID, "extra")
	if code, body := catRefreshCheckin(t, base, kimTok); code != http.StatusOK {
		t.Fatalf("refresh check-in = %d: %v", code, body)
	}

	select {
	case line := <-kimCh:
		if !strings.Contains(line, "notifications/tools/list_changed") {
			t.Errorf("kim SSE line = %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("kim's stream did not receive list_changed after a role change")
	}
	select {
	case line := <-leeCh:
		t.Errorf("unrelated session's stream received a nudge: %q", line)
	case <-time.After(1500 * time.Millisecond):
		// good: the nudge is session-scoped, no cross-session leak
	}

	// Unchanged roles on the next refresh → no nudge.
	if code, body := catRefreshCheckin(t, base, kimTok); code != http.StatusOK {
		t.Fatalf("second refresh check-in = %d: %v", code, body)
	}
	select {
	case line := <-kimCh:
		t.Errorf("unchanged-role check-in still nudged: %q", line)
	case <-time.After(1500 * time.Millisecond):
		// good
	}
}

func catOpenStream(t *testing.T, base, token string) (<-chan string, *http.Response) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE stream = %d", resp.StatusCode)
	}
	ch := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
				ch <- line
			}
		}
	}()
	return ch, resp
}

func catAssignRole(t *testing.T, app *App, userID, roleName string) {
	t.Helper()
	ctx := context.Background()
	role, err := app.store.Roles().GetByName(ctx, roleName)
	if err != nil {
		role, err = app.store.Roles().Create(ctx, store.Role{Name: roleName})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.store.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: userID, RoleID: role.ID,
	}); err != nil {
		t.Fatal(err)
	}
	app.resolver.Bump()
}

func catRefreshCheckin(t *testing.T, base, sessionTok string) (int, map[string]any) {
	t.Helper()
	return postJSON(t, base+"/v1/checkin", map[string]any{
		"session_token": sessionTok,
		"harness":       map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation":   map[string]any{"managed": false, "hashes": map[string]string{"self": "x"}},
	})
}

// TestCatalogPreview: the admin preview endpoint reports every status in the
// taxonomy, including a user-lane approve rule scoped to a SECOND role the
// bare role lane is not given (the user lane resolves the full role set).
func TestCatalogPreview(t *testing.T) {
	t.Parallel()
	app, _, _ := testAppCounting(t)
	up := startGatewayUpstream(t)
	ctx := context.Background()
	kim := seedGatewayUser(t, app, "kim", "dev")

	// kim also holds role "sec": resolved in the user lane, absent from the
	// bare role-lane preview below, which is the flip this test pins.
	secRole, err := app.store.Roles().Create(ctx, store.Role{Name: "sec", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: kim.ID, RoleID: secRole.ID,
	}); err != nil {
		t.Fatal(err)
	}

	// echoapp: only echo bound (env misses the matcher).
	catInstallEcho(t, app, up, "echoapp", []string{"echo"})
	// gateapp: whole set bound; env denied, echo sec-role-approved.
	catInstallEcho(t, app, up, "gateapp", []string{"*"})
	// openapp: running, no binding at all.
	catInstallEcho(t, app, up, "openapp", nil)
	// ghostapp: a bound app row with no managed instance.
	ghost, err := app.store.Apps().Create(ctx, store.App{
		Name: "ghostapp", Version: "1.0.0", RuntimeKind: "remote", Status: "stopped",
	})
	if err != nil {
		t.Fatal(err)
	}
	devRole, err := app.store.Roles().GetByName(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{
		RoleID: devRole.ID, AppID: ghost.ID, ToolMatcher: `["*"]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}

	catCreatePolicy(t, app, "preview-role", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: preview-role}
spec:
  match: {roles: [dev]}
  rules:
    - id: allow-echoapp-echo
      tools: [mcp.call]
      apps: [echoapp]
      toolNames: {allow: ["echo"]}
      effect: allow
    - id: deny-gateapp-env
      tools: [mcp.call]
      apps: [gateapp]
      toolNames: {deny: ["env"]}
      effect: deny
      reason: "env blocked on gateapp"
`)
	catActivate(t, app, "preview-sec", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: preview-sec}
spec:
  match: {roles: [sec]}
  rules:
    - id: approve-gateapp-echo
      tools: [mcp.call]
      apps: [gateapp]
      toolNames: {allow: ["echo"]}
      effect: allow
      mode: approve
      approve: {roles: [sec-approvers], timeoutSeconds: 60}
`)

	// --- user lane (resolves kim's FULL role set, sec included) ---
	rec := httptest.NewRecorder()
	app.handleCatalogPreview(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/catalog/preview?user=kim", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("preview(user=kim) = %d: %s", rec.Code, rec.Body)
	}
	var resp catalogPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Subject.User != "kim" || !containsStr(resp.Subject.Roles, "sec") {
		t.Errorf("subject = %+v, want user kim with resolved role sec", resp.Subject)
	}
	got := catStatusMap(resp.Entries)
	want := map[string]string{
		"echoapp/echo": previewVisible,
		"echoapp/env":  previewMatcherMiss,
		"gateapp/echo": previewApproveGated, // sec-scoped approve applies in the user lane
		"gateapp/env":  previewHiddenPolicy,
		"openapp":      previewNoBinding,
		"ghostapp":     previewNotRunning,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("user-lane status[%s] = %q, want %q (all=%v)", k, got[k], v, got)
		}
	}
	// The hidden_policy row carries the deny reason.
	for _, e := range resp.Entries {
		if e.App == "gateapp" && e.Tool == "env" && !strings.Contains(e.Reason, "env blocked on gateapp") {
			t.Errorf("hidden_policy reason = %q, want the rule reason", e.Reason)
		}
	}

	// --- role lane (dev and the reach roles it implies, never kim's sec
	// role): the sec-scoped approve does NOT apply, and gateapp/echo is
	// bound, so with the sec approve rule absent it runs on the grant alone
	// and reads visible (spec/policyset revision 17), the exact flip kim's
	// resolved sec role causes (approve_gated in the user lane above).
	// echoapp/echo stays visible on its own allow rule. ---
	roleSub, _, err := app.roleLaneSubject(ctx, []string{"dev"})
	if err != nil {
		t.Fatal(err)
	}
	roleGot := catStatusMap(app.previewEntries(roleSub, "", nil))
	if roleGot["gateapp/echo"] != previewVisible {
		t.Errorf("role-lane gateapp/echo = %q, want visible (sec rule absent, granted with no rule)", roleGot["gateapp/echo"])
	}
	if roleGot["echoapp/echo"] != previewVisible {
		t.Errorf("role-lane echoapp/echo = %q, want visible", roleGot["echoapp/echo"])
	}

	// --- app filter narrows the result ---
	rec = httptest.NewRecorder()
	app.handleCatalogPreview(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/catalog/preview?user=kim&app=echoapp", nil))
	var filtered catalogPreviewResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &filtered)
	for _, e := range filtered.Entries {
		if e.App != "echoapp" {
			t.Errorf("app filter leaked app %q", e.App)
		}
	}

	// --- errors: neither role nor user → 400; unknown user → 404 ---
	rec = httptest.NewRecorder()
	app.handleCatalogPreview(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/catalog/preview", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("no params = %d, want 400", rec.Code)
	}
	rec = httptest.NewRecorder()
	app.handleCatalogPreview(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/catalog/preview?user=nobody", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown user = %d, want 404", rec.Code)
	}
}

func catStatusMap(entries []previewEntry) map[string]string {
	m := map[string]string{}
	for _, e := range entries {
		key := e.App
		if e.Tool != "" {
			key += "/" + e.Tool
		}
		m[key] = e.Status
	}
	return m
}
