-- The rows the Entra sync wrote end here, because the schema below this
-- migration cannot hold them. The members stay in the group in Entra, and
-- the sync writes them again after the next move up.
DROP TABLE entra_role_groups;
DROP TABLE leases;
CREATE TABLE role_assignments_old (
    id TEXT PRIMARY KEY,
    subject_kind TEXT NOT NULL CHECK (subject_kind IN ('user')),
    subject_id TEXT NOT NULL,
    role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    valid_from TEXT,
    valid_to TEXT,
    origin TEXT NOT NULL DEFAULT 'admin' CHECK (origin IN ('scim', 'admin')),
    created_at TEXT NOT NULL,
    UNIQUE (subject_kind, subject_id, role_id)
);
INSERT INTO role_assignments_old (id, subject_kind, subject_id, role_id, valid_from, valid_to, origin, created_at)
    SELECT id, subject_kind, subject_id, role_id, valid_from, valid_to, origin, created_at FROM role_assignments
    WHERE origin <> 'entra';
DROP TABLE role_assignments;
ALTER TABLE role_assignments_old RENAME TO role_assignments;
CREATE INDEX idx_role_assignments_subject ON role_assignments(subject_kind, subject_id);
