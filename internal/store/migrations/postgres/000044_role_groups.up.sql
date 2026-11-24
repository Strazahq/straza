-- A role assignment may come from the Entra sync, which writes the members
-- of the role's group in Entra with the origin entra. A holder has at most
-- one such row and at most one row of any other origin, so the unique key
-- over the holder becomes two partial keys. The identity manager and an
-- admin then change only their row, the sync only its own, and a removal by
-- one never takes away what the other granted.
ALTER TABLE role_assignments DROP CONSTRAINT role_assignments_origin_check;
ALTER TABLE role_assignments ADD CONSTRAINT role_assignments_origin_check
    CHECK ((origin = ANY (ARRAY['scim'::text, 'admin'::text, 'entra'::text])));
ALTER TABLE role_assignments DROP CONSTRAINT role_assignments_subject_kind_subject_id_role_id_key;
CREATE UNIQUE INDEX role_assignments_holder_key ON role_assignments (subject_kind, subject_id, role_id)
    WHERE origin <> 'entra';
CREATE UNIQUE INDEX role_assignments_entra_holder_key ON role_assignments (subject_kind, subject_id, role_id)
    WHERE origin = 'entra';

-- One row per background job that must run on one replica at a time. holder
-- is the replica that runs it until the time until, and '' when nobody does.
CREATE TABLE leases (
    name text PRIMARY KEY,
    holder text NOT NULL DEFAULT '',
    until timestamp with time zone
);

-- One row per role that has, had or is getting a group in Entra. role_id has
-- no foreign key, because the row outlives a deleted role until its group is
-- deleted too. unique_name is the key of the create in Entra and never
-- changes. group_id is the group's object id, '' until the create answered.
-- read_at is the time of the last complete read of the members. error is
-- the sentence of the last failure, '' after a good pass. not_applied is a
-- JSON list of the members the last read found and did not apply.
CREATE TABLE entra_role_groups (
    role_id text PRIMARY KEY,
    unique_name text NOT NULL UNIQUE,
    group_id text NOT NULL DEFAULT '',
    state text NOT NULL,
    read_at timestamp with time zone,
    error text NOT NULL DEFAULT '',
    not_applied text NOT NULL DEFAULT '[]',
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT entra_role_groups_state_check
        CHECK ((state = ANY (ARRAY['creating'::text, 'linked'::text, 'missing'::text, 'deleting'::text, 'held'::text, 'released'::text])))
);
