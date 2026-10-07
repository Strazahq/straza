---
title: Gemini CLI
description: Gemini CLI on this machine hands every tool call to your Straza policy, and you have seen a destructive command denied with a reason.
pagetype: how-to
weight: 30
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: An Alpine 3.20 container against a standalone server, as the local user alice enrolled by the device flow, where the SessionStart, BeforeTool deny, allow and no-session payloads and the managed-attestation refusal were fired and the four hook events were read from the server's rendered managed config, while straza install, the doctor's gemini-hooks line and straza uninstall were not run because this box never runs straza install and has no Gemini, and the demo stack's guardrails reason was read from its seed policy and the agent-sam log instead of fired, because the demo stack takes no writes
  date: 2026-10-06
applies_to: both
who: You, on the machine where Gemini CLI runs
where: A terminal on that machine
steps: true
keywords: gemini cli hooks enroll install harness
---


You wire Gemini CLI to Straza on the workstation where it runs. At the end, Gemini hands every tool call to Straza before it runs it, and you have seen a destructive command denied with a reason.


Before Gemini runs a tool, it passes the call to `straza hook`, which decides the call against a signed policy snapshot cached on the machine. Gemini names the events its own way, such as `BeforeTool` for a tool call. It reads every answer as a strict JSON document on stdout, either `{"decision":"allow"}` or `{"decision":"deny"}` with a reason. Gemini can also turn hooks off in two ways, so this page has one step more than the Claude Code page.

## Before you start {.nostep}


- Gemini CLI on this machine.
- A role whose policy allows your local tools. The seeded `developer` role of the [demo stack]({{< relref "get-started/enterprise-demo-stack.md" >}}) is one. A standalone server needs no role, because its seeded `standalone-starter` PolicySet applies to every identity.

## Enroll this machine


Enroll the machine once, as [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) shows. The machine is ready for the next step when `straza status` names your identity and `straza doctor` warns only about the session, the snapshot and the wiring.

## Install the hook wiring


Run the installer as yourself, on the machine where Gemini runs. It takes no server address. The hooks use the server that `straza enroll --server` saved for you.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza install gemini
```
{{< /command >}}

{{< see >}}A line that starts `installed Straza hooks for gemini into`, a line that starts `registered Straza MCP server for gemini in`, and a note on stderr about folder trust, which the next step answers.{{< /see >}}

The installer merges the Straza hook entries into `~/.gemini/settings.json` and registers the `straza mcp` gateway server in the same file. Everything else in it stays as it was. Restart Gemini so it loads the new settings.


{{< details summary="What the installer writes" >}}
The wiring registers `straza hook --harness gemini` on four Gemini events: `SessionStart`, `BeforeAgent`, `BeforeTool` with a `*` matcher, and `AfterAgent`. Together they cover enforcement and conversation recording.
{{< /details >}}


The command above is the user-mode install. A fleet machine takes the managed install instead. So does any machine whose server runs the enterprise profile, because an enterprise server refuses a user-mode session by default. Either install works against a standalone server and the demo stack.

{{< command terminal="Terminal" purpose="as an administrator" >}}
```sh
sudo straza install --managed --server https://straza.example.com gemini
```
{{< /command >}}

`--server` names the server that the root-owned layout pins for every user of the machine, and the install refuses to run without it. Use your own address in place of the example. Each person still enrolls once as themselves, so every call is decided and audited as the person who made it.

| Install | Who can change the wiring | Sessions attest as |
|---|---|---|
| User mode | You | `advisory` |
| Managed | An administrator only. The system settings pin `hooksConfig.enabled = true`, which Gemini ranks above a user's opt-out | `managed`, when the server holds the hashes of its files |

A user-mode hook is advisory, because an agent that can run any command can also turn the hook off. The managed install writes the root-owned layout that the server checks for tampering at every check-in. When Gemini's folder trust is on, a managed install still needs the folder trusted, as the next step shows. [Attestation levels]({{< relref "guides/govern-an-agent/enroll-a-machine.md#attestation-levels" >}}) explains how the server sets the level.

## Check that Gemini runs the hooks


Gemini runs no hooks at all when `hooksConfig.enabled` is `false`. When its folder trust is on, it also runs none in a folder it does not trust. The install warns about both in this note:

```text
NOTE: gemini refuses to start headless in an untrusted folder, and runs NO hooks anywhere if hooksConfig.enabled=false; `straza doctor` checks both (gemini-hooks). Trust the working folder inside gemini before relying on governance.
```

Run the doctor in the folder where you use Gemini, so it checks that folder's trust.

{{< command terminal="Terminal" purpose="in the folder where you use Gemini" >}}
```sh
straza doctor
```
{{< /command >}}

{{< see >}}`[ ok ]` on the `gemini-hooks` line.{{< /see >}}

{{< fails >}}
`has no trust record`
: Folder trust is on and this folder is not trusted yet. Open Gemini in this folder once and trust it in its dialog.

`is recorded UNTRUSTED in`
: Gemini records this folder as untrusted. Trust it in Gemini's dialog, or remove its `DO_NOT_TRUST` record from the file the line names.

`hooks are switched OFF`
: A settings file sets `hooksConfig.enabled = false`, and the line names that file. Remove the setting, or use the managed install, which pins it to `true`.

`GEMINI_CLI_SYSTEM_SETTINGS_PATH redirects gemini's system settings to`
: Gemini reads another system settings file than the managed one. Unset the variable, or point it at the managed file.
{{< /fails >}}

