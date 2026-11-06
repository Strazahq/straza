---
title: Credentials and sessions
description: Which credential sits where on a machine, how long each one lives, how a session renews, and how to stop each one.
summary: >-
  `straza service` keeps each person's Ed25519 enrollment key under its own OS account, out of
  the agent's reach. It starts a 300-second session token per harness window, bound to that key,
  so a copied token works nowhere else. A session revoke, a device revoke or a user disable
  stops it, through the push stream or at the next renewal.
pagetype: explanation
weight: 15
draft: false
keywords: credentials sessions straza service background service key custody device credential session token enrollment check-in renewal revocation kill switch disk
---


An operator or a reviewer reads this page to learn which credential sits where, how long each one lives and how to stop it. On Linux, macOS and Windows, `straza service`, the Straza background service, enrolls each person's machine with a key it keeps under its own OS account. It starts a short-lived session for each harness window and refreshes the session's token while the window works. The hooks and `straza mcp` hold no credential and reach the service over a local socket, or a named pipe on Windows. The service decides local calls against signed policy snapshots, and the gateway checks calls on the server.

[Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) walks the enrollment itself, and [Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md" >}}) lists every lifetime with the setting that changes it. For an inventory of the files to protect and back up, see [Keys, certificates and tokens]({{< relref "security/keys-certificates-and-tokens.md" >}}).

## The problem


An AI agent runs as the person whose harness started it, so it can read every file that person can read. A credential kept in the person's home directory is therefore a credential the agent can copy and use from anywhere. Two kinds of credential matter here. A session token continues an existing session, and an enrollment credential starts a new one.

On Linux, macOS and Windows the person's enrollment is an Ed25519 key that `straza service` keeps under its own OS account, and every session token the service holds is bound to that key. Neither sits in the person's home directory, and a copied token works nowhere else, as [Enrollment bound to a key](#enrollment-bound-to-a-key) describes.

strazactl keeps no credential of its own. It runs only on an admin API token that a person mints in the console and passes in `STRAZA_API_TOKEN`, so no admin credential sits on a machine unless someone puts a token there. strazad refuses the sign-in, the device credential and the session that older versions of strazactl kept in `~/.straza/credentials.json`, so a file left from one works nowhere and can be deleted. An admin API token is a bearer credential: whoever holds a copy acts with its scope from any machine until it expires or is revoked. Never put one where an agent runs as you, and revoke it under Settings, API tokens in the console when it may have been copied.

An AI agent that checks in with its key or client secret without the service, as clients older than this version do, also gets a session token bound to nothing, as [Where the design stops](#where-the-design-stops) says. [Known limits]({{< relref "security/known-limits.md" >}}) lists each copy with what limits it today.

## The background service


On Linux, `straza service` runs in the foreground and serves a local API over a Unix socket. The system service listens on `/run/straza-service/service.sock` and keeps its state in `/var/lib/straza-service`, under an OS account of its own named `straza-service`. With `STRAZA_HOME` set, the service and every straza command use `$STRAZA_HOME/service.sock` and the state directory `$STRAZA_HOME/service` instead, and the service runs as the person who started it. That user mode is how one person runs a test service, and how the journey tests run one service per identity. It works only on a machine with neither a system service nor a managed install. On a machine with either, every straza command ignores `STRAZA_HOME` when it picks the service, so the environment a harness starts with cannot point its hooks at a socket its user owns.

For every connection, the kernel names the caller's numeric user id, and the service keeps each user id's enrollment, key, sessions and audit spool apart. A session of one user is out of reach of every other by construction. Before it sends anything, each straza command checks that the socket belongs to root or the `straza-service` account, or in user mode to the caller, so a program that answers on that path in their place gets nothing. A file lock on the state directory lets one service use it at a time.

### The service on Windows


On Windows, `straza service` runs only as the Windows service straza-service, which the Service Control Manager starts under its own OS account, the virtual account NT SERVICE\straza-service. It serves the same local API on the named pipe `\\.\pipe\straza-service` and keeps its state in `C:\ProgramData\straza-service`. Windows has no user mode. Started from a console, `straza service` refuses to run, and every straza command ignores `STRAZA_HOME` when it picks the service.

For every request, the service reads the caller's Windows account from the pipe, never from a process id, and keeps each account's enrollment, key, sessions and audit spool apart by its SID. It refuses a request whose account it cannot read, and an anonymous one. The pipe takes callers on this machine only. Its access rules let every signed-in account read and write it but not open another instance of it, and the service holds the name for as long as it runs. Before it sends anything, each straza command opens the pipe at the identification level, which lets the service read who is calling but never act as them. The command then checks that the pipe belongs to the service's own account, so a program that took the name first gets nothing.

The service creates `C:\ProgramData\straza-service` with access rules for itself, SYSTEM and Administrators alone, and every file inside inherits them. On Windows that folder is the boundary, and the file modes the service also sets inside it change nothing. At every start the service refuses a folder that another account owns or whose rules let any other account in, so a folder that someone created first stays unused until an administrator moves it away. A file lock on the folder lets one service use it at a time. `straza install --managed` locks `C:\ProgramData\straza`, which holds the managed configuration and the binary the hooks run, so that only SYSTEM and Administrators can change it and users can read it. Straza trusts that configuration only while no other account can change it or its folder.

### The service on macOS


On macOS, `straza service` runs as the launchd daemon ai.straza.service, which the Straza package for macOS installs under its own OS account, the hidden account _straza-service with no login shell. It serves the same local API on the Unix socket `/opt/straza/run/service.sock` and keeps its state in `/var/db/straza-service`. The package makes both folders for that account, because the account cannot make a folder under `/opt` or `/var/db` itself. The service refuses to start when `/opt/straza/run` is missing, is a link, belongs to another account or lets other accounts write it, so nobody else can swap the socket the hooks dial. With `STRAZA_HOME` set, it runs as the person who started it, as on Linux. On a Mac where the package made `/opt/straza/run`, or with a managed install, every straza command ignores `STRAZA_HOME` when it picks the service, even while the service is stopped.

For every connection, the service reads the caller's user id from the kernel with `LOCAL_PEERCRED`, which holds the effective user id the caller connected with, and never takes it from a process id. It keeps each user id's enrollment, key, sessions and audit spool apart, as on Linux. Before it sends anything, each straza command checks that the socket belongs to root or the _straza-service account, or in user mode to the caller. The hooks run `/opt/straza/bin/straza`, and the package refuses to install while accounts other than root can change a folder on that path, because whoever could change one could swap the program the hooks run.

### Custody and the service's sentences

The service computes the custody of each key it holds. It reports `account` when the caller runs under another account than the service, which is always the case for the system service and the Windows service, and `user` when the service runs as the caller. The service answers these sentences itself:

| Condition | Message |
|---|---|
| The system service cannot be reached | ``Straza's background service isn't running on this machine, so this command can't reach your Straza session. Run `straza doctor` to see why, or ask IT to reinstall Straza. On Linux, `systemctl status straza-service` shows why.`` |
| The Windows service cannot be reached | ``Straza's background service isn't running on this machine, so this command can't reach your Straza session. Run `straza doctor` to see why, or ask IT to reinstall Straza. On Windows, `Get-Service straza-service` shows whether it runs, and the Application event log under straza-service shows why it stopped.`` |
| The macOS service cannot be reached | ``Straza's background service isn't running on this machine, so this command can't reach your Straza session. Run `straza doctor` to see why, or ask IT to reinstall Straza. On macOS, `sudo launchctl print system/ai.straza.service` shows whether it runs, and /var/log/straza-service/service.log shows why it stopped.`` |
| A user-mode service cannot be reached, here for the `STRAZA_HOME` `/home/alice/.straza` | ``Straza's background service for STRAZA_HOME /home/alice/.straza isn't running, so this command can't reach your Straza session. Start it with `straza service` in a shell that has the same STRAZA_HOME.`` |
| The caller has no enrollment | ``This machine isn't enrolled for you yet. A person runs `straza enroll --server <strazad url>` and signs in with their work account. An AI agent runs `straza enroll --server <strazad url> --headless` with its registered key or the client secret IT installed for it.`` |
| The caller holds an AI agent's key from `straza keygen` and no enrollment | ``This machine isn't enrolled for you yet. This OS account holds an AI agent's key from `straza keygen`. Once an admin has registered its public key, run `straza enroll --server <strazad url> --headless`.`` |
| The machine identity changed since a person's enrollment, here at `https://straza.example.com` | ``This machine's identity changed since it was enrolled, so Straza stopped using its key. Run `straza enroll --server https://straza.example.com` to enroll it again.`` |
| The machine identity changed since an AI agent enrolled with a key, here the agent build-bot at `https://straza.example.com` | ``This machine's identity changed since it was enrolled, so Straza stopped using its key. Run `straza keygen --force --user build-bot`, have an admin register the new public key it prints, and then run `straza enroll --server https://straza.example.com --headless`.`` |
| The machine identity changed since an AI agent enrolled with a client secret, here the agent build-bot at `https://straza.example.com` | ``This machine's identity changed since it was enrolled, so Straza stopped using its key. Run `straza enroll --server https://straza.example.com --headless --user build-bot` to enroll the agent again.`` |
| A session start inside the wait after a lost check-in answer | `Straza is waiting for the server to confirm this machine's last check-in, which takes up to two minutes after a lost connection. Start the session again in a minute.` |
| A second service on the same state directory | `Another Straza background service already uses this state directory, so this one stopped. Stop the other one first, or set a different STRAZA_HOME.` |
| `straza service` on a system other than Linux, macOS and Windows | `` `straza service` runs only on Linux, macOS and Windows in this version of Straza, so this machine cannot hold Straza sessions yet. `` |
| `straza service` started from a console on Windows | `` `straza service` runs as the Windows service straza-service and cannot run from a console. Start it from an administrator PowerShell with `Start-Service straza-service`. `` |
| The Windows service cannot read which account sent a request | `The Straza background service could not tell which Windows account sent this request, so it refused it. Run the command again from your own signed-in session.` |
| `straza doctor` finds that the program answering on the pipe is not the service, here with an example SID | ``The program answering on \\.\pipe\straza-service is not the Straza background service, so Straza sent it nothing. Its pipe belongs to S-1-5-21-1004336348-1177238915-682003330-1001, not to NT SERVICE\straza-service. Ask your administrator to check the straza-service service on this computer`` |

On a machine whose managed configuration names the server, the enroll commands in these sentences leave out `--server`, because the service enrolls at that server whatever the flag says.

A hook that cannot reach the service denies the call with one of the first four sentences, and so does a hook that finds another program on the pipe. The sentences the Straza server answers, such as the old-client and custody refusals below, pass through to the person unchanged.

## Enrollment


`straza enroll --server` asks `straza service` to enroll the caller. The service asks strazad where the login lives, makes the new key, and builds a sign-in request with a PKCE verifier, a state and a nonce that names the key. It listens on a free port of 127.0.0.1, or on `oidc.loopbackPort`, and streams the link back, and `straza enroll` opens it in the person's browser and prints it. Because the request carries `prompt=login` and `max_age=0`, the identity provider asks the person to sign in again, and it sends the browser back to the service's port. The service checks the state, redeems the code with the verifier, checks that the ID token carries its nonce, and closes the port. Neither the verifier nor the ID token passes through the person's processes, and the service never accepts a pasted redirect, because then an approval on another device could enroll this machine. With `--use-device-code` the service runs an OIDC device flow instead and streams the address and code back for the person to confirm in any browser. In the standalone profile the issuer is strazad's own, and in the enterprise profile it is your identity provider. The built-in issuer's ID token lives 10 minutes, and in the enterprise profile your identity provider sets its lifetime. A token that short cannot cover a session started the next morning, which is why the enrollment binds a key. The service uses the ID token once, to enroll, and does not keep it.


For each enrollment the service makes a new Ed25519 key. It posts the ID token to `/v1/enroll` with a proof from that key, the hostname as the device name, the platform, a fingerprint made of `host:` followed by the hostname, the custody it computed and a counter of 0. It names its kind in `client_kind` as `kit`, and the row records it.

strazad verifies the login, refuses a disabled user, and refuses a human credential to a user whose type is agent or service. It refuses a `kit` enrollment, or one with no kind, that carries no proof, so a straza client older than this version cannot enroll. It refuses every enrollment of the kind `human`, which only strazactl sent, because strazactl no longer signs in. For a person's machine key it then judges the sign-in. A nonce that names the thumbprint of the key that signed the proof makes the sign-in a browser one, which must carry an `auth_time` at most 10 minutes old and at most 1 minute ahead of strazad's clock. An empty nonce makes it a code sign-in, because RFC 8628 has no nonce, and any other nonce is refused, so a browser sign-in that strazactl or another enrollment asked for enrolls nothing. strazad also refuses an ID token of its own issuer that was minted for strazactl or the console, and one whose `auth_time` is already past the sign-in deadline. `governance.minSignIn`, `browser` by default in the enterprise profile and `code` in the standalone profile, refuses a code sign-in where it is `browser`. strazad keeps the lane it judged with the device's custody, and the client cannot claim one. `governance.minCustody` checks the custody the client reports for a person's machine key, `account` by default in the enterprise profile and `user` in the standalone profile, and strazad refuses a key reported in a lower custody. strazad cannot verify that report, so the setting keeps out a wrong install and not a program written to lie. It then reuses or creates the device row by fingerprint and kind and stores the key's thumbprint on it.

| Condition | Status | Message |
|---|---|---|
| A `kit` enrollment, or one with no kind, without a proof, as a client older than this version sends it | 403 | ``This Straza client is older than the server's sign-in rules, which now need the Straza background service on this machine. Update Straza from your software portal, then run `straza enroll`.`` |
| An enrollment of the kind `human`, a device credential of that kind at check-in, or a check-in labelled strazactl, as strazactl before this version sent them | 403 | `strazactl no longer signs in, because its sign-in kept an administrator credential in a file that any program running as you could copy. Mint an admin API token in the console under Settings, API tokens, and run strazactl with STRAZA_API_TOKEN set to it.` |
| An ID token of the built-in issuer made for another Straza program, such as the console | 403 | ``Straza refused this sign-in because it was made for another Straza program. Run `straza enroll --server https://straza.example.com` on this machine and finish the sign-in it starts.`` |
| A browser sign-in whose nonce names another key, or a nonce of another form | 403 | ``Straza refused this sign-in because it was made for a different enrollment. Run `straza enroll --server https://straza.example.com` again and finish the sign-in in the browser it opens.`` |
| A browser sign-in without `auth_time` | 403 | ``Straza refused this sign-in because the identity provider sent no auth_time claim, so Straza cannot tell that you signed in just now. Ask IT to add the auth_time claim to the ID token of the Straza app, then run `straza enroll --server https://straza.example.com` again.`` |
| A browser sign-in older than 10 minutes, here with an example time | 403 | ``Straza refused this sign-in because your identity provider says you last signed in at 2026-10-22 09:14:03 UTC, more than 10 minutes ago. Run `straza enroll --server https://straza.example.com` again and sign in when the browser asks.`` |
| A browser sign-in more than 1 minute ahead of strazad's clock | 403 | ``Straza refused this sign-in because your identity provider says you signed in at 2026-10-22 09:14:03 UTC, which is ahead of the Straza server's clock. Ask your Straza admin to check the clocks of the Straza server and the identity provider, then run `straza enroll --server https://straza.example.com` again.`` |
| A sign-in on either lane whose `auth_time` is already older than `governance.signInMaxAge` | 403 | ``Straza refused this sign-in because your identity provider says you last signed in at 2026-10-22 09:14:03 UTC, longer ago than your Straza admin allows. Sign out at your identity provider, then run `straza enroll --server https://straza.example.com` again and sign in.`` |
| A code sign-in while `governance.minSignIn` is `browser` | 403 | ``Straza refused this sign-in because your Straza admin requires a browser sign-in, and a sign-in code cannot show which machine it was approved for. Run `straza enroll --server https://straza.example.com` in a desktop session on this machine, and update Straza first if it does not open a browser.`` |
| A person's machine key reported in a custody below `governance.minCustody` | 403 | ``Straza refused this machine because its key is kept in your own account instead of by the Straza background service, and your Straza admin requires the service. Install Straza from your software portal, then run `straza enroll`.`` |

Each refusal writes one `straza.audit.authn` record with the action `login` and the outcome `failure`, whose reason is `strazactl no longer signs in`, `the client's bearer device credential is retired`, `the sign-in was made for another Straza program`, `the sign-in was made for another key`, `the sign-in carries no auth_time`, `the sign-in is older than 10 minutes`, `the sign-in time is ahead of the server clock`, `the sign-in lane is below governance.minSignIn`, `the last sign-in on this machine is older than governance.signInMaxAge` or `key custody is below governance.minCustody`. A person's key enrollment that succeeds writes one `straza.audit.identity` record with the action `device.enrolled`, the user, the device, the lane in `signIn`, the custody, and the provider's `auth_time` in `authTime` when the token carried one. A current straza client reads `min_sign_in` from idp.json and stops before it shows a code, with the same refusal minus the advice to update, so the server sees no request and writes no record.

No enrollment answer carries a device credential. That bearer JWT in the wire field `device_token` came only to strazactl, whose enrollment strazad refuses.


The service then pins the snapshot signing keys from `/.well-known/straza/snapshot-keys.json`, unless a root-owned managed configuration already pins them, and keeps them in the enrollment record beside the key. Those keys are trusted on first use, so they are only as good as the connection the enrollment ran over. Enroll over TLS or on a network you trust, as [Known limits]({{< relref "security/known-limits.md" >}}) advises.

## The session token


A session starts when the harness fires its session-start hook. The hook asks `straza service` for the session of its harness window, which the service names by the caller's user id, the harness and the harness's own session id. A resumed window reuses its live session and a new window gets its own, so the console shows one session per window. `straza mcp`, `straza exec` and `straza connect` do not know their window, so they use the caller's newest live session of their harness and start one only when none is live.

To start one, the service checks in on the key lane described below, or for an AI agent with a fresh ID token and a proof from the agent's key. It sends the attestation it measures itself: its own binary and, on a managed install, the root-owned managed configuration and the managed hook wiring of that harness. strazad checks the proof, consults the denylist before any store read and checks both status rows and the custody. Then it computes the attestation level and mints the session token.

The token binds the user, the session id, the device, the harness, the attestation level, a hash of the sorted role ids, the snapshot id and the key's thumbprint. It lives 300 seconds. Every lane that verifies it accepts it for 30 seconds past that expiry as the clock skew allowance, so a session token is refused 330 seconds after its mint and not 300.


Only the server verifies that signature. The service keeps the session in memory with the check-in's `expires_in` as an expiry time, trusts that clock, and never verifies the token itself. It verifies the policy snapshot's signature, under a key pinned at enrollment, when it fetches the snapshot and when it reads its copy on disk after a restart, and it never enforces a snapshot that fails. The token never leaves the service. What the hooks and `straza mcp` get is the session's id, user, roles and snapshot, and the service adds the token and a fresh proof to every request it sends for them.


The gateway refuses in this order:

| Condition | Status | Message |
|---|---|---|
| No bearer, or a token that fails to verify | 401 | `missing session token`, or `session token rejected` |
| The token's user or device is revoked | 403 | `Straza: this device or user has been revoked. Contact your administrator` |
| Only its session or the token itself is revoked | 403 | `Straza: this session has been revoked. Start a new session: its check-in uses this device's existing enrollment, so re-enrolling is not needed` |
| An attestation level below `governance.minAttestation`, read from the token's own claim with no exemption for admin tools | 403 | ``Straza: attestation level %q is below the required level %q. An administrator installs the Straza package for this system, then runs `straza install --managed --server <server-url> <harness>` with sudo, or as an administrator on Windows.`` |
| A session with no cached subject, or one cached for roles other than those the token names | 401 | `Straza: session state expired. Check in again` |

A restarted pod holds no cached subject, and a cached subject also ends at the next time one of the user's role assignments starts or ends, so the check-in that follows resolves the roles again. A pod serves a cached subject only to a token that carries the hash of that subject's roles. When a role changes and the session's refresh lands on another pod, the new token sends the session to check in again on this pod, so no pod serves a role the token does not name. `/v1/decide` turns the same denylist hit into a deny whose rule id is `revoked`, with the same two reasons. A refresh re-resolves the roles but copies the device binding, the attestation level and the harness name from the stored session row. No refresh can therefore change the device, attestation or harness in the token.

## After a pause


The service refreshes a session when a call for it arrives with 120 seconds or less left on its token. A window nobody uses lets its token run out, and its next call refreshes the session or starts it again. The response determines what happens next:

| Refresh result | Service behavior |
|---|---|
| 401, session no longer usable | Check in once through the enrollment, under the key's lock, to start a new server session for the window through the normal checks. When that check-in gets a 408, a 429 or another status below 500 other than 401 and 403 with no reason, the service keeps the window's session as it was and the next call tries again. Any other refusal of the check-in ends the renewal as a 403 does. |
| 403, user or device revoked | Mark the window's session revoked with the server's sentence, as a revocation push does. The next call denies, and the window's next session start checks in again. |
| Transport failure | Keep the old token and use the verified snapshot only within the expiry and offline-grace rules |

A refresh returns 401 when the token is beyond its clock allowance or the session has been closed. The janitor closes sessions after 600 seconds without a check-in or at `governance.sessionMaxLifetime`, 12 hours by default. A revoked session also returns 401. Revoking its device or disabling its user returns 403.

A renewal the server refused marks the session revoked at once, and the call that renewed it denies with the server's sentence in the revoked reason:

```text
Straza: session revoked (this device or user has been revoked. Contact your administrator). Tool calls stay denied until a new session starts and checks in again. If only this session was revoked, that check-in starts a new session. If the device or user was disabled, the check-in is refused until an administrator re-enables it. Inform the user and stop.
```

A session renews only when a call arrives with 120 seconds or less left, so while its push stream is down, a refusal reaches a window that keeps working at most about 180 seconds after its token was minted. Meanwhile, a window that makes no call decides nothing.

The offline grace, zero in enterprise and 900 seconds in standalone, is added only when the renewal cannot learn the server's judgment. That happens in four cases:

- No answer came back, or the answer was a server error.
- Something in front of strazad answered with a timeout or a rate limit.
- The answer was a status other than 401 or 403 with no reason.
- The server refused the token, and the check-in through the enrollment could not complete.

A renewal can also succeed while straza cannot fetch the newer policy it names. This is the case when the server refuses the fetch, redirects it to another origin or 10 times in a row, or sends a snapshot the keys this machine pinned cannot verify. Every renewal counts.

The service then keeps the reason and the session time it held at that moment. Once that time plus the offline grace has run out, it denies every governed call of that window, however often the token renews, until a fetch of the policy succeeds. `straza doctor` shows the state as a failing `policy` line with the reason. A network failure, a server error, or a renewal that names no policy does not start this, so an outage alone never stops work.

After that gate closes, the hook reports the applicable reason:

```text
Straza: session token expired %s ago and automatic renewal failed (%s). Try the call once more in a minute. If it is denied again, tell the user to start a new session or run `straza doctor`.
Straza: session token expired %s ago, and the renewal got an answer that does not say whether this session is still accepted: %s Try the call once more in a minute, which tries the renewal again. If it is denied again, tell the user to check that the configured server URL reaches strazad with nothing in front of it that limits or blocks straza, and to run `straza doctor`.
Straza: session token expired %s ago and the offline grace period (%s) is exhausted. Straza cannot verify current policy while the platform is unreachable. This is a connection problem, not a policy decision. Try the call once more in a minute. If it is denied again, tell the user to check the network and run `straza doctor`.
Straza: this session's policy is out of date: the server holds a newer policy that straza could not fetch. <reason> Tool calls stay denied until straza can fetch the current policy, and `straza doctor` shows this reason.
```

Each `%s` stands for the token's age, then the failure, the answer or the grace period, and `<reason>` names what failed and what to do. A refused token whose check-in through the enrollment could not complete gives the first. An answer that judged nothing, a 408, a 429, or a status other than 401 or 403 with no reason, gives the second. Losing the server entirely gives the third, and the service never claims the server was unreachable after it answered. The fourth is the policy that could not be fetched.

## Enrollment bound to a key


Every enrollment that `straza service` makes binds a person's machine to an Ed25519 key that never leaves the service, and this page calls that the key lane. The enrollment request carries a DPoP proof (RFC 9449), a short JWT that the key signs over the request and a nonce from strazad, with the public key in its header. Its body adds the custody the service reports for the key, a machine binding and a counter that starts at 0. strazad records the proof's id before it judges anything, so a replayed enrollment is refused, and verifies the sign-in as it does for strazactl. It stores the key's thumbprint on the device row. The answer carries the device id, the thumbprint and the seconds left until the sign-in deadline, and no device credential.

A check-in on the key lane carries no token. It carries a fresh proof from the key and a counter above the last one strazad accepted, and the proof signs that counter too. strazad checks the nonce, the proof and that the key is the one on the device row. It then records the proof's id in the database, so the same proof replayed to any replica is refused. It checks the denylist, the user's status and the sign-in deadline, raises the counter, and then checks the custody. The session token it mints carries the key's thumbprint as `cnf.jkt`. Every route that accepts that token then needs a fresh proof from the same key on each request, so a copied token works nowhere else. A refresh also needs such a proof, and it fails once the machine has enrolled a new key.


Many harness windows on one machine share its key, so the service takes one lock per key for every key check-in, the start of a session and the check-in after a refused refresh alike. It writes each counter to disk and syncs it before the request leaves, and it never sends a number twice. When the answer to a check-in is lost, the key's next check-in waits two minutes from when that request's nonce arrived, so a late copy of the lost request fails at the nonce check. A session start inside that wait fails closed with the waiting sentence of [The background service](#the-background-service).

A counter at or below the last one accepted means that the key was also used from a copy of the machine, such as a cloned VM or a restored backup. Only a valid proof reaches that check, so nobody without the key can trigger it. strazad revokes the device the way `strazactl devices revoke` does and writes one `straza.audit.identity` record with the action `device.key.reused`. A key-bound token that arrives without a valid proof gets one `straza.audit.authn` record with the action `proof` and the outcome `failure`, the trace a copied token leaves. A device enrolled with a key accepts only that key, so neither a sign-in token nor a device credential starts a session for it.

Beside each key, the service keeps an HMAC of the machine identity in `/etc/machine-id`, or of a random identity it makes in its state directory when that file is missing. When the identity changes, as it does in a cloned VM that got a new machine id, the service stops using the key and answers with the machine-changed sentence. In a container without a machine id, a copy keeps the random identity, so detecting it rests on the server's counter alone. On Windows the identity is the MachineGuid value under `HKLM\SOFTWARE\Microsoft\Cryptography`. The service refuses to start when it cannot read that value, and it never makes an identity of its own there, because one kept in its folder would travel with a copy of the folder. On macOS the identity is the Mac's hardware UUID, the `IOPlatformUUID` that `/usr/sbin/ioreg` reports, which stays the same across macOS updates. There too the service refuses to start when it cannot read it, and it makes no identity of its own.


On Windows the service renames each counter file into place with the rename written through to disk, then flushes the file. Microsoft does not document that this write survives a power cut. When the last counter is lost, the machine's next check-in sends a number the server already accepted, so the server revokes the device as a copy, with the copy sentence below and a `device.key.reused` record, although nobody copied the machine. Each person on that machine then runs `straza enroll` again.

The sign-in deadline is `governance.signInMaxAge`, 7 days by default in both profiles. It is how long a key enrollment lasts, counted from the sign-in at the identity provider that the enrollment used, the ID token's `auth_time`, or from the enrollment when the token carries none, and use never renews it. A browser sign-in asks the provider for a fresh authentication and is refused when its `auth_time` is older than 10 minutes, so the deadline starts when the person last authenticated. A code sign-in has no such check, so whether the provider asked the person to authenticate again is its own session policy. Past the deadline strazad refuses the next key check-in and the next refresh, so a session ends within one token lifetime, 300 seconds, of the deadline, even inside its 12 hours. When your policy follows NIST SP 800-63B-4 at AAL2, which sets 24 hours, lower it and set the identity provider's reauthentication period to match.


AI agents have no sign-in deadline, because they prove themselves at every session start. The service's check-in for an agent carries a proof from the agent's key, so strazad binds the agent's session to that key too.

The new messages of the key lanes include these, and the deadline message names the configured period:

| Condition | Status | Message |
|---|---|---|
| A key-bound token without a valid proof, or sent under `Bearer` | 401 | `This session token belongs to a key on the machine that checked in, and this request carried no valid proof from that key. Send it from the Straza client on that machine. A copied token works nowhere else.` |
| A proof with a missing or stale nonce, answered with a fresh `DPoP-Nonce` header and no audit record | 401 | `Straza needs a fresh proof nonce. Sign the request again with the nonce in the DPoP-Nonce header.` |
| A check-in counter at or below the last one accepted | 403 | `Straza stopped trusting this machine because its key was also used from a copy of it, such as a cloned VM or a restored backup. Run straza enroll to enroll this machine again, and tell your Straza admin if nobody copied it.` |
| A check-in or refresh past the sign-in deadline | 403 | ``Your last sign-in on this machine was more than 7 days ago, and your Straza admin requires a new one. Run `straza enroll --server https://straza.example.com` and sign in with your work account.`` |
| A key below `governance.minCustody` at a check-in or refresh, after the setting was raised | 403 | the custody message of [Enrollment](#enrollment) |
| A sign-in token or a device credential for a device enrolled with a key | 403 | `This machine checks in with its own key, so a sign-in token or device credential cannot start a session for it. Check in from the Straza client on that machine.` |
| A refresh of a session whose machine has enrolled a new key since | 401 | `This session was started with a key this machine no longer uses, because the machine was enrolled again. Start a new session, and the Straza client checks in with the new key.` |
| An agent's check-in whose proof fails | 401 | `the DPoP proof was not accepted: <reason>. Sign a fresh proof with this agent's key.` |

## Revocation and lockout


Three actions stop a credential, each at its own scope:

| Action | What it does |
|---|---|
| A session revoke, with `strazactl sessions revoke` or the session's own logout | Ends the session and leaves the enrollment. It marks the row revoked, writes a revocation record and adds the session to the denylist. The machine's next check-in starts a new session. |
| A device revoke, with `strazactl devices revoke` | Deletes the device row and adds the device to the denylist, with a revocation record, and ends the device's active sessions in the same write, with one `session.end` record each. It records `device.revoke` on the audit chain with who revoked it. No lift exists for a device. |
| Disabling a user, with `strazactl users disable` | Adds the user to the denylist and revokes every active session of theirs, recording `user.killed` on the audit chain. |

None of the three touches an admin API token, which belongs to no session or device. A token keeps working after the person who minted it is disabled or locked, so revoke the tokens that Settings, API tokens lists as minted by that person.

A session revoke marks its row revoked and a device revoke deletes its row and marks the device's active sessions revoked, and each writes the revocation record and the event that tells the other replicas in that same transaction. When the database refuses any of them nothing changes, the answer says so with its request id, the replica that answered refuses the target at once, and running the same revoke again ends it.

The cascade logs at Error every write it cannot make, whether the revocation record, a session row, the event that carries the revoke to the other replicas or the `user.killed` record. The `user.killed` record and that event are tried once more first, and one still not written is counted in `straza_outbox_records_lost_total`. A session row it could not mark revoked is left out of the count in `user.killed`, and the user's denylist entry still refuses that session. SCIM and the lock route run the same cascade as the admin API, and SCIM also deletes the user's OAuth grants. Deleting a user over the admin API writes one `user.delete` record that names who deleted the user, then deletes the user's OAuth grants, an AI agent's key and a person's approver devices, and each approver device's token answers device_revoked at its next request.


Each replica holds the denylist in memory, so no request path reads the store for it. It rebuilds the list at boot from the persisted revocation rows, refuses to serve when that read fails, then replays `straza.revocation.>` from the event stream's beginning. A session entry is never lifted. The cost is a list that only grows and a boot that replays every revocation before serving.


`straza service` is the kill switch on the machine. For each live session it opens `GET /v1/push` with the session token and its proof, and holds an event stream for that session, its user and its device. A revocation arrives as one event. The service marks that window's session revoked, drops its token and keeps running, and the next hook call in the window denies with this reason:

```text
Straza: session revoked (kill-switch push from the server). Tool calls stay denied until a new session starts and checks in again. If only this session was revoked, that check-in starts a new session. If the device or user was disabled, the check-in is refused until an administrator re-enables it. Inform the user and stop.
```

While a session's stream is down, the revocation reaches the window at the first call that renews the session, once 120 seconds or less of its 300-second token remain, so at most about 180 seconds after the token was minted for a window that keeps working. A stream that reconnects and is refused makes the service renew at once. That refused renewal marks the session revoked with the server's sentence in both profiles, and a window that makes no call decides nothing in the meantime. The stream is down when strazad cannot be reached, when something in front of it drops or buffers the stream, or after the session's token ran out in a window nobody used. The gateway checks the denylist on every request and refuses at once. A standalone service whose renewal cannot learn the server's judgment, because strazad cannot be reached or something in front of it answers instead, keeps deciding past the token's expiry for its offline grace, up to 1,200 seconds after the mint. A session the service starts again for a window gets a stream of its own. [Known limits]({{< relref "security/known-limits.md" >}}) lists this delay with what shortens it.


After a revoke, each route answers in its own way:

| After | A request to | Answer |
|---|---|---|
| A session revoke | Refresh | 401 `session is no longer active`, from the denylist before the row is read |
| A session revoke | The gateway | 403 with the session message of the gateway table above |
| A device revoke | Check-in, or a refresh from that device | 403 with the device or user message of the gateway table above |
| A user disable | A new login | 403 `this user is disabled, so Straza refuses this request. Ask your IGA owner or a Straza administrator to enable the user.` |
| A disable or a lock | The user's live console session, at every replica whose denylist holds the user | 403 `Straza: this device or user has been revoked. Contact your administrator` |
| A disable or a lock | The same session, at a replica that has not heard of the revoke yet | 401 `session is no longer active`, because the cascade marked the session revoked, or 403 `this user is disabled, so Straza refuses this request. Ask your IGA owner or a Straza administrator to enable the user.` after a disable whose session write failed |

The admin API and the routes that decide a request judge a session the same way on every request. The connect routes and `GET /v1/self/servers` judge a session the same way, a straza session included. In the browser, the console drops the session at its next refresh.

Reactivation over SCIM deletes only the rows SCIM wrote and lifts the denylist entry when no admin or external lock remains, recording `user.reactivated` or, when one does, `user.lift.blocked`. Revoked sessions and deleted OAuth grants stay that way, and the device enrollment is untouched.

## What sits on disk


`straza service` keeps its state in its state directory: `/var/lib/straza-service` for the system service on Linux, `/var/db/straza-service` on macOS, `C:\ProgramData\straza-service` for the Windows service and `$STRAZA_HOME/service` for a service started with `STRAZA_HOME` set. The directory holds a `lock` file that lets one service use it at a time, a `machine-id` file only on Linux when `/etc/machine-id` is missing, and one directory per enrolled user id under `u/`. On Windows each of those directories is named by the account's SID. Each of those holds the enrollment record `enrollment.json`, the key `key.pem`, the signed snapshot `snapshot.cbor` and, while records wait to upload, the audit spool `audit-spool.jsonl`. The record names the server, the user, the device id, the key's thumbprint, the reported custody, the pinned snapshot keys, the machine binding and the last counter. An agent's identity provider client secret, when it has one, is a file the service reads from the directory that systemd's `LoadCredential` names, or from `credentials/` in the state directory without it. [Headless agents]({{< relref "guides/govern-an-agent/headless-agents.md#protect-the-key" >}}) shows the drop-in that passes it.

The service also saves the sessions it holds for each user in `sessions.json` in that user's directory, with their tokens and revoked marks, and reads them back when it starts. A restart, such as the one a package upgrade makes, therefore keeps every open window's session. A file it cannot read is logged, and that user's windows then start their sessions again through the key lane. Every file is plain, with mode 0600 in directories of mode 0700 that belong to the service's own account, and nothing is encrypted or kept in a TPM or a keychain. On Windows the folder's access rules, which let in only the service, SYSTEM and Administrators, take the place of the modes. On macOS the Straza package keeps Time Machine from backing up the state directory. [Known limits]({{< relref "security/known-limits.md" >}}) lists the plain key file with what reduces the risk.


`~/.straza`, or `STRAZA_HOME` when set, holds only what the straza commands keep beside the harness, and none of it is a credential. `config.yaml` holds the trace setting, `state/trace.jsonl` the decision journal and the debug trace, `state/errors.jsonl` the client error log, and `state/capture-*.offset` how far conversation recording has read each transcript. A root-owned `/etc/straza/config.yaml` from a managed install wins over the user `config.yaml`. On Windows that file is `C:\ProgramData\straza\config.yaml`, and Straza counts it only while accounts other than administrators cannot change it or its folder. The managed config names the server the service enrolls with, which the service reads only at enrollment, so each person enrolls again after an administrator changes it. It also names the snapshot keys, which the service reads each time it fetches a new policy and when it starts. A managed config that cannot be read or parsed verifies no policy until it is fixed, so the hooks deny.


`strazactl` writes nothing to disk. It reads its server from `--server` or `STRAZA_SERVER` and its admin API token from `STRAZA_API_TOKEN`, so the token sits wherever you put it. A `~/.straza/credentials.json` that an older strazactl wrote works nowhere and can be deleted.

## Where the design stops


An admin API token is a bearer secret. Nothing binds it to a machine or a person, so a copy acts with the token's scope from anywhere until the token expires or is revoked, and where a person puts the token is the boundary. No Straza program holds a bearer enrollment credential, because strazad refuses the straza client's bearer enrollment and check-in, and strazactl's sign-in and device credential. The fingerprint is the hostname, whose only job is to make a repeat enrollment reuse the device row. It identifies nothing, and no device certificate factor is built.

The custody and the attestation that an enrollment or a check-in reports are the client's word. strazad cannot tell whether `straza service` sent them or another program that holds a fresh sign-in of the person, so `governance.minCustody` and the attestation minimum keep out a wrong install and not a program written to lie. On Linux, macOS and Windows, the system service measures its own binary and the managed files under its own OS account, which a process of the person cannot change.

In the standalone profile an AI agent's check-in without a proof still mints a session token bound to nothing, and the enterprise profile refuses it with a sentence that names the agent and says to enroll it with a current `straza service`. The service always sends a proof. A client older than this version, which kept the agent's key or client secret in the agent's own files and environment, does not. Once this version runs beside your agents, register each agent's new key, which `straza keygen` asks the service to make, with `strazactl users nhi-key set`. Rotate every identity provider client secret that sat in an agent's environment too, so the old copies stop working.

Under enterprise strazad also refuses a person's ID token at a check-in under a coding harness name, so a token minted from the person's browser session starts no session outside `straza service`. The standalone profile keeps that lane, because its built-in issuer asks for the password at every sign-in. A person's raw ID token still opens the admin and person routes while it lives.

A copy of the database is another place the design stops. The session signing key's Ed25519 seed sits unencrypted in the `signing_keys` table, so a copy mints session tokens for any user. It yields no admin API token, the identity manager's SCIM credential included, because those are stored as SHA-256 hashes. [Keys, certificates and tokens]({{< relref "security/keys-certificates-and-tokens.md#what-a-copy-yields" >}}) lists everything a copy yields, and [Known limits]({{< relref "security/known-limits.md" >}}) says what to do after a suspected copy.

The sign-in routes carry one per-address limit, `server.loginPerIPRPS`, at 2 requests per second by default. It covers the password submits of the built-in sign-in pages, `/oidc/device` and `/oidc/authorize`, in both profiles and the headless token endpoint in the enterprise profile, so `/v1/enroll` and `/v1/checkin` carry none and nothing counts failed attempts. The start of a device code sign-in, `/oidc/device_authorization`, has the same rate from a budget of its own, so starting sign-ins never uses up the password attempts. That route accepts only the client ids straza, strazactl and console, and strazad holds at most 4096 device code sign-ins at once. Past that it refuses new ones. Every per-address limit counts an IPv6 client by its /64 network, so rotating addresses inside one network gains nothing. Four IPv4 addresses, or four IPv6 /64 networks, can keep it full for as long as they keep starting sign-ins, and meanwhile the browser sign-in keeps working and the table stays at 4096 sign-ins. [Hardening]({{< relref "security/hardening.md#security-settings-and-their-defaults" >}}) lists every rate limit and the body cap with their defaults.

The phone approver holds a P-256 key. Both a decision and the 30 day renewal of its token cost a fresh signature over a single-use challenge, so a copied approver token can neither decide nor renew. That key proves which device decided, and it does not prove that a person acted. An AI agent that runs as the person can enroll an approval device of its own with any credential of the person it can read, such as an admin API token left in its reach, as [the security model]({{< relref "security/security-model.md#an-agent-that-holds-its-persons-login-can-enroll-an-approval-device" >}}) explains. [Known limits]({{< relref "security/known-limits.md" >}}) lists what reduces that today.

On Linux, macOS and Windows, `straza service` holds an AI agent's Ed25519 key, or reads its identity provider client secret from a file only the service can read, and mints a short token per session. Agents therefore have no device credential and their sessions are deviceless, as [Machines and sessions]({{< relref "concepts/machines-and-sessions.md#an-agent-with-its-own-identity" >}}) describes.

### Limits on Windows


This version leaves four gaps on Windows:

- Commands that Codex CLI and Gemini CLI run inside their sandboxes run as another account or at a lower integrity level, and they cannot reach the service. A Straza command run there, such as `straza exec`, gets no session and stops. Claude Code on Windows runs its commands outside a sandbox, so they reach the service.
- An agent that runs inside WSL2 is not governed, because nothing connects WSL2 to the service yet.
- The Windows binaries and the MSI carry no Authenticode signature yet, so SmartScreen may warn when one runs for the first time. The cosign-signed checksums of [Install]({{< relref "guides/operate/install.md" >}}) show where a file came from.
- After a power cut the service may have lost its last check-in counter, and the machine must then enroll again, as [Enrollment bound to a key](#enrollment-bound-to-a-key) explains.

[Known limits]({{< relref "security/known-limits.md" >}}) lists each gap with what reduces it today.

### Limits on macOS


This version leaves four gaps on macOS:

- A package built from the source tree without a Developer ID is unsigned and not notarized. Releases from 2.0.0 carry the Straza package for macOS signed with a Developer ID and notarized by Apple, as [Install]({{< relref "guides/operate/install.md#install-the-service-on-macos" >}}) shows, and releases from 1.1.2 and earlier carry none.
- On a Mac without a Managed Login Items profile, which only a device management tool installs, the service can be switched off under Login Items & Extensions, and while it is off every governed tool call is refused. [Deploy with Intune]({{< relref "guides/govern-an-agent/deploy-with-intune.md#deploy-to-macos" >}}) and [Deploy with Jamf Pro]({{< relref "guides/govern-an-agent/deploy-with-jamf.md" >}}) install that profile.
- Claude Code uses only the highest of its managed sources, and a Claude Code profile from a device management tool ranks above the managed settings file. On a Mac with such a profile, Claude Code skips Straza's hooks with no warning.
- A shell command that Claude Code runs in its sandbox, when an administrator turns the sandbox on, cannot reach the service's socket, because the wiring does not name it in the sandbox's `allowUnixSockets` setting.

## How to check it


`straza doctor` prints one line per check and a hint under any line that is not ok. The service, enrollment, session and killswitch lines read what the service holds for you, and `straza status` prints the same facts in short. On a standalone walk machine, with alice enrolled through a service started with `STRAZA_HOME` and one live session, they read as follows, trimmed to those four lines:

```text
[ ok ] service     straza service v1.0.0-1570-gaf326165-dirty answers at /root/.straza/service.sock and keeps keys in user custody
[ ok ] enrollment  alice (device 01a15d8a-bc3b-7821-8651-c3cd0d22f431) at http://127.0.0.1:8420, key jotq8ezInO5o, straza v1.0.0-1570-gaf326165-dirty
[ ok ] session     claude-code 01a15d8a-bd38-7f04-a43b-1b1090003ec7 as alice (token valid 4m57s)
[ ok ] killswitch  push stream connected for all 1 session(s): a revocation reaches this machine within a second
```

The enrollment line names the key's thumbprint and turns to a failure when the machine identity changes, and to a warning during the wait after a lost check-in answer. On the same machine, after the `machine-id` file in the state directory was changed and the service restarted, it reads:

```text
[FAIL] enrollment  alice (device 01a15d8a-bc3b-7821-8651-c3cd0d22f431) at http://127.0.0.1:8420, key jotq8ezInO5o, straza v1.0.0-1570-gaf326165-dirty: this machine's identity changed since it was enrolled, so Straza stopped using its key
                   → Run `straza enroll --server http://127.0.0.1:8420` to enroll it again.
```


Administrators list and revoke devices with `strazactl` on an admin API token that holds `identity:read`, and `identity:write` for a revoke. A machine that `straza enroll` enrolled is named by its host name. Revoking takes the username and the device ID from the list:

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl devices list alice
strazactl devices revoke alice <device id>
```
{{< /command >}}

The revoke deletes the device row and adds the device to the denylist, so the machine's next check-in is refused until the person runs `straza enroll` again. It ends the device's active sessions in the same write, and each ended session gets one `session.end` record. [The kill-switch runbook]({{< relref "guides/operate/kill-switch-runbook.md" >}}) covers the lockouts, and [Doctor and logs]({{< relref "guides/operate/doctor-and-logs.md" >}}) the rest of the doctor output.
