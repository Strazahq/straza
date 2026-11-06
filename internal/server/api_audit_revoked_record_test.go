package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// sessionTokenFailures returns the data of the chained straza.audit.authn login
// failures on the session-token lane that name session.
func sessionTokenFailures(app *App, session string) []map[string]any {
	recs, _ := app.store.Audit().List(context.Background(), 0, 1000)
	var out []map[string]any
	for _, rec := range recs {
		var ce struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal([]byte(rec.CE), &ce) != nil || ce.Type != "straza.audit.authn" {
			continue
		}
		if ce.Data["action"] == "login" && ce.Data["outcome"] == "failure" && ce.Data["via"] == "session-token" && ce.Data["session"] == session {
			out = append(out, ce.Data)
		}
	}
	return out
}

// TestAuditBatchRefusalOfARevokedPrincipalIsRecorded pins that an upload
// refused because its session, device or user is revoked writes exactly one
// straza.audit.authn login failure through the audit queue. It names the
// token's user, session and harness, carries the reason the refresh lane
// writes for the same revocation, and nothing of the upload reaches the
// chain.
func TestAuditBatchRefusalOfARevokedPrincipalIsRecorded(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, reason string
		revoke       func(app *App, userID, session string)
	}{
		{"a revoked session", "session is no longer active", func(app *App, _, session string) { app.denylist.revokeSession(session) }},
		{"a revoked user", "device or user revoked", func(app *App, userID, _ string) { app.denylist.revokeUser(userID) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			app, base := testApp(t)
			user := seedIdentity(t, app)
			token, session := checkinTokenAs(t, base, "claude-code")
			c.revoke(app, user.ID, session)

			batch := map[string]any{"events": []map[string]any{
				{"data": map[string]any{"event": "tool.pre", "tool": "shell.exec", "command": "refused-spooled"}},
			}}
			if code, resp := postJSONAuth(t, base+"/v1/audit/batch", token, batch); code != http.StatusForbidden {
				t.Fatalf("batch of a revoked principal = %d %v, want 403", code, resp)
			}
			// The revoked token's decide is denied and audited through the
			// same queue, after the refusal, so once its record is chained the
			// refusal's record is too.
			decide(t, base, token, map[string]any{"kind": "tool.pre", "tool": "shell.exec", "command": "after-the-refusal"})
			waitChainData(t, app, "after-the-refusal")
			var got []map[string]any
			for deadline := time.Now().Add(5 * time.Second); len(got) == 0 && time.Now().Before(deadline); time.Sleep(30 * time.Millisecond) {
				got = sessionTokenFailures(app, session)
			}
			if len(got) != 1 {
				t.Fatalf("login failure records for the refused upload = %d %v, want exactly 1", len(got), got)
			}
			rec := got[0]
			harness, _ := rec["harness"].(string)
			if rec["userId"] != user.ID || rec["reason"] != c.reason || !strings.HasPrefix(harness, "claude-code") || rec["sourceIp"] == nil {
				t.Fatalf("the refusal record is %v, want userId %s, reason %q, the claude-code harness and the source address", rec, user.ID, c.reason)
			}
			if d := chainData(app, "refused-spooled"); d != nil {
				t.Fatalf("a record of the refused upload reached the chain: %v", d)
			}
		})
	}
}
