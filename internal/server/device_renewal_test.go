package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// TestDeviceCredentialRenewal pins the server half of credential renewal: a
// device-token check-in renews the credential exactly when it is past half
// its life and every gate passed, with one identity.updated renew event per
// renewal; a fresh credential, a refused check-in and the session-token
// refresh lane never mint one.
func TestDeviceCredentialRenewal(t *testing.T) {
	t.Parallel()
	const renewTTL = 90 * time.Second
	app, base := testApp(t, func(c *config.Config) { c.Governance.DeviceTokenTTL = renewTTL })
	u := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	code, enroll := postJSON(t, base+"/v1/enroll", map[string]any{
		"id_token": idToken,
		"device":   map[string]string{"name": "kim-laptop", "platform": "windows", "fingerprint": "host:kim-laptop"},
	})
	if code != http.StatusOK {
		t.Fatalf("enroll = %d %v", code, enroll)
	}
	devID, _ := enroll["device_id"].(string)
	if devID == "" {
		t.Fatalf("enroll response missing device id: %v", enroll)
	}

	checkin := func(auth map[string]any) (int, map[string]any) {
		body := map[string]any{
			"harness":     map[string]string{"name": "claude-code", "version": "2.1.0"},
			"attestation": map[string]any{"managed": false, "hashes": map[string]string{"self": "sha256:aaaaaa"}},
		}
		for k, v := range auth {
			body[k] = v
		}
		return postJSON(t, base+"/v1/checkin", body)
	}
	renewEvents := func() []map[string]any {
		var out []map[string]any
		for _, ev := range identityUpdatedEvents(t, app) {
			if ev["action"] == "renew" {
				out = append(out, ev)
			}
		}
		return out
	}
	// aged mints a credential past half its life: a 2 s lifetime slept past
	// its 1 s midpoint. The verifier accepts 30 s of skew, so a slow box
	// cannot turn "past half-life" into "expired".
	aged := func() string {
		tok, err := app.tokens.MintDeviceToken(u.ID, devID, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(1200 * time.Millisecond)
		return tok
	}
	fresh := func() string {
		tok, err := app.tokens.MintDeviceToken(u.ID, devID, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	setUser := func(status string) func() {
		return func() {
			u.Status = status
			if _, err := app.store.Users().Update(context.Background(), u); err != nil {
				t.Fatal(err)
			}
		}
	}

	cases := []struct {
		name        string
		token       func() string
		before      func() // state change before the check-in, nil = none
		after       func() // restores it, nil = none
		wantCode    int
		wantRenewed bool
	}{
		{name: "fresh credential: session, no renewal, no event", token: fresh, wantCode: http.StatusOK},
		{name: "credential past half-life: renewed once, one renew event", token: aged, wantCode: http.StatusOK, wantRenewed: true},
		{name: "disabled user: refused, nothing minted", token: aged, before: setUser(store.UserDisabled), after: setUser(store.UserActive), wantCode: http.StatusForbidden},
		{name: "denylisted device: refused, nothing minted", token: aged, before: func() { app.denylist.RevokeDevice(devID) }, wantCode: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := tc.token()
			if tc.before != nil {
				tc.before()
			}
			if tc.after != nil {
				defer tc.after()
			}
			eventsBefore := len(renewEvents())
			code, body := checkin(map[string]any{"device_token": tok})
			if code != tc.wantCode {
				t.Fatalf("checkin = %d %v, want %d", code, body, tc.wantCode)
			}
			renewed, _ := body["device_token"].(string)
			if (renewed != "") != tc.wantRenewed {
				t.Fatalf("device_token present = %v, want %v (%v)", renewed != "", tc.wantRenewed, body)
			}
			got := renewEvents()
			wantEvents := eventsBefore
			if tc.wantRenewed {
				wantEvents++
			}
			if len(got) != wantEvents {
				t.Fatalf("renew events = %d, want %d", len(got), wantEvents)
			}
			if !tc.wantRenewed {
				if _, has := body["device_token_expires_in"]; has {
					t.Errorf("device_token_expires_in present without a renewal: %v", body)
				}
				return
			}
			if ttl, _ := body["device_token_expires_in"].(float64); int(ttl) != int(renewTTL.Seconds()) {
				t.Errorf("device_token_expires_in = %v, want %v", ttl, renewTTL.Seconds())
			}
			ev := got[len(got)-1]
			if ev["user"] != u.ID || ev["device"] != devID {
				t.Errorf("renew event = %v, want user %q device %q", ev, u.ID, devID)
			}
			dc, err := app.tokens.VerifyDeviceToken(renewed)
			if err != nil {
				t.Fatalf("renewed credential does not verify: %v", err)
			}
			if dc.Subject != u.ID || dc.Device != devID || dc.Expiry.Sub(dc.IssuedAt) != renewTTL {
				t.Errorf("renewed claims = %+v, want %s/%s over %s", dc, u.ID, devID, renewTTL)
			}
			// The renewed credential starts a session and, being fresh, is
			// not renewed again; the session-token refresh lane never mints.
			code, again := checkin(map[string]any{"device_token": renewed})
			if code != http.StatusOK || again["device_token"] != nil {
				t.Errorf("checkin with the renewed credential = %d %v, want 200 without device_token", code, again)
			}
			code, refreshed := checkin(map[string]any{"session_token": again["session_token"]})
			if code != http.StatusOK || refreshed["device_token"] != nil {
				t.Errorf("session refresh = %d %v, want 200 without device_token", code, refreshed)
			}
			if n := len(renewEvents()); n != wantEvents {
				t.Errorf("renew events after follow-ups = %d, want %d", n, wantEvents)
			}
		})
	}
}
