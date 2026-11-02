package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/store"
)

// The sign-in refusals of a person's key enrollment, pinned verbatim.
const (
	wantOtherKeyMsg = "Straza refused this sign-in because it was made for a different enrollment. " +
		"Run `straza enroll` again and finish the sign-in in the browser it opens."
	wantNoAuthTimeMsg = "Straza refused this sign-in because the identity provider sent no auth_time claim, " +
		"so Straza cannot tell that you signed in just now. Ask IT to add the auth_time claim to the ID token " +
		"of the Straza app, then run `straza enroll` again."
	wantCodeLaneMsg = "Straza refused this sign-in because your Straza admin requires a browser sign-in, and a " +
		"sign-in code cannot show which machine it was approved for. Run `straza enroll` in a desktop session on " +
		"this machine, and update Straza first if it does not open a browser."
	wantCodeMachineMsg = "Straza refused this machine because it was enrolled with a sign-in code, and your " +
		"Straza admin now requires a browser sign-in. Run `straza enroll` in a desktop session on this machine, " +
		"and update Straza first if it does not open a browser."
	wantDoorMsg = "Straza refused this session because a sign-in token alone no longer starts a session on this " +
		"server. Install Straza on this machine and run `straza enroll`."
	wantOtherProgramMsg = "Straza refused this sign-in because it was made for another Straza program. " +
		"Run `straza enroll` on this machine and finish the sign-in it starts."
)

// wantPastDeadlineMsg is the refusal of a sign-in made at at, already older
// than governance.signInMaxAge when the enrollment arrives.
func wantPastDeadlineMsg(at time.Time) string {
	return "Straza refused this sign-in because your identity provider says you last signed in at " +
		at.UTC().Format("2006-01-02 15:04:05 UTC") + ", longer ago than your Straza admin allows. " +
		"Sign out at your identity provider, then run `straza enroll` again and sign in."
}

// wantStaleMsg is the refusal of a sign-in made at at, more than 10 minutes
// before the enrollment.
func wantStaleMsg(at time.Time) string {
	return "Straza refused this sign-in because your identity provider says you last signed in at " +
		at.UTC().Format("2006-01-02 15:04:05 UTC") + ", more than 10 minutes ago. " +
		"Run `straza enroll` again and sign in when the browser asks."
}

// wantAheadMsg is the refusal of a sign-in time ahead of the server clock.
func wantAheadMsg(at time.Time) string {
	return "Straza refused this sign-in because your identity provider says you signed in at " +
		at.UTC().Format("2006-01-02 15:04:05 UTC") + ", which is ahead of the Straza server's clock. " +
		"Ask your Straza admin to check the clocks of the Straza server and the identity provider, " +
		"then run `straza enroll` again."
}

// wantAgentProofMsg is the refusal of an agent's id-token check-in without
// a key proof on enterprise.
func wantAgentProofMsg(username, kind string) string {
	return "Straza refused this session because the user " + username + " is " + kind +
		", and this server accepts its check-in only with a key proof. Install a current Straza on the " +
		"machine it runs on, with `straza service` running, and enroll it there with `straza enroll --headless`."
}

// keyNonce is a browser sign-in's nonce for the key whose thumbprint is
// thumb, with a random part of n bytes.
func keyNonce(t *testing.T, thumb string, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return thumb + "." + base64.RawURLEncoding.EncodeToString(b)
}

// signInToken mints the built-in issuer's ID token for u with the sign-in
// facts f, for the straza client.
func signInToken(t *testing.T, app *App, u store.User, f authn.LoginFacts) string {
	t.Helper()
	return signInTokenFor(t, app, u, "straza", f)
}

// signInTokenFor is signInToken for the client aud.
func signInTokenFor(t *testing.T, app *App, u store.User, aud string, f authn.LoginFacts) string {
	t.Helper()
	idt, err := app.tokens.MintIDTokenFor(u.ID, aud, 5*time.Minute, u.Username, u.Email, f)
	if err != nil {
		t.Fatal(err)
	}
	return idt
}

// enrolledRecords returns the device.enrolled records of user.
func enrolledRecords(t *testing.T, app *App, user string) []map[string]any {
	t.Helper()
	return matching(t, app, "straza.audit.identity", func(d map[string]any) bool {
		return d["action"] == "device.enrolled" && d["user"] == user
	})
}

