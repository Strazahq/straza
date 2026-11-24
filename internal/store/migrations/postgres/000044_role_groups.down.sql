-- The rows the Entra sync wrote end here, because the schema below this
-- migration cannot hold them. The members stay in the group in Entra, and
-- the sync writes them again after the next move up.
DROP TABLE entra_role_groups;
DROP TABLE leases;
DELETE FROM role_assignments WHERE origin = 'entra';
DROP INDEX role_assignments_entra_holder_key;
DROP INDEX role_assignments_holder_key;
ALTER TABLE role_assignments
    ADD CONSTRAINT role_assignments_subject_kind_subject_id_role_id_key UNIQUE (subject_kind, subject_id, role_id);
ALTER TABLE role_assignments DROP CONSTRAINT role_assignments_origin_check;
ALTER TABLE role_assignments ADD CONSTRAINT role_assignments_origin_check
    CHECK ((origin = ANY (ARRAY['scim'::text, 'admin'::text])));
