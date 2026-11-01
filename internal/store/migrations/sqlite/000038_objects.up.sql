-- Object lists (object-scoped roles, slice 1): a fourth
-- credential scope, "listing", for the credential Straza lists a server's
-- objects with, and the two tables that hold each server's object list.
--
-- sqlite cannot widen a CHECK in place, so credentials is rebuilt the way
-- 000027 and 000030 did, which is safe only with foreign-key enforcement
-- OFF for the run, as the store's migration lane guarantees. No table
-- references credentials, the rebuild keeps ids and rows, and the index is
-- recreated because DROP TABLE takes it along.
CREATE TABLE credentials_new (
    id TEXT PRIMARY KEY,
    app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    scope TEXT NOT NULL CHECK (scope IN ('role', 'user', 'app', 'listing')),
    owner_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('static', 'oauth', 'token')),
    enc_payload BLOB NOT NULL,
    oauth_meta TEXT NOT NULL DEFAULT '{}',
    rotated_at TEXT,
    created_at TEXT NOT NULL
);
INSERT INTO credentials_new (id, app_id, scope, owner_id, kind, enc_payload, oauth_meta, rotated_at, created_at)
    SELECT id, app_id, scope, owner_id, kind, enc_payload, oauth_meta, rotated_at, created_at FROM credentials;
DROP TABLE credentials;
ALTER TABLE credentials_new RENAME TO credentials;
CREATE INDEX idx_credentials_app ON credentials(app_id);

-- One row per server that pins an object type. version counts the changed
-- listings, 0 before the first good one. The claim columns name the
-- replica that lists the server now, so only one does.
CREATE TABLE object_lists (
    app_id TEXT PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    decl_hash TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 0,
    list_hash TEXT NOT NULL DEFAULT '',
    object_count INTEGER NOT NULL DEFAULT 0,
    joined_count INTEGER NOT NULL DEFAULT 0,
    left_count INTEGER NOT NULL DEFAULT 0,
    outcome TEXT NOT NULL DEFAULT 'never' CHECK (outcome IN ('never', 'ok', 'failed')),
    reason TEXT NOT NULL DEFAULT '',
    listed_at TEXT,
    attempted_at TEXT,
    due_at TEXT NOT NULL,
    claim_owner TEXT NOT NULL DEFAULT '',
    claim_until TEXT
);

-- The objects of each server's last good listing.
CREATE TABLE objects (
    app_id TEXT NOT NULL REFERENCES object_lists(app_id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    value TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    attributes TEXT NOT NULL DEFAULT '{}',
    added_version INTEGER NOT NULL,
    PRIMARY KEY (app_id, id),
    UNIQUE (app_id, value)
);
