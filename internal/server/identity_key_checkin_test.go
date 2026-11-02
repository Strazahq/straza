package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/store"
)

// deviceKeyFailures returns the login failures of the key lane.
func deviceKeyFailures(t *testing.T, app *App) []map[string]any {
	t.Helper()
	return matching(t, app, "straza.audit.authn", func(d map[string]any) bool {
		return d["action"] == "login" && d["outcome"] == "failure" && d["via"] == "device-key"
	})
}

// reuseRecords returns the device.key.reused records of device.
func reuseRecords(t *testing.T, app *App, device string) []map[string]any {
	t.Helper()
	return matching(t, app, "straza.audit.identity", func(d map[string]any) bool {
		return d["action"] == "device.key.reused" && d["device"] == device
	})
}

// TestKeyCheckinBindsTokenToKey pins the key lane of check-in: it starts a
// session whose token carries cnf equal to the key thumbprint and the
// enrolled device, raises the stored counter, and answers no device token.
func TestKeyCheckinBindsTokenToKey(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	h := newKeyHolder(t, base)
	h.enroll(loginDeviceFlow(t, base, "kim", "hunter2!"), "sha256:k1")

	code, out := h.checkin()
	if code != http.StatusOK {
		t.Fatalf("key checkin = %d %v", code, out)
	}
	claims, err := app.tokens.Verify(out["session_token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if claims.CNF != h.thumbprint() || claims.Device != h.device || claims.Subject != kim.ID || claims.Session != out["session_id"] {
		t.Errorf("claims = %+v, want cnf %s on device %s", claims, h.thumbprint(), h.device)
	}
	if _, ok := out["device_token"]; ok {
		t.Errorf("key checkin answered a device token: %v", out)
	}
	if d := deviceRow(t, app, h.device); d.KeyCounter != 1 {
		t.Errorf("stored counter = %d, want 1", d.KeyCounter)
	}
}

// TestOnlyVerifiedProofMintsCNF pins that only a verified DPoP proof mints a
// key-bound token: a person's id-token check-in, an agent's id-token
// check-in without a proof and the refresh of a bearer session carry no cnf, while the key check-in and an agent's
// id-token check-in with a proof carry the proof key's thumbprint.
func TestOnlyVerifiedProofMintsCNF(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	_, botToken := proofAgent(t, app, "bot")
	claude := map[string]string{"name": "claude-code", "version": "2.1.0"}
	checkin := func(body map[string]any, harness map[string]string) string {
		body["harness"] = harness
		code, out := postJSON(t, base+"/v1/checkin", body)
		if code != http.StatusOK {
			t.Fatalf("checkin %v = %d %v", body, code, out)
		}
		return out["session_token"].(string)
	}
	h := newKeyHolder(t, base)
	h.enroll(idToken, "sha256:key")
	agentKey := newKeyHolder(t, base)

	cases := []struct {
		name    string
		token   func() string
		wantCNF string
	}{
		{"a person's id-token check-in", func() string { return checkin(map[string]any{"id_token": idToken}, claude) }, ""},
		{"an agent's id-token check-in without a proof", func() string { return checkin(map[string]any{"id_token": botToken}, claude) }, ""},
		{"refresh of a bearer session", func() string {
			return checkin(map[string]any{"session_token": checkin(map[string]any{"id_token": idToken}, claude)}, claude)
		}, ""},
		{"key check-in", func() string { tok, _ := h.mustCheckin(); return tok }, h.thumbprint()},
		{"an agent's id-token check-in with a proof", func() string {
			code, out := agentKey.call("/v1/checkin", "", nhiCheckinBody(botToken))
			if code != http.StatusOK {
				t.Fatalf("agent check-in with a proof = %d %v", code, out)
			}
			return out["session_token"].(string)
		}, agentKey.thumbprint()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := app.tokens.Verify(tc.token())
			if err != nil {
				t.Fatal(err)
			}
			if claims.CNF != tc.wantCNF {
				t.Errorf("cnf = %q, want %q", claims.CNF, tc.wantCNF)
			}
		})
	}
}

