---
title: The sentinel
description: The audit sentinel watches each session's decisions as they reach the audit chain and records a verdict when an agent probes the policy edge, and you read and act on the verdicts in Audit.
pagetype: how-to
weight: 20
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: A throwaway standalone server on Linux with the sentinel turned on in its config file, where a person's machine sent six denied commands through the Claude Code hook within a minute. The verdicts were read with strazactl and in headless Chromium, and no session was revoked
  date: 2026-10-06
applies_to: both
who: You, as the admin who runs strazad
where: The config file, a terminal and the console
steps: true
modes: [console, cli]
mode_default: console
keywords: sentinel audit verdict detector deny burst variant write then execute capture content tool mix alert revoke session
---


The sentinel looks for patterns across a session that one decision alone cannot show, such as an agent that keeps trying commands the policy denies. When it finds one, it writes a verdict to the same chain, so the verdict is hash-chained like the records it points at. The sentinel alerts and never blocks or revokes anything. It is off by default in both profiles.

## Before you start {.nostep}

- Access to strazad's config file and a way to restart strazad.
- A console sign-in that holds the `audit` area, or `strazactl` logged in as an admin.

## Turn it on


Add the setting to the config file. It has no environment variable.

```yaml
governance:
  sentinel:
    enabled: true
```

Restart strazad.

{{< see >}}The log says `audit sentinel enabled`, with the two deny counts it uses.{{< /see >}}

{{< details summary="Recorded log line" >}}
```text
{"time":"2026-10-06T19:54:09.252136872Z","level":"INFO","msg":"audit sentinel enabled","denyBurstWarn":5,"denyBurstCritical":10}
```
{{< /details >}}


When you first turn it on, the sentinel judges the records written from then on, not the history of the chain. It keeps its windows in memory, so a restart starts them afresh, and it then reads on from where it stopped. When it stops on an error, strazad logs `sentinel down (detection gap)` and starts it again, and calls go on being decided as before.

## Know what it looks for


Five detectors run on each session. A detector repeats a verdict for the same session only after its window has passed, so a long burst does not flood the chain.

| Detector | Severity | What raises it |
|---|---|---|
| `denyBurst` | `warn`, then `critical` | 5 denied calls in one session within a minute raise `warn`, and 10 raise `critical`. |
| `denyThenVariant` | `critical` | A denied shell command comes back rewritten or wrapped within 10 minutes, or comes back base64-encoded. An identical retry counts toward the burst instead. |
| `writeThenExecute` | `critical` | The session writes a file and then runs a command that names that file, within 30 minutes. |
| `captureContent` | `critical` | A recorded prompt or reply holds the shape of a credential, such as an AWS access key id, a private key, a GitHub token, a bearer token or a Straza token. It sees only sessions a policy records, and text recorded with secrets masked does not match. |
| `toolMixAnomaly` | `info` | A user calls a tool outside their usual mix, once they have 50 audit events since strazad started. |

Each verdict holds the session, the user, the detector, the severity, a reason in one sentence, the ids of the records it points at as `evidence`, and the window when the detector has one.

## Tune the thresholds


The counts and windows sit beside `enabled` in the config file, and these are their defaults.

```yaml
governance:
  sentinel:
    enabled: true
    denyBurstWarn: 5
    denyBurstCritical: 10
    denyBurstWindow: 1m
    variantWindow: 10m
    writeExecWindow: 30m
    baselineMinEvents: 50
```

Raise `denyBurstWarn` when agents on your deployment retry denied calls on purpose, and shorten a window to forget old events sooner. [Configuration]({{< relref "reference/configuration.md#config-governance" >}}) lists each key.

{{< fails >}}
`governance.sentinel: denyBurstCritical (<n>) must be >= denyBurstWarn (<n>)`
: strazad refuses to start with this, with your two numbers in place of `<n>`. Set `denyBurstCritical` to at least `denyBurstWarn`.

`governance.sentinel: detector windows must be positive durations`
: A window is 0 or negative. Give each window a duration such as `1m`.
{{< /fails >}}

## Read the verdicts

{{< console >}}

{{< clicks "Audit" "Lens" "sentinel" >}}

The note under the controls reads `sentinel = type sentinel: the audit watcher's verdicts on sessions. Alert-only: it flags, it never revokes`. Each verdict row names the detector and the count of its evidence, such as `denyThenVariant sentinel verdict · evidence ×2`, with its reason, such as `a denied command was re-attempted as a rewritten/wrapped variant within 10m0s`. The **Effect** column holds the severity, and a critical verdict has a red edge. Sentinel rows never fold into one another.

{{< shot name="audit-sentinel" caption="Verdicts under the `sentinel` lens, with the severity under **Effect**, the detector, its evidence and **Revoke session** on a critical one." >}}

Select a row to open the record. A strip at the top repeats the severity, the detector and the session, with `The sentinel is alert-only: it never revokes on its own, and revoking is your call.` **The record as stored** lists the evidence ids, which you can search for in Audit.
{{< /console >}}

{{< cli >}}

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl audit tail --type straza.audit.sentinel --limit 200
```
{{< /command >}}

{{< see >}}One line per verdict, newest last, each with its detector, severity and reason.{{< /see >}}

{{< details summary="Recorded output, one verdict" >}}
```text
#74 [alice] {"data":{"detector":"denyThenVariant","evidence":["29f32da3-4820-4d97-b881-3937b4c461e7","4681a7fa-1053-4efa-a905-4874911a50eb"],"reason":"a denied command was re-attempted as a rewritten/wrapped variant within 10m0s","session":"01a112d3-41cb-7bf4-9b63-7a6111400c8e","severity":"critical","user":"01a112c8-8bba-7882-a955-fdee5b4e414e","window":"10m0s"},"id":"5aa6f5eb-bcee-4413-8a6d-8b59fa4ebaf3","source":"strazad-sentinel","specversion":"1.0","time":"2026-10-06T20:06:37.385131454Z","type":"straza.audit.sentinel"}
```
{{< /details >}}

`--limit` counts the newest records before the type filter, so a busy chain needs a larger number to reach older verdicts.
{{< /cli >}}

A burst reads like `5 denied actions within 1m0s. The agent is probing the policy edge`. The same verdicts reach every sink that keeps the default subjects, so your SIEM can page on `straza.audit.sentinel`, as [Sinks and SIEM]({{< relref "guides/audit/sinks-and-siem.md" >}}) sets up.

## Act on a verdict


A verdict is a lead, and the decision what to do is yours. Read the evidence first: open the session under Sessions, or search Audit for the session id.

{{< console >}}
A critical verdict carries **Revoke session** on its row and on its record. The question says what a revoke does.

{{< see >}}`Session <id>… is revoked. Every agent action in it is denied within seconds.`, with the start of the session id.{{< /see >}}

A session revoke is a stand-down, not a ban. The device can check in again as a new session.
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl sessions revoke <session-id>
```
{{< /command >}}

The session id is the `session` field of the verdict.
{{< /cli >}}

To stop the person or the machine as well, revoke the device or lock the user, as the [Kill-switch runbook]({{< relref "guides/operate/kill-switch-runbook.md" >}}) shows.

## Next {.nostep}

- [Overview and Audit]({{< relref "guides/console/overview-and-audit.md" >}}) walks the Audit screen the verdicts appear on.
- [Events and the audit record]({{< relref "reference/events.md" >}}) lists the fields of `straza.audit.sentinel`.
