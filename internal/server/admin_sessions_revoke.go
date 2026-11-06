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

// revokeDevice deletes the device row of d with its revocation row of
// reason and its straza.revocation.device event in one transaction, which
// also ends the device's active sessions, then runs the rest of the
// kill-switch cascade handleDeviceRevoke describes. Each ended session is
// refused locally, as a revoked session is, and gets one session.end record
// with outcome and reason. It is shared by the admin route and the
// key-reuse refusal of a key check-in. It answers store.ErrNotFound, and
// does nothing else, when the row is already gone. On any other error
// nothing durable changed and no push goes out, but this server's denylist
// refuses the device at once. The write runs on a context the client's
// leaving does not cancel, bounded by adminWriteBound, so a copy's holder
// who hangs up cannot cancel the revoke its own check-in set off.
func (a *App) revokeDevice(r *http.Request, d store.Device, reason, outcome string) error {
	// Payload carries ONLY the device id: the spine consumer keys the denylist
	// scope off which field is set, so a user id riding along would lock the
	// whole user (spine/revocation.go apply).
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), adminWriteBound)
	defer cancel()
	ev, err := a.cloudEvent(ctx, "straza.revocation.device", map[string]any{"device": d.ID})
	var ended []store.ClosedSession
	if err == nil {
		ended, err = a.revokeDeviceRow(ctx, d, reason, ev)
	}
	if errors.Is(err, store.ErrNotFound) {
		return err
	}
	a.denylist.RevokeDevice(d.ID)
	if err != nil {
		return err
	}
	a.pushRevocation("device", d.ID)
	for _, c := range ended {
		a.refuseLocally([]string{c.ID})
		a.emitAuthnSessionEnd(ctx, r, outcome, authnFields{UserID: c.UserID, Session: c.ID, Reason: reason})
	}
	return nil
}

// revokeDeviceRow is Devices().Revoke with a write that ended without a
// clear answer settled the way writeLanded settles a user write: when the
// device row is gone, the revoke committed and the rest of it runs as after
// any commit. The rows the revoke ends are revoked once it commits, so the
// device's sessions are read first, and the ones active then are the ones
// it ended. A session that started between the read and the commit gets no
// record. The revoke is sent even when that read fails, because it is a
// kill switch, and when such a revoke's answer is lost the missing
// session.end records leave one Error line and one count of
// straza_outbox_records_lost_total.
func (a *App) revokeDeviceRow(ctx context.Context, d store.Device, reason string, ev store.OutboxEvent) ([]store.ClosedSession, error) {
	held, heldErr := a.store.Sessions().ListByUser(ctx, d.UserID)
	ended, err := a.store.Devices().Revoke(ctx, d.ID, reason, ev)
	if errors.Is(err, store.ErrNotFound) || !store.MaybeWritten(err) {
		return ended, err
	}
	cctx, cancel := afterCommit(ctx)
	defer cancel()
	if _, readErr := a.store.Devices().GetByID(cctx, d.ID); !errors.Is(readErr, store.ErrNotFound) {
		return nil, err
	}
	a.reqlog(ctx).Warn("device revoke: the database committed the revoke but its answer was lost, so the rest of the revoke runs as after any commit",
		"device", d.ID, "err", err)
	if heldErr != nil {
		a.recordLost()
		a.reqlog(ctx).Error("device revoke: the revoke committed but the device's sessions could not be listed, so the sessions it ended have no session.end record",
			"device", d.ID, "user", d.UserID, "err", heldErr)
	}
	ended = nil
	for _, s := range held {
		if s.DeviceID == d.ID && s.Status == store.SessionActive {
			ended = append(ended, store.ClosedSession{ID: s.ID, UserID: s.UserID})
		}
	}
	return ended, nil
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
