-- Object-scoped roles, slice 2: a binding's covers line, the objects of its
-- server's pinned type it covers. The column holds JSON text,
-- {"every":true} or {"objects":[{"id":"...","value":"..."}]}, and NULL
-- for no covers line. The name is not "covers" because COVERS is an SQL
-- keyword.
--
-- On a server that pins a type, NULL covers nothing at the gate. So every
-- binding whose server's stored manifest already pins a type is backfilled
-- with {"every":true}, which is the reach it had before this migration.
-- json_type answers NULL for a missing path and the guard keeps a manifest
-- that is not valid JSON from failing the run.
ALTER TABLE tool_bindings ADD COLUMN object_scope TEXT;
UPDATE tool_bindings SET object_scope = '{"every":true}'
WHERE app_id IN (
    SELECT id FROM apps
    WHERE CASE WHEN json_valid(manifest)
        THEN json_type(manifest, '$.straza.objects.type') = 'text'
            AND json_extract(manifest, '$.straza.objects.type') <> ''
        ELSE 0 END
);
