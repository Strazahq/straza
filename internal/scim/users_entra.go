package scim

import (
	"fmt"
	"net/http"

	"github.com/strazahq/straza/internal/store"
)

// The SCIM error details of an entraAgentId write Straza refuses.
const (
	entraNotGUIDMsg = "entraAgentId %q is not an Entra agent identity id, which is a GUID such as 5f8c2a1e-3b4d-4e6f-9a7b-1c2d3e4f5a6b. " +
		"Set it in the identity manager to the client id of the agent identity in the Entra admin centre, then send the user again"
	entraNotAgentMsg = "entraAgentId is set only on a user of type agent, because Straza gets Entra tokens for agents only, and %s is %s. " +
		"Map entraAgentId for agents only, or send userType agent if %s is an agent"
	entraTypeChangeMsg = "%s holds entraAgentId %s, and only a user of type agent can hold one. " +
		"Send a PATCH remove of entraAgentId first, then change userType"
	entraHeldMsg = "entraAgentId %s already belongs to %s, and two agents cannot share one Entra agent identity, because Entra would record both agents' calls as one identity. " +
		"Send a PATCH remove of entraAgentId for %s first, or correct the value in the identity manager"
	entraCheckFailedMsg = "Straza could not check entraAgentId against its other users, because its database did not answer, so nothing was saved. " +
		"Send the user again. The identity manager's next reconciliation sends it again too"
)

// normEntraAgentID answers the stored form of an entraAgentId value, or the
// error a write of it answers with 400 invalidValue.
func normEntraAgentID(v any) (string, error) {
	s, _ := v.(string)
	id, ok := store.NormalizeEntraAgentID(s)
	if !ok {
		return "", fmt.Errorf(entraNotGUIDMsg, s)
	}
	return id, nil
}

// applyEntraOp applies one PATCH operation on entraAgentId.
func applyEntraOp(u *store.User, op patchOp) error {
	if op.op == "remove" {
		u.EntraAgentID = ""
		return nil
	}
	id, err := normEntraAgentID(op.value)
	if err != nil {
		return err
	}
	u.EntraAgentID = id
	return nil
}

// refuseEntraAgentID judges the write that turns before into after with
// store.CheckEntraAgentID, writes the refusal and answers true when the
// write must not go on. A create passes the zero before. A create whose id
// is held by a row of the same userName goes on, because that create
// revives the row or answers the userName conflict. A failed check writes
// 500 and answers true, so nothing is saved unchecked.
func (s *Server) refuseEntraAgentID(w http.ResponseWriter, r *http.Request, before, after store.User) bool {
	refusal, holder, err := store.CheckEntraAgentID(r.Context(), s.deps.Store.Users(), before, after)
	switch {
	case err != nil:
		writeError(w, http.StatusInternalServerError, "", entraCheckFailedMsg)
	case refusal == store.EntraAgentNotAgent:
		writeError(w, http.StatusBadRequest, "invalidValue", fmt.Sprintf(entraNotAgentMsg, after.Username, typeWord(after.UserType), after.Username))
	case refusal == store.EntraAgentTypeChange:
		writeError(w, http.StatusBadRequest, "invalidValue", fmt.Sprintf(entraTypeChangeMsg, after.Username, after.EntraAgentID))
	case refusal == store.EntraAgentHeld && (before.ID != "" || holder.Username != after.Username):
		writeError(w, http.StatusConflict, "uniqueness", fmt.Sprintf(entraHeldMsg, after.EntraAgentID, holder.Username, holder.Username))
	default:
		return false
	}
	return true
}

// typeWord names a userType in a sentence about the user.
func typeWord(userType string) string {
	switch userType {
	case store.UserTypeHuman:
		return "a person"
	case store.UserTypeService:
		return "a service account"
	case store.UserTypeAgent:
		return "an agent"
	}
	return "unclassified, with no userType"
}
