package server

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

// TestKeyEnrollRefusesNonPerson pins that only a person enrolls a machine
// key: an agent with a valid ID token is refused with 403 and exactly one
// login failure, and no device is registered for it.
func TestKeyEnrollRefusesNonPerson(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()
	bot, err := app.store.Users().Create(ctx, store.User{Username: "bot", UserType: store.UserTypeAgent})
	if err != nil {
		t.Fatal(err)
	}
	idt, err := app.tokens.MintIDToken(bot.ID, "straza", time.Minute, "bot", "")
	if err != nil {
		t.Fatal(err)
	}
	h := newKeyHolder(t, base)
	code, out := h.call("/v1/enroll", "", keyEnrollBody(idt, "sha256:bot"))
	want := "the user bot is an agent, and only a person can enroll a machine key. " +
		"An AI agent or a service account checks in with its own client credentials instead."
	if code != http.StatusForbidden || out["error"] != want {
		t.Fatalf("agent key enroll = %d %v, want 403 %q", code, out, want)
	}
	failures := deviceKeyFailures(t, app)
	if len(failures) != 1 {
		t.Fatalf("device-key login failures = %v, want exactly one", failures)
	}
	if f := failures[0]; f["user"] != "bot" || f["userId"] != bot.ID || f["reason"] != "only a person can enroll a machine key" {
		t.Errorf("login failure = %v", f)
	}
	if devs, _ := app.store.Devices().ListByUser(ctx, bot.ID); len(devs) != 0 {
		t.Errorf("agent enrolled devices %+v", devs)
	}
}

// TestKeyCheckinProofReplay pins the shared spent-proof record on the key
// check-in lane. A captured proof passes every check a replica that never
// saw it makes, so only the record stops its replay: the same body is
// refused as replayed, and a changed body counter is refused because the
// proof signs the counter. No replay moves the counter, revokes the device
// or writes a reuse record, and each writes exactly one login failure.
func TestKeyCheckinProofReplay(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app)
	ctx := context.Background()
	h := newKeyHolder(t, base)
	h.enroll(loginDeviceFlow(t, base, "kim", "hunter2!"), "sha256:replay")
	h.mustCheckin()

	captured := h.proof("/v1/checkin", "", 2)
	if code, _, out := h.post("/v1/checkin", captured, h.checkinBody(2)); code != http.StatusOK {
		t.Fatalf("the captured check-in = %d %v", code, out)
	}
	// Positive control: a replica that never saw the proof accepts it, so the
	// refusals below come from the shared record or the signed counter.
	key, err := authn.LoadOrCreateNonceKey(ctx, app.store.Settings())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authn.NewProofVerifier(key).Verify(captured, authn.ProofCheck{
		Method: http.MethodPost, Path: "/v1/checkin", Origins: []string{base}, Counter: 2, CallerSpends: true,
	}, time.Now()); err != nil {
		t.Fatalf("a fresh replica refused the captured proof: %v", err)
	}

	cases := []struct {
		name       string
		counter    int64
		wantError  string
		wantReason string
	}{
		{"the same body", 2,
			"the DPoP proof was not accepted: jti was already used. Sign a fresh proof with the key this machine enrolled.",
			"the DPoP proof was replayed"},
		{"the counter changed to 1", 1,
			"the DPoP proof was not accepted: ctr does not match the check-in counter. Sign a fresh proof with the key this machine enrolled.",
			"the DPoP proof did not verify"},
		{"the counter changed to 2^62", 1 << 62,
			"the DPoP proof was not accepted: ctr does not match the check-in counter. Sign a fresh proof with the key this machine enrolled.",
			"the DPoP proof did not verify"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(deviceKeyFailures(t, app))
			code, hdr, out := h.post("/v1/checkin", captured, h.checkinBody(tc.counter))
			if code != http.StatusUnauthorized || out["error"] != tc.wantError ||
				!strings.Contains(hdr.Get("WWW-Authenticate"), `error="invalid_dpop_proof"`) {
				t.Fatalf("replay = %d %v %v, want 401 %q", code, hdr, out, tc.wantError)
			}
			failures := deviceKeyFailures(t, app)
			if len(failures) != before+1 || failures[len(failures)-1]["reason"] != tc.wantReason {
				t.Errorf("device-key login failures = %v, want one more with reason %q", failures, tc.wantReason)
			}
			if d := deviceRow(t, app, h.device); d.KeyCounter != 2 {
				t.Errorf("stored counter = %d, want 2", d.KeyCounter)
			}
			if n := len(reuseRecords(t, app, h.device)); n != 0 {
				t.Errorf("reuse records = %d, want none", n)
			}
			if revs, err := app.store.Revocations().ListByTarget(ctx, store.RevokeDevice, h.device); err != nil || len(revs) != 0 {
				t.Errorf("revocations = %+v %v, want none", revs, err)
			}
		})
	}
}

