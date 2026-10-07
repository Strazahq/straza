package server

import (
	"context"
	"errors"

	"github.com/strazahq/straza/internal/store"
)

// deprovisionApproverDevices deletes every approver device of a deleted
// user, emits one straza.identity.updated per device, the event the admin
// device revoke writes, with the reason, and chains one straza.audit.admin
// record approver.revoke per device that names the device and the user. A
// failed list or delete is logged and skipped, never returned: the user row
// is already soft-deleted, so requireApprover refuses the user before the
// device matters. A device already gone was revoked by its own lane and is
// skipped. The caller passes a context that a client disconnect cannot
// cancel.
func (a *App) deprovisionApproverDevices(ctx context.Context, userID, username, reason string) {
	if userID == "" {
		return // ListDevices reads an empty id as every user's devices
	}
	devs, err := a.store.Approvers().ListDevices(ctx, userID)
	if err != nil {
		a.log.Error("deprovision: approver device list failed", "user", userID, "reason", reason, "err", err)
		return
	}
	for _, d := range devs {
		err := a.store.Approvers().DeleteDevice(ctx, d.ID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			a.log.Error("deprovision: approver device delete failed", "user", userID, "reason", reason, "device", d.ID, "err", err)
			continue
		}
		a.emitEventCtx(ctx, "straza.identity.updated", map[string]any{
			"action": "approver-device-revoked", "device": d.ID, "user": userID,
			"via": "admin", "reason": reason,
		})
		a.emitIdentityAudit(ctx, map[string]any{
			"action": actionApproverRevoke, "target": d.ID, "device": d.ID,
			"user": userID, "username": username, "reason": reason,
		})
	}
}

// actionApproverRevoke is the straza.audit.admin action of an approver
// device that a user delete removed.
const actionApproverRevoke = "approver.revoke"
