# Session Token, v1beta1

Status: **beta**. It began as v1alpha1, and the move to beta changed no
wire format. The reference implementation is `internal/authn`.

Revision (2026-07-21): the JWKS discovery path `/.well-known/straza/`, the
issuer `strazad` and the `straza.revocation.*` subjects replace the
pre-release names; token shape and claims unchanged.

Revision (2026-09-07): key rotation gains the `staged` status and the JWKS
carries staged keys (see Refresh & revocation); token shape and claims
unchanged.

Revision (2026-10-20): additive. A session token may carry the `cnf` claim,
which binds it to an Ed25519 key. Such a token is used only under the `DPoP`
scheme with a DPoP proof (RFC 9449) from that key, under the proof and nonce
rules in Key-bound tokens. A key check-in proof also signs the check-in
counter as `ctr`. The proof has a schema, `dpop-proof.schema.json`, and
examples. The refresh rule no longer says that expiry may be waived on
refresh, because no implementation ever waived it. Tokens without `cnf` are
unchanged.

Revision (2026-10-21): additive. An AI agent's session token carries `cnf`
when its check-in carried a valid DPoP proof. The check-in is the ID-token
lane of an AI agent or a service account, which has no device, and its
proof carries no `ath` and no `ctr` (spec/attestation, section Agent proofs
at check-in). Such a token is used like every key-bound token, and its
refresh checks no device row. An agent's check-in without a proof mints a token
without `cnf`, as before.

Revision (2026-10-24): additive. The session key set also signs git grants
(spec/grant), so a resource server verifies a grant against the same JWKS. A
grant carries the header typ `straza-grant+jwt`. A session verifier MUST
refuse a token with that typ, compared without case and with or without an
`application/` prefix, so a grant never opens a session route, even at a site
that forgets to require `ses`. Session tokens are unchanged.

Revision (2026-10-27): additive. An AI agent's ID token from the built-in
issuer's client_credentials grant carries `straza_jkt`, the thumbprint of the
key that verified the agent's client assertion (section Built-in ID tokens).
A verifier MUST refuse an ID token whose `straza_jkt` is present and is not a
non-empty string. A server refuses a stamped token once that key is no longer
registered for the agent, and refuses a check-in proof from another key
(spec/attestation, section Agent proofs at check-in). The examples in
`examples/id-token/` show the claim. ID tokens without the claim, a person's
and an agent's minted before this revision, are unchanged, and session
tokens and the proof examples are unchanged.

Revision (2026-11-04): a proof on key enrollment, key check-in or an AI
agent's ID-token check-in, the lanes whose `jti` the server spends in its
database, is refused at step 7 of the proof checks when its `jti` holds a
NUL character (U+0000). The answer is that lane's proof failure, 401 with
`invalid_dpop_proof` (spec/attestation). A Postgres store cannot hold the
character in text, so on Postgres such a proof passed the checks, its
spend failed, and a key check-in answered 503 as for a store outage. Only
a SQLite store accepted it before. A proof on a request with a key-bound session
token, whose `jti` a replica spends in memory, is unchanged. A straza
client's `jti` is 32 hex characters. This refusal ships as a fix without
a major release. The proof schema and the examples are
unchanged, because the schema describes one proof without its lane.

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
| `cnf` | `{"jkt": "<thumbprint>"}`, present only on a key-bound token: the RFC 7638 SHA-256 thumbprint of the Ed25519 key whose DPoP proof must come with every use. Absent on a bearer token. A verifier MUST refuse a token whose `cnf` is not an object with a non-empty string `jkt` |
| `jti`, `iat`, `exp` | standard |

## Built-in ID tokens

The built-in issuer mints OpenID Connect ID tokens with the same key set:
`iss`, `sub`, `aud`, `iat`, `exp`, `preferred_username` and `email`, plus
`nonce` and `auth_time` when the sign-in sets them. An ID token carries no
`ses`. An AI agent's token from the client_credentials grant adds one claim.

