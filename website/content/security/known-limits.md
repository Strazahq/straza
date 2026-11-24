---
title: Known limits
description: "The limits of what Straza protects on an enterprise installation: who can reach each one, what you do about it and what the roadmap plans."
pagetype: reference
weight: 12
draft: false
applies_to: both
keywords: known limits residual risk enterprise interpreter script hook failure codex gemini system settings wsl2 attestation managed admin api token audit records crash windows authenticode smartscreen wdac applocker roadmap
---

Straza leaves the risks below with you on an enterprise installation. Each one names who can reach it and what you do about it. A limit of one feature or one platform is on that feature's page, and the last section lists those pages.

## On the agent's machine


A managed install keeps the hook in files that only root or an administrator can change, so an agent without those rights cannot switch it off. This is what remains.

### A command rule does not read the script it starts


{{< limit who="the agent, where a rule allows an interpreter" status="no date" >}}
A rule that allows `python build.py` allows whatever the file does. The hook decides the command line and does not open the file.

{{< whattodo >}}
Deny interpreter calls for roles that do not need them, or send them to the [classifier]({{< relref "guides/write-policy/classify.md" >}}). To record a file that is written and then run, set `governance.sentinel.enabled` to true.
{{< /whattodo >}}
{{< /limit >}}

### Codex CLI and Gemini CLI run a call when the hook fails


{{< limit who="a fault in the hook process" status="no date" >}}
When the hook process cannot start, is killed or times out, Codex CLI and Gemini CLI run the tool call with no decision and no record. Claude Code from 2.1.295 blocks the call.

{{< whattodo >}}
Where a call must never run unchecked, require Claude Code in the rule:

```yaml
require:
  harness: ["claude-code>=2.1.295"]
```
{{< /whattodo >}}
{{< /limit >}}

### A person can start Gemini CLI without its system settings


{{< limit who="the person at that machine" status="no date" >}}
Gemini CLI reads its system settings from the path in `GEMINI_CLI_SYSTEM_SETTINGS_PATH`. Pointed at another file, it starts with no Straza hook, and the session still attests as `managed`.

{{< whattodo >}}
`straza doctor` names an active redirect. Where a call must never run unchecked, require Claude Code in the rule, as above.
{{< /whattodo >}}
{{< /limit >}}

### An agent inside WSL2 is not governed


{{< limit who="any person on a Windows machine with WSL2" status="planned" roadmap="planned" >}}
The Windows service does not reach into WSL2. An agent started there gets no decision and no Straza session, so it reaches nothing behind the gateway.

{{< whattodo >}}
Block WSL2 on governed machines through device management, or keep every credential an agent needs behind the gateway.
{{< /whattodo >}}
{{< /limit >}}

## Identity and tokens

### Attestation is the client's own report


{{< limit who="a person with a client they built themselves" status="January 2027" roadmap="january-2027" >}}
strazad compares the hashes a client reports with the ones it expects. It cannot measure the machine, so a client that a person built can report a managed install.

{{< whattodo >}}
Use `managed` to catch a changed or missing install. Put what must never happen behind the gateway or an approval, where the server decides.
{{< /whattodo >}}
{{< /limit >}}

### An admin API token outlives the person who made it


{{< limit who="whoever holds a copy of the token" status="no date" >}}
A token acts with its own scopes until it expires or is revoked. Disabling the person who made it does not stop it.

{{< whattodo >}}
Give each token narrow scopes and a short lifetime, from one day up. When a person leaves, revoke their tokens under Settings, API tokens.
{{< /whattodo >}}
{{< /limit >}}

## The record

### A crash can lose the newest audit records


{{< limit who="nobody at will: a crash, a killed pod or a database fault" status="no date" >}}
A decision runs once its record is queued in memory. If strazad is killed before the write, the queued records are lost and nothing counts them.

{{< whattodo >}}
Give strazad 30 seconds to stop, which the chart does. Send the audit stream to your SIEM, and alert on `straza_audit_lost_total` and `straza_outbox_records_lost_total`.
{{< /whattodo >}}
{{< /limit >}}

## The release

### The Windows binaries are not Authenticode-signed


{{< limit who="nobody: it changes how you admit the files" status="January 2027" roadmap="january-2027" >}}
SmartScreen may warn on first run, and WDAC, AppLocker or Smart App Control cannot admit Straza by its publisher.

{{< whattodo >}}
Verify the zip or the MSI with cosign and its checksum, as [Install]({{< relref "guides/operate/install.md#download-a-release-and-verify-it" >}}) shows, and admit the files by hash.
{{< /whattodo >}}
{{< /limit >}}

## Limits of one feature or one platform

These pages carry the limits that matter only when you use what they describe.

| Page | What it covers |
|---|---|
| [Git through Straza]({{< relref "guides/serve-mcp-apps/git-through-straza.md" >}}) | A force push looks like any other push, a rule names the tool and no branch, and only Claude Code is wired. |
| [Objects]({{< relref "concepts/objects.md" >}}) | What `covers: every` reaches, how fresh the object list is, and one object type for each server. |
| [Hookless processes]({{< relref "guides/govern-an-agent/hookless-processes.md" >}}) | What the sandbox image leaves open, and that a session cannot prove its image. |
| [Credentials and sessions]({{< relref "security/credentials-and-sessions.md" >}}) | How late a revocation can arrive, and the harness sandboxes on Windows and macOS that cannot reach the service. |
| [Claude Code]({{< relref "guides/govern-an-agent/claude-code.md" >}}) | How your own Claude Code managed settings combine with the file Straza writes. |
| [Deploy with Intune]({{< relref "guides/govern-an-agent/deploy-with-intune.md" >}}) and [Deploy with Jamf]({{< relref "guides/govern-an-agent/deploy-with-jamf.md" >}}) | The service switch in macOS System Settings. |
| [The security model in depth]({{< relref "security/security-model.md" >}}) | Manifest values in plain text, a remote server at an internal address and an MCP server outside the gateway. |
| [Requirements and sizing]({{< relref "reference/requirements-and-sizing.md" >}}) | Connections for each replica, memory and the size of the audit stream. |
| [The security model]({{< relref "evaluate/security-model.md" >}}) | What Straza does not try to do. |
