package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// signRefresh signs the REFRESH-scoped canonical message (a distinct domain
// from the decide message; see approval.RefreshSigned).
func signRefresh(t *testing.T, priv *ecdsa.PrivateKey, deviceID, challenge string) string {
	t.Helper()
	d := sha256.Sum256([]byte("refresh\n" + deviceID + "\n" + challenge))
	sig, err := ecdsa.SignASN1(rand.Reader, priv, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// TestApproverLockedUserDenied pins the Straza-lane kill switch to the mobile
// approver surface. handleUserLock deliberately leaves users.status to the IdM
// and records the lock on the revocation lane (denylist), so the approver
// surface must consult the denylist too, otherwise a locked user's phone keeps
// deciding approvals, refreshing its token, and enrolling new devices while
// every other credential the user holds is dead. Unlock lifts the lane and the
// phone resumes with its existing key and token (the lock is reversible: no
// device rows are destroyed).
func TestApproverLockedUserDenied(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	ctx := context.Background()

	// kim is the admin driving lock/unlock; lee is the approver being locked.
	kim := seedIdentity(t, app)
	grantAdmin(t, app, kim.ID)
	adminTok, _ := checkinToken(t, app, base)

	hash, err := testPasswordHash("hunter2!")
	if err != nil {
		t.Fatal(err)
	}
	lee, err := app.store.Users().Create(ctx, store.User{Username: "lee", Email: "lee@x.io", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	sre, err := app.store.Roles().Create(ctx, store.Role{Name: "sre"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Roles().Assign(ctx, store.RoleAssignment{
		SubjectKind: store.SubjectUser, SubjectID: lee.ID, RoleID: sre.ID,
	}); err != nil {
		t.Fatal(err)
	}
	app.resolver.Bump()

	// Enroll lee's phone.
	var mint struct {
		EnrollToken string `json:"enroll_token"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/approvers/enroll-token", adminTok,
		map[string]any{"username": "lee"}, &mint); code != http.StatusOK {
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
			"name": "lee-pixel", "platform": "android", "key_alg": "ecdsa-p256",
			"public_key": spki, "key_security_level": "strongbox",
			"attestation": map[string]any{"kind": "none"},
		},
	}, &enr); code != http.StatusCreated {
		t.Fatalf("enroll = %d", code)
	}

	// Positive control: before the lock the phone works end-to-end (a decidable
	// row with a challenge proves role resolution AND the credential).
	rec := seedApproval(t, app, "u-other", []string{"sre"})
	var rows []approverRow
	if code := adminReq(t, "GET", base+"/v1/approver/pending?scope=decidable", enr.DeviceToken, nil, &rows); code != http.StatusOK {
		t.Fatalf("pending before lock = %d", code)
	}
	var challenge string
	for _, row := range rows {
		if row.ID == rec.ID {
			challenge = row.Challenge
		}
	}
	if challenge == "" {
		t.Fatalf("no decidable row with a challenge for %s: %+v", rec.ID, rows)
	}

	// Minted while lee is still active: a spare enroll token and a refresh
	// challenge; the lock must render both unusable.
	var mint2 struct {
		EnrollToken string `json:"enroll_token"`
	}
	if code := adminReq(t, "POST", base+"/v1/admin/approvers/enroll-token", adminTok,
		map[string]any{"username": "lee"}, &mint2); code != http.StatusOK {
		t.Fatalf("mint second enroll-token = %d", code)
	}
	var refCh struct {
		Challenge string `json:"challenge"`
	}
	if code := adminReq(t, "POST", base+"/v1/approver/refresh/challenge", "",
		map[string]any{"approver_device_id": enr.DeviceID}, &refCh); code != http.StatusOK {
		t.Fatalf("refresh challenge = %d", code)
	}

	// The kill switch: admin locks lee (revocation lane only; users.status
	// stays active, that field belongs to the IdM).
	if code := adminReq(t, "POST", base+"/v1/admin/users/"+lee.ID+"/lock", adminTok,
		map[string]any{"reason": "incident drill"}, nil); code != http.StatusOK {
		t.Fatalf("lock = %d", code)
	}

	ts := time.Now().Unix()
	_, spki2 := genApproverKey(t)
	denied := []struct {
		name     string
		method   string
		path     string
		bearer   string
		body     map[string]any
		wantCode string // machine-readable body code; "" = unasserted
	}{
		{"pending", "GET", "/v1/approver/pending?scope=decidable", enr.DeviceToken, nil, "user_inactive"},
		{"history", "GET", "/v1/approver/history", enr.DeviceToken, nil, "user_inactive"},
		{"push register", "PUT", "/v1/approver/push", enr.DeviceToken,
			map[string]any{"kind": "unifiedpush", "token_or_endpoint": "https://ntfy.example/t"}, "user_inactive"},
		{"decide", "POST", "/v1/approver/decide", enr.DeviceToken, map[string]any{
			"request_id": rec.ID, "verdict": "approve", "challenge": challenge,
			"signature": signApprover(t, priv, rec.ID, "approve", challenge, ts),
			"ts":        ts,
		}, "user_inactive"},
		{"refresh", "POST", "/v1/approver/refresh", "", map[string]any{
			"approver_device_id": enr.DeviceID, "challenge": refCh.Challenge,
			"signature": signRefresh(t, priv, enr.DeviceID, refCh.Challenge),
		}, "user_inactive"},
		// Enroll keeps its single coarse 401 (spent/expired/disabled all read
		// "enroll token invalid"; no oracle for WHY a token died).
		{"enroll", "POST", "/v1/approver/enroll", "", map[string]any{
			"enroll_token": mint2.EnrollToken,
			"device": map[string]any{
				"name": "lee-pixel-2", "platform": "android", "key_alg": "ecdsa-p256",
				"public_key": spki2, "key_security_level": "strongbox",
				"attestation": map[string]any{"kind": "none"},
			},
		}, ""},
	}
	for _, tc := range denied {
		t.Run("locked "+tc.name, func(t *testing.T) {
			var errBody struct {
				Code string `json:"code"`
			}
			if code := adminReq(t, tc.method, base+tc.path, tc.bearer, tc.body, &errBody); code != http.StatusUnauthorized {
				t.Fatalf("%s while locked = %d, want 401", tc.name, code)
			}
			if tc.wantCode != "" && errBody.Code != tc.wantCode {
				t.Errorf("%s while locked code = %q, want %q", tc.name, errBody.Code, tc.wantCode)
			}
		})
	}

	// Unlock lifts the lane: the SAME key and token resume (nothing was
	// destroyed: the lock is reversible, unlike device revocation).
	if code := adminReq(t, "POST", base+"/v1/admin/users/"+lee.ID+"/unlock", adminTok, nil, nil); code != http.StatusOK {
		t.Fatalf("unlock = %d", code)
	}
	if code := adminReq(t, "GET", base+"/v1/approver/pending?scope=decidable", enr.DeviceToken, nil, nil); code != http.StatusOK {
		t.Errorf("pending after unlock = %d, want 200", code)
	}
}
