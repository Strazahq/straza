-- What Microsoft Entra ID says about an agent the Entra sync owns, one row
-- for each such user. identity and agent_user say how Entra lists the agent
-- identity and its agent user: '' when the sync has not seen the object,
-- else enabled or disabled. The agent is active only while neither is
-- disabled. agent_user_id is the agent user's object id, '' for none, so
-- that a deleted agent user is found again. sponsor_state is '' while Entra lists exactly one sponsor that
-- is a Straza person, and otherwise says why the agent has no sponsor.
-- sponsors is a JSON list of the sponsors Entra lists in that case, each
-- with its object id and kind. The row goes with its user.
CREATE TABLE entra_agents (
    user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    identity TEXT NOT NULL DEFAULT '' CHECK (identity IN ('', 'enabled', 'disabled')),
    agent_user TEXT NOT NULL DEFAULT '' CHECK (agent_user IN ('', 'enabled', 'disabled')),
    agent_user_id TEXT NOT NULL DEFAULT '',
    sponsor_state TEXT NOT NULL DEFAULT '' CHECK (sponsor_state IN ('', 'none', 'several', 'not-person', 'no-user')),
    sponsors TEXT NOT NULL DEFAULT '[]',
    updated_at TEXT NOT NULL
);
