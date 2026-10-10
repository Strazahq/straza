---
title: Docker
description: Run a standalone strazad in a container, with all of its state on one named volume you can back up.
pagetype: how-to
weight: 20
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: Linux with Docker Engine 29.4.0, where the release image was not published yet, so the pull, the run, the log, the verify block and the container removal were not run. The inspect command and a listing of the image's files ran against the demo stack's strazad image, built locally from deploy/Dockerfile at v1.1.0-104-g07d2df0e, the volume ownership step and the volume removal ran with busybox:1.36 on a scratch volume, and the permission-denied line and the list of what the data directory holds come from strazad at the tested version in an Alpine 3.20 container. The enterprise compose file passed docker compose config and was not brought up
  date: 2026-10-06
applies_to: both
who: You, as the operator of the Docker host
where: A terminal with Docker Engine
steps: true
keywords: docker container volume image uid
---


You run a standalone strazad in a container on a Docker host, with its state on one named volume. You need Docker Engine, and `jq` for one check. At the end the server answers on `127.0.0.1:8420` as an unprivileged user, and everything it wrote sits on the volume.

The image is built from scratch and holds the `strazad` and `strazactl` binaries, a bundle of public certificate authorities, and the license and notice files under `/licenses/`. It has no shell and no package manager. The process keeps everything it writes in one directory that you mount as a volume, and in the enterprise profile that directory stays nearly empty, because Postgres and NATS hold the state.

