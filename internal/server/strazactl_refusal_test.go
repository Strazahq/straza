package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

// wantStrazactlMsg is the refusal of strazactl's sign-in, its device
// credential and its sessions, pinned verbatim.
const wantStrazactlMsg = "strazactl no longer signs in, because its sign-in kept an administrator credential in a file " +
	"that any program running as you could copy. Mint an admin API token in the console under Settings, API tokens, " +
	"and run strazactl with STRAZA_API_TOKEN set to it."

// strazactlFailures returns the login failures of the user whose reason is
// that strazactl no longer signs in.
func strazactlFailures(t *testing.T, app *App, userID string) []map[string]any {
	t.Helper()
	return authnFailures(t, app, userID, "strazactl no longer signs in")
}

// storedSession stores a live session of harness for the user on deviceID,
// empty for none, the way a check-in of that client started it, and answers
// its id and token.
func storedSession(t *testing.T, app *App, userID, deviceID, harness string) (string, string) {
	t.Helper()
	ses, err := app.store.Sessions().Create(context.Background(), store.Session{
		UserID: userID, DeviceID: deviceID, HarnessName: harness, HarnessVersion: "2.1.0", AttestationLevel: "advisory",
	})
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := app.tokens.Mint(authn.Claims{Subject: userID, Session: ses.ID, Device: deviceID,
		Harness: harness + "/2.1.0", Attestation: "advisory"})
	if err != nil {
		t.Fatal(err)
	}
	return ses.ID, tok
}

// TestStrazactlNoLongerSignsIn pins that strazad refuses strazactl's
// sign-in, its device credential and a check-in under its name on every
// lane. Each refusal answers 403 with the sentence that names the admin API
// token, writes exactly one login failure with the lane and the reason that
// strazactl no longer signs in, and registers no device. The console's
// id-token check-in and the Straza client's key check-in still pass.
func TestStrazactlNoLongerSignsIn(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	ctx := context.Background()
	att := map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:x"}}
	strazactl := map[string]string{"name": "strazactl", "version": "2.1.0"}
	console := map[string]string{"name": "console", "version": "1"}
	humanRow, humanToken := mintDevice(t, app, kim.ID, store.DeviceClientHuman, "cli:kim-laptop")
	_, kitToken := mintDevice(t, app, kim.ID, store.DeviceClientKit, "fp-kit")
	_, strazactlSession := storedSession(t, app, kim.ID, humanRow.ID, "strazactl")
	_, consoleSession := storedSession(t, app, kim.ID, "", "console")
	key := newKeyHolder(t, base)
	key.enroll(loginDeviceFlow(t, base, "kim", "hunter2!"), "fp-key")
	checkin := func(body map[string]any) func() (int, map[string]any) {
		return func() (int, map[string]any) {
			body["attestation"] = att
			return postJSON(t, base+"/v1/checkin", body)
		}
	}

	cases := []struct {
		name, via string
		send      func() (int, map[string]any)
	}{
		{"an enrollment for strazactl", "id-token", func() (int, map[string]any) {
			return postJSON(t, base+"/v1/enroll", map[string]any{
				"id_token": loginDeviceFlow(t, base, "kim", "hunter2!"), "client_kind": "human",
				"device": map[string]string{"name": "strazactl@kim-laptop", "platform": "linux", "fingerprint": "cli:kim-laptop-2"},
			})
		}},
		{"an id-token check-in under strazactl", "id-token",
			checkin(map[string]any{"id_token": loginDeviceFlow(t, base, "kim", "hunter2!"), "harness": strazactl})},
		{"strazactl's device credential under strazactl", "device-token",
			checkin(map[string]any{"device_token": humanToken, "harness": strazactl})},
		{"strazactl's device credential under console", "device-token",
			checkin(map[string]any{"device_token": humanToken, "harness": console})},
		{"a kit device credential under strazactl", "device-token",
			checkin(map[string]any{"device_token": kitToken, "harness": strazactl})},
		{"the refresh of a strazactl session", "session-token",
			checkin(map[string]any{"session_token": strazactlSession, "harness": strazactl})},
		{"the refresh of a console session under strazactl", "session-token",
			checkin(map[string]any{"session_token": consoleSession, "harness": strazactl})},
		{"a key check-in under strazactl", "device-key", func() (int, map[string]any) {
			key.counter++
			body := key.checkinBody(key.counter)
			body["harness"] = strazactl
			return key.call("/v1/checkin", "", body)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(strazactlFailures(t, app, kim.ID))
			devices, _ := app.store.Devices().ListByUser(ctx, kim.ID)
			code, out := tc.send()
			if code != http.StatusForbidden || out["error"] != wantStrazactlMsg {
				t.Fatalf("answer = %d %v, want 403 with the strazactl sentence", code, out)
			}
			if _, ok := out["device_token"]; ok {
				t.Errorf("the refusal carries a device token: %v", out)
			}
			failures := strazactlFailures(t, app, kim.ID)
			if len(failures) != before+1 {
				t.Fatalf("login failures with the strazactl reason = %d, want exactly one more than %d", len(failures), before)
			}
			if f := failures[len(failures)-1]; f["via"] != tc.via {
				t.Errorf("login failure = %v, want via %s", f, tc.via)
			}
			if after, _ := app.store.Devices().ListByUser(ctx, kim.ID); len(after) != len(devices) {
				t.Errorf("devices = %d after the refusal, want %d", len(after), len(devices))
			}
		})
	}

	t.Run("the console's id-token check-in passes", func(t *testing.T) {
		code, out := checkin(map[string]any{"id_token": loginDeviceFlow(t, base, "kim", "hunter2!"), "harness": console})()
		if code != http.StatusOK || out["device_token"] != nil {
			t.Fatalf("console check-in = %d %v, want 200 with no device token", code, out)
		}
	})
	t.Run("the Straza client's key check-in passes", func(t *testing.T) {
		key.mustCheckin()
	})
}