// signInFailures returns the login failures of user via via.
func signInFailures(t *testing.T, app *App, user, via string) []map[string]any {
	t.Helper()
	return matching(t, app, "straza.audit.authn", func(d map[string]any) bool {
		return d["action"] == "login" && d["outcome"] == "failure" && d["userId"] == user && d["via"] == via
	})
}

// storedSignInOf reads the sign_in a device row's custody column holds.
func storedSignInOf(t *testing.T, app *App, device string) string {
	t.Helper()
	var c map[string]string
	if err := json.Unmarshal([]byte(deviceRow(t, app, device).Custody), &c); err != nil {
		t.Fatalf("custody of %s: %v", device, err)
	}
	return c["sign_in"]
}

// TestKeyEnrollSignIn pins the sign-in judgments of a person's key
// enrollment, in their order: the nonce names the key being enrolled, a
// browser sign-in is at most 10 minutes old with 1 minute of tolerance for a
// clock ahead, the lane ranks at governance.minSignIn or above, the sign-in
// is younger than governance.signInMaxAge, and then the custody floor. An
// auth_time of 0 or below is a sign-in in 1970 or before, so it is stale on
// both lanes. A refusal answers 403 with its sentence, writes exactly one
// login failure via device-key with its reason and registers nothing. An
// enrollment stores the lane strazad judged, takes the sign-in time from
// auth_time when it is not more than 1 minute ahead, counts the deadline
// from it, and writes exactly one device.enrolled record with every field.
func TestKeyEnrollSignIn(t *testing.T) {
	t.Parallel()
	type rig struct {
		app  *App
		base string
		kim  store.User
	}
	// The rigs are browser and code, named after their minSignIn, and short,
	// whose minSignIn is code and whose signInMaxAge is 5 minutes.
	rigs := map[string]rig{}
	for _, name := range []string{config.SignInBrowser, config.SignInCode, "short"} {
		app, base := testApp(t, func(c *config.Config) {
			c.Governance.MinSignIn = name
			switch name {
			case config.SignInBrowser:
				c.Governance.MinCustody = config.CustodyAccount
			case "short":
				c.Governance.MinSignIn, c.Governance.SignInMaxAge = config.SignInCode, 5*time.Minute
			}
		})
		rigs[name] = rig{app: app, base: base, kim: seedIdentity(t, app)}
	}
	now := time.Now().Truncate(time.Second)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }

	cases := []struct {
		name    string
		rig     string
		facts   func(t *testing.T, h, other *keyHolder) authn.LoginFacts
		custody string
		// lane is the lane an accepted enrollment stores, "" for a refusal.
		lane     string
		signedAt time.Time // zero means now
		msg      string
		reason   string
	}{
		{"a browser sign-in for this key", config.SignInBrowser, func(t *testing.T, h, _ *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{Nonce: keyNonce(t, h.thumbprint(), 16), AuthTime: ago(30 * time.Second)}
		}, "", config.SignInBrowser, ago(30 * time.Second), "", ""},
		{"a browser sign-in 50 seconds ahead", config.SignInBrowser, func(t *testing.T, h, _ *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{Nonce: keyNonce(t, h.thumbprint(), 32), AuthTime: now.Add(50 * time.Second)}
		}, "", config.SignInBrowser, now.Add(50 * time.Second), "", ""},
		{"a browser sign-in under code", config.SignInCode, func(t *testing.T, h, _ *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{Nonce: keyNonce(t, h.thumbprint(), 16), AuthTime: now}
		}, "", config.SignInBrowser, now, "", ""},
		{"a code sign-in an hour old under code", config.SignInCode, func(*testing.T, *keyHolder, *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{AuthTime: ago(time.Hour)}
		}, "user", config.SignInCode, ago(time.Hour), "", ""},
		{"a code sign-in 3 minutes ahead under code", config.SignInCode, func(*testing.T, *keyHolder, *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{AuthTime: now.Add(3 * time.Minute)}
		}, "", config.SignInCode, time.Time{}, "", ""},
		{"a code sign-in with no auth_time under code", config.SignInCode, func(*testing.T, *keyHolder, *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{}
		}, "", config.SignInCode, time.Time{}, "", ""},
		{"a code sign-in under browser", config.SignInBrowser, func(*testing.T, *keyHolder, *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{AuthTime: now}
		}, "", "", time.Time{}, wantCodeLaneMsg, "the sign-in lane is below governance.minSignIn"},
		{"a nonce for another key", config.SignInBrowser, func(t *testing.T, _, other *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{Nonce: keyNonce(t, other.thumbprint(), 16), AuthTime: now}
		}, "", "", time.Time{}, wantOtherKeyMsg, "the sign-in was made for another key"},
		{"a nonce for another key under code", config.SignInCode, func(t *testing.T, _, other *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{Nonce: keyNonce(t, other.thumbprint(), 16), AuthTime: now}
		}, "", "", time.Time{}, wantOtherKeyMsg, "the sign-in was made for another key"},
		{"a nonce with a short random part", config.SignInBrowser, func(t *testing.T, h, _ *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{Nonce: keyNonce(t, h.thumbprint(), 15), AuthTime: now}
		}, "", "", time.Time{}, wantOtherKeyMsg, "the sign-in was made for another key"},
		{"a nonce that names no key", config.SignInBrowser, func(t *testing.T, _, _ *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{Nonce: "console-" + keyNonce(t, "x", 16), AuthTime: now}
		}, "", "", time.Time{}, wantOtherKeyMsg, "the sign-in was made for another key"},
		{"a nonce with padding", config.SignInBrowser, func(t *testing.T, h, _ *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{Nonce: keyNonce(t, h.thumbprint(), 16) + "==", AuthTime: now}
		}, "", "", time.Time{}, wantOtherKeyMsg, "the sign-in was made for another key"},
		{"a nonce for another key and no auth_time", config.SignInBrowser, func(t *testing.T, _, other *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{Nonce: keyNonce(t, other.thumbprint(), 16)}
		}, "", "", time.Time{}, wantOtherKeyMsg, "the sign-in was made for another key"},
		{"no auth_time", config.SignInBrowser, func(t *testing.T, h, _ *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{Nonce: keyNonce(t, h.thumbprint(), 16)}
		}, "", "", time.Time{}, wantNoAuthTimeMsg, "the sign-in carries no auth_time"},
		{"auth_time 11 minutes old", config.SignInBrowser, func(t *testing.T, h, _ *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{Nonce: keyNonce(t, h.thumbprint(), 16), AuthTime: ago(11 * time.Minute)}
		}, "", "", time.Time{}, wantStaleMsg(ago(11 * time.Minute)), "the sign-in is older than 10 minutes"},
		{"auth_time 2 minutes ahead", config.SignInBrowser, func(t *testing.T, h, _ *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{Nonce: keyNonce(t, h.thumbprint(), 16), AuthTime: now.Add(2 * time.Minute)}
		}, "", "", time.Time{}, wantAheadMsg(now.Add(2 * time.Minute)), "the sign-in time is ahead of the server clock"},
		{"a stale browser sign-in with custody below the floor", config.SignInBrowser, func(t *testing.T, h, _ *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{Nonce: keyNonce(t, h.thumbprint(), 16), AuthTime: ago(11 * time.Minute)}
		}, "user", "", time.Time{}, wantStaleMsg(ago(11 * time.Minute)), "the sign-in is older than 10 minutes"},
		{"a code sign-in with custody below the floor", config.SignInBrowser, func(*testing.T, *keyHolder, *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{AuthTime: now}
		}, "user", "", time.Time{}, wantCodeLaneMsg, "the sign-in lane is below governance.minSignIn"},
		{"a browser sign-in with custody below the floor", config.SignInBrowser, func(t *testing.T, h, _ *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{Nonce: keyNonce(t, h.thumbprint(), 16), AuthTime: now}
		}, "user", "", time.Time{}, wantCustodyMsg, custodyBelowReason},
		{"a code sign-in older than signInMaxAge", config.SignInCode, func(*testing.T, *keyHolder, *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{AuthTime: ago(8 * 24 * time.Hour)}
		}, "", "", time.Time{}, wantPastDeadlineMsg(ago(8 * 24 * time.Hour)), signInPassedReason},
		{"a code sign-in with auth_time 0", config.SignInCode, func(*testing.T, *keyHolder, *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{AuthTime: time.Unix(0, 0)}
		}, "", "", time.Time{}, wantPastDeadlineMsg(time.Unix(0, 0)), signInPassedReason},
		{"a code sign-in with a negative auth_time", config.SignInCode, func(*testing.T, *keyHolder, *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{AuthTime: time.Unix(-3600, 0)}
		}, "", "", time.Time{}, wantPastDeadlineMsg(time.Unix(-3600, 0)), signInPassedReason},
		{"a browser sign-in with auth_time 0", config.SignInBrowser, func(t *testing.T, h, _ *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{Nonce: keyNonce(t, h.thumbprint(), 16), AuthTime: time.Unix(0, 0)}
		}, "", "", time.Time{}, wantStaleMsg(time.Unix(0, 0)), "the sign-in is older than 10 minutes"},
		{"a browser sign-in older than a short signInMaxAge", "short", func(t *testing.T, h, _ *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{Nonce: keyNonce(t, h.thumbprint(), 16), AuthTime: ago(6 * time.Minute)}
		}, "", "", time.Time{}, wantPastDeadlineMsg(ago(6 * time.Minute)), signInPassedReason},
		{"a browser sign-in within a short signInMaxAge", "short", func(t *testing.T, h, _ *keyHolder) authn.LoginFacts {
			return authn.LoginFacts{Nonce: keyNonce(t, h.thumbprint(), 16), AuthTime: ago(time.Minute)}
		}, "", config.SignInBrowser, ago(time.Minute), "", ""},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := rigs[tc.rig]
			h, other := newKeyHolder(t, r.base), newKeyHolder(t, r.base)
			body := keyEnrollBody(signInToken(t, r.app, r.kim, tc.facts(t, h, other)), "sha256:sign-in-"+string(rune('a'+i)))
			if tc.custody != "" {
				body["custody"] = map[string]string{"kind": tc.custody, "platform": "linux/amd64", "version": "0.3.0"}
			}
			devsBefore, _ := r.app.store.Devices().ListByUser(context.Background(), r.kim.ID)
			failsBefore := len(signInFailures(t, r.app, r.kim.ID, "device-key"))
			recsBefore := len(enrolledRecords(t, r.app, r.kim.ID))

			code, out := h.call("/v1/enroll", "", body)
			devs, _ := r.app.store.Devices().ListByUser(context.Background(), r.kim.ID)
			fails := signInFailures(t, r.app, r.kim.ID, "device-key")
			recs := enrolledRecords(t, r.app, r.kim.ID)
			if tc.lane == "" {
				if code != http.StatusForbidden || out["error"] != tc.msg {
					t.Fatalf("enroll = %d %v, want 403 %q", code, out, tc.msg)
				}
				if len(devs) != len(devsBefore) || len(recs) != recsBefore {
					t.Errorf("a refused enrollment registered devices %d to %d and records %d to %d", len(devsBefore), len(devs), recsBefore, len(recs))
				}
				if len(fails) != failsBefore+1 {
					t.Fatalf("login failures = %d, want exactly one more than %d", len(fails), failsBefore)
				}
				if f := fails[len(fails)-1]; f["reason"] != tc.reason || f["user"] != "kim" {
					t.Errorf("login failure = %v, want reason %q naming kim", f, tc.reason)
				}
				return
			}
			if code != http.StatusOK {
				t.Fatalf("enroll = %d %v, want 200", code, out)
			}
			if len(fails) != failsBefore {
				t.Errorf("an accepted enrollment wrote %d login failures", len(fails)-failsBefore)
			}
			device := out["device_id"].(string)
			if got := storedSignInOf(t, r.app, device); got != tc.lane {
				t.Errorf("stored sign_in = %q, want %q", got, tc.lane)
			}
			d := deviceRow(t, r.app, device)
			wantAt := tc.signedAt
			if wantAt.IsZero() {
				if since := time.Since(d.SignedInAt); since < 0 || since > time.Minute {
					t.Errorf("signed_in_at = %s, want now", d.SignedInAt)
				}
				wantAt = d.SignedInAt
			} else if !d.SignedInAt.Equal(wantAt) {
				t.Errorf("signed_in_at = %s, want auth_time %s", d.SignedInAt, wantAt)
			}
			wantLeft := (r.app.cfg.EffectiveSignInMaxAge() - time.Since(wantAt)).Seconds()
			if left, _ := out["sign_in_expires_in"].(float64); left > wantLeft+2 || left < wantLeft-60 {
				t.Errorf("sign_in_expires_in = %v, want about %v counted from the sign-in", left, wantLeft)
			}
			if len(recs) != recsBefore+1 {
				t.Fatalf("device.enrolled records = %d, want exactly one more than %d", len(recs), recsBefore)
			}
			kind := tc.custody
			if kind == "" {
				kind = config.CustodyAccount
			}
			rec := recs[len(recs)-1]
			if rec["device"] != device || rec["signIn"] != tc.lane || rec["custody"] != kind {
				t.Errorf("device.enrolled = %v, want device %s, signIn %s and custody %s", rec, device, tc.lane, kind)
			}
			at, fields := tc.facts(t, h, other).AuthTime, 6
			if at.IsZero() {
				fields = 5
				if _, ok := rec["authTime"]; ok {
					t.Errorf("device.enrolled = %v, want no authTime for a token without auth_time", rec)
				}
			} else if rec["authTime"] != at.UTC().Format(time.RFC3339) {
				t.Errorf("device.enrolled authTime = %v, want %s", rec["authTime"], at.UTC().Format(time.RFC3339))
			}
			if len(rec) != fields {
				t.Errorf("device.enrolled = %v, want only action, user, device, signIn, custody and an authTime when known", rec)
			}
		})
	}
}

