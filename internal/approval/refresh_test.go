package approval

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// signRefresh signs the refresh-scoped canonical message with a device key.
// The "refresh" domain tag makes this message-space disjoint from the decide
// message (request_id-led), so the two signatures are never interchangeable.
func signRefresh(t *testing.T, priv *ecdsa.PrivateKey, deviceID, challenge string) string {
	t.Helper()
	msg := "refresh\n" + deviceID + "\n" + challenge
	d := sha256.Sum256([]byte(msg))
	sig, err := ecdsa.SignASN1(rand.Reader, priv, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// TestApproverRefreshSignedEndToEnd drives the device-key-signed refresh: the
// enrolled hardware key fetches a challenge, signs it, and RefreshSigned returns
// the identity a fresh token must bind. A replay of the same signed request is
// then refused (the challenge is single-use).
func TestApproverRefreshSignedEndToEnd(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	approver := h.seedUser(t, "kim", "sec-approvers")
	priv, spki := genP256(t)
	deviceID := h.enroll(t, approver.ID, spki)

	ch, err := h.svc.RefreshChallenge(ctx, deviceID)
	if err != nil || ch == "" {
		t.Fatalf("RefreshChallenge = %q, %v", ch, err)
	}
	sig := signRefresh(t, priv, deviceID, ch)
	uid, did, err := h.svc.RefreshSigned(ctx, RefreshInput{DeviceID: deviceID, Challenge: ch, SignatureB64: sig})
	if err != nil {
		t.Fatalf("RefreshSigned: %v", err)
	}
	if uid != approver.ID || did != deviceID {
		t.Fatalf("RefreshSigned identity = (%s,%s), want (%s,%s)", uid, did, approver.ID, deviceID)
	}

	// Replay of the exact signed request: challenge already consumed → deny.
	if _, _, err := h.svc.RefreshSigned(ctx, RefreshInput{DeviceID: deviceID, Challenge: ch, SignatureB64: sig}); err != ErrChallengeInvalid {
		t.Errorf("replay = %v, want ErrChallengeInvalid", err)
	}
}

// TestApproverRefreshChallengeUnknownDevice: minting a refresh challenge for a
// device with no row is refused (the handler maps this to 404: nothing to
// retire, and the inert nonce is not a secret).
func TestApproverRefreshChallengeUnknownDevice(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.RefreshChallenge(context.Background(), "apd_nope"); err != ErrDeviceRevoked {
		t.Errorf("RefreshChallenge unknown device = %v, want ErrDeviceRevoked", err)
	}
}

// TestApproverRefreshSignedDenies is the table of fail-closed refresh paths:
// wrong key, a signature over a tampered message, a DECIDE signature replayed as
// a refresh (domain separation), an expired challenge, a revoked (row-deleted)
// device, and an inactive bound user; each denies with its sentinel.
func TestApproverRefreshSignedDenies(t *testing.T) {
	cases := []struct {
		name string
		// prep mutates state as needed and returns the (challenge, signature) to submit.
		prep    func(t *testing.T, h *harness, approver store.User, priv *ecdsa.PrivateKey, deviceID string) (challenge, sig string)
		wantErr error
	}{
		{
			name: "wrong key",
			prep: func(t *testing.T, h *harness, _ store.User, _ *ecdsa.PrivateKey, deviceID string) (string, string) {
				ch, _ := h.svc.RefreshChallenge(context.Background(), deviceID)
				other, _ := genP256(t)
				return ch, signRefresh(t, other, deviceID, ch)
			},
			wantErr: ErrBadSignature,
		},
		{
			name: "signature over a tampered message",
			prep: func(t *testing.T, h *harness, _ store.User, priv *ecdsa.PrivateKey, deviceID string) (string, string) {
				ch, _ := h.svc.RefreshChallenge(context.Background(), deviceID)
				// Sign a DIFFERENT challenge string than the one submitted.
				return ch, signRefresh(t, priv, deviceID, ch+"x")
			},
			wantErr: ErrBadSignature,
		},
		{
			name: "decide signature replayed as refresh",
			prep: func(t *testing.T, h *harness, _ store.User, priv *ecdsa.PrivateKey, deviceID string) (string, string) {
				ch, _ := h.svc.RefreshChallenge(context.Background(), deviceID)
				// A perfectly valid DECIDE signature over the same nonce is worthless
				// here: the refresh message space is a disjoint domain.
				sig := signDecision(t, priv, "req-1", "approve", ch, time.Now().Unix())
				return ch, sig
			},
			wantErr: ErrBadSignature,
		},
		{
			name: "expired challenge",
			prep: func(t *testing.T, h *harness, _ store.User, priv *ecdsa.PrivateKey, deviceID string) (string, string) {
				ch, _ := h.svc.RefreshChallenge(context.Background(), deviceID)
				h.svc.now = func() time.Time { return time.Now().Add(2 * ChallengeTTL) }
				return ch, signRefresh(t, priv, deviceID, ch)
			},
			wantErr: ErrChallengeInvalid,
		},
		{
			name: "revoked device",
			prep: func(t *testing.T, h *harness, _ store.User, priv *ecdsa.PrivateKey, deviceID string) (string, string) {
				ch, _ := h.svc.RefreshChallenge(context.Background(), deviceID)
				if err := h.st.Approvers().DeleteDevice(context.Background(), deviceID); err != nil {
					t.Fatalf("DeleteDevice: %v", err)
				}
				return ch, signRefresh(t, priv, deviceID, ch)
			},
			wantErr: ErrDeviceRevoked,
		},
		{
			name: "inactive user",
			prep: func(t *testing.T, h *harness, approver store.User, priv *ecdsa.PrivateKey, deviceID string) (string, string) {
				ch, _ := h.svc.RefreshChallenge(context.Background(), deviceID)
				u, err := h.st.Users().GetByID(context.Background(), approver.ID)
				if err != nil {
					t.Fatalf("GetByID: %v", err)
				}
				u.Status = store.UserDisabled
				if _, err := h.st.Users().Update(context.Background(), u); err != nil {
					t.Fatalf("Update: %v", err)
				}
				return ch, signRefresh(t, priv, deviceID, ch)
			},
			wantErr: ErrUserInactive,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			approver := h.seedUser(t, "kim", "sec-approvers")
			priv, spki := genP256(t)
			deviceID := h.enroll(t, approver.ID, spki)
			ch, sig := tc.prep(t, h, approver, priv, deviceID)
			_, _, err := h.svc.RefreshSigned(context.Background(), RefreshInput{
				DeviceID: deviceID, Challenge: ch, SignatureB64: sig,
			})
			if err != tc.wantErr {
				t.Errorf("RefreshSigned = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
