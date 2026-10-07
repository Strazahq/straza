package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestCatalogPreviewHints pins the plain-sentence hint on every preview row,
// including the two approve shapes (a hold names its wait, a ticket names its
// decision and consume windows), the note for a role that does not exist, the
// role lane's implication closure, and the one not_running row a bound server
// with no known tools yields.
func TestCatalogPreviewHints(t *testing.T) {
	t.Parallel()
	app, _, _ := testAppCounting(t)
	up := startGatewayUpstream(t)
	ctx := context.Background()
	seedGatewayUser(t, app, "kim", "dev")

	// hintapp: only echo bound, no rule. gateapp: whole set bound, echo
	// gated, env refused. openapp: running, unbound.
	catInstallEcho(t, app, up, "hintapp", []string{"echo"})
	catInstallEcho(t, app, up, "gateapp", []string{"*"})
	catInstallEcho(t, app, up, "openapp", nil)
	// ticketapp: whole set bound, echo on a ticket rule that sets both
	// windows, env on one that omits them and takes the compiled defaults.
	catInstallEcho(t, app, up, "ticketapp", []string{"*"})
	catActivate(t, app, "hint-gates", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: hint-gates}
spec:
  match: {roles: [dev]}
  rules:
    - id: gateapp-echo-approve
      tools: [mcp.call]
      apps: [gateapp]
      toolNames: {allow: ["echo"]}
      effect: allow
      mode: approve
      approve: {deciders: [sponsor], timeoutSeconds: 45}
    - id: gateapp-env-deny
      tools: [mcp.call]
      apps: [gateapp]
      toolNames: {deny: ["env"]}
      effect: deny
      reason: "env dumps the environment"
    - id: ticketapp-echo-ticket
      tools: [mcp.call]
      apps: [ticketapp]
      toolNames: {allow: ["echo"]}
      effect: allow
      mode: approve
      approve: {class: ticket, ticketTTLSeconds: 172800, grantTTLSeconds: 7200}
    - id: ticketapp-env-ticket
      tools: [mcp.call]
      apps: [ticketapp]
      toolNames: {allow: ["env"]}
      effect: allow
      mode: approve
      approve: {class: ticket}
`)
	// deadapp: bound, but nothing answers at its address, so it settles
	// degraded with no known tools.
	deadMF, err := managerParse(t, `
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: deadapp}
server: {name: straza.test/deadapp, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: "http://127.0.0.1:9/mcp"}
`)
	if err != nil {
		t.Fatal(err)
	}
	dead, err := app.manager.Install(ctx, deadMF, store.AppSourceAPI)
	if err != nil {
		t.Fatal(err)
	}
	devRole, err := app.store.Roles().GetByName(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: devRole.ID, AppID: dead.ID, ToolMatcher: `["*"]`}); err != nil {
		t.Fatal(err)
	}
	// biz composes dev, so the role lane asked for biz sees what dev reaches.
	biz, err := app.store.Roles().Create(ctx, store.Role{Name: "biz", Kind: store.RoleKindBusiness})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.Roles().AddImplication(ctx, biz.ID, devRole.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if v, ok := app.manager.View("deadapp"); ok && v.Status == "degraded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deadapp never settled degraded")
		}
		time.Sleep(20 * time.Millisecond)
	}

	get := func(target string) catalogPreviewResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		app.handleCatalogPreview(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("preview %s = %d: %s", target, rec.Code, rec.Body)
		}
		var resp catalogPreviewResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	rows := func(resp catalogPreviewResponse) map[string]previewEntry {
		m := map[string]previewEntry{}
		for _, e := range resp.Entries {
			key := e.App
			if e.Tool != "" {
				key += "/" + e.Tool
			}
			m[key] = e
		}
		return m
	}

	resp := get("/v1/admin/catalog/preview?role=dev")
	if len(resp.Notes) != 0 {
		t.Errorf("notes for an existing role = %v, want none", resp.Notes)
	}
	got := rows(resp)
	cases := []struct {
		key, status, hint string
	}{
		{"hintapp/echo", previewVisible, "has access, no policy gates it"},
		{"hintapp/env", previewMatcherMiss, "not in the role's access row on this server"},
		{"gateapp/echo", previewApproveGated, "has access; hint-gates, rule gateapp-echo-approve, holds it for the person behind the agent up to 45 s"},
		{"gateapp/env", previewHiddenPolicy, "env dumps the environment"},
		{"ticketapp/echo", previewApproveGated, "has access; hint-gates, rule ticketapp-echo-ticket, the first call raises a ticket for the person behind the agent to decide within 48 hours, and the approval is good for 2 hours"},
		{"ticketapp/env", previewApproveGated, "has access; hint-gates, rule ticketapp-env-ticket, the first call raises a ticket for the person behind the agent to decide within 24 hours, and the approval is good for 1 hour"},
		{"openapp", previewNoBinding, "no role of this subject has access to this server"},
	}
	for _, tc := range cases {
		e, ok := got[tc.key]
		if !ok || e.Status != tc.status || e.Hint != tc.hint {
			t.Errorf("%s = %+v, want status %s hint %q", tc.key, e, tc.status, tc.hint)
		}
	}
	dead0, ok := got["deadapp"]
	if !ok || dead0.Status != previewNotRunning || !strings.HasPrefix(dead0.Hint, "has access, but the server is degraded: ") {
		t.Errorf("deadapp = %+v, want one not_running row with the probe reason", dead0)
	}
	deadRows := 0
	for _, e := range resp.Entries {
		if e.App == "deadapp" {
			deadRows++
		}
	}
	if deadRows != 1 {
		t.Errorf("deadapp rows = %d, want exactly one", deadRows)
	}

	// Closure: the business role reaches dev's grants and the subject echoes both.
	resp = get("/v1/admin/catalog/preview?role=biz&app=hintapp")
	if !containsStr(resp.Subject.Roles, "dev") || !containsStr(resp.Subject.Roles, "biz") {
		t.Errorf("subject roles = %v, want biz and dev (implication closure)", resp.Subject.Roles)
	}
	if e := rows(resp)["hintapp/echo"]; e.Status != previewVisible {
		t.Errorf("biz lane hintapp/echo = %+v, want visible through dev", e)
	}

	// A role that does not exist gets a note and a hypothetical subject.
	resp = get("/v1/admin/catalog/preview?role=scout-rol&app=hintapp")
	if len(resp.Notes) != 1 || resp.Notes[0] != "no role named scout-rol exists; this preview is for a hypothetical subject." {
		t.Errorf("notes = %v", resp.Notes)
	}
	if e := rows(resp)["hintapp"]; e.Status != previewNoBinding {
		t.Errorf("hypothetical subject row = %+v, want no_binding", e)
	}
}
