package server

import (
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/authn"
)

// renewDeviceCredential is the credential-renewal half of check-in. Once the
// presented device credential is past half its life it mints a fresh one
// with the configured lifetime, records the renewal on
// straza.identity.updated, and returns the token with its lifetime; before
// that point it returns the empty token and no error. Callers run it only
// after every check-in gate passed, so a revoked device, a disabled user or
// an under-attested harness never receives a fresh credential. Half-life
// rather than every check-in keeps the mint and the audit line rare, and an
// active workstation never re-enrolls while one idle past the lifetime
// still dies.
func (a *App) renewDeviceCredential(r *http.Request, dc authn.DeviceClaims, userID, deviceID string) (string, time.Duration, error) {
	if !deviceCredentialPastHalfLife(dc, time.Now()) {
		return "", 0, nil
	}
	ttl := a.deviceTokenTTL()
	token, err := a.tokens.MintDeviceToken(userID, deviceID, ttl)
	if err != nil {
		return "", 0, err
	}
	a.emitEvent(r, "straza.identity.updated", map[string]any{
		"action": "renew", "user": userID, "device": deviceID,
	})
	return token, ttl, nil
}

// deviceTokenTTL is the configured enroll credential lifetime, the
// default when the knob is unset.
func (a *App) deviceTokenTTL() time.Duration {
	if ttl := a.cfg.Governance.DeviceTokenTTL; ttl > 0 {
		return ttl
	}
	return authn.DefaultDeviceTokenTTL
}

// deviceCredentialPastHalfLife reports whether a verified device credential
// has used up more than half of its own lifetime. A credential missing
// either timestamp is never renewed: no renewal is the safe answer.
func deviceCredentialPastHalfLife(dc authn.DeviceClaims, now time.Time) bool {
	if dc.IssuedAt.IsZero() || !dc.Expiry.After(dc.IssuedAt) {
		return false
	}
	return now.After(dc.IssuedAt.Add(dc.Expiry.Sub(dc.IssuedAt) / 2))
}