// TestStrazactlSessionEndsEverywhere pins that a strazactl session alive at
// the upgrade is refused on its next request wherever a session is judged:
// the admin plane, the connect routes, a decide route and the self route.
// Each refusal answers 403 with the strazactl sentence and writes exactly
// one login failure via session-token on that session. A console session of
// the same administrator still passes.
func TestStrazactlSessionEndsEverywhere(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	sesID, tok := storedSession(t, app, kim.ID, "", "strazactl")
	routes := []struct{ method, path string }{
		{http.MethodGet, "/v1/admin/users"},
		{http.MethodGet, "/v1/connect"},
		{http.MethodPost, "/v1/admin/approvals/no-such-request/approve"},
		{http.MethodGet, "/v1/self"},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			before := len(strazactlFailures(t, app, kim.ID))
			code, _, out := callJSON(t, rt.method, base+rt.path, tok, nil)
			if code != http.StatusForbidden || out["error"] != wantStrazactlMsg {
				t.Fatalf("answer = %d %v, want 403 with the strazactl sentence", code, out)
			}
			failures := strazactlFailures(t, app, kim.ID)
			if len(failures) != before+1 {
				t.Fatalf("login failures with the strazactl reason = %d, want exactly one more than %d", len(failures), before)
			}
			if f := failures[len(failures)-1]; f["via"] != "session-token" || f["session"] != sesID {
				t.Errorf("login failure = %v, want via session-token on session %s", f, sesID)
			}
		})
	}
	t.Run("a console session passes", func(t *testing.T) {
		_, console := storedSession(t, app, kim.ID, "", "console")
		if code, _, out := callJSON(t, http.MethodGet, base+"/v1/admin/users", console, nil); code != http.StatusOK {
			t.Fatalf("console session on the admin plane = %d %v, want 200", code, out)
		}
	})
}
