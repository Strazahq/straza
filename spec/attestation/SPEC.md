# Attestation, v1beta1

Status: **beta** (no wire changes from v1alpha3, see Changelog).

The check-in payload (`checkin-request.schema.json`) carries the harness
identity, the client platform, and the measured hashes of every artifact the
client-side decision depends on. Levels computed by the server:

- `managed` means the check-in claims a managed install (`managed: true`) and
  **verifies** against the expected-hash registry (see below).
- `advisory` means measurements are present but unverified: an unmanaged
  (user-mode) install, or a managed claim for which the registry holds no
  applicable rows.
- `none` means no measured hashes, **or tamper evidence**: a managed claim
  whose measurements mismatch or omit a registered artifact.

## Measurement keys

| Artifact | Meaning |
|---|---|
| `self` | The straza binary. |
| `config` | The straza managed configuration (server URL, pinned keys). |
| `hooks.<harness>` | The managed hook-wiring file for one harness. |

`platform` is the client's `GOOS/GOARCH` (e.g. `linux/amd64`); it selects
platform-scoped registry rows.

## Verification algorithm (server, at session start)

1. No hashes → `none`.
2. `managed: false` → `advisory` (user-mode installs are advisory at best).
3. `managed: true`: select registry rows whose `harness` is empty or equals
   the check-in harness, and whose `platform` is empty or equals the payload
   platform. Group the selected rows by artifact: each group is that
   artifact's **allowed set** (multiple rows per artifact support upgrade
   windows).
   - No selected rows → `advisory` (nothing to verify against).
   - Every selected artifact must be present in the payload with a hash from
     its allowed set; a mismatch **or a missing measurement** → `none`.
   - Otherwise → `managed`. Extra unregistered measurements are recorded but
     ignored.
4. Attestation is session-scoped: fixed at session start, never re-derived
   from a refresh payload.
5. Renewal: when the check-in authenticated with a
   `device_token` that is past half its lifetime and every step above passed
   (denylist, user and device status, the attestation minimum), the answer
   carries a fresh `device_token` with `device_token_expires_in` and the
   client replaces its stored credential. No other lane and no refused
   check-in ever carries one. An active device therefore never re-enrolls,
   while one idle for the full lifetime still expires. Since the revision of
   2026-10-21 only a `human` row checks in with a `device_token` (section
   Retired kit bearer lane), so only the human clients renew one. Since the
   revision of 2026-11-01 the server refuses every `device_token`, so no
   answer carries a renewed one (section strazactl sign-in retired).
6. Client binding: a device row records the kind of client that enrolled
   it, `kit` or `human`, from the enrol request's `client_kind` (openapi
   0.114.0), and a row from before the kind was recorded counts as `kit`.
   The harness names of the human clients (`console`, `self-service`)
   open a session only for a user who is a person, and never on a `kit`
   row; every other harness name opens a session only on a `kit` row. A
   mismatch is refused with 403 before the attestation minimum and its
   exemption are judged, and writes one `straza.audit.authn` login failure
   naming the reason. Since the revision of 2026-10-21 a `kit` row opens a
   session only when it holds a key (section Retired kit bearer lane).
   Since the revision of 2026-11-01 a `human` row opens no session at all,
   and the harness name `strazactl` opens none on any lane (section
   strazactl sign-in retired).

The profile knob `governance.minAttestation` (`none` | `advisory` | `managed`;
enterprise default `managed`) gates token issuance: a check-in below the
minimum receives **no session token**, with an actionable reason.

Registry rows are managed via `/v1/admin/attestation-hashes` (see
`pkg/api/openapi.yaml`). The server registers the hooks rows of its own
harness-config render at every boot; an administrator adds a row by hand
only for a local render the server never published (`strazactl attestation
add`, the line `straza install --managed` prints in that one case).

A check-in whose `harness.name`, `harness.version` or `client.version`
holds more than 256 bytes is refused with 400 and a sentence that names the
field, its length and the limit, before the server reads a token or writes a
session or a record. The server refuses rather than cuts, because policies
match the harness name as sent. A straza client sends a few dozen bytes in
each, so a longer one comes from another program.

A check-in whose `harness.name`, `harness.version`, `client.version` or
`client.commit`, or an artifact name or a hash in `attestation.hashes`,
holds a NUL character (U+0000, a zero byte) is refused with 400 and a
sentence that names the field at the same point, so no token is read and
no session or record is written. The server stores these fields when a
check-in starts a session, and a Postgres store cannot hold that character
in text or jsonb, so the server refuses it in every check-in, a refresh
included, rather than strip it. A straza client never adds the character itself.

