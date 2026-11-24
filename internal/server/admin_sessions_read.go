package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// sessionSearchMax is the most bytes the sessions list's search takes.
const sessionSearchMax = 128

// The answers of the sessions list's search and of the read of one session
// that a person reads. The search ones name the length and the bound, the
// read ones the id, and the 500 the request id as well.
const (
	sessionSearchBareMsg = "q was not applied: the search works on the paged list only, and this request asked for the plain list. " +
		"Add limit= to the request, for example limit=100, and send it again."
	sessionSearchLongMsg = "q was not applied: it is %d bytes long, and a search takes at most %d. " +
		"Send a shorter part of the sign-in name or of the session id."
	noSessionMsg = "no session has the id %s on this server: the id may be mistyped or come from another Straza server. " +
		"Copy it from the Sessions page or from strazactl sessions list and send the request again."
	sessionNotReadMsg = "session %s could not be read: Straza's database did not answer the lookup, so nothing is known about the session. " +
		"Send the request again. The strazad log names the cause under request id %s."
	sessionRevokeIsPostMsg = "GET /v1/admin/sessions/revoke reads no session: revoke is the bulk stand-down, which takes POST. " +
		"To read one session, send GET /v1/admin/sessions/ followed by the session id."
)

// sessionSearch reads the sessions list's q without its surrounding
// spaces. A search needs the paged lane and at most sessionSearchMax bytes.
// On a refusal the 400 is written here and ok is false.
func sessionSearch(w http.ResponseWriter, r *http.Request, paged bool) (q string, ok bool) {
	q = strings.TrimSpace(r.URL.Query().Get("q"))
	switch {
	case q == "":
		return "", true
	case !paged:
		apiError(w, http.StatusBadRequest, sessionSearchBareMsg)
		return "", false
	case len(q) > sessionSearchMax:
		apiError(w, http.StatusBadRequest, fmt.Sprintf(sessionSearchLongMsg, len(q), sessionSearchMax))
		return "", false
	}
	return q, true
}

// handleSessionGet serves GET /v1/admin/sessions/{id}: one session in the
// row shape of the list. An id no session has is a 404. The segment revoke
// belongs to the bulk revoke, which takes POST, so a GET of it answers 405
// and never reads the store.
func (a *App) handleSessionGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "revoke" {
		w.Header().Set("Allow", http.MethodPost)
		apiError(w, http.StatusMethodNotAllowed, sessionRevokeIsPostMsg)
		return
	}
	s, err := a.store.Sessions().GetByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, fmt.Sprintf(noSessionMsg, id))
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, fmt.Sprintf(sessionNotReadMsg, id, reqID(r.Context())), err)
		return
	}
	out, _ := a.sessionRows(r.Context(), []store.Session{s})
	writeJSON(w, http.StatusOK, out[0])
}

// sessionRows answers the wire rows of sessions in their order, and the
// owners' usernames by user id, which the sort by user puts in its cursor.
// One users read gives each row its owner's username, kind and sponsor, one
// approvals read its waiting count and one registry read its wiring status.
// Each read is reporting garnish: one that fails leaves its fields out and
// never fails the fleet screen, which is the kill switch's own view.
func (a *App) sessionRows(ctx context.Context, sessions []store.Session) ([]sessionPayload, map[string]string) {
	userIDs := make([]string, 0, len(sessions))
	sessionIDs := make([]string, len(sessions))
	seen := map[string]bool{}
	for i, s := range sessions {
		sessionIDs[i] = s.ID
		if !seen[s.UserID] {
			seen[s.UserID] = true
			userIDs = append(userIDs, s.UserID)
		}
	}
	// Resolve owners in one batch; operators think in usernames, not UUIDs.
	owners := map[string]store.User{}
	names := map[string]string{}
	if users, err := a.store.Users().GetByIDs(ctx, userIDs); err == nil {
		for _, u := range users {
			owners[u.ID], names[u.ID] = u, u.Username
		}
	}
	waiting, err := a.store.Approvals().WaitingBySession(ctx, sessionIDs, time.Now())
	if err != nil {
		a.log.Warn("sessions list: approvals unavailable; waiting counts omitted", "err", err)
	}
	registry, regErr := a.store.AttestationHashes().List(ctx)
	if regErr != nil {
		a.log.Warn("sessions list: attestation registry unavailable; wiring status omitted", "err", regErr)
	}
	out := make([]sessionPayload, len(sessions))
	for i, s := range sessions {
		out[i] = sessionPayload{ID: s.ID, UserID: s.UserID, Waiting: waiting[s.ID],
			Harness: joinHarness(s.HarnessName, s.HarnessVersion), ClientVersion: s.ClientVersion, ClientCommit: s.ClientCommit, Host: s.Host, Folder: s.Folder, Attestation: s.AttestationLevel,
			Status: s.Status, StartedAt: s.StartedAt, LastSeen: s.LastSeen}
		if u, ok := owners[s.UserID]; ok {
			out[i].Username, out[i].UserKind, out[i].Sponsor = u.Username, u.Kind(), u.Sponsor
		}
		if regErr == nil {
			out[i].WiringStatus, out[i].WiringHash = a.harnessWiringStatus(s.HarnessName, s.AttestationHashes, registry)
		}
	}
	return out, names
}
