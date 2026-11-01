---
title: Standalone host
description: Run strazad on one Linux host in the standalone profile, with its settings in a file and all its state in one directory.
pagetype: how-to
weight: 10
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: An Alpine 3.20 container with the static binaries and curl, in the standalone profile, where every terminal block on this page but the token step ran in order. In that run strazactl signed in with strazactl login, answered with the form post the built-in sign-in page sends, and strazactl no longer signs in, so the token step was written on 2026-11-01 and not walked. The console sign-in was not clicked, because the server listened on the container's loopback address. The console's browser sign-in was written from the code on 2026-10-22 and not walked
  date: 2026-10-06
applies_to: standalone
who: You, as the operator of the host
where: A terminal on the host, and a browser for the sign-in
steps: true
modes: [console, cli]
mode_default: cli
keywords: standalone host binary profile data directory
---


You run strazad as one binary on one Linux host in the standalone profile, from a terminal on that host. You need the `strazad` and `strazactl` binaries from [Install]({{< relref "get-started/install.md" >}}). At the end the server runs from a config file, you are signed in as its first administrator, and you know which directory holds everything it knows.

In the standalone profile strazad is its own login provider. It keeps users, policy and the audit record in one SQLite file and runs the event bus inside the process, so a single directory holds all of its state. The standalone profile binds its main listener to 127.0.0.1:8420. Unless `server.approverTLS.autoMint` is false, it also serves the phone approver routes on port 8443 on every interface, with a self-signed certificate it creates in the data directory.

This shape runs on exactly one host, because a second copy of the binary is a second, unrelated server. [Standalone and enterprise compared]({{< relref "reference/standalone-and-enterprise.md" >}}) lists what the other profile changes.

## Start the server

{{< only form="cli" >}}A server starts from a terminal on its host. The console opens once it runs.{{< /only >}}


Put the two binaries on your path and start the server from an empty directory. Without a config file it uses the built-in defaults of the profile: the data directory `data` under the current directory, the listener `127.0.0.1:8420`, and JSON logs at level `info` on stderr.

{{< command terminal="Terminal 1" purpose="server, keep it running" >}}
```sh
strazad serve --profile standalone
```
{{< /command >}}

{{< see >}}The last line is `strazad serving` with the profile `standalone` at `127.0.0.1:8420`.{{< /see >}}

```text
{"time":"2026-10-06T21:36:47.616367119Z","level":"INFO","msg":"approver TLS pair minted","cert":"data/approver-tls/cert.pem","expires":"2029-01-03T21:36:47Z"}
{"time":"2026-10-06T21:36:47.661829986Z","level":"INFO","msg":"store ready","driver":"sqlite"}
{"time":"2026-10-06T21:36:47.672023617Z","level":"INFO","msg":"project identity","id":"prj_01a11325-d034-7581-abc8-b0549819034c","name":"straza-034c"}
{"time":"2026-10-06T21:36:47.782946047Z","level":"WARN","msg":"bootstrap admin created. Store this password now, it will not be shown again","username":"admin","password":"7d2f"}
{"time":"2026-10-06T21:36:47.78488454Z","level":"INFO","msg":"starter policy seeded. Recursive force-delete (rm -rf) is denied with a reason; edit or delete PolicySet standalone-starter to change this"}
{"time":"2026-10-06T21:36:47.895298696Z","level":"WARN","msg":"break-glass admin created. Store this password in your vault now, it will not be shown again; rotate on-box via `strazactl users set-password break-glass`","username":"break-glass","password":"4a21"}
{"time":"2026-10-06T21:36:47.923022969Z","level":"INFO","msg":"event bus ready","embedded":true}
{"time":"2026-10-06T21:36:47.933341583Z","level":"INFO","msg":"policy snapshot ready","id":"df8d228ad19c00ed378201df30c89a2051e6d36546f3947c282fc365c99a6395"}
{"time":"2026-10-06T21:36:48.015473807Z","level":"INFO","msg":"strazad serving","addr":"127.0.0.1:8420","profile":"standalone","publicUrl":"http://127.0.0.1:8420","tls":false,"version":"v1.1.0-117-g106081a8"}
```


The output is trimmed to the lines that matter, and the lines for the harness-config hashes, the apps watcher and the approver listener are left out. Both passwords are masked to their first four characters here.

{{< now title="Store both passwords now" >}}The first boot prints them once and never again. `admin` is your first administrator. `break-glass` is the standing emergency account, which the server refuses to deactivate, lock or delete, so that no mistake in an identity manager can lock you out.{{< /now >}}


The first boot created the data directory and everything in it. List it from a second terminal in the same directory:

