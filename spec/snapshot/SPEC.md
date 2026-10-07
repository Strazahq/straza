# Policy Snapshot envelope, v1 (descriptive backfill)

Status: **descriptive** (written 2026-08-09). The format has been
`format: 1` since it was first built, and this document records it as built;
no wire change accompanies the backfill. A JSON Schema and conformance
fixtures are owed at the FIRST format bump (the envelope is binary CBOR, and
retro-fixtures without a format change would pin bytes no consumer needs to
re-implement from here); until then this prose is the source of truth
outside the Go code (`internal/policy/snapshot.go`).

## What this is

The policy snapshot is the only policy distribution format: the
platform compiles every active PolicySet into one signed, content-addressed
blob; every PDP (server, gateway, agent-side) evaluates in memory against a
verified snapshot and never reads the DB on a decision path.

## Envelope (CBOR)

| Field | Meaning |
|---|---|
| `format` | Envelope format version, `1`. |
| `payload` | CBOR bytes of the payload below; the signature covers exactly these bytes. |
| `keyId` | Snapshot signing key id (`/.well-known/straza/snapshot-keys.json`). |
| `sig` | ed25519 signature over `payload` (raw, no domain prefix; the harness-config artifact added domain separation for every LATER use of this key, see spec/harness-config). |

Payload:

| Field | Meaning |
|---|---|
| `documents` | Canonical PolicySet YAML documents, sorted by `metadata.name` for byte determinism. |
| `localDefault` | Profile default effect for local tools (`allow` \| `deny`). |
| `maxAgeSecs` | Offline grace bound: past `session expiry + maxAgeSecs` a client fails closed. |
| `createdUnix` | Compile time (staleness surfacing). |

The snapshot id is the sha256 hex of the SIGNED envelope bytes: signing is
deterministic (ed25519), so identical policy state reproduces identical
bytes and id.

## Distribution and verification

- `GET /v1/snapshot`: `application/cbor`, ETag = `"<id>"`, `If-None-Match`
  answers 304, `X-Straza-Snapshot-Id` header. In the enterprise profile the
  request MUST carry the session token issued at check-in as a bearer. A
  request without a valid one is refused with 401 before any 304 or 503, so
  this route tells such a caller neither the active id nor whether a
  snapshot exists. The guarantee covers this route only: `/metrics` counts
  its answers by status, a 503 included, and answers without a token unless
  the operator sets `server.metricsToken`. The standalone profile serves the
  snapshot without a token. The snapshot carries no secrets, and every
  enrolled client holds a copy.
- Verification keys ride `/.well-known/straza/snapshot-keys.json`; clients
  pin them at enroll and MUST verify id equality, key resolution, and the
  ed25519 signature before use; any failure is fail-closed (a client MUST
  refuse to enforce an unverifiable snapshot). The platform self-verifies a
  snapshot before activating or serving it.
- The session token's `snp` claim (spec/session-token) and the
  `straza.policy.updated` / `straza.push.policy` events (spec/events §5)
  carry the id, never the blob.

## Changelog

- **v1 descriptive backfill** (2026-08-09): as-built documentation; no wire
  change; schema + fixtures deferred to the first format bump, reason above.
- **Enterprise session gate** (2026-09-27): in the enterprise profile
  `GET /v1/snapshot` requires the check-in session token and answers 401
  without one. The envelope is unchanged. This makes a request header
  required, which the additive-only rule forbids, and it is a stated
  exception because it lands before the first public version.
