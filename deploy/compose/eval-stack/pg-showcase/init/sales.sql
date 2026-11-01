-- One sales database: customers, products, orders and their lines, about
-- 18 months of trade. sales_eu and sales_us share this schema, and init.sh
-- passes each its currency, its regions and its login as psql variables.
-- The one login may read and change every row, the single broad account
-- most teams run, so Straza alone decides who reaches which database. The
-- rows come from a fixed seed, and only the run that creates the tables
-- writes them, so a later run keeps what people changed.

SELECT to_regclass('sales.orders') IS NULL AS fresh \gset

CREATE SCHEMA IF NOT EXISTS sales;

CREATE TABLE IF NOT EXISTS sales.customers (
  id      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name    text NOT NULL,
  region  text NOT NULL,
  segment text NOT NULL CHECK (segment IN ('enterprise', 'mid-market', 'small business'))
);

CREATE TABLE IF NOT EXISTS sales.products (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  sku        text NOT NULL UNIQUE,
  name       text NOT NULL,
  category   text NOT NULL,
  list_price numeric(10, 2) NOT NULL
);

CREATE TABLE IF NOT EXISTS sales.orders (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  customer_id bigint NOT NULL REFERENCES sales.customers (id),
  region      text NOT NULL,
  ordered_on  date NOT NULL,
  status      text NOT NULL CHECK (status IN ('placed', 'shipped', 'invoiced', 'cancelled')),
  currency    char(3) NOT NULL,
  net_amount  numeric(12, 2) NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS sales.order_lines (
  order_id   bigint NOT NULL REFERENCES sales.orders (id) ON DELETE CASCADE,
  line       int NOT NULL,
  product_id bigint NOT NULL REFERENCES sales.products (id),
  quantity   int NOT NULL CHECK (quantity > 0),
  unit_price numeric(10, 2) NOT NULL,
  PRIMARY KEY (order_id, line)
);

\if :fresh
SELECT setseed(:seed) \gset

INSERT INTO sales.products (sku, name, category, list_price)
SELECT p.sku, p.name, p.category, p.price
FROM (VALUES
  ('SRV-100', 'Rack server, 1U', 'hardware', 3400.00),
  ('SRV-200', 'Rack server, 2U', 'hardware', 5900.00),
  ('NET-010', 'Access switch, 48 ports', 'hardware', 1800.00),
  ('NET-020', 'Firewall appliance', 'hardware', 4200.00),
  ('LIC-CRM', 'CRM licence, per seat and year', 'software', 420.00),
  ('LIC-ERP', 'ERP licence, per seat and year', 'software', 960.00),
  ('LIC-SEC', 'Endpoint security, per device and year', 'software', 38.00),
  ('SUP-GLD', 'Gold support, per year', 'service', 7500.00),
  ('SUP-SLV', 'Silver support, per year', 'service', 3900.00),
  ('SVC-MIG', 'Migration project, per day', 'service', 1100.00),
  ('SVC-TRN', 'Training workshop, per day', 'service', 1400.00),
  ('ACC-DCK', 'Laptop dock', 'accessory', 240.00)
) AS p (sku, name, category, price);

INSERT INTO sales.customers (name, region, segment)
SELECT a.word || ' ' || b.word || ' ' || (ARRAY['Group', 'Systems', 'Partners', 'Industries', 'Labs'])[1 + (random() * 4)::int + g * 0],
       r.region,
       (ARRAY['enterprise', 'mid-market', 'mid-market', 'small business', 'small business'])[1 + (random() * 4)::int + g * 0]
FROM generate_series(1, 300) AS g
CROSS JOIN LATERAL (SELECT (ARRAY['North', 'Silver', 'Granite', 'Harbor', 'Summit', 'Cedar', 'Falcon', 'Atlas', 'Beacon', 'Vertex'])[1 + (random() * 9)::int + g * 0] AS word) AS a
CROSS JOIN LATERAL (SELECT (ARRAY['Freight', 'Health', 'Capital', 'Foods', 'Motors', 'Media', 'Energy', 'Software', 'Retail', 'Pharma'])[1 + (random() * 9)::int + g * 0] AS word) AS b
CROSS JOIN LATERAL (SELECT (:'regions'::text[])[1 + (random() * (cardinality(:'regions'::text[]) - 1))::int + g * 0] AS region) AS r;

INSERT INTO sales.orders (customer_id, region, ordered_on, status, currency)
SELECT c.id, c.region, o.ordered_on,
       CASE WHEN random() < 0.04 THEN 'cancelled'
            WHEN o.ordered_on > current_date - 10 THEN 'placed'
            WHEN o.ordered_on > current_date - 40 THEN 'shipped'
            ELSE 'invoiced' END,
       :'currency'
FROM (SELECT g, 1 + (random() * 299)::int AS customer_id, current_date - (random() * 540)::int AS ordered_on
      FROM generate_series(1, 10000) AS g) AS o
JOIN sales.customers AS c ON c.id = o.customer_id;

INSERT INTO sales.order_lines (order_id, line, product_id, quantity, unit_price)
SELECT o.id, n, p.id, q.quantity, round(p.list_price * (0.8 + random() * 0.2)::numeric, 2)
FROM sales.orders AS o
CROSS JOIN LATERAL generate_series(1, 1 + (random() * 3)::int + (o.id * 0)::int) AS n
CROSS JOIN LATERAL (SELECT 1 + (random() * 11)::int + (o.id * 0)::int + n * 0 AS product_id) AS pick
JOIN sales.products AS p ON p.id = pick.product_id
CROSS JOIN LATERAL (SELECT 1 + (random() * 9)::int + n * 0 AS quantity) AS q;

UPDATE sales.orders AS o
SET net_amount = l.total
FROM (SELECT order_id, sum(quantity * unit_price) AS total FROM sales.order_lines GROUP BY order_id) AS l
WHERE l.order_id = o.id;
\endif

GRANT USAGE ON SCHEMA sales TO :"login";
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA sales TO :"login";
