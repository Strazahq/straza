package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// wantCustodyMsg is the custody refusal, pinned verbatim.
const wantCustodyMsg = "Straza refused this machine because its key is kept in your own account instead of by " +
	"the Straza background service, and your Straza admin requires the service. Install Straza from your " +
	"software portal, then run `straza enroll`."

// custodyFailures returns the login failures that carry the custody reason.
func custodyFailures(t *testing.T, app *App) []map[string]any {
	t.Helper()
	return matching(t, app, "straza.audit.authn", func(d map[string]any) bool {
		return d["action"] == "login" && d["outcome"] == "failure" && d["reason"] == "key custody is below governance.minCustody"
	})
}

// minCustody sets governance.minCustody before the app starts.
func minCustody(kind string) func(*config.Config) {
	return func(c *config.Config) { c.Governance.MinCustody = kind }
}

// TestCustodyEnroll pins the custody gate at key enrollment: under account a
// key in the person's own account is refused with the custody 403 and
// exactly one login failure via device-key naming the person, and registers
// no device, while a key the service holds enrolls. Under user both enroll.
func TestCustodyEnroll(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		min     string
		custody string
		refused bool
	}{
		{"account refuses user custody", config.CustodyAccount, config.CustodyUser, true},
		{"account takes account custody", config.CustodyAccount, config.CustodyAccount, false},
		{"user takes user custody", config.CustodyUser, config.CustodyUser, false},
		{"user takes account custody", config.CustodyUser, config.CustodyAccount, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, base := testApp(t, minCustody(tc.min))
			kim := seedIdentity(t, app)
			idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
			body := keyEnrollBody(idToken, "sha256:custody")
			body["custody"] = map[string]string{"kind": tc.custody, "platform": "linux/amd64", "version": "0.2.0"}
			code, out := newKeyHolder(t, base).call("/v1/enroll", "", body)
			devs, _ := app.store.Devices().ListByUser(context.Background(), kim.ID)
			if !tc.refused {
				if code != http.StatusOK || len(devs) != 1 {
					t.Fatalf("enroll = %d %v with devices %+v, want 200 and one device", code, out, devs)
				}
				if n := len(custodyFailures(t, app)); n != 0 {
					t.Errorf("custody failures = %d, want none", n)
				}
				return
			}
			if code != http.StatusForbidden || out["error"] != wantCustodyMsg {
				t.Fatalf("enroll = %d %v, want 403 with the custody sentence", code, out)
			}
			if len(devs) != 0 {
				t.Errorf("the refused enrollment registered devices %+v", devs)
			}
			failures := custodyFailures(t, app)
			if len(failures) != 1 {
				t.Fatalf("custody failures = %v, want exactly one", failures)
			}
			if f := failures[0]; f["via"] != "device-key" || f["user"] != "kim" || f["userId"] != kim.ID {
				t.Errorf("login failure = %v, want via device-key naming kim", f)
			}
		})
	}
}

// TestCustodyEnrollReplayWritesOneRecord pins that a refused key enrollment
// spends its proof first: the same request sent again with the same proof
// gets the replayed-proof 401, so the custody refusal and the person-only
// refusal each write exactly one record.
func TestCustodyEnrollReplayWritesOneRecord(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, minCustody(config.CustodyAccount))
	kim := seedIdentity(t, app)
	userHeld := keyEnrollBody(loginDeviceFlow(t, base, "kim", "hunter2!"), "sha256:replay-custody")
	userHeld["custody"] = map[string]string{"kind": "user"}
	bot, botToken := proofAgent(t, app, "bot")
	const replayed = "the DPoP proof was not accepted: jti was already used. Sign a fresh proof with the key you are enrolling."

	cases := []struct {
		name       string
		body       map[string]any
		user       store.User
		wantError  string
		wantReason string
	}{
		{"the custody refusal", userHeld, kim, wantCustodyMsg, custodyBelowReason},
		{"the person-only refusal", keyEnrollBody(botToken, "sha256:replay-agent"), bot,
			"the user bot is an agent, and only a person can enroll a machine key. " +
				"An AI agent or a service account checks in with its own client credentials instead.",
			"only a person can enroll a machine key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newKeyHolder(t, base)
			h.send("/v1/enroll", "", tc.body) // the nonce challenge
			captured := h.proof("/v1/enroll", "", 0)
			if code, _, out := h.post("/v1/enroll", captured, tc.body); code != http.StatusForbidden || out["error"] != tc.wantError {
				t.Fatalf("enroll = %d %v, want 403 %q", code, out, tc.wantError)
			}
			if code, _, out := h.post("/v1/enroll", captured, tc.body); code != http.StatusUnauthorized || out["error"] != replayed {
				t.Fatalf("replayed enroll = %d %v, want 401 %q", code, out, replayed)
			}
			failures := matching(t, app, "straza.audit.authn", func(d map[string]any) bool {
				return d["action"] == "login" && d["outcome"] == "failure" && d["userId"] == tc.user.ID
			})
			if len(failures) != 1 {
				t.Fatalf("login failures of %s = %v, want exactly one", tc.user.Username, failures)
			}
			if f := failures[0]; f["via"] != "device-key" || f["user"] != tc.user.Username || f["reason"] != tc.wantReason {
				t.Errorf("login failure = %v, want via device-key with reason %q", f, tc.wantReason)
			}
		})
	}
}

