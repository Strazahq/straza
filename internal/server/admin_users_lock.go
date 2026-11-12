package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/strazahq/straza/internal/store"
)

// The 500 sentences of a lock whose revocation row or straza.revocation.user
// event the database refused. Each names the user, says how far the lock
// reached and gives the request id.
const (
	userLockHereOnlyMsg = "user %s is locked on this server only, until it restarts. Straza could not write the lock to its database or the event that tells the other servers, so they still admit the user. " +
		"Run the lock again, and check the server's database connection if it fails again. The strazad log names the cause under request id %s."
	userLockUnsentMsg = "user %s is locked and the lock is saved, but only this server refuses the user now. Straza could not write the event that tells the other servers, so they admit the user until they restart. " +
		"Run strazactl users lock %s again with the same reason, which writes the event again. The strazad log names the cause under request id %s."
	userLockUnsavedMsg = "user %s is locked on every server, but Straza could not write the lock to its database, so the console and your identity manager show no lock, and an identity manager that disables and enables the user lifts it. " +
		"Run the lock again, which saves it. The strazad log names the cause under request id %s."
)

// handleUserLock is the Straza-lane lock: the kill-switch
// cascade with origin admin|external, WITHOUT touching users.status: the
// IdM keeps mastering its own lane, and its enable/reconciliation cannot
// lift this row. wat_ admin API tokens ride the normal admin middleware, so a
// SOAR/SIEM automation (e.g. a Kibana alert action) can call this directly.
// It answers 500 when the lock's row or its event was not written, and this
// server's denylist keeps the user blocked all the same.
func (a *App) handleUserLock(w http.ResponseWriter, r *http.Request) {
	u, err := a.store.Users().GetByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "no such user")
		return
	}
	if err == nil && u.Username == BreakGlassUsername {
		apiError(w, http.StatusForbidden, "the break-glass admin cannot be locked (lockout guarantee)")
		return
	}
	if err != nil {
		// A store blip on the kill-switch path must not claim the user does
		// not exist.
		a.fail(w, r, http.StatusInternalServerError, "get failed", err)
		return
	}
	var req struct {
		Reason string `json:"reason"`
		Origin string `json:"origin"`
	}
	err = decodeAdmin(r.Body, &req)
	if refusedUnknownField(w, err) {
		return
	}
	if err != nil || strings.TrimSpace(req.Reason) == "" {
		apiError(w, http.StatusBadRequest, "reason is required")
		return
	}
	if req.Origin == "" {
		req.Origin = store.RevocationOriginAdmin
	}
	if req.Origin != store.RevocationOriginAdmin && req.Origin != store.RevocationOriginExternal {
		apiError(w, http.StatusBadRequest, "origin must be admin or external")
		return
	}
	rowErr, eventErr := a.revokeUserCtx(r.Context(), u.ID, req.Reason, req.Origin)
	a.emitEvent(r, "straza.audit.admin", map[string]any{
		"action": "user.lock", "target": u.ID, "reason": req.Reason, "origin": req.Origin,
	})
	// The event reaches every server: a running one through the stream, and
	// a restarted one through the stream's replay. The row alone reaches the
	// others only when they restart and rebuild their denylists from it. A
	// lock without its row blocks everywhere, but the console and the SCIM
	// lock block do not show it and an identity manager's disable and enable
	// lift it (spec/scim-profile), so any failed write answers 500.
	switch {
	case eventErr != nil && rowErr != nil:
		a.fail(w, r, http.StatusInternalServerError,
			fmt.Sprintf(userLockHereOnlyMsg, u.Username, reqID(r.Context())), errors.Join(rowErr, eventErr))
		return
	case eventErr != nil:
		a.fail(w, r, http.StatusInternalServerError,
			fmt.Sprintf(userLockUnsentMsg, u.Username, u.Username, reqID(r.Context())), eventErr)
		return
	case rowErr != nil:
		a.fail(w, r, http.StatusInternalServerError,
			fmt.Sprintf(userLockUnsavedMsg, u.Username, reqID(r.Context())), rowErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": u.ID, "locked": true, "origin": req.Origin, "reason": req.Reason,
	})
}
