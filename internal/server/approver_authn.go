package server

import (
	"context"
	"net/http"

	"github.com/strazahq/straza/internal/authn"
	"github.com/strazahq/straza/internal/store"
)

// approverTokenVia names the credential of the signed phone lane in a
// straza.audit.authn record: the approver token minted from the device key.
const approverTokenVia = "approver-token"

// approverRefusalKey keys App.approverRefused by the device and the id of
// the approver token.
func approverRefusalKey(c authn.ApproverClaims) string { return c.Device + " " + c.JTI }

// auditApproverInactive writes the straza.audit.authn login failure of
// requireApprover's user_inactive refusal. The reason comes from the same
// checks that refuse, in the login lanes' words: a user row that is gone or
// not active is "user is disabled", the lock denylist "user is locked". u is
// zero when the row could not be read, and the record then names the userId
// only. The phone keeps polling every 15 seconds while its user is
// suspended, so one approver token writes one record per reason: the token
// is marked once the record is in the outbox, a failed write leaves it
// unmarked for the next refused request, and a request that requireApprover
// accepts forgets it, so the next suspension writes again. The memory is per
// replica. The write outlives the request and waits at most
// auditInsertTimeout, as emitEventCtx's does, because the refusal has
// already happened when the phone's connection drops.
func (a *App) auditApproverInactive(r *http.Request, u store.User, c authn.ApproverClaims, readErr error) {
	reason := "user is locked"
	if readErr != nil || u.Status != store.UserActive {
		reason = "user is disabled"
	}
	key := approverRefusalKey(c)
	if prev, seen := a.approverRefused.Load(key); seen && prev == reason {
		return
	}
	// emitEventCtx reports no failure, and the mark needs one, so this
	// write derives the same context itself.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditInsertTimeout)
	defer cancel()
	f := authnFields{Via: approverTokenVia, User: u.Username, UserID: c.Subject, Reason: reason}
	ev, err := a.cloudEvent(ctx, "straza.audit.authn", authnPayload("login", "failure", f, r))
	if err == nil {
		_, err = a.store.Outbox().Insert(ctx, ev)
	}
	if err != nil {
		a.log.Warn("approver auth: the user_inactive refusal record could not be written; the next refused request writes it",
			"user", c.Subject, "device", c.Device, "err", err)
		return
	}
	a.approverRefused.Store(key, reason)
}

// auditApproverNonPerson writes the straza.audit.authn login failure of a
// signed decision refused because its user is not a person, naming the
// request it tried to decide. Each decision spends its own single-use
// challenge, so the record is written once per decision. u is zero when the
// row could not be read, and the record then names the userId only.
func (a *App) auditApproverNonPerson(r *http.Request, u store.User, userID, approvalID string) {
	a.emitAuthnLogin(r, "failure", authnFields{Via: approverTokenVia, User: u.Username, UserID: userID,
		ApprovalID: approvalID, Reason: "only a person can decide a request"})
}
