-- Downgrade: the two tables go and the listing rows are deleted, because a
-- pre-000038 server cannot represent them. Every other credential survives.
DROP TABLE objects;
DROP TABLE object_lists;
DELETE FROM credentials WHERE scope = 'listing';
ALTER TABLE credentials DROP CONSTRAINT credentials_scope_check;
ALTER TABLE credentials ADD CONSTRAINT credentials_scope_check CHECK (scope IN ('role', 'user', 'app'));
