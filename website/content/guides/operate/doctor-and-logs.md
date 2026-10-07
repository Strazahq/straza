---
title: Doctor and logs
description: Find out why a hook denies or a server misbehaves, from straza doctor on the agent's machine and the strazad log on the server.
pagetype: how-to
weight: 90
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: A throwaway standalone server in an Alpine 3.20 container on Linux, with the person omar enrolled in a separate home directory for doctor and straza logs, a copy of that state pointed at a closed port for the failure case, and mara's deny record read back after the kill-switch runbook's disable. The log settings were checked by booting with bad values and with debug on. The enterprise boot lines were read with docker logs from the demo stack, which runs v1.1.0-104-g07d2df0e
  date: 2026-10-06
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
| An agent cannot start a session or a hook fails | On that agent machine, run `straza doctor`, then `straza logs` for local hook, spool, and drain errors. |
| strazad has a boot failure or an endpoint fails | Read the server process, container, or Kubernetes log. Find `strazad serving`, or the earlier component that stopped boot. |
| You need the durable record of a decision, login, or administrative change | Read the audit chain, not the operational log. |


These ten symptoms are common ones. Each links the step whose failure help quotes the product's sentence and gives the fix.

| What you see | Where the fix is |
|---|---|
| The sign-in page answers `Invalid username or password.` for the admin | [Sign in as the admin]({{< relref "get-started/first-governed-session.md#sign-in-as-the-admin" >}}) on Your first governed session |
| `straza enroll` stops with `the device code expired before it was approved`, or with an error that ends `connect: connection refused` | [Sign in and enroll]({{< relref "guides/govern-an-agent/enroll-a-machine.md#sign-in-and-enroll" >}}) on Enroll a machine |
| Every governed call is denied with `Straza: no active Straza session. Restart the session so straza can check in` | [Check a deny and an allow]({{< relref "guides/govern-an-agent/claude-code.md#check-a-deny-and-an-allow" >}}) on Claude Code |
| A session start is refused with `attestation level "advisory" is below the required level "managed"` | [Open a session]({{< relref "guides/govern-an-agent/claude-code.md#open-a-session" >}}) on Claude Code |
| An MCP server reads `degraded`, and its reason says `requires a credential and none is stored` or `Straza does not dial 127.0.0.1` | [Check the connection]({{< relref "guides/serve-mcp-apps/add-a-server.md#verify" >}}) on Add a server |
| An agent's MCP call is denied with `needs your own token and none is stored for you` | [Paste your own token]({{< relref "guides/serve-mcp-apps/caller-credentials.md#paste-your-own-token" >}}) on Each caller's own credential |
| strazad does not start, and its message starts `strazad: approver listen` | [The approver listener]({{< relref "reference/ports-and-network.md#the-approver-listener" >}}) on Ports and network |
| The container stops at once with `mkdir /var/lib/straza/approver-tls: permission denied` | [Run the standalone profile]({{< relref "guides/operate/docker.md#run-the-standalone-profile" >}}) on Docker |
| strazad refuses the database with `this strazad's migrations start at 37` | [Upgrade]({{< relref "guides/operate/backup-and-upgrade.md#upgrade" >}}) on Backup and upgrade |
| Your identity manager's SCIM calls get a `403` that says `token lacks scope scim:read` | [Mint a token]({{< relref "guides/connect-identity/generic-scim.md#mint-a-token" >}}) on Generic SCIM |

## Run doctor on the machine


You need the `straza` binary and the state that `straza enroll` wrote under your home. Doctor never changes anything. It keeps going after a failed check, so a broken server does not hide a broken hook. Here it runs for the person `omar`, enrolled but without a harness session yet:

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza doctor
```
{{< /command >}}

```text
[ ok ] enrollment  server http://localhost:8420, 1 snapshot key(s) pinned, straza v1.1.0-117-g106081a8
[ ok ] identity    omar (device 01a1132b-cac4-7d77-8056-1b636a9554ce), device credential valid until 2026-11-05
[ ok ] server      http://localhost:8420 healthy (strazad v1.1.0-117-g106081a8, standalone profile)
[ ok ] approver    https://172.29.77.10:8443 (pin sha256/ILLqvC0KeV4PfC+9QjM5nBEOJaEAEaJTwtUK6tBAScM=, auto-minted cert data/approver-tls/cert.pem, expires 2029-01-03, verified from here: the surface presents the pinned key)
[WARN] session     no active session
                   → start a harness session (the SessionStart hook checks in), or pipe a SessionStart payload through `straza hook`
