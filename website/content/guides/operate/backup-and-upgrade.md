---
title: Backup and upgrade
description: Take a backup that restores the whole server, restore it and rehearse the restore, upgrade strazad without logging anyone out, and know the way back.
pagetype: how-to
weight: 70
draft: false
tested:
  version: v1.0.0-1500-g0cd3e356
  platform: Alpine 3.20 containers in the standalone profile, laid out as the standalone host guide describes. One container ran the backup, two restores that left out secret.key and approver-tls, a restore without secret.key over a store that held a client assertion key, the restore steps, the Verify list with a governed tool call and straza doctor on a client enrolled before the backup, the upgrade block with a copy of the same build standing in for the new binary because no later release exists, the migrate refusal and the refusal of a store set to version 30. A second container ran the drill, where a form post with the admin password stood in for the browser sign-in. The pg_dump and pg_restore commands and dropdb ran against a Postgres 16 container with strazad in the standalone profile on that database. The enterprise profile, Kubernetes, the WebPush key, the apps export and the console sentences were not run
  date: 2026-10-07
applies_to: both
who: You, as whoever is on call for the server
where: A terminal on the strazad host, or your Postgres and Kubernetes tooling
steps: true
keywords: backup upgrade migration migrate restore rollback vapid kek drill pg_dump pg_restore disaster recovery
---


You back up the state strazad depends on and upgrade the binary, as whoever is on call for the server. You work in a terminal on the strazad host, or with your Postgres and Kubernetes tooling. At the end the new build runs on the same store, nobody had to log in again, and you hold a backup that restores the whole server.

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

On the standalone host, download and verify the new release as [Install]({{< relref "get-started/install.md" >}}) describes. Then, from the directory that holds the new `strazad` and as the user that owns the installed one, stop the server, put the new binary in place of the old one and start it again.

{{< command terminal="Terminal" purpose="on the strazad host" >}}
```sh
kill "$(pidof strazad)"
while pidof strazad >/dev/null; do sleep 1; done
install -m 0755 strazad "$(command -v strazad)"
cd /srv/straza && strazad serve
```
{{< /command >}}

{{< see >}}The `strazad serving` line names the new version, and the `project identity` line names the same project as before the upgrade. That is the sign that the new binary found its store.{{< /see >}}

A `strazactl` that was logged in before the upgrade keeps working without a new login, because its long-lived device credential starts a new session.


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

## Roll back {.nostep}


A rollback has two ways:

| Way back | What it keeps |
|---|---|
| `strazad migrate --to`, with the newer binary | Moves the schema down and keeps what was written since the upgrade. |
| A restore of the backup | Puts the backup back and loses what was written since. |

An older strazad refuses to start on a database a newer one migrated, and only the newer binary carries the step that undoes its own migration. The command therefore runs before the older binary starts. It cannot see other replicas, so stop every replica before you run it. Give it the same `--config`, `--profile`, `--data-dir` and `--store-dsn` that `strazad serve` runs with, from the same directory.

The target is the newest migration of the release you return to. This release has one migration, 37, so the command cannot move a database down from it, and the way back from this release is a restore.

{{< fails >}}
`this strazad has no migration 36. Its only migration is 37, so there is no older version to move the database to`
: This release has no schema step to roll back. Restore the backup you took before the upgrade.
{{< /fails >}}

From a later release, the command moves one migration per run. When the newer release added more than one, it refuses a target further down before anything runs and names the step to run first, so run it once for each step. On the standalone host, with the target in the shell variable `TARGET`:

{{< command terminal="Terminal" purpose="on the strazad host" >}}
```sh
kill "$(pidof strazad)"
while pidof strazad >/dev/null; do sleep 1; done
cd /srv/straza && strazad migrate --to "$TARGET"
```
{{< /command >}}

The command prints the migration the database is at now and the release to start. Put the older binary back in place and start it the way you started it before the upgrade.


A restore is the way back from anything the command cannot undo. Stop the new binary, restore the backup you took before the upgrade as [Restore a backup](#restore-a-backup) shows, and put the older binary back before you start the server. Anything written between the upgrade and the rollback is lost with the restore, which is the reason the backup and the upgrade belong in the same change window.

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
[ ok ] enrollment  server http://127.0.0.1:8420, 1 snapshot key(s) pinned, straza v1.0.0-1500-g0cd3e356
[ ok ] identity    alice (device 01a116fa-3f2a-7a7e-a149-61fbc788cf4d), device credential valid until 2026-11-06
[ ok ] server      http://127.0.0.1:8420 healthy (strazad v1.0.0-1500-g0cd3e356, standalone profile)
[ ok ] session     01a116fa-4001-7b96-bdf0-c214bb1fcbca (roles [], attestation advisory, token valid 4m16s)
[ ok ] snapshot    verified against pinned keys (compiled 49s ago)
```

{{< see >}}The `server` line names the restored server's build, and the `snapshot` line says `verified against pinned keys`. The machine needs no new enrollment. Its device and the keys it pinned came back with the store.{{< /see >}}

The output is trimmed to five of its lines.

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

3. In a second terminal, log in as `admin` with the password that was current when the backup was taken, and check the audit chain.

{{< command terminal="Terminal 2" purpose="on the spare host" >}}
```sh
strazactl login --server http://127.0.0.1:8420
strazactl audit verify
```
{{< /command >}}

{{< see >}}`Logged in as admin.` and `audit chain intact` with the number of records it checked.{{< /see >}}

4. Enroll the spare host as a client, start one session the way a harness does when it opens, and run doctor.

{{< command terminal="Terminal 2" purpose="on the spare host" >}}
```sh
straza enroll --server http://127.0.0.1:8420
echo '{"hook_event_name":"SessionStart","session_id":"restore-drill"}' | straza hook --harness claude-code >/dev/null
straza doctor
```
{{< /command >}}

```text
[ ok ] enrollment  server http://127.0.0.1:8420, 1 snapshot key(s) pinned, straza v1.0.0-1500-g0cd3e356
[ ok ] identity    admin (device 01a116fb-4464-7717-acd7-bef4b8df6ac0), device credential valid until 2026-11-06
[ ok ] server      http://127.0.0.1:8420 healthy (strazad v1.0.0-1500-g0cd3e356, standalone profile)
[ ok ] session     01a116fb-450b-7e40-bbe3-d7e4fd6f5d15 (roles [straza-admin], attestation advisory, token valid 4m59s)
[ ok ] snapshot    verified against pinned keys (compiled 1m12s ago)
[WARN] wiring      no harness has Straza hooks wired
[WARN] killswitch  edge push (SSE via http://127.0.0.1:8420/v1/push): verified from here (the server accepted a push subscription), but no daemon heartbeat exists in this straza home: no daemon is subscribed to hear a push
```

The output is trimmed to the lines the drill checks, without the hint under each warning.

{{< see >}}The `server` line is `ok`, and so are the lines for the session and the snapshot. The `wiring` line warns because no harness runs on the spare host, and the `killswitch` line warns because no `straza daemon` runs there.{{< /see >}}

5. Stop the server and wipe the spare host's copy when the drill ends, because it holds the same store and keys as production.
