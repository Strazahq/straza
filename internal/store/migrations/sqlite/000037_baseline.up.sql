-- The whole schema of a Straza store at version 37, the oldest version this
-- strazad upgrades from. A new store is created by this file alone.

CREATE TABLE settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE users (
    id TEXT PRIMARY KEY,
    external_id TEXT NOT NULL DEFAULT '',
    username TEXT NOT NULL UNIQUE,
    email TEXT NOT NULL DEFAULT '',
    display TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    origin TEXT NOT NULL DEFAULT 'local' CHECK (origin IN ('local', 'scim')),
    password_hash TEXT NOT NULL DEFAULT '',
    attrs TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    deleted_at TEXT,
    user_type TEXT NOT NULL DEFAULT '',
    agency_mode TEXT NOT NULL DEFAULT '',
    sponsor TEXT NOT NULL DEFAULT '',
    swarm_id TEXT NOT NULL DEFAULT '',
    ephemeral INTEGER NOT NULL DEFAULT 0,
    title TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_users_external_id ON users(external_id) WHERE external_id <> '';

CREATE TABLE role_implications (
    role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    implies_role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, implies_role_id),
    CHECK (role_id <> implies_role_id)
);

CREATE TABLE devices (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL DEFAULT '',
    fingerprint TEXT NOT NULL DEFAULT '',
    platform TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    enrolled_at TEXT NOT NULL,
    client_kind TEXT NOT NULL DEFAULT '' CHECK (client_kind IN ('', 'kit', 'human'))
);
CREATE INDEX idx_devices_user ON devices(user_id);

CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_id TEXT NOT NULL DEFAULT '',
    harness_name TEXT NOT NULL DEFAULT '',
    harness_version TEXT NOT NULL DEFAULT '',
    attestation_level TEXT NOT NULL DEFAULT 'none' CHECK (attestation_level IN ('managed', 'advisory', 'none')),
    attestation_hashes TEXT NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked', 'closed')),
    started_at TEXT NOT NULL,
    last_seen TEXT NOT NULL,
    client_version TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_sessions_status ON sessions(status);

CREATE TABLE knowledge_packs (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    version TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL DEFAULT '',
    checksum TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE pack_bindings (
    role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    pack_id TEXT NOT NULL REFERENCES knowledge_packs(id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, pack_id)
);

CREATE TABLE apps (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    version TEXT NOT NULL DEFAULT '',
    manifest TEXT NOT NULL DEFAULT '{}',
    runtime_kind TEXT NOT NULL CHECK (runtime_kind IN ('oci', 'command', 'remote')),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'starting', 'running', 'degraded', 'stopped', 'failed')),
    source TEXT NOT NULL DEFAULT 'api' CHECK (source IN ('api', 'gitops', 'registry')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    deleted_at TEXT,
    admin_role_id TEXT
);
CREATE INDEX idx_apps_admin_role ON apps(admin_role_id);

CREATE TABLE tool_bindings (
    id TEXT PRIMARY KEY,
    role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    tool_matcher TEXT NOT NULL DEFAULT '[]',
    effect TEXT NOT NULL DEFAULT 'allow' CHECK (effect = 'allow'),
    created_at TEXT NOT NULL
);
CREATE INDEX idx_tool_bindings_app ON tool_bindings(app_id);
CREATE UNIQUE INDEX idx_tool_bindings_role_app ON tool_bindings (role_id, app_id);
CREATE UNIQUE INDEX idx_tool_bindings_role ON tool_bindings (role_id);