| Claim | Meaning |
|---|---|
| `straza_jkt` | The RFC 7638 SHA-256 thumbprint of the agent's registered Ed25519 key that verified its client assertion, in the form `cnf.jkt` uses. Present only on an AI agent's token from the client_credentials grant. A verifier MUST refuse a token whose `straza_jkt` is present and is not a non-empty string, and MUST NOT read such a claim as absent |

The claim is not `cnf`, because the ID token is presented under `Bearer`, at
`GET /v1/self` for one, and a verifier refuses a `cnf` token under `Bearer`
(section Key-bound tokens). The stamp does not bind the ID token to its key:
whoever holds the token can present it until it expires or the key changes.
It lets the server tell which key a token was issued for. A server that
accepts a built-in ID token MUST compare a present `straza_jkt` with the
thumbprint of the key registered for the token's subject when it reads the
token, and MUST refuse the token with 401 when no key is registered or the
key is another. A server whose store does not answer that read answers 503
and does not refuse the token. An ID token from an external identity provider is
never compared, because its issuer registers no key with Straza.

`examples/id-token/` holds three valid and three invalid ID tokens signed at
a fixed time with keys from fixed seeds. Each file names its verifier's
issuer and audience and the public key that verifies it, and an invalid file
states the rule it breaks.

## Refresh & revocation

- Refresh via `POST /v1/checkin` with `session_token`. Servers MUST verify
  signature, issuer and expiry, with 30 s of clock skew, and confirm the
  session row is `active`. A key-bound token refreshes only with a proof
  whose `htu` is the server's `/v1/checkin` and whose `ath` hashes the
  `session_token` in the body. The new token carries the same `cnf.jkt`.
  The server refuses the refresh when the device was enrolled again with
  another key, or when the person's sign-in deadline has passed. A client
  that enrolled a new key drops the sessions minted under the old key
  instead of refreshing them, because a refresh signed by the new key does
  not match the old token's `cnf.jkt` and is refused as a proof failure,
  with a proof failure audit record. A key-bound token of an AI agent names
  no device, so its refresh checks no device row.
- Revocation travels as `straza.revocation.*` events into in-memory
  denylists keyed by `ses`/`sub`/`dev`/`jti`. Enforcement points MUST check
  their denylist on every verify; they MUST NOT query the database.
- Key rotation: staged → active → retiring (still verifies) → retired. JWKS
  carries staged + active + retiring public keys, never a retired one. A
  staged key signs nothing; it exists so that every replica and every JWKS
  consumer holds the public key before any replica signs with it, which is
  what makes a rotation safe on a multi-replica deployment. A retiring key
  is retired only after every credential it could have signed has expired.

## Key-bound tokens

A token with `cnf` is presented as `Authorization: DPoP <token>`, and the
request carries a `DPoP` header with a proof signed by the key that `cnf.jkt`
names. A verifier MUST refuse a `cnf` token presented under `Bearer`, and a
`cnf` token whose request carries no valid proof. It answers 401 with
`WWW-Authenticate: DPoP error="invalid_dpop_proof"`. A token without `cnf`
MUST NOT be presented under the `DPoP` scheme, and a verifier refuses it as
an invalid token. The same proof rules apply to key enrollment
(`POST /v1/enroll`) and key check-in (`POST /v1/checkin`), which carry a
proof and no token. They apply as well to an AI agent's ID-token check-in
(`POST /v1/checkin` with `id_token`), whose proof carries no `ath`, because
the ID token is not a session token, and no `ctr`. When that proof verifies,
the session token it mints carries the proof key's thumbprint as `cnf.jkt`.

### The proof

A proof is a compact JWS. `dpop-proof.schema.json` gives the shape of its
decoded header and claims.

- Header: `typ` is `dpop+jwt`, `alg` is `EdDSA`, and `jwk` is the Ed25519
  public key (`kty` `OKP`, `crv` `Ed25519`, `x`). The `jwk` MUST NOT carry
  the private part `d`. A header with `crit` is refused, because no
  extension is supported.
