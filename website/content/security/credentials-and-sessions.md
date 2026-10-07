---
title: Credentials and sessions
description: Which credential sits where on a machine, how long each one lives, how a session renews, and how to stop each one.
pagetype: explanation
weight: 15
draft: false
keywords: credentials sessions device credential session token enrollment check-in renewal revocation kill switch disk
---


An operator or a reviewer reads this page to learn which credential sits where, how long each one lives and how to stop it. Enrollment gives a machine a device credential, the client uses it to start sessions, and it refreshes a short-lived session token while it works. Local hooks evaluate signed policy snapshots, and the gateway checks calls on the server.

[Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) walks the enrollment itself, and [Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md" >}}) lists every lifetime with the setting that changes it. For an inventory of the files to protect and back up, see [Keys, certificates and tokens]({{< relref "security/keys-certificates-and-tokens.md" >}}).

## The problem


Session tokens and device credentials allow different actions. A session token continues an existing session. The device credential can start a new one.

A copied session token lives 300 seconds, but its holder can refresh it like the original while the session and user remain active. Access can therefore continue until revocation, idle closure, or the configured maximum session lifetime, 12 hours by default. Each refresh checks the denylist before reading the session.

A device credential lasts 30 days from its issue or its last renewal. A check-in renews it once it is past half that life, so a machine that stops checking in loses it 15 to 30 days after its last check-in. It can be revoked by device. If it may have been copied, revoking one session is insufficient because the credential can start another. Disabling the user prevents further check-ins and revokes that user's sessions.

[Known limits]({{< relref "security/known-limits.md" >}}) lists both copies, a session token and a device credential, with what limits each one today.

## Enrollment


`straza enroll --server` asks strazad where the login lives, runs an OIDC device flow there and prints the address and code you confirm in a browser. The issuer is strazad's own in the standalone profile and your identity provider in the enterprise profile. The built-in issuer's ID token lives 10 minutes, and in the enterprise profile your identity provider sets its lifetime. A token that short cannot cover a session started the next morning, which is why a second credential exists.


The client posts the ID token to `/v1/enroll` with the hostname as the device name and the platform, plus a fingerprint made of `host:` followed by the hostname. `strazactl login` names its device `strazactl@` plus the hostname, with a `cli:` fingerprint. Each names its own kind in `client_kind`, `kit` for the enforcement kit and `human` for strazactl, and the row records it.

strazad verifies the login, refuses a disabled user, and refuses a human credential to a user whose type is agent or service. It reuses or creates the device row by fingerprint and kind.

Its answer carries the wire field `device_token`, which this page calls the device credential. It is a JWT signed with the same Ed25519 key as session tokens, whose claims are the user id, a fresh token id, both timestamps, `use` set to `device` and `dev` set to the device id. With no session claim and no audience, the one door it opens is `/v1/checkin`. Its lifetime is `governance.deviceTokenTTL`, 30 days when unset.


The client then pins the snapshot signing keys from `/.well-known/straza/snapshot-keys.json` and writes `config.yaml` with the server address and those keys. Next it writes `state/identity.json` with the login ID token, the device id, the username and the device credential. Those keys are trusted on first use, so they are only as good as the connection `straza enroll` ran over. Enroll over TLS or on a network you trust, as [Known limits]({{< relref "security/known-limits.md" >}}) advises. The credential's expiry is read from its own `exp` claim, never stored.

## The session token


A session starts when the harness fires its session-start hook and the client posts the device credential to `/v1/checkin`. strazad verifies the signature in memory and consults the denylist before any store read. It checks both status rows, and it refuses a kit credential under the harness name of strazactl, the console or the self-service page, and a human credential under any coding harness. Then it computes the attestation level and mints the session token.

The token binds the user, the session id, the device, the harness, the attestation level, a hash of the sorted role ids and the snapshot id. It lives 300 seconds. Every lane that verifies it accepts it for 30 seconds past that expiry as the clock skew allowance, and the device credential gets the same 30 seconds, so a session token is refused 330 seconds after its mint and not 300.


Only the server verifies that signature. The client records the check-in's `expires_in` as an expiry time in `state/session.json`, trusts that clock, and never verifies the token itself. What it does verify on every hook call is the snapshot's signature, under a key pinned at enrollment, and a failure denies with `Straza: snapshot verify failed (refuse to enforce untrusted policy)`.


The gateway refuses in this order:

| Condition | Status | Message |
|---|---|---|
| No bearer, or a token that fails to verify | 401 | `missing session token`, or `session token rejected` |
| The token's user or device is revoked | 403 | `Straza: this device or user has been revoked. Contact your administrator` |
| Only its session or the token itself is revoked | 403 | `Straza: this session has been revoked. Start a new session: its check-in uses this device's existing enrollment, so re-enrolling is not needed` |
| An attestation level below `governance.minAttestation`, read from the token's own claim with no exemption for admin tools | 403 | ``Straza: attestation level %q is below the required level %q. Reinstall with `straza install --managed <harness>` `` |
| A session with no cached subject | 401 | `Straza: session state expired. Check in again` |

A restarted pod holds no cached subject, and a cached subject also ends at the next time one of the user's role assignments starts or ends, so the check-in that follows resolves the roles again. `/v1/decide` turns the same denylist hit into a deny whose rule id is `revoked`, with the same two reasons. A refresh re-resolves the roles but copies the device binding, the attestation level and the harness name from the stored session row. No refresh can therefore change the device, attestation or harness in the token.

## After a pause


The daemon refreshes every 30 seconds. A hook also attempts a refresh when its session is within 120 seconds of expiry. The response determines what happens next:

| Refresh result | Client behavior |
|---|---|
| 401, session no longer usable | Try the device credential once to start a session through the normal checks. When that check-in gets a 408, a 429 or another status below 500 with no reason, the daemon keeps its session state and tries again at its next poll. Any other refusal of the check-in ends the renewal as a 403 does. |
| 403, user or device revoked | End this renewal attempt. The daemon removes its session state and writes a revocation marker. |
| Transport failure | Keep the old token and use the verified snapshot only within the expiry and offline-grace rules |

A refresh returns 401 when the token is beyond its clock allowance or the session has been closed. The janitor closes sessions after 600 seconds without a check-in or at `governance.sessionMaxLifetime`, 12 hours by default. A revoked session also returns 401. Revoking its device or disabling its user returns 403.

For local hooks, the expiry gate remains significant even when a reachable server has refused renewal. Without a revocation marker, the hook can continue using its cached state until the token expires, and then it denies with the server's refusal. Do not treat a failed refresh as proof that every local hook has already stopped.

The offline grace, zero in enterprise and 900 seconds in standalone, is added only when the renewal cannot learn the server's judgment. That happens in four cases:

- No answer came back, or the answer was a server error.
- Something in front of strazad answered with a timeout or a rate limit.
- The answer was a status other than 401 or 403 with no reason.
- The server refused the token, and the check-in through the enrolled identity could not complete.

A renewal can also succeed while straza cannot fetch the newer policy it names. This is the case when the server refuses the fetch, redirects it to another origin or 10 times in a row, or sends a snapshot the keys this machine pinned cannot verify. Every renewal counts, the MCP proxy's own token refresh included.

The client then keeps the reason and the session time it held at that moment. Once that time plus the offline grace has run out, it denies every governed call, however often the token renews, until a fetch of the policy succeeds. `straza doctor` shows the state as a failing `policy` line that names the moment the deny starts. A network failure, a server error, or a renewal that names no policy does not start this, so an outage alone never stops work.

After that gate closes, the hook reports the applicable reason:

```text
Straza: session renewal was refused: this device or user has been revoked. Contact your administrator
Straza: session token expired %s ago and automatic renewal failed (%s). Restart the session, or run `straza doctor`.
Straza: session token expired %s ago, and the renewal got an answer that does not say whether this session is still accepted: %s The next tool call tries the renewal again. If this keeps happening, check that the configured server URL reaches strazad with nothing in front of it that limits or blocks straza, and run `straza doctor`.
Straza: session token expired %s ago and the offline grace period (%s) is exhausted. Straza cannot verify current policy while the platform is unreachable; reconnect and run `straza doctor`.
Straza: this session's policy is out of date: the server holds a newer policy that straza could not fetch. <reason> Tool calls stay denied until straza can fetch the current policy, and `straza doctor` shows this reason.
```

Each `%s` stands for the token's age, then the refusal, the answer or the grace period, and `<reason>` names what failed and what to do. The first comes after the server answered and refused. A renewal that failed for another reason gives the second. An answer that judged nothing, a 408, a 429, or a status other than 401 or 403 with no reason, gives the third. Losing the server entirely gives the fourth, and the client never claims the server was unreachable after it answered. The fifth is the policy that could not be fetched.

## Renewal


A device-credential check-in that passes every gate also looks at the credential's age. Past half its life, strazad mints a fresh one and adds it to the answer beside its lifetime in seconds. Before the half-life those fields are absent. The rule runs only after every gate has passed, so nothing is renewed that could not have started a session. In daily use a machine never re-enrolls, and one that stays idle past the life of its last credential has to enroll again.


The client stores the new credential in `state/identity.json`, and a failed write is logged with the kind `identity` while the check-in stands on the old credential. `strazactl` keeps its copy in `credentials.json`.

Renewal writes nothing about the old credential, and the verifier keeps no per-token state. The old one therefore stays valid until its own expiry, and a stolen copy in use stays alive, because the server cannot tell a copy from the original. The answer to that is a device revoke or a user disable, both checked before any mint, as the device credential's row in [Known limits]({{< relref "security/known-limits.md" >}}) says.

## Revocation and lockout


Three actions stop a credential, each at its own scope:

| Action | What it does |
|---|---|
| A session revoke, with `strazactl sessions revoke` or the session's own logout | Ends the session and leaves the enrollment. It marks the row revoked, writes a revocation record and adds the session to the denylist. The machine's next check-in starts a new session. |
| A device revoke, with `strazactl devices revoke` | Deletes the device row and adds the device to the denylist, with a revocation record. No lift exists for a device. |
| Disabling a user, with `strazactl users disable` | Adds the user to the denylist and revokes every active session of theirs, recording `user.killed` on the audit chain. |

The cascade logs at Error every write it cannot make, whether the revocation record, a session row, the event that carries the revoke to the other replicas or the `user.killed` record. A session row it could not mark revoked is left out of the count in `user.killed`, and the user's denylist entry still refuses that session. SCIM and the lock route run the same cascade as the admin API, and SCIM also deletes the user's OAuth grants. Deleting a user over the admin API deletes the user's OAuth grants, an AI agent's key and a person's approver devices, and each approver device's token answers device_revoked at its next request.


Each replica holds the denylist in memory, so no request path reads the store for it. It rebuilds the list at boot from the persisted revocation rows, refuses to serve when that read fails, then replays `straza.revocation.>` from the event stream's beginning. A session entry is never lifted. The cost is a list that only grows and a boot that replays every revocation before serving.


The daemon is the kill switch. It opens `GET /v1/push` with the session token and holds an event stream for this session, this user and this device. A revocation arrives as one event, the daemon drops `state/session.json` and exits, and the next hook call denies with this reason:

```text
Straza: session revoked (kill-switch push from the server). Tool calls stay denied until a new session starts and checks in again. If only this session was revoked, that check-in starts a new session. If the device or user was disabled, the check-in is refused until an administrator re-enables it. Inform the user and stop.
```

Without the daemon the hook keeps allowing from its cache until its token expires, at most 300 seconds after the token was minted, and then denies with the server's refusal in both profiles. The gateway checks the denylist on every request and refuses at once. A standalone hook whose renewal cannot learn the server's judgment, because strazad cannot be reached or something in front of it answers instead, adds its offline grace, up to 1,200 seconds after the mint. After the daemon has started a session again on its own, a revoke of that new session arrives at the next 30-second poll instead of by push. [Known limits]({{< relref "security/known-limits.md" >}}) lists this delay with what shortens it.


After a revoke, each route answers in its own way:

| After | A request to | Answer |
|---|---|---|
| A session revoke | Refresh | 401 `session is no longer active`, from the denylist before the row is read |
| A session revoke | The gateway | 403 with the session message of the gateway table above |
| A device revoke | Check-in, or a refresh from that device | 403 with the device or user message of the gateway table above |
| A user disable | A new login | 403 `user is disabled. Contact your administrator` |
| A disable or a lock | The user's live console or strazactl session, at every replica whose denylist holds the user | 403 `Straza: this device or user has been revoked. Contact your administrator` |
| A disable or a lock | The same session, at a replica that has not heard of the revoke yet | 401 `session is no longer active`, because the cascade marked the session revoked, or 403 `user is disabled. Contact your administrator` after a disable whose session write failed |

The admin API and the routes that decide a request judge a session the same way on every request. The connect routes and `GET /v1/self/servers` judge a session the same way, a straza session included. In the browser, the console drops the session at its next refresh.

Reactivation over SCIM deletes only the rows SCIM wrote and lifts the denylist entry when no admin or external lock remains, recording `user.reactivated` or, when one does, `user.lift.blocked`. Revoked sessions and deleted OAuth grants stay that way, and the device enrollment is untouched.

## What sits on disk


Everything the client keeps lives under `STRAZA_HOME` when set and `~/.straza` otherwise. `config.yaml` holds the server address and the pinned snapshot keys, and a root-owned `/etc/straza/config.yaml` from a managed install wins over it. `state/identity.json` carries `idToken`, `deviceId`, `username`, `deviceToken` and, for an AI agent, `headless`. `state/session.json` keeps the session id with its token and expiry, the pinned snapshot id, plus the subject facts the check-in answered with. Beside them sit the signed snapshot, the audit spool, the logs and, for a headless identity, `nhi-key.json` with the Ed25519 seed.


Every one of those files is plaintext JSON or YAML with mode 0600, in directories of mode 0700, and nothing is encrypted, DPAPI-wrapped or kept in a keychain. On Windows the mode buys nothing. Go applies only the owner-writable bit there, as the read-only attribute, and writes no security descriptor, so every file under the home inherits the access control list of your profile folder. [Known limits]({{< relref "security/known-limits.md" >}}) lists plain-text client files with what reduces the risk.

`strazactl` keeps a separate `~/.straza/credentials.json`, with the server, the session id, the session token and the device credential, in mode 0600 and ignoring `STRAZA_HOME`. The login ID token stays in `state/identity.json` only as the fallback for state files written before device credentials existed. On a current install it expired 10 minutes after the login and is never presented.

## Where the design stops


A device credential is a bearer secret. Nothing in a check-in is signed by a key the client holds, and a copy of `state/identity.json` is as good as the original until the device is revoked or the user disabled. The fingerprint is the hostname, whose only job is to make a repeat enrollment reuse the device row. It identifies nothing, and no device certificate factor is built.

A copy of the database is the other place the design stops. The session signing key's Ed25519 seed sits unencrypted in the `signing_keys` table, so a copy mints session tokens and device credentials for any user. It yields no admin API token, the identity manager's SCIM credential included, because those are stored as SHA-256 hashes. [Keys, certificates and tokens]({{< relref "security/keys-certificates-and-tokens.md#what-a-copy-yields" >}}) lists everything a copy yields, and [Known limits]({{< relref "security/known-limits.md" >}}) says what to do after a suspected copy.

The sign-in routes carry one per-address limit, `server.loginPerIPRPS`, at 2 requests per second by default. It covers the password submit of the device-code login in both profiles and the headless token endpoint in the enterprise profile, so `/v1/enroll` and `/v1/checkin` carry none and nothing counts failed attempts. [Hardening]({{< relref "security/hardening.md#security-settings-and-their-defaults" >}}) lists every rate limit and the body cap with their defaults.

The phone approver holds a P-256 key. Both a decision and the 30 day renewal of its token cost a fresh signature over a single-use challenge, so a copied approver token can neither decide nor renew. That key proves which device decided, and it does not prove that a person acted. An AI agent that runs as the person can read the person's strazactl login and enroll an approval device of its own, as [the security model]({{< relref "security/security-model.md#an-agent-that-holds-its-persons-login-can-enroll-an-approval-device" >}}) explains. [Known limits]({{< relref "security/known-limits.md" >}}) lists what reduces that today.

AI agents hold an Ed25519 key or your identity provider's client secret and mint a short token per session, so they have no device credential and their sessions are deviceless, as [Identities and roles]({{< relref "concepts/identities.md" >}}) describes.

## How to check it


`straza doctor` prints one line per check and a hint under any line that is not ok, and the identity, session and killswitch lines read the credential. The identity line below is from the [doctor guide]({{< relref "guides/operate/doctor-and-logs.md" >}}):

```text
[ ok ] identity    omar (device 01a067ac-f38f-79d7-aa3b-8c25455e21b3), device credential valid until 2026-10-03
```


Within 48 hours of the expiry it turns to a warning and past it to a failure, and once the session token has also expired, the session and killswitch lines follow that verdict. On the [demo stack]({{< relref "get-started/enterprise-demo-stack.md" >}}), with a credential crafted to expire in 36 hours and a session token three minutes past its own expiry, the three lines read:

```text
[WARN] identity    alice (device 01a07056-3b80-75f5-90f1-e9c11c7b2070): device credential expires 2026-09-06T18:51:34Z
                   → re-enroll soon (`straza enroll`)
[WARN] session     01a06ffd-2b1c-7e40-9c3a-5d8e2f7a4b61 (roles [straza-admin], attestation advisory): token expired 3m0s ago
                   → the next hook call refreshes it automatically; if it stays expired, restart the harness session
[WARN] killswitch  edge push (SSE via http://localhost:8420/v1/push). NOT verified: this session's token has expired
                   → the next hook call refreshes the session; re-run `straza doctor` afterwards to verify the push lane
```

With the credential three days past its expiry and the same session file on disk, they read:

```text
[FAIL] identity    alice (device 01a07056-3b80-75f5-90f1-e9c11c7b2070): device credential EXPIRED 2026-09-02T06:51:34Z
                   → run `straza enroll` again
[FAIL] session     01a06ffd-2b1c-7e40-9c3a-5d8e2f7a4b61 (roles [straza-admin], attestation advisory): token expired 72h0m0s ago and cannot refresh
                   → the automatic refresh needs the device credential the identity check above found dead: run `straza enroll` again, then restart the harness session
[WARN] killswitch  edge push (SSE via http://localhost:8420/v1/push). NOT verified: this session's token has expired and cannot refresh
                   → run `straza enroll` again (the identity check above says why), start a harness session, then re-run `straza doctor` to verify the push lane
```


Administrators list and revoke devices with `strazactl`. Here alice has logged in with `strazactl login` on a machine named `laptop`, as the [Keycloak login guide]({{< relref "guides/connect-identity/keycloak-login.md" >}}) shows:

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl devices list alice
```
{{< /command >}}

{{< see >}}A row for `strazactl@laptop`, the device that `strazactl login` created.{{< /see >}}

```table
ID                                    NAME                      PLATFORM  STATUS  ENROLLED
01a0e9a5-fab3-70c7-a565-67ca1e01ba06  strazactl@laptop          linux     active  2026-09-28 20:12:44
```

The listing is trimmed to that device. A device that `strazactl login` created is named `strazactl@` followed by the host name of the machine it ran on. Revoking takes the username and the device ID from the list:

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl devices revoke alice 01a0e9a5-fab3-70c7-a565-67ca1e01ba06
```
{{< /command >}}

{{< see >}}The answer says that the device credential is dead.{{< /see >}}

```text
revoked device 01a0e9a5-fab3-70c7-a565-67ca1e01ba06 of alice: its device credential is dead; re-enrolling needs a fresh login
```


A renewal leaves the event `straza.identity.updated` with the action `renew` plus the user and the device, on the events stream every configured sink receives by default and never on the hash chain, which holds the `straza.audit.` subjects alone. [The kill-switch runbook]({{< relref "guides/operate/kill-switch-runbook.md" >}}) covers the lockouts, and [Doctor and logs]({{< relref "guides/operate/doctor-and-logs.md" >}}) the rest of the doctor output.
