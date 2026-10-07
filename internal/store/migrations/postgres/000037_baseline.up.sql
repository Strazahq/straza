-- The whole schema of a Straza store at version 37, the oldest version this
-- strazad upgrades from. A new store is created by this file alone.

CREATE TABLE approvals (
    id text NOT NULL,
    session_id text NOT NULL,
    user_id text DEFAULT ''::text NOT NULL,
    username text DEFAULT ''::text NOT NULL,
    rule_id text DEFAULT ''::text NOT NULL,
    set_name text DEFAULT ''::text NOT NULL,
    argv_hash text DEFAULT ''::text NOT NULL,
    lane text DEFAULT ''::text NOT NULL,
    summary text DEFAULT ''::text NOT NULL,
    justification text DEFAULT ''::text NOT NULL,
    approver_roles text DEFAULT '[]'::text NOT NULL,
    self_approval boolean DEFAULT false NOT NULL,
    timeout_seconds integer DEFAULT 0 NOT NULL,
    retry_ttl_seconds integer DEFAULT 0 NOT NULL,
    state text DEFAULT 'pending'::text NOT NULL,
    decided_by text DEFAULT ''::text NOT NULL,
    decided_by_name text DEFAULT ''::text NOT NULL,
    channel text DEFAULT ''::text NOT NULL,
    channel_refs text DEFAULT '{}'::text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    decided_at timestamp with time zone,
    class text DEFAULT 'hold'::text NOT NULL,
    consumed_at timestamp with time zone,
    consumed_by text,
    grant_expires_at timestamp with time zone,
    grant_ttl_seconds integer DEFAULT 0 NOT NULL,
    args_preview text DEFAULT ''::text NOT NULL,
    args_truncated boolean DEFAULT false NOT NULL,
    args_bytes integer DEFAULT 0 NOT NULL,
    notify text DEFAULT ''::text NOT NULL,
    mode text DEFAULT 'approve'::text NOT NULL,
    decided_reason text DEFAULT ''::text NOT NULL,
    decided_device_id text DEFAULT ''::text NOT NULL,
    approver_users text DEFAULT '[]'::text NOT NULL,
    CONSTRAINT approvals_mode_check CHECK ((mode = ANY (ARRAY['approve'::text, 'confirm'::text])))
);

CREATE TABLE approver_challenges (
    challenge text NOT NULL,
    device_id text NOT NULL,
    approval_id text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    used_at timestamp with time zone
);

CREATE TABLE approver_devices (
    id text NOT NULL,
    user_id text NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    platform text DEFAULT ''::text NOT NULL,
    key_alg text DEFAULT ''::text NOT NULL,
    public_key text DEFAULT ''::text NOT NULL,
    key_security_level text DEFAULT ''::text NOT NULL,
    attestation_kind text DEFAULT 'none'::text NOT NULL,
    attestation_blob text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    last_seen_at timestamp with time zone
);

CREATE TABLE approver_enroll_tokens (
    id text NOT NULL,
    token_hash text NOT NULL,
    user_id text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    used_at timestamp with time zone,
    channel text DEFAULT ''::text NOT NULL
);

CREATE TABLE approver_push (
    id text NOT NULL,
    device_id text NOT NULL,
    kind text NOT NULL,
    token_or_endpoint text NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE apps (
    id text NOT NULL,
    name text NOT NULL,
    version text DEFAULT ''::text NOT NULL,
    manifest jsonb DEFAULT '{}'::jsonb NOT NULL,
    runtime_kind text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    source text DEFAULT 'api'::text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone,
    admin_role_id text,
    CONSTRAINT apps_runtime_kind_check CHECK ((runtime_kind = ANY (ARRAY['oci'::text, 'command'::text, 'remote'::text]))),
    CONSTRAINT apps_source_check CHECK ((source = ANY (ARRAY['api'::text, 'gitops'::text, 'registry'::text]))),
    CONSTRAINT apps_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'starting'::text, 'running'::text, 'degraded'::text, 'stopped'::text, 'failed'::text])))
);

