-- Device keys: a device can be enrolled with a key that never leaves its
-- machine. A device enrolled without a key keeps every column at its
-- default. See the sqlite twin for what each column holds. custody is
-- text, not jsonb, so both drivers read back the bytes the store wrote.
ALTER TABLE devices
    ADD COLUMN key_jwk text DEFAULT ''::text NOT NULL,
    ADD COLUMN key_thumbprint text DEFAULT ''::text NOT NULL,
    ADD COLUMN custody text DEFAULT ''::text NOT NULL,
    ADD COLUMN machine_binding text DEFAULT ''::text NOT NULL,
    ADD COLUMN key_counter bigint DEFAULT 0 NOT NULL,
    ADD COLUMN signed_in_at timestamp with time zone;

-- One row per DPoP proof a key lane accepted, kept until its nonce can no
-- longer pass, so a proof replayed on another replica is refused. The
-- index serves the sweep of the rows whose time has passed.
CREATE TABLE spent_proofs (
    key text PRIMARY KEY,
    expires_at timestamp with time zone NOT NULL
);
CREATE INDEX idx_spent_proofs_expires ON spent_proofs USING btree (expires_at);
