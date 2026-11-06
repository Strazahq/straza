package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/dpop"
	"github.com/strazahq/straza/internal/store"
)

// verifyLogin is verifyLoginFacts without the sign-in facts.
func (a *App) verifyLogin(r *http.Request, rawIDToken string) (store.User, error) {
	u, _, err := a.verifyLoginFacts(r, rawIDToken)
	return u, err
}

// verifyLoginFacts maps a raw ID token to an active local user: built-in
// issuer first (its sub IS the user id), then the external IdP when
// configured (claim mapping + JIT knob). Beside the user it answers the
// token's nonce, auth_time and key stamp, and for a built-in token the
// audience it verified for, which a person's key enrollment judges. A lock
// leaves the status active, so the denylist is asked too. A disabled user, a
// locked user, a built-in token stamped for a key the user no longer has
// registered, and an external identity that names the break-glass account
// come back as a *loginRefusal. A failed read of the registered key comes
// back as the store's error, which the callers answer as an outage.
func (a *App) verifyLoginFacts(r *http.Request, rawIDToken string) (store.User, signInFacts, error) {
	none := signInFacts{}
	var lastErr error
	for _, aud := range allowedAudiences {
		claims, err := a.tokens.VerifyIDToken(rawIDToken, aud)
		if err != nil {
			lastErr = err
			continue
		}
		u, err := a.store.Users().GetByID(r.Context(), claims.Subject)
		if err != nil {
			return store.User{}, none, fmt.Errorf("unknown subject: %w", err)
		}
		if u.Status != store.UserActive {
			return store.User{}, none, disabledRefusal(u)
		}
		if a.denylist.userBlocked(u.ID) {
			return store.User{}, none, lockedRefusal(u)
		}
		if claims.KeyThumbprint != "" {
			if err := a.checkKeyStamp(r.Context(), u, claims.KeyThumbprint); err != nil {
				return store.User{}, none, err
			}
		}
		return u, signInFacts{authn.LoginFacts{Nonce: claims.Nonce, AuthTime: claims.AuthTime, KeyThumbprint: claims.KeyThumbprint}, aud}, nil
	}
	if a.external != nil {
		u, facts, err := a.external.VerifyLoginFacts(r.Context(), rawIDToken)
		if err == nil {
			if u.Status != store.UserActive {
				return store.User{}, none, disabledRefusal(u)
			}
			if a.denylist.userBlocked(u.ID) {
				return store.User{}, none, lockedRefusal(u)
			}
			a.maybeBootstrapAdmin(r, u)
			return u, signInFacts{LoginFacts: facts}, nil
		}
		if errors.Is(err, authn.ErrProtectedAccount) {
			a.log.Warn("BREAK-GLASS: an external identity named as the emergency admin tried to sign in and was refused", "issuer", a.cfg.OIDC.Issuer)
			return store.User{}, none, &loginRefusal{
				user: BreakGlassUsername, reason: "external identity names the break-glass account",
				sentence: "the account " + BreakGlassUsername + " at your identity provider cannot sign in to Straza, because " + BreakGlassUsername +
					" is the local emergency admin and signs in with its own password only. Rename or remove that account at the identity provider.",
			}
		}
		var disabled *authn.DisabledUserError
		if errors.As(err, &disabled) {
			return store.User{}, none, disabledRefusal(disabled.User)
		}
		lastErr = err
	}
	return store.User{}, none, fmt.Errorf("id token not accepted: %w", lastErr)
}

// loginRefusal is verifyLogin's refusal of an identity that proved who it is
// and may not sign in. It carries what the caller records and answers: the
// user and the reason for the login record, and the sentence and the status
// for the answer, 403 when status is zero.
type loginRefusal struct {
	user, userID     string
	reason, sentence string
	status           int
}

func (e *loginRefusal) Error() string { return e.reason }

// disabledUserMsg is the 403 sentence for a request by a user whose status is
// not active, on every lane that judges the user: a sign-in with an ID token,
// a check-in with a device credential or a key, and a request on a live
// session. It keeps the words user is disabled, which the hint catalog of
// straza doctor matches, and names who can enable the user again.
const disabledUserMsg = "this user is disabled, so Straza refuses this request. " +
	"Ask your IGA owner or a Straza administrator to enable the user."

