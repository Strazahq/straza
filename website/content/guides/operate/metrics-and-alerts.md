---
title: Metrics and alerts
description: Scrape strazad's metrics with a token, read what each one counts, and alert on the counters that mean a person must act.
summary: >-
  strazad serves its own counts at `/metrics` in the Prometheus text format, labelled by server,
  sink, route, status, effect and lane, never by session or user. Set `server.metricsToken` and
  scrape every replica at its own address. Alert on the counters that mean a person must act,
  such as lost audit records.
pagetype: how-to
weight: 92
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: An Alpine 3.20 container with a standalone strazad, where the /metrics answer of a fresh boot, the answer after one request to /version, and both requests of the token step against a server started with STRAZA_METRICS_TOKEN were run with curl on the default address. The Prometheus scrape job and the alert rules were written from the metric names and the Prometheus configuration page and were not loaded into a Prometheus server
  except: the Prometheus scrape job and the alert rules, never loaded into Prometheus
  date: 2026-10-06
applies_to: both
who: You, as the operator who runs strazad
where: The strazad config file and your Prometheus server
steps: true
keywords: metrics prometheus alerts scrape monitoring metricstoken counters
---


strazad counts what it decides, what it records and what it delivers, and serves those counts at `/metrics` on its main listener in the Prometheus text format. This page sets a token on that endpoint, points Prometheus at it, says what each metric counts, and names the conditions worth an alert. /metrics labels its counts by MCP server, sink, route, status, effect and lane. No metric names a session or a user. The endpoint carries only Straza's own series, with no Go runtime or process metrics beside them, and the approver listener on port 8443 does not serve it.

## Set a metrics token


`/metrics` answers anyone who reaches the main listener until you set `server.metricsToken`. With a token set, every scrape must send it as a bearer token. Generate a long random value and put it in the config file on every strazad host:

```yaml
server:
  metricsToken: REPLACE_WITH_THE_GENERATED_VALUE
```

`openssl rand -hex 32` prints a value that fits. The environment variable `STRAZA_METRICS_TOKEN` sets the same thing, and on the Helm chart the key goes in `configYaml`. Restart strazad after the change, because it reads the token at boot.


{{< command terminal="Terminal" purpose="on the strazad host" >}}
```sh
curl -s http://127.0.0.1:8420/metrics
curl -s -H "Authorization: Bearer $STRAZA_METRICS_TOKEN" http://127.0.0.1:8420/metrics | head -3
```
{{< /command >}}

{{< see >}}The first request answers 401 with `{"error":"metrics token required (server.metricsToken)"}`. The second prints the start of the metrics, beginning with `# HELP straza_approvals_unroutable_total`.{{< /see >}}

A token does not make the endpoint fit for a public ingress. Scrape it over the private network, as [TLS and exposure]({{< relref "guides/operate/tls-and-exposure.md#decide-what-faces-the-internet" >}}) explains, and read [Hardening]({{< relref "security/hardening.md" >}}) for the rest of the server's exposure settings.

## Point Prometheus at every replica


Each strazad process keeps its own counts, and they start at zero when it starts. Scrape every replica at its own address, so a counter never jumps between two processes behind a load balancer. This scrape job reads the token from a file on the Prometheus host:

```yaml
scrape_configs:
  - job_name: straza
    scheme: https
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/straza-metrics-token
    static_configs:
      - targets: ["straza-1.internal:8420", "straza-2.internal:8420"]
```

Drop `scheme: https` for a main listener without TLS on a trusted network. On Kubernetes, discover the pods.


A fresh server shows its counters without labels at zero and its two storage gauges. A labelled series appears with the first event it counts, so `straza_pdp_decisions_total` is absent until strazad has decided something. After one request to `/version`, the request counter reads:

```text
straza_http_requests_total{route="GET /version",status="200"} 1
```

## Know what each metric counts


The route label is the route pattern strazad matched, such as `GET /version`, `POST /v1/decide` or `/mcp`, and a dash when no route matched. Two gauges are measured when strazad starts and then once an hour.

