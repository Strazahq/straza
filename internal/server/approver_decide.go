package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/strazahq/straza/internal/approval"
)

// --- decide ---

type approverDecideRequest struct {
	RequestID string `json:"request_id"`
	Verdict   string `json:"verdict"`
	Challenge string `json:"challenge"`
	Signature string `json:"signature"`
	TS        int64  `json:"ts"`
	// Reason (0.63.0): the decider's optional own words, both verdicts.
	// When present the signature must cover the 5-line string (its sha256 is
	// the 5th line); clients that omit it sign the canonical 4-line
	// reason-less string.
	Reason string `json:"reason,omitempty"`
}

// handleApproverDecide serves POST /v1/approver/decide: a hardware-signed,
// single-use, idempotent verdict. A resolved-conflict returns 409 with the
// final state (the app renders server state, never a stale button).
func (a *App) handleApproverDecide(w http.ResponseWriter, r *http.Request, deviceID, userID string) {
	var req approverDecideRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "malformed JSON body")
		return
	}
	rec, err := a.approval.DecideSigned(r.Context(), approval.SignedDecision{
		RequestID: req.RequestID, Verdict: req.Verdict, Challenge: req.Challenge,
		SignatureB64: req.Signature, TS: req.TS, DeviceID: deviceID, DeciderUserID: userID,
		Reason: req.Reason,
	})
	if err != nil {
		if errors.Is(err, approval.ErrStoreUnavailable) {
			a.answerServiceOutageCode(w, r, "approver decide", err)
			return
		}
		if errors.Is(err, approval.ErrConflict) {
			if cur, e := a.approval.Get(r.Context(), req.RequestID); e == nil {
				writeJSON(w, http.StatusConflict, map[string]any{"state": string(cur.State)})
				return
			}
		}
		if errors.Is(err, approval.ErrNHIDecider) {
			a.refuseNonPersonDecider(w, r, userID, req.RequestID)
			return
		}
		// Additive: an empty code keeps the plain {"error":…} envelope
		// byte-identical to the pre-code behavior; a non-empty code adds the
		// machine-readable field the mobile app branches on. Message is
		// err.Error() either way.
		if status, code := approverDecideError(err); status >= http.StatusInternalServerError {
			a.fail(w, r, status, err.Error(), err)
		} else if code != "" {
			apiErrorCode(w, status, code, err.Error())
		} else {
			apiError(w, status, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": string(rec.State)})
}

// nonPersonDeciderRefusal is the 403 sentence of a signed decision whose user
// is not a person, formatted with the username and userTypeWord.
const nonPersonDeciderRefusal = "the user %[1]s is %[2]s, and only a person can decide a request, so this decision was not recorded. " +
	"If %[1]s is a person, ask an administrator to set its user_type back to human with PATCH /v1/admin/users/{id}, " +
	"or in the identity manager when one manages the user."

// refuseNonPersonDecider answers 403 not_authorized to a signed decision
// whose user an administrator or the identity manager retyped as an AI agent
// or a service account after its phone enrolled. The record stays pending.
// The user is read again for the sentence; when that read fails the sentence
// names the user id.
func (a *App) refuseNonPersonDecider(w http.ResponseWriter, r *http.Request, userID, requestID string) {
	u, err := a.store.Users().GetByID(r.Context(), userID)
	if err != nil && a.answerOutageCode(w, r, "approver decide", err) {
		return
	}
	a.auditApproverNonPerson(r, u, userID, requestID)
	name, word := u.Username, userTypeWord(u)
	if err != nil {
		name, word = userID, "not a person"
	}
	apiErrorCode(w, http.StatusForbidden, codeNotAuthorized, fmt.Sprintf(nonPersonDeciderRefusal, name, word))
}

// approverDecideError maps a DecideSigned failure to its HTTP status and the
// machine-readable code the mobile app branches on: the single source of truth
// for the decide path. An empty code ⇒ the caller writes the plain {"error":…}
// envelope (no code field). The whole verification family collapses to one
// coarse code on purpose (see codeChallengeRejected); device_revoked is the only
// key-destroy signal; the authorization outcomes carry not_authorized.
//
// ErrConflict never reaches here on the happy path; the handler resolves it
// first (409 + final state); the mapper still returns a plain 409 for the
// degenerate fallthrough where that final-state Get hit a store error (the
// record IS resolved, so a 500 would invite a pointless retry, matching
// pre-code parity). ErrUserInactive is not decide-reachable
// (requireApprover gates an inactive user upstream; DecideSigned/Decide never
// return it), so it is intentionally not special-cased and falls to the default.
func approverDecideError(err error) (status int, code string) {
	switch {
	case errors.Is(err, approval.ErrBadVerdict), errors.Is(err, approval.ErrBadReason):
		return http.StatusBadRequest, ""
	case errors.Is(err, approval.ErrStaleTimestamp),
		errors.Is(err, approval.ErrBadKey),
		errors.Is(err, approval.ErrBadSignature),
		errors.Is(err, approval.ErrChallengeInvalid):
		return http.StatusUnauthorized, codeChallengeRejected
	case errors.Is(err, approval.ErrDeviceRevoked):
		return http.StatusUnauthorized, codeDeviceRevoked
	case errors.Is(err, approval.ErrSelfApproval),
		errors.Is(err, approval.ErrNotApprover),
		errors.Is(err, approval.ErrNotRequester),
		errors.Is(err, approval.ErrNHIDecider):
		return http.StatusForbidden, codeNotAuthorized
	case errors.Is(err, approval.ErrNotFound):
		return http.StatusNotFound, ""
	case errors.Is(err, approval.ErrExpired):
		return http.StatusGone, ""
	case errors.Is(err, approval.ErrConflict):
		return http.StatusConflict, ""
	default:
		return http.StatusInternalServerError, ""
	}
}
