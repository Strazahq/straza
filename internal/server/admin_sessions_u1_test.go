package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// TestSessionSelfRevokeExpiredToken pins the answers of the self revoke to
// a token it cannot use. An expired session token hears that it cannot end
// a session, how its session ends, and how to start a new one, and the
// session stays open when a newer token of it is live. An expired ID token
// and an expired device credential each hear that they name no session.
// Any other token, and an expired token this server did not sign, keep the
// sentence that only a session token can end its own session, and a live
// session token still revokes.
func TestSessionSelfRevokeExpiredToken(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	other, _ := testApp(t)
	kim := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	live, liveID := checkinToken(t, app, base)
	past := time.Now().Add(-time.Hour)
	mint := func(tok string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}

	sessionMsg := func(id string) string {
		return "token rejected: this session token has expired, so it cannot end a session. " +
			"Session " + id + " ends by itself once no client refreshes it or at its lifetime limit. " +
			"The session token that is live now ends it at once, and so does an administrator with strazactl sessions revoke " + id + ". " +
			"Run strazactl login to start a new session."
	}
	const (
		idTokenMsg = "token rejected: this ID token has expired, and an ID token names no session, so there is nothing for it to end. " +
			"Only a session token ends its own session. A person runs strazactl login to sign in again, " +
			"and an AI agent requests a new token with a fresh client assertion."
		deviceMsg = "token rejected: this device credential has expired, and a device credential names no session, so there is nothing for it to end. " +
			"Only a session token ends its own session. Run strazactl login, or straza enroll on a machine with the straza client, for a new device credential."
		notSessionMsg = "token rejected: only a session token can end its own session"
	)
	cases := []struct {
		name   string
		bearer string
		code   int
		says   string
	}{
		{"a session token that expired an hour ago", signedSessionToken(t, app, base, "ses-expired", past), http.StatusUnauthorized, sessionMsg("ses-expired")},
		{"an expired copy of a token whose session a newer token still holds", signedSessionToken(t, app, base, liveID, past), http.StatusUnauthorized, sessionMsg(liveID)},
		{"an expired ID token", mint(app.tokens.MintIDToken(kim.ID, "straza", -time.Hour, "kim", "kim@x.io")), http.StatusUnauthorized, idTokenMsg},
		{"an expired device credential", mint(app.tokens.MintDeviceToken(kim.ID, "dev-1", -time.Hour)), http.StatusUnauthorized, deviceMsg},
		{"an expired approver device token", mint(app.tokens.MintApproverToken(kim.ID, "apd-1", -time.Hour)), http.StatusUnauthorized, notSessionMsg},
		{"an expired session token under another server's key", signedSessionToken(t, other, base, "ses-forged", past), http.StatusUnauthorized, notSessionMsg},
		{"a live ID token, which names no session", idToken, http.StatusUnauthorized, notSessionMsg},
		{"a malformed token", "not-a-token", http.StatusUnauthorized, notSessionMsg},
		{"a live session token, the positive control", live, http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, out := callJSON(t, http.MethodPost, base+"/v1/session/revoke", tc.bearer, nil)
			if code != tc.code {
				t.Fatalf("self revoke = %d %v, want %d", code, out, tc.code)
			}
			if tc.says != "" && out["error"] != tc.says {
				t.Errorf("error = %q, want %q", out["error"], tc.says)
			}
			if tc.code == http.StatusOK && (out["status"] != "revoked" || out["session"] != liveID) {
				t.Errorf("answer = %v, want status revoked for session %s", out, liveID)
			}
			ses, err := app.store.Sessions().GetByID(context.Background(), liveID)
			if want := map[bool]string{true: store.SessionRevoked, false: store.SessionActive}[tc.code == http.StatusOK]; err != nil || ses.Status != want {
				t.Errorf("session %s after the answer = %q (%v), want %s", liveID, ses.Status, err, want)
			}
		})
	}
}
