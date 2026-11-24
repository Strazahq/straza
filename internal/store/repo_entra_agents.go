package store

import (
	"context"
	"encoding/json"
)

// How Entra lists one of the two objects of an agent.
const (
	// EntraObjectEnabled: Entra lists the object and it is enabled.
	EntraObjectEnabled = "enabled"
	// EntraObjectDisabled: Entra lists the object as disabled, or deleted it.
	EntraObjectDisabled = "disabled"
)

// Why an agent from Entra has no sponsor in Straza.
const (
	// EntraSponsorNone: Entra lists no sponsor.
	EntraSponsorNone = "none"
	// EntraSponsorSeveral: Entra lists more than one sponsor.
	EntraSponsorSeveral = "several"
	// EntraSponsorNotPerson: the one sponsor is a group or an agent.
	EntraSponsorNotPerson = "not-person"
	// EntraSponsorNoUser: the one sponsor is a person Straza has no user for.
	EntraSponsorNoUser = "no-user"
)

// EntraSponsor is one sponsor Entra lists for an agent: its object id and
// its kind, a word of the Graph client such as user or group.
type EntraSponsor struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// EntraAgent is what Microsoft Entra ID says about the agent UserID, which
// the Entra sync owns. Identity and AgentUser say how Entra lists the agent
// identity and its agent user: empty when the sync has not seen the object,
// else EntraObjectEnabled or EntraObjectDisabled. AgentUserID is the agent
// user's object id while Entra lists one. SponsorState is empty
// while Entra lists exactly one sponsor that is a Straza person, and else
// one of the EntraSponsor words. Sponsors is what Entra lists in that case.
type EntraAgent struct {
	UserID       string
	Identity     string
	AgentUser    string
	AgentUserID  string
	SponsorState string
	Sponsors     []EntraSponsor
}

// Active reports whether Entra lets the agent act: it lists at least one
// of the two objects and neither is disabled. An agent Entra says nothing
// about is not active.
func (a EntraAgent) Active() bool {
	return a.Identity != EntraObjectDisabled && a.AgentUser != EntraObjectDisabled &&
		(a.Identity != "" || a.AgentUser != "")
}

// EntraAgentRepo holds what Entra says about the agents the Entra sync
// owns. Only the sync writes it. A row goes when its user is deleted for
// good.
type EntraAgentRepo interface {
	// List answers every row, by user id.
	List(ctx context.Context) ([]EntraAgent, error)
	// GetByAgentUser answers the row of the live user whose agent user has
	// the Entra object id, and ErrNotFound for an empty id or when no such
	// row exists.
	GetByAgentUser(ctx context.Context, agentUserID string) (EntraAgent, error)
	// Get answers ErrNotFound for a user that has no row.
	Get(ctx context.Context, userID string) (EntraAgent, error)
	// Put stores a whole under its UserID, creating the row or replacing
	// it.
	Put(ctx context.Context, a EntraAgent) error
}

type entraAgentRepo struct{ s *sqlStore }

// EntraAgents answers the Entra agent repo over the same sqlStore.
func (s *sqlStore) EntraAgents() EntraAgentRepo { return entraAgentRepo{s} }

const entraAgentCols = "user_id, identity, agent_user, agent_user_id, sponsor_state, sponsors"

func scanEntraAgent(row scanner) (EntraAgent, error) {
	var a EntraAgent
	var sponsors string
	if err := row.Scan(&a.UserID, &a.Identity, &a.AgentUser, &a.AgentUserID, &a.SponsorState, &sponsors); err != nil {
		return EntraAgent{}, scanErr(err)
	}
	if sponsors == "[]" {
		return a, nil
	}
	if err := json.Unmarshal([]byte(sponsors), &a.Sponsors); err != nil {
		return EntraAgent{}, err
	}
	return a, nil
}

func (r entraAgentRepo) List(ctx context.Context) ([]EntraAgent, error) {
	rows, err := r.s.query(ctx, `SELECT `+entraAgentCols+` FROM entra_agents ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []EntraAgent
	for rows.Next() {
		a, err := scanEntraAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r entraAgentRepo) Get(ctx context.Context, userID string) (EntraAgent, error) {
	return scanEntraAgent(r.s.queryRow(ctx, `SELECT `+entraAgentCols+` FROM entra_agents WHERE user_id = $1`, userID))
}

func (r entraAgentRepo) GetByAgentUser(ctx context.Context, agentUserID string) (EntraAgent, error) {
	if agentUserID == "" {
		return EntraAgent{}, ErrNotFound
	}
	return scanEntraAgent(r.s.queryRow(ctx, `SELECT `+entraAgentCols+` FROM entra_agents
		WHERE agent_user_id = $1 AND user_id IN (SELECT id FROM users WHERE deleted_at IS NULL)
		ORDER BY user_id LIMIT 1`, agentUserID))
}

func (r entraAgentRepo) Put(ctx context.Context, a EntraAgent) error {
	sponsors := "[]"
	if len(a.Sponsors) > 0 {
		raw, err := json.Marshal(a.Sponsors)
		if err != nil {
			return err
		}
		sponsors = string(raw)
	}
	_, err := r.s.exec(ctx, `INSERT INTO entra_agents (`+entraAgentCols+`, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (user_id) DO UPDATE SET identity = excluded.identity, agent_user = excluded.agent_user,
			agent_user_id = excluded.agent_user_id, sponsor_state = excluded.sponsor_state,
			sponsors = excluded.sponsors, updated_at = excluded.updated_at`,
		a.UserID, a.Identity, a.AgentUser, a.AgentUserID, a.SponsorState, sponsors, r.s.tArg(now()))
	return err
}
