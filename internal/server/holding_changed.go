package server

import "context"

// holdingChanged drops the cached subject of each session of the user
// userID, after the roles the user holds changed, so the next call of each
// session on this server checks in again and resolves its roles anew. An id
// that names no user, such as a role's, has no sessions and drops nothing.
// When the sessions cannot be listed nothing is dropped, and each session
// keeps its roles until its token ends, at most 300 seconds, because
// dropping every subject while the store fails would send every session of
// this server to a check-in the store cannot answer.
func (a *App) holdingChanged(ctx context.Context, userID string) {
	if userID == "" {
		return
	}
	sessions, err := a.store.Sessions().ListByUser(ctx, userID)
	if err != nil {
		a.reqlog(ctx).Warn("the roles of a user changed and the user's sessions could not be listed, so each session keeps its roles until its token ends, at most 300 seconds",
			"user", userID, "err", err)
		return
	}
	for _, s := range sessions {
		a.subjects.drop(s.ID)
	}
}

// identityHeard applies an identity change another server made: role
// resolutions are read again, and the sessions of the user the event names
// check in again at their next call.
func (a *App) identityHeard(ctx context.Context, id string) error {
	a.resolver.Bump()
	a.holdingChanged(ctx, id)
	return nil
}