## Key lane

A device can enroll with an Ed25519 key instead of a device token (openapi
0.143.0). Each key enrollment brings a new key: a re-enrollment with a new
key replaces the old key and starts the counter at 0, and a key already
enrolled for the user answers 409. The device's check-in carries no token
in the body. The body carries `device_id`, `counter`, the harness, the
attestation and the client, and the
request carries a `DPoP` header (RFC 9449) with a proof signed by the
enrolled key: `htm` POST, `htu` the server's `/v1/checkin`, the server's
nonce, `ctr`, the counter the body sends, and no `ath`. The proof signs the
counter so that nobody without the key can change it. The origin of `htu`
must be the origin of `server.publicUrl`. The origin the request arrived on
is accepted as well only when `server.publicUrl` is unset, or when both are
loopback hosts with the same scheme and port. The server judges a key-lane
check-in in this order:

1. The nonce. A proof with no nonce or a stale one is answered 401 with
   `WWW-Authenticate: DPoP error="use_dpop_nonce"` and a fresh nonce in the
   `DPoP-Nonce` header. The client signs again with that nonce. Nothing is
   recorded and the counter does not move.
2. The proof itself, against any key, including its `ctr`, which must
   equal the body's `counter`. A request with no proof, or with a proof
   that does not verify, is answered 401 with
   `WWW-Authenticate: DPoP error="invalid_dpop_proof"` and writes one
   `straza.audit.authn` login failure.
3. The device row. It must exist, be active and hold a key whose
   thumbprint equals the proof key's, else 403. Once the device is
   identified, the proof's `jti` is spent in the store that every replica
   shares, so a replayed proof is refused with 401 and one login failure on
   any replica.
4. The user and device denylists, the user's status, and the sign-in
   deadline: a key enrollment lasts `governance.signInMaxAge` (7 days by
   default) counted from the key enrollment, which needs a sign-in at the
   identity provider, and a check-in past it is refused with 403. Since the
   revision of 2026-10-22 the deadline counts from the sign-in's
   `auth_time` when the enrollment's ID token carried one (section Person
   sign-in at key enrollment).
5. The counter. It must be higher than the last counter the server
   accepted for the device, and the server raises the stored counter in
   one conditional write. A counter at or below it, under a valid proof,
   means a copy of the key checked in. The server then revokes the device
   as an admin revoke does, writes one `straza.audit.identity` record with
   the action `device.key.reused` (spec/events revision 45) and answers 403.

Because a reused counter revokes the device, and so does the check-in that
loses a race with another, a client sends each counter at most once. It
records the counter before it sends the request, and it takes the next
counter for every new check-in. It resends the same counter only after the
nonce challenge, with a fresh proof. It never runs two key check-ins for one
device at once.

Then the verification algorithm above applies as on every lane, and the
session token carries `cnf.jkt`, the key's thumbprint (spec/session-token),
so every request with it needs a fresh proof from the same key. The refresh
of such a session sends `session_token` in the body with a proof whose
`ath` hashes it. A body with a `device_token` and a `DPoP` header is
refused with 400, and so is a body with a token and a `counter`, or with a
`counter` below 1, before any proof is asked for, because `counter` belongs
to the key lane and starts at 1. A person's verified `id_token` with a
`DPoP` header is refused with 400 as well (section Agent proofs at
check-in). A device enrolled with a key accepts only its key: an `id_token`
check-in that names it and a `device_token` on it are refused with 403.

A key enrollment whose `device.name`, `device.platform`,
`device.fingerprint` or `machine_binding` holds a NUL character (U+0000)
is refused with 400 and a sentence that names the field, with the other
checks of the body and before the proof is checked. It writes no record,
spends no proof and changes no device row or counter. The server stores
these four fields when an enrollment creates a device row, and a Postgres
store cannot hold that character in text, so the server refuses it in every
key enrollment, a re-enrollment included. The `custody` fields are stored
as JSON text, which escapes the character, so a NUL there is accepted as
before.