// TestCustodyEndsSessions pins the custody gate in keyDeviceRefusal: once
// governance.minCustody ranks above a key's custody, its next key check-in
// and the next refresh of its session are refused with the custody 403 and
// exactly one login failure each, via device-key and via session-token on
// the session. A keyed row with no custody ranks below user too.
func TestCustodyEndsSessions(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, minCustody(config.CustodyUser))
	kim := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	harness := map[string]string{"name": "claude-code", "version": "2.1.0"}

	userHeld := newKeyHolder(t, base)
	body := keyEnrollBody(idToken, "sha256:user-held")
	body["custody"] = map[string]string{"kind": "user", "platform": "linux/amd64", "version": "0.2.0"}
	code, out := userHeld.call("/v1/enroll", "", body)
	if code != http.StatusOK {
		t.Fatalf("enroll = %d %v", code, out)
	}
	userHeld.device = out["device_id"].(string)
	token, sesID := userHeld.mustCheckin()

	noCustody := newKeyHolder(t, base)
	noCustody.enroll(idToken, "sha256:no-custody")
	d := deviceRow(t, app, noCustody.device)
	d.Custody = ""
	if _, err := app.store.Devices().Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		raise   bool
		send    func() (int, map[string]any)
		via     string
		session string
	}{
		{"a keyed row with no custody at its check-in", false, noCustody.checkin, "device-key", ""},
		{"user custody at a refresh after the raise", true, func() (int, map[string]any) {
			return userHeld.call("/v1/checkin", token, map[string]any{"session_token": token, "harness": harness})
		}, "session-token", sesID},
		{"user custody at a key check-in after the raise", true, userHeld.checkin, "device-key", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.raise {
				app.cfg.Governance.MinCustody = config.CustodyAccount
			}
			before := len(custodyFailures(t, app))
			code, out := tc.send()
			if code != http.StatusForbidden || out["error"] != wantCustodyMsg {
				t.Fatalf("answer = %d %v, want 403 with the custody sentence", code, out)
			}
			failures := custodyFailures(t, app)
			if len(failures) != before+1 {
				t.Fatalf("custody failures = %d, want exactly one more than %d", len(failures), before)
			}
			f := failures[len(failures)-1]
			if f["via"] != tc.via || f["user"] != "kim" || f["userId"] != kim.ID || f["harness"] != "claude-code/2.1.0" {
				t.Errorf("login failure = %v, want via %s naming kim and the harness", f, tc.via)
			}
			if tc.session != "" && f["session"] != tc.session {
				t.Errorf("login failure session = %v, want %s", f["session"], tc.session)
			}
		})
	}
	serviceHeld := newKeyHolder(t, base)
	serviceHeld.enroll(idToken, "sha256:service-held")
	if code, out := serviceHeld.checkin(); code != http.StatusOK {
		t.Errorf("a key the service holds after the raise = %d %v, want 200", code, out)
	}
}

// TestCustodyAdminDevicePayload pins custody in the admin device list: a
// keyed row answers its stored kind, platform and version, and a row with no
// custody leaves the field out.
func TestCustodyAdminDevicePayload(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	h := newKeyHolder(t, base)
	h.enroll(idToken, "sha256:keyed")
	human, _ := mintDevice(t, app, kim.ID, store.DeviceClientHuman, "sha256:human")
	tok, _ := checkinToken(t, app, base)

	var devs []map[string]any
	if code := adminReq(t, "GET", base+"/v1/admin/users/"+kim.ID+"/devices", tok, nil, &devs); code != http.StatusOK {
		t.Fatalf("devices list = %d", code)
	}
	if len(devs) != 2 {
		t.Fatalf("devices = %v, want two", devs)
	}
	for _, d := range devs {
		custody, has := d["custody"]
		switch d["id"] {
		case h.device:
			c, _ := custody.(map[string]any)
			if c["kind"] != "account" || c["platform"] != "linux/amd64" || c["version"] != "0.1.0" || len(c) != 3 {
				t.Errorf("keyed row custody = %v, want kind account, platform linux/amd64 and version 0.1.0", custody)
			}
		case human.ID:
			if has {
				t.Errorf("the human row carries custody %v, want the field left out", custody)
			}
		default:
			t.Errorf("unexpected device %v", d)
		}
	}
}