// TestKeyEnrollSignInExternal pins that the sign-in judgments read the nonce
// and auth_time of an external identity provider's ID token too, and that
// such a token enrolls with any audience oidc.clientId names, because every
// Straza program shares that one client id at the provider.
func TestKeyEnrollSignInExternal(t *testing.T) {
	t.Parallel()
	idpApp, idpBase := testApp(t)
	idpKim := seedIdentity(t, idpApp)
	platform, base := testApp(t, func(c *config.Config) {
		c.OIDC.Issuer, c.OIDC.ClientID, c.OIDC.JITProvision = idpBase, "shared-client", true
		c.Governance.MinSignIn = config.SignInBrowser
	})

	h, other := newKeyHolder(t, base), newKeyHolder(t, base)
	at := time.Now().Add(-time.Minute).Truncate(time.Second)
	foreign := signInTokenFor(t, idpApp, idpKim, "shared-client", authn.LoginFacts{Nonce: keyNonce(t, other.thumbprint(), 16), AuthTime: at})
	if code, out := h.call("/v1/enroll", "", keyEnrollBody(foreign, "sha256:external-other")); code != http.StatusForbidden || out["error"] != wantOtherKeyMsg {
		t.Fatalf("enroll with a nonce for another key = %d %v, want 403 %q", code, out, wantOtherKeyMsg)
	}
	own := signInTokenFor(t, idpApp, idpKim, "shared-client", authn.LoginFacts{Nonce: keyNonce(t, h.thumbprint(), 16), AuthTime: at})
	code, out := h.call("/v1/enroll", "", keyEnrollBody(own, "sha256:external-own"))
	if code != http.StatusOK {
		t.Fatalf("enroll with a browser sign-in = %d %v", code, out)
	}
	device := out["device_id"].(string)
	if got := storedSignInOf(t, platform, device); got != config.SignInBrowser {
		t.Errorf("stored sign_in = %q, want browser", got)
	}
	if d := deviceRow(t, platform, device); !d.SignedInAt.Equal(at) {
		t.Errorf("signed_in_at = %s, want the provider's auth_time %s", d.SignedInAt, at)
	}
	recs := enrolledRecords(t, platform, out["user_id"].(string))
	if len(recs) != 1 || recs[0]["signIn"] != config.SignInBrowser || recs[0]["authTime"] != at.UTC().Format(time.RFC3339) {
		t.Errorf("device.enrolled records = %v, want one browser record with the provider's auth_time", recs)
	}
}

