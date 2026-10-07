package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCatalogPreviewAssume pins the draft-binding lane the role wizard rides:
// `assume` merges hypothetical tool matchers for the filtered app into the
// subject's bindings, so a role that does not exist yet (and binds nothing)
// still gets per-tool policy truth instead of one no_binding row.
func TestCatalogPreviewAssume(t *testing.T) {
	t.Parallel()
	app, _, _ := testAppCounting(t)
	up := startGatewayUpstream(t)

	// Running app, no binding at all: the wizard's mid-flight state.
	catInstallEcho(t, app, up, "draftapp", nil)

	// Policies matching the DRAFT role name: an approve gate on echo and a
	// deny on env. The role is never created; the engine matches names.
	catActivate(t, app, "assume-gates", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: assume-gates}
spec:
  match: {roles: [draft-support]}
  rules:
    - id: approve-draftapp-echo
      tools: [mcp.call]
      apps: [draftapp]
      toolNames: {allow: ["echo"]}
      effect: allow
      mode: approve
      approve: {roles: [sec-approvers], timeoutSeconds: 60}
    - id: deny-draftapp-env
      tools: [mcp.call]
      apps: [draftapp]
      toolNames: {deny: ["env"]}
      effect: deny
      reason: "env blocked for draft-support"
`)

	get := func(target string) (*httptest.ResponseRecorder, catalogPreviewResponse) {
		t.Helper()
		rec := httptest.NewRecorder()
		app.handleCatalogPreview(rec, httptest.NewRequest(http.MethodGet, target, nil))
		var resp catalogPreviewResponse
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
		}
		return rec, resp
	}

	// Without assume: the draft role has no binding, one app-level row.
	rec, resp := get("/v1/admin/catalog/preview?role=draft-support&app=draftapp")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview without assume = %d: %s", rec.Code, rec.Body)
	}
	got := catStatusMap(resp.Entries)
	if got["draftapp"] != previewNoBinding {
		t.Errorf("without assume: draftapp = %q, want %q (rows: %v)", got["draftapp"], previewNoBinding, got)
	}

	// With assume="*": per-tool policy truth for the hypothetical binding.
	rec, resp = get("/v1/admin/catalog/preview?role=draft-support&app=draftapp&assume=*")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview with assume = %d: %s", rec.Code, rec.Body)
	}
	got = catStatusMap(resp.Entries)
	if got["draftapp/echo"] != previewApproveGated {
		t.Errorf("assume=*: draftapp/echo = %q, want %q (rows: %v)", got["draftapp/echo"], previewApproveGated, got)
	}
	if got["draftapp/env"] != previewHiddenPolicy {
		t.Errorf("assume=*: draftapp/env = %q, want %q (rows: %v)", got["draftapp/env"], previewHiddenPolicy, got)
	}

	// Assumed matchers are a real matcher list: tools outside them miss.
	rec, resp = get("/v1/admin/catalog/preview?role=draft-support&app=draftapp&assume=echo")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview with assume=echo = %d: %s", rec.Code, rec.Body)
	}
	got = catStatusMap(resp.Entries)
	if got["draftapp/echo"] != previewApproveGated || got["draftapp/env"] != previewMatcherMiss {
		t.Errorf("assume=echo: got %v, want echo=%s env=%s", got, previewApproveGated, previewMatcherMiss)
	}

	// Comma-joined values split; repeatable params merge.
	rec, resp = get("/v1/admin/catalog/preview?role=draft-support&app=draftapp&assume=echo,env")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview with assume=echo,env = %d: %s", rec.Code, rec.Body)
	}
	got = catStatusMap(resp.Entries)
	if got["draftapp/env"] != previewHiddenPolicy {
		t.Errorf("assume=echo,env: draftapp/env = %q, want %q", got["draftapp/env"], previewHiddenPolicy)
	}

	// assume is app-scoped by definition: without an app filter it is a 400
	// with an actionable reason, never a silently ignored parameter.
	rec, _ = get("/v1/admin/catalog/preview?role=draft-support&assume=*")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("assume without app = %d, want 400", rec.Code)
	}
}

// TestCatalogPreviewDenyProvenance pins the 0.71.0 attribution fields: the
// preview says WHICH rule produced an outcome and whether the fail-closed
// default applied. "No rule mentions this yet" and "a rule refuses this on
// purpose" are different facts, and the console renders them differently.
func TestCatalogPreviewDenyProvenance(t *testing.T) {
	t.Parallel()
	app, _, _ := testAppCounting(t)
	up := startGatewayUpstream(t)
	catInstallEcho(t, app, up, "provapp", nil)
	catActivate(t, app, "prov-gates", `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: prov-gates}
spec:
  match: {roles: [prov-support]}
  rules:
    - id: approve-provapp-echo
      tools: [mcp.call]
      apps: [provapp]
      toolNames: {allow: ["echo"]}
      effect: allow
      mode: approve
      reason: "security signs off on echo"
      approve: {roles: [sec-approvers], timeoutSeconds: 60}
    - id: deny-provapp-env
      tools: [mcp.call]
      apps: [provapp]
      toolNames: {deny: ["env"]}
      effect: deny
      reason: "env blocked for prov-support"
`)

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
	find := func(resp catalogPreviewResponse, tool string) previewEntry {
		t.Helper()
		for _, e := range resp.Entries {
			if e.Tool == tool {
				return e
			}
		}
		t.Fatalf("no entry for tool %q in %+v", tool, resp.Entries)
		return previewEntry{}
	}

	// An explicit deny names its rule and set, and is NOT the default.
	resp := get("/v1/admin/catalog/preview?role=prov-support&app=provapp&assume=*")
	env := find(resp, "env")
	if env.Status != previewHiddenPolicy || env.Default || env.RuleID != "deny-provapp-env" || env.SetName != "prov-gates" {
		t.Errorf("explicit deny attribution wrong: %+v", env)
	}
	if env.Reason != "env blocked for prov-support" {
		t.Errorf("explicit deny reason = %q", env.Reason)
	}

	// An approve gate keeps its rule's reason and attribution.
	echo := find(resp, "echo")
	if echo.Status != previewApproveGated || echo.RuleID != "approve-provapp-echo" || echo.SetName != "prov-gates" {
		t.Errorf("approve attribution wrong: %+v", echo)
	}
	if echo.Reason != "security signs off on echo" {
		t.Errorf("approve reason = %q", echo.Reason)
	}

	// The assumed grant is access, so with no rule for this role the tool
	// runs on the grant alone (spec/policyset revision 17): the row says it
	// is the default, names no rule, and carries the engine's reason.
	resp = get("/v1/admin/catalog/preview?role=nobody-holds-this&app=provapp&assume=*")
	dflt := find(resp, "env")
	if dflt.Status != previewVisible || !dflt.Default || dflt.RuleID != "" || dflt.SetName != "" {
		t.Errorf("default allow attribution wrong: %+v", dflt)
	}
	if dflt.Reason != "allowed by role access. No policy rule gates this tool." {
		t.Errorf("default allow reason = %q", dflt.Reason)
	}
}