CREATE TABLE attestation_hashes (
    id text NOT NULL,
    artifact text NOT NULL,
    harness text DEFAULT ''::text NOT NULL,
    platform text DEFAULT ''::text NOT NULL,
    hash text NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE audit_log (
    seq bigint NOT NULL,
    ce text NOT NULL,
    prev_hash text NOT NULL,
    hash text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    ce_id text DEFAULT ''::text NOT NULL
);

CREATE SEQUENCE audit_log_seq_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE audit_log_seq_seq OWNED BY audit_log.seq;

CREATE TABLE config_generation (
    id integer NOT NULL,
    generation bigint NOT NULL,
    CONSTRAINT config_generation_id_check CHECK ((id = 1))
);

INSERT INTO config_generation (id, generation) VALUES (1, 0);

CREATE TABLE conversation_sessions (
    session_id text NOT NULL,
    user_id text DEFAULT ''::text NOT NULL,
    turns bigint DEFAULT 0 NOT NULL,
    first_at timestamp with time zone NOT NULL,
    last_at timestamp with time zone NOT NULL,
    preview text DEFAULT ''::text NOT NULL
);

CREATE TABLE conversation_turns (
    id text NOT NULL,
    ce_id text NOT NULL,
    session_id text NOT NULL,
    user_id text DEFAULT ''::text NOT NULL,
    kind text NOT NULL,
    mode text DEFAULT 'verbatim'::text NOT NULL,
    content text NOT NULL,
    truncated boolean DEFAULT false NOT NULL,
    content_hash text DEFAULT ''::text NOT NULL,
    at timestamp with time zone NOT NULL,
    agent_type text DEFAULT ''::text NOT NULL,
    body_external boolean DEFAULT false NOT NULL
);

CREATE TABLE credentials (
    id text NOT NULL,
    app_id text NOT NULL,
    scope text NOT NULL,
    owner_id text NOT NULL,
    kind text NOT NULL,
    enc_payload bytea NOT NULL,
    oauth_meta jsonb DEFAULT '{}'::jsonb NOT NULL,
    rotated_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT credentials_kind_check CHECK ((kind = ANY (ARRAY['static'::text, 'oauth'::text, 'token'::text]))),
    CONSTRAINT credentials_scope_check CHECK ((scope = ANY (ARRAY['role'::text, 'user'::text, 'app'::text])))
);

CREATE TABLE devices (
    id text NOT NULL,
    user_id text NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    fingerprint text DEFAULT ''::text NOT NULL,
    platform text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    enrolled_at timestamp with time zone NOT NULL,
    client_kind text DEFAULT ''::text NOT NULL,
    CONSTRAINT devices_client_kind_check CHECK ((client_kind = ANY (ARRAY[''::text, 'kit'::text, 'human'::text]))),
    CONSTRAINT devices_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text])))
);

CREATE TABLE draft_changes (
    draft_id bigint NOT NULL,
    seq integer NOT NULL,
    kind text NOT NULL,
    name text NOT NULL,
    implied boolean DEFAULT false NOT NULL,
    before_op text NOT NULL,
    before_doc text DEFAULT ''::text NOT NULL,
    before_fp text DEFAULT ''::text NOT NULL,
    after_op text NOT NULL,
    after_doc text DEFAULT ''::text NOT NULL,
    after_fp text DEFAULT ''::text NOT NULL,
    CONSTRAINT draft_changes_after_op_check CHECK ((after_op = ANY (ARRAY['put'::text, 'off'::text, 'remove'::text]))),
    CONSTRAINT draft_changes_before_op_check CHECK ((before_op = ANY (ARRAY['put'::text, 'off'::text, 'remove'::text]))),
    CONSTRAINT draft_changes_kind_check CHECK ((kind = ANY (ARRAY['App'::text, 'Role'::text, 'PolicySet'::text])))
);

CREATE TABLE draft_items (
    draft_id bigint NOT NULL,
    seq integer NOT NULL,
    kind text NOT NULL,
    name text NOT NULL,
    op text NOT NULL,
    doc text DEFAULT ''::text NOT NULL,
    base text DEFAULT ''::text NOT NULL,
    base_op text DEFAULT ''::text NOT NULL,
    base_doc text DEFAULT ''::text NOT NULL,
    offered text DEFAULT ''::text NOT NULL,
    CONSTRAINT draft_items_base_op_check CHECK ((base_op = ANY (ARRAY[''::text, 'put'::text, 'off'::text, 'remove'::text]))),
    CONSTRAINT draft_items_kind_check CHECK ((kind = ANY (ARRAY['App'::text, 'Role'::text, 'PolicySet'::text]))),
    CONSTRAINT draft_items_op_check CHECK ((op = ANY (ARRAY['put'::text, 'off'::text, 'remove'::text])))
);