CREATE TABLE policy_sets (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    priority INTEGER NOT NULL DEFAULT 0,
    yaml_source TEXT NOT NULL DEFAULT '',
    compiled_hash TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE snapshots (
    id TEXT PRIMARY KEY,
    signer_key_id TEXT NOT NULL DEFAULT '',
    size INTEGER NOT NULL DEFAULT 0,
    blob BLOB NOT NULL,
    active INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);

CREATE TABLE events_outbox (
    id TEXT PRIMARY KEY,
    subject TEXT NOT NULL,
    ce TEXT NOT NULL,
    published INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_outbox_unpublished ON events_outbox(published, created_at);

CREATE TABLE audit_log (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    ce TEXT NOT NULL,
    prev_hash TEXT NOT NULL,
    hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    ce_id TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_audit_ce_id ON audit_log(ce_id) WHERE ce_id <> '';

CREATE TABLE revocations (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('user', 'session', 'device', 'jti')),
    target_id TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    origin TEXT NOT NULL DEFAULT 'scim'
);
CREATE INDEX idx_revocations_target ON revocations(kind, target_id);
CREATE INDEX idx_revocations_created ON revocations(created_at);

CREATE TABLE attestation_hashes (
    id TEXT PRIMARY KEY,
    artifact TEXT NOT NULL,
    harness TEXT NOT NULL DEFAULT '',
    platform TEXT NOT NULL DEFAULT '',
    hash TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE (artifact, harness, platform, hash)
);
CREATE INDEX idx_attestation_hashes_artifact ON attestation_hashes(artifact);

CREATE TABLE conversation_turns (
    id TEXT PRIMARY KEY,
    ce_id TEXT NOT NULL UNIQUE,
    session_id TEXT NOT NULL,
    user_id TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL,
    mode TEXT NOT NULL DEFAULT 'verbatim',
    content TEXT NOT NULL,
    truncated INTEGER NOT NULL DEFAULT 0,
    content_hash TEXT NOT NULL DEFAULT '',
    at TEXT NOT NULL,
    agent_type TEXT NOT NULL DEFAULT '',
    body_external INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_conversation_turns_session ON conversation_turns(session_id, at);
CREATE INDEX idx_conversation_turns_at ON conversation_turns(at);
CREATE INDEX idx_conversation_turns_hash ON conversation_turns(content_hash);
CREATE INDEX idx_conversation_turns_user ON conversation_turns(user_id, at DESC);

CREATE TABLE approvals (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    user_id TEXT NOT NULL DEFAULT '',
    username TEXT NOT NULL DEFAULT '',
    rule_id TEXT NOT NULL DEFAULT '',
    set_name TEXT NOT NULL DEFAULT '',
    argv_hash TEXT NOT NULL DEFAULT '',
    lane TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    justification TEXT NOT NULL DEFAULT '',
    approver_roles TEXT NOT NULL DEFAULT '[]',
    self_approval INTEGER NOT NULL DEFAULT 0,
    timeout_seconds INTEGER NOT NULL DEFAULT 0,
    retry_ttl_seconds INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL DEFAULT 'pending',
    decided_by TEXT NOT NULL DEFAULT '',
    decided_by_name TEXT NOT NULL DEFAULT '',
    channel TEXT NOT NULL DEFAULT '',
    channel_refs TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    decided_at TEXT,
    class TEXT NOT NULL DEFAULT 'hold',
    consumed_at TEXT,
    consumed_by TEXT,
    grant_expires_at TEXT,
    grant_ttl_seconds INTEGER NOT NULL DEFAULT 0,
    args_preview TEXT NOT NULL DEFAULT '',
    args_truncated INTEGER NOT NULL DEFAULT 0,
    args_bytes INTEGER NOT NULL DEFAULT 0,
    notify TEXT NOT NULL DEFAULT '',
    mode TEXT NOT NULL DEFAULT 'approve' CHECK (mode IN ('approve', 'confirm')),
    decided_reason TEXT NOT NULL DEFAULT '',
    decided_device_id TEXT NOT NULL DEFAULT '',
    approver_users TEXT NOT NULL DEFAULT '[]'
);
CREATE UNIQUE INDEX idx_approvals_pending_key ON approvals(session_id, rule_id, argv_hash)
    WHERE state = 'pending';
CREATE INDEX idx_approvals_state_expires ON approvals(state, expires_at);
CREATE INDEX idx_approvals_created ON approvals(created_at);
CREATE UNIQUE INDEX idx_approvals_pending_ticket_user ON approvals(user_id, rule_id, argv_hash)
    WHERE state = 'pending' AND class = 'ticket';
CREATE INDEX idx_approvals_grant_consume ON approvals(user_id, argv_hash) WHERE state = 'approved';
CREATE INDEX idx_approvals_user ON approvals (user_id, id);

CREATE TABLE approver_devices (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    platform TEXT NOT NULL DEFAULT '',
    key_alg TEXT NOT NULL DEFAULT '',
    public_key TEXT NOT NULL DEFAULT '',
    key_security_level TEXT NOT NULL DEFAULT '',
    attestation_kind TEXT NOT NULL DEFAULT 'none',
    attestation_blob TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    last_seen_at TEXT
);
CREATE INDEX idx_approver_devices_user ON approver_devices(user_id);

CREATE TABLE approver_enroll_tokens (
    id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    user_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at TEXT,
    channel TEXT NOT NULL DEFAULT ''
);

CREATE TABLE approver_challenges (
    challenge TEXT PRIMARY KEY,
    device_id TEXT NOT NULL,
    approval_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    used_at TEXT
);
CREATE INDEX idx_approver_challenges_expires ON approver_challenges(expires_at);

CREATE TABLE approver_push (
    id TEXT PRIMARY KEY,
    device_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    token_or_endpoint TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (device_id, kind, token_or_endpoint)
);
CREATE INDEX idx_approver_push_device ON approver_push(device_id);

CREATE TABLE conversation_sessions (
    session_id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL DEFAULT '',
    turns INTEGER NOT NULL DEFAULT 0,
    first_at TEXT NOT NULL,
    last_at TEXT NOT NULL,
    preview TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_conversation_sessions_last ON conversation_sessions(last_at DESC);

CREATE TABLE "role_assignments" (
    id TEXT PRIMARY KEY,
    subject_kind TEXT NOT NULL CHECK (subject_kind IN ('user')),
    subject_id TEXT NOT NULL,
    role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    valid_from TEXT,
    valid_to TEXT,
    origin TEXT NOT NULL DEFAULT 'admin' CHECK (origin IN ('scim', 'admin')),
    created_at TEXT NOT NULL,
    UNIQUE (subject_kind, subject_id, role_id)
);
CREATE INDEX idx_role_assignments_subject ON role_assignments(subject_kind, subject_id);

CREATE TABLE "roles" (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT 'business' CHECK (kind IN ('business', 'application', 'approver')),
    plane TEXT NOT NULL DEFAULT 'access' CHECK (plane IN ('access', 'control')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    owner_app_id TEXT
);
CREATE INDEX idx_roles_owner_app ON roles(owner_app_id);

CREATE TABLE "credentials" (
    id TEXT PRIMARY KEY,
    app_id TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    scope TEXT NOT NULL CHECK (scope IN ('role', 'user', 'app')),
    owner_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('static', 'oauth', 'token')),
    enc_payload BLOB NOT NULL,
    oauth_meta TEXT NOT NULL DEFAULT '{}',
    rotated_at TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_credentials_app ON credentials(app_id);

CREATE TABLE "signing_keys" (
    kid TEXT PRIMARY KEY,
    purpose TEXT NOT NULL CHECK (purpose IN ('session', 'snapshot', 'client_assertion')),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('staged', 'active', 'retiring', 'retired')),
    private_key BLOB NOT NULL,
    public_key BLOB NOT NULL,
    created_at TEXT NOT NULL,
    rotated_at TEXT
);
CREATE INDEX idx_signing_keys_purpose ON signing_keys(purpose, status);
CREATE UNIQUE INDEX idx_signing_keys_client_assertion_staged ON signing_keys(purpose)
    WHERE purpose = 'client_assertion' AND status = 'staged';
CREATE UNIQUE INDEX idx_signing_keys_client_assertion_active ON signing_keys(purpose)
    WHERE purpose = 'client_assertion' AND status = 'active';
CREATE UNIQUE INDEX idx_signing_keys_session_active ON signing_keys(purpose)
    WHERE purpose = 'session' AND status = 'active';
CREATE UNIQUE INDEX idx_signing_keys_snapshot_active ON signing_keys(purpose)
    WHERE purpose = 'snapshot' AND status = 'active';

CREATE TABLE drafts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    revision INTEGER NOT NULL DEFAULT 1,
    state TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'published', 'discarded', 'expired')),
    door TEXT NOT NULL CHECK (door IN ('console', 'strazactl', 'straza-app', 'apps-directory', 'api')),
    source TEXT NOT NULL DEFAULT '',
    source_hash TEXT NOT NULL DEFAULT '',
    slot TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    refusal TEXT NOT NULL DEFAULT '',
    reverts INTEGER,
    proposer_id TEXT NOT NULL DEFAULT '',
    proposer_name TEXT NOT NULL DEFAULT '',
    proposer_agent INTEGER NOT NULL DEFAULT 0,
    sponsor_id TEXT NOT NULL DEFAULT '',
    sponsor_name TEXT NOT NULL DEFAULT '',
    checked_revision INTEGER NOT NULL DEFAULT 0,
    checked_at TEXT,
    checked_snapshot TEXT NOT NULL DEFAULT '',
    check_counts TEXT NOT NULL DEFAULT '',
    agent_verdict TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    expires_at TEXT,
    decided_at TEXT,
    decided_by_id TEXT NOT NULL DEFAULT '',
    decided_by_name TEXT NOT NULL DEFAULT '',
    decided_via TEXT NOT NULL DEFAULT '',
    decided_client TEXT NOT NULL DEFAULT '',
    decided_reason TEXT NOT NULL DEFAULT '',
    published_snapshot TEXT NOT NULL DEFAULT '',
    acks TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_drafts_state ON drafts(state, id);
CREATE INDEX idx_drafts_open_proposer ON drafts(proposer_id) WHERE state = 'open';
CREATE UNIQUE INDEX idx_drafts_open_source ON drafts(source, source_hash)
    WHERE state = 'open' AND source <> '';
CREATE INDEX idx_drafts_source ON drafts(source) WHERE source <> '';
CREATE UNIQUE INDEX idx_drafts_open_slot ON drafts(slot) WHERE state = 'open' AND slot <> '';
CREATE INDEX idx_drafts_open_expiry ON drafts(expires_at)
    WHERE state = 'open' AND expires_at IS NOT NULL;

CREATE TABLE draft_items (
    draft_id INTEGER NOT NULL REFERENCES drafts(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('App', 'Role', 'PolicySet')),
    name TEXT NOT NULL,
    op TEXT NOT NULL CHECK (op IN ('put', 'off', 'remove')),
    doc TEXT NOT NULL DEFAULT '',
    base TEXT NOT NULL DEFAULT '',
    base_op TEXT NOT NULL DEFAULT '' CHECK (base_op IN ('', 'put', 'off', 'remove')),
    base_doc TEXT NOT NULL DEFAULT '',
    offered TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (draft_id, kind, name)
);
CREATE INDEX idx_draft_items_object ON draft_items(kind, name);

CREATE TABLE draft_revisions (
    draft_id INTEGER NOT NULL REFERENCES drafts(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL,
    author_id TEXT NOT NULL DEFAULT '',
    author_name TEXT NOT NULL DEFAULT '',
    author_agent INTEGER NOT NULL DEFAULT 0,
    author_via TEXT NOT NULL DEFAULT '',
    author_client TEXT NOT NULL DEFAULT '',
    sponsor_id TEXT NOT NULL DEFAULT '',
    sponsor_name TEXT NOT NULL DEFAULT '',
    door TEXT NOT NULL,
    digest TEXT NOT NULL,
    mechanical INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    PRIMARY KEY (draft_id, revision)
);
CREATE INDEX idx_draft_revisions_author ON draft_revisions(author_id) WHERE author_id <> '';

CREATE TABLE draft_changes (
    draft_id INTEGER NOT NULL REFERENCES drafts(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('App', 'Role', 'PolicySet')),
    name TEXT NOT NULL,
    implied INTEGER NOT NULL DEFAULT 0,
    before_op TEXT NOT NULL CHECK (before_op IN ('put', 'off', 'remove')),
    before_doc TEXT NOT NULL DEFAULT '',
    before_fp TEXT NOT NULL DEFAULT '',
    after_op TEXT NOT NULL CHECK (after_op IN ('put', 'off', 'remove')),
    after_doc TEXT NOT NULL DEFAULT '',
    after_fp TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (draft_id, seq)
);
CREATE INDEX idx_draft_changes_object ON draft_changes(kind, name);

CREATE TABLE config_generation (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    generation INTEGER NOT NULL
);
INSERT INTO config_generation (id, generation) VALUES (1, 0);
