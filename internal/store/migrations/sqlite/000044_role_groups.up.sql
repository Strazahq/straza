-- A role assignment may come from the Entra sync, which writes the members
-- of the role's group in Entra with the origin entra. A holder has at most
-- one such row and at most one row of any other origin, so the unique key
-- over the holder becomes two partial keys. The identity manager and an
-- admin then change only their row, the sync only its own, and a removal by
-- one never takes away what the other granted. SQLite cannot change a check
-- in place, so the table is rebuilt.
CREATE TABLE role_assignments_new (
    id TEXT PRIMARY KEY,
    subject_kind TEXT NOT NULL CHECK (subject_kind IN ('user')),
    subject_id TEXT NOT NULL,
    role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    valid_from TEXT,
    valid_to TEXT,
    origin TEXT NOT NULL DEFAULT 'admin' CHECK (origin IN ('scim', 'admin', 'entra')),
    created_at TEXT NOT NULL
);
INSERT INTO role_assignments_new (id, subject_kind, subject_id, role_id, valid_from, valid_to, origin, created_at)
    SELECT id, subject_kind, subject_id, role_id, valid_from, valid_to, origin, created_at FROM role_assignments;
DROP TABLE role_assignments;
ALTER TABLE role_assignments_new RENAME TO role_assignments;
CREATE INDEX idx_role_assignments_subject ON role_assignments(subject_kind, subject_id);
CREATE UNIQUE INDEX role_assignments_holder_key ON role_assignments(subject_kind, subject_id, role_id)
    WHERE origin <> 'entra';
CREATE UNIQUE INDEX role_assignments_entra_holder_key ON role_assignments(subject_kind, subject_id, role_id)
    WHERE origin = 'entra';

-- One row per background job that must run on one replica at a time. holder
-- is the replica that runs it until the time until, and '' when nobody does.
CREATE TABLE leases (
    name TEXT PRIMARY KEY,
    holder TEXT NOT NULL DEFAULT '',
    until TEXT
);

-- One row per role that has, had or is getting a group in Entra. role_id has
-- no foreign key, because the row outlives a deleted role until its group is
-- deleted too. unique_name is the key of the create in Entra and never
-- changes. group_id is the group's object id, '' until the create answered.
-- read_at is the time of the last complete read of the members. error is
-- the sentence of the last failure, '' after a good pass. not_applied is a
-- JSON list of the members the last read found and did not apply.
CREATE TABLE entra_role_groups (
    role_id TEXT PRIMARY KEY,
    unique_name TEXT NOT NULL UNIQUE,
    group_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('creating', 'linked', 'missing', 'deleting', 'held', 'released')),
    read_at TEXT,
    error TEXT NOT NULL DEFAULT '',
    not_applied TEXT NOT NULL DEFAULT '[]',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