Custody: a person's key enrollment reports where its key is kept in
`custody.kind` (openapi 0.143.0). `account` is a key holder under its own
operating system account, such as the Straza background service, and `user`
is a key in the person's own account. `user` ranks below `account`. The
server setting `governance.minCustody` names the lowest kind it accepts:
`account` or `user`, `account` by default on the enterprise profile and
`user` on the standalone profile (openapi 0.144.0). A key enrollment whose
kind ranks below it is refused with 403 and writes one `straza.audit.authn`
login failure with the reason `key custody is below governance.minCustody`
(spec/events revision 46). The server checks the stored custody again on
every key check-in, once the counter is raised, and on every refresh of a
session bound to a keyed device, with the same answer and record, so a
stricter setting ends the sessions of a lower kind within one token
lifetime, 300 seconds. A keyed device with no custody recorded ranks below
both kinds. The kind is what the client reports at enrollment, and the
server cannot prove it, so the setting refuses a client that reports a
lower kind and does not stop one that reports a false kind. Only a person's
machine key carries custody: an AI agent's key, registered with `PUT
/v1/admin/users/{id}/nhi-key` or used only for the proofs of section Agent
proofs at check-in, carries none, and the setting does not apply to it.

## Person sign-in at key enrollment

Since the revision of 2026-10-22 the server judges how the person signed in
before it enrolls a person's machine key. The request does not change,
because the facts ride in the ID token. Once the ID token verifies, the
proof's `jti` is spent and the user is a person (section Key lane), the
server judges, in this order:

1. The audience, on the built-in issuer only. A key enrollment accepts only
   an ID token whose audience is `straza`, else 403 with the reason `the
   sign-in was made for another Straza program`, so a token minted for the
   console or for strazactl enrolls nothing there.
2. The nonce. The ID token's `nonce` claim names the key. A nonce of the
   form `<jkt>.<r>` gives the lane `browser`, where `jkt` is the RFC 7638
   SHA-256 thumbprint of the key that signs the enrollment's `DPoP` proof,
   the value the session token later carries as `cnf.jkt`, and `r` is at
   least 16 random bytes. Both are base64url without padding, so `r` has
   22 characters or more. An absent or empty nonce gives the lane `code`,
   because a device code sign-in (RFC 8628) carries none. Any other nonce,
   one that names another key included, is refused with 403 and the reason
   `the sign-in was made for another key`. So a browser sign-in made for
   strazactl or for another enrollment enrolls nothing. A device code
   sign-in names no key, so it enrolls as a code sign-in wherever
   `governance.minSignIn` is `code`, the standalone default: from the
   straza client on the built-in issuer, and from any Straza program at an
   external identity provider, whose programs share one client id.
3. Freshness, for the lane `browser` only. The ID token must carry
   `auth_time`, else 403 with the reason `the sign-in carries no
   auth_time`. `auth_time` must be no more than 10 minutes before the
   server's clock, else 403 with the reason `the sign-in is older than 10
   minutes`, and no more than 1 minute after it, else 403 with the reason
   `the sign-in time is ahead of the server clock`, whose sentence asks
   the admin to check the clocks.
4. The lowest lane. `governance.minSignIn` names the lowest lane the server
   accepts. `code` ranks below `browser`, and the default is `browser` on
   the enterprise profile and `code` on the standalone profile. A lane that
   ranks below it is refused with 403 and the reason `the sign-in lane is
   below governance.minSignIn`.
5. The deadline, on both lanes. A sign-in whose `auth_time` is already
   older than `governance.signInMaxAge` is refused with 403 and the reason
   `the last sign-in on this machine is older than governance.signInMaxAge`,
   the reason a key check-in past the deadline writes.
6. The custody rule of section Key lane.

Each refusal writes exactly one `straza.audit.authn` login failure with
`via` `device-key` and its reason, and enrolls nothing. The server stores
the lane it judged as `sign_in` beside the device's custody. The request's
`custody` object keeps its three fields, so a client cannot claim a lane,
and a `sign_in` member sent in it is ignored. A keyed device stored without
a lane counts as `code`, because every key enrollment before this revision
signed in by device code. The server checks the stored lane against
`governance.minSignIn` again at every key check-in, once the counter is
raised, and at every refresh of a session bound to a keyed device, with 403
and the same reason, `via` `device-key` on a check-in and `session-token`
on a refresh. A stricter setting therefore ends the sessions of a lower
lane within one token lifetime, 300 seconds. Under the enterprise default,
a device enrolled before this revision is refused at its next key check-in
until the person enrolls again with a browser sign-in.

The sign-in deadline of section Key lane counts from the ID token's
`auth_time` when it carries one no more than 1 minute ahead of the
server's clock, and from the enrollment otherwise, on both lanes. A
successful key enrollment writes one `straza.audit.identity` record with
the action `device.enrolled`, the lane as `signIn`, the custody kind and
`authTime` when the token carried one (spec/events revision 47). An
enrollment with `client_kind` `human` is not judged by these rules, because
the server refuses it (section strazactl sign-in retired).

The server sees only the ID token. A client obtains it through an
authorization code request with PKCE (RFC 7636, `S256`) that redirects to
`http://127.0.0.1:<port>/callback` (RFC 8252), with the scopes `openid
profile`, `state`, the nonce above, `prompt=login` and `max_age=600`. The
straza client makes that request from the Straza background service, which
alone holds the PKCE verifier. The rules above are the contract, and they
do not depend on the identity provider. They leave one gap. The server
cannot see whether the sign-in asked for `prompt=login` or whether the
provider honored it, so an agent that runs as the person, and opens the
person's browser at an authorization request of its own within 10 minutes
of a real sign-in at the provider, can enroll a key it holds with the lane
`browser`. A confirmation of each new machine on the person's phone, a
later revision of its own, closes it.

## Retired kit bearer lane

Since the revision of 2026-10-21 the straza client enrolls a person's
machine and checks it in on the key lane only. A device row whose client
kind is `kit`, or empty because it was enrolled before the kind was
recorded, and that holds no key is a bearer row of an older straza client,
and the server refuses it on every lane:

- `POST /v1/enroll` with `client_kind` `kit` or absent and no `DPoP` header
  verifies the ID token as before, then answers 403 instead of enrolling.
- A `device_token` check-in on such a row answers 403.
- An `id_token` check-in whose `device_id` names such a row answers 403.
- The refresh of a session bound to such a row answers 403, so a live
  session on such a row ends within one token lifetime, 300 seconds.

The answer's sentence tells the person to update Straza and enroll again.
Each refusal writes one `straza.audit.authn` login failure with the reason
`the client's bearer device credential is retired` and the `via` of its
lane: `id-token` on enrollment and on the ID-token lane, `device-token` on
the device-token lane and `session-token` on a refresh (spec/events
revision 46). Rows of kind `human`, which strazactl enrolled, kept the
device-token lane until the revision of 2026-11-01 (section strazactl
sign-in retired). A check-in under a human client's harness name
(`console`, `self-service`) on a `kit` row is refused by the client binding
of step 6 first, with that step's sentence and reason. The cut does not reach an AI agent's deviceless
`id_token` check-in, which an older straza client also makes: without a
`DPoP` header it still mints a token without `cnf` (section Agent proofs at
check-in). Since the revision of 2026-10-22 that holds on the standalone
profile only (section Enterprise ID-token check-ins). The bearer rows stay
in the store as history.

