-- Downgrade: a pre-000038 server has no object lists and cannot represent
-- a listing credential, so the two tables go and the listing rows are
-- deleted (an operator sets each again on the new version). Every other
-- credential survives. Same rebuild procedure as the up, same FK-off
-- requirement.
DROP TABLE objects;
DROP TABLE object_lists;
DELETE FROM credentials WHERE scope = 'listing';
CREATE TABLE credentials_new (
    id TEXT PRIMARY KEY,
    app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    scope TEXT NOT NULL CHECK (scope IN ('role', 'user', 'app')),
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
