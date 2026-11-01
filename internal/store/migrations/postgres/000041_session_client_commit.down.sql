-- Downgrade: an older server reads no client commit, so the column goes.
ALTER TABLE sessions DROP COLUMN client_commit;
