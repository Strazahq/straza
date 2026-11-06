package server

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// copiedTokenMsg is the answer to a key-bound token without its proof.
const copiedTokenMsg = "This session token belongs to a key on the machine that checked in, and this request " +
	"carried no valid proof from that key. Send it from the Straza client on that machine. A copied token works nowhere else."

// TestKeySessionRefresh pins the refresh of a key-bound session: it needs a
// proof over the token from the session's key and carries cnf forward, it
// ends once the machine enrolled a new key, and it ends at the sign-in
// deadline. Each refusal writes exactly one login failure on the session.
func TestKeySessionRefresh(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	harness := map[string]string{"name": "claude-code", "version": "2.1.0"}
	refresh := func(h *keyHolder, token string) (int, map[string]any) {
		return h.call("/v1/checkin", token, map[string]any{"session_token": token, "harness": harness})
	}

	t.Run("needs the proof and carries cnf", func(t *testing.T) {
		h := newKeyHolder(t, base)
		h.enroll(idToken, "sha256:refresh")
		token, sesID := h.mustCheckin()
		code, hdr, out := h.post("/v1/checkin", "", map[string]any{"session_token": token, "harness": harness})
		if code != http.StatusUnauthorized || out["error"] != copiedTokenMsg ||
			!strings.Contains(hdr.Get("WWW-Authenticate"), `error="invalid_dpop_proof"`) {
			t.Fatalf("refresh without a proof = %d %v %v, want the copied-token 401", code, hdr, out)
		}
		code, out = refresh(h, token)
		if code != http.StatusOK || out["session_id"] != sesID {
			t.Fatalf("refresh with the proof = %d %v", code, out)
		}
		claims, err := app.tokens.Verify(out["session_token"].(string))
		if err != nil || claims.CNF != h.thumbprint() || claims.Device != h.device {
			t.Errorf("refreshed claims = %+v %v, want cnf %s", claims, err, h.thumbprint())
		}
	})

	cases := []struct {
		name       string
		arm        func(h *keyHolder)
		wantCode   int
		wantError  string
		wantReason string
	}{
		{"after a re-enrollment", func(h *keyHolder) {
			// A new key enrolls the same fingerprint, so it takes over the row.
			newKeyHolder(t, base).enroll(idToken, "sha256:after-a-re-enrollment")
		}, http.StatusUnauthorized, staleKeyMsg, "the session's key is no longer the device's key"},
		{"after the sign-in deadline", func(h *keyHolder) {
			signInAgo(t, app, h.device, 8*24*time.Hour)
		}, http.StatusForbidden,
			"Your last sign-in on this machine was more than 7 days ago, and your Straza admin requires a new one. " +
				"Run `straza enroll --server " + app.cfg.Server.PublicURL + "` and sign in with your work account.",
			signInPassedReason},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newKeyHolder(t, base)
			h.enroll(idToken, "sha256:"+strings.ReplaceAll(tc.name, " ", "-"))
			token, sesID := h.mustCheckin()
			tc.arm(h)
			code, out := refresh(h, token)
			if code != tc.wantCode || out["error"] != tc.wantError {
				t.Fatalf("refresh = %d %v, want %d %q", code, out, tc.wantCode, tc.wantError)
			}
			failures := matching(t, app, "straza.audit.authn", func(d map[string]any) bool {
				return d["outcome"] == "failure" && d["session"] == sesID
			})
			if len(failures) != 1 {
				t.Fatalf("login failures on the session = %v, want exactly one", failures)
			}
			if f := failures[0]; f["via"] != "session-token" || f["userId"] != kim.ID || f["reason"] != tc.wantReason {
				t.Errorf("login failure = %v, want via session-token and reason %q", f, tc.wantReason)
			}
		})
	}
}

// TestKeyedDeviceRefusesOtherCredentials pins that a keyed device opens a
// session from its key only. The id-token lane naming it and a device token
// for its row are refused with one login failure each, and a body token
// beside a DPoP header is a malformed check-in.
func TestKeyedDeviceRefusesOtherCredentials(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	h := newKeyHolder(t, base)
	h.enroll(idToken, "sha256:keyed")
	deviceToken, err := app.tokens.MintDeviceToken(kim.ID, h.device, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	harness := map[string]string{"name": "claude-code", "version": "2.1.0"}

	cases := []struct {
		name string
		via  string
		body map[string]any
	}{
		{"the id-token lane naming the device", "id-token", map[string]any{"id_token": idToken, "device_id": h.device}},
		{"a device token for the row", "device-token", map[string]any{"device_token": deviceToken}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.body["harness"] = harness
			pred := func(d map[string]any) bool { return d["outcome"] == "failure" && d["via"] == tc.via }
			before := len(matching(t, app, "straza.audit.authn", pred))
			code, out := postJSON(t, base+"/v1/checkin", tc.body)
			if code != http.StatusForbidden || out["error"] != keyOnlyDeviceMsg {
				t.Fatalf("checkin = %d %v, want 403 %q", code, out, keyOnlyDeviceMsg)
			}
			failures := matching(t, app, "straza.audit.authn", pred)
			if len(failures) != before+1 {
				t.Fatalf("login failures via %s = %d, want exactly one more than %d", tc.via, len(failures), before)
			}
			if f := failures[len(failures)-1]; f["userId"] != kim.ID || f["reason"] != "the device checks in with its key" {
				t.Errorf("login failure = %v", f)
			}
			code, out = h.call("/v1/checkin", "", tc.body)
			if code != http.StatusBadRequest || out["error"] != oneCredentialMsg {
				t.Errorf("the same body with a DPoP header = %d %v, want 400 %q", code, out, oneCredentialMsg)
			}
		})
	}
	if d := deviceRow(t, app, h.device); d.KeyCounter != 0 {
		t.Errorf("stored counter = %d, want 0", d.KeyCounter)
	}
}