// TestKeyCheckinRefusals pins each refusal of the key lane before the
// counter: the answer, exactly one login failure with via device-key and
// its reason, the user only once the proof key matched the device, and a
// counter that does not move. The disabled user runs last because it bars
// every later check-in of kim.
func TestKeyCheckinRefusals(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	ctx := context.Background()

	cases := []struct {
		name       string
		arm        func(h *keyHolder) *keyHolder // answers the holder that checks in
		wantCode   int
		wantError  string
		wantReason string
		wantUser   bool
	}{
		{"a key the device did not enroll", func(h *keyHolder) *keyHolder {
			other := newKeyHolder(t, base)
			other.device, other.counter = h.device, h.counter
			return other
		}, http.StatusForbidden, keyNotEnrolledMsg, keyNotEnrolledMsg, false},
		{"a device an admin revoked", func(h *keyHolder) *keyHolder {
			if err := app.revokeDevice(httptest.NewRequest(http.MethodDelete, "/", nil), h.device, "device revoked by admin"); err != nil {
				t.Fatal(err)
			}
			return h
		}, http.StatusForbidden, keyNotEnrolledMsg, keyNotEnrolledMsg, false},
		{"a device on the denylist", func(h *keyHolder) *keyHolder {
			app.denylist.RevokeDevice(h.device)
			return h
		}, http.StatusForbidden, revokedIdentityMsg, "device or user revoked", true},
		{"a passed sign-in deadline", func(h *keyHolder) *keyHolder {
			signInAgo(t, app, h.device, 7*24*time.Hour+time.Minute)
			return h
		}, http.StatusForbidden,
			"Your last sign-in on this machine was more than 7 days ago, and your Straza admin requires a new one. " +
				"Run straza enroll and sign in with your work account.",
			signInPassedReason, true},
		{"a disabled user", func(h *keyHolder) *keyHolder {
			u := kim
			u.Status = store.UserDisabled
			if _, err := app.store.Users().Update(ctx, u); err != nil {
				t.Fatal(err)
			}
			return h
		}, http.StatusForbidden, "user is disabled. Contact your administrator", "user is disabled", true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newKeyHolder(t, base)
			h.enroll(idToken, "sha256:refusal-"+string(rune('a'+i)))
			h.mustCheckin()
			before := len(deviceKeyFailures(t, app))
			code, out := tc.arm(h).checkin()
			if code != tc.wantCode || out["error"] != tc.wantError {
				t.Fatalf("checkin = %d %v, want %d %q", code, out, tc.wantCode, tc.wantError)
			}
			failures := deviceKeyFailures(t, app)
			if len(failures) != before+1 {
				t.Fatalf("device-key login failures = %d, want exactly one more than %d", len(failures), before)
			}
			got := failures[len(failures)-1]
			if got["reason"] != tc.wantReason || got["harness"] != "claude-code/2.1.0" {
				t.Errorf("login failure = %v, want reason %q", got, tc.wantReason)
			}
			if _, hasUser := got["userId"]; hasUser != tc.wantUser || (tc.wantUser && got["userId"] != kim.ID) {
				t.Errorf("login failure userId = %v, want present %v", got["userId"], tc.wantUser)
			}
			if d, err := app.store.Devices().GetByID(ctx, h.device); err == nil && d.KeyCounter != 1 {
				t.Errorf("stored counter = %d, want 1", d.KeyCounter)
			}
		})
	}
}

// TestKeyReuseRevokesDevice pins the key-reuse path: a counter at or below
// the stored one under a valid proof revokes the device the way an admin
// revoke does and writes exactly one device.key.reused record and no login
// record.
func TestKeyReuseRevokesDevice(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	ctx := context.Background()

	for _, sent := range []int64{3, 1} {
		name := map[int64]string{3: "the same counter", 1: "a lower counter"}[sent]
		t.Run(name, func(t *testing.T) {
			h := newKeyHolder(t, base)
			h.enroll(idToken, "sha256:"+strings.ReplaceAll(name, " ", "-"))
			h.counter = 2
			h.mustCheckin()
			authnBefore := len(outboxDataFor(t, app, "straza.audit.authn"))

			code, out := h.call("/v1/checkin", "", h.checkinBody(sent))
			if code != http.StatusForbidden || out["error"] != keyReusedMsg {
				t.Fatalf("reused counter = %d %v, want 403 %q", code, out, keyReusedMsg)
			}
			if _, err := app.store.Devices().GetByID(ctx, h.device); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("device row after reuse: %v, want it gone", err)
			}
			revs, err := app.store.Revocations().ListByTarget(ctx, store.RevokeDevice, h.device)
			if err != nil || len(revs) != 1 || revs[0].Reason != "device key reused" {
				t.Errorf("revocations = %+v %v, want one with reason device key reused", revs, err)
			}
			if !app.denylist.deviceBlocked(h.device) {
				t.Error("device is not on the denylist")
			}
			pushed := matching(t, app, "straza.revocation.device", func(d map[string]any) bool { return d["device"] == h.device })
			if len(pushed) != 1 || len(pushed[0]) != 1 {
				t.Errorf("revocation events = %v, want one carrying the device only", pushed)
			}
			recs := reuseRecords(t, app, h.device)
			if len(recs) != 1 {
				t.Fatalf("device.key.reused records = %d, want exactly 1", len(recs))
			}
			want := map[string]any{"action": "device.key.reused", "user": kim.ID, "device": h.device,
				"counter": float64(sent), "storedCounter": float64(3),
				"reason": "check-in counter at or below the last one accepted"}
			for k, v := range want {
				if recs[0][k] != v {
					t.Errorf("record %s = %v, want %v", k, recs[0][k], v)
				}
			}
			if n := len(outboxDataFor(t, app, "straza.audit.authn")); n != authnBefore {
				t.Errorf("authn records = %d, want %d: the reuse path writes no login record", n, authnBefore)
			}
		})
	}
}

