-- entra_agent_id is the client id of the Entra agent identity an AI agent
-- acts as at Entra, a GUID in lower case, or empty for none. Only an agent
-- holds one. The index keeps two live users from holding the same id, so
-- Entra never records two agents' calls as one identity. A soft-deleted
-- row is left out, because no API can read that row to clear its id. See
-- the sqlite twin.
ALTER TABLE users ADD COLUMN entra_agent_id text DEFAULT ''::text NOT NULL;
CREATE UNIQUE INDEX idx_users_entra_agent_id ON users USING btree (entra_agent_id)
    WHERE (entra_agent_id <> ''::text AND deleted_at IS NULL);
