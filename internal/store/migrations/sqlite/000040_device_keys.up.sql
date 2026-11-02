-- Device keys: a device can be enrolled with a key that never leaves its
-- machine, so a copied credential works nowhere else. A device enrolled
-- without a key keeps every one of these columns at its default.
--
-- key_jwk is the public key as JWK JSON and key_thumbprint its RFC 7638
-- thumbprint, which every session token minted from the key carries.
-- custody is JSON text that says how the client holds the key: kind, and
-- the platform and its version. machine_binding is the client's hash of
-- the machine's identity. key_counter is the highest check-in counter the
-- server accepted, so a copy of the key shows up as a counter that goes
-- backwards. signed_in_at is the last identity provider sign-in that
-- enrolled the key, NULL for a device enrolled without one, and a sign-in
-- deadline counts from it.
ALTER TABLE devices ADD COLUMN key_jwk TEXT NOT NULL DEFAULT '';
ALTER TABLE devices ADD COLUMN key_thumbprint TEXT NOT NULL DEFAULT '';
ALTER TABLE devices ADD COLUMN custody TEXT NOT NULL DEFAULT '';
ALTER TABLE devices ADD COLUMN machine_binding TEXT NOT NULL DEFAULT '';
ALTER TABLE devices ADD COLUMN key_counter INTEGER NOT NULL DEFAULT 0;
ALTER TABLE devices ADD COLUMN signed_in_at TEXT;

-- One row per DPoP proof a key lane accepted, keyed by the key's thumbprint
-- and the proof id joined by a space, and kept until its nonce can no
-- longer pass, so a proof replayed on another replica is refused. The
-- sweep of passed rows compares times through julianday, which a plain
-- index cannot serve, and the table holds a few minutes of proofs, so it
-- has no index.
CREATE TABLE spent_proofs (
    key TEXT PRIMARY KEY,
    expires_at TEXT NOT NULL
);
