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
    user_id text PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    identity text NOT NULL DEFAULT '',
    agent_user text NOT NULL DEFAULT '',
    agent_user_id text NOT NULL DEFAULT '',
    sponsor_state text NOT NULL DEFAULT '',
    sponsors text NOT NULL DEFAULT '[]',
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT entra_agents_identity_check CHECK ((identity = ANY (ARRAY[''::text, 'enabled'::text, 'disabled'::text]))),
    CONSTRAINT entra_agents_agent_user_check CHECK ((agent_user = ANY (ARRAY[''::text, 'enabled'::text, 'disabled'::text]))),
    CONSTRAINT entra_agents_sponsor_state_check
        CHECK ((sponsor_state = ANY (ARRAY[''::text, 'none'::text, 'several'::text, 'not-person'::text, 'no-user'::text])))
);
