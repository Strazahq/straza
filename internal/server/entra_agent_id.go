package server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/strazahq/straza/internal/store"
)

// The admin API answers of an entra_agent_id write Straza refuses.
const (
	entraNotGUIDMsg = "%q is not an Entra agent identity id, which is a GUID such as 5f8c2a1e-3b4d-4e6f-9a7b-1c2d3e4f5a6b. " +
		"Copy the client id of the agent identity from the Entra admin centre and set it again."
	entraNotAgentMsg = "%s is %s, and only a user of type agent can hold an Entra agent identity, because Straza gets Entra tokens for agents only. " +
		"Set it on the agent's own record, or make %s an agent first if it is one."
	entraTypeChangeMsg = "%s holds the Entra agent identity %s, and only an agent can hold one. " +
		"Unset it first with strazactl users entra-agent-id unset %s, then change its type."
	entraHeldMsg = "%s is already the Entra agent identity of %s, and two agents cannot share one, because Entra would record both agents' calls as one identity. " +
		"Unset it on %s first with strazactl users entra-agent-id unset %s, or give %s an agent identity of its own."
	entraCheckFailedMsg = "Straza could not check the Entra agent id against its other users, because its database did not answer, so nothing was saved. " +
		"Send the request again."
)

// refuseEntraAgentID judges the admin write that turns before into after
// with store.CheckEntraAgentID, writes the refusal and answers true when the
// write must not go on. A failed check answers 500 and true, so nothing is
// saved unchecked.
func (a *App) refuseEntraAgentID(w http.ResponseWriter, r *http.Request, before, after store.User) bool {
	refusal, holder, err := store.CheckEntraAgentID(r.Context(), a.store.Users(), before, after)
	switch {
	case err != nil:
		a.fail(w, r, http.StatusInternalServerError, entraCheckFailedMsg, err)
	case refusal == store.EntraAgentNotAgent:
		apiError(w, http.StatusBadRequest, fmt.Sprintf(entraNotAgentMsg, after.Username, userTypePhrase(after.UserType), after.Username))
	case refusal == store.EntraAgentTypeChange:
		apiError(w, http.StatusBadRequest, fmt.Sprintf(entraTypeChangeMsg, after.Username, after.EntraAgentID, after.Username))
	case refusal == store.EntraAgentHeld:
		apiError(w, http.StatusConflict, fmt.Sprintf(entraHeldMsg, after.EntraAgentID, holder.Username, holder.Username, holder.Username, after.Username))
	default:
		return false
	}
	return true
}

// userTypePhrase names a user_type in a sentence about the user.
func userTypePhrase(userType string) string {
	switch userType {
	case store.UserTypeHuman:
		return "a person"
	case store.UserTypeService:
		return "a service account"
	case store.UserTypeAgent:
		return "an agent"
	}
	return "an unclassified identity with no user_type"
}

// entraAgentIDChanged drops the cached subject of each of the user's
// sessions after a write changed the user's Entra agent id, so the next
// call on this server checks in again and reads the new id. Another server
// stops serving the old id once the agent holds a token from a check-in
// that began after the change, because forToken compares the token's eai
// claim, and serves a token from before it until it expires. The user's
// client tokens stay, because an Entra provider keys them by the id and no
// other provider's token depends on it. When the sessions cannot be listed,
// every cached subject is dropped, so no session on this server keeps the
// old id.
func (a *App) entraAgentIDChanged(ctx context.Context, userID string) {
	sessions, err := a.store.Sessions().ListByUser(ctx, userID)
	if err != nil {
		n := a.subjects.dropAll()
		a.reqlog(ctx).Warn("the Entra agent id of a user changed and the user's sessions could not be listed, so every cached subject was dropped and each session checks in again at its next call",
			"user", userID, "dropped", n, "err", err)
		return
	}
	for _, s := range sessions {
		a.subjects.drop(s.ID)
	}
}
