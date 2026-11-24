-- A user may come from Microsoft Entra ID over Graph. The sync that reads
-- Entra creates such a user with the origin entra, and takes a user of
-- another origin over when Entra owns that kind of record. SQLite cannot
-- change a check in place, so the table is rebuilt. The migration runner
-- switches foreign keys off for the rebuild, so the drop does not cascade
-- into devices and sessions, and checks every key after it.
CREATE TABLE users_new (
    id TEXT PRIMARY KEY,
    external_id TEXT NOT NULL DEFAULT '',
    username TEXT NOT NULL UNIQUE,
    email TEXT NOT NULL DEFAULT '',
    display TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    origin TEXT NOT NULL DEFAULT 'local' CHECK (origin IN ('local', 'scim', 'entra')),
    password_hash TEXT NOT NULL DEFAULT '',
    attrs TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    deleted_at TEXT,
    user_type TEXT NOT NULL DEFAULT '',
    agency_mode TEXT NOT NULL DEFAULT '',
    sponsor TEXT NOT NULL DEFAULT '',
    swarm_id TEXT NOT NULL DEFAULT '',
    ephemeral INTEGER NOT NULL DEFAULT 0,
    title TEXT NOT NULL DEFAULT '',
    entra_agent_id TEXT NOT NULL DEFAULT ''
);
INSERT INTO users_new (id, external_id, username, email, display, status, origin, password_hash, attrs,
        created_at, updated_at, deleted_at, user_type, agency_mode, sponsor, swarm_id, ephemeral, title, entra_agent_id)
    SELECT id, external_id, username, email, display, status, origin, password_hash, attrs,
        created_at, updated_at, deleted_at, user_type, agency_mode, sponsor, swarm_id, ephemeral, title, entra_agent_id FROM users;
DROP TABLE users;
ALTER TABLE users_new RENAME TO users;
CREATE UNIQUE INDEX idx_users_external_id ON users(external_id) WHERE external_id <> '';
CREATE UNIQUE INDEX idx_users_entra_agent_id ON users(entra_agent_id)
    WHERE entra_agent_id <> '' AND deleted_at IS NULL;
