package server

import (
	"net/http"
	"strings"
	"testing"
)

const byNamePolicy = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: by-name }
spec:
  match: { roles: [dev] }
  rules:
    - id: deny-monitor
      tools: [other]
      toolNames: { deny: [Monitor] }
      reason: "Monitor is off for dev"
`

// TestDecideHarnessToolName pins the server half of a rule that names a
// harness tool: an unclassified tool is named by the event's toolName, a
// classified one by the request's nativeTool, and a nativeTool inside the
// event is not read, because the canonical event stays harness-neutral.
func TestDecideHarnessToolName(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	adminTok, _ := checkinToken(t, app, base)
	applyPolicyYAML(t, base, adminTok, byNamePolicy, http.StatusCreated)
	if code := adminReq(t, "POST", base+"/v1/admin/policies/by-name/activate", adminTok, map[string]string{"status": "active"}, nil); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}
	token, _ := checkinToken(t, app, base)

	spawn := map[string]any{"kind": "tool.pre", "tool": "task.spawn"}
	rows := []struct {
		name       string
		body       map[string]any
		wantEffect string
		wantRule   string
	}{
		{"classified tool named on the request", map[string]any{"event": spawn, "nativeTool": "Monitor"}, "deny", "deny-monitor"},
		{"unclassified tool named on the event", map[string]any{"event": map[string]any{"kind": "tool.pre", "tool": "other", "toolName": "Monitor"}}, "deny", "deny-monitor"},
		{"no name keeps the lane", map[string]any{"event": spawn}, "allow", ""},
		{"a nativeTool inside the event is not read", map[string]any{"event": map[string]any{"kind": "tool.pre", "tool": "task.spawn", "nativeTool": "Monitor"}}, "allow", ""},
	}
	for _, row := range rows {
		code, dec := postJSONAuth(t, base+"/v1/decide", token, row.body)
		if code != http.StatusOK || dec["effect"] != row.wantEffect || (dec["ruleId"] != nil && dec["ruleId"] != row.wantRule) {
			t.Errorf("%s: %d %v, want %s by %q", row.name, code, dec, row.wantEffect, row.wantRule)
		}
	}
}

// TestByNameGateValidateAndActivate pins that validate and activate refuse,
// in the same words, a rule that allows by name a tool Straza already
// classifies, and that the same allow of an unclassified tool passes both.
func TestByNameGateValidateAndActivate(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	user := seedIdentity(t, app)
	grantAdmin(t, app, user.ID)
	tok, _ := checkinToken(t, app, base)
	set := func(name string) string {
		return "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata: { name: by-name-gate }\nspec:\n  rules:\n" +
			"    - id: allow-it\n      tools: [other]\n      toolNames: { allow: [" + name + "] }\n"
	}
	const refusal = `rule "allow-it": toolNames.allow names Bash, which Straza already classifies as shell.exec.`

	var res map[string]any
	if code := yamlReq(t, http.MethodPost, base+"/v1/admin/policies/validate", tok, set("Bash"), &res); code != http.StatusBadRequest {
		t.Fatalf("validate a by-name allow of Bash = %d (%v), want 400", code, res)
	}
	if msg, _ := res["error"].(string); !strings.Contains(msg, refusal) {
		t.Errorf("validate error = %q, want %q", msg, refusal)
	}
	applyPolicyYAML(t, base, tok, set("Bash"), http.StatusCreated)
	var act map[string]any
	if code := adminReq(t, "POST", base+"/v1/admin/policies/by-name-gate/activate", tok, map[string]string{"status": "active"}, &act); code != http.StatusBadRequest {
		t.Fatalf("activate a by-name allow of Bash = %d (%v), want 400", code, act)
	}
	if msg, _ := act["error"].(string); !strings.Contains(msg, refusal) {
		t.Errorf("activate error = %q, want %q", msg, refusal)
	}

	if code := yamlReq(t, http.MethodPost, base+"/v1/admin/policies/validate", tok, set("FutureTool"), &res); code != http.StatusOK {
		t.Fatalf("validate a by-name allow of FutureTool = %d (%v), want 200", code, res)
	}
	applyPolicyYAML(t, base, tok, set("FutureTool"), http.StatusOK)
	if code := adminReq(t, "POST", base+"/v1/admin/policies/by-name-gate/activate", tok, map[string]string{"status": "active"}, &act); code != http.StatusOK {
		t.Fatalf("activate a by-name allow of FutureTool = %d (%v), want 200", code, act)
	}
}
