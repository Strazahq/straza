package server

import (
	"context"
	"net/http"
	"testing"
)

// TestRefreshConsultsDenylist pins the session-token lane of check-in
// against the in-memory denylist: a session entry answers 401 with the
// stand-down sentence, a user entry answers 403 with the revoked sentence,
// each refusal chains exactly one authn failure, and the session row is
// left untouched, which proves the refusal came from memory and not from
// the store. The user case runs last because a user entry is sticky for
// every later check-in of the same user.
func TestRefreshConsultsDenylist(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	ctx := context.Background()
	harness := map[string]string{"name": "claude-code", "version": "2.1.0"}

	cases := []struct {
		name       string
		arm        func(sessionID string)
		wantCode   int
		wantError  string
		wantReason string
	}{
		{
			name:       "session on the denylist",
			arm:        app.denylist.revokeSession,
			wantCode:   http.StatusUnauthorized,
			wantError:  "session is no longer active",
			wantReason: "session is no longer active",
		},
		{
			name:       "user on the denylist",
			arm:        func(string) { app.denylist.revokeUser(kim.ID) },
			wantCode:   http.StatusForbidden,
			wantError:  "Straza: this device or user has been revoked. Contact your administrator",
			wantReason: "device or user revoked",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token, sesID := checkinToken(t, app, base)
			tc.arm(sesID)
			code, body := postJSON(t, base+"/v1/checkin", map[string]any{"session_token": token, "harness": harness})
			if code != tc.wantCode || body["error"] != tc.wantError {
				t.Fatalf("refresh = %d %v, want %d %q", code, body, tc.wantCode, tc.wantError)
			}
			got := waitAuthn(t, app, tc.name, func(d map[string]any) bool {
				return d["action"] == "login" && d["outcome"] == "failure" && d["session"] == sesID
			})
			if got["via"] != "session-token" || got["userId"] != kim.ID || got["reason"] != tc.wantReason {
				t.Errorf("authn failure payload = %v, want via session-token, userId %s, reason %q", got, kim.ID, tc.wantReason)
			}
			if n := countAuthn(t, app, func(d map[string]any) bool {
				return d["outcome"] == "failure" && d["session"] == sesID
			}); n != 1 {
				t.Errorf("authn failures for %s = %d, want exactly 1", sesID, n)
			}
			ses, err := app.store.Sessions().GetByID(ctx, sesID)
			if err != nil {
				t.Fatal(err)
			}
			if ses.Status != "active" {
				t.Errorf("session row status = %q, want active (the denylist refused, not the row)", ses.Status)
			}
		})
	}
}
