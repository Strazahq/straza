package server

// The kill switch of a user: the revocation row and straza.revocation.user
// event, this server's denylist, the user's sessions and the user.killed
// record.

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/store"
)

// The answers of a disable or a delete whose transaction the database did
// not take: the user's status write, its revocation row and the
// straza.revocation.user event commit together or not at all. The two 500
// sentences name the user, the user again and the request id. The unsure
// one names the change, the user, the change twice more, the user and the
// request id. The 400 sentence names the user.
const (
	userNotDisabledMsg = "user %s was not disabled: Straza could not write the change to its database, so nothing changed and the user stays active. " +
		"This server already refuses the user, but the other servers do not. Run the disable again. " +
		"Without a saved disable, this server's refusal lasts until it restarts, or until strazactl users unlock %s lifts it. " +
		"The strazad log names the cause under request id %s."
	userNotDeletedMsg = "user %s was not deleted: Straza could not write the delete to its database, so nothing changed. " +
		"This server already refuses the user, and the other servers still treat the user as before. Run the delete again. " +
		"Without a saved delete, this server's refusal lasts until it restarts, or until strazactl users unlock %s lifts it. " +
		"The strazad log names the cause under request id %s."
	userChangeUnsureMsg = "Straza could not tell whether its database saved the %s of user %s, because the write ended without a clear answer. " +
		"This server already refuses the user. Read the user back, and if it is unchanged, run the %s again. " +
		"Without a saved %s, this server's refusal lasts until it restarts, or until strazactl users unlock %s lifts it. " +
		"The strazad log names the cause under request id %s."
	userValueRefusedMsg = "user %s was not changed: the database refused a value in the request, so nothing in it was saved and the user's status did not change. " +
		"Send the request again without that value, such as text that holds a NUL character."
)

// failUserRevoke answers 500 for a disable or a delete of the user id, named
// name, that the database did not take, and puts the user on this server's
// denylist first, because the operator asked for the user to be refused and
// this server can refuse it without the database. notDoneMsg says nothing
// changed. A write that may still have committed gets the unsure sentence
// instead, which names change, unless its error is marked with
// errUserWriteNotSaved.
func (a *App) failUserRevoke(w http.ResponseWriter, r *http.Request, id, name, change, notDoneMsg string, err error) {
	a.denylist.revokeUser(id)
	msg := fmt.Sprintf(notDoneMsg, name, name, reqID(r.Context()))
	if store.MaybeWritten(err) && !errors.Is(err, errUserWriteNotSaved) {
		msg = fmt.Sprintf(userChangeUnsureMsg, change, name, change, change, name, reqID(r.Context()))
	}
	a.fail(w, r, http.StatusInternalServerError, msg, err)
}

// revokeUserCtx runs the kill switch for a user whose revocation row and
// straza.revocation.user event are written apart from any other change, as
// the lock writes them. origin records
// which authority created the row: the
// SCIM reactivation lift removes scim-origin rows only, so admin/external
// locks survive IdM writes. It answers the error of the revocation row write
// and of the straza.revocation.user event write, the two writes that carry
// the block to the other servers, and only logs every other failed write.
// This server's denylist blocks the user whatever the two answer. Its Error
// lines go to the request's logger, so they carry the correlation id when
// ctx comes from a request.
func (a *App) revokeUserCtx(ctx context.Context, userID, reason, origin string) (rowErr, eventErr error) {
	log := a.reqlog(ctx)
	// The deactivation this cascade enforces has committed, so each of its
	// writes runs on afterCommit's context: a client that left or a write
	// that ran past its bound must not stop the kill switch halfway.
	wctx, cancel := afterCommit(ctx)
	_, rowErr = a.store.Revocations().Create(wctx, store.Revocation{
		Kind: store.RevokeUser, TargetID: userID, Reason: reason, Origin: origin,
	})
	cancel()
	a.denylist.revokeUser(userID)
	a.pushRevocation("user", userID)
	revokedSessions := a.revokeUserSessions(ctx, userID)
	eventErr = a.emitRevokeRecord(ctx, "straza.revocation.user",
		map[string]any{"user": userID, "reason": reason, "origin": origin})
	// The event reaches every replica now and at a restart, and the row
	// reaches them only at a restart, so one line names how far the block
	// reached once both writes are known.
	switch {
	case rowErr != nil && eventErr != nil:
		log.Error("user revoke: neither the revocation record nor the revocation event could be written, so only this replica blocks the user, and only until it restarts",
			"user", userID, "err", errors.Join(rowErr, eventErr))
	case rowErr != nil:
		log.Error("user revoke: the revocation record could not be written, so the event alone blocks the user on every replica, and with no record the console shows no lock and an identity manager that enables the user again lifts the block",
			"user", userID, "err", rowErr)
	case eventErr != nil:
		log.Error("user revoke: the revocation event could not be written, so the other replicas miss the block until they restart",
			"user", userID, "err", eventErr)
	}
	a.recordUserKilled(ctx, userID, reason, origin, revokedSessions)
	return rowErr, eventErr
}

