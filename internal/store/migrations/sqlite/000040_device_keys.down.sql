-- Downgrade: a pre-000040 server reads no device key, so it would take a
-- copied session token of a key-bound device for a bearer one. The move
-- down therefore revokes every key-bound device itself, as the server's
-- own device revocation does: one device revocation row each, origin
-- admin, and the device row deleted. Each such machine enrolls again
-- after the rollback. A bearer device keeps its row and gets no
-- revocation. SQL cannot mint the time-ordered ids the server writes, so
-- each revocation id is a random version 4 UUID, and created_at is UTC
-- RFC3339 text like every other time here. The spent-proof table goes,
-- because only the key lanes use it.
DROP TABLE spent_proofs;
INSERT INTO revocations (id, kind, target_id, reason, origin, created_at)
SELECT lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' ||
        substr(lower(hex(randomblob(2))), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) ||
        substr(lower(hex(randomblob(2))), 2) || '-' || lower(hex(randomblob(6))),
    'device', id, 'device key binding rolled back', 'admin', strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM devices WHERE key_thumbprint <> '';
DELETE FROM devices WHERE key_thumbprint <> '';
ALTER TABLE devices DROP COLUMN signed_in_at;
ALTER TABLE devices DROP COLUMN key_counter;
ALTER TABLE devices DROP COLUMN machine_binding;
ALTER TABLE devices DROP COLUMN custody;
ALTER TABLE devices DROP COLUMN key_thumbprint;
ALTER TABLE devices DROP COLUMN key_jwk;
