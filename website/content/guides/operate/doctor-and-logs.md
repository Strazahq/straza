---
title: Doctor and logs
description: Find out why a hook denies or a server misbehaves, from straza doctor on the agent's machine and the strazad log on the server.
summary: >-
  `straza doctor` reads one agent machine: whether it reaches the server, holds a session and
  delivers its audit, with the fix on every line that is not ok. The strazad log on standard
  error says what the server did, line by line. Neither is evidence, which lives in the
  hash-chained audit record.
pagetype: how-to
weight: 90
draft: false
tested:
  version: v1.0.0-1570-gaf326165-dirty
  platform: A throwaway standalone server in an Alpine 3.20 container on Linux, built from an uncommitted working tree, with the person omar enrolled through a straza service started with STRAZA_HOME in a home directory of his own, for doctor and straza logs, and strazad stopped for the failure case. mara's deny record was read back after the kill-switch runbook's disable on 2026-10-06 at v1.1.0-117-g106081a8, and the log settings were checked then by booting with bad values and with debug on. The enterprise boot lines were read with docker logs from the demo stack, which runs v1.1.0-104-g07d2df0e
  date: 2026-10-21
applies_to: both
who: You, as the operator, and the person at the agent's machine
where: A terminal on the agent's machine, and the strazad log
steps: true
keywords: doctor logs correlation id debug
---


You find out why a hook denies or a server misbehaves, as the operator with the person at the agent's machine. `straza doctor` reads one agent machine, and the strazad log reads the server. At the end you know which side failed, and the line that names the fix.

Doctor says whether that machine can reach the server, hold a session and deliver its audit. The server log says what strazad did, line by line, and it is an operating record that you may rotate or lose. Evidence lives in a third place, the hash-chained and durable audit record. [Metrics and alerts]({{< relref "guides/operate/metrics-and-alerts.md" >}}) covers the counters strazad keeps for a scraper.

## Start with the symptom {.nostep}

| What failed | Start here |
|---|---|
| An agent cannot start a session or a hook fails | On that agent machine, run `straza doctor`, then `straza logs` for local hook, capture and MCP bridge errors. What `straza service` itself did, such as an audit upload that failed, is on that service's standard error. |
| strazad has a boot failure or an endpoint fails | Read the server process, container, or Kubernetes log. Find `strazad serving`, or the earlier component that stopped boot. |
| You need the durable record of a decision, login, or administrative change | Read the audit chain, not the operational log. |


These ten symptoms are common ones. Each links the step whose failure help quotes the product's sentence and gives the fix.

| What you see | Where the fix is |
|---|---|
| The sign-in page answers `Invalid username or password.` for the admin | [Sign in to the console]({{< relref "evaluate/try-it-on-one-laptop.md#sign-in-to-the-console" >}}) on Your first governed session |
| `straza enroll` stops with `The browser sign-in did not finish within 5 minutes`, with `the sign-in code expired before it was approved`, or with an error that ends `connect: connection refused` | [Sign in and enroll]({{< relref "guides/govern-an-agent/enroll-a-machine.md#sign-in-and-enroll" >}}) on Enroll a machine |
| Every governed call is denied with `Straza: no active Straza session. Restart the session so straza can check in` | [Check a deny and an allow]({{< relref "guides/govern-an-agent/claude-code.md#check-a-deny-and-an-allow" >}}) on Claude Code |
| A session start is refused with `attestation level "advisory" is below the required level "managed"` | [Open a session]({{< relref "guides/govern-an-agent/claude-code.md#open-a-session" >}}) on Claude Code |
| An MCP server reads `degraded`, and its reason says `requires a credential and none is stored` or `Straza does not dial 127.0.0.1` | [Check the connection]({{< relref "guides/serve-mcp-apps/add-a-server.md#verify" >}}) on Add a server |
| An agent's MCP call is denied with `needs your own token and none is stored for you` | [Paste your own token]({{< relref "guides/serve-mcp-apps/caller-credentials.md#paste-your-own-token" >}}) on Each caller's own credential |
| strazad does not start, and its message starts `strazad: approver listen` | [The approver listener]({{< relref "reference/ports-and-network.md#the-approver-listener" >}}) on Ports and network |
| The container stops at once with `mkdir /var/lib/straza/approver-tls: permission denied` | [Run the standalone profile]({{< relref "guides/operate/docker.md#run-the-standalone-profile" >}}) on Docker |
| strazad refuses the database with `this strazad's migrations start at 37` | [Upgrade]({{< relref "guides/operate/backup-and-upgrade.md#upgrade" >}}) on Backup and upgrade |
| Your identity manager's SCIM calls get a `403` that says `token lacks scope scim:read` | [Mint a token]({{< relref "guides/connect-identity/generic-scim.md#mint-a-token" >}}) on Generic SCIM |