// userRevocation is the revocation row and the straza.revocation.user event
// a user write carries in its own transaction, the same row and event
// revokeUserCtx writes apart. The event's outbox id is chosen here, so
// writeLanded can ask whether this write's transaction committed.
func (a *App) userRevocation(ctx context.Context, userID, reason, origin string) (*store.UserRevocation, error) {
	ev, err := a.cloudEvent(ctx, "straza.revocation.user", map[string]any{"user": userID, "reason": reason, "origin": origin})
	if err != nil {
		return nil, err
	}
	ev.ID = uuid.Must(uuid.NewV7()).String()
	return &store.UserRevocation{Reason: reason, Origin: origin, Event: ev}, nil
}

// errUserWriteNotSaved marks the error of a user write that is known to
// have saved nothing, so the answer says so: one whose answer was lost and
// that writeLanded showed did not commit, and a delete that was not sent.
var errUserWriteNotSaved = errors.New("nothing was saved")

// lostAnswer reports whether err, the answer of a user write that carried a
// revocation, leaves open whether the write committed. A missing user is a
// clear answer, because that write changed no row.
func lostAnswer(err error) bool {
	return !errors.Is(err, store.ErrNotFound) && store.MaybeWritten(err)
}

// writeLanded asks the store whether the write of the user userID that
// carried revoke committed, after the write answered err, for which
// lostAnswer holds, and answers the user's row as stored when it did. It
// asks once, on afterCommit's context, because the request's context may be
// what ended the write. A write that did not commit answers err marked with
// errUserWriteNotSaved. A store that cannot tell answers err with its
// reason, which keeps the unsure answer.
func (a *App) writeLanded(ctx context.Context, userID string, revoke *store.UserRevocation, err error) (store.User, error) {
	cctx, cancel := afterCommit(ctx)
	defer cancel()
	u, landed, cerr := a.store.Users().RevocationCommitted(cctx, userID, revoke.Event.ID)
	switch {
	case cerr != nil:
		// %v keeps cerr out of the chain store.MaybeWritten reads, so the
		// answer stays unsure whatever cerr is.
		return store.User{}, fmt.Errorf("%w, and the database could not tell whether the write committed: %v", err, cerr)
	case !landed:
		return store.User{}, fmt.Errorf("%w, because the write did not commit and its revocation event is not in the database: %w", errUserWriteNotSaved, err)
	}
	a.reqlog(ctx).Warn("user write: the database committed the write but its answer was lost, so the rest of the change runs as after any commit",
		"user", userID, "err", err)
	return u, nil
}

// updateUserFields is UpdateFields with the answer of a write that carries
// revoke settled by writeLanded, so a write that committed answers the
// user's row and its caller runs the rest of the change as after a clean
// commit.
func (a *App) updateUserFields(ctx context.Context, id string, f store.UserFields, revoke *store.UserRevocation) (store.User, error) {
	u, err := a.store.Users().UpdateFields(ctx, id, f, revoke)
	if revoke == nil || !lostAnswer(err) {
		return u, err
	}
	return a.writeLanded(ctx, id, revoke, err)
}