## strazactl sign-in retired

Since the revision of 2026-11-01 strazactl no longer signs in. Its sign-in
kept a renewing `device_token` and a session in a file that any program
running as the person could copy, so strazactl runs on an admin API token
instead, which the check-in never takes. The server refuses strazactl's
sign-in on every lane once the credential has named the user:

- `POST /v1/enroll` with `client_kind` `human` and no `DPoP` header
  verifies the ID token as before, then answers 403 and writes no device
  row.
- A check-in whose `harness.name` is `strazactl` answers 403 on every lane:
  `id_token`, `device_token`, `session_token` and the key lane.
- A `device_token` check-in on a `human` row answers 403 under any harness
  name.
- A session that strazactl started is refused at its next refresh, and on
  the admin API, the connect routes and the routes that decide a request,
  so a session alive at the upgrade ends at its next request there.

The answer's sentence tells the person to mint an admin API token in the
console and run strazactl with it. Each refusal writes one
`straza.audit.authn` login failure with the reason `strazactl no longer
signs in` and the `via` of its lane (spec/events revision 55). With the kit
bearer lane retired on 2026-10-21, the server now accepts no `device_token`
at all, and no check-in answer carries one. The human clients are the
console and the self-service page, which check in with an `id_token`,
refresh with a `session_token` and name no device.

## Agent proofs at check-in

An `id_token` check-in whose verified user is an AI agent or a service
account, the deviceless lane, may carry a `DPoP` header (openapi 0.144.0).
The server checks the proof as on the key lane, with `htm` POST, `htu` the
server's `/v1/checkin`, the server's nonce, no `ath` and no `ctr`. A
missing or stale nonce answers the nonce challenge and writes no record.
Any other proof failure, a replayed proof included, answers 401 with
`WWW-Authenticate: DPoP error="invalid_dpop_proof"` and writes one
`straza.audit.authn` login failure with `via` `id-token` that names the
agent (spec/events revision 46). Once the ID token and the proof verify,
the server spends the proof's `jti` in the store that every replica shares,
as on the key lane, so a replayed proof is refused on any replica. The
session token then carries `cnf.jkt`, the thumbprint of the proof's key
(spec/session-token), so every request with it needs a fresh proof from
that key, and its refresh needs a proof whose `ath` hashes it. Such a
session has no device row, so its refresh checks no device key, custody or
sign-in deadline.

