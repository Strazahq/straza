-- Downgrade: the schema below this migration knows no user of origin entra.
-- Such a user stays, with its Entra object id, as a local user, because
-- sessions, devices and audit records name it.
UPDATE users SET origin = 'local' WHERE origin = 'entra';
ALTER TABLE users DROP CONSTRAINT users_origin_check;
ALTER TABLE users ADD CONSTRAINT users_origin_check
    CHECK ((origin = ANY (ARRAY['local'::text, 'scim'::text])));
