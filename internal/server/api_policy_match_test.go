package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

const matchGateYAML = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: match-gate }
spec:
  priority: 90
  match: { roles: [TARGET] }
  rules:
    - id: quiet
      tools: [shell.exec]
      command: { allowPatterns: ["echo *"] }
      effect: allow
`

// TestMatchRolesGate pins the revision 16 gate at validate AND activate:
// match.roles may name only roles a session can actually reach tools
// through, which is application-kind roles. A business role is refused with
// the composition fix (sessions carry the implication closure, so a policy
// naming the application role covers every path to its tools; one naming
// the business role governs a single grant path and the same application
// role held any other way walks past it). Approver and straza-kind roles never
// match sessions and are refused by kind. An UNKNOWN name stays legal:
// match is a selector, not a reference (match.users has no existence check
// either); it matches nobody until the role exists, and a role later born
// with a refused kind is caught at the set's next activation plus the boot
// audit. Drafts stay permissive (git-first flow); activation is the gate; a
// set active from before the revision keeps governing and is what the boot
// audit names.
func TestMatchRolesGate(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	for _, seed := range []map[string]string{
		{"name": "payments-ops", "kind": "business"},
		{"name": "sec-approvers", "kind": "approver"},
		{"name": "auditor", "kind": "straza"},
	} {
		if code := adminReq(t, "POST", base+"/v1/admin/roles", tok, seed, nil); code != http.StatusCreated {
			t.Fatalf("seed role %s = %d", seed["name"], code)
		}
	}
	withMatch := func(roles string) string { return strings.Replace(matchGateYAML, "[TARGET]", roles, 1) }

	cases := []struct {
		roles    string
		wantCode int
		wantText string
	}{
		// The fixture dev is application kind: the legal selector.
		{"[dev]", http.StatusOK, ""},
		// Unknown names are selectors that match nobody, not violations.
		{"[ghost]", http.StatusOK, ""},
		{"[dev, ghost]", http.StatusOK, ""},
		{"[payments-ops]", http.StatusBadRequest, `match.roles names "payments-ops" (business role)`},
		{"[sec-approvers]", http.StatusBadRequest, `match.roles names "sec-approvers" (approver role)`},
		{"[auditor]", http.StatusBadRequest, `match.roles names "auditor" (straza role)`},
		{"[straza-admin]", http.StatusBadRequest, `match.roles names "straza-admin" (straza role)`},
		{"[payments-ops, ghost]", http.StatusBadRequest, `"payments-ops" (business role)`},
	}
	for _, tc := range cases {
		var res map[string]any
		code := yamlReq(t, http.MethodPost, base+"/v1/admin/policies/validate", tok, withMatch(tc.roles), &res)
		if code != tc.wantCode {
			t.Errorf("validate match %s = %d (%v), want %d", tc.roles, code, res, tc.wantCode)
			continue
		}
		if tc.wantText != "" {
			msg, _ := res["error"].(string)
			if !strings.Contains(msg, tc.wantText) {
				t.Errorf("validate match %s error = %q, want %q", tc.roles, msg, tc.wantText)
			}
		}
	}
	// The business refusal carries the composition fix, not a bare no.
	var biz map[string]any
	if code := yamlReq(t, http.MethodPost, base+"/v1/admin/policies/validate", tok, withMatch("[payments-ops]"), &biz); code != http.StatusBadRequest {
		t.Fatalf("business match validate = %d", code)
	}
	if msg, _ := biz["error"].(string); !strings.Contains(msg, "application roles") {
		t.Errorf("business refusal = %q, want the application-role fix named", msg)
	}
	// Every violation in one answer.
	var both map[string]any
	if code := yamlReq(t, http.MethodPost, base+"/v1/admin/policies/validate", tok, withMatch("[payments-ops, sec-approvers]"), &both); code != http.StatusBadRequest {
		t.Fatalf("two violations = %d", code)
	}
	if msg, _ := both["error"].(string); !strings.Contains(msg, `"payments-ops"`) || !strings.Contains(msg, `"sec-approvers"`) {
		t.Errorf("two-violation message = %q, want both named", msg)
	}

	// Apply stays permissive (draft), activation is the gate.
	if code, b, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", tok, "application/yaml", []byte(withMatch("[payments-ops]"))); code != http.StatusCreated {
		t.Fatalf("apply draft with business match = %d %s, want 201 (drafts are not gated)", code, b)
	}
	var act map[string]any
	if code := adminReq(t, "POST", base+"/v1/admin/policies/match-gate/activate", tok, map[string]string{"status": "active"}, &act); code != http.StatusBadRequest {
		t.Fatalf("activate with business match = %d (%v), want 400", code, act)
	}
	if ps, err := app.store.Policies().GetByName(ctx, "match-gate"); err != nil || ps.Status != "draft" {
		t.Fatalf("set after refused activation = %+v, %v; want still draft", ps, err)
	}
	if code, b, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", tok, "application/yaml", []byte(withMatch("[dev]"))); code != http.StatusOK {
		t.Fatalf("re-apply with application match = %d %s", code, b)
	}
	if code := adminReq(t, "POST", base+"/v1/admin/policies/match-gate/activate", tok, map[string]string{"status": "active"}, &act); code != http.StatusOK {
		t.Fatalf("activate with application match = %d (%v), want 200", code, act)
	}

	// Legacy: a set active with a business match (stored around the API, as
	// a pre-revision-16 upgrade leaves it) keeps governing and the boot
	// audit names it with the same words.
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: "legacy-match", Priority: 80, Status: "active",
		YAMLSource: strings.Replace(withMatch("[payments-ops]"), "name: match-gate", "name: legacy-match", 1),
	}); err != nil {
		t.Fatal(err)
	}
	legacy, err := app.legacyMatchRoles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy) != 1 || len(legacy["legacy-match"]) != 1 || !strings.Contains(legacy["legacy-match"][0], `"payments-ops" (business role)`) {
		t.Errorf("legacyMatchRoles = %v, want exactly legacy-match with its business violation", legacy)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatalf("recompile with the legacy set still compiles: %v", err)
	}
}