// TestKeyEnrollClientCannotClaimLane pins that the lane is strazad's own
// judgment: a sign_in a client sends in custody is ignored, so a code
// sign-in that claims browser is stored as code under code and refused under
// browser.
func TestKeyEnrollClientCannotClaimLane(t *testing.T) {
	t.Parallel()
	for _, minimum := range []string{config.SignInCode, config.SignInBrowser} {
		t.Run(minimum, func(t *testing.T) {
			t.Parallel()
			app, base := testApp(t, func(c *config.Config) { c.Governance.MinSignIn = minimum })
			kim := seedIdentity(t, app)
			body := keyEnrollBody(signInToken(t, app, kim, authn.LoginFacts{AuthTime: time.Now()}), "sha256:claims-browser")
			body["custody"] = map[string]string{"kind": "account", "platform": "linux/amd64", "version": "0.3.0", "sign_in": "browser"}
			code, out := newKeyHolder(t, base).call("/v1/enroll", "", body)
			if minimum == config.SignInBrowser {
				if code != http.StatusForbidden || out["error"] != wantCodeLaneMsg {
					t.Fatalf("enroll = %d %v, want the code-lane 403", code, out)
				}
				return
			}
			if code != http.StatusOK {
				t.Fatalf("enroll = %d %v", code, out)
			}
			if got := storedSignInOf(t, app, out["device_id"].(string)); got != config.SignInCode {
				t.Errorf("stored sign_in = %q, want code whatever the client claims", got)
			}
			if recs := enrolledRecords(t, app, kim.ID); len(recs) != 1 || recs[0]["signIn"] != config.SignInCode {
				t.Errorf("device.enrolled = %v, want one record with signIn code", recs)
			}
		})
	}
}