CREATE TABLE draft_revisions (
    draft_id bigint NOT NULL,
    revision integer NOT NULL,
    author_id text DEFAULT ''::text NOT NULL,
    author_name text DEFAULT ''::text NOT NULL,
    author_agent boolean DEFAULT false NOT NULL,
    author_via text DEFAULT ''::text NOT NULL,
    author_client text DEFAULT ''::text NOT NULL,
    sponsor_id text DEFAULT ''::text NOT NULL,
    sponsor_name text DEFAULT ''::text NOT NULL,
    door text NOT NULL,
    digest text NOT NULL,
    mechanical boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE drafts (
    id bigint NOT NULL,
    revision integer DEFAULT 1 NOT NULL,
    state text DEFAULT 'open'::text NOT NULL,
    door text NOT NULL,
    source text DEFAULT ''::text NOT NULL,
    source_hash text DEFAULT ''::text NOT NULL,
    slot text DEFAULT ''::text NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    refusal text DEFAULT ''::text NOT NULL,
    reverts bigint,
    proposer_id text DEFAULT ''::text NOT NULL,
    proposer_name text DEFAULT ''::text NOT NULL,
    proposer_agent boolean DEFAULT false NOT NULL,
    sponsor_id text DEFAULT ''::text NOT NULL,
    sponsor_name text DEFAULT ''::text NOT NULL,
    checked_revision integer DEFAULT 0 NOT NULL,
    checked_at timestamp with time zone,
    checked_snapshot text DEFAULT ''::text NOT NULL,
    check_counts text DEFAULT ''::text NOT NULL,
    agent_verdict text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone,
    decided_at timestamp with time zone,
    decided_by_id text DEFAULT ''::text NOT NULL,
    decided_by_name text DEFAULT ''::text NOT NULL,
    decided_via text DEFAULT ''::text NOT NULL,
    decided_client text DEFAULT ''::text NOT NULL,
    decided_reason text DEFAULT ''::text NOT NULL,
    published_snapshot text DEFAULT ''::text NOT NULL,
    acks text DEFAULT ''::text NOT NULL,
    CONSTRAINT drafts_door_check CHECK ((door = ANY (ARRAY['console'::text, 'strazactl'::text, 'straza-app'::text, 'apps-directory'::text, 'api'::text]))),
    CONSTRAINT drafts_state_check CHECK ((state = ANY (ARRAY['open'::text, 'published'::text, 'discarded'::text, 'expired'::text])))
);

CREATE SEQUENCE drafts_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE drafts_id_seq OWNED BY drafts.id;

CREATE TABLE events_outbox (
    id text NOT NULL,
    subject text NOT NULL,
    ce text NOT NULL,
    published boolean DEFAULT false NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE knowledge_packs (
    id text NOT NULL,
    name text NOT NULL,
    version text DEFAULT ''::text NOT NULL,
    content text DEFAULT ''::text NOT NULL,
    checksum text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL
);

CREATE TABLE pack_bindings (
    role_id text NOT NULL,
    pack_id text NOT NULL
);

CREATE TABLE policy_sets (
    id text NOT NULL,
    name text NOT NULL,
    priority integer DEFAULT 0 NOT NULL,
    yaml_source text DEFAULT ''::text NOT NULL,
    compiled_hash text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'draft'::text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT policy_sets_status_check CHECK ((status = ANY (ARRAY['draft'::text, 'active'::text])))
);

CREATE TABLE revocations (
    id text NOT NULL,
    kind text NOT NULL,
    target_id text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    origin text DEFAULT 'scim'::text NOT NULL,
    CONSTRAINT revocations_kind_check CHECK ((kind = ANY (ARRAY['user'::text, 'session'::text, 'device'::text, 'jti'::text])))
);

CREATE TABLE role_assignments (
    id text NOT NULL,
    subject_kind text NOT NULL,
    subject_id text NOT NULL,
    role_id text NOT NULL,
    valid_from timestamp with time zone,
    valid_to timestamp with time zone,
    origin text DEFAULT 'admin'::text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT role_assignments_origin_check CHECK ((origin = ANY (ARRAY['scim'::text, 'admin'::text]))),
    CONSTRAINT role_assignments_subject_kind_check CHECK ((subject_kind = 'user'::text))
);

CREATE TABLE role_implications (
    role_id text NOT NULL,
    implies_role_id text NOT NULL,
    CONSTRAINT role_implications_check CHECK ((role_id <> implies_role_id))
);

CREATE TABLE roles (
    id text NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    kind text DEFAULT 'business'::text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    plane text DEFAULT 'access'::text NOT NULL,
    owner_app_id text,
    CONSTRAINT roles_kind_check CHECK ((kind = ANY (ARRAY['business'::text, 'application'::text, 'approver'::text]))),
    CONSTRAINT roles_plane_check CHECK ((plane = ANY (ARRAY['access'::text, 'control'::text])))
);

CREATE TABLE sessions (
    id text NOT NULL,
    user_id text NOT NULL,
    device_id text DEFAULT ''::text NOT NULL,
    harness_name text DEFAULT ''::text NOT NULL,
    harness_version text DEFAULT ''::text NOT NULL,
    attestation_level text DEFAULT 'none'::text NOT NULL,
    attestation_hashes jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    started_at timestamp with time zone NOT NULL,
    last_seen timestamp with time zone NOT NULL,
    client_version text DEFAULT ''::text NOT NULL,
    CONSTRAINT sessions_attestation_level_check CHECK ((attestation_level = ANY (ARRAY['managed'::text, 'advisory'::text, 'none'::text]))),
    CONSTRAINT sessions_status_check CHECK ((status = ANY (ARRAY['active'::text, 'revoked'::text, 'closed'::text])))
);

CREATE TABLE settings (
    key text NOT NULL,
    value text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE signing_keys (
    kid text NOT NULL,
    purpose text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    private_key bytea NOT NULL,
    public_key bytea NOT NULL,
    created_at timestamp with time zone NOT NULL,
    rotated_at timestamp with time zone,
    CONSTRAINT signing_keys_purpose_check CHECK ((purpose = ANY (ARRAY['session'::text, 'snapshot'::text, 'client_assertion'::text]))),
    CONSTRAINT signing_keys_status_check CHECK ((status = ANY (ARRAY['staged'::text, 'active'::text, 'retiring'::text, 'retired'::text])))
);

CREATE TABLE snapshots (
    id text NOT NULL,
    signer_key_id text DEFAULT ''::text NOT NULL,
    size bigint DEFAULT 0 NOT NULL,
    blob bytea NOT NULL,
    active boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone NOT NULL
);

CREATE TABLE tool_bindings (
    id text NOT NULL,
    role_id text NOT NULL,
    app_id text NOT NULL,
    tool_matcher jsonb DEFAULT '[]'::jsonb NOT NULL,
    effect text DEFAULT 'allow'::text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT tool_bindings_effect_check CHECK ((effect = 'allow'::text))
);

CREATE TABLE users (
    id text NOT NULL,
    external_id text DEFAULT ''::text NOT NULL,
    username text NOT NULL,
    email text DEFAULT ''::text NOT NULL,
    display text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    origin text DEFAULT 'local'::text NOT NULL,
    password_hash text DEFAULT ''::text NOT NULL,
    attrs jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    deleted_at timestamp with time zone,
    user_type text DEFAULT ''::text NOT NULL,
    agency_mode text DEFAULT ''::text NOT NULL,
    sponsor text DEFAULT ''::text NOT NULL,
    swarm_id text DEFAULT ''::text NOT NULL,
    ephemeral boolean DEFAULT false NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    CONSTRAINT users_origin_check CHECK ((origin = ANY (ARRAY['local'::text, 'scim'::text]))),
    CONSTRAINT users_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text])))
);

