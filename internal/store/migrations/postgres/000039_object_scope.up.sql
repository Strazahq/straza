-- Object-scoped roles, slice 2: a binding's covers line, and the backfill
-- that keeps today's reach on a server that already pins an object type.
-- See the sqlite twin for the semantics. The column is text, not jsonb,
-- so both drivers read back the bytes the store wrote.
ALTER TABLE tool_bindings ADD COLUMN object_scope text;
UPDATE tool_bindings SET object_scope = '{"every":true}'
WHERE app_id IN (
    SELECT id FROM apps
    WHERE jsonb_typeof(manifest -> 'straza' -> 'objects' -> 'type') = 'string'
        AND manifest -> 'straza' -> 'objects' ->> 'type' <> ''
);
