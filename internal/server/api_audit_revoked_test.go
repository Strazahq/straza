package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestAuditBatchRefusesRevokedSession pins that a revoked session appends
// nothing to the chain through the spool drain route, the way the decide,
// gateway and push lanes already refuse it: the same token that was accepted
// before the revocation is refused with a sentence after it.
func TestAuditBatchRefusesRevokedSession(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app)
	token, sessionID := checkinTokenAs(t, base, "claude-code")
	batch := map[string]any{
		"events": []map[string]any{
			{"data": map[string]any{"event": "tool.pre", "tool": "shell.exec", "command": "spooled-1"}},
		},
	}
	if code, resp := postJSONAuth(t, base+"/v1/audit/batch", token, batch); code != http.StatusOK {
		t.Fatalf("batch before the revocation = %d %v", code, resp)
	}

	if err := app.store.Sessions().SetStatus(context.Background(), sessionID, "revoked"); err != nil {
		t.Fatal(err)
	}
	app.denylist.revokeSession(sessionID)
	code, resp := postJSONAuth(t, base+"/v1/audit/batch", token, batch)
	if code != http.StatusForbidden {
		t.Fatalf("batch after the revocation = %d %v, want 403", code, resp)
	}
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "revoked") || !strings.Contains(msg, "Start a new session") {
		t.Errorf("refusal %q must say the session is revoked and what to do", msg)
	}
}
