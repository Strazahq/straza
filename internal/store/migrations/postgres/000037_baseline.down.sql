-- Drops every table the baseline creates, children before their parents.
-- The test harness uses it to empty a database. strazad migrate never
-- reaches it, because no version below 37 exists to move to.

DROP TABLE approvals;
DROP TABLE approver_challenges;
DROP TABLE approver_devices;
DROP TABLE approver_enroll_tokens;
DROP TABLE approver_push;
DROP TABLE attestation_hashes;
DROP TABLE audit_log;
DROP TABLE config_generation;
DROP TABLE conversation_sessions;
DROP TABLE conversation_turns;
DROP TABLE credentials;
DROP TABLE devices;
DROP TABLE draft_changes;
DROP TABLE draft_items;
DROP TABLE draft_revisions;
DROP TABLE events_outbox;
DROP TABLE pack_bindings;
DROP TABLE policy_sets;
DROP TABLE revocations;
DROP TABLE role_assignments;
DROP TABLE role_implications;
DROP TABLE sessions;
DROP TABLE settings;
DROP TABLE signing_keys;
DROP TABLE snapshots;
DROP TABLE tool_bindings;
DROP TABLE apps;
DROP TABLE drafts;
DROP TABLE knowledge_packs;
DROP TABLE roles;
DROP TABLE users;