// TestKeyCheckinWithoutTheKeyRevokesNothing pins that only the device's key
// reaches the reuse path: a low counter with a garbage proof, another key's
// proof or a proof for another path leaves the device, its counter and the
// audit chain's reuse records alone.
func TestKeyCheckinWithoutTheKeyRevokesNothing(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app)
	h := newKeyHolder(t, base)
	h.enroll(loginDeviceFlow(t, base, "kim", "hunter2!"), "sha256:k1")
	h.counter = 2
	h.mustCheckin()

	other := newKeyHolder(t, base)
	other.nonce = h.nonce
	cases := []struct {
		name  string
		proof func() string
	}{
		{"a garbage proof", func() string { return "not.a.proof" }},
		{"another key's proof", func() string { return other.proof("/v1/checkin", "", 1) }},
		{"a proof for another path", func() string { return h.proof("/v1/enroll", "", 1) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, out := h.post("/v1/checkin", tc.proof(), h.checkinBody(1))
			if code != http.StatusUnauthorized && code != http.StatusForbidden {
				t.Fatalf("checkin = %d %v, want a refusal", code, out)
			}
			if out["error"] == keyReusedMsg {
				t.Fatalf("checkin took the reuse path: %v", out)
			}
			if d := deviceRow(t, app, h.device); d.KeyCounter != 3 {
				t.Errorf("stored counter = %d, want 3", d.KeyCounter)
			}
			if n := len(reuseRecords(t, app, h.device)); n != 0 {
				t.Errorf("reuse records = %d, want none", n)
			}
		})
	}
	if code, out := h.checkin(); code != http.StatusOK {
		t.Errorf("the device's own key afterwards = %d %v", code, out)
	}
}

// TestKeyNonceChallengeMovesNothing pins that a check-in with a missing or
// forged nonce is answered with the nonce challenge before any other check,
// moves no counter and writes no record, and that the retry with the
// answered nonce goes through.
func TestKeyNonceChallengeMovesNothing(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app)
	h := newKeyHolder(t, base)
	h.enroll(loginDeviceFlow(t, base, "kim", "hunter2!"), "sha256:k1")
	authnBefore := len(outboxDataFor(t, app, "straza.audit.authn"))
	identityBefore := len(outboxDataFor(t, app, "straza.audit.identity"))

	for _, nonce := range []string{"", "forged-nonce"} {
		h.nonce = nonce
		code, hdr, out := h.send("/v1/checkin", "", h.checkinBody(5))
		if code != http.StatusUnauthorized || out["error"] != nonceMsg || !strings.Contains(hdr.Get("WWW-Authenticate"), "use_dpop_nonce") {
			t.Fatalf("nonce %q: checkin = %d %v, want the nonce challenge", nonce, code, out)
		}
		if d := deviceRow(t, app, h.device); d.KeyCounter != 0 {
			t.Errorf("nonce %q: stored counter = %d, want 0", nonce, d.KeyCounter)
		}
	}
	if n := len(outboxDataFor(t, app, "straza.audit.authn")); n != authnBefore {
		t.Errorf("authn records = %d, want %d", n, authnBefore)
	}
	if n := len(outboxDataFor(t, app, "straza.audit.identity")); n != identityBefore {
		t.Errorf("identity records = %d, want %d", n, identityBefore)
	}
	if code, _, out := h.send("/v1/checkin", "", h.checkinBody(5)); code != http.StatusOK {
		t.Errorf("retry with the answered nonce = %d %v", code, out)
	}
}