{{< command terminal="Terminal 2" purpose="on the host" >}}
```sh
ls -la data data/approver-tls
```
{{< /command >}}

```text
data:
drwx------    2 root     root          4096 Oct  6 21:36 approver-tls
drwxr-x---    2 root     root          4096 Oct  6 21:36 apps
drwx------    3 root     root          4096 Oct  6 21:36 nats
-rw-------    1 root     root            32 Oct  6 21:36 secret.key
-rw-r--r--    1 root     root          4096 Oct  6 21:36 straza.db
-rw-r--r--    1 root     root         32768 Oct  6 21:36 straza.db-shm
-rw-r--r--    1 root     root       1380232 Oct  6 21:36 straza.db-wal

data/approver-tls:
-rw-r--r--    1 root     root           672 Oct  6 21:36 cert.pem
-rw-------    1 root     root           227 Oct  6 21:36 key.pem
```


| Entry | What it is |
|---|---|
| `straza.db`, with `-shm` and `-wal` | The SQLite store, with its write-ahead log beside it. |
| `nats` | The storage of the embedded event bus. |
| `apps` | The directory the server watches for declarative MCP server manifests. |
| `secret.key` | The 32-byte key-encryption key that seals stored credentials. |
| `approver-tls` | The self-signed certificate and key that the Straza approver app pins. |

The server never replaces the approver pair on its own. A new pair is a new pin, and every enrolled phone would have to enroll again.

## Sign in as the first administrator


In this profile the sign-in page is served by strazad itself, and you complete it in a browser with the `admin` username and the password from the boot log.

{{< console >}}
Open `http://127.0.0.1:8420/console/` in a browser on the host. The card says `You sign in on this server's own sign-in page and come back to this page.` Press **Sign in**.

{{< shot name="signin" caption="The console's sign-in card." >}}

The tab goes to the page titled Sign in to Straza, which asks for the password every time. Enter `admin` and the password, and press **Sign in**. Then the tab comes back to the console, which says `Signing you in`.

{{< see >}}The console opens on **Overview**, and **Users** lists `admin` and `break-glass`.{{< /see >}}

{{< fails >}}
`This page is served over plain HTTP from a network address, so the browser will not run the sign-in. Serve Straza over HTTPS, or open it at 127.0.0.1 on the server.`
: The browser opened the console at the host's network address over plain HTTP, where it offers no Web Crypto for the sign-in. Open `http://127.0.0.1:8420/console/` on the host, or serve Straza over HTTPS.
{{< /fails >}}
{{< /console >}}

{{< cli >}}

strazactl has no sign-in of its own. It runs on an admin API token that you mint in the console, so first sign in there as `admin`, as the console form of this step shows. On a host you reach over SSH, forward the port with `ssh -L 8420:127.0.0.1:8420` and the host's name, and open `http://127.0.0.1:8420/console/` in your own browser. Then open the mint sheet:

{{< clicks "Settings" "API tokens" "New API token" >}}

Name the token after yourself and the host. Pick `expires in 30 days` and the job **Full root** before you press **Mint token**. The token is shown once. Copy it and hand it to strazactl:

{{< command terminal="Terminal 2" purpose="administration" >}}
```sh
export STRAZA_SERVER=http://127.0.0.1:8420
read -rs STRAZA_API_TOKEN && export STRAZA_API_TOKEN
```
{{< /command >}}

`read -rs` reads the token you paste without showing it and keeps it out of the shell history. Never put the token where an agent runs as you, such as a shell profile or a file an agent can read. A token is a bearer credential, so whoever holds it acts with its scope until it expires or is revoked. Revoke this one under **Settings**, **API tokens** once the setup is done. Automation gets a token of its own with narrow scopes, and an agent proposes config changes through the built-in straza MCP server.


List the users the first boot made to confirm the token works:

{{< command terminal="Terminal 2" purpose="administration" >}}
```sh
strazactl users list
```
{{< /command >}}

```table
USERNAME     STATUS  ORIGIN  EMAIL  ID
admin        active  local          01a11325-d09e-79d1-9a05-5994634f951f
break-glass  active  local          01a11325-d112-7aee-99ae-9d474ff1a878
```
{{< /cli >}}

## Move the settings into a file


strazad reads one config file, and these rules decide which one:

- It looks for `straza.yaml` in the directory it starts in, unless `--config` or the `STRAZA_CONFIG` variable names another path.
- A named file that does not exist stops the boot. A missing default file is fine.
- Settings layer in a fixed order: the profile's defaults, then the file, then environment variables, then command-line flags.
- The profile itself is read first, from flag, environment or file, so it can pick the defaults the other layers override.