// TestKeyEnrollProofReplay pins that an enroll proof works once. Replayed
// with another person's ID token after the enrollment it answered, it is
// refused with the enroll proof 401 and no audit record, and the key stays
// enrolled for its owner only.
func TestKeyEnrollProofReplay(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app)
	ctx := context.Background()
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	h := newKeyHolder(t, base)
	body := keyEnrollBody(idToken, "sha256:enroll-replay")
	h.send("/v1/enroll", "", body) // the nonce challenge
	captured := h.proof("/v1/enroll", "", 0)
	code, _, out := h.post("/v1/enroll", captured, body)
	if code != http.StatusOK {
		t.Fatalf("the captured enroll = %d %v", code, out)
	}
	h.device = out["device_id"].(string)
	h.mustCheckin()
	lee, err := app.store.Users().Create(ctx, store.User{Username: "lee"})
	if err != nil {
		t.Fatal(err)
	}
	leeToken, err := app.tokens.MintIDToken(lee.ID, "straza", time.Minute, "lee", "")
	if err != nil {
		t.Fatal(err)
	}
	authnBefore := len(outboxDataFor(t, app, "straza.audit.authn"))

	code, hdr, out := h.post("/v1/enroll", captured, keyEnrollBody(leeToken, "sha256:enroll-replay"))
	want := "the DPoP proof was not accepted: jti was already used. Sign a fresh proof with the key you are enrolling."
	if code != http.StatusUnauthorized || out["error"] != want ||
		!strings.Contains(hdr.Get("WWW-Authenticate"), `error="invalid_dpop_proof"`) {
		t.Fatalf("replayed enroll = %d %v %v, want 401 %q", code, hdr, out, want)
	}
	if n := len(outboxDataFor(t, app, "straza.audit.authn")); n != authnBefore {
		t.Errorf("authn records = %d, want %d", n, authnBefore)
	}
	if devs, _ := app.store.Devices().ListByUser(ctx, lee.ID); len(devs) != 0 {
		t.Errorf("the replay enrolled devices for lee: %+v", devs)
	}
	if n := len(matching(t, app, "straza.identity.updated", func(e map[string]any) bool { return e["action"] == "enroll" })); n != 1 {
		t.Errorf("enroll events = %d, want 1", n)
	}
	if d := deviceRow(t, app, h.device); d.KeyCounter != 1 {
		t.Errorf("stored counter = %d, want 1", d.KeyCounter)
	}
}