// TestKeyEnrollBuiltinAudience pins that a person's key enrollment takes a
// built-in issuer's ID token only when it was minted for the straza client:
// a token for strazactl or the console is refused with 403 and exactly one
// login failure via device-key, before the nonce is judged, and registers
// nothing.
func TestKeyEnrollBuiltinAudience(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) { c.Governance.MinSignIn = config.SignInCode })
	kim := seedIdentity(t, app)
	const reason = "the sign-in was made for another Straza program"
	cases := []struct {
		name  string
		aud   string
		nonce bool
	}{
		{"a strazactl token", "strazactl", false},
		{"a console token", "console", false},
		{"a console token with a nonce for this key", "console", true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newKeyHolder(t, base)
			f := authn.LoginFacts{AuthTime: time.Now()}
			if tc.nonce {
				f.Nonce = keyNonce(t, h.thumbprint(), 16)
			}
			before := len(signInFailures(t, app, kim.ID, "device-key"))
			body := keyEnrollBody(signInTokenFor(t, app, kim, tc.aud, f), "sha256:audience-"+string(rune('a'+i)))
			code, out := h.call("/v1/enroll", "", body)
			if code != http.StatusForbidden || out["error"] != wantOtherProgramMsg {
				t.Fatalf("enroll = %d %v, want 403 %q", code, out, wantOtherProgramMsg)
			}
			fails := signInFailures(t, app, kim.ID, "device-key")
			if len(fails) != before+1 || fails[len(fails)-1]["reason"] != reason {
				t.Errorf("login failures = %v, want exactly one more with reason %q", fails, reason)
			}
		})
	}
	devs, _ := app.store.Devices().ListByUser(context.Background(), kim.ID)
	if len(devs) != 0 || len(enrolledRecords(t, app, kim.ID)) != 0 {
		t.Errorf("refused enrollments registered devices %+v or records", devs)
	}
}

