package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestApproverKindWireAndGuards pins the approver role kind's wire contract
// (spec/policyset revision 15, openapi 0.73.0): kind "approver" round-trips
// on the access plane, the kind is fixed at create (no approver<->access
// PATCH flips; business<->application still flip), and an approver role
// can bind neither tools nor packs and can neither imply nor be implied.
func TestApproverKindWireAndGuards(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	var created struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/roles", tok,
		map[string]string{"name": "sec-approvers", "kind": "approver", "description": "deciders"}, &created); code != http.StatusCreated {
		t.Fatalf("create approver role = %d", code)
	}
	if created.Kind != "approver" {
		t.Fatalf("created kind = %q, want approver", created.Kind)
	}
	stored, err := app.store.Roles().GetByName(ctx, "sec-approvers")
	if err != nil || stored.Plane != store.RolePlaneAccess || stored.Kind != store.RoleKindApprover {
		t.Fatalf("stored = %+v, %v; want access plane, approver kind", stored, err)
	}

	var out map[string]any
	if code := adminReq(t, "PATCH", base+"/v1/admin/roles/"+created.ID, tok,
		map[string]string{"kind": "business"}, &out); code != http.StatusBadRequest {
		t.Errorf("approver→business PATCH = %d (%v), want 400", code, out)
	}
	dev, err := app.store.Roles().GetByName(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if code := adminReq(t, "PATCH", base+"/v1/admin/roles/"+dev.ID, tok,
		map[string]string{"kind": "approver"}, &out); code != http.StatusBadRequest {
		t.Errorf("business→approver PATCH = %d (%v), want 400", code, out)
	}
	var flipped struct {
		Kind string `json:"kind"`
	}
	if code := adminReq(t, "PATCH", base+"/v1/admin/roles/"+dev.ID, tok,
		map[string]string{"kind": "application"}, &flipped); code != http.StatusOK || flipped.Kind != "application" {
		t.Errorf("restating application PATCH = %d (%+v), want 200 application", code, flipped)
	}
	var patched struct {
		Kind        string `json:"kind"`
		Description string `json:"description"`
	}
	if code := adminReq(t, "PATCH", base+"/v1/admin/roles/"+created.ID, tok,
		map[string]string{"description": "who may approve agent actions"}, &patched); code != http.StatusOK {
		t.Fatalf("approver description PATCH = %d", code)
	}
	if patched.Kind != "approver" || patched.Description != "who may approve agent actions" {
		t.Errorf("patched = %+v, want kind approver kept", patched)
	}

	demoApp, err := app.store.Apps().Create(ctx, store.App{Name: "demo-tools", RuntimeKind: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/apps/"+demoApp.ID+"/bindings", tok,
		map[string]any{"role": "sec-approvers", "tools": []string{"echo"}}, &out); code != http.StatusBadRequest {
		t.Errorf("tool binding onto approver role = %d (%v), want 400", code, out)
	}
	pack, err := app.store.Packs().Create(ctx, store.KnowledgePack{Name: "p", Version: "1", Content: "c", Checksum: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/packs/"+pack.ID+"/bindings", tok,
		map[string]string{"role_id": created.ID}, &out); code != http.StatusBadRequest {
		t.Errorf("pack binding onto approver role = %d (%v), want 400", code, out)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/roles/"+dev.ID+"/implications", tok,
		map[string]string{"implies_role_id": created.ID}, &out); code != http.StatusBadRequest {
		t.Errorf("dev implies approver = %d (%v), want 400", code, out)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/roles/"+created.ID+"/implications", tok,
		map[string]string{"implies_role_id": dev.ID}, &out); code != http.StatusBadRequest {
		t.Errorf("approver implies dev = %d (%v), want 400", code, out)
	}
	imps, err := app.store.Roles().ListImplications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range imps {
		if imp.RoleID == created.ID || imp.ImpliesRoleID == created.ID {
			t.Errorf("an implication touches the approver role after the refusals: %+v", imp)
		}
	}
}

const approvePoolYAML = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: pool-gate }
spec:
  priority: 100
  match: { roles: [dev] }
  rules:
    - id: held
      tools: [shell.exec]
      command: { allowPatterns: ["echo *"] }
      effect: allow
      mode: approve
      approve:
        roles: [POOL]
      reason: "Straza: held"
`

// TestApprovePoolGate pins the revision 15 gate at validate AND activate: a
// pool naming an access role, a straza-kind role other than straza-admin, or
// an unknown role answers 400 with every violation and its fix; approver
// roles and straza-admin pass; a draft may still be applied (the git-first
// flow survives as drafts); and a set that reached active before the gate
// (stored around the API) keeps governing and is what the boot audit names.
func TestApprovePoolGate(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	for _, seed := range []map[string]string{
		{"name": "sec-approvers", "kind": "approver"},
		{"name": "auditor", "kind": "straza"},
	} {
		if code := adminReq(t, "POST", base+"/v1/admin/roles", tok, seed, nil); code != http.StatusCreated {
			t.Fatalf("seed role %s = %d", seed["name"], code)
		}
	}
	withPool := func(pool string) string { return strings.Replace(approvePoolYAML, "[POOL]", pool, 1) }

	cases := []struct {
		pool     string
		wantCode int
		wantText string
	}{
		{"[sec-approvers]", http.StatusOK, ""},
		{"[sec-approvers, straza-admin]", http.StatusOK, ""},
		{"[straza-admin]", http.StatusOK, ""},
		{"[dev]", http.StatusBadRequest, `names "dev" (application role)`},
		{"[auditor]", http.StatusBadRequest, `names "auditor" (straza role)`},
		{"[sec-aprovers]", http.StatusBadRequest, `names "sec-aprovers", which is not a role in Straza`},
		{"[dev, sec-aprovers]", http.StatusBadRequest, `names "dev" (application role)`},
	}
	for _, tc := range cases {
		var res map[string]any
		code := yamlReq(t, http.MethodPost, base+"/v1/admin/policies/validate", tok, withPool(tc.pool), &res)
		if code != tc.wantCode {
			t.Errorf("validate pool %s = %d (%v), want %d", tc.pool, code, res, tc.wantCode)
			continue
		}
		if tc.wantText != "" {
			msg, _ := res["error"].(string)
			if !strings.Contains(msg, tc.wantText) || !strings.Contains(msg, `rule "held"`) || !strings.Contains(msg, "--kind approver") {
				t.Errorf("validate pool %s error = %q, want rule-scoped text containing %q and the fix", tc.pool, msg, tc.wantText)
			}
		}
	}
	// Every violation in one answer, not the first one only.
	var both map[string]any
	if code := yamlReq(t, http.MethodPost, base+"/v1/admin/policies/validate", tok, withPool("[dev, sec-aprovers]"), &both); code != http.StatusBadRequest {
		t.Fatalf("two violations = %d", code)
	}
	if msg, _ := both["error"].(string); !strings.Contains(msg, `"dev"`) || !strings.Contains(msg, `"sec-aprovers"`) {
		t.Errorf("two-violation message = %q, want both named", msg)
	}

	// Apply stays permissive (draft), activation is the gate.
	if code, b, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", tok, "application/yaml", []byte(withPool("[dev]"))); code != http.StatusCreated {
		t.Fatalf("apply draft with access pool = %d %s, want 201 (drafts are not gated)", code, b)
	}
	var act map[string]any
	if code := adminReq(t, "POST", base+"/v1/admin/policies/pool-gate/activate", tok, map[string]string{"status": "active"}, &act); code != http.StatusBadRequest {
		t.Fatalf("activate with access pool = %d (%v), want 400", code, act)
	}
	if ps, err := app.store.Policies().GetByName(ctx, "pool-gate"); err != nil || ps.Status != "draft" {
		t.Fatalf("set after refused activation = %+v, %v; want still draft", ps, err)
	}
	if code, b, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", tok, "application/yaml", []byte(withPool("[sec-approvers]"))); code != http.StatusOK {
		t.Fatalf("re-apply with approver pool = %d %s", code, b)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/policies/pool-gate/activate", tok, map[string]string{"status": "active"}, &act); code != http.StatusOK {
		t.Fatalf("activate with approver pool = %d (%v), want 200", code, act)
	}

	// Legacy: a set that is active with an access pool (stored around the
	// API, as a pre-revision-15 upgrade leaves it) keeps governing and the
	// boot audit names it with the same words.
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: "legacy-pool", Priority: 90, Status: "active",
		YAMLSource: strings.Replace(withPool("[dev]"), "name: pool-gate", "name: legacy-pool", 1),
	}); err != nil {
		t.Fatal(err)
	}
	legacy, err := app.legacyApprovePools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy) != 1 || len(legacy["legacy-pool"]) != 1 || !strings.Contains(legacy["legacy-pool"][0], `"dev" (application role)`) {
		t.Errorf("legacyApprovePools = %v, want exactly legacy-pool with its dev violation", legacy)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatalf("recompile with the legacy set still compiles: %v", err)
	}
}
