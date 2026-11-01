-- Downgrade: the column goes, which deletes every covers line. Moving up
-- again leaves every binding of a pinned server covering every object
-- until someone narrows it. See the sqlite twin.
ALTER TABLE tool_bindings DROP COLUMN object_scope;
