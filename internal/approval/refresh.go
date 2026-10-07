package approval

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"fmt"

	"github.com/strazahq/straza/internal/store"
)

// refreshApprovalID is the sentinel approval_id under which refresh challenges
// live in approver_challenges: it reuses the decide flow's table and its
// atomic single-use ConsumeChallenge gate with NO schema change. Real approval
// ids are opaque record ids, so this literal can never collide with one, and it
// scopes a refresh challenge to the refresh flow: a challenge minted here
// consumes only against this id, never against a pending decision.
const refreshApprovalID = "__refresh__"

// RefreshInput is the parsed POST /v1/approver/refresh body: the device proving
// possession of its enrolled key over a fetched challenge.
type RefreshInput struct {
	DeviceID     string
	Challenge    string
	SignatureB64 string // base64 ASN.1 DER (X9.62), variable length
}

// RefreshChallenge mints a fresh single-use challenge for a device-key-signed
// token refresh (mirrors the decide flow's per-fetch challenge, under the
// refresh sentinel so no schema changes). It gates on the device row existing:
// an unknown/revoked device denies with ErrDeviceRevoked, which the handler maps
// to 404 (there is nothing to retire, and the inert nonce is useless without the
// private key; echoing a device id is not a leak). No bearer is required by the
// caller: an expired token must never block its own refresh.
func (s *Service) RefreshChallenge(ctx context.Context, deviceID string) (string, error) {
	if _, err := s.st.Approvers().GetDevice(ctx, deviceID); err != nil {
		if store.IsUnavailable(err) {
			return "", fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
		}
		return "", ErrDeviceRevoked
	}
	return s.mintChallenge(ctx, deviceID, refreshApprovalID)
}

// RefreshSigned verifies a device-key-signed refresh and returns the identity a
// fresh token must bind (userID, deviceID), mirroring DecideSigned, minus the
// mint (the token service lives in the server layer, matching the enroll/decide
// split). Order: device row (revocation) → bound user active and off the
// revocation denylist → ECDSA-P256 verify
// over the REFRESH-scoped canonical message (a distinct domain from the decide
// message, so a decide signature can never be replayed as a refresh) → atomic
// single-use challenge consume (replay gate). Fail closed on every step.
func (s *Service) RefreshSigned(ctx context.Context, in RefreshInput) (userID, deviceID string, err error) {
	dev, err := s.st.Approvers().GetDevice(ctx, in.DeviceID)
	if err != nil {
		if store.IsUnavailable(err) {
			return "", "", fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
		}
		return "", "", ErrDeviceRevoked
	}
	u, err := s.st.Users().GetByID(ctx, dev.UserID)
	if err != nil && store.IsUnavailable(err) {
		return "", "", fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	if err != nil || u.Status != store.UserActive || s.userBlocked(u.ID) {
		return "", "", ErrUserInactive
	}
	pub, err := parseP256SPKI(dev.PublicKey)
	if err != nil {
		return "", "", ErrBadKey
	}
	// The refresh-scoped signed byte string (UTF-8, no trailing newline). The
	// leading "refresh" domain tag makes this message-space disjoint from the
	// decide message (request_id-led), so a signature captured from a decide can
	// never verify here. Variable-length DER: decode and verify, never length-check.
	msg := "refresh\n" + in.DeviceID + "\n" + in.Challenge
	sig, err := base64.StdEncoding.DecodeString(in.SignatureB64)
	if err != nil {
		return "", "", ErrBadSignature
	}
	digest := sha256.Sum256([]byte(msg))
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		return "", "", ErrBadSignature
	}
	won, err := s.st.Approvers().ConsumeChallenge(ctx, in.Challenge, in.DeviceID, refreshApprovalID, s.now())
	if err != nil {
		return "", "", err
	}
	if !won {
		return "", "", ErrChallengeInvalid
	}
	return dev.UserID, dev.ID, nil
}
