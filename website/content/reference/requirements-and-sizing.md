---
title: Requirements and sizing
description: The processors, memory and disk each shape of Straza needs, what each number rests on, how the audit record grows, and the platforms and versions Straza was tested with.
pagetype: reference
weight: 4
draft: false
applies_to: both
keywords: requirements sizing hardware capacity cpu memory ram disk storage agents fleet audit growth platforms versions tested postgres nats kubernetes helm kind docker chromium
---


Pick the row that matches how you run Straza, then read what that row rests on. A row marked measured comes from a reading of a running system: a load run in a capped container for the standalone rows, and the running demo stack for the client and demo stack rows. A row marked chart default is a setting of the Helm chart, and a row marked estimate was derived from a run on another shape. [How the numbers were measured](#how-the-numbers-were-measured) gives the test setup and the command that repeats it.

## Choose a shape

| Shape | Who it is for | CPU | Memory | Disk | Basis |
|---|---|---|---|---|---|
| Standalone, one host, up to 100 connected agents | A solo operator or a small team | 1 core | 512 MB | About 60 MB for the `strazad` binary, and a data directory that starts near 2 MB and grows with the audit record | Measured |
| Standalone, one host, up to 1,000 connected agents | A team that runs every agent through Straza | 2 cores | 1 GB | The same | Measured |
| Enterprise, each strazad replica | An organization that runs the Helm chart | Request 100m, limit 1 | Request 128Mi, limit 512Mi | None, because Postgres and NATS hold the state | Chart default. Up to about 1,000 connected agents per replica is an estimate |
| Enterprise, the bundled Postgres | The same organization | Request 100m | Request 256Mi, limit 1Gi | 20Gi, then the audit growth below | Chart default |
| Enterprise, the bundled NATS | The same organization | Request 50m | Request 64Mi, limit 256Mi | 5Gi, of which the audit stream takes up to 2 GiB | Chart default |
| The client on each agent machine | Every workstation or host that runs a governed agent | Under 0.1 percent of one core on average | About 12 MiB resident, 46 MiB at its peak | About 25 to 27 MB for the `straza` binary | Measured |
| The demo stack | Someone trying the whole product on one machine | 4 cores | 8 GB, with swap on | 20 GB free before the first boot, and the disk kept under 85 percent full | Measured at idle. The first boot was not measured |

## What each row rests on

### Standalone


Three runs of the load harness passed all of its checks. Each held its agents for 60 seconds in a container capped at the processors and memory shown, with swap off.

| Run | Peak memory of the container | CPU over the steady minute | CPU throttling |
|---|---|---|---|
| 100 agents in 1 CPU and 512 MB | 68 MiB | 14 percent of one core | Two periods during the start, 0.1 seconds in all |
| 100 agents in 2 CPUs and 1 GB | 68 MiB | 15 percent of one core | None |
| 1,000 agents in 2 CPUs and 1 GB | 326 MiB | 24 percent of one core | None |

The 1,000-agent run passed again when repeated with the command below, and the latencies here span both runs. At the 99th percentile, a check-in took 124 to 133 ms and a governed MCP call through the gateway 7 to 9 ms. An approved call resumed within 24 to 42 ms of the approval, and 50 revocations reached their agents' push streams within 104 to 187 ms with none missed. One binding change reached all 1,000 standing `/mcp` streams, 99 percent of them within 1.08 to 1.10 seconds, which includes the gateway's 1 second notification delay.

An idle standalone strazad holds about 50 MiB resident and uses 0.6 percent of one core. Its first boot on an empty data directory took 478 ms. After that boot and a burst of 45,000 requests, the directory held 1.6 MB.

The 512 MB row leaves seven times the 100-agent peak, and the 1 GB row three times the 1,000-agent peak. That room is for bursts of decisions, the SQLite page cache and more transcript capture than the runs used, and these runs did not measure any of the three.

### Enterprise


The chart's defaults are the enterprise rows. The demo stack's enterprise strazad, with its demo agents connected, holds 43 MiB and uses 0.8 percent of one core. Its Postgres holds 62 MiB and its NATS 15 MiB.

The 1,000-agent run peaked at 326 MiB, inside the chart's 512Mi limit, but it ran the standalone profile on SQLite. No replica on Postgres ran at that load, so about 1,000 agents per replica under the default limit is an estimate. Above that, raise the memory limit or add replicas.

A database outage needs memory too. Under `block`, the enterprise default of `governance.auditBackpressure`, a replica whose audit queue is full holds each new decision for up to 25 seconds before it refuses it. [The security model]({{< relref "security/security-model.md#audit-records-can-be-lost" >}}) gives the decision rate at which those held decisions reach the chart's memory limit.

A replica opens up to 16 Postgres connections, or 4 for each processor the Go runtime uses when that is more. Go takes that processor count from the container's CPU limit, so a replica under the chart's limit of 1 opens up to 16. Postgres allows 100 connections unless you raise `max_connections`, and the bundled Postgres keeps that default. Six replicas come close to it, and the chart's autoscaler, once you turn it on, allows 10 replicas by default, which can open up to 160.

The audit stream on NATS keeps a copy of each audit record for 60 days, and at most 2 GiB by default. It drops the oldest records first, so it fits the chart's 5Gi volume.

### The client


The demo agent's `straza daemon` held 11.7 MiB resident, with a peak of 46 MiB since it started, and used 0.8 seconds of CPU over 25 minutes. A hook is a short process that starts for each tool call, decides and exits. The load harness holds it to a budget of 25 ms from start to exit at the 95th percentile on Linux, which this page did not measure.

### The demo stack


Fifteen containers ran on about 4.4 GiB of memory at idle, counting resident and swapped memory. Of the 4.4 GiB, midPoint with its Postgres took 1.6 GiB, Elasticsearch and Kibana 1.8 GiB, Keycloak 0.65 GiB, and strazad with its Postgres and NATS 120 MiB. At their peaks, the containers summed to 5.6 GiB. The host also ran other services and held 3.6 GiB in swap, which is why the row asks for swap on an 8 GB machine.

The images take about 10.8 GB and the volumes about 0.2 GB after the seed. Elasticsearch stops placing new indices on a disk that is more than 85 percent full, so the row keeps the disk below that. The 20 GB is an estimate: those 11 GB, plus room for the index and the logs to grow while the disk stays under 85 percent full.

## How the numbers were measured


The standalone rows come from the load harness in the repository's `test/load` directory. It boots a real standalone strazad, with SQLite and the embedded NATS, and drives real agent sessions against it from the same process. Each agent checks in, holds a push stream and a standing `/mcp` stream open, and checks in again every 30 seconds. One agent in ten sends a recorded conversation turn every 20 seconds. While the agents stand, the harness calls the gateway, revokes sessions, approves held calls and changes a binding, and it times each step against a budget.

Because the agents share the process with strazad, every memory and CPU number from these runs is an upper bound for strazad and its agents together.

The runs used the harness built from source with Go 1.26.6 on 2026-10-07, in an Alpine 3.20 container with swap off. The host had 4 cores and 7.3 GiB of memory, and ran Rocky Linux 10.1, Docker Engine 29.4.0 and the demo stack at the same time. Memory is the peak that the container's control group charged, and CPU is the container's use over the steady minute. The command below repeats the 1,000-agent run.

{{< command terminal="Terminal" purpose="in a clone of the repository" >}}
```sh
mkdir -p /tmp/sizing
CGO_ENABLED=0 go build -o /tmp/sizing/loadrig ./test/load
docker run --rm --cpus 2 --memory 1g --memory-swap 1g --user "$(id -u):$(id -g)" \
  -v /tmp/sizing:/w -w /w alpine:3.20 \
  ./loadrig -only fleet -fleet 1000 -fleet-duration 60s -fleet-capture-pct 10
```
{{< /command >}}

{{< see >}}The last line reads `report: perf-report.json (pass=true)`, and `perf-report.md` in `/tmp/sizing` holds one row for each probe.{{< /see >}}

## How the audit record grows


Decisions, approvals, sign-ins and admin changes write audit records, and [Events and the audit record]({{< relref "reference/events.md" >}}) lists each kind. A stored record takes about 0.9 KB with its index on SQLite, and an MCP decision on Postgres takes 911 bytes before its index, so plan on about 1 KB. On the demo stack's Postgres, a tool decision averaged 741 bytes and an MCP decision 911 bytes before the index. The audit chain keeps every record, and no setting deletes old ones, so this part grows for the life of the deployment.

Two more copies are bounded. The audit stream keeps each record, at about 0.9 KB, for `events.auditStreamMaxAge`, 60 days by default, and up to `events.auditStreamMaxBytes`, 2 GiB by default. It sits in the data directory when strazad runs its embedded NATS, and on the NATS server's volume otherwise. The outbox table keeps a published record for `governance.outboxBulkRetention`, 48 hours by default. A recorded conversation turn is as large as its prompt and reply, and it stays for `governance.captureRetention`, 30 days by default.

To size the database, multiply the audit records a day by 1 KB. An agent that makes 5 decisions a minute around the clock writes 7,200 records a day, about 7.2 MB. Ten such agents write about 72 MB a day, about 2.2 GB a month. Agents that work a few hours a day write a fraction of that.

## Tested with


This table records what the docs and the tests ran on, and it makes no promise of support for each version.

| Component | Version | How it was tested |
|---|---|---|
| `strazad`, `strazactl` and `straza` on Linux amd64 | Rocky Linux 10.1 and Alpine 3.20 | The install walk ran in an Alpine 3.20 container on 2026-10-06, and the runs on this page on a Rocky Linux 10.1 host on 2026-10-07. |
| The Go test suite on Windows amd64 | Windows with Go 1.26.6 | The full suite passed natively on 2026-09-22, and the three binaries were built in release shape on 2026-10-07. |
| The Go test suite and `straza` on macOS | One Mac with Docker Desktop 29.7.2 | The commit checks, which run the suite, passed on 2026-10-01, and `straza` served Claude Desktop the same day. |
| The other release builds | Linux arm64, macOS amd64 and arm64, Windows arm64 | Built in release shape with `CGO_ENABLED=0` on 2026-10-07, and not run. |
| The container image | linux/amd64 | Built from `deploy/Dockerfile` and run as the demo stack's strazad. The release also publishes linux/arm64, which was not run. |
| Go, for a build from source | 1.26.6 | The version every build and run on this page used. `go.mod` requires Go 1.26 and names go1.26.6 as its toolchain, which the go command fetches when the installed one is older. |
| PostgreSQL | 16.13 | The demo stack's database for the enterprise profile. The chart's bundled Postgres uses the `postgres:16` image. |
| NATS | 2.14.3 | The demo stack's event bus. The chart's bundled NATS uses the `nats:2` image. |
| Keycloak | 26.0.8 | The demo stack's OIDC login provider. |
| midPoint | 4.10.3 | The demo stack's identity manager, connected over SCIM. |
| Elasticsearch and Kibana | 8.14.3 | The demo stack's SIEM overlay. |
| Docker Engine and Docker Compose | Engine 29.4.0, Compose 5.1.3 | The demo stack and the runs on this page. |
| kind, kubectl and Kubernetes | kind 0.33.0, kubectl 1.37.0, node image v1.37.0 | The chart walk on a kind cluster on 2026-09-19. |
| Helm | 3.16.4 | Rendered and linted chart 1.1.0 on 2026-10-06, and installed the chart in the kind walk. |
| Chromium | 151.0.7922.34, the headless shell, driven by Playwright 1.62.1 | The console walks. |

## What these numbers leave out


- The agents ran in the same process as strazad, so the server's own share of memory and CPU was not separated. That needs the harness on a second machine.
- Postgres ran in none of the capped containers, so the enterprise numbers per replica rest on the chart defaults and the standalone runs.
- No run held more than 1,000 agents in a capped container.
- A server on arm64, on macOS or on Windows was not measured for this page.
- The first boot of the demo stack was not timed or measured.
