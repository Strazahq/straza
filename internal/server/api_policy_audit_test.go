package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestPolicyAdminAuditEvents pins the policy lifecycle's admin audit trail:
// save/activate/deactivate/delete are THE governance-shaping mutations, so
// "who activated this policy" needs an answer as user locks and token mints
// have one. Each mutation must emit its action with the rev-18 actor
// triple, activation must name the resulting snapshot (the change-management
// evidence: which compiled artifact started governing), and the read-shaped
// POSTs (validate/simulate) must stay silent.
func TestPolicyAdminAuditEvents(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	grantAdmin(t, app, user.ID)

	assertPolicyEvent := func(t *testing.T, action, name string) map[string]any {
		t.Helper()
		data := lastAdminAction(t, app, action)
		if got := data["name"]; got != name {
			t.Errorf("%s name = %v, want %q", action, got, name)
		}
		if id, _ := data["id"].(string); id == "" {
			t.Errorf("%s carries no policy id", action)
		}
		if got := data["actor"]; got != "kim" {
			t.Errorf("%s actor = %v, want %q", action, got, "kim")
		}
		if got := data["actorId"]; got != user.ID {
			t.Errorf("%s actorId = %v, want %q", action, got, user.ID)
		}
		if got := data["actorVia"]; got != "login" {
			t.Errorf("%s actorVia = %v, want %q", action, got, "login")
		}
		return data
	}

	// Create, then update: same endpoint, distinct actions; an auditor must
	// see "new set appeared" and "existing set changed" as different facts.
	// The update sends a new text, since the stored text sent again writes
	// nothing.
	if code, body, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", idToken, "application/yaml", []byte(rmPolicy)); code != http.StatusCreated {
		t.Fatalf("apply (create) = %d: %s", code, body)
	}
	assertPolicyEvent(t, "policy.create", "block-rm")

	updated := strings.Replace(rmPolicy, "Destructive delete blocked", "Destructive delete refused", 1)
	if code, body, _ := adminBytes(t, "PUT", base+"/v1/admin/policies", idToken, "application/yaml", []byte(updated)); code != http.StatusOK {
		t.Fatalf("apply (update) = %d: %s", code, body)
	}
	assertPolicyEvent(t, "policy.update", "block-rm")

	// Activation and deactivation both recompile the snapshot; both events
	// must name the snapshot that resulted.
	if code := adminReq(t, "POST", base+"/v1/admin/policies/block-rm/activate", idToken,
		map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}
	if data := assertPolicyEvent(t, "policy.activate", "block-rm"); data["snapshot"] == "" || data["snapshot"] == nil {
		t.Errorf("policy.activate carries no snapshot id")
	}

	if code := adminReq(t, "POST", base+"/v1/admin/policies/block-rm/activate", idToken,
		map[string]string{"status": "draft"}, nil); code != http.StatusOK {
		t.Fatalf("deactivate = %d", code)
	}
	if data := assertPolicyEvent(t, "policy.deactivate", "block-rm"); data["snapshot"] == "" || data["snapshot"] == nil {
		t.Errorf("policy.deactivate carries no snapshot id")
	}

	if code, body, _ := adminBytes(t, "DELETE", base+"/v1/admin/policies/block-rm", idToken, "", nil); code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", code, body)
	}
	assertPolicyEvent(t, "policy.delete", "block-rm")

	// Validate mutates nothing and must add no policy.* event.
	before := countPolicyEvents(t, app)
	if code, body, _ := adminBytes(t, "POST", base+"/v1/admin/policies/validate", idToken, "application/yaml", []byte(rmPolicy)); code != http.StatusOK {
		t.Fatalf("validate = %d: %s", code, body)
	}
	if after := countPolicyEvents(t, app); after != before {
		t.Errorf("validate emitted admin events: %d -> %d", before, after)
	}
}

// countPolicyEvents counts straza.audit.admin events whose action is in the
// policy.* family.
func countPolicyEvents(t *testing.T, app *App) int {
	t.Helper()
	n := 0
	for _, data := range adminAuditEvents(t, app) {
		if action, _ := data["action"].(string); strings.HasPrefix(action, "policy.") {
			n++
		}
	}
	return n
}
