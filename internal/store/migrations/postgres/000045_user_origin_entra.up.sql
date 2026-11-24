-- A user may come from Microsoft Entra ID over Graph. The sync that reads
-- Entra creates such a user with the origin entra, and takes a user of
-- another origin over when Entra owns that kind of record.
ALTER TABLE users DROP CONSTRAINT users_origin_check;
ALTER TABLE users ADD CONSTRAINT users_origin_check
    CHECK ((origin = ANY (ARRAY['local'::text, 'scim'::text, 'entra'::text])));
