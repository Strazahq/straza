package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// startEchoUpstream serves a real MCP server over streamable HTTP for remote
// runtime tests.
func startEchoUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "upstream", Version: "1.0.0"}, nil)
	type echoArgs struct {
		Text string `json:"text"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "echo"},
		func(_ context.Context, _ *mcp.CallToolRequest, a echoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + a.Text}}}, nil, nil
		})
	up := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(up.Close)
	return up
}

func echoManifest(upstreamURL string) string {
	return fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: echoapp}
server: {name: straza.test/echo, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
`, upstreamURL)
}

// rawReq sends a non-JSON body (e.g. YAML manifests) to the admin API.
func rawReq(t *testing.T, method, urlStr, bearer, contentType string, body []byte, out any) int {
	t.Helper()
	req, err := http.NewRequest(method, urlStr, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s %s: %v", method, urlStr, err)
		}
	}
	return resp.StatusCode
}

// TestAppsAdminAPI drives install → running → logs → remove over the admin
// surface.
func TestAppsAdminAPI(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	up := startEchoUpstream(t)

	// Install: invalid manifest is rejected with the validator's reason.
	if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte("kind: Nope"), nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid manifest = %d, want 422", code)
	}

	var installed appPayload
	code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(echoManifest(up.URL)), &installed)
	if code != http.StatusCreated || installed.Name != "echoapp" {
		t.Fatalf("install = %d %+v", code, installed)
	}

	deadline := time.Now().Add(10 * time.Second)
	var apps []appPayload
	for {
		apps = nil
		if code := adminReq(t, "GET", base+"/v1/admin/apps", bearer, nil, &apps); code != http.StatusOK {
			t.Fatalf("list apps = %d", code)
		}
		if len(apps) == 1 && apps[0].Status == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("app never reached running: %+v", apps)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(apps[0].Tools) != 1 || apps[0].Tools[0] != "echo" {
		t.Errorf("tools = %v", apps[0].Tools)
	}
	// A running app has been probed and found healthy (health timestamps).
	probedAt, err := time.Parse(time.RFC3339, apps[0].LastProbeAt)
	if err != nil {
		t.Fatalf("last_probe_at %q: %v", apps[0].LastProbeAt, err)
	}
	if _, err := time.Parse(time.RFC3339, apps[0].LastHealthyAt); err != nil {
		t.Errorf("last_healthy_at %q: %v", apps[0].LastHealthyAt, err)
	}
	// The status word carries the time it last changed (0.94.0), so the
	// console can say how long a server has read this way.
	if since, err := time.Parse(time.RFC3339, apps[0].StatusSince); err != nil || since.After(time.Now()) {
		t.Errorf("status_since %q: %v", apps[0].StatusSince, err)
	}

	// The flat tool list carries the upstream description, so the IGA side
	// sees real entitlement text.
	var tools []toolPayload
	if code := adminReq(t, "GET", base+"/v1/admin/tools", bearer, nil, &tools); code != http.StatusOK {
		t.Fatalf("list tools = %d", code)
	}
	if len(tools) != 1 || tools[0].ID != "echoapp:echo" || tools[0].App != "echoapp" ||
		tools[0].Name != "echo" || tools[0].Description != "echo" || tools[0].AppID != installed.ID {
		t.Errorf("tools list = %+v", tools)
	}

	// On-demand recheck: re-probes now and returns the refreshed state.
	var rechecked appPayload
	if code := adminReq(t, "POST", base+"/v1/admin/apps/echoapp/health", bearer, nil, &rechecked); code != http.StatusOK {
		t.Fatalf("recheck = %d", code)
	}
	reprobedAt, err := time.Parse(time.RFC3339, rechecked.LastProbeAt)
	if err != nil || rechecked.Status != "running" {
		t.Fatalf("recheck payload = %+v err=%v", rechecked, err)
	}
	if reprobedAt.Before(probedAt) {
		t.Errorf("recheck did not advance last_probe_at: %v -> %v", probedAt, reprobedAt)
	}
	// The recheck is an admin action the server's Events list shows, so it
	// lands in the audit stream exactly once, naming the app and the status
	// the check found.
	var rechecks []map[string]any
	for _, ev := range adminAuditEvents(t, app) {
		if ev["action"] == "apps.recheck" {
			rechecks = append(rechecks, ev)
		}
	}
	if len(rechecks) != 1 || rechecks[0]["app"] != "echoapp" || rechecks[0]["status"] != "running" {
		t.Errorf("apps.recheck audit records = %v, want exactly one for echoapp running", rechecks)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/apps/nope/health", bearer, nil, nil); code != http.StatusNotFound {
		t.Errorf("recheck unknown app = %d, want 404", code)
	}

	var logs struct {
		Lines   []string          `json:"lines"`
		Entries []logEntryPayload `json:"entries"`
	}
	if code := adminReq(t, "GET", base+"/v1/admin/apps/echoapp/logs", bearer, nil, &logs); code != http.StatusOK {
		t.Fatalf("logs = %d", code)
	}
	if !strings.Contains(strings.Join(logs.Lines, "\n"), "connected") {
		t.Errorf("logs = %v", logs.Lines)
	}
	// entries (0.94.0) carries the same lines with the receive time the
	// console's gutter shows; lines stays for the older shape.
	if len(logs.Entries) != len(logs.Lines) {
		t.Fatalf("entries = %d, lines = %d, want the same count", len(logs.Entries), len(logs.Lines))
	}
	for i, e := range logs.Entries {
		if e.Line != logs.Lines[i] {
			t.Errorf("entry %d line = %q, lines[%d] = %q", i, e.Line, i, logs.Lines[i])
		}
		if _, err := time.Parse(time.RFC3339Nano, e.At); err != nil {
			t.Errorf("entry %d t %q: %v", i, e.At, err)
		}
	}

	if code := adminReq(t, "DELETE", base+"/v1/admin/apps/"+installed.ID, bearer, nil, nil); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}
	apps = nil
	if code := adminReq(t, "GET", base+"/v1/admin/apps", bearer, nil, &apps); code != http.StatusOK {
		t.Fatalf("list after delete = %d", code)
	}
	if len(apps) != 0 {
		t.Errorf("after delete: %+v, want the row gone", apps)
	}
	// A removed app has no row to probe.
	if code := adminReq(t, "POST", base+"/v1/admin/apps/echoapp/health", bearer, nil, nil); code != http.StatusNotFound {
		t.Errorf("recheck removed app = %d, want 404", code)
	}
	// …and its tools leave the flat list (fail-closed truth).
	var afterTools []toolPayload
	if code := adminReq(t, "GET", base+"/v1/admin/tools", bearer, nil, &afterTools); code != http.StatusOK || len(afterTools) != 0 {
		t.Errorf("tools after remove = %d %+v, want empty", code, afterTools)
	}

	// The admin surface requires the admin role.
	if code := adminReq(t, "GET", base+"/v1/admin/apps", "", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated list = %d, want 401", code)
	}
}