// TestKeyReenrollWritesDeviceEnrolled pins that a re-enrollment of the same
// fingerprint writes its own device.enrolled record for the same device.
func TestKeyReenrollWritesDeviceEnrolled(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	first := newKeyHolder(t, base)
	first.enroll(signInToken(t, app, kim, authn.LoginFacts{}), "sha256:again")
	second := newKeyHolder(t, base)
	second.enroll(signInToken(t, app, kim, authn.LoginFacts{Nonce: keyNonce(t, second.thumbprint(), 16), AuthTime: time.Now()}), "sha256:again")
	recs := enrolledRecords(t, app, kim.ID)
	if len(recs) != 2 || recs[0]["device"] != first.device || recs[1]["device"] != first.device ||
		recs[0]["signIn"] != config.SignInCode || recs[1]["signIn"] != config.SignInBrowser {
		t.Errorf("device.enrolled records = %v, want a code and a browser record of device %s", recs, first.device)
	}
}

// TestMinSignInEndsSessions pins the sign-in floor in keyDeviceRefusal: once
// governance.minSignIn ranks above a machine's stored lane, its next key
// check-in and the next refresh of its session are refused with the 403 and
// exactly one login failure each, a stored custody without sign_in counts as
// code, and a machine enrolled through the browser keeps checking in.
func TestMinSignInEndsSessions(t *testing.T) {
	t.Parallel()
	app, base := testApp(t, func(c *config.Config) { c.Governance.MinSignIn = config.SignInCode })
	kim := seedIdentity(t, app)
	harness := map[string]string{"name": "claude-code", "version": "2.1.0"}

	code := newKeyHolder(t, base)
	code.enroll(signInToken(t, app, kim, authn.LoginFacts{AuthTime: time.Now()}), "sha256:code")
	token, sesID := code.mustCheckin()
	legacy := newKeyHolder(t, base)
	legacy.enroll(signInToken(t, app, kim, authn.LoginFacts{}), "sha256:legacy")
	d := deviceRow(t, app, legacy.device)
	d.Custody = `{"kind":"account","platform":"linux/amd64","version":"0.2.0"}`
	if _, err := app.store.Devices().Update(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	browser := newKeyHolder(t, base)
	browser.enroll(signInToken(t, app, kim, authn.LoginFacts{Nonce: keyNonce(t, browser.thumbprint(), 16), AuthTime: time.Now()}), "sha256:browser")
	if status, out := legacy.checkin(); status != http.StatusOK {
		t.Fatalf("a row without sign_in under code = %d %v, want 200", status, out)
	}
	app.cfg.Governance.MinSignIn = config.SignInBrowser

	const reason = "the sign-in lane is below governance.minSignIn"
	cases := []struct {
		name    string
		send    func() (int, map[string]any)
		via     string
		session string
	}{
		{"a code lane key check-in", code.checkin, "device-key", ""},
		{"a code lane session refresh", func() (int, map[string]any) {
			return code.call("/v1/checkin", token, map[string]any{"session_token": token, "harness": harness})
		}, "session-token", sesID},
		{"a row without sign_in", legacy.checkin, "device-key", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(signInFailures(t, app, kim.ID, tc.via))
			status, out := tc.send()
			if status != http.StatusForbidden || out["error"] != wantCodeMachineMsg {
				t.Fatalf("answer = %d %v, want 403 %q", status, out, wantCodeMachineMsg)
			}
			fails := signInFailures(t, app, kim.ID, tc.via)
			if len(fails) != before+1 {
				t.Fatalf("login failures = %d, want exactly one more than %d", len(fails), before)
			}
			f := fails[len(fails)-1]
			if f["reason"] != reason || f["user"] != "kim" || f["harness"] != "claude-code/2.1.0" {
				t.Errorf("login failure = %v, want reason %q naming kim and the harness", f, reason)
			}
			if tc.session != "" && f["session"] != tc.session {
				t.Errorf("login failure session = %v, want %s", f["session"], tc.session)
			}
		})
	}
	if status, out := browser.checkin(); status != http.StatusOK {
		t.Errorf("a browser lane key check-in after the raise = %d %v, want 200", status, out)
	}
}