// TestConcurrentKeyCheckins races check-ins with distinct increasing
// counters on one device. No accepted counter is lost: a device that
// survives stores the highest counter accepted, and a check-in that lost the
// race to a higher counter takes the reuse path with an honest record.
func TestConcurrentKeyCheckins(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app)
	h := newKeyHolder(t, base)
	h.enroll(loginDeviceFlow(t, base, "kim", "hunter2!"), "sha256:k1")
	h.mustCheckin()

	const n = 8
	proofs := make([]string, n)
	for i := range proofs {
		proofs[i] = h.proof("/v1/checkin", "", int64(i+2))
	}
	bodies := make([][]byte, n)
	for i := range bodies {
		bodies[i] = mustJSON(h.checkinBody(int64(i + 2)))
	}
	codes := make([]int, n)
	errs := make([]string, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			req, _ := http.NewRequest(http.MethodPost, base+"/v1/checkin", bytes.NewReader(bodies[i]))
			req.Header.Set(dpop.HeaderProof, proofs[i])
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs[i] = err.Error()
				return
			}
			defer func() { _ = resp.Body.Close() }()
			var out map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&out)
			codes[i] = resp.StatusCode
			errs[i], _ = out["error"].(string)
		}(i)
	}
	close(start)
	wg.Wait()

	var accepted int64
	for i, code := range codes {
		switch {
		case code == http.StatusOK:
			accepted = max(accepted, int64(i+2))
		case code != http.StatusForbidden:
			t.Errorf("counter %d: %d %q, want 200 or 403", i+2, code, errs[i])
		}
	}
	recs := reuseRecords(t, app, h.device)
	t.Logf("counters 2..%d answered %v, highest accepted %d, reuse records %d", n+1, codes, accepted, len(recs))
	for _, r := range recs {
		if r["counter"].(float64) > r["storedCounter"].(float64) {
			t.Errorf("reuse record %v says the counter beat the stored one", r)
		}
	}
	d, err := app.store.Devices().GetByID(context.Background(), h.device)
	switch {
	case err == nil && d.KeyCounter != accepted:
		t.Errorf("stored counter = %d, want the highest accepted %d", d.KeyCounter, accepted)
	case err == nil && len(recs) != 0:
		t.Errorf("the device survived %d reuse records", len(recs))
	case errors.Is(err, store.ErrNotFound) && len(recs) == 0:
		t.Error("the device is gone with no reuse record")
	case err != nil && !errors.Is(err, store.ErrNotFound):
		t.Fatal(err)
	}
}

// TestKeyCheckinBodyShapes pins how a check-in body picks its lane: a counter
// beside a body token is malformed, and a key body without a DPoP header is
// the key lane missing its proof, refused with one login failure.
func TestKeyCheckinBodyShapes(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	_ = seedIdentity(t, app)
	idToken := loginDeviceFlow(t, base, "kim", "hunter2!")
	h := newKeyHolder(t, base)
	h.enroll(idToken, "sha256:shapes")
	token, _ := h.mustCheckin()

	cases := []struct {
		name      string
		body      map[string]any
		wantCode  int
		wantError string
	}{
		{"an id token and a counter", map[string]any{"id_token": idToken, "device_id": h.device, "counter": 5},
			http.StatusBadRequest, counterWithTokenMsg},
		{"a session token and a counter", map[string]any{"session_token": token, "counter": 5},
			http.StatusBadRequest, counterWithTokenMsg},
		{"a key body without a proof", h.checkinBody(5), http.StatusUnauthorized,
			"the DPoP proof was not accepted: the request carries no DPoP proof. Sign a fresh proof with the key this machine enrolled."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(deviceKeyFailures(t, app))
			code, _, out := h.post("/v1/checkin", "", tc.body)
			if code != tc.wantCode || out["error"] != tc.wantError {
				t.Fatalf("checkin = %d %v, want %d %q", code, out, tc.wantCode, tc.wantError)
			}
			want := before
			if code == http.StatusUnauthorized {
				want++
			}
			if n := len(deviceKeyFailures(t, app)); n != want {
				t.Errorf("device-key login failures = %d, want %d", n, want)
			}
		})
	}
	if d := deviceRow(t, app, h.device); d.KeyCounter != 1 {
		t.Errorf("stored counter = %d, want 1", d.KeyCounter)
	}
}
