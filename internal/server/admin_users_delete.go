package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/strazahq/straza/internal/store"
)

// actionUserDelete is the straza.audit.admin action of a user that an admin
// deleted.
const actionUserDelete = "user.delete"

// entraOwnedUserMsg is the 409 answer to a change of the status or a name
// of a person Microsoft Entra owns, and to the delete of one, formatted
// with the username.
const entraOwnedUserMsg = "%[1]s was not changed here, because the account comes from Microsoft Entra ID and Straza would write Entra's state back at its next read. " +
	"Change the account in Entra. To stop %[1]s at once, lock the account."

// entraOwnedAgentMsg is the 409 answer to a change of the status, a name,
// the sponsor or the Entra agent id of an agent Microsoft Entra owns, and
// to the delete of one, formatted with the username.
const entraOwnedAgentMsg = "%[1]s was not changed here, because the agent comes from Microsoft Entra ID and Straza would write Entra's state back at its next read. " +
	"Change the agent, or its sponsor, in Entra. To stop %[1]s at once, lock the account."

// entraOwnedMsg answers the 409 sentence of a change or a delete of u that
// the admin API leaves to Microsoft Entra, or "" when Entra does not own u.
// Entra owns a person or an agent of origin entra while identity.entra.read
// lists its kind, and never a service.
func (a *App) entraOwnedMsg(u store.User) string {
	kind := u.Kind()
	switch {
	case u.Origin != store.OriginEntra || !a.entraOwns(kind):
		return ""
	case kind == store.UserKindAgent:
		return fmt.Sprintf(entraOwnedAgentMsg, u.Username)
	}
	return fmt.Sprintf(entraOwnedUserMsg, u.Username)
}

// entraWrites reports whether the write of u over before changes a field
// the Entra sync writes: the status, the display name or the email, and of
// an agent also the sponsor or the Entra agent id.
func entraWrites(before, u store.User) bool {
	if u.Status != before.Status || u.Display != before.Display || u.Email != before.Email {
		return true
	}
	return before.Kind() == store.UserKindAgent && (u.Sponsor != before.Sponsor || u.EntraAgentID != before.EntraAgentID)
}

// handleUsersDelete serves DELETE /v1/admin/users/{id}. A delete that
// removes the user chains one user.delete record, before the records of the
// grants, sessions, key and approver devices that end with the user, and a
// 404, a write the database refused or the 409 for a person or an agent
// Microsoft Entra owns chains none.
func (a *App) handleUsersDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, err := a.store.Users().GetByID(r.Context(), id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.fail(w, r, http.StatusInternalServerError, "lookup failed", err)
		return
	}
	if u.Username == BreakGlassUsername {
		apiError(w, http.StatusForbidden, "the break-glass admin cannot be deleted (lockout guarantee); rotate its credential on-box instead")
		return
	}
	// The Entra sync owns this person or agent and would write the account
	// back at its next read, so the account is deleted in Entra.
	if msg := a.entraOwnedMsg(u); msg != "" {
		apiError(w, http.StatusConflict, msg)
		return
	}
	// The delete commits with the user's revocation row and event, so a
	// refused write leaves the user as it was and the same request runs again.
	revoke, err := a.userRevocation(r.Context(), id, "user deleted by admin", store.RevocationOriginAdmin)
	var removed []store.RoleAssignment
	if err == nil {
		removed, err = a.softDeleteUser(r.Context(), id, revoke)
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusNotFound, "no such user")
			return
		}
		a.failUserRevoke(w, r, id, cmp.Or(u.Username, id), "delete", userNotDeletedMsg, err)
		return
	}
	ctx := context.WithoutCancel(r.Context()) // a retried delete answers 404, so a disconnect must not stop this
	a.emitIdentityAudit(ctx, map[string]any{
		"action": actionUserDelete, "target": id, "user": id, "username": u.Username, "reason": "user deleted by admin",
	})
	// The grants ended with the row: one record per ended grant names who
	// deleted the user, so a certifier never finds a grant that stopped
	// without a record.
	for _, as := range removed {
		a.auditAssignment(ctx, actionRolesUnassign, as, "user deleted by admin")
	}
	a.userRevokedCtx(ctx, id, "user deleted by admin", store.RevocationOriginAdmin)
	// The credentials end with the row too, so nothing the deleted user
	// could authenticate with outlives the delete.
	a.deprovisionGrants(ctx, id, "user deleted by admin")
	a.deprovisionNHIKey(ctx, id, "user deleted by admin")
	a.deprovisionApproverDevices(ctx, id, u.Username, "user deleted by admin")
	a.identityChangedCtx(ctx, "straza.identity.deactivated", id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// deprovisionNHIKey removes the assertion key of a deleted user and writes
// one straza.audit.admin record when a key was registered. Most users hold
// no key and leave no record. A failed delete is logged and skipped, never
// returned: the row is already soft-deleted, so the issuer's lookup refuses
// the user before it reads the key.
func (a *App) deprovisionNHIKey(ctx context.Context, userID, reason string) {
	err := a.store.Settings().Delete(ctx, nhiKeyPrefix+userID)
	if errors.Is(err, store.ErrNotFound) {
		return
	}
	if err != nil {
		a.log.Error("deprovision: nhi key delete failed", "user", userID, "reason", reason, "err", err)
		return
	}
	a.emitEventCtx(ctx, "straza.audit.admin", map[string]any{
		"action": actionNHIKeyRemoved, "target": userID, "user": userID, "reason": reason,
	})
}
