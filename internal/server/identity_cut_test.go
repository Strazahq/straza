package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// wantOldClientMsg is the old-client sentence, pinned verbatim.
const wantOldClientMsg = "This Straza client is older than the server's sign-in rules, which now need the Straza " +
	"background service on this machine. Update Straza from your software portal, then run `straza enroll`."

// retiredFailures returns the login failures of user whose reason is the
// retired bearer device credential.
func retiredFailures(t *testing.T, app *App, userID string) []map[string]any {
	t.Helper()
	return matching(t, app, "straza.audit.authn", func(d map[string]any) bool {
		return d["action"] == "login" && d["outcome"] == "failure" && d["userId"] == userID &&
			d["reason"] == "the client's bearer device credential is retired"
	})
}

// TestCutEnrollRefusesBearerKit pins the retired bearer lane at enroll. A
// kit enrollment, or one with no client_kind, without a DPoP header has its
// ID token verified as before, then is refused with the old-client 403 and
// exactly one login failure via id-token that names the user, and registers
// no device. An agent's ID token takes the same refusal, so no agent enrolls
// a bearer row.
func TestCutEnrollRefusesBearerKit(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	bot, err := app.store.Users().Create(context.Background(), store.User{Username: "bot", UserType: store.UserTypeAgent})
	if err != nil {
		t.Fatal(err)
	}
	botToken, err := app.tokens.MintIDToken(bot.ID, "straza", time.Minute, "bot", "")
	if err != nil {
		t.Fatal(err)
	}
	device := map[string]string{"name": "kim-laptop", "platform": "linux", "fingerprint": "sha256:cut"}

	cases := []struct {
		name string
		body map[string]any
		user store.User
	}{
		{"a kit enrollment", map[string]any{"id_token": idToken, "client_kind": "kit", "device": device}, kim},
		{"an enrollment with no client kind", map[string]any{"id_token": idToken, "device": device}, kim},
		{"an agent's enrollment", map[string]any{"id_token": botToken, "device": device}, bot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(retiredFailures(t, app, tc.user.ID))
			code, out := postJSON(t, base+"/v1/enroll", tc.body)
			if code != http.StatusForbidden || out["error"] != wantOldClientMsg {
				t.Fatalf("enroll = %d %v, want 403 with the old-client sentence", code, out)
			}
			if _, ok := out["device_token"]; ok {
				t.Errorf("the refusal carries a device token: %v", out)
			}
			failures := retiredFailures(t, app, tc.user.ID)
			if len(failures) != before+1 {
				t.Fatalf("login failures with the retired-credential reason = %d, want exactly one more than %d", len(failures), before)
			}
			if f := failures[len(failures)-1]; f["via"] != "id-token" || f["user"] != tc.user.Username {
				t.Errorf("login failure = %v, want via id-token naming %s", f, tc.user.Username)
			}
			if devs, _ := app.store.Devices().ListByUser(context.Background(), tc.user.ID); len(devs) != 0 {
				t.Errorf("the refused enrollment registered devices %+v", devs)
			}
		})
	}

	t.Run("a bad ID token keeps its 401", func(t *testing.T) {
		code, out := postJSON(t, base+"/v1/enroll", map[string]any{"id_token": "not-a-token", "device": device})
		if code != http.StatusUnauthorized || out["error"] != "login not accepted: run `straza enroll` again" {
			t.Fatalf("enroll = %d %v, want the login 401", code, out)
		}
	})
}

