# Session Token, v1beta1

Status: **beta**. It began as v1alpha1, and the move to beta changed no
wire format. The reference implementation is `internal/authn`.

Revision (2026-07-21): the JWKS discovery path `/.well-known/straza/`, the
issuer `strazad` and the `straza.revocation.*` subjects replace the
pre-release names; token shape and claims unchanged.

Revision (2026-09-07): key rotation gains the `staged` status and the JWKS
carries staged keys (see Refresh & revocation); token shape and claims
unchanged.

Straza session tokens are EdDSA (ed25519) JWTs, TTL **300 s**, verified
locally against the JWKS at `/.well-known/straza/jwks.json` (invariant:
self-validating tokens + push revocation; verifiers never call home per
request; see https://docs.straza.ai/security/security-model/).

## Claims

| Claim | Meaning |
|---|---|
| `iss` | strazad public URL |
| `sub` | user id (UUIDv7) |
| `ses` | session id, **present on every session token**; its absence distinguishes ID tokens from session tokens |
| `dev` | device id, empty when no device factor |
| `hrn` | harness `<name>/<version>` |
| `att` | attestation level `managed\|advisory\|none` |
| `rol` | hex sha256/128 over the sorted resolved role ids |
| `snp` | active policy snapshot id (content hash) |
| `jti`, `iat`, `exp` | standard |

## Refresh & revocation

- Refresh via `POST /v1/checkin` with `session_token`. Servers MUST verify
  signature/issuer and confirm the session row is `active`; expiry MAY be
  waived on the refresh path only (proof-of-possession; the session status
  is authoritative). This keeps idle harness sessions resumable.
- Revocation travels as `straza.revocation.*` events into in-memory
  denylists keyed by `ses`/`sub`/`dev`/`jti`. Enforcement points MUST check
  their denylist on every verify; they MUST NOT query the database.
- Key rotation: staged → active → retiring (still verifies) → retired. JWKS
  carries staged + active + retiring public keys, never a retired one. A
  staged key signs nothing; it exists so that every replica and every JWKS
  consumer holds the public key before any replica signs with it, which is
  what makes a rotation safe on a multi-replica deployment. A retiring key
  is retired only after every credential it could have signed has expired.

Changes follow the additive-change rule in the spec README.
