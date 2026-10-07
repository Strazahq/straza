---
title: Keys, certificates and tokens
description: Every key and token Straza holds, where it lives, how long it lasts, how to rotate or revoke it, and what a stolen copy yields.
pagetype: explanation
weight: 17
draft: false
keywords: keys certificates tokens secrets signing key snapshot key sealing key approver tls webpush rotation revocation backup what a copy yields
---


An operator or an auditor reads this page to learn which keys and tokens Straza holds, what a stolen copy of each gives an attacker, and how to back up, rotate or revoke each one. One server key signs every token Straza hands out, a second signs the policy every workstation trusts, and one sealing key opens every secret the server stores. Everything else is a token those keys sign, a bearer secret the server keeps only as a hash, or a secret you brought from another system and rotate there.

Three files outside the store cannot be regenerated for free. They are the sealing key, the approver TLS pair that phones pinned, and the WebPush key that browser subscriptions are bound to.

## What signs what


The session signing key is an Ed25519 key in the `signing_keys` table with the purpose `session`. It signs the 300 second session token, the 30 day device credential, the 30 day approver device token, the 10 minute login token of the built-in issuer and the 10 minute connect state. Every replica loads it into memory and verifies locally, so no request path reads the store for a signature check.

Rotation is one command, `strazactl signing-keys rotate session`, and it runs in two phases so a replica never signs with a key another replica has not loaded. The previous key keeps verifying for 30 days after promotion, because the device and approver credentials it signed live that long. Because the database holds one active session key, replicas that start at the same moment against a new store sign with the same key.


The snapshot signing key is a second Ed25519 key in the same table with the purpose `snapshot`. It signs every compiled policy snapshot, and each workstation pins every public half the server publishes at `/.well-known/straza/snapshot-keys.json` into its `config.yaml` at enrollment. A hook call verifies the snapshot against that pinned set on every decision and refuses to enforce a snapshot it cannot verify.

