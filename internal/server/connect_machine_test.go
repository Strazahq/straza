package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// machineSession stores a live session for the user on a machine, the way
// the Straza background service's check-in starts one, so the session
// carries the device that every machine session carries.
func machineSession(t *testing.T, app *App, user store.User) string {
	t.Helper()
	_, tok := storedSession(t, app, user.ID, user.Username+"-laptop", "claude-code")
	return tok
}

// TestConnectWritesFromAMachineSession drives the connect writes that need
// the person: from a session a machine enrollment started, turning the
// agents opt-in on and any write on another user's connection are refused
// with the self-service page and one connection.refused record naming the
// subject, the actor, the server and the reason, and nothing is stored. The
// same session still pastes its own token, turns the opt-in off and
// disconnects itself, and a session that began with the person's own
// sign-in makes every write.
func TestConnectWritesFromAMachineSession(t *testing.T) {
	t.Parallel()
	app, base, _ := testAppCounting(t)
	up := startGatewayUpstream(t)
	alice := seedGatewayUser(t, app, "alice", "dev")
	joe := seedAgent(t, app, "joe", "alice", "dev")
	kim := seedGatewayUser(t, app, "kim", AdminRole)
	installTokenApp(t, app, up, "tokapp", "sponsor")
	catRecompile(t, app)

	aliceSignIn := sessionToken(t, base, "alice")
	aliceMachine := machineSession(t, app, alice)
	kimSignIn := sessionToken(t, base, "kim")
	kimMachine := machineSession(t, app, kim)
	if code := adminReq(t, http.MethodPost, base+"/v1/connect/tokapp", aliceSignIn, map[string]any{"token": "tok-alice"}, nil); code != http.StatusOK {
		t.Fatalf("alice's own paste = %d", code)
	}

	const page = "/self-service/credentials"
	tests := []struct {
		name   string
		tok    string
		method string
		path   string
		body   map[string]any
		want   int
		// refusedFor is the subject of the one connection.refused record a
		// refusal leaves, with its reason.
		refusedFor, reason string
	}{
		{"a machine cannot turn the agents opt-in on", aliceMachine, http.MethodPatch, "/v1/connect/tokapp",
			map[string]any{"allow_agents": true}, http.StatusForbidden, alice.ID, "a session a machine started cannot allow agents"},
		{"a machine cannot paste a token for the agent it sponsors", aliceMachine, http.MethodPost, "/v1/connect/tokapp",
			map[string]any{"token": "tok-joe", "user": "joe"}, http.StatusForbidden, joe.ID, "a session a machine started cannot change another user's connection"},
		{"an admin's machine cannot remove another user's connection", kimMachine, http.MethodDelete, "/v1/connect/tokapp?user=alice",
			nil, http.StatusForbidden, alice.ID, "a session a machine started cannot change another user's connection"},
		{"a machine turns its own opt-in off", aliceMachine, http.MethodPatch, "/v1/connect/tokapp",
			map[string]any{"allow_agents": false}, http.StatusOK, "", ""},
		{"a machine pastes its own token", aliceMachine, http.MethodPost, "/v1/connect/tokapp",
			map[string]any{"token": "tok-alice-2"}, http.StatusOK, "", ""},
		{"the person's sign-in turns the opt-in on", aliceSignIn, http.MethodPatch, "/v1/connect/tokapp",
			map[string]any{"allow_agents": true}, http.StatusOK, "", ""},
		{"the person's sign-in pastes for the agent they sponsor", aliceSignIn, http.MethodPost, "/v1/connect/tokapp",
			map[string]any{"token": "tok-joe", "user": "joe"}, http.StatusOK, "", ""},
		{"an admin's sign-in removes another user's connection", kimSignIn, http.MethodDelete, "/v1/connect/tokapp?user=joe",
			nil, http.StatusOK, "", ""},
		{"a machine disconnects itself", aliceMachine, http.MethodDelete, "/v1/connect/tokapp",
			nil, http.StatusOK, "", ""},
	}
	for _, tc := range tests {
		before := len(identityActions(t, app, "connection.refused"))
		var out map[string]any
		code := adminReq(t, tc.method, base+tc.path, tc.tok, tc.body, &out)
		if code != tc.want {
			t.Fatalf("%s: %s %s = %d %v, want %d", tc.name, tc.method, tc.path, code, out, tc.want)
		}
		refused := identityActions(t, app, "connection.refused")[before:]
		if tc.refusedFor == "" {
			if len(refused) != 0 {
				t.Errorf("%s: refusal records %v, want none", tc.name, refused)
			}
			continue
		}
		if msg, _ := out["error"].(string); !strings.Contains(msg, "your own sign-in in a browser") || !strings.Contains(msg, page) {
			t.Errorf("%s: refusal = %q, want the reason and the self-service page", tc.name, msg)
		}
		if len(refused) != 1 {
			t.Fatalf("%s: refusal records %v, want exactly one", tc.name, refused)
		}
		rec := refused[0]
		setBy := alice.ID
		if tc.tok == kimMachine {
			setBy = kim.ID
		}
		if rec["user"] != tc.refusedFor || rec["setBy"] != setBy || rec["app"] != "tokapp" || rec["reason"] != tc.reason {
			t.Errorf("%s: record = %v, want user %s, setBy %s, app tokapp, reason %q", tc.name, rec, tc.refusedFor, setBy, tc.reason)
		}
	}
	// The refused writes stored nothing: joe's only paste is the one his
	// sponsor's own sign-in made, which kim's sign-in then removed.
	if pastes := identityActions(t, app, "token.connect"); len(pastes) != 3 {
		t.Errorf("token.connect records = %d, want alice's two and the one for joe", len(pastes))
	}
}