## Run doctor on the machine


You need the `straza` binary and a running `straza service`, the Straza background service, which holds the enrollment that `straza enroll` made. Doctor asks the service for your enrollment, sessions and audit spool, and reads this machine's own files for the rest. It never changes anything. It keeps going after a failed check, so a broken server does not hide a broken hook, and the checks that read only this machine run even when the service does not answer. Here it runs for the person `omar`, enrolled but without a harness session yet:

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza doctor
```
{{< /command >}}

```text
[ ok ] service     straza service v1.0.0-1570-gaf326165-dirty answers at /home/omar/.straza/service.sock and keeps keys in user custody
[ ok ] enrollment  omar (device 01a15d8e-6842-7281-9538-7998c922bc98) at http://127.0.0.1:8420, key cr_GU1wJJWlS, straza v1.0.0-1570-gaf326165-dirty
[ ok ] server      http://127.0.0.1:8420 healthy (strazad v1.0.0-1570-gaf326165-dirty, standalone profile)
[ ok ] approver    https://172.17.0.4:8443 (pin sha256/q/+fca2tzRIxC/Wkrnde88ShLVP6Qua+Di9tng51vJ8=, auto-minted cert data/approver-tls/cert.pem, expires 2029-01-18, verified from here: the surface presents the pinned key)
[WARN] session     no live session
                   → start a harness session (its SessionStart hook starts one through the Straza background service), or pipe a SessionStart payload through `straza hook`
[WARN] wiring      no harness has Straza hooks wired
                   → run `straza install claude-code` (or codex/gemini). Enterprise layouts use `straza install --managed --server <server-url> claude-code` (or codex/gemini)
[ ok ] trace       journal on, 0 records; debug off
[ ok ] audit-spool no pending audit events (no successful upload recorded yet)
```


{{< see >}}`[ ok ]` on service, enrollment and server. The warnings for session and wiring are the normal state of a machine that has not started a harness session yet.{{< /see >}}

Read the list top down, because each line stands on the one above. The lines from wiring to trace read only this machine.

| Line | What it checks |
|---|---|
| service | Whether `straza service` answers on its socket, or its named pipe on Windows, its version, and whether it keeps your key in its own account or in yours. When it does not answer, the line says how to start it, and the enrollment, server, session and killswitch lines wait for it. |
| enrollment | You, the device, the server this machine is enrolled with, the start of the key's thumbprint and the straza build that is running. It fails when the machine's identity changed since it was enrolled, and warns while the service waits for the server to confirm a check-in whose answer was lost. |
| server | A live probe of `/healthz` and `/version`. |
| approver | Appears only when the server advertises an approver listener. Doctor connects to it and confirms that the presented key matches the pin. |
| session, policy | One line for each live session the service holds for you, one per harness window, with how long its token stays valid. A policy line replaces a session line when the server holds a newer policy that straza could not fetch. |
| managed-content | Appears only on a managed install. It compares the managed wiring with what the server published. |
| wiring, hook-binary | Which harness has Straza hooks, and whether the binary those hooks point at still exists. A codex or gemini line joins them only when that harness is in use. |
| managed layout | Appears only on Windows, and fails when `C:\ProgramData\straza` holds a managed config while accounts other than administrators can change that folder. Straza then ignores the managed config, and the service logs `the managed config is not used` in the Application event log at each start, until an administrator runs `straza install --managed` again, which locks the folder. |
| trace | The local decision journal that `straza trace show` prints. |
| audit-spool | Where your audit data is right now. The service holds pending records for you and uploads them on its own, and a backlog that persists means uploads fail. |
| audit-dropped | Appears only when this machine lost audit before it reached strazad: the spool cap or the upload size guard discarded records, or the service refused decide or capture calls before it could record them. The hint reads `The Straza background service reports this loss to strazad with its next upload, and this check clears once strazad took it`. strazad then holds one `straza.audit.tool` record with the event `audit.dropped` that names the counts and the times. |
| killswitch | Appears with a live session, and closes the list. It says whether the service holds a connected push stream for each of your live sessions. A revoked or lapsed window holds none and is not counted. |

## Read a failing doctor


When the server is down or the URL is wrong, the server line fails. Here strazad is stopped, and the output is trimmed to the enrollment line, the failed line and the last line:

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza doctor
```
{{< /command >}}