ALTER TABLE ONLY audit_log ALTER COLUMN seq SET DEFAULT nextval('audit_log_seq_seq'::regclass);

ALTER TABLE ONLY drafts ALTER COLUMN id SET DEFAULT nextval('drafts_id_seq'::regclass);

ALTER TABLE ONLY approvals
    ADD CONSTRAINT approvals_pkey PRIMARY KEY (id);

ALTER TABLE ONLY approver_challenges
    ADD CONSTRAINT approver_challenges_pkey PRIMARY KEY (challenge);

ALTER TABLE ONLY approver_devices
    ADD CONSTRAINT approver_devices_pkey PRIMARY KEY (id);

ALTER TABLE ONLY approver_enroll_tokens
    ADD CONSTRAINT approver_enroll_tokens_pkey PRIMARY KEY (id);

ALTER TABLE ONLY approver_enroll_tokens
    ADD CONSTRAINT approver_enroll_tokens_token_hash_key UNIQUE (token_hash);

ALTER TABLE ONLY approver_push
    ADD CONSTRAINT approver_push_device_id_kind_token_or_endpoint_key UNIQUE (device_id, kind, token_or_endpoint);

ALTER TABLE ONLY approver_push
    ADD CONSTRAINT approver_push_pkey PRIMARY KEY (id);

