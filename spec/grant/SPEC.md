# Git Grant, v1alpha1

Status: **alpha**. This artifact was added on 2026-10-24, after the spec-v1.0
tag, and its shapes may still change with migration notes until beta. Straza
mints grants in `internal/authn`, and ghapp is the one resource server that
verifies them today.

Revision (2026-10-24): the first version, for git through a Straza grant.

A git grant is a short signed statement from Straza: one session may run one
git operation, a fetch or a push, on one repo at one resource server. Straza
issues it at `POST /v1/grants` after the decision a call to that server's git
tool would get, with the same roles, object scope, policy and approvals. The
resource server holds the credential for the git host. It verifies the grant
and a DPoP proof (RFC 9449) from the machine key the grant names, and only
then serves git. `straza service` holds the grant in memory on the machine
and never hands it to the agent.

## The grant

A grant is a compact JWS (RFC 7515) with exactly one signature.
`grant.schema.json` gives the shape of its decoded header and claims.

A grant travels in the `Authorization` header line, and many proxies cap a
header line at 8 KiB. An issuer MUST NOT issue a grant longer than 8000 bytes
in its compact form, so that the whole line, with `Authorization: DPoP ` in
front and the line end after, stays under 8 KiB. A resource server MUST accept
a grant of up to 8000 bytes. It MAY accept a longer one or refuse it before it
reads it.

### Header

| Member | Value |
|---|---|
| `alg` | `EdDSA` (Ed25519). |
| `typ` | `straza-grant+jwt`. |
| `kid` | The id of the newest active key of Straza's session key set, the set that signs session tokens (spec/session-token). |

A grant carries no `crit` member.

### Claims

| Claim | Meaning |
|---|---|
| `iss` | Straza's public URL, the same value session tokens carry. |
| `aud` | One string: the resource server's git base URL, exactly as the app manifest parser canonicalized the app's `straza.git.url` (spec/app-manifest). |
| `sub` | The user id. |
| `act` | An object. `ses` is the session id. `hrn` is the session token's harness claim, `<name>/<version>`, and is absent when the session names no harness. `kind` is the subject's user type: `human`, `agent` or `service`. |
| `iat`, `exp` | Whole seconds since the epoch. Straza sets `exp` to `iat` plus 120. |
| `jti` | The grant id, in the form session tokens use, a UUID. |
| `cnf` | `{"jkt": "<thumbprint>"}`: the RFC 7638 SHA-256 thumbprint of the machine key whose proof comes with every request. |
| `srv` | The app's `metadata.name`. |
| `obj` | An object. `type` is the object type the git tool pins, `repo` for a git server. `value` is the canonical `owner/name` from the app's object list. `id` is the list's id for that object, as a string. |
| `op` | `fetch` or `push`. |
| `refs` | Present only on a push that names updates: an array of `{ref, old, new}`, as Refs says. |
| `dec` | The CloudEvents id of the grant's audit record, a `straza.audit.mcp` record with event `grant` (spec/events). |
| `apr` | The approval request id. Present only when an approval ran. |

A verifier MUST ignore a claim it does not know, so that a later revision can
add one.

### Refs

- `ref` is a full ref name that starts with `refs/`, such as
  `refs/heads/main`.
- `old` is the id the ref holds before the push, and `new` is the id it moves
  to. Both are lower-case hex of the repo's hash length: 40 characters for
  SHA-1 or 64 for SHA-256. A create has an `old` of all zeros, and a delete has
  a `new` of all zeros.
- A grant names at most 1000 updates. The byte cap binds long before that:
  a grant holds a few dozen updates with SHA-1 ids and fewer with SHA-256 ids.
  Straza refuses a push grant that would be longer than 8000 bytes, so a push
  that names more updates than fit has to be split into several pushes.
- A pinned `old` is a race guard, never a fast-forward proof. It names the tip
  the push expects to replace, so a push that lost a race to another push no
  longer matches. A force push that names the current tip as `old` matches as
  well, because a server that sees only ids cannot tell a rewrite from a fast
  forward. A deployment that must stop rewrites does so at the git host, for
  example with a branch rule.

## Issuance

These rules bind Straza, the issuer.

- Straza MUST sign a grant with the newest active key of the session key set.
  It publishes that set at `<iss>/.well-known/straza/jwks.json`, and a key
  reaches the set at least 60 seconds before any replica signs with it
  (spec/session-token, Refresh & revocation).
- Straza MUST take `cnf.jkt` from the verified session token, never from a
  request. A session whose token carries no `cnf` gets no grant.
- Straza MUST NOT put `refs` on a fetch grant. A push grant without `refs` is
  the one that opens the receive-pack advertisement. Straza runs no approval
  for it, because it moves no ref.
- Straza MUST queue the grant's audit record before it answers with the
  grant, and `dec` names that record.
- Straza issues a grant for 120 seconds.
- Every Straza verifier other than a git resource server MUST refuse a
  grant, so a grant opens nothing but git. The session token verifier
  (spec/session-token) and the built-in issuer's ID token verifier refuse a
  token whose `typ` is `straza-grant+jwt`, and the other verifiers that trust
  the session key set refuse it because it lacks the `use` claim they require.

## The git binding

A resource server serves git's smart HTTP protocol under
`<aud>/git/<owner>/<name>.git/`, with these requests:

- `GET .../info/refs?service=git-upload-pack` or
  `GET .../info/refs?service=git-receive-pack`