```text
[ ok ] enrollment  omar (device 01a15d8e-6842-7281-9538-7998c922bc98) at http://127.0.0.1:8420, key cr_GU1wJJWlS, straza v1.0.0-1570-gaf326165-dirty
[FAIL] server      http://127.0.0.1:8420 unreachable: Get "http://127.0.0.1:8420/healthz": dial tcp 127.0.0.1:8420: connect: connection refused
                   → is strazad running? verify the URL and any TLS/proxy in between; hooks fail closed while it is unreachable (grace TTL permitting)
straza: doctor found failures (see hints above)
```


Every line that is not ok carries an arrow with the action. The command exits 1 when any line failed, so a script can gate on it, and a warning does not fail the run.

Work the list with the arrows. Fix the first failed line, run doctor again, and stop when the list is green or carries only the warnings you expect.

## Read the client error log


Hooks run where their stderr belongs to the harness, so a hook that fails cannot tell you so on the terminal. It writes one line to a local error log instead, and `straza logs` prints that log:

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza logs
```
{{< /command >}}

```text
no client errors recorded (log: /home/omar/.straza/state/errors.jsonl)
```

{{< see >}}`no client errors recorded`, which is the healthy answer.{{< /see >}}

The log sits in `state` under your Straza home, here omar's `/home/omar/.straza`. It holds hook, capture and MCP bridge failures as the event name, the error and a timestamp, never the payload of the call. The audit spool and its uploads live in `straza service`, which logs their failures to its own standard error. Its cap is about one megabyte across one live file and one rotation, so the whole thing is always printable and filtering is your shell's job. Doctor reads the same file and turns a failure inside the last 24 hours into a client-errors warning.

## Read the server log


strazad writes its log to standard error and nowhere else, one JSON object per line by default. Your process manager, container runtime or Kubernetes node owns the rotation and the shipping. Two keys shape the log, the same in both profiles:

| Key | Environment variable | Values |
|---|---|---|
| `log.level` | `STRAZA_LOG_LEVEL`, or `--log-level` on `strazad serve` | `debug`, `info`, `warn`, `error` |
| `log.format` | `STRAZA_LOG_FORMAT` | `json`, `text` |

The server refuses to boot on any other value and names the accepted ones in the error. The level is fixed at boot, so changing it means a restart.


These are the boot lines of the enterprise demo stack's server, trimmed to the ones the next paragraph explains:

```text
{"time":"2026-10-06T10:26:37.162438114Z","level":"INFO","msg":"store ready","driver":"postgres"}
{"time":"2026-10-06T10:26:38.414708053Z","level":"INFO","msg":"external OIDC configured","issuer":"http://localhost:8480/realms/straza","discoveryUrl":"http://keycloak:8080/realms/straza/.well-known/openid-configuration","jit":false,"bootstrapAdmin":true}
{"time":"2026-10-06T10:26:38.468530705Z","level":"INFO","msg":"event bus ready","embedded":false}
{"time":"2026-10-06T10:26:38.479054353Z","level":"INFO","msg":"policy snapshot ready","id":"fcdeabbb17793e6b02e366baaffa47f679d7c6134fd111735c0805d0eab9968b"}
{"time":"2026-10-06T10:26:38.541285195Z","level":"INFO","msg":"sink configured","name":"elastic","type":"webhook","target":"http://elasticsearch:9200/straza-events/_doc?…","batch":0,"subjects":["straza.audit.>","straza.revocation.>"]}
{"time":"2026-10-06T10:26:39.17675033Z","level":"WARN","msg":"serving PLAINTEXT HTTP in the enterprise profile. Set server.tls.{certFile,keyFile} or terminate TLS at your ingress/LB; session tokens and admin API tokens transit this listener. The TLS and exposure guide walks both shapes","docs":"https://docs.straza.ai/guides/operate/tls-and-exposure/"}
{"time":"2026-10-06T10:26:39.177374693Z","level":"INFO","msg":"approver surface serving","addr":"[::]:8443","publicUrl":"https://127.0.0.1:8443","pin":"sha256/FWLhewF1uqb8mj2+J0OTVM2TuIE8vNQdt5lYjwWmpy4=","cert":"/var/lib/straza/approver-tls/cert.pem","certExpires":"2028-12-29T16:18:42Z","autoMinted":true}
{"time":"2026-10-06T10:26:39.177416683Z","level":"INFO","msg":"strazad serving","addr":"[::]:8420","profile":"enterprise","publicUrl":"http://localhost:8420","tls":false,"version":"v1.1.0-104-g07d2df0e"}
```


In the enterprise profile, store, OIDC, event bus and snapshot are the four things a server needs before it can decide anything. A boot that stops before `strazad serving` names the one that failed.

- The sink line shows the redaction rule of the whole log. A target URL is printed as scheme, host and path, and its query string, where a credential could sit, is replaced by a marker.
- A plaintext warning fires in the enterprise profile whenever the main listener has no TLS.
- The approver line names the facts a phone enrollment stands on.
- Last comes the serving line with the version. Comparing it with `straza doctor` on a client is the quickest check that both sides run the build you think they run.


Every request gets a correlation id. strazad takes it from the `X-Request-Id` header when the caller sent a sane one, and mints one otherwise. The server echoes it in the response header, binds it to every log line that request writes as `correlation_id`, and puts it in the body of a 5xx answer. When you report a server error, quote that id, and the operator finds the lines.

The per-request access record exists only at `debug` level, and the probes `/healthz`, `/readyz` and `/metrics` never appear in it. The `info` default therefore stays quiet on a healthy server.


Seven server lines mean that an audit record may be missing from the chain:

| Log line | What it means |
|---|---|
| `audit record lost: every attempt` | The database could not take the record it names four times, in the standalone profile. |
| `audit record lost: the database refused the record itself` | The database refused the record as a row. That is a strazad defect to report. |
| `audit record not confirmed` | The write of the record ended without an answer. Search the chain for its id before you treat it as lost. |
| `audit records lost at shutdown` | strazad could not write this count of records before it stopped. The line is their only trace, because `straza_audit_lost_total` starts at zero in the next process. |
| `audit batch: spooled records that name one session were refused` | One upload held records that name a session of another user or another device, an unknown session, or a value that is not a session id. The line says which, and gives their count and the first one's id. |
| `audit batch: an upload of more than 1000 records was refused with 413` | Something other than a straza client sent an upload of more than 1000 records, so strazad stored none of them. A straza client never sends more than 1000, so find out what sent it. |
| `audit batch: N spooled records were stored with their data omitted` | An upload held more than 65,536 JSON values, so the records past that budget keep their envelope, who acted, on what, the outcome and `dataOmitted` with their size and SHA-256, and lose their other fields. A straza client's upload holds far fewer, so find out what sent it. |
| `outbox: a record could not be written after one retry` | strazad could not write the admin, identity or authentication record the line names by subject and action, even on a second try, so the change stands without its record on the chain. `straza_outbox_records_lost_total` counts it. The same line for an approval record starts `approval: the record could not be written after one retry`. |
| `device revoke: the revoke committed but the device's sessions could not be listed` | A device revoke committed although its answer was lost, and the read of the device's sessions before it had failed, so the sessions it ended have no `session.end` record. The line names the device and the user, and `straza_outbox_records_lost_total` counts it. The sessions list shows which of the user's sessions are revoked. |
| `outbox row set aside` | The event bus refused the outbox row it names by id, subject and size as larger than its `max_payload`, which the line also gives, so its record never reaches the chain or the sinks, and every later record is published. The row stays in `events_outbox` with `attempts` -1, and `straza_outbox_set_aside_total` counts it. strazad cuts each record to 256 KiB when it writes it, so raise a `max_payload` below that on the NATS server and then run `UPDATE events_outbox SET attempts = 0 WHERE attempts = -1`, which lets the relay publish the rows it set aside, or else report the line. |

