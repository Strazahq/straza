package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestRolesDeleteRefusedWhileActivePoolNamesIt pins that a role named in
// approve.roles of an ACTIVE policy set cannot be deleted. Without
// the guard the pool silently loses its deciders and every record that rule
// holds only expires. The refusal is a 409 that names the set, the rule and
// the fix; a DRAFT set naming the role does not block (drafts govern
// nothing); once the set is deactivated the delete goes through.
func TestRolesDeleteRefusedWhileActivePoolNamesIt(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	ctx := context.Background()

	var pool rolePayload
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/roles", tok, map[string]string{"name": "sec-approvers", "kind": "approver"}, &pool); code != http.StatusCreated {
		t.Fatalf("seed approver role = %d", code)
	}
	doc := strings.Replace(approvePoolYAML, "[POOL]", "[sec-approvers]", 1)
	if code, b, _ := adminBytes(t, http.MethodPut, base+"/v1/admin/policies", tok, "application/yaml", []byte(doc)); code != http.StatusCreated {
		t.Fatalf("apply draft = %d %s", code, b)
	}

	// Draft: no guard, nothing governs yet. Prove it without deleting the
	// role we still need: the helper must report no active set.
	if naming, err := app.activeApprovePoolsNaming(ctx, "sec-approvers"); err != nil || len(naming) != 0 {
		t.Fatalf("draft set counted as an active pool: %v, %v", naming, err)
	}

	if code := adminReq(t, http.MethodPost, base+"/v1/admin/policies/pool-gate/activate", tok, map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}
	var res map[string]any
	code := adminReq(t, http.MethodDelete, base+"/v1/admin/roles/"+pool.ID, tok, nil, &res)
	if code != http.StatusConflict {
		t.Fatalf("delete pooled role = %d (%v), want 409", code, res)
	}
	msg, _ := res["error"].(string)
	for _, want := range []string{`"sec-approvers"`, `"pool-gate"`, `rule "held"`, "turn the set off"} {
		if !strings.Contains(msg, want) {
			t.Errorf("409 message = %q, want it to name %s", msg, want)
		}
	}
	if _, err := app.store.Roles().GetByID(ctx, pool.ID); err != nil {
		t.Fatalf("role after refused delete: %v, want it still present", err)
	}

	// Deactivate, then the delete is ordinary.
	if code := adminReq(t, http.MethodPost, base+"/v1/admin/policies/pool-gate/activate", tok, map[string]string{"status": "draft"}, nil); code != http.StatusOK {
		t.Fatalf("deactivate = %d", code)
	}
	var out map[string]string
	if code := adminReq(t, http.MethodDelete, base+"/v1/admin/roles/"+pool.ID, tok, nil, &out); code != http.StatusOK || out["status"] != "deleted" {
		t.Fatalf("delete after deactivation = %d %v, want 200 deleted", code, out)
	}
}
