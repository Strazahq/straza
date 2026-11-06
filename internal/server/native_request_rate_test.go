package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestApprovalRequestRate pins the rate of new tickets that
// straza__approval_request opens for one session: five at once, and the
// sixth is refused with the bounded wait before anyone is asked. Asking
// again for a call whose ticket is pending joins that ticket and spends
// nothing, and another session keeps a rate of its own. The refused call's
// one record is a deny that keeps the refusal, the person and the session.
func TestApprovalRequestRate(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	kim := seedGatewayUser(t, app, "kim", "dev")
	if _, err := app.store.Policies().Create(ctx, store.PolicySet{
		Name: "native-rate", Status: "active", YAMLSource: `
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: native-rate}
spec:
  match: {roles: [dev]}
  rules:
    - id: allow-native
      tools: [mcp.call]
      apps: [straza]
      toolNames: {allow: ["*"]}
      effect: allow
    - id: deploy-ticket
      tools: [shell.exec]
      command: {allowPatterns: ["deploy-prod*"]}
      effect: allow
      mode: approve
      approve: {class: ticket, roles: [change], ticketTTLSeconds: 86400, grantTTLSeconds: 3600}
`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.snapshots.Recompile(ctx); err != nil {
		t.Fatal(err)
	}
	busy, busySession := checkinTokenAs(t, base, "claude-code")
	other, _ := checkinTokenAs(t, base, "claude-code")

	ask := func(token, command string) (ref, refusal string) {
		_, res, _ := mcpCall(t, base, token, "tools/call", map[string]any{
			"name":      "straza__approval_request",
			"arguments": map[string]any{"action": map[string]any{"tool": "shell.exec", "command": command}},
		})
		result, _ := res["result"].(map[string]any)
		if isErr, _ := result["isError"].(bool); isErr {
			content, _ := result["content"].([]any)
			first, _ := content[0].(map[string]any)
			text, _ := first["text"].(string)
			return "", text
		}
		sc, _ := result["structuredContent"].(map[string]any)
		ref, _ = sc["ref"].(string)
		return ref, ""
	}

	var firstRef string
	cases := []struct {
		name, token, command string
		want                 string // new, joins or refused
	}{
		{"the first new ticket", busy, "deploy-prod --step=1", "new"},
		{"the second new ticket", busy, "deploy-prod --step=2", "new"},
		{"the third new ticket", busy, "deploy-prod --step=3", "new"},
		{"the fourth new ticket", busy, "deploy-prod --step=4", "new"},
		{"the fifth new ticket", busy, "deploy-prod --step=5", "new"},
		{"the sixth new ticket is refused", busy, "deploy-prod --step=6", "refused"},
		{"asking again for a pending call joins its ticket", busy, "deploy-prod --step=1", "joins"},
		{"another session keeps its own rate", other, "deploy-prod --step=6", "new"},
	}
	for _, tc := range cases {
		ref, refusal := ask(tc.token, tc.command)
		switch tc.want {
		case "new":
			if ref == "" || refusal != "" {
				t.Fatalf("%s: ref %q, refusal %q, want a new ticket", tc.name, ref, refusal)
			}
			if firstRef == "" {
				firstRef = ref
			}
		case "joins":
			if ref != firstRef {
				t.Errorf("%s: ref %q, refusal %q, want the pending ticket %s", tc.name, ref, refusal, firstRef)
			}
		case "refused":
			if refusal != approvalRateReason {
				t.Errorf("%s: ref %q, refusal %q\nwant %q", tc.name, ref, refusal, approvalRateReason)
			}
		}
	}
	pending, err := app.approval.List(ctx, "pending")
	if err != nil || len(pending) != 6 {
		t.Fatalf("pending tickets = %d, %v, want 6: five of the busy session and one of the other", len(pending), err)
	}

	// The refused call's one record.
	var kept []map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for len(kept) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		kept = requestRecords(t, app, approvalRateReason)
	}
	if len(kept) != 1 {
		t.Fatalf("records that keep the refusal = %d, want exactly 1: %v", len(kept), kept)
	}
	for field, want := range map[string]any{
		"user": kim.ID, "session": busySession, "effect": "deny", "app": "straza", "toolName": "approval_request",
	} {
		if kept[0][field] != want {
			t.Errorf("record %s = %v, want %v", field, kept[0][field], want)
		}
	}
}

// requestRecords returns the data of every straza.audit.mcp record in the
// outbox, published or not, whose reason is reason.
func requestRecords(t *testing.T, app *App, reason string) []map[string]any {
	t.Helper()
	evs, err := app.store.Outbox().ListRecent(context.Background(), 500)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, e := range evs {
		var ce struct {
			Data map[string]any `json:"data"`
		}
		if e.Subject != "straza.audit.mcp" || json.Unmarshal([]byte(e.CE), &ce) != nil {
			continue
		}
		if fmt.Sprint(ce.Data["reason"]) == reason {
			out = append(out, ce.Data)
		}
	}
	return out
}