ALTER TABLE ONLY apps
    ADD CONSTRAINT apps_name_key UNIQUE (name);

ALTER TABLE ONLY apps
    ADD CONSTRAINT apps_pkey PRIMARY KEY (id);

ALTER TABLE ONLY attestation_hashes
    ADD CONSTRAINT attestation_hashes_artifact_harness_platform_hash_key UNIQUE (artifact, harness, platform, hash);

ALTER TABLE ONLY attestation_hashes
    ADD CONSTRAINT attestation_hashes_pkey PRIMARY KEY (id);

ALTER TABLE ONLY audit_log
    ADD CONSTRAINT audit_log_pkey PRIMARY KEY (seq);

ALTER TABLE ONLY config_generation
    ADD CONSTRAINT config_generation_pkey PRIMARY KEY (id);

ALTER TABLE ONLY conversation_sessions
    ADD CONSTRAINT conversation_sessions_pkey PRIMARY KEY (session_id);

ALTER TABLE ONLY conversation_turns
    ADD CONSTRAINT conversation_turns_ce_id_key UNIQUE (ce_id);

ALTER TABLE ONLY conversation_turns
    ADD CONSTRAINT conversation_turns_pkey PRIMARY KEY (id);

ALTER TABLE ONLY credentials
    ADD CONSTRAINT credentials_pkey PRIMARY KEY (id);

ALTER TABLE ONLY devices
    ADD CONSTRAINT devices_pkey PRIMARY KEY (id);

ALTER TABLE ONLY draft_changes
    ADD CONSTRAINT draft_changes_pkey PRIMARY KEY (draft_id, seq);

ALTER TABLE ONLY draft_items
    ADD CONSTRAINT draft_items_pkey PRIMARY KEY (draft_id, kind, name);

ALTER TABLE ONLY draft_revisions
    ADD CONSTRAINT draft_revisions_pkey PRIMARY KEY (draft_id, revision);

ALTER TABLE ONLY drafts
    ADD CONSTRAINT drafts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY events_outbox
    ADD CONSTRAINT events_outbox_pkey PRIMARY KEY (id);

ALTER TABLE ONLY knowledge_packs
    ADD CONSTRAINT knowledge_packs_name_key UNIQUE (name);

ALTER TABLE ONLY knowledge_packs
    ADD CONSTRAINT knowledge_packs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY pack_bindings
    ADD CONSTRAINT pack_bindings_pkey PRIMARY KEY (role_id, pack_id);

ALTER TABLE ONLY policy_sets
    ADD CONSTRAINT policy_sets_name_key UNIQUE (name);

ALTER TABLE ONLY policy_sets
    ADD CONSTRAINT policy_sets_pkey PRIMARY KEY (id);

ALTER TABLE ONLY revocations
    ADD CONSTRAINT revocations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY role_assignments
    ADD CONSTRAINT role_assignments_pkey PRIMARY KEY (id);

ALTER TABLE ONLY role_assignments
    ADD CONSTRAINT role_assignments_subject_kind_subject_id_role_id_key UNIQUE (subject_kind, subject_id, role_id);