{{< note title="This page starts one standalone server" >}}The image defaults to the enterprise profile, so the run command below passes `serve --profile standalone`. For Postgres, NATS and an external identity provider, read [Run the enterprise profile](#run-the-enterprise-profile).{{< /note >}}

## Know the image


The release image is `ghcr.io/strazahq/straza`, built from `deploy/Dockerfile` in the repository and tagged with each release version and `latest`. Pull the release and inspect it before running it.

{{< command terminal="Terminal" purpose="on the Docker host" >}}
```sh
docker pull ghcr.io/strazahq/straza:1.1.1
docker inspect --format 'user={{.Config.User}} entrypoint={{json .Config.Entrypoint}} cmd={{json .Config.Cmd}} volumes={{json .Config.Volumes}} exposed={{json .Config.ExposedPorts}} env={{json .Config.Env}}' ghcr.io/strazahq/straza:1.1.1
```
{{< /command >}}


The answer shows four facts that shape every deployment:

| Fact | What it means for you |
|---|---|
| The user and group are `65532`, the id the distroless images call nonroot. | The volume must belong to 65532 before the first start. |
| The data directory is `/var/lib/straza`, set through `STRAZA_DATA_DIR` and declared as a volume. | Mount your named volume there. |
| The default command is `serve --profile enterprise --listen 0.0.0.0:8420`, the enterprise profile on every interface of the container. | A standalone container passes its own `serve` arguments. |
| Only port 8420 is exposed. | The approver listener on 8443 is not reachable from the host until you add a `-p` mapping for it. |

## Hand the volume to the runtime user


Docker creates a named volume owned by root, and a process running as 65532 cannot write into it. Create the volume and change its owner once with a throwaway container, the same step the compose file in the repository runs as an init service.

{{< command terminal="Terminal" purpose="on the Docker host" >}}
```sh
docker volume create straza-data
docker run --rm -v straza-data:/var/lib/straza busybox:1.36 chown -R 65532:65532 /var/lib/straza
```
{{< /command >}}

{{< see >}}The `chown` prints nothing when it succeeds.{{< /see >}}

## Run the standalone profile


Start the container with the volume, the port published on the loopback address of the host, and the standalone profile. Two flags matter:

- `--listen 0.0.0.0:8420`, because the profile's default binds the container's own loopback address, which nothing outside the container reaches.
- `--stop-timeout 30`, so strazad gets the 30 seconds it needs to finish requests and write its last audit records. Docker's default of 10 seconds would kill it first.

{{< command terminal="Terminal" purpose="on the Docker host" >}}
```sh
docker run -d --stop-timeout 30 --name straza -v straza-data:/var/lib/straza -p 127.0.0.1:8420:8420 ghcr.io/strazahq/straza:1.1.1 serve --profile standalone --listen 0.0.0.0:8420
docker logs straza
```
{{< /command >}}

{{< see >}}The last line of the log is `strazad serving` with the profile `standalone` and the public URL `http://127.0.0.1:8420`.{{< /see >}}


The log is one JSON object per line. On a first boot it records these lines, among others:

- the approver TLS pair minted under `/var/lib/straza/approver-tls`
- `store ready` with the driver `sqlite`
- the password of the bootstrap admin `admin` and of the emergency account `break-glass`
- `event bus ready` with `embedded` set to true
- the approver listener on port 8443

{{< now title="Save both passwords from your log now" >}}Neither password is shown again.{{< /now >}}

If you publish another host port, also set `-e STRAZA_PUBLIC_URL=http://127.0.0.1:PORT` with that port, because clients on this host use that address. The approver URL in the log is the container's internal address. Phone access also requires publishing its port and setting `STRAZA_APPROVER_TLS_PUBLIC_URL` to an address the phone can reach.

{{< fails >}}
`strazad: server.approverTLS auto-mint: approver TLS state dir: mkdir /var/lib/straza/approver-tls: permission denied`
: The volume still belongs to root, so the container stopped at once. Run the `chown` of [Hand the volume to the runtime user](#hand-the-volume-to-the-runtime-user), then `docker rm straza` and start it again.
{{< /fails >}}

## Verify


Ask the server from the host, then confirm which user owns the process and what landed in the volume.

{{< command terminal="Terminal" purpose="on the Docker host" >}}
```sh
curl -s http://127.0.0.1:8420/version | jq -c '{version, profile}'
docker top straza -o uid,pid,cmd
docker run --rm -v straza-data:/v:ro busybox:1.36 ls -ln /v
```
{{< /command >}}


{{< see >}}The image's version with the profile `standalone`, the process running as UID 65532, and every entry of the volume owned by 65532.{{< /see >}}

The volume holds `approver-tls`, `apps`, `nats`, `secret.key`, and `straza.db` with its `-shm` and `-wal` files. Those are the approver certificate pair, the empty apps directory, the embedded event bus's storage, the key-encryption key for stored credentials, and the SQLite store with its write-ahead log. Back up the volume as a whole and you have the standalone server, as [Backup and upgrade]({{< relref "guides/operate/backup-and-upgrade.md" >}}) shows.

## Run the enterprise profile {.nostep}


The image's default command is the enterprise profile. It keeps its store in a Postgres database, signs people in through an external login provider and, once it runs more than one replica, shares a NATS server with JetStream between them. Each is named through environment variables.

A minimal working example is the compose file at `deploy/compose/docker-compose.yaml` in the repository. It holds:

- a `postgres:16` service
- a `nats:2` service started with JetStream on a volume
- the ownership init step from above
- strazad with `STRAZA_STORE_DRIVER=postgres`, `STRAZA_STORE_DSN` pointing at the database and `STRAZA_EVENTS_URL=nats://nats:4222`

The two `STRAZA_OIDC_*` variables are commented out in the file, so set them to your identity provider before `docker compose up`.

With the store and the event stream in their own services, the data volume keeps only what strazad mints at first boot: the key-encryption key unless you mount one, the empty apps directory and, in the repository compose file, the push relay token and the WebPush key. Back up the volume with the database.


Settings without an environment variable, such as sinks and delegated admin areas, ride a config file: mount it read-only and point `STRAZA_CONFIG` at it, as the Helm chart does with `/etc/straza/straza.yaml`. Environment variables still win over the file. The shape with several replicas, one database and one event bus is [The enterprise shape]({{< relref "guides/operate/enterprise-shape.md" >}}).

## Undo {.nostep}


Stop and remove the container. If you want nothing left, remove the volume as well.

{{< command terminal="Terminal" purpose="on the Docker host" >}}
```sh
docker rm -f straza
docker volume rm straza-data
```
{{< /command >}}

Removing the volume deletes the approver certificate with the rest, so every phone that enrolled against this container enrolls again after the next first boot.