A data directory in a system location and a text log format are the two settings most hosts want in the file. Stop the first server with Ctrl-C, because the next one listens on the same address, 127.0.0.1:8420.

The block below creates directories under `/srv` and `/var/lib`, so run it as root. Or create the two directories with `sudo mkdir -p` and hand them to the user that runs strazad with `sudo chown`.

{{< command terminal="Terminal 1" purpose="server, as root" >}}
```sh
mkdir -p /srv/straza /var/lib/straza
cat > /srv/straza/straza.yaml <<'EOF'
profile: standalone
dataDir: /var/lib/straza
log:
  level: info
  format: text
EOF
cd /srv/straza && strazad serve
```
{{< /command >}}

{{< see >}}Text log lines, ending in a serving line, with the paths under `/var/lib/straza`.{{< /see >}}

```text
time=2026-10-06T21:37:33.546Z level=INFO msg="approver TLS pair minted" cert=/var/lib/straza/approver-tls/cert.pem expires=2029-01-03T21:37:33Z
time=2026-10-06T21:37:33.589Z level=INFO msg="store ready" driver=sqlite
time=2026-10-06T21:37:33.687Z level=WARN msg="bootstrap admin created. Store this password now, it will not be shown again" username=admin password=8fbd
time=2026-10-06T21:37:33.893Z level=INFO msg="apps GitOps watcher ready" dir=/var/lib/straza/apps
time=2026-10-06T21:37:33.897Z level=INFO msg="strazad serving" addr=127.0.0.1:8420 profile=standalone publicUrl=http://127.0.0.1:8420 tls=false version=v1.1.0-117-g106081a8
```


This is a fresh store, so the boot minted a new administrator password, masked to four characters again, and a new approver pair. It also logged a new break-glass password, trimmed here with the other lines that do not show the new paths. Store both new passwords, then sign in to the console again and mint a new token, because the token you minted on the first store does not open this one. The old `data` directory is untouched.

[Configuration]({{< relref "reference/configuration.md" >}}) lists every key of the file with its environment variable.


For a one-off run, flags override the file: `--config`, `--profile`, `--data-dir`, `--listen`, `--log-level` and `--store-dsn`. `--store-dsn` takes the path of the SQLite file, or a PostgreSQL connection string. Started with `--data-dir` and `--store-dsn` pointing at two different directories, strazad keeps the key, the event bus and the apps directory in the first and the database files at the path of the second.

`strazad completion bash` prints a completion script for bash, and `zsh`, `fish` and `powershell` work the same way. `strazad completion bash --help` says how to load it.

## Verify


Three unauthenticated endpoints tell you the server is up:

- `/healthz` answers as soon as the process serves.
- `/readyz` also pings the store and the event bus.
- `/version` names the build and the profile.

{{< command terminal="Terminal 2" purpose="on the host" >}}
```sh
curl -s http://127.0.0.1:8420/healthz
curl -s http://127.0.0.1:8420/readyz
curl -s http://127.0.0.1:8420/version
```
{{< /command >}}

{{< see >}}`"status":"ok"` from the first two, the second with `bus` and `store` both `ok`, and a version answer with the profile `standalone`.{{< /see >}}

```text
{"status":"ok"}
{"status":"ok","components":{"bus":"ok","store":"ok"}}
{"version":"v1.1.0-117-g106081a8","commit":"106081a8","go":"go1.26.6","os":"linux","arch":"amd64","profile":"standalone","runtimes":["remote","command"],"approver":{"public_url":"https://172.17.0.4:8443","tls_spki_pin":"sha256/0/a6IgEJ2vSWckKKADsDBYt0ZyD2m+Aix9Fdjr6iNkw=","cert_not_after":"2029-01-03T21:37:33Z","auto_minted":true,"cert_file":"/var/lib/straza/approver-tls/cert.pem"}}
```

## Reach it from other machines {.nostep}


The listener binds `127.0.0.1:8420` by default and `server.publicUrl` defaults to match. The public URL is also the issuer written into every session token. A server that agents on other machines dial therefore needs three things in the file:

- `server.listen` set to an address they reach.
- `server.publicUrl` set to the URL they use.
- TLS in front of that listener or on it.

A standalone listener on a routable address gets no plaintext warning, so adding TLS is your step. [TLS and exposure]({{< relref "guides/operate/tls-and-exposure.md" >}}) covers both listeners, and [Docker]({{< relref "guides/operate/docker.md" >}}) is the container form of the same server.

## Undo {.nostep}


Stop the process and the server is gone. Deleting the data directory deletes the store, the audit record, the key-encryption key and the approver certificate, so any phone that enrolled against this server enrolls again after the next first boot.
