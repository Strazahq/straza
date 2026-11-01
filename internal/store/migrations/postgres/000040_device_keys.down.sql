-- Downgrade: the move down revokes every key-bound device itself, one
-- device revocation row of origin admin each and the device row deleted,
-- so the older server never takes a copied key-bound token for a bearer
-- one. A bearer device keeps its row. Each revocation id is a random
-- UUID. The spent-proof table goes too. See the sqlite twin.
DROP TABLE spent_proofs;
INSERT INTO revocations (id, kind, target_id, reason, origin, created_at)
SELECT gen_random_uuid()::text, 'device', id, 'device key binding rolled back', 'admin', now()
FROM devices WHERE key_thumbprint <> '';
DELETE FROM devices WHERE key_thumbprint <> '';
ALTER TABLE devices
    DROP COLUMN signed_in_at,
    DROP COLUMN key_counter,
    DROP COLUMN machine_binding,
    DROP COLUMN custody,
    DROP COLUMN key_thumbprint,
    DROP COLUMN key_jwk;
