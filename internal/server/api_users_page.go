package server

import (
	"net/http"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// The sort keys the users and sessions lists offer, with the direction each
// opens on when no order is given: the id and time keys on the newest, the
// text keys on A.
var (
	userSortKeys    = sortKeys{"": true, "created": true, "last_seen": true, "name": false, "status": false}
	sessionSortKeys = sortKeys{"": true, "started": true, "last_seen": true, "user": false, "status": false, "attestation": false}
)

// stamp is the cursor spelling of a time; the zero time reads as never.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// userSortValue is the cursor value of a user row under key.
func userSortValue(key string, u store.User, lastSeen map[string]time.Time) string {
	switch key {
	case "name":
		return u.Username
	case "status":
		return u.Status
	case "last_seen":
		return stamp(lastSeen[u.ID])
	}
	return ""
}

// sessionSortValue is the cursor value of a session row under key.
func sessionSortValue(key string, s store.Session, names map[string]string) string {
	switch key {
	case "last_seen":
		return stamp(s.LastSeen)
	case "status":
		return s.Status
	case "attestation":
		return s.AttestationLevel
	case "user":
		return names[s.UserID]
	}
	return ""
}

// writeUsersPage answers the paged users lane: the rows enriched
// with effective roles from the same resolver every admin request runs,
// the lock rows the kill-switch lanes write, the last seen stamp and the
// sponsored count, each from one batched read, then the cursor for the row
// the page ends on. A store fault must not render a locked user as unlocked
// or a sponsor as sponsoring nobody, so these lanes fail the read rather
// than degrade to absent. The legacy bare-array lane stays byte-identical.
func (a *App) writeUsersPage(w http.ResponseWriter, r *http.Request, p pageParams, users []store.User, after *store.User) {
	ids := make([]string, len(users))
	names := make([]string, len(users))
	for i, u := range users {
		ids[i], names[i] = u.ID, u.Username
	}
	lastSeen, err := a.store.Sessions().LastSeenByUsers(r.Context(), ids)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "last-seen lookup failed", err)
		return
	}
	locks, err := a.store.Revocations().ListByTargets(r.Context(), store.RevokeUser, ids)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "lock lookup failed", err)
		return
	}
	sponsored, err := a.store.Users().SponsoredCounts(r.Context(), names)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "sponsored-count lookup failed", err)
		return
	}
	out := make([]pagedUserPayload, len(users))
	for i, u := range users {
		out[i] = pagedUserPayload{userPayload: toUserPayload(u), EffectiveRoles: []string{}, Locks: []lockPayload{}, SponsoredCount: sponsored[u.Username]}
		for _, rv := range locks[u.ID] {
			out[i].Locks = append(out[i].Locks, lockPayload{Origin: rv.Origin, Reason: rv.Reason, CreatedAt: rv.CreatedAt})
		}
		roles, err := a.resolver.ResolveRoles(r.Context(), u.ID, time.Now())
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "role resolution failed", err)
			return
		}
		for _, ro := range roles {
			out[i].EffectiveRoles = append(out[i].EffectiveRoles, ro.Name)
		}
		if ls, ok := lastSeen[u.ID]; ok {
			t := ls
			out[i].LastSeen = &t
		}
	}
	cursor := ""
	if after != nil {
		cursor = pageCursor(p, userSortValue(p.sort.Key, *after, lastSeen), after.ID)
	}
	writePageCursor(w, out, cursor)
}