Two more lines belong to the enterprise profile, and a third follows once its audit queue is full:

| Log line | What it means |
|---|---|
| `audit queue waiting for the database` | strazad holds its audit queue until the database answers, and new decisions wait once the queue is full. |
| `audit queue moving again` | The database took the record, and the queue is being written. |
| `fail-closed: internal failure answered as a deny`, with a cause that begins `the audit queue stayed full` | The queue is full, and this decision was refused. A refused tools/list logs the refusal sentence itself. |

## Know what the log is not {.nostep}


The log is operational and never evidence. Evidence lives in the audit record. Every decision, admin change, login and identity event becomes one CloudEvent that a consumer appends to a hash-chained table, where each record's hash covers the previous hash and its own bytes. Sinks deliver the same records to your SIEM.

Deleting the log loses nothing you would show an auditor. Deleting a chain record breaks the chain, and `strazactl audit verify` names the sequence number where it broke.

This is a deny record for the person `mara`, whom the [Kill-switch runbook]({{< relref "guides/operate/kill-switch-runbook.md" >}}) stops, read back with `strazactl audit tail --user`:

```text
#50 [mara] {"data":{"app":"","command":"git status","effect":"deny","event":"tool.pre","harness":"claude-code","reason":"Straza: session renewal was refused: this device or user has been revoked. Contact your administrator","ruleId":"","session":"01a1132b-84c8-7033-ae15-019d95d7c482","setName":"","snapshot":"aee645782dad8a6bfdf2509f758ac89ede9facd3ad1a37768dbafb6382f53970","tool":"shell.exec","user":"01a11328-ee86-70d6-810e-e66e2cd0af2c"},"id":"27a9b016-81c7-451c-bf3b-1afde4993087","source":"straza","specversion":"1.0","time":"2026-10-06T21:48:14.820020941Z","type":"straza.audit.tool"}
```


A deny record carries the session, the user, the harness, the event kind, the tool and the exact shell command or MCP tool name. It also carries the effect, the rule and PolicySet that decided, the reason the agent saw, and the snapshot id the decision ran against. A gateway record adds the MCP call arguments, capped at 8 KiB with a truncation flag.

Neither the log nor these records carry a prompt or a model reply. Recorded conversation turns exist only when a PolicySet turns recording on, and they land as their own event types. A sink receives them only when you name those types in its subject list, because the default subject set leaves them out.

Upstream credentials never enter either surface. Sink and bus URLs are redacted before they are printed, and secrets stay in the credential store.


Nothing on this page needs undoing, because doctor reads and never writes. When you remove `log.level` from the file, the log level goes back to `info` at the next restart. [Evidence and audit]({{< relref "concepts/evidence.md" >}}) explains the audit chain, and [Sinks and SIEM]({{< relref "guides/audit/sinks-and-siem.md" >}}) forwards it.
