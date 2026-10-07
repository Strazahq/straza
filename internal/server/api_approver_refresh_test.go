package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/strazahq/straza/internal/approval"
	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

// signApproverRefresh signs the refresh-scoped canonical message with a device
// key, a distinct domain from the decide message (signApprover).
func signApproverRefresh(t *testing.T, priv *ecdsa.PrivateKey, deviceID, challenge string) string {
	t.Helper()
	msg := "refresh\n" + deviceID + "\n" + challenge
	d := sha256.Sum256([]byte(msg))
	sig, err := ecdsa.SignASN1(rand.Reader, priv, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// enrollApproverDevice drives the HTTP enroll flow and returns the device id,
// its freshly minted token, and the private key that signs for it.
func enrollApproverDevice(t *testing.T, base, adminTok string) (deviceID, deviceToken string, priv *ecdsa.PrivateKey) {
	t.Helper()
	var mint struct {
		EnrollToken string `json:"enroll_token"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/approvers/enroll-token", adminTok,
		map[string]any{"username": "kim"}, &mint); code != http.StatusOK {
		t.Fatalf("mint enroll-token = %d", code)
	}
	priv, spki := genApproverKey(t)
	var enr struct {
		DeviceID    string `json:"approver_device_id"`
		DeviceToken string `json:"device_token"`
	}
	if code := adminReq(t, "POST", base+"/v1/approver/enroll", "", map[string]any{
		"enroll_token": mint.EnrollToken,
		"device": map[string]any{
			"name": "kim-pixel", "platform": "android", "key_alg": "ecdsa-p256",
			"public_key": spki, "key_security_level": "strongbox",
			"attestation": map[string]any{"kind": "none"},
		},
	}, &enr); code != http.StatusCreated {
		t.Fatalf("enroll = %d", code)
	}
	if enr.DeviceID == "" || enr.DeviceToken == "" {
		t.Fatalf("enroll response = %+v", enr)
	}
	return enr.DeviceID, enr.DeviceToken, priv
}

// TestApproverTokenExpiredSentinelProbe is the empirical check for the
// expiry-detection path: mint an already-expired approver token,
// verify it, and assert whether the jwx expiry sentinel propagates through
// VerifyApproverToken's %w wrap. It DOES (this passes), so requireApprover
// classifies expiry with a plain errors.Is, no unverified re-parse. This test
// pins that branch so a jwx upgrade that changes the behavior fails loudly here.
func TestApproverTokenExpiredSentinelProbe(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	kim := seedIdentity(t, app)

	raw, err := app.tokens.MintApproverToken(kim.ID, "apd_probe", -time.Minute)
	if err != nil {
		t.Fatalf("MintApproverToken: %v", err)
	}
	_, verr := app.tokens.VerifyApproverToken(raw)
	if verr == nil {
		t.Fatal("expected VerifyApproverToken to reject an already-expired token")
	}
	if !errors.Is(verr, jwt.TokenExpiredError()) {
		t.Fatalf("errors.Is(err, jwt.TokenExpiredError()) = false (err=%v): "+
			"the sentinel does NOT propagate; requireApprover must use the "+
			"unverified-reparse fallback", verr)
	}
}

// TestApproverRefreshOverHTTP drives the whole refresh loop end to end: enroll a
// device, fetch a refresh challenge, sign it with the hardware key, exchange it
// for a fresh token, and prove the new token verifies with the same user/device,
// carries the ~30-day expiry, and opens the surface. Then the consumed challenge
// is refused (token_invalid) and an unknown device on the challenge route is 404.
func TestApproverRefreshOverHTTP(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)

	deviceID, _, priv := enrollApproverDevice(t, base, adminTok)

	// 1. Challenge (no bearer); expires_in is the 5-minute challenge window.
	var chResp struct {
		Challenge string `json:"challenge"`
		ExpiresIn int    `json:"expires_in"`
	}
	if code := adminReq(t, "POST", base+"/v1/approver/refresh/challenge", "",
		map[string]any{"approver_device_id": deviceID}, &chResp); code != http.StatusOK {
		t.Fatalf("refresh/challenge = %d", code)
	}
	if wantCh := int(approval.ChallengeTTL.Seconds()); chResp.Challenge == "" || chResp.ExpiresIn != wantCh {
		t.Fatalf("refresh/challenge = %+v, want challenge + expires_in %d", chResp, wantCh)
	}

	// 2. Sign and exchange (no bearer) → a fresh 30-day token.
	sig := signApproverRefresh(t, priv, deviceID, chResp.Challenge)
	var rf struct {
		DeviceToken string `json:"device_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if code := adminReq(t, "POST", base+"/v1/approver/refresh", "", map[string]any{
		"approver_device_id": deviceID, "challenge": chResp.Challenge, "signature": sig,
	}, &rf); code != http.StatusOK {
		t.Fatalf("refresh = %d", code)
	}
	if rf.DeviceToken == "" {
		t.Fatal("refresh returned no device_token")
	}
	if wantTTL := int(authn.DefaultApproverTokenTTL.Seconds()); rf.ExpiresIn != wantTTL {
		t.Errorf("refresh expires_in = %d, want %d (~30d)", rf.ExpiresIn, wantTTL)
	}

	// 3. The fresh token verifies and binds the SAME user/device.
	claims, err := app.tokens.VerifyApproverToken(rf.DeviceToken)
	if err != nil {
		t.Fatalf("VerifyApproverToken(refreshed): %v", err)
	}
	if claims.Subject != kim.ID || claims.Device != deviceID {
		t.Errorf("refreshed token binds (%s,%s), want (%s,%s)", claims.Subject, claims.Device, kim.ID, deviceID)
	}

	// 4. And it opens the approver surface.
	if code := adminReq(t, "GET", base+"/v1/approver/pending", rf.DeviceToken, nil, nil); code != http.StatusOK {
		t.Errorf("pending with refreshed token = %d, want 200", code)
	}

	// 5. Replay of the consumed challenge is refused (single-use) → token_invalid.
	var replay struct {
		Code string `json:"code"`
	}
	if code := adminReq(t, "POST", base+"/v1/approver/refresh", "", map[string]any{
		"approver_device_id": deviceID, "challenge": chResp.Challenge, "signature": sig,
	}, &replay); code != http.StatusUnauthorized || replay.Code != codeTokenInvalid {
		t.Errorf("challenge replay = %d/%q, want 401/%s", code, replay.Code, codeTokenInvalid)
	}

	// 6. Unknown device on the challenge route is 404 (nothing to retire).
	if code := adminReq(t, "POST", base+"/v1/approver/refresh/challenge", "",
		map[string]any{"approver_device_id": "apd_ghost"}, nil); code != http.StatusNotFound {
		t.Errorf("refresh/challenge unknown device = %d, want 404", code)
	}
}

// TestApproverRequire401Codes pins the machine-readable 401 codes on a
// requireApprover-guarded route: missing bearer ⇒ missing_token; garbage ⇒
// token_invalid; expired ⇒ token_expired; absent device row ⇒ device_revoked;
// disabled user ⇒ user_inactive.
func TestApproverRequire401Codes(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)

	deviceID, deviceTok, _ := enrollApproverDevice(t, base, adminTok)

	expiredTok, err := app.tokens.MintApproverToken(kim.ID, deviceID, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// A well-formed use=approver token for a device row that does not exist:
	// the "device row gone" case (row-backed revocation).
	ghostTok, err := app.tokens.MintApproverToken(kim.ID, "apd_ghost", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range []struct {
		name, token, wantCode string
	}{
		{"missing token", "", codeMissingToken},
		{"garbage token", "not.a.jwt", codeTokenInvalid},
		{"expired token", expiredTok, codeTokenExpired},
		{"revoked device", ghostTok, codeDeviceRevoked},
	} {
		var body struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		code := adminReq(t, "GET", base+"/v1/approver/pending", p.token, nil, &body)
		if code != http.StatusUnauthorized || body.Code != p.wantCode {
			t.Errorf("%s: got %d/%q, want 401/%s", p.name, code, body.Code, p.wantCode)
		}
		if body.Error == "" {
			t.Errorf("%s: error message must stay populated alongside the code", p.name)
		}
	}

	// user_inactive (mutates kim, run last): the device row is present but the
	// bound user is disabled, so the check falls through to user_inactive.
	u, err := app.store.Users().GetByID(context.Background(), kim.ID)
	if err != nil {
		t.Fatal(err)
	}
	u.Status = store.UserDisabled
	if _, err := app.store.Users().Update(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Code string `json:"code"`
	}
	if code := adminReq(t, "GET", base+"/v1/approver/pending", deviceTok, nil, &body); code != http.StatusUnauthorized || body.Code != codeUserInactive {
		t.Errorf("user_inactive: got %d/%q, want 401/%s", code, body.Code, codeUserInactive)
	}
}