// TestCutCheckinRefusesBearerKitRow pins the retired bearer lane at
// check-in. The device-token lane on a kit row or a row with no kind that
// has no key, and the id-token lane naming such a row by device_id, are
// refused with the old-client 403 and exactly one login failure via the
// lane, naming the user and the harness. An agent's id-token check-in with a
// valid proof that names its own keyless kit row gets the same answer. No
// session starts.
func TestCutCheckinRefusesBearerKitRow(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	kitRow, kitToken := mintDevice(t, app, kim.ID, store.DeviceClientKit, "sha256:kit")
	oldRow, oldToken := mintDevice(t, app, kim.ID, "", "sha256:no-kind")
	bot, botToken := proofAgent(t, app, "bot")
	botRow, _ := mintDevice(t, app, bot.ID, store.DeviceClientKit, "sha256:bot-kit")
	harness := map[string]string{"name": "claude-code", "version": "2.1.0"}

	cases := []struct {
		name  string
		via   string
		user  store.User
		body  map[string]any
		proof bool
	}{
		{"a kit row's device token", "device-token", kim, map[string]any{"device_token": kitToken}, false},
		{"a kindless row's device token", "device-token", kim, map[string]any{"device_token": oldToken}, false},
		{"the id-token lane naming a kit row", "id-token", kim, map[string]any{"id_token": idToken, "device_id": kitRow.ID}, false},
		{"the id-token lane naming a kindless row", "id-token", kim, map[string]any{"id_token": idToken, "device_id": oldRow.ID}, false},
		{"an agent's proof check-in naming its keyless kit row", "id-token", bot,
			map[string]any{"id_token": botToken, "device_id": botRow.ID}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.body["harness"] = harness
			tc.body["attestation"] = map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:x"}}
			before := len(retiredFailures(t, app, tc.user.ID))
			sessions, _ := app.store.Sessions().ListByUser(context.Background(), tc.user.ID)
			var code int
			var out map[string]any
			if tc.proof {
				code, out = newKeyHolder(t, base).call("/v1/checkin", "", tc.body)
			} else {
				code, out = postJSON(t, base+"/v1/checkin", tc.body)
			}
			if code != http.StatusForbidden || out["error"] != wantOldClientMsg {
				t.Fatalf("checkin = %d %v, want 403 with the old-client sentence", code, out)
			}
			failures := retiredFailures(t, app, tc.user.ID)
			if len(failures) != before+1 {
				t.Fatalf("login failures with the retired-credential reason = %d, want exactly one more than %d", len(failures), before)
			}
			if f := failures[len(failures)-1]; f["via"] != tc.via || f["user"] != tc.user.Username || f["harness"] != "claude-code/2.1.0" {
				t.Errorf("login failure = %v, want via %s naming %s and the harness", f, tc.via, tc.user.Username)
			}
			if after, _ := app.store.Sessions().ListByUser(context.Background(), tc.user.ID); len(after) != len(sessions) {
				t.Errorf("sessions = %d after the refusal, want %d", len(after), len(sessions))
			}
		})
	}
}

// TestCutRefreshEndsBearerKitSession pins the retired bearer lane at
// refresh. A live session on a kit row or a row with no kind and no key is
// refused at its next refresh with the old-client 403 and exactly one login
// failure via session-token on that session, so it ends within one token
// lifetime.
func TestCutRefreshEndsBearerKitSession(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)

	for _, kind := range []string{store.DeviceClientKit, ""} {
		name := map[string]string{store.DeviceClientKit: "a kit row", "": "a kindless row"}[kind]
		t.Run(name, func(t *testing.T) {
			d, _ := mintDevice(t, app, kim.ID, kind, "sha256:refresh-"+kind)
			sesID, token := storedSession(t, app, kim.ID, d.ID, "claude-code")
			before := len(retiredFailures(t, app, kim.ID))
			code, out := postJSON(t, base+"/v1/checkin", map[string]any{
				"session_token": token, "harness": map[string]string{"name": "claude-code", "version": "2.1.0"},
			})
			if code != http.StatusForbidden || out["error"] != wantOldClientMsg {
				t.Fatalf("refresh = %d %v, want 403 with the old-client sentence", code, out)
			}
			failures := retiredFailures(t, app, kim.ID)
			if len(failures) != before+1 {
				t.Fatalf("login failures with the retired-credential reason = %d, want exactly one more than %d", len(failures), before)
			}
			if f := failures[len(failures)-1]; f["via"] != "session-token" || f["session"] != sesID || f["user"] != "kim" {
				t.Errorf("login failure = %v, want via session-token on session %s", f, sesID)
			}
		})
	}
}

// TestCutLeavesDevicelessLane is the positive control of the retired
// bearer lane: a person's deviceless id-token check-in under a coding
// harness, and its refresh, still pass with no login failure of the retired
// credential.
func TestCutLeavesDevicelessLane(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	checkin := func(body map[string]any) map[string]any {
		t.Helper()
		body["attestation"] = map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:x"}}
		code, out := postJSON(t, base+"/v1/checkin", body)
		if code != http.StatusOK {
			t.Fatalf("checkin %v = %d %v", body, code, out)
		}
		return out
	}
	claude := map[string]string{"name": "claude-code", "version": "2.1.0"}
	deviceless := checkin(map[string]any{"id_token": idToken, "harness": claude})
	checkin(map[string]any{"session_token": deviceless["session_token"], "harness": claude})
	if n := len(retiredFailures(t, app, kim.ID)); n != 0 {
		t.Errorf("login failures with the retired-credential reason = %d, want none", n)
	}
}
