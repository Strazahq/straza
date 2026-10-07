package server

import (
	"errors"
	"net/http"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

// refreshSession is the session-token lane of check-in: the daemon's refresh
// of a live session. It verifies the token in memory and judges the session
// with judgeSession. On a refusal it answers the request itself and reports
// false. On true the caller mints from the returned row, whose device
// binding, attestation level and harness name are authoritative, so no
// refresh payload can change the minted claims.
func (a *App) refreshSession(w http.ResponseWriter, r *http.Request, req checkinRequest) (store.User, store.Session, bool) {
	claims, err := a.tokens.Verify(req.SessionToken)
	if err != nil {
		a.emitAuthnLogin(r, "failure", authnFields{Via: "session-token",
			Harness: harnessLabel(req.Harness), Reason: "session token rejected"})
		apiError(w, http.StatusUnauthorized, "session token rejected: re-enroll or restart the session")
		return store.User{}, store.Session{}, false
	}
	return a.judgeSession(w, r, claims, "session refresh", harnessLabel(req.Harness))
}

// unreadSessionUserMsg is the 403 of a session whose user row could not be
// read while the store answers: the refusal fails closed and says so.
const unreadSessionUserMsg = "Straza could not read your user record, so it refuses the request. " +
	"Try again, and check the strazad log if it keeps failing."

// judgeSession judges a verified session token the same way on the refresh,
// the admin plane and the decide routes. It consults the denylist before
// any store read (the push-fed kill switch bites first, as on the
// device-token lane), then checks the session row and its user, so a
// disabled or deleted user and a lock are refused on the next request even
// when the session row stayed active. lane names the caller in the outage
// log and harness is the label the refusal record carries. On a refusal it
// writes one login failure record, answers the request itself and reports
// false. A store outage answers 503 and writes no record.
func (a *App) judgeSession(w http.ResponseWriter, r *http.Request, claims authn.Claims, lane, harness string) (store.User, store.Session, bool) {
	const via = "session-token"
	none := func() (store.User, store.Session, bool) { return store.User{}, store.Session{}, false }
	inactive := func() (store.User, store.Session, bool) {
		a.emitAuthnLogin(r, "failure", authnFields{Via: via,
			UserID: claims.Subject, Session: claims.Session,
			Harness: harness, Reason: "session is no longer active"})
		apiError(w, http.StatusUnauthorized, "session is no longer active")
		return none()
	}
	// A user or device revoke judges the identity: 403, terminal for the
	// daemon. A session revoke is a stand-down: 401, and the daemon starts a
	// fresh session from its device credential with no human action.
	if a.denylist.userBlocked(claims.Subject) || a.denylist.deviceBlocked(claims.Device) {
		a.emitAuthnLogin(r, "failure", authnFields{Via: via,
			UserID: claims.Subject, Session: claims.Session,
			Harness: harness, Reason: "device or user revoked"})
		apiError(w, http.StatusForbidden, revokedIdentityMsg)
		return none()
	}
	if a.denylist.blocked(claims) {
		return inactive()
	}
	ses, err := a.store.Sessions().GetByID(r.Context(), claims.Session)
	if err != nil && a.answerOutage(w, r, lane, err) {
		return none() // a store blip is not a dead session (the daemon would drop it)
	}
	if err != nil || ses.Status != store.SessionActive {
		return inactive()
	}
	u, err := a.store.Users().GetByID(r.Context(), ses.UserID)
	if err != nil && a.answerOutage(w, r, lane, err) {
		return none()
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.log.Warn("session judgment: the user record could not be read, refused (fail closed)",
			"lane", lane, "user", ses.UserID, "session", ses.ID, "err", err)
		a.emitAuthnLogin(r, "failure", authnFields{Via: via,
			UserID: ses.UserID, Session: ses.ID,
			Harness: harness, Reason: "user record could not be read"})
		apiError(w, http.StatusForbidden, unreadSessionUserMsg)
		return none()
	}
	if err != nil || u.Status != store.UserActive {
		a.emitAuthnLogin(r, "failure", authnFields{Via: via,
			UserID: ses.UserID, Session: ses.ID,
			Harness: harness, Reason: "user is disabled"})
		apiError(w, http.StatusForbidden, "user is disabled. Contact your administrator")
		return none()
	}
	return u, ses, true
}