## Open a session


Straza decides a tool call inside a session. Gemini opens one with a SessionStart event each time it starts. To check the wiring without Gemini, send that event to the hook by hand.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
printf %s '{"hook_event_name":"SessionStart"}' | straza hook --harness gemini
```
{{< /command >}}

{{< see >}}`"decision":"allow"` and an `additionalContext` that begins `Straza governance is active for this session.` and names your identity, roles, server, policy snapshot and attestation.{{< /see >}}

That context is the banner the model reads at the start of each session. [What the agent reads back]({{< relref "guides/govern-an-agent/what-the-agent-reads-back.md" >}}) quotes it in full.

{{< fails >}}
`attestation level "advisory" is below the required level "managed"`
: The server requires a managed install. Run `sudo straza install --managed --server <server-url> gemini`, as the install step shows.
{{< /fails >}}

## Check a deny and an allow


Send the hook a shell call of a destructive command, the way Gemini sends it.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
printf %s '{"hook_event_name":"BeforeTool","tool_name":"run_shell_command","tool_input":{"command":"rm -rf /tmp/x"}}' | straza hook --harness gemini
```
{{< /command >}}

{{< see >}}A JSON document with `"decision":"deny"` and the policy's reason. Gemini acts on that field and ignores the exit code.{{< /see >}}

```text
{"decision":"deny","reason":"Straza: destructive command denied by the agent guardrails"}
```


That reason comes from the demo stack's guardrails policy. On a standalone server, the seeded `standalone-starter` PolicySet denies the same command for every identity, with the reason `Straza starter policy: recursive force-delete is denied. Delete files individually, or an admin can edit the standalone-starter PolicySet.`


Now send a safe command.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
printf %s '{"hook_event_name":"BeforeTool","tool_name":"run_shell_command","tool_input":{"command":"git status"}}' | straza hook --harness gemini
```
{{< /command >}}

{{< see >}}`{"decision":"allow"}`{{< /see >}}

A standalone server allows the command too, because its default allows a local tool that no rule matches. Both decisions land in the session's audit trail.

{{< fails >}}
`Straza: no active Straza session. Restart the session so straza can check in`
: No session is open on this machine. Send the SessionStart event of the step before, or restart Gemini.
{{< /fails >}}

## Remove the wiring {.nostep}


Run `straza uninstall gemini` to remove the hook entries and the MCP registration. It leaves the rest of your settings intact. A managed install comes off with `sudo straza uninstall --managed gemini`, which leaves the `hooksConfig.enabled = true` pin in place.

## When a hook seems silent {.nostep}


On Gemini the usual cause is folder trust or the hooks switch. Run `straza doctor` in the working folder and read its `gemini-hooks` line, as the step above shows. Straza fails closed, so a missing snapshot denies with a reason and never lets a call through.


When the server cannot be reached, the hook keeps deciding for the offline grace and then denies, as [Working offline]({{< relref "guides/govern-an-agent/enroll-a-machine.md#working-offline" >}}) explains.

## Next {.nostep}


- [Write your first deny]({{< relref "guides/write-policy/first-deny.md" >}}) turns one dangerous command into a deny with your own reason.
- [What the agent reads back]({{< relref "guides/govern-an-agent/what-the-agent-reads-back.md" >}}) lists every answer the model can get from Straza.
- For work where an advisory hook is not enough, route the agent's tool calls through the gateway, as [Any MCP client]({{< relref "guides/govern-an-agent/any-mcp-client.md" >}}) shows, or run it in the sandbox image of [Hookless processes]({{< relref "guides/govern-an-agent/hookless-processes.md" >}}). [The trust model]({{< relref "concepts/trust-model-and-non-goals.md" >}}) explains why those two hold at a boundary.
