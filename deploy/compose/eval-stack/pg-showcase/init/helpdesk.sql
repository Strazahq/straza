-- The helpdesk database: customers, their tickets and the comments on them,
-- about a year of a support team's work. helpdesk_read may only read.
-- helpdesk_write may also insert, update and delete rows, and neither may
-- create tables, so Postgres refuses a change of structure even when Straza
-- lets it through. The rows come from a fixed seed, and only the run that
-- creates the tables writes them, so a later run keeps what people changed.

SELECT to_regclass('public.tickets') IS NULL AS fresh \gset

CREATE TABLE IF NOT EXISTS public.customers (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name       text NOT NULL,
  email      text NOT NULL UNIQUE,
  company    text NOT NULL,
  created_on date NOT NULL
);

CREATE TABLE IF NOT EXISTS public.tickets (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  customer_id bigint NOT NULL REFERENCES public.customers (id),
  subject     text NOT NULL,
  status      text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'pending', 'closed')),
  priority    text NOT NULL DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
  assignee    text,
  opened_on   date NOT NULL DEFAULT current_date,
  updated_on  date NOT NULL DEFAULT current_date
);

CREATE TABLE IF NOT EXISTS public.comments (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ticket_id  bigint NOT NULL REFERENCES public.tickets (id) ON DELETE CASCADE,
  author     text NOT NULL,
  body       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

\if :fresh
SELECT setseed(0.42) \gset

INSERT INTO public.customers (name, email, company, created_on)
SELECT f.name || ' ' || l.name,
       lower(f.name || '.' || l.name || g) || '@' || lower(replace(c.name, ' ', '')) || '.example',
       c.name,
       current_date - (400 + (random() * 900)::int)
FROM generate_series(1, 400) AS g
CROSS JOIN LATERAL (SELECT (ARRAY['Anna', 'Peter', 'Maria', 'Jan', 'Eva', 'Tomas', 'Lucia', 'Martin', 'Sofia', 'Daniel', 'Elena', 'Lukas'])[1 + (random() * 11)::int + g * 0] AS name) AS f
CROSS JOIN LATERAL (SELECT (ARRAY['Novak', 'Horvat', 'Kovac', 'Weber', 'Muller', 'Rossi', 'Dubois', 'Smith', 'Varga', 'Nagy', 'Jansen', 'Garcia'])[1 + (random() * 11)::int + g * 0] AS name) AS l
CROSS JOIN LATERAL (SELECT (ARRAY['Northwind Logistics', 'Blue Harbor Bank', 'Alpine Clinics', 'Kestrel Retail', 'Danube Energy', 'Meridian Insurance', 'Tatra Foods', 'Orbit Telecom'])[1 + (random() * 7)::int + g * 0] AS name) AS c;

-- Older tickets are mostly closed, and recent ones are open or pending.
-- Most pending tickets moved in the last week, so close_stale_tickets
-- with a wait of 7 days finds more than its cap of 50 and rolls back, while
-- a wait of 30 days closes a few dozen.
INSERT INTO public.tickets (customer_id, subject, status, priority, assignee, opened_on, updated_on)
SELECT 1 + (random() * 399)::int,
       s.subject,
       t.status,
       (ARRAY['low', 'normal', 'normal', 'normal', 'high', 'urgent'])[1 + (random() * 5)::int],
       (ARRAY['ivan', 'grace', 'carol', NULL])[1 + (random() * 3)::int],
       current_date - t.age_days,
       CASE WHEN t.status = 'pending' THEN current_date - least(t.age_days, (random() ^ 6 * 60)::int)
            ELSE current_date - (t.age_days * random())::int END
FROM (SELECT g, age_days,
             CASE WHEN age_days > 60 AND random() < 0.85 THEN 'closed'
                  WHEN random() < 0.45 THEN 'pending'
                  WHEN random() < 0.5 THEN 'open'
                  ELSE 'closed' END AS status
      FROM (SELECT g, (random() * 365)::int AS age_days FROM generate_series(1, 3000) AS g) AS a) AS t
CROSS JOIN LATERAL (SELECT (ARRAY[
  'VPN drops every hour', 'Printer on floor 2 is offline', 'Reset password for a new hire',
  'Laptop battery swells', 'Shared drive is read-only', 'Badge does not open door 3',
  'Invoice PDF shows the wrong VAT', 'Mail to a partner bounces', 'Payroll export times out',
  'Calendar invites arrive twice', 'Monitor flickers after docking', 'Cannot install the CRM plugin',
  'Account locked after travel', 'Wi-Fi in meeting room B is slow', 'Report lost in last update',
  'Phone app crashes on login'])[1 + (random() * 15)::int + t.g * 0] AS subject) AS s;

INSERT INTO public.comments (ticket_id, author, body, created_at)
SELECT t.id,
       (ARRAY['ivan', 'grace', 'carol', 'customer'])[1 + (random() * 3)::int + n * 0],
       (ARRAY['Asked the customer for a screenshot.', 'Reproduced on a test machine.',
              'Escalated to the network team.', 'Waiting for the vendor to answer.',
              'Applied the workaround from the knowledge base.', 'Customer confirms it works again.',
              'Scheduled a call for tomorrow morning.', 'Replaced the device.'])[1 + (random() * 7)::int + n * 0],
       t.opened_on + make_interval(hours => (n * 20 + random() * 18)::int)
FROM public.tickets AS t
CROSS JOIN LATERAL generate_series(1, 1 + (random() * 3)::int + (t.id * 0)::int) AS n;
\endif

GRANT SELECT ON ALL TABLES IN SCHEMA public TO helpdesk_read;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO helpdesk_write;