// TestKeyLanesRefuseNULProofID pins the refusal of a proof whose jti holds
// a NUL character on the three lanes that spend the jti in the store: key
// enrollment, key check-in and an AI agent's id-token check-in. Each answers
// 401 with invalid_dpop_proof and its lane's proof sentence, and writes the
// record its other proof failures write: none on enrollment, one login
// failure on the check-ins. No device, counter or session changes, and a
// proof from the same signer with a plain jti passes on each lane.
func TestKeyLanesRefuseNULProofID(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	ctx := context.Background()
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	bot, botToken := proofAgent(t, app, "bot")
	keyed := newKeyHolder(t, base)
	keyed.enroll(idToken, "sha256:nul-jti")
	const refused = "the DPoP proof was not accepted: jti holds a NUL character (a zero byte), which strazad does not accept in a proof id it stores. "

	lanes := []struct {
		name      string
		h         *keyHolder
		path      string
		body      func() map[string]any
		ctr       func() int64
		wantError string
		via       string // the via of the login failure the refusal writes, "" for no record
		userID    string // the user the login failure names, "" for none
	}{
		{"key enrollment", newKeyHolder(t, base), "/v1/enroll",
			func() map[string]any { return keyEnrollBody(idToken, "sha256:nul-jti-enroll") },
			func() int64 { return 0 }, refused + "Sign a fresh proof with the key you are enrolling.", "", ""},
		{"key check-in", keyed, "/v1/checkin",
			func() map[string]any { return keyed.checkinBody(keyed.counter + 1) },
			func() int64 { return keyed.counter + 1 }, refused + "Sign a fresh proof with the key this machine enrolled.", "device-key", ""},
		{"agent check-in", newKeyHolder(t, base), "/v1/checkin",
			func() map[string]any { return nhiCheckinBody(botToken) },
			func() int64 { return 0 }, refused + "Sign a fresh proof with this agent's key.", "id-token", bot.ID},
	}
	for _, lane := range lanes {
		t.Run(lane.name, func(t *testing.T) {
			if lane.h.nonce == "" {
				lane.h.send(lane.path, "", lane.body()) // the nonce challenge
			}
			failure := func(d map[string]any) bool {
				return d["action"] == "login" && d["outcome"] == "failure" && d["via"] == lane.via &&
					d["reason"] == "the DPoP proof did not verify" && (lane.userID == "" || d["userId"] == lane.userID)
			}
			authnBefore := len(outboxDataFor(t, app, "straza.audit.authn"))
			failuresBefore := len(matching(t, app, "straza.audit.authn", failure))
			devices, _ := app.store.Devices().ListByUser(ctx, kim.ID)
			sessions, _ := app.store.Sessions().List(ctx, "")

			code, hdr, out := lane.h.post(lane.path, lane.h.proofJTI(lane.path, "nul\x00jti", lane.ctr()), lane.body())
			if code != http.StatusUnauthorized || out["error"] != lane.wantError ||
				!strings.Contains(hdr.Get("WWW-Authenticate"), `error="invalid_dpop_proof"`) {
				t.Fatalf("%s = %d %v %v, want 401 %q", lane.path, code, hdr, out, lane.wantError)
			}
			wantRecords := 0
			if lane.via != "" {
				wantRecords = 1
			}
			if n := len(outboxDataFor(t, app, "straza.audit.authn")); n != authnBefore+wantRecords {
				t.Errorf("authn records = %d, want %d", n, authnBefore+wantRecords)
			}
			if n := len(matching(t, app, "straza.audit.authn", failure)); lane.via != "" && n != failuresBefore+1 {
				t.Errorf("login failures via %s = %d, want %d", lane.via, n, failuresBefore+1)
			}
			if after, _ := app.store.Devices().ListByUser(ctx, kim.ID); !reflect.DeepEqual(after, devices) {
				t.Errorf("devices after the refusal = %+v, want %+v", after, devices)
			}
			if after, _ := app.store.Sessions().List(ctx, ""); len(after) != len(sessions) {
				t.Errorf("sessions = %d after the refusal, want %d", len(after), len(sessions))
			}

			ctr := lane.ctr()
			if code, _, out := lane.h.post(lane.path, lane.h.proofJTI(lane.path, "plain-jti", ctr), lane.body()); code != http.StatusOK {
				t.Fatalf("%s with a plain jti = %d %v, want 200", lane.path, code, out)
			}
			lane.h.counter = ctr
		})
	}
}