| Metric | What it counts |
|---|---|
| `straza_pdp_decisions_total` | By `effect`. Decisions strazad makes itself, `allow` or `deny`. They come from `POST /v1/decide`, which a client calls for a rule that needs a person's approval or a server check, and from the MCP gateway. A hook that decides from its local snapshot does not count here. |
| `straza_pdp_decision_seconds` | By `effect`. How long those decisions took, as a histogram from 20 microseconds to 100 milliseconds. |
| `straza_failclosed_total` | By `lane`. Internal failures answered as a deny, with `hook` for `/v1/decide`, `gateway` for a call to an MCP server on `/mcp`, and `native` for Straza's own tools on `/mcp`, such as `straza__approval_request`. A failed approval service, fingerprint or classifier counts, and so does a record the full audit queue refused under `block`. Policy denies and approval outcomes never count. |
| `straza_gateway_throttled_total` | By `app`. Tool calls the gateway refused because one session called one MCP server faster than the server's `straza.limits.rps` allows. A server whose manifest sets no rate has no limit. The drafting tools of the built-in `straza` server have their own limits: two calls a second for one session, and one submitted draft every ten seconds for one person. |
| `straza_gateway_replies_withheld_total` | By `app`. Tool replies the gateway withheld because they held the credential Straza injects for that server. Each one also leaves a deny record and a critical `credentialEcho` verdict on the audit chain, so alert on any increment. |
| `straza_gateway_catalog_oversize_total` | Catalog builds with more tools than `apps.catalog.warnSize`, 100 by default. The warning in the log names the role. |
| `straza_approvals_unroutable_total` | Approval requests denied at once because nobody could decide them: the rule names no approver role, and no person behind the requester can decide, such as for an agent with no sponsor or a disabled one. A rule whose approver role has no holder is not counted here, because its request still opens and waits. |
| `straza_audit_lost_total` | Audit records strazad took in and could not confirm as written to the database. It starts at zero in each process. |
| `straza_audit_dropped_total` | Audit records dropped because the in-memory audit queue was full, under `governance.auditBackpressure: drop-with-counter`. |
| `straza_audit_refused_total` | Records a client uploaded that `POST /v1/audit/batch` refused, because the session they name is not a session id, is unknown, or belongs to another user or device. The client has deleted them. |
| `straza_outbox_records_lost_total` | Admin, identity, authentication and approval records, identity events and a user revoke's event that strazad could not write to its database even after one retry. The change each one records stands without it on the audit chain, and each leaves one error line that names it, so any increment is a gap to look into. It also counts once a device revoke whose answer was lost while the device's sessions could not be read, so their `session.end` records are missing. The refusal record of an approver phone is not counted, because the phone's next refused request writes it again. It starts at zero in each process. |
| `straza_outbox_set_aside_total` | Rows of the database outbox that the relay set aside, because the event bus refused them as larger than its maximum message size, 1 MiB by default. Such a row stays in the outbox, and its record never reaches the chain or the sinks, while every later record is published. strazad cuts each record to 256 KiB when it writes it, so only an external NATS server whose `max_payload` is below 256 KiB, or a row written past strazad, counts here. It starts at zero in each process. |
| `straza_sink_deadletter_total` | By `sink`. Deliveries a sink parked, because the receiver refused the event or a failure outlived its retries. Parked events are kept for a replay. |
| `straza_sink_duplicates_total` | By `sink`. Deliveries the receiver answered with 409, which means it already held the event. |
| `straza_sink_replayed_total` | By `sink`. Parked events that an operator's replay delivered and took off the parked list. |
| `straza_http_requests_total` | By `route` and `status`. Every answered request on both listeners. `/healthz`, `/readyz` and `/metrics` are left out. |
| `straza_http_request_seconds` | By `route`. How long requests took, with the same three routes left out. A streamed answer counts when the stream ends. |
| `straza_http_errors_total` | By `route` and `status`. Server-side failures: answers of 500 and above, recovered panics, and internal JSON-RPC errors on `/mcp`, whose status reads `rpc` and the code. Answers in the 400 range are the caller's mistake and do not count. |
| `straza_transcript_store_bytes` | Bytes the recorded transcripts take. On Postgres it is the size of the transcript table, and on SQLite the size of the whole database file. |
| `straza_data_disk_free_bytes` | Free bytes on the filesystem that holds the data directory, or -1 when strazad cannot tell. |

## Alert on what needs a person


Each row below is a condition that the metric's own help text calls an alert, or a counter with one error log line per increment. The log line carries the cause, so an alert sends you to the log first.

