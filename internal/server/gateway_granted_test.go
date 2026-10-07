package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestGatewayGrantedFact pins spec/policyset revision 17 on the gateway: a
// bound app tool with no policy rule runs on the grant alone and its audit
// record says so (granted, default, the binding id, the granting role and
// the credential row), a native straza tool with no rule stays hidden and
// unknown, and an unbound tool stays unknown.
func TestGatewayGrantedFact(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t)
	up := startGatewayUpstream(t)
	ctx := context.Background()
	seedGatewayUser(t, app, "kim", "dev")

	mf, err := managerParse(t, fmt.Sprintf(`
apiVersion: straza.dev/v1beta1
kind: App
metadata: {name: grantapp}
server: {name: straza.test/grantapp, version: "1.0.0"}
straza:
  runtime:
    kind: remote
    remote: {url: %q}
  credential:
    kind: static
    inject: {as: header, name: Authorization, template: "Bearer {{secret}}"}
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
	const secretValue = "sk-grant-NEVER-LEAK"
	cred, err := app.broker.Set(ctx, row.ID, devRole.ID, secretValue)
	if err != nil {
		t.Fatal(err)
	}
	app.manager.SecretUpdated(ctx, row.ID)
	binding, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{
		RoleID: devRole.ID, AppID: row.ID, ToolMatcher: `["echo"]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	catWaitRunning(t, app, "grantapp")
	// No policy set names grantapp or the native app: every decision below
	// is the engine default.
	catRecompile(t, app)
	tok := sessionToken(t, base, "kim")

	names := toolNamesOf(t, second(mcpCall(t, base, tok, "tools/list", nil)))
	if len(names) != 1 || names[0] != "grantapp__echo" {
		t.Fatalf("catalog = %v, want [grantapp__echo] (granted tool visible, native tools hidden)", names)
	}

	_, _, raw := mcpCall(t, base, tok, "tools/call", map[string]any{
		"name": "grantapp__echo", "arguments": map[string]string{"text": "hi"},
	})
	if !strings.Contains(string(raw), "echo: hi") {
		t.Fatalf("granted call with no rule = %s, want the upstream answer", raw)
	}
	if strings.Contains(string(raw), secretValue) {
		t.Fatal("SECRET LEAKED into a client-visible response")
	}
	for _, name := range []string{"grantapp__env", "straza__approval_status"} {
		_, call, _ := mcpCall(t, base, tok, "tools/call", map[string]any{"name": name})
		errObj, _ := call["error"].(map[string]any)
		if errObj == nil || !strings.Contains(errObj["message"].(string), "unknown tool") {
			t.Errorf("%s call = %v, want -32602 unknown tool", name, call)
		}
	}

	// Exactly one mcp record for the granted call, with the revision 23 fields.
	var data map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for {
		recs, err := app.store.Audit().List(ctx, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		var matches []string
		for _, r := range recs {
			if strings.Contains(r.CE, `"type":"straza.audit.mcp"`) && strings.Contains(r.CE, `"app":"grantapp"`) {
				matches = append(matches, r.CE)
			}
		}
		if len(matches) > 1 {
			t.Fatalf("%d mcp records for one call, want exactly one", len(matches))
		}
		if len(matches) == 1 {
			var ce struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal([]byte(matches[0]), &ce); err != nil {
				t.Fatal(err)
			}
			data = ce.Data
			t.Logf("granted mcp audit record: %s", matches[0])
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no straza.audit.mcp record for the granted call")
		}
		time.Sleep(50 * time.Millisecond)
	}
	want := map[string]any{
		"effect": "allow", "granted": true, "default": true, "ruleId": "", "setName": "",
		"reason":    "allowed by role access. No policy rule gates this tool.",
		"bindingId": binding.ID, "role": "dev", "credentialId": cred.ID, "toolName": "echo",
	}
	for k, v := range want {
		if data[k] != v {
			t.Errorf("record %s = %v, want %v", k, data[k], v)
		}
	}
	if b, _ := json.Marshal(data); strings.Contains(string(b), secretValue) {
		t.Fatal("SECRET LEAKED into the audit record")
	}
}