Since the revision of 2026-10-27 an AI agent's ID token from the built-in
issuer's client_credentials grant names the key it was issued for in
`straza_jkt` (spec/session-token, section Built-in ID tokens). The server
compares that stamp with the key registered for the agent when it verifies
the ID token, before the proof. A stamp for a key that is no longer
registered, because an admin replaced or removed it, answers 401 and writes
one login failure with `via` `id-token` that names the agent and the reason
`the ID token was issued for a key that is no longer registered` (spec/events
revision 49). The server reads the key again once the check-in's session
row exists and before it hands out a token, so a key change that ended the
agent's sessions before that row was written still ends this one, with the
same 401 and record and no `session.start`. The straza client fetches a
new ID token after a 401, so an agent whose machine holds the new key
checks in again on its own. Then the
proof's key MUST be the stamped key. A proof from another key answers 401
with `WWW-Authenticate: DPoP error="invalid_dpop_proof"` and writes one
login failure with the reason `the DPoP proof key is not the key the ID
token was issued for`, before the proof's `jti` is spent. The same stamp
check answers every other route that takes a built-in ID token, such as
`GET /v1/self`, and a store that does not answer the key read gets 503. An ID
token without `straza_jkt`, from an external identity provider or minted
before that revision, is checked in as before: the server compares its proof
key with no key it stores, the ID token proves which agent checks in, and
the proof binds the session to the key holder that made it. When an admin
replaces an agent's key with another or removes it, the server also ends
every active session of the agent, with one `session.end` record each
(openapi, `PUT` and `DELETE /v1/admin/users/{id}/nhi-key`). An agent's `id_token` check-in without a
`DPoP` header is unchanged and mints a token without `cnf` on the
standalone profile, and since the revision of 2026-10-22 the enterprise
profile refuses it (section Enterprise ID-token check-ins). A person's
`id_token` check-in with a `DPoP` header is refused with 400 and no record
once its ID token verifies, because the server tells a person from an agent
only from the verified token. An ID token that does not verify answers the
ID-token lane's 401 with one login failure, as it does without a proof. The
revision of 2026-10-20 answered 400 with no record before it read the
token.

## Enterprise ID-token check-ins

Since the revision of 2026-10-22 the enterprise profile refuses two
`id_token` check-ins that the standalone profile keeps. Each refusal
answers 403 and writes one `straza.audit.authn` login failure with `via`
`id-token`, the user and the harness (spec/events revision 47).

- A person's `id_token` check-in under a coding harness name, any name but
  `strazactl`, `console` and `self-service`, is refused with a sentence
  that tells the person to install Straza on the machine and run `straza
  enroll`, and with the reason `a person's ID token alone does not start a
  session on enterprise`. Such a check-in would start a session with no
  device, key, custody or sign-in deadline. On enterprise an agent that
  runs as the person and reads the person's browser cookies can get the
  person's ID token for the public Straza client without a prompt, so the
  check-in would turn that token into a session outside the Straza
  background service.
- An `id_token` check-in of an AI agent or a service account without a
  `DPoP` header is refused with the reason `an agent check-in without a
  key proof is refused on enterprise`. Every agent session on enterprise
  then carries `cnf.jkt`, the key that signed its check-in proof (section
  Agent proofs at check-in). Since the revision of 2026-10-27 the built-in
  issuer's agent token names the agent's registered key, and the server
  refuses a proof from any other key, so a copied ID token starts no session
  without the agent's private key. An agent that signs in at an external
  identity provider with a client secret gets a token that names no key, so
  a leaked client secret still starts a session, bound to the key of
  whoever holds it.

The person rule answers before the device checks, so on enterprise a
person's check-in under a coding harness name that names a `kit` row
without a key gets the reason `a person's ID token alone does not start a
session on enterprise` instead of `the client's bearer device credential
is retired` (section Retired kit bearer lane).

The standalone profile keeps both lanes. Its built-in issuer asks for the
password on every sign-in, so nothing mints a person's ID token silently,
and an older client's agent keeps its check-in without a proof. These two
refusals are not additive. They were accepted on 2026-10-22 as a break
that ships with the major release 2.0.0, and the additive rule of the
spec README is suspended for them, as it was for the SCIM token kind.