// disabledRefusal is the refusal of a user whose status is not active.
func disabledRefusal(u store.User) *loginRefusal {
	return &loginRefusal{
		user: u.Username, userID: u.ID, reason: "user is disabled",
		sentence: disabledUserMsg,
	}
}

// lockedRefusal is the refusal of a user whose lock is on the denylist.
func lockedRefusal(u store.User) *loginRefusal {
	return &loginRefusal{
		user: u.Username, userID: u.ID, reason: "user is locked",
		sentence: fmt.Sprintf("the user %s is locked, so this sign-in is refused. An administrator lifts the lock with strazactl users unlock %s.", u.Username, u.Username),
	}
}

// refuseLogin answers a refused sign-in: one login failure record with the
// user and the reason, and the refusal's status, 403 unless it names
// another, with the sentence. harness is the label the client declared,
// empty on a lane that has none.
func (a *App) refuseLogin(w http.ResponseWriter, r *http.Request, e *loginRefusal, harness string) {
	a.emitAuthnLogin(r, "failure", authnFields{Via: "id-token", User: e.user, UserID: e.userID, Harness: harness, Reason: e.reason})
	status := e.status
	if status == 0 {
		status = http.StatusForbidden
	}
	apiError(w, status, e.sentence)
}

// The refusal of an agent's ID token stamped for a key that is no longer
// registered for it. It answers 401, because straza service fetches a new
// ID token only after a 401.
const (
	staleKeyStampReason = "the ID token was issued for a key that is no longer registered"
	staleKeyStampMsg    = "This agent's ID token was issued for a key that is no longer registered for it. " +
		"The Straza background service fetches a new token with the current key on its own. " +
		"If the refusal repeats, register the agent's current public key with strazactl users nhi-key set."
)

// checkKeyStamp compares the key stamp of u's built-in ID token with the
// key registered for u now. A missing or malformed key row, or another
// key, is a 401 *loginRefusal. A failed read returns the store's error.
func (a *App) checkKeyStamp(ctx context.Context, u store.User, stamp string) error {
	key, err := a.registeredNHIKey(ctx, u.ID)
	switch {
	case err == nil && dpop.Thumbprint(key) == stamp:
		return nil
	case err == nil, errors.Is(err, store.ErrNotFound), errors.Is(err, errNHIKeyMalformed):
		return &loginRefusal{user: u.Username, userID: u.ID, reason: staleKeyStampReason,
			sentence: staleKeyStampMsg, status: http.StatusUnauthorized}
	}
	return fmt.Errorf("read the registered key of %s: %w", u.ID, err)
}

// keepStampedSession checks the key stamp of u's id-token check-in again
// once its session row exists, because a key change writes the key before
// it lists the sessions to end. It reports true when the stamp still holds.
// Otherwise it ends the session, which was never started or handed out, so
// no session.end is written, and answers the stamp 401 with one login
// failure, the outage 503, or the login lane's 401 for any other error.
func (a *App) keepStampedSession(w http.ResponseWriter, r *http.Request, u store.User, sessionID, stamp string, harness harnessInfo) bool {
	err := a.checkKeyStamp(r.Context(), u, stamp)
	if err == nil {
		return true
	}
	var refused *loginRefusal
	reason := "the agent's key could not be read when the session started"
	if errors.As(err, &refused) {
		reason = staleKeyStampReason
	}
	if _, endErr := a.endSessions(r.Context(), []string{sessionID}, reason); endErr != nil {
		// No token names the session, so the row only waits for the janitor.
		a.log.Error("check-in: a session refused at its second key check could not be ended",
			"user", u.ID, "session", sessionID, "err", endErr)
	}
	switch {
	case errors.As(err, &refused):
		a.refuseLogin(w, r, refused, harnessLabel(harness))
	case a.answerOutage(w, r, "session exchange", err):
	default:
		a.log.Warn("session exchange: login not accepted", "err", err)
		a.emitAuthnLogin(r, "failure", authnFields{Via: "id-token", User: u.Username, UserID: u.ID,
			Harness: harnessLabel(harness), Reason: "login not accepted"})
		apiError(w, http.StatusUnauthorized, "login not accepted")
	}
	return false
}