| Alert when | What it means, and what you do |
|---|---|
| `straza_audit_lost_total` or `straza_audit_dropped_total` rises | A decision ran and its audit record never reached the chain. Read the strazad log for `audit record lost`, as [Doctor and logs]({{< relref "guides/operate/doctor-and-logs.md#read-the-server-log" >}}) explains, and look at the database. |
| `straza_audit_refused_total` rises | A client's uploaded records were refused, and the log line is their only trace. Read the warn line that starts `audit batch:`, which names the session and the reason. |
| `straza_outbox_records_lost_total` rises | An admin, identity, authentication or approval record is missing from the chain although its change stands. Read the error line that starts `outbox: a record could not be written after one retry`, `approval: the record could not be written after one retry` or `device revoke: the revoke committed but the device's sessions could not be listed`, which names the record or the device, and look at the database. |
| `straza_gateway_replies_withheld_total` rises | An MCP server answered a call with the credential Straza injects for it, so the gateway withheld the reply. Find the critical `credentialEcho` verdict in Audit, which names the server and the tool, and fix or remove the server before you give it the credential again. |
| `straza_outbox_set_aside_total` rises | A record can never be published, so it is missing from the chain and the sinks. Read the error line `outbox row set aside`, which names the row's id, subject and size and the bus's `max_payload`. When `max_payload` is below 262144, raise it on the NATS server to its 1 MiB default, then run `UPDATE events_outbox SET attempts = 0 WHERE attempts = -1` so the relay publishes the rows it set aside. Otherwise read the row in `events_outbox` and report the line to the Straza project. |
| `straza_failclosed_total` rises | An internal failure turned a call into a deny. Find the error line `fail-closed: internal failure answered as a deny` and its `correlation_id`. |
| `straza_http_errors_total` rises | strazad answered a request with a server-side failure. Each count has one error line in the log, with its `correlation_id` and route. |
| `straza_approvals_unroutable_total` rises | An approval rule that names no approver role found no person behind the requester who can decide. Read the deny reason or the warn line `approval unroutable; denied at request`, then set the agent's sponsor to an active person in your identity manager, or add an approver role to the rule. |
| `straza_sink_deadletter_total` rises | A sink receiver refuses events or has been down past its retries. Run `strazactl sinks list`, fix the receiver, then `strazactl sinks replay <sink>`, as [Sinks and SIEM]({{< relref "guides/audit/sinks-and-siem.md" >}}) shows. |
| `straza_sink_duplicates_total` grows steadily | The receiver is not removing duplicates by event id. Check the receiver's pipeline. One 409 after a redelivery is normal. |
| `straza_gateway_throttled_total` keeps climbing | An agent calls one MCP server in a tight loop. Find the session in the gateway's deny records, which name the rate. |
| `straza_transcript_store_bytes` passes `governance.transcriptBytesWatermark` | Recording is filling the disk. strazad also logs `transcript storage above watermark` every hour. Narrow the recording rule, shorten `governance.captureRetention`, grow the volume, or raise the watermark. |
| `straza_data_disk_free_bytes` runs low | The data directory's disk is filling. Free or grow the disk. In the enterprise profile the database disk belongs to Postgres, so watch it there too. |


These rules turn five of the rows into Prometheus alerts. The windows and the disk threshold are examples, so set them to your own on-call habits.

```yaml
groups:
  - name: straza
    rules:
      - alert: StrazaAuditRecordsLost
        expr: increase(straza_audit_lost_total[10m]) > 0 or increase(straza_audit_dropped_total[10m]) > 0
        annotations:
          summary: strazad lost or dropped audit records. Read its log for "audit record lost".
      - alert: StrazaOutboxRowSetAside
        expr: increase(straza_outbox_set_aside_total[10m]) > 0
        annotations:
          summary: A record can never be published. Read the strazad log for "outbox row set aside".
      - alert: StrazaFailClosed
        expr: increase(straza_failclosed_total[10m]) > 0
        annotations:
          summary: An internal failure was answered as a deny. Find the error line by its correlation_id.
      - alert: StrazaSinkParked
        expr: increase(straza_sink_deadletter_total[15m]) > 0
        annotations:
          summary: The sink {{ $labels.sink }} parked events. Fix the receiver, then replay them.
      - alert: StrazaTranscriptsAboveWatermark
        expr: straza_transcript_store_bytes > 10737418240
        annotations:
          summary: Recorded transcripts are above 10 GiB.
```

## Settings that move these numbers {.nostep}


A few settings decide when a counter moves, so read them together with your alerts. The [Configuration]({{< relref "reference/configuration.md" >}}) page lists every key.

| Setting | Default, and what it changes |
|---|---|
| `governance.auditBackpressure` | Default `drop-with-counter` on standalone, `block` on enterprise. Under `drop-with-counter` a database outage shows in the lost and dropped counters. Under `block`, decisions wait and then fail closed, which shows in `straza_failclosed_total`. |
| `governance.transcriptBytesWatermark` | Default 10 GiB. The size at which strazad starts logging the watermark warning. |
| `apps.catalog.warnSize` | Default 100. The catalog size that counts as oversize, and -1 turns the check off. |
| `governance.auditIngestBacklogLimit` | Default 50000. While this many records wait to be published, client uploads get 429 and retry. They show in `straza_http_requests_total` with the route `POST /v1/audit/batch` and the status `429`. |
| `governance.auditIngestPerSessionRPS` | Default 5. Uploads per second one session may make at one strazad, refused with 429 above it. |
| `events.auditStreamMaxAge` and `events.auditStreamMaxBytes` | Default twice `governance.captureRetention`, so 1440h, and 2 GiB. How long and how much the audit stream keeps. A sink that stays down longer than that misses the records that aged out, so act on a parked sink before then. |
| `straza.limits.rps` in a server's manifest | No default. The rate per session and server above which the gateway throttles. |


Patterns across many records, such as a burst of denies from one session, are the audit sentinel's job. It records its verdicts on the audit chain rather than as metrics, and it is off until you set `governance.sentinel.enabled`, as [The sentinel]({{< relref "guides/audit/sentinel.md" >}}) shows.
