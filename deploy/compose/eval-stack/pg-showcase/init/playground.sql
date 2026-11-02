-- The playground database: one small table and three rows, and no Straza
-- role covers it after the seed, so a person can try covering it from the
-- console. playground_app owns the database and everything in it, so once
-- a role covers it, execute_ddl may create tables here too. The table is
-- made as playground_app, so that login owns it. Only the run that creates
-- the table writes the three rows, so notes a person deletes stay deleted.

SET ROLE playground_app;

SELECT to_regclass('public.notes') IS NULL AS fresh \gset

CREATE TABLE IF NOT EXISTS public.notes (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  title      text NOT NULL,
  body       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

\if :fresh
INSERT INTO public.notes (title, body)
SELECT n.title, n.body
FROM (VALUES
  ('Welcome', 'This database is yours to try. No role covers it until you write one.'),
  ('Covering it', 'Give a role on the postgres server a covers line that names playground.'),
  ('Changing it', 'playground_app owns this database, so execute_ddl may create tables here.')
) AS n (title, body);
\endif
