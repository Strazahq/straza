-- The showcase logins and databases on midPoint's Postgres server. It runs
-- in the postgres maintenance database, so midPoint's own database is never
-- opened. Every statement checks first, so a second run changes nothing.
-- Each password is the login's own name, a lab value like the straza and
-- midpoint logins of this stack.

SELECT format('CREATE ROLE %I LOGIN PASSWORD %L', r, r)
FROM unnest(ARRAY['helpdesk_read', 'helpdesk_write', 'sales_eu_app', 'sales_us_app', 'playground_app']) AS r
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r)
\gexec

SELECT format('CREATE DATABASE %I', d)
FROM unnest(ARRAY['helpdesk', 'sales_eu', 'sales_us']) AS d
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = d)
\gexec

-- playground belongs to its login, so a person who covers it in Straza can
-- also create tables there through execute_ddl.
SELECT 'CREATE DATABASE playground OWNER playground_app'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'playground')
\gexec

-- Postgres lets every login connect to a new database. Each showcase
-- database takes only its own logins, so no showcase login can open another
-- showcase database.
REVOKE CONNECT, TEMPORARY ON DATABASE helpdesk, sales_eu, sales_us, playground FROM PUBLIC;
GRANT CONNECT ON DATABASE helpdesk TO helpdesk_read, helpdesk_write;
GRANT CONNECT ON DATABASE sales_eu TO sales_eu_app;
GRANT CONNECT ON DATABASE sales_us TO sales_us_app;
