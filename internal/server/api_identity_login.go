package server

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

// verifyLogin maps a raw ID token to an active local user: built-in issuer
// first (its sub IS the user id), then the external IdP when configured
// (claim mapping + JIT knob). A lock leaves the status active, so the
// denylist is asked too. A disabled user, a locked user, and an external
// identity that names the break-glass account come back as a *loginRefusal.
func (a *App) verifyLogin(r *http.Request, rawIDToken string) (store.User, error) {
	var lastErr error
	for _, aud := range allowedAudiences {
		claims, err := a.tokens.VerifyIDToken(rawIDToken, aud)
		if err != nil {
			lastErr = err
			continue
		}
		u, err := a.store.Users().GetByID(r.Context(), claims.Subject)
		if err != nil {
			return store.User{}, fmt.Errorf("unknown subject: %w", err)
		}
		if u.Status != store.UserActive {
			return store.User{}, disabledRefusal(u)
		}
		if a.denylist.userBlocked(u.ID) {
			return store.User{}, lockedRefusal(u)
		}
		return u, nil
	}
	if a.external != nil {
		u, err := a.external.VerifyLogin(r.Context(), rawIDToken)
		if err == nil {
			if u.Status != store.UserActive {
				return store.User{}, disabledRefusal(u)
			}
			if a.denylist.userBlocked(u.ID) {
				return store.User{}, lockedRefusal(u)
			}
			a.maybeBootstrapAdmin(r, u)
			return u, nil
		}
		if errors.Is(err, authn.ErrProtectedAccount) {
			a.log.Warn("BREAK-GLASS: an external identity named as the emergency admin tried to sign in and was refused", "issuer", a.cfg.OIDC.Issuer)
			return store.User{}, &loginRefusal{
				user: BreakGlassUsername, reason: "external identity names the break-glass account",
				sentence: "the account " + BreakGlassUsername + " at your identity provider cannot sign in to Straza, because " + BreakGlassUsername +
					" is the local emergency admin and signs in with its own password only. Rename or remove that account at the identity provider.",
			}
		}
		var disabled *authn.DisabledUserError
		if errors.As(err, &disabled) {
			return store.User{}, disabledRefusal(disabled.User)
		}
		lastErr = err
	}
	return store.User{}, fmt.Errorf("id token not accepted: %w", lastErr)
}

// loginRefusal is verifyLogin's refusal of an identity that proved who it is
// and may not sign in. It carries what the caller records and answers: the
// user and the reason for the login record, and the sentence for the 403.
type loginRefusal struct {
	user, userID     string
	reason, sentence string
}

func (e *loginRefusal) Error() string { return e.reason }

// disabledRefusal is the refusal of a user whose status is not active, in
// the words the session lane answers a disabled user with.
func disabledRefusal(u store.User) *loginRefusal {
	return &loginRefusal{
		user: u.Username, userID: u.ID, reason: "user is disabled",
		sentence: "user is disabled. Contact your administrator",
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
// user and the reason, and a 403 with the sentence. harness is the label the
// client declared, empty on a lane that has none.
func (a *App) refuseLogin(w http.ResponseWriter, r *http.Request, e *loginRefusal, harness string) {
	a.emitAuthnLogin(r, "failure", authnFields{Via: "id-token", User: e.user, UserID: e.userID, Harness: harness, Reason: e.reason})
	apiError(w, http.StatusForbidden, e.sentence)
}