Examples in `examples/` are replayed verbatim by the platform contract
tests (`internal/server`): the `${ID_TOKEN}`, `${DEVICE_ID}` and
`${DEVICE_TOKEN}` placeholders are substituted at test runtime. The
key-lane examples are replayed without a `DPoP` header, because a fixture
cannot carry a fresh proof, so the server refuses the valid one with 401
for its missing proof. The proof itself is pinned by the examples of
spec/session-token. `checkin-valid-device-token.json`,
`checkin-valid-advisory.json` and `checkin-valid-managed.json` are what a
straza client from before the revision of 2026-10-21 sends. The tests
replay them against a `kit` row without a key, and the server refuses each
with 403 (section Retired kit bearer lane). They keep their names because
they still pass the schema. `checkin-valid-none.json` and
`checkin-valid-client.json` are `id_token` check-ins that name no device,
under the harness names `codex` and `claude-code`. The tests replay them
with a person's ID token on the standalone profile, where they still start
a session. On the enterprise profile the same bodies are refused with 403,
with a person's ID token and with an AI agent's ID token and no proof
alike (section Enterprise ID-token check-ins).

Changes to this artifact follow the additive-change rule in the spec README:
schema + fixtures + CHANGELOG + version bump in the same PR.

## Changelog
- **v1beta1** (2026-11-08, max_age of the browser sign-in): the straza
  client's authorization request carries `max_age=600` instead of
  `max_age=0` (section Person sign-in at key enrollment). Microsoft Entra
  answers `max_age=0` with an ID token that expires the moment it is
  issued, as seen against a real tenant on 2026-11-08, so the server
  refused every browser sign-in through Entra as expired. 600 seconds is
  the window in which the server accepts a browser sign-in's `auth_time`,
  so the request asks for no older sign-in than the server takes, and
  `max_age` still obliges the provider to send `auth_time`. The server's
  rules, the request schema and the examples are unchanged, so the change
  is additive.
- **v1beta1** (2026-11-04, NUL characters): a check-in whose
  `harness.name`, `harness.version`, `client.version` or `client.commit`,
  or an artifact name or a hash in `attestation.hashes`, holds a NUL
  character (U+0000) answers 400 with a sentence that names the field, and
  writes no session and no record. A key enrollment whose `device.name`,
  `device.platform`, `device.fingerprint` or `machine_binding` holds one
  answers 400 the same way before its proof is checked, and writes no
  record and no device row. A Postgres store cannot hold a NUL character in
  a text column or a jsonb value, so before this revision such a request
  answered 500 on Postgres where the field reached the store. Where the
  field did not reach the store, it answered 200 and now answers 400: a
  session refresh, a check-in whose `client.commit` holds the character
  after its 64th byte, which the store does not keep, and a key
  re-enrollment of a keyed device with it in `device.name` or
  `device.platform`, which the re-enrollment does not store. The `custody`
  fields of a key enrollment are stored as JSON text, which escapes the
  character, so a NUL there is still accepted. A straza client never adds
  the character itself. The key lanes also refuse a proof whose `jti`
  holds one (spec/session-token, revision 2026-11-04). The request shape is
  unchanged. The schema gives the check-in's text fields and the artifact
  names of `attestation.hashes` a pattern that excludes the character, and
  its hash values already excluded it. The example
  `checkin-invalid-harness-name-nul.json`, derived from this document, pins
  a harness name that ends in a NUL character, and the earlier examples
  replay as before. These refusals ship as fixes without a major release,
  so the break rule of the spec README is suspended for them.
- **v1beta1** (2026-11-01, strazactl sign-in retired): strazactl no longer
  signs in (section strazactl sign-in retired). Enrollment with
  `client_kind` `human` without a `DPoP` header, a check-in under the
  harness name `strazactl` on any lane, the `device_token` lane on a
  `human` row and the requests of a strazactl session answer 403 with one
  login failure (spec/events revision 55, openapi 0.155.0). With the kit
  bearer lane retired on 2026-10-21, the server accepts no `device_token`
  and sends none in an answer. This change is not additive, because a
  strazactl sign-in that worked before is refused now. It ships with the
  major release 2.0.0, so the additive rule of the spec README is suspended
  for it, as it was for the SCIM token kind. The
  request schema keeps `device_token` and every other field, and only its
  description changes. No example shows a `human` row or the harness name
  `strazactl`: `checkin-valid-device-token.json` replays against a kit row
  and already answered the retired bearer lane's refusal, so every example
  replays as before (derived from this document, not captured from a live
  wire: [doc]).