// TestIDTokenDoor pins the id-token lane per profile. On enterprise a
// person's id-token check-in under a harness name other than console and
// self-service is refused, and so is an AI agent's or a service
// account's without a key proof, each with 403 and exactly one login failure
// via id-token. A person under a human client name and an agent with a proof
// still check in, and the person-only harness gate still answers first for
// an agent. Standalone keeps both lanes.
func TestIDTokenDoor(t *testing.T) {
	t.Parallel()
	const (
		doorReason  = "a person's ID token alone does not start a session on enterprise"
		proofReason = "an agent check-in without a key proof is refused on enterprise"
	)
	for _, profile := range []string{config.ProfileEnterprise, config.ProfileStandalone} {
		t.Run(profile, func(t *testing.T) {
			t.Parallel()
			app, base := testApp(t, func(c *config.Config) { c.Profile = profile })
			kim := seedIdentity(t, app)
			bot, botToken := proofAgent(t, app, "bot")
			svc, err := app.store.Users().Create(context.Background(), store.User{Username: "svc", UserType: store.UserTypeService})
			if err != nil {
				t.Fatal(err)
			}
			svcToken := signInToken(t, app, svc, authn.LoginFacts{})
			kimToken := signInToken(t, app, kim, authn.LoginFacts{AuthTime: time.Now()})
			body := func(idToken, harness string) map[string]any {
				b := nhiCheckinBody(idToken)
				b["harness"] = map[string]string{"name": harness, "version": "2.1.0"}
				return b
			}
			enterprise := profile == config.ProfileEnterprise
			cases := []struct {
				name    string
				user    store.User
				send    func() (int, map[string]any)
				refused bool
				msg     string
				reason  string
			}{
				{"a person under claude-code", kim, func() (int, map[string]any) {
					return postJSON(t, base+"/v1/checkin", body(kimToken, "claude-code"))
				}, enterprise, wantDoorMsg, doorReason},
				{"a person under console", kim, func() (int, map[string]any) {
					return postJSON(t, base+"/v1/checkin", body(kimToken, "console"))
				}, false, "", ""},
				{"an agent without a proof", bot, func() (int, map[string]any) {
					return postJSON(t, base+"/v1/checkin", body(botToken, "claude-code"))
				}, enterprise, wantAgentProofMsg("bot", "an agent"), proofReason},
				{"a service account without a proof", svc, func() (int, map[string]any) {
					return postJSON(t, base+"/v1/checkin", body(svcToken, "exec-wrapper"))
				}, enterprise, wantAgentProofMsg("svc", "a service account"), proofReason},
				{"an agent without a proof under console", bot, func() (int, map[string]any) {
					return postJSON(t, base+"/v1/checkin", body(botToken, "console"))
				}, true, "the user bot is an agent, and only a person can open a console session. " +
					"Sign in as yourself in your own browser", "only a person can open a human client session"},
				{"an agent with a proof", bot, func() (int, map[string]any) {
					return newKeyHolder(t, base).call("/v1/checkin", "", body(botToken, "claude-code"))
				}, false, "", ""},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					before := len(signInFailures(t, app, tc.user.ID, "id-token"))
					status, out := tc.send()
					fails := signInFailures(t, app, tc.user.ID, "id-token")
					if !tc.refused {
						if status != http.StatusOK || len(fails) != before {
							t.Fatalf("check-in = %d %v with %d new failures, want 200 and none", status, out, len(fails)-before)
						}
						return
					}
					if status != http.StatusForbidden || out["error"] != tc.msg {
						t.Fatalf("check-in = %d %v, want 403 %q", status, out, tc.msg)
					}
					if len(fails) != before+1 {
						t.Fatalf("login failures = %d, want exactly one more than %d", len(fails), before)
					}
					if f := fails[len(fails)-1]; f["reason"] != tc.reason || f["user"] != tc.user.Username {
						t.Errorf("login failure = %v, want reason %q naming %s", f, tc.reason, tc.user.Username)
					}
				})
			}
		})
	}
}

