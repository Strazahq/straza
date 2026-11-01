-- Object lists (object-scoped roles, slice 1): a fourth
-- credential scope, "listing", and the two tables that hold each server's
-- object list. See the sqlite twin for the semantics.
ALTER TABLE credentials DROP CONSTRAINT credentials_scope_check;
ALTER TABLE credentials ADD CONSTRAINT credentials_scope_check CHECK (scope IN ('role', 'user', 'app', 'listing'));

CREATE TABLE object_lists (
    app_id text PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
    type text NOT NULL,
    decl_hash text NOT NULL,
    version bigint DEFAULT 0 NOT NULL,
    list_hash text DEFAULT ''::text NOT NULL,
    object_count integer DEFAULT 0 NOT NULL,
    joined_count integer DEFAULT 0 NOT NULL,
    left_count integer DEFAULT 0 NOT NULL,
    outcome text DEFAULT 'never'::text NOT NULL CHECK (outcome IN ('never', 'ok', 'failed')),
    reason text DEFAULT ''::text NOT NULL,
    listed_at timestamp with time zone,
    attempted_at timestamp with time zone,
    due_at timestamp with time zone NOT NULL,
    claim_owner text DEFAULT ''::text NOT NULL,
    claim_until timestamp with time zone
);

CREATE TABLE objects (
    app_id text NOT NULL REFERENCES object_lists(app_id) ON DELETE CASCADE,
    id text NOT NULL,
    value text NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    attributes jsonb DEFAULT '{}'::jsonb NOT NULL,
    added_version bigint NOT NULL,
    PRIMARY KEY (app_id, id),
    UNIQUE (app_id, value)
);
