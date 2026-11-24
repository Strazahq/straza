package server

import (
	"context"
	"net/http"
	"testing"
)

// TestRefreshKeepsSessionHarness pins that the session row names the
// harness of a session for its whole life: a refresh that names another
// harness, another version, a human client or none at all answers 200, and
// the refreshed token's harness claim, the cached subject the decide path
// reads and the row all keep the harness the session started with.
func TestRefreshKeepsSessionHarness(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	h := newKeyHolder(t, base)
	h.enroll(idToken, "sha256:fp-harness")
	ctx := context.Background()

	cases := []struct {
		name    string
		harness map[string]string
	}{
		{"the same harness", map[string]string{"name": "claude-code", "version": "2.1.0"}},
		{"another harness name and version", map[string]string{"name": "codex", "version": "9.0.0"}},
		{"another version of the same harness", map[string]string{"name": "claude-code", "version": "9.9.9"}},
		{"a human client's name", map[string]string{"name": "console", "version": "1"}},
		{"no harness at all", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token, sessionID := h.mustCheckin()
			body := map[string]any{
				"session_token": token,
				"attestation":   map[string]any{"managed": false, "hashes": map[string]string{}},
			}
			if tc.harness != nil {
				body["harness"] = tc.harness
			}
			code, refresh := h.call("/v1/checkin", token, body)
			if code != http.StatusOK || refresh["session_id"] != sessionID {
				t.Fatalf("refresh = %d %v, want 200 for session %s", code, refresh, sessionID)
			}
			claims, err := app.tokens.Verify(refresh["session_token"].(string))
			if err != nil {
				t.Fatalf("verify the refreshed token: %v", err)
			}
			if claims.Harness != "claude-code/2.1.0" {
				t.Errorf("the refreshed token names harness %q, want claude-code/2.1.0", claims.Harness)
			}
			e, ok := app.subjects.forToken(claims)
			if !ok {
				t.Fatal("the decide path finds no cached subject for the refreshed token")
			}
			if e.sub.Harness != "claude-code/2.1.0" || e.facts.harness != "claude-code" {
				t.Errorf("the cached subject names harness %q with facts %q, want claude-code/2.1.0 and claude-code",
					e.sub.Harness, e.facts.harness)
			}
			ses, err := app.store.Sessions().GetByID(ctx, sessionID)
			if err != nil || ses.HarnessName != "claude-code" || ses.HarnessVersion != "2.1.0" {
				t.Errorf("the session row names %q %q (%v), want claude-code 2.1.0", ses.HarnessName, ses.HarnessVersion, err)
			}
		})
	}
}
