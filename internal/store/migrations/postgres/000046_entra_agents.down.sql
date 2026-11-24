-- Downgrade: the schema below this migration keeps no state of an agent
-- from Entra. The agents stay as users, and the sync writes the rows again
-- after the next move up.
DROP TABLE entra_agents;
