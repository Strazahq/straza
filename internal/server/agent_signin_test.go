package server

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// agentIDToken answers an ID token for the audience straza that this
// server's issuer signs for the agent or service account named name, the
// token its key lane's grant hands it. Tests sign a non-person in with it,
// because the password page signs in a person only.
func agentIDToken(t *testing.T, app *App, name string) string {
	t.Helper()
	u, err := app.store.Users().GetByUsername(context.Background(), name)
	if err != nil {
		t.Fatalf("agent %s: %v", name, err)
	}
	idt, err := app.tokens.MintIDToken(u.ID, "straza", 10*time.Minute, u.Username, u.Email)
	if err != nil {
		t.Fatal(err)
	}
	return idt
}

// agentCheckin checks the agent named name in under claude-code with
// agentIDToken, as gatewaySession checks a person in, so the check-in fills
// the gateway's subject cache, and answers the session token and its id.
func agentCheckin(t *testing.T, app *App, base, name string) (string, string) {
	t.Helper()
	code, body := postJSON(t, base+"/v1/checkin", map[string]any{
		"id_token":    agentIDToken(t, app, name),
		"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
		"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "x"}},
	})
	tok, _ := body["session_token"].(string)
	sid, _ := body["session_id"].(string)
	if code != http.StatusOK || tok == "" || sid == "" {
		t.Fatalf("check-in of %s = %d %v", name, code, body)
	}
	return tok, sid
}

// agentSessionToken is agentCheckin's session token alone, as sessionToken
// answers a person's.
func agentSessionToken(t *testing.T, app *App, base, name string) string {
	t.Helper()
	tok, _ := agentCheckin(t, app, base, name)
	return tok
}