- `POST .../git-upload-pack`
- `POST .../git-receive-pack`

Each request carries `Authorization: DPoP <grant>` and exactly one `DPoP`
header with a proof. The proof header has `typ` `dpop+jwt`, `alg` `EdDSA` and
the machine's public Ed25519 key in `jwk`, and the key's RFC 7638 thumbprint
equals the grant's `cnf.jkt`. The proof claims are these.

- `htm` is the request method.
- `htu` is the exact request URL without query or fragment.
- `iat` is the time the proof was made.
- `jti` is fresh for each request.
- `ath` is the unpadded base64url SHA-256 of the grant.

Straza's nonce (spec/session-token, Nonces) is never sent to a resource
server, and a resource server MUST NOT require or check one.

| Grant | Opens |
|---|---|
| `fetch` | The upload-pack `info/refs` and every `git-upload-pack` POST of one operation. |
| `push` without `refs` | The receive-pack `info/refs` and a `git-receive-pack` POST that carries no commands. |
| `push` with `refs` | The `git-receive-pack` POST whose commands equal `refs` as an exact multiset. |

A grant is spent when its operation's data starts. A fetch grant is spent at
the first pack byte the server sends. A push grant with `refs` is spent at the
POST with commands, whether the server then refuses the push or not. A push
grant without `refs` opens no POST with commands, so none of its requests
spends it, and it ends at its expiry.

## Verification

A resource server checks every request in this order, and it refuses the
request at the first rule that fails.

1. The key. The server is configured with Straza's public URL, the issuer.
   It reads the key set from `<issuer>/.well-known/straza/jwks.json`, and it
   MUST NOT fetch keys from a URL that an unverified grant names. The header's
   `kid` MUST name a key of the set. On an unknown `kid`, the server reads the
   set again at a bounded rate, such as once in 30 seconds, and refuses the
   grant when the key is still missing. It uses only keys with `kty` `OKP`
   and `crv` `Ed25519`.
2. `typ` MUST be `straza-grant+jwt`. Straza writes exactly this value. A
   verifier MAY compare it as a media type, without case and with or without
   an `application/` prefix (RFC 7515 section 4.1.9). It MUST refuse every
   other value, including `JWT`, the typ of a session token, and a header
   without `typ`.
3. `alg` MUST be `EdDSA`, and the signature MUST verify under the key.
4. The claims MUST match `grant.schema.json`: every required claim is
   present with its type, `cnf` carries `jkt`, `op` is `fetch` or `push`, and
   a fetch carries no `refs`.
5. `iss` MUST equal the configured issuer exactly.
6. `aud` MUST be a string equal to the server's own git base URL exactly. The
   server never normalizes it.
7. Time. `exp` MUST be after `iat`, and `exp` minus `iat` MUST be at most 300
   seconds, whatever `exp` says. `iat` MUST NOT be more than 60 seconds in the
   future, and the grant is refused once the clock reaches `exp` plus 60
   seconds.
8. `cnf.jkt` MUST equal the RFC 7638 thumbprint of the `jwk` in the request's
   one DPoP proof.
9. The proof. Its header and claims are those of The git binding, and its
   signature MUST verify under its `jwk`. `htm` MUST equal the request method.
   `htu` MUST equal the server's git base URL plus the request path, without
   query or fragment, with scheme and host compared without case and a default
   port treated as none. `iat` MUST NOT be more than 60 seconds in the future
   or more than 120 seconds in the past. `ath` MUST match the grant. The proof's
   `jti` MUST NOT be spent, and the server keeps it spent for that key while
   the proof's `iat` stays inside the window, so a proof opens one request.
10. `op` against the route, as the table in The git binding says. A fetch
    grant opens no receive-pack request, and a push grant opens no
    upload-pack request.
11. `obj.value` MUST equal the `<owner>/<name>` of the request path byte for
    byte. The server MAY check `obj.id` against the git host, so that a repo
    deleted and created again under the same name is refused.
12. `refs`. On a receive-pack POST with commands, the commands MUST equal
    `refs` as an exact multiset: the same count, each `ref` byte for byte, and
    `old` and `new` compared in lower case.
13. Single use. The grant's `jti` MUST NOT be spent. The server spends it where
    The git binding says and keeps it spent until `exp` plus 60 seconds.

A resource server MAY refuse more than this spec requires. ghapp, for example,
refuses deletes, tags, refs outside `refs/heads/`, push options and signed
pushes.

## Examples

`examples/` holds grants that `TestGrantExamplesUpToDate` in `internal/authn`
generates from fixed seeds and fixed times, and `TestGrantExamplesReplay`
replays them. Each file has these members.

| Member | Meaning |
|---|---|
| `provenance` | How the file was made. |
| `description` | What the example shows. |
| `at` | The verifier's clock. |
| `verifier` | The `issuer` and the `audience` the verifier is configured with. |
| `key` | The public JWK that verifies a valid example, as the key set publishes it. |
| `grant` | The compact grant. |
| `header`, `claims` | Its decoded header and claims. |
| `reason` | On an invalid example, the first rule of Verification it breaks. |

A `valid-*` example passes every rule that needs no request, and an
`invalid-*` example breaks the rule its reason names. An example whose
description starts with "INVALID (semantic)" breaks a rule the schema cannot
express, so the schema admits it. The proof, route, path, commands and
single-use rules need a request, so the examples do not cover them.

Changes follow the additive-change rule in the spec README.