[WARN] snapshot    no cached snapshot
                   → start a harness session. SessionStart downloads and verifies the active snapshot
[WARN] wiring      no harness has Straza hooks wired
                   → run `straza install claude-code` (or codex/gemini). Enterprise layouts use `straza install --managed --server <server-url> claude-code` (or codex/gemini)
[ ok ] audit-spool no pending audit events (no successful drain recorded yet)
[ ok ] trace       journal on, 0 records; debug off
```


{{< see >}}`[ ok ]` on enrollment, identity and server. The three warnings for session, snapshot and wiring are the normal state of a machine that has not started a harness session yet.{{< /see >}}

Read the list top down, because each line stands on the one above. Everything below the snapshot line reads only this machine.

| Line | What it checks |
|---|---|
| enrollment | The server this machine trusts, how many snapshot signing keys it pinned when it enrolled, and the straza build that is running. |
| identity | The user and the device, with the expiry of the device credential. It turns to a warning two days before that expiry. |
| server | A live probe of `/healthz` and `/version`. |
| approver | Appears only when the server advertises an approver listener. Doctor connects to it and confirms that the presented key matches the pin. |
| session, snapshot | Whether a harness session has checked in and left a verified policy snapshot on this machine. |
| wiring, hook-binary | Which harness has Straza hooks, and whether the binary those hooks point at still exists. A codex or gemini line joins them only when that harness is in use. |
| audit-spool | Where your audit data is right now. Pending events wait for the next drain, and a backlog that persists means uploads fail. |
| audit-dropped | Appears only when the spool cap or the upload size guard discarded records. It stays red until you delete the marker it names, because lost evidence on a governed machine deserves an acknowledgement. |
| trace | The local decision journal that `straza trace show` prints. |
| killswitch | Appears with a live session, and closes the list. It says whether the revocation push lane was verified from this machine. |

## Read a failing doctor


When the server is down or the URL is wrong, the server line fails. Here the machine's `serverUrl` points at a port where nothing listens, and the output is trimmed to the enrollment line, the failed line and the last line:

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza doctor
```
{{< /command >}}

```text
[ ok ] enrollment  server http://localhost:8421, 1 snapshot key(s) pinned, straza v1.1.0-117-g106081a8
[FAIL] server      http://localhost:8421 unreachable: Get "http://localhost:8421/healthz": dial tcp [::1]:8421: connect: connection refused
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
no client errors recorded (log: .straza/state/errors.jsonl)
```

{{< see >}}`no client errors recorded`, which is the healthy answer.{{< /see >}}

The log sits under your home directory, and the path above is trimmed to the part after it. It holds hook, spool and drain failures as the event name, the error and a timestamp, never the payload of the call. Its cap is about one megabyte across one live file and one rotation, so the whole thing is always printable and filtering is your shell's job. Doctor reads the same file and turns a failure inside the last 24 hours into a client-errors warning.

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


Six server lines mean that an audit record may be missing from the chain:

| Log line | What it means |
|---|---|
| `audit record lost: every attempt` | The database could not take the record it names four times, in the standalone profile. |
| `audit record lost: the database refused the record itself` | The database refused the record as a row. That is a strazad defect to report. |
| `audit record not confirmed` | The write of the record ended without an answer. Search the chain for its id before you treat it as lost. |
| `audit records lost at shutdown` | strazad could not write this count of records before it stopped. The line is their only trace, because `straza_audit_lost_total` starts at zero in the next process. |
| `audit batch: spooled records that name one session were refused` | One upload held records that name a session of another user or another device, an unknown session, or a value that is not a session id. The line says which, and gives their count and the first one's id. |
| `audit batch: N spooled records were refused` | The records past the cap of 1000 sessions one upload may name. The client has already deleted them, so the line is their only trace, and `straza_audit_refused_total` counts them. |

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
