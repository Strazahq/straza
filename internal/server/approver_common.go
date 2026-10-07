package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/strazahq/straza/internal/store"
)

// Machine-readable 401 codes for the approver surface. The mobile app branches
// on these before touching its hardware key: token_expired ⇒ attempt a
// device-key-signed refresh (KEEP the key); device_revoked ⇒ the device was
// retired, DESTROY the key; the rest ⇒ the credential is unusable and a refresh
// cannot fix it. Without this signal the app treats every 401 as revocation and
// self-destructs its key at natural expiry.
const (
	codeMissingToken  = "missing_token"
	codeTokenExpired  = "token_expired"
	codeTokenInvalid  = "token_invalid"
	codeDeviceRevoked = "device_revoked"
	codeUserInactive  = "user_inactive"
)

// codeInvalidCursor is the 400 classifier on the history feed when the opaque
// pagination cursor fails to decode or carries an unknown version. Unlike the
// 401 codes above it is NOT an auth failure: the app drops the cursor and
// re-fetches from the top; the hardware key is untouched (mirrors the tool
// catalog's fail-closed "re-issue from the start").
const codeInvalidCursor = "invalid_cursor"

// Decide-path outcome codes. The verification family is deliberately COARSE:
// codeChallengeRejected is the SINGLE code for every unverifiable decide, so a
// stale or skewed timestamp, a bad or unparseable device key, a bad signature,
// and a missing, expired or replayed challenge all collapse into it. Per-cause
// codes would hand an attacker a signature oracle telling a forger WHY a
// forgery failed. The app reads challenge_rejected as one instruction, re-fetch
// a fresh challenge and retry the decision, and NEVER destroys its hardware key
// on it; device_revoked stays the only decide-path key-destroy signal.
//
// codeNotAuthorized is the 403 classifier: the bound user may not decide THIS
// record (a fresh approve-role check found none, ErrNotApprover, or four-eyes
// bars self-approval, ErrSelfApproval). It is an authorization outcome, not a
// credential fault: token and key stay valid, so the app shows "you can't
// approve this" and leaves the key untouched.
const (
	codeChallengeRejected = "challenge_rejected"
	codeNotAuthorized     = "not_authorized"
)

// apiErrorCode writes the approver-scoped error envelope {"error":…,"code":…}.
// It is deliberately separate from the shared apiError (api_identity.go), whose
// {"error":…} shape every other surface depends on; only the approver 401s and
// refresh failures carry the extra machine-readable code.
func apiErrorCode(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// failCode is apiErrorCode for 5xx answers: the same coded
// envelope plus correlation_id, and exactly one Error record carrying the
// id (middleware.go logFailure). The app's classifier keys on `code`, which
// is untouched; `correlation_id` is NOT the approval `request_id`.
func (a *App) failCode(w http.ResponseWriter, r *http.Request, status int, code, msg string, err error) {
	a.logFailure(r, strconv.Itoa(status), msg, err, "code", code)
	a.countHTTPError(r, strconv.Itoa(status))
	writeJSON(w, status, map[string]string{"error": msg, "code": code, correlationKey: reqID(r.Context())})
}

// The mobile approver surface. Every handler is a thin HTTP shell over the
// internal/approval service; the crypto, eligibility, and store logic live
// there. Enrolment is unauthenticated (the one-time enroll token IS the
// credential); every other approver route requires a use=approver device token
// (requireApprover); a session/login/device/connect token is refused.

// requireApprover authenticates a mobile approver device token and hands the
// handler the (deviceID, userID) it binds. Verification is row-backed: the
// device row must still exist (deleting it revokes the device) and the bound
// user must be active AND off the in-memory revocation denylist (the
// admin/external kill switch never touches users.status, so the status check
// alone would leave a locked user's phone deciding approvals). Fail closed on
// anything else.
func (a *App) requireApprover(next func(w http.ResponseWriter, r *http.Request, deviceID, userID string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := bearerToken(r)
		if raw == "" {
			apiErrorCode(w, http.StatusUnauthorized, codeMissingToken, "missing bearer token")
			return
		}
		claims, err := a.tokens.VerifyApproverToken(raw)
		if err != nil {
			apiErrorCode(w, http.StatusUnauthorized, approverVerifyErrorCode(err), "approver token rejected")
			return
		}
		dev, err := a.store.Approvers().GetDevice(r.Context(), claims.Device)
		if err != nil && a.answerOutageCode(w, r, "approver auth", err) {
			return // an outage is not a revocation: device_revoked makes the app wipe its key
		}
		if err != nil || dev.UserID != claims.Subject {
			// The device row is the credential; its absence is revocation.
			apiErrorCode(w, http.StatusUnauthorized, codeDeviceRevoked, "approver device revoked. Re-enroll")
			return
		}
		u, err := a.store.Users().GetByID(r.Context(), claims.Subject)
		if err != nil && a.answerOutageCode(w, r, "approver auth", err) {
			return
		}
		if err != nil || u.Status != store.UserActive || a.denylist.userBlocked(claims.Subject) {
			a.auditApproverInactive(r, u, claims, err)
			apiErrorCode(w, http.StatusUnauthorized, codeUserInactive, "user is not active")
			return
		}
		a.approverRefused.Delete(approverRefusalKey(claims))
		next(w, r, dev.ID, claims.Subject)
	}
}

// approverVerifyErrorCode classifies a VerifyApproverToken failure as
// token_expired vs token_invalid. The jwx expiry sentinel propagates through
// VerifyApproverToken's %w wrap (pinned empirically by
// TestApproverTokenExpiredSentinelProbe), so a plain errors.Is is authoritative
// and no unverified re-parse is needed. The distinction is the whole point of the
// codes: token_expired routes the app to a key-preserving refresh, everything
// else (bad signature, wrong purpose, malformed) to token_invalid.
func approverVerifyErrorCode(err error) string {
	if errors.Is(err, jwt.TokenExpiredError()) {
		return codeTokenExpired
	}
	return codeTokenInvalid
}