// waitAppStatus polls the admin apps list until the named app reaches want
// (or fails the test after 10s).
func waitAppStatus(t *testing.T, base, bearer, name, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var apps []appPayload
		if code := adminReq(t, "GET", base+"/v1/admin/apps", bearer, nil, &apps); code != http.StatusOK {
			t.Fatalf("list apps = %d", code)
		}
		for _, a := range apps {
			if a.Name == name && a.Status == want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("app %q never reached %q: %+v", name, want, apps)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestAppsEnableDisableAPI drives the per-app admin enable/disable pause:
// disable stops an app and marks it paused; enable clears the
// pause and brings it back. Covers the admin gate (401/403), unknown-app 404,
// disable-then-list showing stopped, and the idempotent happy paths.
func TestAppsEnableDisableAPI(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	up := startEchoUpstream(t)

	// Admin-gated: no token → 401, authenticated non-admin → 403. The gate
	// runs before the handler, so the app need not exist for these.
	if code := adminReq(t, "POST", base+"/v1/admin/apps/whatever/disable", "", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated disable = %d, want 401", code)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/apps/whatever/enable", bearer, nil, nil); code != http.StatusForbidden {
		t.Errorf("non-admin enable = %d, want 403", code)
	}

	grantAdmin(t, app, user.ID)

	// Install and wait for the app to come up healthy.
	var installed appPayload
	if code := rawReq(t, "POST", base+"/v1/admin/apps", bearer, "application/yaml", []byte(echoManifest(up.URL)), &installed); code != http.StatusCreated {
		t.Fatalf("install = %d", code)
	}
	waitAppStatus(t, base, bearer, "echoapp", "running")

	// Unknown app → 404 on both verbs.
	if code := adminReq(t, "POST", base+"/v1/admin/apps/nope/disable", bearer, nil, nil); code != http.StatusNotFound {
		t.Errorf("disable unknown = %d, want 404", code)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/apps/nope/enable", bearer, nil, nil); code != http.StatusNotFound {
		t.Errorf("enable unknown = %d, want 404", code)
	}

	// Disable: the app stops and is marked paused. Resolve by id here to
	// exercise the shared {id} resolution the health endpoint uses.
	var disabled appPayload
	if code := adminReq(t, "POST", base+"/v1/admin/apps/"+installed.ID+"/disable", bearer, nil, &disabled); code != http.StatusOK {
		t.Fatalf("disable = %d", code)
	}
	if disabled.Status != "stopped" || !disabled.Paused {
		t.Errorf("disabled view = %+v, want stopped+paused", disabled)
	}

	// …and the list agrees the app is stopped.
	var apps []appPayload
	if code := adminReq(t, "GET", base+"/v1/admin/apps", bearer, nil, &apps); code != http.StatusOK {
		t.Fatalf("list after disable = %d", code)
	}
	if len(apps) != 1 || apps[0].Status != "stopped" {
		t.Errorf("after disable: %+v, want one stopped app", apps)
	}

	// Disabling an already-stopped app is idempotent: still 200, still paused.
	if code := adminReq(t, "POST", base+"/v1/admin/apps/echoapp/disable", bearer, nil, &disabled); code != http.StatusOK || !disabled.Paused {
		t.Errorf("idempotent disable = %d %+v", code, disabled)
	}

	// Enable: the pause clears and the app comes back (starting → running).
	var enabled appPayload
	if code := adminReq(t, "POST", base+"/v1/admin/apps/echoapp/enable", bearer, nil, &enabled); code != http.StatusOK {
		t.Fatalf("enable = %d", code)
	}
	if enabled.Paused {
		t.Errorf("enabled view still paused: %+v", enabled)
	}
	if enabled.Status != "running" && enabled.Status != "starting" {
		t.Errorf("enabled status = %q, want running/starting", enabled.Status)
	}
	waitAppStatus(t, base, bearer, "echoapp", "running")

	// Enabling an already-running app is idempotent.
	if code := adminReq(t, "POST", base+"/v1/admin/apps/echoapp/enable", bearer, nil, &enabled); code != http.StatusOK || enabled.Paused {
		t.Errorf("idempotent enable = %d %+v", code, enabled)
	}
}
