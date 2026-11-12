---
title: Backup and upgrade
description: Take a backup that restores the whole server, restore it and rehearse the restore, upgrade strazad without logging anyone out, and know the way back.
summary: >-
  A backup is the store, SQLite or Postgres, plus three key files beside it: the key-encryption
  key, the approver TLS pair and the WebPush key. An upgrade restarts on the newer binary, which
  migrates the store at boot, and `strazad migrate --to` or a restore is the way back. Only an
  upgrade from 1.1.2 to 2.0.0 makes people enroll again.
pagetype: how-to
weight: 70
draft: false
tested:
  version: v1.0.0-1500-g0cd3e356
  platform: Alpine 3.20 containers in the standalone profile, laid out as the standalone host guide describes. One container ran the backup, two restores that left out secret.key and approver-tls, a restore without secret.key over a store that held a client assertion key, the restore steps, the Verify list with a governed tool call and straza doctor on a client enrolled before the backup, the upgrade block with a copy of the same build standing in for the new binary because no later release exists, the migrate refusal and the refusal of a store set to version 30. A second container ran the drill, where a form post with the admin password stood in for the browser sign-in. The pg_dump and pg_restore commands and dropdb ran against a Postgres 16 container with strazad in the standalone profile on that database. The enterprise profile, Kubernetes, the WebPush key, the apps export and the console sentences were not run. On 2026-10-21 the two straza doctor outputs were captured again in a third Alpine container with a working tree of the background service as v1.0.0-1570-gaf326165-dirty, where straza service ran with STRAZA_HOME, alice enrolled and started a session before a copy of the data directory was put back, and admin enrolled and started one session after it. That container also put a copy of a service's state directory back and saw the next session start refused as a copied machine. The upgrade text for older clients was not run, and the section Upgrade from 1.1.2 to 2.0.0 was written from the code on 2026-11-01 and not walked. The Windows sentences were written from the code on 2026-10-22 and 2026-10-23 and not run by these steps. On 2026-10-26 a test script installed MSIs built from the source tree at v1.0.0-1600-g3c02a286 on a Windows 10 machine, upgraded one while hooks ran, saw an older one refused, and uninstalled and purged them. The macOS sentences were written from the code on 2026-10-26. An unsigned package built from the source tree was upgraded in place, uninstalled, installed again and purged in a macOS 26.6.2 virtual machine on 2026-10-26 at v1.0.0-1610-g366fac50, by a test script and not by these steps. On 2026-10-27 the same script did the same with a pkg built at v1.0.0-1615-g272946e5, signed with the Developer ID and notarized by Apple
  except: the enterprise, Kubernetes and Windows steps, the WebPush key, the console, and the upgrade from 1.1.2 to 2.0.0, written from the code
  date: 2026-10-07
applies_to: both
who: Whoever is on call for the server
where: Terminal on the strazad host, or your Postgres and Kubernetes tooling
steps: true
keywords: backup upgrade migration migrate restore rollback vapid kek drill pg_dump pg_restore disaster recovery package remove purge decommission macos pkg uninstall major release 2.0.0 1.1.2 redirect re-enroll
---


