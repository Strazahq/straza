package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/strazahq/straza/internal/store"
)

// The 500 sentences of a revoke whose transaction the database refused.
// Each names the target, the session id again where it names the command,
// and the request id. The bulk one starts with the count that did end.
const (
	deviceNotRevokedMsg = "device %s was not revoked: Straza could not write the revocation to its database, so nothing changed and the device stays enrolled. " +
		"This server already refuses it, but the other servers do not. Run the revoke again. The strazad log names the cause under request id %s."
	sessionNotRevokedMsg = "session %s was not revoked: Straza could not write the revocation to its database, so nothing changed and the session stays active. " +
		"This server already refuses it, but the other servers do not. Run strazactl sessions revoke %s again. The strazad log names the cause under request id %s."
	sessionNotEndedMsg = "session %s was not ended: Straza could not write the revocation to its database, so the session stays active. " +
		"Ask an administrator to run strazactl sessions revoke %s. The strazad log names the cause under request id %s."
	sessionsNotEndedMsg = "Straza ended %s and could not end the rest of the set, because its database refused the write, so those stay active. " +
		"Run the same stand-down again: it ends only the sessions still active. The strazad log names the cause under request id %s."
	sessionUncheckedMsg = "session %s could not be checked: Straza ended nothing, because the session was already ended or does not exist, " +
		"and its database did not answer the lookup that tells which. Run strazactl sessions revoke %s again. The strazad log names the cause under request id %s."
)

// revokeDevice deletes the device row with its revocation row of reason and
// its straza.revocation.device event in one transaction, then runs the rest
// of the kill-switch cascade handleDeviceRevoke describes. It is shared by
// the admin route and the key-reuse refusal of a key check-in. It answers
// store.ErrNotFound, and does nothing else, when the row is already gone.
// On any other error nothing durable changed and no push goes out, but this
// server's denylist refuses the device at once. The write runs on a context
// the client's leaving does not cancel, bounded by adminWriteBound, because
// the check-in route that refuses a reused key is not bounded like an admin
// write, and a copy's holder who hangs up must not cancel the revoke its
// own check-in set off.
func (a *App) revokeDevice(r *http.Request, deviceID, reason string) error {
	// Payload carries ONLY the device id: the spine consumer keys the denylist
	// scope off which field is set, so a user id riding along would lock the
	// whole user (spine/revocation.go apply).
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), adminWriteBound)
	defer cancel()
	ev, err := a.cloudEvent(ctx, "straza.revocation.device", map[string]any{"device": deviceID})
	if err == nil {
		err = a.store.Devices().Revoke(ctx, deviceID, reason, ev)
	}
	if errors.Is(err, store.ErrNotFound) {
		return err
	}
	a.denylist.RevokeDevice(deviceID)
	if err != nil {
		return err
	}
	a.pushRevocation("device", deviceID)
	return nil
}

// revokeSession ends session id with reason in the store's revoke
// transaction, with one straza.revocation.session event, and reports
// whether its row changed. On an error nothing durable changed and no push
// goes out, but this server's denylist refuses the session at once. The
// write runs on a context the client's leaving does not cancel, bounded by
// adminWriteBound, so a logout whose client hangs up still lands whole, its
// session.end included.
func (a *App) revokeSession(ctx context.Context, id, reason string) (bool, error) {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), adminWriteBound)
	defer cancel()
	ended, err := a.store.Sessions().Revoke(wctx, []string{id}, reason, func([]string) (store.OutboxEvent, error) {
		return a.cloudEvent(wctx, "straza.revocation.session", map[string]any{"session": id})
	})
	if err == nil && len(ended) == 0 {
		return false, nil
	}
	a.refuseLocally([]string{id})
	if err != nil {
		return false, err
	}
	a.pushRevocation("session", id)
	return true, nil
}

// refuseLocally puts sessions on this server's denylist and drops them from
// its subject cache. It runs for a session a revoke just ended, and for
// sessions the store did not end, which then fail closed on this server.
func (a *App) refuseLocally(ids []string) {
	for _, id := range ids {
		a.denylist.revokeSession(id)
		a.subjects.drop(id)
	}
}
