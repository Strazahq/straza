package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestConnectRoutesJudgeTheSession pins that the connect routes and GET
// /v1/self/servers judge a session as the admin plane does, on every
// request: a disabled, deleted, locked or revoked user, a revoked session
// and a revoked device are refused with the admin plane's words and one
// login failure record per request, a user row that cannot be read while
// the store answers is refused in words that say so, and a store outage
// answers 503 with no record. It runs for a person's console session and
// for an AI agent's kit session, because straza connect calls these routes
// on the agent's own session, so an active agent passes as a person does.
func TestConnectRoutesJudgeTheSession(t *testing.T) {
	t.Parallel()
	app, base, fs := testAppFault(t)
	ctx := context.Background()
	routes := []adminRoute{
		{http.MethodGet, "/v1/connect", http.StatusOK},
		{http.MethodGet, "/v1/self/servers", http.StatusOK},
		{http.MethodPost, "/v1/connect/no-such-server", http.StatusNotFound},
		{http.MethodPatch, "/v1/connect/no-such-server", http.StatusNotFound},
		{http.MethodDelete, "/v1/connect/no-such-server", http.StatusNotFound},
		{http.MethodPost, "/v1/connect/callback", http.StatusBadRequest},
	}
	callers := []struct {
		name, userType, client, harness string
	}{
		{"person", store.UserTypeHuman, store.DeviceClientHuman, "console"},
		{"agent", store.UserTypeAgent, store.DeviceClientKit, "claude-code"},
	}
	const disabled = "user is disabled. Contact your administrator"
	cases := []struct {
		name         string
		arm          func(u store.User, device, session string)
		code         int
		says, reason string
	}{
		{"active", func(store.User, string, string) {}, 0, "", ""},
		{"disabled in the store with the session row still active", func(u store.User, _, _ string) {
			u.Status = store.UserDisabled
			if _, err := app.store.Users().Update(ctx, u); err != nil {
				t.Fatal(err)
			}
		}, http.StatusForbidden, disabled, "user is disabled"},
		{"deleted with the session row still active", func(u store.User, _, _ string) {
			if _, err := app.store.Users().SoftDelete(ctx, u.ID); err != nil {
				t.Fatal(err)
			}
		}, http.StatusForbidden, disabled, "user is disabled"},
		{"locked, on the denylist", func(u store.User, _, _ string) { app.denylist.revokeUser(u.ID) },
			http.StatusForbidden, revokedIdentityMsg, "device or user revoked"},
		{"with the session on the denylist", func(_ store.User, _, session string) { app.denylist.revokeSession(session) },
			http.StatusUnauthorized, "session is no longer active", "session is no longer active"},
		{"with the device on the denylist", func(_ store.User, device, _ string) { app.denylist.RevokeDevice(device) },
			http.StatusForbidden, revokedIdentityMsg, "device or user revoked"},
		{"while the users read is down", func(store.User, string, string) { fs.arm("users", pgDown) },
			http.StatusServiceUnavailable, loginOutageBody, ""},
		{"whose row cannot be read while the store answers", func(store.User, string, string) { fs.arm("users", errors.New("boom")) },
			http.StatusForbidden, unreadSessionUserMsg, "user record could not be read"},
	}
	for ci, c := range callers {
		for i, tc := range cases {
			t.Run(c.name+" "+tc.name, func(t *testing.T) {
				name := fmt.Sprintf("conn%d%d", ci, i)
				u, err := app.store.Users().Create(ctx, store.User{Username: name, Email: name + "@x.io", UserType: c.userType})
				if err != nil {
					t.Fatal(err)
				}
				d, deviceToken := mintDevice(t, app, u.ID, c.client, "fp-"+name)
				code, checkin := checkinDeviceAs(t, base, deviceToken, c.harness, "1")
				tok, _ := checkin["session_token"].(string)
				sesID, _ := checkin["session_id"].(string)
				if code != http.StatusOK || tok == "" {
					t.Fatalf("%s check-in = %d %v", c.harness, code, checkin)
				}
				tc.arm(u, d.ID, sesID)
				defer fs.disarm()
				for _, rt := range routes {
					before := len(authnFailures(t, app, u.ID, ""))
					code, hdr, out := callJSON(t, rt.method, base+rt.path, tok, nil)
					msg, _ := out["error"].(string)
					recs := authnFailures(t, app, u.ID, "")
					switch tc.code {
					case 0:
						if code != rt.pass {
							t.Errorf("%s %s = %d %q, want %d", rt.method, rt.path, code, msg, rt.pass)
						}
						if len(recs) != before {
							t.Errorf("%s %s wrote a login failure for an active user: %v", rt.method, rt.path, recs[before:])
						}
					case http.StatusServiceUnavailable:
						requireOutage(t, rt.path, code, hdr, out)
						if len(recs) != before {
							t.Errorf("%s %s wrote a login failure for an outage: %v", rt.method, rt.path, recs[before:])
						}
					default:
						if code != tc.code || msg != tc.says {
							t.Errorf("%s %s = %d %q, want %d %q", rt.method, rt.path, code, msg, tc.code, tc.says)
						}
						if len(recs) != before+1 {
							t.Fatalf("%s %s wrote %d login failures, want exactly 1", rt.method, rt.path, len(recs)-before)
						}
						if rec := recs[len(recs)-1]; rec["reason"] != tc.reason || rec["via"] != "session-token" || rec["session"] != sesID || rec["outcome"] != "failure" {
							t.Errorf("record = %v, want reason %q via session-token session %s", rec, tc.reason, sesID)
						}
					}
				}
			})
		}
	}
}