The pin is what makes the key hard to rotate. A new key is unknown to every enrolled workstation until it enrolls again, and an old key stays trusted, as [the procedure section](#back-up-and-rotate-by-procedure) explains. Because the database holds one active snapshot key, replicas that start at the same moment against a new store sign every snapshot with the same key, and a workstation verifies it whichever replica it enrolled through.


The client assertion key is an RSA 3072 key in the same table with the purpose `client_assertion`, and it exists only once an administrator runs `strazactl signing-keys rotate client_assertion`. It signs one thing, the 30 second client assertion with which an agent's client gets a token at your identity provider (RFC 7523). Whoever holds it can therefore sign in there as any agent whose client trusts it. The gateway asks for such a token on each call to a server whose manifest sets `credential.agents: client_credentials`, at a provider whose `oauth.providers.<name>.clientCredentials` block names the assertion audience. Once such a server exists, this key signs on live calls.

Unlike the two Ed25519 seeds, its private half is sealed under the sealing key before the row reaches the database. The sealed value names its purpose and key id, so a copy of the database yields nothing without `secret.key` and a sealed key cannot be moved to another row. The server publishes the public halves at `/.well-known/straza/client-assertion-jwks.json`, a plain RFC 7517 key set that the identity provider stores as the JWKS URL of each agent's client. It builds that set from the opened private keys, never from a column a database write could change.

Rotation follows the session key's two phases. A staged key is published two reload intervals before it signs, and the previous key stays published one more minute, which covers every assertion it signed. The database allows one staged and one active key of this purpose, so two replicas that act at the same moment still create one key. `strazactl signing-keys retire <kid>` takes one key out of the key document on every replica within 30 seconds when a key may have been copied. Creating, rotating and retiring need a full administrator, the role `straza-admin` or an admin API token with the scope `full`, and a session that a coding harness checked in is refused even then.


The sealing key is 32 random bytes in `secret.key` under the data directory, or wherever `secrets.kekFile` points, created on the first boot when absent. Server messages call it the KEK, the key-encryption key. It seals every stored MCP server secret, every OAuth grant and server token a person stores, and the private half of the client assertion key with NaCl secretbox before the row reaches the database, and the server opens a row only at the moment a call needs the credential. The database therefore holds these credentials only as ciphertext, and the file holds the only key that opens them. Values written into a server's manifest are not among them, as [What a copy yields](#what-a-copy-yields) explains.

## Keys the server owns


| Kind | Lives in | Lives for | Rotate or revoke | A copy yields |
|---|---|---|---|---|
| Session signing key | the `signing_keys` table, the seed unencrypted | until rotated, through staged, active, retiring and retired | `strazactl signing-keys rotate session` | every token kind for any user |
| Client assertion key | the `signing_keys` table, purpose `client_assertion`, the private half sealed under the sealing key | until rotated, through staged, active, retiring and retired | `strazactl signing-keys rotate client_assertion`, and `strazactl signing-keys retire <kid>` for a key that may have been copied | nothing without the sealing key, and with it a token as any agent whose client trusts the key document |
| Snapshot signing key | the same table, purpose `snapshot`, the seed unencrypted | until replaced | no rotation withdraws it today, as described below | a policy every workstation accepts |
| Sealing key | `secret.key` in the data directory, 0600 | until replaced | a procedure, described below, that re-enters every stored secret | every stored secret, together with the database |
| Approver TLS pair | `approver-tls/` in the data directory, P-256, self-signed | 820 days, with a boot warning 30 days ahead | delete the directory and restart, then every approver device enrolls again | the approver listener's identity to every pinned phone |
| WebPush signing key | `approval.push.webpush.vapidKeyFile`, P-256, minted once | no expiry | replacing it ends every browser push subscription | pushes to those subscriptions, which carry no content |
| Main TLS pair | `server.tls.certFile` and `server.tls.keyFile`, yours | your own PKI's term | replace the files and restart, there is no hot reload | the server's identity on the wire |

## Tokens Straza mints


The session token, the device credential, the approver device token, the login token and the connect state are JWTs signed under the session signing key, and each dies at its own expiry. Of those, the device credential, the approver device token and the connect state also carry a `use` claim that opens exactly one door. The login codes, the phone enroll token and the decision challenge are random values that the server checks and burns. A caller's own token for a server is that server's secret, sealed. The admin API token is the one long-lived bearer secret: 32 random bytes shown once and stored as a SHA-256 hash, so a copy of the database yields no usable token.

| Kind | Lives in | Lives for | Rotate or revoke | A copy yields |
|---|---|---|---|---|
| Session token | `state/session.json` on the workstation, and gateway memory | 300 seconds, refreshed, inside a session that ends after 12 hours | `strazactl sessions revoke`, or disabling the user | that session's access, including refresh while the session remains active |
| Device credential | `state/identity.json`, or `credentials.json` for strazactl | 30 days, renewed past half its life | `strazactl devices revoke`, or disabling the user | from `state/identity.json`, new coding-harness sessions as that user, renewed at each check-in past half its life, until the device is revoked. From strazactl's `credentials.json`, everything the person may do on the admin and self-service routes, enrolling a new approval device for that person included |
| Approver device token | the phone, or the enrolled browser | 30 days, renewed by a signature from the phone's key | `strazactl approvers revoke` | the pending list, never a decision |
| Login token of the built-in issuer | the workstation, briefly | 10 minutes | expires | for its 10 minutes, enrollment and every admin and self-service route open to that user, a new approval device included |
| Connect state | the browser, during an OAuth connect | 10 minutes | expires | starting a sign-in as that user, and nothing more: strazad redeems a provider code only when the state names the user of the session that presents it, so a stolen state needs its owner's session too |
| A caller's own token for a server | the `credentials` table, sealed under the sealing key, one row per person and server | the date the person recorded, or until removed | Remove on the Credentials tab of the self-service page, `straza disconnect <server>`, or disabling the person | that person's identity at the server, together with the sealing key |
| Login codes of the device flow | server memory | 10 minutes, single use | expires | redeeming a pending login into a login token |
| Phone enroll token | shown once as a QR, stored as a hash | 10 minutes, single use | expires or burns on use | enrolling a foreign phone as that user's approver device |
| Decision challenge | the `approver_challenges` table | 5 minutes, single use | expires | nothing without the phone's key |
| Admin API token | the settings table, as a hash, with its scope | as long as it was minted for, or forever when no expiry was given | `strazactl api-token revoke` | everything in its scope |


An AI agent runs as its person and can read both `state/identity.json` and strazactl's `credentials.json`, so a person's admin login is within its reach. [The security model]({{< relref "security/security-model.md#an-agent-that-holds-its-persons-login-can-enroll-an-approval-device" >}}) explains what that allows, and [Known limits]({{< relref "security/known-limits.md" >}}) lists what reduces it.


The scope on an admin API token is the whole story of what a copy yields. A token with `full` or `tokens:write` can mint itself a `full` token. A token with `identity:write` can assign `straza-admin` to any person and set the break-glass password, and one with `scim:write` writes the membership of every role, `straza-admin` included. Treat all four as root: give them to the operator, and the two `scim` scopes to the identity manager only. The identity manager's SCIM credential is the same token kind with the `scim` scopes, and [Hardening]({{< relref "security/hardening.md" >}}) walks its rotation.

## Keys other parties hold


| Kind | Lives in | Lives for | Rotate or revoke | A copy yields |
|---|---|---|---|---|
| Phone signing key | the enrolled device, P-256, public half in `approver_devices` | the enrollment | `strazactl approvers revoke`, then enroll again | decisions that phone may sign, each needing a live challenge |
| AI agent key | `state/nhi-key.json` on the agent's box, Ed25519, public half in the settings table | no expiry | `straza keygen --force` and register again, or `strazactl users nhi-key unset` | logins as that identity for as long as the public half stays registered |
| Pinned snapshot keys | `config.yaml` on every workstation, public halves only | until the workstation enrolls again | `straza enroll`, which pins the server's whole published set again, or on a managed install `straza install --managed --server <strazad URL> <harness>` run again by an administrator | nothing to sign with, though write access to the file redirects the workstation's trust |

A phone decides only by signing a fresh single-use challenge, and it renews its 30 day token the same way, so a copied approver token can neither decide nor renew. The server keeps only the public half of the key. It stores the key posture the device reports without checking it, so the key proves which enrolled device decided, and not that a person did.

## Passwords


| Kind | Lives in | Lives for | Rotate or revoke | A copy yields |
|---|---|---|---|---|
| Local user password | the `users` table as a bcrypt hash | no expiry | `strazactl users set-password` | a login as that user in the standalone profile |
| Bootstrap admin password | printed once at the first boot of an empty standalone store, as the `password` field of a warning line in the server log, which keeps it in every copy of that log until the password is changed | until changed | `strazactl users set-password admin` | full administration |
| Break-glass password | printed once, in both profiles, for an account no identity manager can disable, as the `password` field of a warning line in the first boot's server log, which keeps it in every copy of that log until the password is changed | until changed | `strazactl users set-password break-glass` from an admin session, or a new hash written into the users table as the last resort | full administration in both profiles, through the emergency sign-in in the enterprise profile |

[Known limits]({{< relref "security/known-limits.md" >}}) lists the copies of that first log as a limit, with what reduces it.

In the enterprise profile people have no local passwords, because the identity provider owns the login and its second factor. The break-glass admin is the one exception. strazad keeps its own sign-in page mounted as the emergency sign-in, and that page accepts the break-glass account and nobody else, so a hash that `strazactl users set-password` stores on any other user opens nothing.

The console offers Emergency sign-in under its sign-in button, and `strazactl login --break-glass` runs the same flow from a terminal. Every interactive login as break-glass lands on the audit chain as an alarm, in both profiles. Nobody logs in as it unnoticed.


The identity provider can never become the emergency admin. An account named `break-glass` at the identity provider is refused on every sign-in route with "the account break-glass at your identity provider cannot sign in to Straza, because break-glass is the local emergency admin and signs in with its own password only. Rename or remove that account at the identity provider." Each attempt writes a login failure with the reason `external identity names the break-glass account` and a warning in the server log, and it never links the two accounts. If a server finds its break-glass row linked to an external identity by an earlier version, it keeps the link as evidence, refuses it, and warns at every start with the external id and the two steps to take: read the audit chain for actions by break-glass, and rotate its password.

### Recover from a lockout with the break-glass admin


The drill is the same in both profiles, and it works while the identity provider is down.

1. Open the console and choose Emergency sign-in under the sign-in button. The card shows a code and opens the server's own sign-in page. From a terminal, `strazactl login --break-glass` shows the same code and page address.
2. On that page enter the code, the username `break-glass` and the vaulted password. The console signs you in with full administration.
3. Fix what locked you out: assign `straza-admin` back to a person, or repair the identity provider settings, then sign in as yourself again.
4. Rotate the break-glass password from a terminal, because the console has no password control. Use a strazactl login that holds full administration: your own restored one, or the one `strazactl login --break-glass` gives, which counts as an emergency sign-in of its own and raises its own alarm.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl users set-password break-glass --password '<new password>'
```
{{< /command >}}

The rotation runs through the admin API, so it needs an admin session or an admin API token with the `identity:write` scope. Then revoke every break-glass session under Sessions.

The login leaves three traces:

- On the audit chain, a `straza.audit.authn` login event names the user break-glass.
- Every default sink receives a `straza.identity.updated` event with the action `breakglass.login` on the event stream.
- The server log gets the line `BREAK-GLASS LOGIN`. Alert on that line.

A wrong password leaves a login failure with `via` `password`, and every other username on that page leaves a login failure without a user, because the page refuses it before judging any credential. The submit is throttled per client address, two a second by default.

If the vaulted password is lost as well, the last resort is the database. Write a new bcrypt hash into the `password_hash` column of the break-glass row in the `users` table. The next emergency sign-in reads it, and no restart is needed. `htpasswd -nbBC 10 x '<new password>'` prints such a hash after the colon.

```sql
UPDATE users SET password_hash = '$2y$10$...' WHERE username = 'break-glass';
```

This edit leaves no audit record, so note it in your own incident record.

A role assignment inserted by hand is a different matter and not a recovery path. The running server caches role resolution until an admin write bumps it, so the row takes effect only after a restart. It carries no origin and no actor, and the identity manager's next reconciliation removes it. Sign in as break-glass and assign the role through the console or the CLI instead. When an identity manager writes role membership over SCIM, restore the membership there as well, or its next full membership write removes the console assignment too.

A first boot that stops after it creates the break-glass account and before it assigns straza-admin leaves the account without the role and with a password nobody saw. The next boot assigns the role again. It also replaces a straza-admin assignment of the account whose validity window has ended or not begun, logs `the break-glass admin held no straza-admin assignment in force` and announces the change to your identity manager.

That boot never sets a password. If nobody holds one, have an admin set it with `strazactl users set-password break-glass --password '<new password>'`. When no other admin exists, set `oidc.bootstrapAdmin` to your username and restart strazad. Sign in through your identity provider to become an admin, and then set the break-glass password, or write a hash into the database as the last resort above describes.

## Secrets you bring


These are issued by another system and rotated there. Straza reads them from its config file, a sibling file, or the environment, and never returns one to a client.

| Kind | Lives in | Rotate or revoke | A copy yields |
|---|---|---|---|
| OAuth provider client secret | `oauth.providers.<name>.clientSecret` or its file | at the provider, then edit the config and restart | redeeming and refreshing grants for that app registration |
| An AI agent's identity provider client secret | `STRAZA_CLIENT_SECRET` on the agent's box, never persisted by Straza | at the identity provider | logins as that AI agent |
| APNs key and FCM key file | `approval.push.apns.keyFile` and `approval.push.fcm.serviceAccountFile` | in the Apple and Google consoles | sending pushes to the Straza approver app |
| Slack bot token and signing secret | `approval.channels.slack.botToken` and `signingSecret`, or their files | in the Slack app settings | the signing secret forges approval decisions arriving from Slack |
| Sink signing secret | `sinks[].secret` or `secretFile` | edit the config and restart | forging audit deliveries into your SIEM |
| Object store keys | `capture.bodyStore.accessKey` and `secretKey`, or their files | at the object store | recorded conversation bodies |
| Metrics token | `server.metricsToken` | edit the config and restart | content-free metrics |
| Push relay token | `approval.push.relay.tokenFile`, minted by strazad on first use | delete the file, a fresh one is minted | content-free push envelopes through the relay |

## What a copy yields


A copy of the database holds both signing seeds unencrypted, so it mints every token for any user and signs a policy every enrolled workstation accepts. The session key has a rotate command for that case, and the snapshot key has no rotation that withdraws it, as the next section says. The same copy holds only hashes of admin API tokens and passwords and only ciphertext for the credentials the sealing key seals, so it yields none of those.

It does yield every value written into a server's manifest, because the manifest is stored as plain JSON in the `apps` table. A credential typed into a fixed environment variable, an argument or an address sits there in clear. The server's lists and exports show secret-shaped values masked while storage stays plain text, so keep a credential in the server's secret, set with `strazactl apps secret set`, and never in the manifest.

A copy of the sealing key yields nothing on its own and every sealed credential together with the database. A copy of the approver TLS pair lets an attacker stand in for the approver listener to every phone that pinned it. The data directory of a standalone deployment holds the store, the sealing key and the approver pair at once, so a copy of that directory is a copy of the deployment.

[Known limits]({{< relref "security/known-limits.md" >}}) lists the database copy and the manifest values among its rows, with what reduces each.

## Back up and rotate by procedure


The backup is the store plus three files, the sealing key, the approver TLS pair and the WebPush key, and [Backup and upgrade]({{< relref "guides/operate/backup-and-upgrade.md" >}}) says what breaks without each. Neither the snapshot signing key nor the sealing key has a rotate command. Replacing the sealing key needs work outside the server, because every sealed credential is entered again, and no procedure withdraws an old snapshot key today.


The server publishes every snapshot signing key it has stored, whatever its status, and `straza enroll` pins the whole published set. Setting the active row to `retired` by hand makes the server generate a new key at its next start, and it withdraws nothing. A workstation that has not enrolled again refuses every snapshot the new key signs and denies from its next session start. One that enrolls again pins the old key beside the new one, so it still accepts a policy signed with a copy of the old key. A suspected copy of the database therefore cannot be answered by rotating this key, and the database and its backups need the protection the key itself would.


To replace the sealing key, run these steps in this order. A row sealed under the old key cannot be opened under the new one, and the server refuses to start while it holds a client assertion key it cannot open.

1. Run `strazactl signing-keys list`, and retire every client assertion key whose status is not `retired` with `strazactl signing-keys retire <kid>`. Agents that sign in with client credentials stop working until step 5.
2. Stop every replica of the server.
3. Write 32 new random bytes to the file, for example with `head -c 32 /dev/urandom > secret.key`, keep its mode at 0600, and give every replica the same file.
4. Start the server. A server whose secret was sealed under the old key now reads `requires a credential and none is stored` and fails closed.
5. Set every server secret again with `strazactl apps secret set`, each `--role` override included, and run `strazactl signing-keys rotate client_assertion` if agents sign in with client credentials.
6. Have every person connect their OAuth apps and enter their own server tokens again. Until they do, those servers fail closed for them.

If the old key may have been copied together with the database, change each of those secrets at its source as well, because a copy of the database taken before the swap still opens under the old key.


The approver TLS pair rotates by deleting `approver-tls/` and restarting, after which every approver device enrolls again because its pin changed. [Known limits]({{< relref "security/known-limits.md" >}}) lists that as a limit, with the announcement of a successor that the roadmap plans. The WebPush key is best never rotated at all, since every browser subscription is bound to it. [Credentials and sessions]({{< relref "security/credentials-and-sessions.md" >}}) follows one workstation's tokens through their life, and [Hardening]({{< relref "security/hardening.md" >}}) shows the rotation of the session signing key step by step.
