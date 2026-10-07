package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

// loginOutageBody is the one sentence every login-lane outage answers with.
// Generic by design: the cause (which store, which SQLSTATE, which host) is
// an operator concern and goes to the log, never to the person holding the
// credential. Clients print it verbatim (straza enroll, the console login,
// the approvals page), so it reads as a sentence on its own.
const loginOutageBody = "Straza is temporarily unavailable. Try again in a moment"

// loginOutageRetryAfter is advisory: the daemon and the console retry on
// their own clocks; a human reads it as "a moment".
const loginOutageRetryAfter = "5"

// loginOutage reports whether a failed identity step on enroll/checkin is an
// infrastructure outage (store or issuer unreachable, timeouts) rather than a
// refusal of the credential. Positive driver/transport shapes first (the
// store seam knows its drivers); an unknown shape asks the store once, so a
// fault this code has never seen still answers honestly when the store is in
// fact down, and stays a refusal (today's behaviour, the safe default) when
// it is up. Never a request-path read: these lanes already read the store.
func (a *App) loginOutage(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if store.IsUnavailable(err) {
		return true
	}
	// Data-level outcomes the lanes produce themselves are never an outage,
	// so they skip the ping: no such row, a refused sign-in such as a
	// disabled or locked user, unknown identity with JIT off.
	if errors.Is(err, store.ErrNotFound) || errors.As(err, new(*loginRefusal)) || errors.Is(err, authn.ErrUnknownIdentity) {
		return false
	}
	pingCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return a.store.Ping(pingCtx) != nil
}

// answerOutage writes the 503 when err is an outage and reports whether it
// did; callers fall through to their own refusal otherwise. The cause lands
// in the log at Error (operator-actionable, one record carrying the
// correlation id), the body stays generic plus the id.
func (a *App) answerOutage(w http.ResponseWriter, r *http.Request, lane string, err error) bool {
	if !a.loginOutage(r.Context(), err) {
		return false
	}
	w.Header().Set("Retry-After", loginOutageRetryAfter)
	a.fail(w, r, http.StatusServiceUnavailable, loginOutageBody, fmt.Errorf("%s: %w", lane, err))
	return true
}

// codeServiceUnavailable is the approver surface's machine-readable code for
// the same answer (the app's classifier branches on codes; an outage is
// neither device_revoked nor user_inactive, both of which make it destroy
// its local credential).
const codeServiceUnavailable = "service_unavailable"

// answerOutageCode is answerOutage for the coded approver surface.
func (a *App) answerOutageCode(w http.ResponseWriter, r *http.Request, lane string, err error) bool {
	if !a.loginOutage(r.Context(), err) {
		return false
	}
	w.Header().Set("Retry-After", loginOutageRetryAfter)
	a.failCode(w, r, http.StatusServiceUnavailable, codeServiceUnavailable, loginOutageBody, fmt.Errorf("%s: %w", lane, err))
	return true
}

// answerServiceOutageCode is answerOutageCode for errors a service already
// classified (approval.ErrStoreUnavailable): no second look at the cause,
// the 503 is unconditional. Same generic body, same Retry-After, cause at
// log Error with the correlation id.
func (a *App) answerServiceOutageCode(w http.ResponseWriter, r *http.Request, lane string, err error) {
	w.Header().Set("Retry-After", loginOutageRetryAfter)
	a.failCode(w, r, http.StatusServiceUnavailable, codeServiceUnavailable, loginOutageBody, fmt.Errorf("%s: %w", lane, err))
}
