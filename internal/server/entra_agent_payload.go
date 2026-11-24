package server

import (
	"context"

	"github.com/strazahq/straza/internal/store"
)

// entraAgentPayload is what Microsoft Entra ID says about an agent that
// Entra owns. Identity and AgentUser say how Entra lists the agent identity
// and its agent user: enabled, disabled, or absent when Entra lists none.
// AgentUserID is the agent user's Entra object id, so a reader can open it.
// SponsorState is absent while the agent's sponsor comes from Entra, and
// else none, several, not-person or no-user, with what Entra lists in
// Sponsors.
type entraAgentPayload struct {
	Identity     string               `json:"identity,omitempty"`
	AgentUser    string               `json:"agent_user,omitempty"`
	AgentUserID  string               `json:"agent_user_id,omitempty"`
	SponsorState string               `json:"sponsor_state,omitempty"`
	Sponsors     []store.EntraSponsor `json:"sponsors,omitempty"`
}

// entraAgentOf answers the Entra state of the user u, or nil when u is no
// agent of origin entra or the sync has stored nothing about it yet. Like
// the other parts of a user's detail, a failed read leaves it out.
func (a *App) entraAgentOf(ctx context.Context, u store.User) *entraAgentPayload {
	if u.Origin != store.OriginEntra || u.Kind() != store.UserKindAgent {
		return nil
	}
	row, err := a.store.EntraAgents().Get(ctx, u.ID)
	if err != nil {
		return nil
	}
	return &entraAgentPayload{Identity: row.Identity, AgentUser: row.AgentUser, AgentUserID: row.AgentUserID, SponsorState: row.SponsorState, Sponsors: row.Sponsors}
}
