package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/config"
)

// TestGatewayAuthRefusalsAudited pins that each gateway request refused at
// the door, for a revoked session or user or for an attestation below
// governance.minAttestation, writes exactly one straza.audit.mcp record, as
// /v1/decide records the same refusals: the event request, effect deny,
// ruleId revoked or attestation, the refusal as reason, the session and the
// user, and on a server endpoint the server as asked for in app. The 403
// answers are unchanged, and no refusal reads the database.
func TestGatewayAuthRefusalsAudited(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name   string
		path   string
		method string
		ruleID string
		refuse func(app *App, userID, sid string)
	}{
		{"revoked session on /mcp", "/mcp", http.MethodPost, "revoked",
			func(app *App, _, sid string) { app.denylist.RevokeSession(sid) }},
		{"revoked user on a server endpoint", "/mcp/someserver", http.MethodPost, "revoked",
			func(app *App, userID, _ string) { app.denylist.RevokeUser(userID) }},
		{"attestation below the minimum on the stream", "/mcp", http.MethodGet, "attestation",
			func(app *App, _, _ string) { app.cfg.Governance.MinAttestation = config.AttestationManaged }},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			app, base, cs := testAppCounting(t)
			user := seedGatewayUser(t, app, "kim", "dev")
			tok, sid := gatewaySession(t, base, "kim")
			row.refuse(app, user.ID, sid)

			before := cs.snapshot()
			var body []byte
			if row.method == http.MethodPost {
				var code int
				code, _, body = mcpPost(t, base+row.path, tok, "tools/list", nil)
				if code != http.StatusForbidden {
					t.Fatalf("answer = %d %s, want 403", code, body)
				}
			} else {
				req, _ := http.NewRequest(http.MethodGet, base+row.path, nil)
				req.Header.Set("Authorization", "Bearer "+tok)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = resp.Body.Close() }()
				if resp.StatusCode != http.StatusForbidden {
					t.Fatalf("answer = %d, want 403", resp.StatusCode)
				}
				var m map[string]any
				_ = json.NewDecoder(resp.Body).Decode(&m)
				body, _ = json.Marshal(m)
			}
			assertNoRepoAccess(t, cs, row.name, before)
			var answer struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(body, &answer); err != nil || answer.Error == "" {
				t.Fatalf("answer %s carries no refusal", body)
			}

			rec := awaitMCPRecords(t, app, 1, func(d map[string]any) bool { return d["event"] == "request" })[0]
			app2 := ""
			if row.path != "/mcp" {
				app2 = "someserver"
			}
			for k, v := range map[string]any{
				"effect": "deny", "ruleId": row.ruleID, "setName": "", "reason": answer.Error,
				"session": sid, "user": user.ID, "app": app2, "toolName": "", "harness": "claude-code/2.1.0",
			} {
				if rec[k] != v {
					t.Errorf("record %s = %v, want %v", k, rec[k], v)
				}
			}
		})
	}
}