- **v1beta1** (2026-10-31, text bounds): a check-in whose `harness.name`,
  `harness.version` or `client.version` holds more than 256 bytes answers
  400 with a sentence that names the field, its length and the limit, and
  writes no session and no record. The harness name and version ride on the
  session, its token and every audit record of the session, so one check-in
  with a name of megabytes made records the event bus could not carry
  (spec/events revision 53). The schema gives the three fields `maxLength`
  256, which counts characters where the server counts bytes. This change is
  not additive, because a check-in with a longer value started a session
  before. It ships with the major release 2.0.0, so the additive rule of the
  spec README is suspended for it, as it was for the SCIM token kind. A
  straza client sends a few dozen bytes in each, and
  the earlier examples replay as before. The example
  `checkin-invalid-harness-name-long.json` pins a name of 257 bytes (derived
  from this document and the server's checkinTextRefusal, not captured from a
  live wire: [doc]).
- **v1beta1** (2026-10-27, the agent key stamp): an AI agent's ID token
  from the built-in issuer names the key it was issued for in `straza_jkt`
  (spec/session-token). The server refuses a stamped ID token with 401 once
  that key is no longer registered for the agent, and refuses an agent's
  check-in whose DPoP proof comes from another key with 401 and
  `invalid_dpop_proof`, each with one login failure (section Agent proofs at
  check-in, spec/events revision 49). An admin's key replacement or removal
  ends the agent's active sessions. The proof key refusal is not additive,
  because a check-in that started a session before, with a proof from any
  key, is refused now when its ID token is stamped for another key. It
  ships with the major release 2.0.0, so the additive rule of the spec README
  is suspended for it, as it was for the SCIM token kind. The straza service
  signs the client assertion and the check-in
  proof with the same key, so no shipped client makes such a check-in. ID
  tokens minted before this revision carry no stamp and are checked in as
  before. The request schema and the examples are unchanged, because the
  stamp rides in the ID token, whose examples spec/session-token adds
  (derived from this document, not captured from a live wire: [doc]).
- **v1beta1** (2026-10-22, the person's sign-in at key enrollment and two
  enterprise refusals): a person's key enrollment is judged on its sign-in
  (section Person sign-in at key enrollment). The ID token's nonce must name
  the enrolling key, which gives the lane `browser`, or be empty, which
  gives the lane `code`. On the built-in issuer the token's audience must be
  `straza`, and on both lanes an `auth_time` already older than
  `governance.signInMaxAge` is refused at enrollment. A browser sign-in must
  carry `auth_time` no more than 10 minutes old and no more than 1 minute
  ahead of the server's clock, and the lane must rank at
  `governance.minSignIn` or above, which the server checks again at every
  key check-in and refresh. The sign-in deadline counts from `auth_time`
  when the token carries it. The enterprise profile refuses a person's
  `id_token` check-in under a coding harness name and an AI agent's
  `id_token` check-in without a `DPoP` header (section Enterprise ID-token
  check-ins). The server cannot see whether the sign-in asked for
  `prompt=login` or whether the provider honored it, so the gap that section
  names stays open until a phone confirmation of each new machine. Each
  refusal writes one login failure with its own reason, and a successful key
  enrollment writes one `device.enrolled` record (spec/events revision 47,
  openapi 0.145.0). The request schema is unchanged, because the nonce and
  `auth_time` ride in the ID token. The two enterprise refusals are not
  additive, and they ship with the major release 2.0.0, so the additive rule
  of the spec README is suspended for them, as it was for the SCIM
  token kind. The `governance.minSignIn` default of `browser` on enterprise
  refuses a straza client from before this revision, which signs in by
  device code only, and the devices it enrolled at their next key check-in,
  which 2.0.0 accepts as part of its break with the 1.x clients. The standalone
  profile keeps the code lane and both `id_token` lanes by default, so every
  earlier example replays as before against the standalone test server. No
  example is added: the enrollment request does not change, and the refused
  check-ins send the bodies of `checkin-valid-none.json` and
  `checkin-valid-client.json` (derived from this document, not captured from
  a live wire: [doc]).
