-- Downgrade: an older server reads no Entra agent id, so the index and the
-- column go. The index goes first, because SQLite refuses to drop an
-- indexed column.
DROP INDEX idx_users_entra_agent_id;
ALTER TABLE users DROP COLUMN entra_agent_id;