// softDeleteUser is SoftDelete with revoke, settled as updateUserFields
// settles a disable. The assignment rows a delete removes are gone once it
// commits, so it first reads the user's assignments, and a delete that
// committed answers that list for one unassign record each. A grant added
// between that read and the commit ends with the user and gets no record
// from the delete, and one removed meanwhile gets a second unassign record
// beside its own. When that read fails, the delete is not sent and the
// error is marked with errUserWriteNotSaved, so no delete commits without
// the list its records need.
func (a *App) softDeleteUser(ctx context.Context, id string, revoke *store.UserRevocation) ([]store.RoleAssignment, error) {
	held, err := a.store.Roles().ListAssignments(ctx, store.SubjectUser, id)
	if err != nil {
		return nil, fmt.Errorf("%w, because the user's role assignments could not be read, so the delete was not sent: %w", errUserWriteNotSaved, err)
	}
	removed, err := a.store.Users().SoftDelete(ctx, id, revoke)
	if !lostAnswer(err) {
		return removed, err
	}
	if _, err := a.writeLanded(ctx, id, revoke, err); err != nil {
		return nil, err
	}
	return held, nil
}

// userRevokedCtx runs the rest of the kill switch for a user whose status
// write committed with the userRevocation row and event: this server's
// denylist, the push to connected clients, the user's sessions and the
// user.killed record. Its writes run on afterCommit's context.
func (a *App) userRevokedCtx(ctx context.Context, userID, reason, origin string) {
	a.denylist.revokeUser(userID)
	a.pushRevocation("user", userID)
	a.recordUserKilled(ctx, userID, reason, origin, a.revokeUserSessions(ctx, userID))
}

// revokeUserSessions marks the user's active sessions revoked, puts each on
// this server's denylist and pushes its revocation, and answers how many
// rows it marked. A session whose write failed stays active in the store,
// and the user's denylist entry refuses it on the refresh and on the admin
// plane.
func (a *App) revokeUserSessions(ctx context.Context, userID string) int {
	log := a.reqlog(ctx)
	revoked := 0
	wctx, cancel := afterCommit(ctx)
	sessions, err := a.store.Sessions().ListByUser(wctx, userID)
	cancel()
	if err != nil {
		log.Error("user revoke: the user's sessions could not be listed, so none was marked revoked and the denylist refuses them",
			"user", userID, "err", err)
	}
	for _, s := range sessions {
		if s.Status != store.SessionActive {
			continue
		}
		wctx, cancel := afterCommit(ctx)
		err := a.store.Sessions().SetStatus(wctx, s.ID, store.SessionRevoked)
		cancel()
		if err != nil {
			log.Error("user revoke: the session could not be marked revoked, so it stays active in the store and the denylist refuses it",
				"user", userID, "session", s.ID, "err", err)
		} else {
			revoked++
		}
		a.denylist.revokeSession(s.ID)
		a.subjects.drop(s.ID)
		a.pushRevocation("session", s.ID)
	}
	return revoked
}

// recordUserKilled chains the user.killed record of a user revoke, with
// sessions the count of session rows it marked revoked, and logs a write
// that failed.
func (a *App) recordUserKilled(ctx context.Context, userID, reason, origin string, sessions int) {
	// The kill switch must leave a chain entry: straza.revocation.* converges
	// enforcement but only straza.audit.> is hash-chained, so without this
	// record the kill would leave no audit trace.
	if err := a.emitRevokeRecord(ctx, "straza.audit.identity", map[string]any{
		"action": "user.killed", "user": userID, "reason": reason, "origin": origin,
		"sessionsRevoked": sessions,
	}); err != nil {
		a.reqlog(ctx).Error("user revoke: the user.killed record could not be written, so the audit chain holds no record of this revoke",
			"user", userID, "err", err)
	}
}

// emitRevokeRecord writes one record of a user revoke to the outbox on
// afterCommit's context with insertRecord's one retry, as emitEventCtx
// writes a record. A record still not written is counted in
// straza_outbox_records_lost_total, and its error is answered for the
// caller to log, because a lost record leaves the other replicas or the
// audit chain without the revoke.
func (a *App) emitRevokeRecord(ctx context.Context, subject string, data map[string]any) error {
	ctx, cancel := afterCommit(ctx)
	defer cancel()
	ev, err := a.cloudEvent(ctx, subject, data)
	if err == nil {
		err = a.insertRecord(ctx, ev)
	}
	if err != nil {
		a.recordLost()
	}
	return err
}