- **v1beta1** (2026-10-21, kit bearer lane retired and agent proofs): the
  straza client enrolls a person's machine and checks it in on the key lane
  only. Enrollment of `client_kind` `kit` or absent without a `DPoP`
  header, the `device_token` lane and the `id_token` lane on a `kit` or
  kindless row without a key, and the refresh of a session on such a row
  answer 403 with one login failure (section Retired kit bearer lane,
  spec/events revision 46). This change is not additive, and the public
  straza 1.1.2 uses the retired branch. The hard cut ships with the major
  release 2.0.0, which refuses that client, so the additive rule of the
  spec README is suspended for this one branch, as it was for the SCIM
  token kind. The `human` rows of strazactl and
  the console keep the device-token lane. In the same revision an AI
  agent's `id_token` check-in may carry a `DPoP` header, and the session it
  starts then carries `cnf.jkt` (section Agent proofs at check-in, openapi
  0.144.0). The server answered 400 to that header before, so this part is
  additive. A person's key enrollment and every check-in and refresh of a
  keyed device judge the custody the client reported against
  `governance.minCustody` (section Key lane). A person's `id_token` with a
  `DPoP` header now answers 400 only once the token verifies, so a token
  that does not verify answers the ID-token lane's 401 with one login
  failure where it answered 400 with none. An older client's AI agent keeps
  its deviceless `id_token` check-in without a proof. The request schema is
  unchanged. The examples `checkin-valid-device-token.json`,
  `checkin-valid-advisory.json` and `checkin-valid-managed.json` keep their
  bodies and now replay as the refusal (derived from this document, not
  captured from a live wire: [doc]).
- **v1beta1** (2026-10-20, device key lane): the request may carry
  `counter`, and the fourth credential lane, the key lane, is `device_id`
  plus `counter` with no token in the body, proven by the request's `DPoP`
  header (section Key lane, openapi 0.143.0, spec/session-token for `cnf`).
  The proof signs the counter as `ctr`, and its `jti` is spent across
  replicas once the device is identified. The token lanes refuse `counter`,
  because it belongs to the key lane only.
  Additive: no field is removed or made required, older clients never send
  `counter`, and the earlier examples are unchanged and replay green.
  Examples `checkin-valid-key.json`, `checkin-invalid-key-counter-zero.json`
  and `checkin-invalid-key-with-token.json` (derived from this document,
  not captured from a live wire: [doc]).
- **v1beta1** (2026-09-23, client binding): verification algorithm step 6.
  A device row records the kind of client that enrolled it from the enrol
  request's `client_kind` (openapi 0.114.0), and a check-in whose harness
  name belongs to the other class of client is refused with 403 and one
  chained login failure. The check-in request shape, the examples and the
  version are unchanged: the binding is judged on the stored row, and the
  harness name was already required.
- **v1beta1** (2026-09-09, client stamp): the request may carry `client`
  (`version`, `commit`), the straza build making the check-in; the server
  records it on the session and the sessions list shows it as
  `client_version` (openapi 0.95.0). Additive: older clients omit it, the
  earlier examples are unchanged, and nothing verifies the value (it is a
  self-reported build stamp for fleet hygiene, not attestation).
- **v1beta1** (2026-09-05, device credential renewal): the check-in ANSWER may carry
  `device_token` + `device_token_expires_in`, a renewed enroll credential
  issued when the presented one is past half its lifetime (verification
  algorithm step 5; openapi 0.87.0). Additive: the request shape and the
  examples are unchanged, older clients ignore the fields, older servers
  never send them.
- **v1beta1** (2026-07-27, client binary rename): references to the `straza`
  binary (was `straz`); CloudEvents source `straza` (spec/events revision 14);
  ID-token audience `straza`. Wire shapes unchanged: artifact keys (`self`,
  `config`, `hooks.<harness>`) are name-independent. No legacy fallback: the
  rename precedes the first release (internal testing only).
- **v1beta1** (2026-07-21, the brand rename): the namespace and brand rename
  brings the `straza.dev` schema id, measurement and registration references
  to the `straz` binary (was `agentguard`), and the CloudEvents source
  `straz`; check-in payload shape unchanged.
- **v1beta1** (2026-07-14): schema id stabilized for the spec v1.0 tag;
  no wire changes from v1alpha3.
- **v1alpha3** (2026-07-14): added optional `device_token` as a third
  check-in authentication alternative, the long-lived enroll credential
  (purpose-scoped JWT, `use=device`, bound to user+device, default 720 h,
  `governance.deviceTokenTTL`). Its device binding is authoritative; a
  `device_id` sent alongside it is ignored. Denylist (user/device) and
  user/device status are re-checked at every use.
- **v1alpha2** (2026-07-14): added optional `attestation.platform`;
  defined the expected-hash registry verification algorithm; `managed` is now
  reachable.
- v1alpha1: initial draft; attested check-ins capped at `advisory`.