ALTER TABLE ONLY role_implications
    ADD CONSTRAINT role_implications_pkey PRIMARY KEY (role_id, implies_role_id);

ALTER TABLE ONLY roles
    ADD CONSTRAINT roles_name_key UNIQUE (name);

ALTER TABLE ONLY roles
    ADD CONSTRAINT roles_pkey PRIMARY KEY (id);

ALTER TABLE ONLY sessions
    ADD CONSTRAINT sessions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY settings
    ADD CONSTRAINT settings_pkey PRIMARY KEY (key);

ALTER TABLE ONLY signing_keys
    ADD CONSTRAINT signing_keys_pkey PRIMARY KEY (kid);

ALTER TABLE ONLY snapshots
    ADD CONSTRAINT snapshots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY tool_bindings
    ADD CONSTRAINT tool_bindings_pkey PRIMARY KEY (id);

ALTER TABLE ONLY users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

ALTER TABLE ONLY users
    ADD CONSTRAINT users_username_key UNIQUE (username);

CREATE INDEX idx_approvals_created ON approvals USING btree (created_at);

CREATE INDEX idx_approvals_grant_consume ON approvals USING btree (user_id, argv_hash) WHERE (state = 'approved'::text);

CREATE UNIQUE INDEX idx_approvals_pending_key ON approvals USING btree (session_id, rule_id, argv_hash) WHERE (state = 'pending'::text);

CREATE UNIQUE INDEX idx_approvals_pending_ticket_user ON approvals USING btree (user_id, rule_id, argv_hash) WHERE ((state = 'pending'::text) AND (class = 'ticket'::text));

CREATE INDEX idx_approvals_state_expires ON approvals USING btree (state, expires_at);

CREATE INDEX idx_approvals_user ON approvals USING btree (user_id, id);

CREATE INDEX idx_approver_challenges_expires ON approver_challenges USING btree (expires_at);

CREATE INDEX idx_approver_devices_user ON approver_devices USING btree (user_id);

CREATE INDEX idx_approver_push_device ON approver_push USING btree (device_id);

CREATE INDEX idx_apps_admin_role ON apps USING btree (admin_role_id);

CREATE INDEX idx_attestation_hashes_artifact ON attestation_hashes USING btree (artifact);

CREATE UNIQUE INDEX idx_audit_ce_id ON audit_log USING btree (ce_id) WHERE (ce_id <> ''::text);

CREATE INDEX idx_conversation_sessions_last ON conversation_sessions USING btree (last_at DESC);

CREATE INDEX idx_conversation_turns_at ON conversation_turns USING btree (at);

CREATE INDEX idx_conversation_turns_hash ON conversation_turns USING btree (content_hash);

CREATE INDEX idx_conversation_turns_session ON conversation_turns USING btree (session_id, at);

CREATE INDEX idx_conversation_turns_user ON conversation_turns USING btree (user_id, at DESC);

CREATE INDEX idx_credentials_app ON credentials USING btree (app_id);

CREATE INDEX idx_devices_user ON devices USING btree (user_id);

CREATE INDEX idx_draft_changes_object ON draft_changes USING btree (kind, name);

CREATE INDEX idx_draft_items_object ON draft_items USING btree (kind, name);

CREATE INDEX idx_draft_revisions_author ON draft_revisions USING btree (author_id) WHERE (author_id <> ''::text);

CREATE INDEX idx_drafts_open_expiry ON drafts USING btree (expires_at) WHERE ((state = 'open'::text) AND (expires_at IS NOT NULL));

CREATE INDEX idx_drafts_open_proposer ON drafts USING btree (proposer_id) WHERE (state = 'open'::text);

CREATE UNIQUE INDEX idx_drafts_open_slot ON drafts USING btree (slot) WHERE ((state = 'open'::text) AND (slot <> ''::text));

CREATE UNIQUE INDEX idx_drafts_open_source ON drafts USING btree (source, source_hash) WHERE ((state = 'open'::text) AND (source <> ''::text));

CREATE INDEX idx_drafts_source ON drafts USING btree (source) WHERE (source <> ''::text);

CREATE INDEX idx_drafts_state ON drafts USING btree (state, id);

CREATE INDEX idx_outbox_unpublished ON events_outbox USING btree (published, created_at);

