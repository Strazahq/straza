-- Drops every table the baseline creates, children before their parents.
-- The test harness uses it to empty a database. strazad migrate never
-- reaches it, because no version below 37 exists to move to.

DROP TABLE settings;
DROP TABLE role_implications;
DROP TABLE devices;
DROP TABLE sessions;
DROP TABLE pack_bindings;
DROP TABLE tool_bindings;
DROP TABLE policy_sets;
DROP TABLE snapshots;
DROP TABLE events_outbox;
DROP TABLE audit_log;
DROP TABLE revocations;
DROP TABLE attestation_hashes;
DROP TABLE conversation_turns;
DROP TABLE approvals;
DROP TABLE approver_devices;
DROP TABLE approver_enroll_tokens;
DROP TABLE approver_challenges;
DROP TABLE approver_push;
DROP TABLE conversation_sessions;
DROP TABLE role_assignments;
DROP TABLE credentials;
DROP TABLE signing_keys;
DROP TABLE draft_items;
DROP TABLE draft_revisions;
DROP TABLE draft_changes;
DROP TABLE config_generation;
DROP TABLE users;
DROP TABLE knowledge_packs;
DROP TABLE apps;
DROP TABLE roles;
DROP TABLE drafts;
