-- Downgrade: a pre-000039 server reads no covers line, so the column goes
-- and every binding grants by its tools alone, as it did before. This
-- deletes every covers line, the named objects of a role on some objects
-- included. Moving up again backfills {"every":true} on every binding of a
-- server that pins an object type, so each such role covers every object
-- until someone narrows it again.
ALTER TABLE tool_bindings DROP COLUMN object_scope;