// TestIdPDiscoverySignInFields pins the two sign-in fields of idp.json on
// both profiles: min_sign_in always, and loopback_port as a number only when
// oidc.loopbackPort is set.
func TestIdPDiscoverySignInFields(t *testing.T) {
	t.Parallel()
	_, idpBase := testApp(t)
	cases := []struct {
		name    string
		profile string
		minimum string
		port    int
	}{
		{"standalone with any port", config.ProfileStandalone, config.SignInCode, 0},
		{"standalone with a fixed port", config.ProfileStandalone, config.SignInBrowser, 8400},
		{"enterprise with any port", config.ProfileEnterprise, config.SignInBrowser, 0},
		{"enterprise with a fixed port", config.ProfileEnterprise, config.SignInCode, 49152},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, base := testApp(t, func(c *config.Config) {
				c.Profile, c.Governance.MinSignIn, c.OIDC.LoopbackPort = tc.profile, tc.minimum, tc.port
				if tc.profile == config.ProfileEnterprise {
					c.OIDC.Issuer, c.OIDC.ClientID = idpBase, "straza"
				}
			})
			resp, err := http.Get(base + "/.well-known/straza/idp.json")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			var doc map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("idp.json = %d %v (%v)", resp.StatusCode, doc, err)
			}
			if doc["min_sign_in"] != tc.minimum || doc["nhi_issuer"] != base {
				t.Errorf("idp.json = %v, want min_sign_in %s and nhi_issuer %s", doc, tc.minimum, base)
			}
			port, has := doc["loopback_port"]
			switch {
			case tc.port == 0 && has:
				t.Errorf("idp.json = %v, want no loopback_port for any port", doc)
			case tc.port != 0 && port != float64(tc.port):
				t.Errorf("loopback_port = %v, want the number %d", port, tc.port)
			}
		})
	}
}
