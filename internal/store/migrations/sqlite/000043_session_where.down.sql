-- Downgrade: an older server reads no machine and no folder of a session,
-- so the two columns go.
ALTER TABLE sessions DROP COLUMN folder;
ALTER TABLE sessions DROP COLUMN host;