You back up the state strazad depends on and upgrade the binary, as whoever is on call for the server. You work in a terminal on the strazad host, or with your Postgres and Kubernetes tooling. At the end the new build runs on the same store, nobody had to log in again, and you hold a backup that restores the whole server. The upgrade from 1.1.2 to 2.0.0 is the exception, because every machine gets a new client and every person enrolls again, as [Upgrade from 1.1.2 to 2.0.0](#upgrade-from-112-to-200) shows.

The store is the record, a SQLite file in the standalone profile and a Postgres database in the enterprise profile. Users, roles, policy, sessions, the signing keys and the hash-chained audit log all live in it. Three files outside the store matter as much, the key-encryption key and two key pairs that clients pinned. A restore that brings the store back without them fails the sealed credentials closed and strands the clients that pinned the pairs.

An upgrade is a restart on a newer binary, which migrates the store forward at boot. The way back is `strazad migrate --to`, run with the newer binary before the older one starts, or a restore of the backup you took before the upgrade. Rehearse that restore on a spare host, so the steps are known before an outage needs them.

## Know what to back up


Five things make up a deployment, and each fails differently when it is missing after a restore:

| What | Where it lives | What breaks without it |
|---|---|---|
| the store | `straza.db` in the data directory, or your Postgres database | everything: identities, policy, sessions, signing keys and the audit chain are gone |
| the key-encryption key | `secret.key` in the data directory, or `secrets.kekFile` | stored credentials cannot be opened and the MCP servers that use them fail closed |
| the approver TLS pair | `approver-tls/` in the data directory, or your own pair | the Straza approver app's pin no longer matches and every approver enrolls again |
| the WebPush signing key | wherever `approval.push.webpush.vapidKeyFile` points: the data volume in the repository compose file, a Secret on the chart | every browser push subscription is bound to the old key and stops receiving |
| the config file | wherever you put `straza.yaml` | the server boots with defaults, or refuses to boot when it named a missing file |


The two key pairs are the ones people forget. Both are secrets, and each is also an identity that clients pinned, so a new pair is a rotation with a cost measured in re-enrollments. Treat them like the store and never like something a fresh boot can regenerate for free.


The agent machines hold none of the server's state. On each of them `straza service`, the Straza background service, keeps every enrolled user's key and enrollment in its state directory: `/var/lib/straza-service` for the system service on Linux, `/var/db/straza-service` on macOS, `C:\ProgramData\straza-service` for the Windows service, or `$STRAZA_HOME/service` for a service started with `STRAZA_HOME` set. A person's enrollment carries a check-in counter, and a copy restored from a backup is behind the counter the server stored, so its first check-in makes the server revoke the device as a copy. Leave those directories out of the server's backup, and enroll a machine whose state is lost again with `straza enroll`. The Straza package for macOS keeps Time Machine from backing up its directory. Removing the `straza` package can delete that state, as [Remove Straza from a client machine](#remove-straza-from-a-client-machine) shows.


The manifests of your MCP servers are rows in the store, so the store backup holds them. `strazactl apps export` is no substitute, because it prints every environment value and every value that looks like a secret as `[REDACTED]`, and a file in the apps directory that still holds such a mask is refused.

## Back up a standalone server


The SQLite store runs in write-ahead-log mode, so a copy taken while the server writes can catch the database and its log at different moments. Stop the server, wait until it has exited, copy the whole data directory and the config file, and start it again.

The commands use the layout from [Standalone host]({{< relref "guides/operate/standalone-host.md" >}}), with the data directory at `/var/lib/straza` and the config file in `/srv/straza`. Name the copy after the day you take it, as the example does with 2026-09-19.

{{< command terminal="Terminal" purpose="on the strazad host" >}}
```sh
kill "$(pidof strazad)"
while pidof strazad >/dev/null; do sleep 1; done
mkdir -p /var/backups
cp -a /var/lib/straza /var/backups/straza-2026-09-19
cp /srv/straza/straza.yaml /var/backups/straza-2026-09-19.straza.yaml
du -sh /var/backups/straza-2026-09-19
ls /var/backups/straza-2026-09-19 /var/backups/straza-2026-09-19/approver-tls
```
{{< /command >}}

```text
712.0K	/var/backups/straza-2026-09-19
/var/backups/straza-2026-09-19:
approver-tls
apps
nats
secret.key
straza.db

/var/backups/straza-2026-09-19/approver-tls:
cert.pem
key.pem
```


{{< see >}}`secret.key`, `straza.db` and `approver-tls` with both of its files in the copy.{{< /see >}}

The copy holds the store, the key-encryption key and the approver pair, and its `nats` directory is the embedded event bus's storage, which comes along with the rest. The WebPush key is in the copy only when `approval.push.webpush.vapidKeyFile` points into the data directory. Start the server again with `cd /srv/straza && strazad serve`.

### Or back up an enterprise deployment


Dump the Postgres database with the tooling you already run for it, and back up the three key files: the key-encryption key, the approver TLS pair you brought, and the WebPush key. Where those files live depends on how you deploy:

- On Kubernetes all three are Secrets you created, `secrets.kekExistingSecret`, `approverTLS.existingSecret` and `webpush.existingSecret`, so a backup of the namespace's Secrets covers them.
- With the repository compose file, the key-encryption key and the WebPush key sit in the data volume, so back up the volume as well.

The signing keys for sessions and snapshots need no separate step, because they are rows in the database.


If you have no database backup tooling yet, `pg_dump` takes a consistent copy while strazad keeps serving. Point the Postgres client tools at your server with the usual libpq variables, such as `PGHOST` and `PGUSER`, as the role that owns the database. The example names the database `straza`, as the repository compose file does.

{{< command terminal="Terminal" purpose="with the Postgres client tools" >}}
```sh
pg_dump --format=custom --file=straza-2026-09-19.dump straza
```
{{< /command >}}

{{< see >}}No output, and the file `straza-2026-09-19.dump` in the current directory.{{< /see >}}

The custom format is the one `pg_restore` reads in [Or restore an enterprise database](#or-restore-an-enterprise-database).

## Upgrade


An upgrade is a restart on the new binary. At boot, before the line `store ready`, the server applies every embedded migration the store has not seen, so an upgrade needs no separate migration step. Take a backup first.

To upgrade from 2.1.0 to 2.2.0, follow [Upgrade from 2.1.0 to 2.2.0](#upgrade-from-210-to-220), because that release migrates the database and the order of the steps matters.

To upgrade from 1.1.2 to 2.0.0, follow [Upgrade from 1.1.2 to 2.0.0](#upgrade-from-112-to-200) first, because that release also changes every governed machine and the order of the steps matters.

On the standalone host, download and verify the new release as [Install]({{< relref "guides/operate/install.md" >}}) describes. Then, from the directory that holds the new `strazad` and as the user that owns the installed one, stop the server, put the new binary in place of the old one and start it again.

{{< command terminal="Terminal" purpose="on the strazad host" >}}
```sh
kill "$(pidof strazad)"
while pidof strazad >/dev/null; do sleep 1; done
install -m 0755 strazad "$(command -v strazad)"
cd /srv/straza && strazad serve
```
{{< /command >}}

{{< see >}}The `strazad serving` line names the new version, and the `project identity` line names the same project as before the upgrade. That is the sign that the new binary found its store.{{< /see >}}


A later release upgrades the `straza` package the way [Install]({{< relref "guides/operate/install.md#install-the-package-on-linux" >}}) installs it, from the new release's file. The upgrade restarts `straza service` on the new binary while systemd keeps its socket open, and the service reloads the sessions it saved, so harness windows that are open keep their sessions.


On Windows a later release upgrades through its MSI, installed the way [Install]({{< relref "guides/operate/install.md#install-the-service-on-windows" >}}) installs it, while harness windows stay open. The upgrade stops `straza service` while it replaces `straza.exe` in place, then starts the service on the new binary, and the service reloads the sessions it saved. A hook that runs while the service is stopped is refused with the sentence that the background service isn't running, and the tool call does not go through. An exit code of `3010` means the upgrade succeeded and a restart finishes removing an old file that was in use. An older MSI than the installed one refuses to install.


On a Mac a later version upgrades through a new Straza package for macOS, the pkg of a later release or a package built from the source tree, installed the way [Install]({{< relref "guides/operate/install.md#install-the-service-on-macos" >}}) shows, while harness windows stay open. When one of the package's checks refuses the upgrade, it does so before any file lands, and the old service keeps running. Otherwise the Installer replaces `/opt/straza/bin/straza`, the package then stops the old service and starts the new one, and the service reloads the sessions it saved. A hook that runs while the service is stopped is refused with the sentence that the background service isn't running, and the tool call does not go through.


This release's migrations start at version 37. Only a build from before the first public release can have made a store below that version.

{{< fails >}}
`the database is at migration 30, and this strazad's migrations start at 37, so it cannot upgrade it. Upgrade the database with an earlier strazad release whose migrations reach 37, then start this one`
: 30 stands for your store's version. Start that store once on an earlier build whose migrations reach 37, wait for its `store ready` line, and stop it. Then start this release on the same store.
{{< /fails >}}


In a container, remove the container and start it again from the new image with the same volume and the same arguments, as [Docker]({{< relref "guides/operate/docker.md" >}}) shows. On Kubernetes, `helm upgrade` with the new `image.tag` rolls the pods, as [Kubernetes with Helm]({{< relref "guides/operate/kubernetes-with-helm.md" >}}) shows. In the enterprise shape a rolling restart costs each session one 401 and no new login, as [The enterprise shape]({{< relref "guides/operate/enterprise-shape.md" >}}) explains.

## Verify


Four checks close an upgrade:

1. `/version` or `strazactl status` reports the new build.
2. `strazactl audit verify` re-hashes the chain and reports it intact.
3. A governed session's next tool call decides normally.
4. `straza doctor` on a client machine shows the server line green with the new version.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl status
strazactl audit verify
```
{{< /command >}}

{{< see >}}`strazactl status` names the new build and reports `ok` on its last line, `status`. `strazactl audit verify` answers `audit chain intact` with the number of records it checked.{{< /see >}}


In the console, the foot of the sidebar names the profile and the strazad build, and its tooltip adds the commit. The Overview card re-hashes the 25 newest records in your browser, and the Audit screen re-hashes every record it has loaded, starting with the newest 200. Only `strazactl audit verify` checks the whole chain.

## Upgrade from 2.1.0 to 2.2.0 {#upgrade-from-210-to-220 .nostep}


Straza 2.2.0 is a minor release that migrates the database, and from this release on strazad is upgraded before strazactl. Do the steps in this order.

1. Take a backup, as [Back up a standalone server](#back-up-a-standalone-server) or [Or back up an enterprise deployment](#or-back-up-an-enterprise-deployment) shows. Migration 42 runs at the first start of strazad 2.2.0.
2. Check the scripts that call the admin API. A request body with a field the route does not take now gets a 400 that names the field, where strazad used to drop the field.
3. Review every policy rule that uses `interpreters`. A command that starts with `eval` now counts as an interpreter, so a rule that denies interpreters also denies `eval "$(ssh-agent -s)"`.
4. Look for policy rules that send shell commands to the classifier. It now denies the Homebrew install command, which runs a download as the payload of `bash -c`.
5. In the config that strazad 2.2.0 starts with, give each audit sink that must not hold secrets a `subjects` list without `straza.audit.tool`, as [Events]({{< relref "reference/events.md" >}}) shows. A tool's audit record now keeps the tool's input unmasked.
6. Upgrade every strazad replica, as [Upgrade](#upgrade) shows. During a rolling upgrade a 2.2.0 replica answers 401 to an older replica's session token for an agent that holds an Entra agent identity, and the client checks in again by itself.
7. Upgrade strazactl after strazad.
8. Install the 2.2.0 `straza` client on each machine whose person enrolls through Microsoft Entra.

Two things wait until every replica runs 2.2.0. Add `entraBlueprint` to the config only then, because an older replica refuses to start with it. Rotate the client assertion key only then, and after each `strazactl signing-keys rotate client_assertion` run `strazactl signing-keys promote client_assertion <kid>`, or the new key never signs.

To go back to 2.1.0, run `strazad migrate --to 41` with the 2.2.0 binary first, as [Roll back](#roll-back) shows. That step drops every agent's Entra agent identity.

## Upgrade from 1.1.2 to 2.0.0 {#upgrade-from-112-to-200 .nostep}


Straza 2.0.0 is a major release. strazad 2.0.0 refuses the machines that a 1.1.x `straza` client enrolled and the sign-in of a 1.1.x `strazactl`, so every governed machine gets a new client and every person enrolls again. People sign in through their browser in the console, on the self-service page and in `straza enroll`, and strazactl runs on an admin API token alone. Take the steps in this order:

1. Take a backup, as [Back up a standalone server](#back-up-a-standalone-server) or [Or back up an enterprise deployment](#or-back-up-an-enterprise-deployment) shows.
2. Register the browser sign-in at your identity provider.
3. Upgrade strazad, then put strazactl 2.0.0 on an admin API token.
4. Install the new client on every governed machine and run the managed install again.
5. Have every person enroll each machine again.
6. Give each AI agent a new key or a new client secret.
7. Move approver enrollment, scripts and custom harnesses off what 2.0.0 removed.

### Register the browser sign-in first


Skip this step when people sign in at strazad's built-in issuer, which needs nothing. At an external identity provider, register two addresses for Straza's client before strazad 2.0.0 starts. From then on the console, the self-service page and `straza enroll` sign in through the browser, and the provider sends each sign-in back to one of them.

- The address people open the console at, normally your strazad public URL, followed by `/console/signin`, such as `https://straza.example.com/console/signin`. Register one for each address people open the console at. On Entra ID it is a redirect of the platform Single-page application. On Keycloak it is a valid redirect URI, and its origin goes under Web origins. On Okta it is a sign-in redirect URI, and its origin is a trusted origin with CORS.
- `http://127.0.0.1/callback`, which `straza enroll` uses on a port each sign-in picks. On Entra ID it is a redirect of the platform Mobile and desktop applications. A provider that matches the port exactly needs a fixed one, such as `http://127.0.0.1:8765/callback` with `oidc.loopbackPort` set to `8765`.

[Entra ID login]({{< relref "guides/connect-identity/entra-login.md" >}}), [Okta login]({{< relref "guides/connect-identity/okta-login.md" >}}) and [Keycloak login]({{< relref "guides/connect-identity/keycloak-login.md" >}}) walk each provider, together with what else it needs, such as the `auth_time` claim on Entra ID.

The browser runs the sign-in only on a page served over HTTPS or opened at 127.0.0.1. Serve the console over HTTPS before the upgrade if people open it at another address over plain HTTP.

{{< fails >}}
`This page is served over plain HTTP from a network address, so the browser will not run the sign-in. Serve Straza over HTTPS, or open it at 127.0.0.1 on the server.`
: The console or the self-service page was opened over plain HTTP at an address other than 127.0.0.1. Serve strazad over HTTPS, as [TLS and exposure]({{< relref "guides/operate/tls-and-exposure.md" >}}) shows.
{{< /fails >}}

### Upgrade the server before the machines


Upgrade strazad, or the Helm chart, to 2.0.0 as [Upgrade](#upgrade) shows. Its first start applies migrations 38 to 41. Upgrade the server before any machine, because on Linux and macOS a managed install of the 2.0.0 client refuses the hook wiring a 1.1.2 server serves. That wiring runs `/usr/local/bin/straza`, where the 1.1.2 managed install copied its binary, and the 2.0.0 client runs from its package.

{{< fails >}}
``The Straza server at https://straza.example.com renders claude-code hooks that run /usr/local/bin/straza, but this machine's Straza is /usr/bin/straza, so those hooks would never run. Upgrade the Straza server first, then run `straza install --managed` again.``
: The managed install reached a server older than the client. Upgrade strazad, then run the managed install again.
{{< /fails >}}

Once strazad 2.0.0 runs, it refuses each machine that a 1.1.x client enrolled, at the machine's next check-in and at its next refresh, so the sessions of those machines end within five minutes. Each refusal is one login failure on the audit chain with the reason `the client's bearer device credential is retired`. Plan the server upgrade for a time when the machines can follow soon after.

{{< fails >}}
``This Straza client is older than the server's sign-in rules, which now need the Straza background service on this machine. Update Straza from your software portal, then run `straza enroll`.``
: The machine still runs a straza client from before 2.0.0. Install the new client and enroll again, as the next two steps show.
{{< /fails >}}


strazactl has no sign-in in 2.0.0. A strazactl 1.1.x that signed in before the upgrade is refused at its next call, and the login it kept in `~/.straza/credentials.json` works nowhere and can be deleted. Put strazactl 2.0.0 from the release archive in its place. Mint an admin API token in the console under **Settings**, **API tokens**, as [Mint an admin API token]({{< relref "guides/console/settings.md#mint-an-admin-api-token" >}}) shows, and run strazactl with `STRAZA_API_TOKEN` set to it.

{{< fails >}}
`strazactl no longer signs in, because its sign-in kept an administrator credential in a file that any program running as you could copy. Mint an admin API token in the console under Settings, API tokens, and run strazactl with STRAZA_API_TOKEN set to it.`
: strazad answers this to an older strazactl's sign-in and to every credential that sign-in kept. Each refusal is one login failure on the audit chain, with the reason `strazactl no longer signs in`. Delete the old login file and run strazactl on a token.
{{< /fails >}}

### Install the new client on every machine


1.1.2 shipped archives only, and 2.0.0 brings the client in packages that also install `straza service`, the background service that holds each machine's keys and sessions. On each governed machine, an administrator installs the package and then runs the managed install again with the new binary, so the hooks run it. The managed install no longer copies a binary, and it refuses to run until the package is installed.

- Install the deb or rpm package on Linux as [Install the package on Linux]({{< relref "guides/operate/install.md#install-the-package-on-linux" >}}) shows. Run the managed install by the package's full path, `sudo /usr/bin/straza install --managed --server <server-url> claude-code`, with each harness the machine governs. The full path matters, because `/usr/local/bin` comes before `/usr/bin` on the usual PATH and still holds the 1.1.2 binary. Then delete `/usr/local/bin/straza`, which the 1.1.2 archive or its managed install left, so that `straza` on the PATH is the package's.
- On Windows, install the MSI and run the managed install with its `straza.exe`, as [Install the service on Windows]({{< relref "guides/operate/install.md#install-the-service-on-windows" >}}) shows.
- A Mac gets the pkg, and then an administrator runs `sudo /opt/straza/bin/straza install --managed --server <server-url> claude-code`, as [Install the service on macOS]({{< relref "guides/operate/install.md#install-the-service-on-macos" >}}) shows. The pkg replaces the `/usr/local/bin/straza` that a 1.1.2 managed install copied there with a link to its own binary.

A machine without a managed install, where a person wired their own harness with `straza install`, runs `straza install claude-code` again as that person with the new binary, because the old wiring runs the old binary. [Deploy with Intune]({{< relref "guides/govern-an-agent/deploy-with-intune.md" >}}) and [Deploy with Jamf Pro]({{< relref "guides/govern-an-agent/deploy-with-jamf.md" >}}) roll the MSI and the pkg out to a fleet.

### Have every person enroll again


The enrollment a 1.1.x client made works nowhere, so each person runs `straza enroll` again on each of their machines, in a desktop session, where it opens the browser to sign in. On a machine with a managed install, `straza enroll` alone enrolls at the server the managed configuration names, as [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md#sign-in-and-enroll" >}}) shows. The background service then holds that machine's key.

A machine with no display enrolls with `straza enroll --use-device-code`. Under the enterprise profile `governance.minSignIn` defaults to `browser`, which refuses a sign-in code, because a code cannot show which machine it was approved for. A deployment whose people must enroll such machines sets `STRAZA_MIN_SIGN_IN=code`, which accepts a sign-in code on every machine.

{{< fails >}}
``Straza refused this sign-in because your Straza admin requires a browser sign-in, and a sign-in code cannot show which machine it was approved for. Run `straza enroll --server https://straza.example.com` in a desktop session on this machine.``
: The person enrolled with `--use-device-code` on a server that requires the browser sign-in. Run `straza enroll` in a desktop session on that machine.
{{< /fails >}}

### Give each AI agent a new key or secret


A 1.1.x client kept an AI agent's key in a file in the agent's own home, or its client secret in the agent's environment, where the agent itself could read it. A standalone server still accepts that agent's check-in without a proof from its key. An enterprise server refuses it with a sentence that starts `Straza refused this session because the user`. Once the agent's machine runs the 2.0.0 client, give the agent a credential that only the background service reads, in one of two ways.

- An agent that signs in at strazad's built-in issuer with a key runs `straza keygen --user <agent>` as its own OS account, and the background service makes the new key. An administrator runs the `strazactl users nhi-key set` line that keygen prints, which replaces the old key, and the agent runs `straza enroll --headless` again. [Headless agents]({{< relref "guides/govern-an-agent/headless-agents.md#generate-the-agents-key" >}}) shows each command.
- For an agent that signs in at your identity provider with a client secret, rotate that secret at the provider, because the old one sat where the agent could read it. An administrator puts the client id and the new secret in the file that only the service reads, as [Protect the key]({{< relref "guides/govern-an-agent/headless-agents.md#protect-the-key" >}}) shows.

### Move off what 2.0.0 removed


Approver phones and browsers that people enrolled before the upgrade keep working. From 2.0.0 a person enrolls a new phone or browser only on the self-service page, signed in as themselves, with **Add a phone**, which needs the role `straza-enroll-mobile`, or **Enable this browser**, which needs `straza-enroll-browser`. Assign those roles in your identity manager, or under Roles in the console when no identity manager feeds Straza. The admin route that minted an enroll code for another person refuses every caller now, and a code it minted before the upgrade no longer enrolls anything. [Phone]({{< relref "guides/approve/phone.md" >}}) walks the enrollment.


`straza daemon` and `straza drain` are gone, because `straza service` does their work. Remove every systemd unit, launchd job, scheduled task or script that runs either of them. `straza connect --allow-agents` is gone too. A person lets their agents use a connection with the switch **My agents may use this** on the Credentials tab of the self-service page.


These strazactl verbs moved to a person, because an admin API token is not a person. Each still answers, exits with status 2 and names what replaced it.

| strazactl 1.1.x | In 2.0.0 |
|---|---|
| `login`, `logout` | An admin API token in `STRAZA_API_TOKEN`. |
| `approvals approve`, `approvals deny` | A person decides in the console under Approvals, on the self-service page or on the phone. |
| `approvals enroll-token` | The person enrolls their own phone or browser on the self-service page. |
| `drafts publish`, `drafts contact` | A person publishes or contacts in the console under Drafts. |


A check-in whose harness name, harness version or client version is longer than 256 bytes is refused with a sentence that names the field, its length and the limit, and starts no session. A straza client sends a few dozen bytes in each, so only a custom harness or a program of your own that checks in meets the limit. Shorten its name or version before the upgrade.

To go back to 1.1.2, run `strazad migrate --to` with the 2.0.0 binary four times, down to migration 37, as [Roll back](#roll-back) shows. The step from 40 to 39 revokes every machine that enrolled with a key, so each machine goes back to the 1.1.2 client and enrolls again.

## Roll back {.nostep}


A rollback has two ways:

| Way back | What it keeps |
|---|---|
| `strazad migrate --to`, with the newer binary | Moves the schema down one migration per run and keeps what was written since the upgrade, except what each undone migration added. |
| A restore of the backup | Puts the backup back and loses what was written since. |

An older strazad refuses to start on a database a newer one migrated, and only the newer binary carries the step that undoes its own migration. The command therefore runs before the older binary starts. It cannot see other replicas, so stop every replica before you run it. Give it the same `--config`, `--profile`, `--data-dir` and `--store-dsn` that `strazad serve` runs with, from the same directory.

The target is the newest migration of the release you return to. This release carries migrations 37 to 42, and each run of the command moves the database down one of them. Each step gives up what its migration added:

| Step | What the older server no longer has |
|---|---|
| 42 to 41 | Every AI agent's Entra agent identity. Set each one again after the next upgrade. |
| 41 to 40 | The commit of the client build recorded on each session. |
| 40 to 39 | Every device enrolled with a key. The step writes a device revocation for each one and deletes its row, so each such machine installs the straza client of the release you return to and enrolls again. A device enrolled without a key keeps its row. |
| 39 to 38 | Every covers line. Each role then reaches every object its tools reach on its server, so a role that covered some objects reaches all of them. Moving up again leaves each role on a server that pins an object type covering every object until you narrow it again. |
| 38 to 37 | The object list of every server and the credential each listing used. Every other credential stays. |

{{< fails >}}
`this strazad has no migration 36. Its migrations run from 37 to 42, so name one of those`
: This build cannot move the database below migration 37. For a release older than that, restore the backup you took before the upgrade.
{{< /fails >}}

The command refuses a target more than one step down before anything runs and names the step to run first. Run it once for each step, with `TARGET` set to 41, then 40, and so on down to the newest migration of the release you return to. On the standalone host, with the target in the shell variable `TARGET`:

{{< command terminal="Terminal" purpose="on the strazad host" >}}
```sh
kill "$(pidof strazad)"
while pidof strazad >/dev/null; do sleep 1; done
cd /srv/straza && strazad migrate --to "$TARGET"
```
{{< /command >}}

The command prints the migration the database is at now and the release to start. Put the older binary back in place and start it the way you started it before the upgrade.


An older strazad claims the outbox rows this release set aside, those with `attempts` -1, again and stalls behind them, so before the older binary starts, read them with `SELECT id, subject, length(ce) FROM events_outbox WHERE attempts = -1` and delete them.


A restore is the way back from anything the command cannot undo, a step that failed part way included. Stop the new binary, restore the backup you took before the upgrade as [Restore a backup](#restore-a-backup) shows, and put the older binary back before you start the server. Anything written between the upgrade and the rollback is lost with the restore, which is the reason the backup and the upgrade belong in the same change window.

## Restore a backup {.nostep}


A restore puts the server back to the moment of the backup. Everything written since then is gone, the audit records included. Restore the store and the three key files from the same backup, because [Know what to back up](#know-what-to-back-up) shows what breaks when one of them is missing.

### Restore a standalone server


Run the steps on the strazad host. Run the first two as root, so that `cp -a` keeps the owner of every file, and start the server as the user that runs it. The steps restore the copy that [Back up a standalone server](#back-up-a-standalone-server) made, with the same paths.

1. Stop the server and wait until it has exited.

{{< command terminal="Terminal 1" purpose="on the strazad host, as root" >}}
```sh
kill "$(pidof strazad)"
while pidof strazad >/dev/null; do sleep 1; done
```
{{< /command >}}

2. Move the data directory and the config file aside, copy the backup into their place, and list what came back. Keep the moved copies until the restored server passes its checks.

{{< command terminal="Terminal 1" purpose="on the strazad host, as root" >}}
```sh
mv /var/lib/straza /var/lib/straza.before-restore
mv /srv/straza/straza.yaml /srv/straza/straza.yaml.before-restore
cp -a /var/backups/straza-2026-09-19 /var/lib/straza
cp /var/backups/straza-2026-09-19.straza.yaml /srv/straza/straza.yaml
ls /var/lib/straza /var/lib/straza/approver-tls
```
{{< /command >}}

```text
/var/lib/straza:
approver-tls
apps
nats
secret.key
straza.db

/var/lib/straza/approver-tls:
cert.pem
key.pem
```

{{< see >}}`secret.key`, `straza.db` and `approver-tls` with both of its files.{{< /see >}}


Look for `secret.key` before you go on. When it is missing, strazad creates a new key without a warning. Then it refuses to start if the store holds a client assertion key. Otherwise it starts, and every credential the store sealed under the old key stops opening. When the WebPush key lives outside the data directory, put it back from its own backup now.

3. Start the server.

{{< command terminal="Terminal 1" purpose="on the strazad host" >}}
```sh
cd /srv/straza && strazad serve
```
{{< /command >}}

{{< see >}}`approver TLS pair loaded` rather than `minted`. The `project identity` line names the same project as before the backup.{{< /see >}}

```text
time=2026-10-07T15:28:24.180Z level=INFO msg="approver TLS pair loaded" cert=/var/lib/straza/approver-tls/cert.pem expires=2029-01-04T15:27:36Z
time=2026-10-07T15:28:24.186Z level=INFO msg="store ready" driver=sqlite
time=2026-10-07T15:28:24.190Z level=INFO msg="project identity" id=prj_01a116fa-2d02-78c9-a564-e237dfb19d7c name=straza-9d7c
time=2026-10-07T15:28:24.218Z level=INFO msg="strazad serving" addr=127.0.0.1:8420 profile=standalone publicUrl=http://127.0.0.1:8420 tls=false version=v1.0.0-1500-g0cd3e356
```

The output is trimmed to the four lines that show the restore.


{{< fails >}}
`approver TLS pair minted`
: The backup's approver pair did not come back, so the server made a new one, and every phone would have to enroll again. Stop the server, replace `/var/lib/straza/approver-tls` with the folder from the backup, and start it again. The server loads a pair it finds and never replaces it, so the phones that pinned the old pair reach the server again.

`does not open: it was sealed under another KEK or changed in the database. Every replica must mount the same secrets.kekFile`
: strazad made a new `secret.key`, because the one from the backup did not come back. Its refusal line starts with `strazad: authn: client assertion key` and the key's id. strazad has already exited. Copy `secret.key` from the backup over the new one, then start it again. This refusal comes only when the store holds a client assertion key. Without one the server starts, and its sealed credentials fail when a server uses them.
{{< /fails >}}

4. In a second terminal, run the checks of [Verify](#verify).

{{< command terminal="Terminal 2" purpose="administration" >}}
```sh
strazactl status
strazactl audit verify
```
{{< /command >}}

```text
server     http://127.0.0.1:8420
strazad    v1.0.0-1500-g0cd3e356 (commit 0cd3e356, profile standalone)
bus        ok
store      ok
status     ok
audit chain intact: 4 records verified
```

{{< see >}}`ok` on the last line of the status, and `audit chain intact`. The count is the number of records the chain held when the backup was taken, so it can be lower than the count you saw before the restore.{{< /see >}}

5. On a machine that enrolled before the backup, run `straza doctor`.

{{< command terminal="Terminal 3" purpose="a client machine" >}}
```sh
straza doctor
```
{{< /command >}}

```text
[ ok ] service     straza service v1.0.0-1570-gaf326165-dirty answers at /home/alice/.straza/service.sock and keeps keys in user custody
[ ok ] enrollment  alice (device 01a15d8f-71e0-7253-8ad3-ea221f0bb6ae) at http://127.0.0.1:8420, key YHgcjJs1HFVw, straza v1.0.0-1570-gaf326165-dirty
[ ok ] server      http://127.0.0.1:8420 healthy (strazad v1.0.0-1570-gaf326165-dirty, standalone profile)
[ ok ] session     claude-code 01a15d8f-7298-732f-9720-f4dbb0b9076f as alice (token valid 4m54s)
```

{{< see >}}The `server` line names the restored server's build, and the `session` line shows a live session. The machine needs no new enrollment, because its device row came back with the store and its key stayed on the machine.{{< /see >}}

The output is trimmed to four of its lines.

### Or restore an enterprise database


Stop every strazad replica first, because Postgres refuses to rename a database while a connection holds it. Then move the current database aside under a new name, create an empty one under the old name, and restore the dump into it. Run the commands with the variables from [Or back up an enterprise deployment](#or-back-up-an-enterprise-deployment), as a role that may create databases.

{{< command terminal="Terminal" purpose="with the Postgres client tools" >}}
```sh
psql --dbname=postgres --command='ALTER DATABASE straza RENAME TO straza_before_restore'
psql --dbname=postgres --command='CREATE DATABASE straza OWNER straza'
pg_restore --dbname=straza --exit-on-error straza-2026-09-19.dump
```
{{< /command >}}

```text
ALTER DATABASE
CREATE DATABASE
```

{{< see >}}`ALTER DATABASE` and `CREATE DATABASE`, and nothing from `pg_restore`.{{< /see >}}

{{< fails >}}
`ERROR:  database "straza" is being accessed by other users`
: A strazad replica, or another client, still holds a connection. Stop every replica, wait until it has exited, and run the commands again.
{{< /fails >}}

Put the three key files back from the same backup: the Secrets on Kubernetes, or the data volume with the repository compose file. Then start the replicas. Each one logs `store ready` with the driver `postgres`, and a `project identity` line that names the same project as before. Run the checks of [Verify](#verify), and when they pass, remove the database you moved aside.

{{< command terminal="Terminal" purpose="with the Postgres client tools" >}}
```sh
dropdb straza_before_restore
```
{{< /command >}}

### Rehearse the restore


A backup is proven only by a restore. Rehearse one with the newest backup on a spare host, never on the production host, and do it again after each upgrade. The commands use the layout and the default listener `127.0.0.1:8420` of [Standalone host]({{< relref "guides/operate/standalone-host.md" >}}), and the sign-ins open in a browser on the spare host.

1. Copy the backup directory and its config file to `/var/backups` on the spare host, with the tool you use to move backups. If the config file sets `sinks`, `approval.channels.slack`, `approval.push` or `oauth`, delete those keys from the spare host's copy, so the drill sends nothing to the systems production uses. Also delete `server.listen`, `server.publicUrl` and `server.tls`, so the server listens on `127.0.0.1:8420` and its sign-in links stay on the spare host. Run the drill on a host with no route to your remote MCP servers, because strazad checks every server in the store with its stored credential.

2. Put the backup in place as root, and start the server.

{{< command terminal="Terminal 1" purpose="on the spare host, as root" >}}
```sh
mkdir -p /srv/straza
cp -a /var/backups/straza-2026-09-19 /var/lib/straza
cp /var/backups/straza-2026-09-19.straza.yaml /srv/straza/straza.yaml
cd /srv/straza && strazad serve
```
{{< /command >}}

{{< see >}}`approver TLS pair loaded`, and the production server's project in the `project identity` line.{{< /see >}}

3. In a browser on the spare host, sign in to the console at `http://127.0.0.1:8420/console/` as `admin` with the password that was current when the backup was taken. Mint an admin API token there that holds `audit:read`, under **Settings**, **API tokens**, and check the audit chain on it in a second terminal. `read -rs` reads the token you paste without showing it and keeps it out of the shell history.

{{< command terminal="Terminal 2" purpose="on the spare host" >}}
```sh
export STRAZA_SERVER=http://127.0.0.1:8420
read -rs STRAZA_API_TOKEN && export STRAZA_API_TOKEN
strazactl audit verify
```
{{< /command >}}

{{< see >}}`audit chain intact` with the number of records it checked.{{< /see >}}

4. Enroll the spare host as a client, start one session the way a harness does when it opens, and run doctor. The client needs `straza service`, so start one in a third terminal and leave it running. With `STRAZA_HOME` set it runs as you and keeps its socket and state inside that directory, and the second terminal sets the same value. This works only on a machine with no system service and no managed install, because on a machine with either one, every `straza` command ignores `STRAZA_HOME` and reaches the system service.

{{< command terminal="Terminal 3" purpose="the background service on the spare host, keep it running" >}}
```sh
export STRAZA_HOME="$HOME/.straza"
straza service
```
{{< /command >}}

{{< command terminal="Terminal 2" purpose="on the spare host" >}}
```sh
export STRAZA_HOME="$HOME/.straza"
straza enroll --server http://127.0.0.1:8420
echo '{"hook_event_name":"SessionStart","session_id":"restore-drill"}' | straza hook --harness claude-code >/dev/null
straza doctor
```
{{< /command >}}

```text
[ ok ] service     straza service v1.0.0-1570-gaf326165-dirty answers at /home/admin/.straza/service.sock and keeps keys in user custody
[ ok ] server      http://127.0.0.1:8420 healthy (strazad v1.0.0-1570-gaf326165-dirty, standalone profile)
[ ok ] session     claude-code 01a15d8f-8de7-7c64-8534-554c8300fde4 as admin (token valid 4m59s)
[WARN] wiring      no harness has Straza hooks wired
```

The output is trimmed to the lines the drill checks, without the hint under each warning.

{{< see >}}The `service`, `server` and `session` lines are `ok`. The `wiring` line warns because no harness runs on the spare host.{{< /see >}}

5. Stop the server and the service, and wipe the spare host's copy and its `STRAZA_HOME` when the drill ends, because the copy holds the same store and keys as production.

## Remove Straza from a client machine {.nostep}


On a Linux machine with the `straza` package, the package manager removes Straza, and the command you choose decides what happens to the keys in `/var/lib/straza-service`.

| Command | What it does |
|---|---|
| `sudo apt remove straza` | Stops `straza service` and its socket, and keeps the keys, so a reinstall resumes every enrollment on the machine. |
| `sudo apt purge straza` | Stops them and deletes the keys. |
| `sudo dnf remove straza` | Stops them and deletes the keys. |

The straza-service OS account stays in every case. No removal revokes anything at the server. After a purge or an erase, each person's enrollment on the machine stops working because its key is gone, and the device stays on that person's list until an admin revokes it, though the server refuses it past its sign-in deadline, 7 days by default. An AI agent's registered key stays registered until an admin unsets it.

To retire a machine, take three steps in order:

1. Remove the managed hook wiring while the package's binary is still there, with `sudo straza uninstall --managed claude-code`, and name each harness the managed install wired.
2. Purge or erase the package, with `sudo apt purge straza` or `sudo dnf remove straza`.
3. Revoke each person's device of that machine, on the person's sheet in the console or with `strazactl devices list alice` and `strazactl devices revoke alice DEVICE-ID`, as [Cut one machine]({{< relref "guides/operate/kill-switch-runbook.md#cut-one-machine" >}}) shows. For an AI agent that ran there, run `strazactl users nhi-key unset build-bot`.

Revoking closes the time in which a copy of the machine's keys, such as one in a disk image, could still start a session.

If the package goes before step 1, the wiring stays and names a binary that is gone. Where the Claude Code wiring carries `onFailure`, which `grep onFailure` on its managed file shows, Claude Code 2.1.295 and later then blocks every tool call. Older Claude Code wiring, Codex CLI and Gemini CLI run their calls unchecked. Install the package again and take step 1, or delete Straza's entries from each harness's managed file by hand.


On a Windows machine, an administrator removes the Straza MSI in an elevated PowerShell, with the install command of [its step]({{< relref "guides/operate/install.md#install-the-service-on-windows" >}}) and `/x` in place of `/i`. The `PURGE` property decides what happens to the keys in `C:\ProgramData\straza-service`.

| Uninstall | What it does |
|---|---|
| `/x` with `/qn` | Removes the managed wiring of Claude Code, Codex CLI and Gemini CLI, stops and removes `straza service`, and removes `straza.exe` with its copy `git-remote-straza.exe` and the MSI's entry in the machine PATH. Keeps the keys, so the MSI installed again resumes every enrollment on the machine. |
| `/x` with `/qn PURGE=1` | Does the same and deletes the keys. |

The wiring and the keys go only once the rest of the uninstall has succeeded. An uninstall that fails is undone by Windows and leaves the wiring and the keys as they were, so the harnesses stay governed by the service it put back.

Both keep `C:\ProgramData\straza\config.yaml` and the codex shim `straza-hook-codex.cmd` beside the binary, which the managed install wrote, until an administrator deletes them. The uninstall runs as the Windows SYSTEM account, so it also keeps the Straza MCP registration in each person's codex `config.toml`, and a person who had one deletes the `[mcp_servers.straza]` table from their own file. To retire a Windows machine, uninstall with `PURGE=1`, then revoke the devices as step 3 above shows. The wiring step is part of the uninstall there, so it needs no command of its own.


A macOS package has no uninstall of its own, so on a Mac an administrator removes the Straza package for macOS with the hidden command `straza installer uninstall`, run by its full path. The `--purge` flag decides what happens to the keys in `/var/db/straza-service`.

| Command | What it does |
|---|---|
| `sudo /opt/straza/bin/straza installer uninstall` | Stops `straza service` and removes its launchd definition, the managed wiring of Claude Code, Codex CLI and Gemini CLI, the socket folder `/opt/straza/run`, the log rotation rule, the package receipt, the two links in `/usr/local/bin`, the binary and the license files. Keeps the keys and the _straza-service account, so the package installed again resumes every enrollment on the Mac. |
| `sudo /opt/straza/bin/straza installer uninstall --purge` | Does the same and also deletes the keys and sessions, the service's log, and the _straza-service account and its group. |

The command stops the service before it unwires the harnesses, so a failure never leaves a running service beside unwired harnesses. Each step skips what is already gone, and the binary goes last, so the same command run again after a failure finishes the job. It keeps a link in `/usr/local/bin` that points somewhere else, and `/etc/straza/config.yaml`, which the managed install wrote, until an administrator deletes it. Run as root outside sudo, as a device management tool runs it, it keeps the Straza MCP registration in each person's codex `config.toml`, and a person who had one deletes the `[mcp_servers.straza]` table from their own file. To retire a Mac, uninstall with `--purge`, then revoke the devices as step 3 above shows.