CREATE INDEX idx_revocations_created ON revocations USING btree (created_at);

CREATE INDEX idx_revocations_target ON revocations USING btree (kind, target_id);

CREATE INDEX idx_role_assignments_subject ON role_assignments USING btree (subject_kind, subject_id);

CREATE INDEX idx_roles_owner_app ON roles USING btree (owner_app_id);

CREATE INDEX idx_sessions_status ON sessions USING btree (status);

CREATE INDEX idx_sessions_user ON sessions USING btree (user_id);

CREATE UNIQUE INDEX idx_signing_keys_client_assertion_active ON signing_keys USING btree (purpose) WHERE ((purpose = 'client_assertion'::text) AND (status = 'active'::text));

CREATE UNIQUE INDEX idx_signing_keys_client_assertion_staged ON signing_keys USING btree (purpose) WHERE ((purpose = 'client_assertion'::text) AND (status = 'staged'::text));

CREATE INDEX idx_signing_keys_purpose ON signing_keys USING btree (purpose, status);

CREATE UNIQUE INDEX idx_signing_keys_session_active ON signing_keys USING btree (purpose) WHERE ((purpose = 'session'::text) AND (status = 'active'::text));

CREATE UNIQUE INDEX idx_signing_keys_snapshot_active ON signing_keys USING btree (purpose) WHERE ((purpose = 'snapshot'::text) AND (status = 'active'::text));

CREATE INDEX idx_tool_bindings_app ON tool_bindings USING btree (app_id);

CREATE UNIQUE INDEX idx_tool_bindings_role ON tool_bindings USING btree (role_id);

CREATE UNIQUE INDEX idx_tool_bindings_role_app ON tool_bindings USING btree (role_id, app_id);

CREATE UNIQUE INDEX idx_users_external_id ON users USING btree (external_id) WHERE (external_id <> ''::text);

ALTER TABLE ONLY credentials
    ADD CONSTRAINT credentials_app_id_fkey FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE;

ALTER TABLE ONLY devices
    ADD CONSTRAINT devices_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY draft_changes
    ADD CONSTRAINT draft_changes_draft_id_fkey FOREIGN KEY (draft_id) REFERENCES drafts(id) ON DELETE CASCADE;

ALTER TABLE ONLY draft_items
    ADD CONSTRAINT draft_items_draft_id_fkey FOREIGN KEY (draft_id) REFERENCES drafts(id) ON DELETE CASCADE;

ALTER TABLE ONLY draft_revisions
    ADD CONSTRAINT draft_revisions_draft_id_fkey FOREIGN KEY (draft_id) REFERENCES drafts(id) ON DELETE CASCADE;

ALTER TABLE ONLY pack_bindings
    ADD CONSTRAINT pack_bindings_pack_id_fkey FOREIGN KEY (pack_id) REFERENCES knowledge_packs(id) ON DELETE CASCADE;

ALTER TABLE ONLY pack_bindings
    ADD CONSTRAINT pack_bindings_role_id_fkey FOREIGN KEY (role_id) REFERENCES roles(id) ON DELETE CASCADE;

ALTER TABLE ONLY role_assignments
    ADD CONSTRAINT role_assignments_role_id_fkey FOREIGN KEY (role_id) REFERENCES roles(id) ON DELETE CASCADE;

ALTER TABLE ONLY role_implications
    ADD CONSTRAINT role_implications_implies_role_id_fkey FOREIGN KEY (implies_role_id) REFERENCES roles(id) ON DELETE CASCADE;

ALTER TABLE ONLY role_implications
    ADD CONSTRAINT role_implications_role_id_fkey FOREIGN KEY (role_id) REFERENCES roles(id) ON DELETE CASCADE;

ALTER TABLE ONLY sessions
    ADD CONSTRAINT sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE ONLY tool_bindings
    ADD CONSTRAINT tool_bindings_app_id_fkey FOREIGN KEY (app_id) REFERENCES apps(id) ON DELETE CASCADE;

ALTER TABLE ONLY tool_bindings
    ADD CONSTRAINT tool_bindings_role_id_fkey FOREIGN KEY (role_id) REFERENCES roles(id) ON DELETE CASCADE;