- Claims: `htm`, `htu`, `iat`, `jti` and `nonce` are required. `ath` is
  present exactly when the request carries a session token, and it is the
  unpadded base64url SHA-256 of that token. A proof with `ath` on a request
  that carries no token is refused.
- `ctr` is present exactly on a key check-in proof. It is an integer of at
  least 1 and MUST equal the `counter` in the check-in body, so the counter
  cannot be changed without the key. A proof with `ctr` on any other
  request is refused, because the claim means nothing there.

A verifier first parses the proof. A proof that is not a compact JWS whose
header and claims decode to JSON objects is a proof failure. Then it checks
the proof in this order:

1. The nonce (see Nonces).
2. The header, as above.
3. The signature, under the header's `jwk`.
4. `htm`, which MUST equal the request method.
5. `htu`, as below.
6. `iat`, which MUST be present.
7. `jti`, which MUST be present and at most 128 bytes. On key enrollment,
   key check-in and an AI agent's ID-token check-in, whose `jti` the
   server spends in its database, it MUST NOT hold a NUL character
   (U+0000), because a Postgres database cannot store that character.
8. `ath`, as above.
9. `ctr`, as above.
10. The key's thumbprint, which MUST equal `cnf.jkt` when the request
    carries a key-bound token.
11. The `jti`, which MUST NOT be spent. Only a proof that passed every
    check before this one is spent, where the `jti` rules below say.

On a key check-in the server then compares the key's thumbprint with the
key the device enrolled.

- `htu`: the query and fragment are ignored. The path MUST equal the request
  path exactly. The origin (`scheme://host[:port]`, with scheme and host
  lower-cased and the scheme's default port dropped) MUST be the origin of
  the server's public URL. The origin the request arrived on is accepted as
  well only when the public URL is unset, or when both are loopback hosts
  with the same scheme and port, because the sender writes the Host header.
- `iat` is required and informational. The nonce carries freshness, so
  clock skew between a client and the server does not matter.
- `jti` on key enrollment, key check-in and an AI agent's ID-token
  check-in: the server spends the proof id in its database, which every
  replica shares, once it has identified the caller and before it acts on
  the request. The proof id stays spent for three minutes, so a proof passes
  once across all replicas.
- `jti` on requests with a key-bound session token: a replica spends the
  proof id in memory for three minutes, so the data plane reads no
  database. Replicas do not share these, so a copied proof can pass once on
  each other replica. It passes only while its nonce is still accepted, at
  most two minutes, and only for the same method, path and token. Making a
  new proof needs the private key.

### Nonces

- The server sends nonces in the `DPoP-Nonce` response header. A client
  treats a nonce as opaque, keeps the latest one from any answer, and puts
  it in the `nonce` claim.
- A nonce belongs to a 60 s window. The server accepts the nonce of the
  current window and of the previous one, so a nonce lives between one and
  two minutes. In the reference implementation a nonce is the unpadded
  base64url of the window number as 8 big-endian bytes followed by their
  HMAC-SHA256 under a key every replica loads from the settings row
  `authn.dpopNonceKey`, so every replica accepts every replica's nonces.
- The nonce is checked first, once the proof parses as a compact JWS whose
  header and claims are JSON objects. A missing or stale nonce gets 401
  with `WWW-Authenticate: DPoP error="use_dpop_nonce"` and a fresh
  `DPoP-Nonce`. The client signs the request again with that nonce and
  retries once. This answer is never audited and changes no state.
- `POST /v1/enroll` and `POST /v1/checkin` use this resource-server form
  too, with a sentence in the JSON `error` field. They do not use the
  token-endpoint form of RFC 9449 section 8, a 400 with
  `{"error":"use_dpop_nonce"}`, so a client written for that form must also
  retry on this 401.
- Every answer to a request that carried a `DPoP` header carries a fresh
  `DPoP-Nonce`.

Changes follow the additive-change rule in the spec README.
