---
title: Claude Code
description: Claude Code on this machine hands every tool call to your Straza policy, and you have seen a destructive command denied with a reason.
pagetype: how-to
weight: 10
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: An Alpine 3.20 container against a standalone server, as the local user alice enrolled by the device flow, where the SessionStart, deny, allow and no-session payloads and the managed-attestation refusal were fired and the hook entries were read from the server's rendered managed config, while straza install, both managed steps and straza uninstall were not run because this box never runs straza install, and the demo stack's guardrails reason was read from its seed policy and the agent-sam log instead of fired, because the demo stack takes no writes
  date: 2026-10-06
applies_to: both
who: You, on the machine where Claude Code runs
where: A terminal on that machine
steps: true
keywords: claude code hooks enroll install harness
---


You wire Claude Code to Straza on the workstation where it runs. At the end, Claude Code hands every tool call to Straza before it runs it, and you have seen a destructive command denied with a reason.


Before Claude Code runs a tool, it passes the call to `straza hook`. The hook decides the call against a signed policy snapshot cached on the machine, with no network call in the common case. Its answer is an allow, or a deny with a reason that the model reads back.

## Before you start {.nostep}


- Claude Code on this machine.
- A role whose policy allows your local tools. The seeded `developer` role of the [demo stack]({{< relref "get-started/enterprise-demo-stack.md" >}}) is one. A standalone server needs no role, because its seeded `standalone-starter` PolicySet applies to every identity.

## Enroll this machine


Enroll the machine once, as [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) shows. The machine is ready for the next step when `straza status` names your identity and `straza doctor` warns only about the session, the snapshot and the wiring.

## Install the hook wiring


Run the installer as yourself, on the machine where Claude Code runs. It takes no server address. The hooks use the server that `straza enroll --server` saved for you.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza install claude-code
```
{{< /command >}}

{{< see >}}A line that starts `installed Straza hooks for claude-code into` and a line that starts `registered Straza MCP server for claude-code in`, each naming the file it changed.{{< /see >}}

The installer merges the Straza hook entries into `~/.claude/settings.json` and registers the `straza mcp` gateway server in `~/.claude.json`. Everything else in both files stays as it was. Restart Claude Code so it loads the new settings.


{{< details summary="What the installer writes" >}}
The wiring registers `straza hook --harness claude-code` on the seven Claude Code events Straza governs: SessionStart, PreToolUse, UserPromptSubmit, Stop, SessionEnd, SubagentStart and SubagentStop. Together they cover enforcement and conversation recording. A user-mode install points each entry at the `straza` binary you ran. The server renders the same entries for a managed install, where the binary sits in `/usr/local/bin`. Trimmed to two events, they read like this:

```json
{
  "hooks": {
    "PreToolUse": [
      { "hooks": [ { "command": "/usr/local/bin/straza hook --harness claude-code", "type": "command" } ], "matcher": "*" }
    ],
    "SessionStart": [
      { "hooks": [ { "command": "/usr/local/bin/straza hook --harness claude-code", "type": "command" } ] }
    ]
  }
}
```
{{< /details >}}


The command above is the user-mode install. A shared or locked-down machine takes the managed install instead. So does any machine whose server runs the enterprise profile, because an enterprise server refuses a user-mode session by default. Either install works against a standalone server and the demo stack.

{{< command terminal="Terminal" purpose="as an administrator" >}}
```sh
sudo straza install --managed --server https://straza.example.com claude-code
```
{{< /command >}}

`--server` names the server that the root-owned layout pins for every user of the machine, and the install refuses to run without it. Use your own address in place of the example. Each person still enrolls once as themselves, so every call is decided and audited as the person who made it.

| Install | Who can change the wiring | Sessions attest as |
|---|---|---|
| User mode | You | `advisory` |
| Managed | An administrator only, because the files are root-owned | `managed`, when the server holds the hashes of its files |

A user-mode hook is advisory, because an agent that can run any command can also turn the hook off. A managed install is root-owned, and any change to its files changes the hashes the server checks at every check-in. Policy can then deny anything less than `managed` to a sensitive role. [Attestation levels]({{< relref "guides/govern-an-agent/enroll-a-machine.md#attestation-levels" >}}) explains how the server sets the level.


{{< note title="A managed install takes over Claude Code's MCP servers" >}}
The managed install also registers `straza mcp` in Claude Code's `managed-mcp.json`, and that file is exclusive. Claude Code then loads only the MCP servers named there, so every server a user registered on their own stops loading. The installer says so in a line that ends `(exclusive: claude-code now loads ONLY servers in this file)`.
{{< /note >}}

## Open a session


Straza decides a tool call inside a session. Claude Code opens one with a SessionStart event each time it starts. To check the wiring without Claude Code, send that event to the hook by hand.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
printf %s '{"hook_event_name":"SessionStart"}' | straza hook --harness claude-code
```
{{< /command >}}

{{< see >}}Exit code 0, and a `systemMessage` with `Straza governance active:` followed by your identity, roles, server, policy snapshot and attestation.{{< /see >}}

The same answer carries the banner the model reads at the start of each session. [What the agent reads back]({{< relref "guides/govern-an-agent/what-the-agent-reads-back.md" >}}) quotes it in full.

{{< fails >}}
`attestation level "advisory" is below the required level "managed"`
: The server requires a managed install. Run `sudo straza install --managed --server <server-url> claude-code`, as the step before shows.
{{< /fails >}}

## Check a deny and an allow


Send the hook a `Bash` call of a destructive command, the way Claude Code sends it.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
printf %s '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /tmp/x"}}' | straza hook --harness claude-code
```
{{< /command >}}

{{< see >}}A deny with the policy's reason, and exit code 2. The decision goes to stdout for Claude Code, and the reason goes to stderr for you.{{< /see >}}

```text
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Straza: destructive command denied by the agent guardrails"}}
Straza: destructive command denied by the agent guardrails
```


That reason comes from the demo stack's guardrails policy. On a standalone server, the seeded `standalone-starter` PolicySet denies the same command for every identity, with the reason `Straza starter policy: recursive force-delete is denied. Delete files individually, or an admin can edit the standalone-starter PolicySet.`


Now send a safe command.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
printf %s '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status"}}' | straza hook --harness claude-code
```
{{< /command >}}

{{< see >}}An allow, and exit code 0. Claude Code runs the tool and shows the person nothing from Straza.{{< /see >}}

```text
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}
```

A standalone server allows it too, because its default allows a local tool that no rule matches. Both decisions land in the session's audit trail, and `straza status` now shows the live session, its roles and the snapshot.

{{< fails >}}
`Straza: no active Straza session. Restart the session so straza can check in`
: No session is open on this machine. Send the SessionStart event of the step before, or restart Claude Code.
{{< /fails >}}

## Remove the wiring {.nostep}


Run `straza uninstall claude-code` to remove the hook entries and the MCP registration. It takes back only what Straza wrote and leaves the rest of your settings untouched. A managed install comes off with `sudo straza uninstall --managed claude-code`.

## When a hook seems silent {.nostep}


Straza fails closed. When the snapshot is missing or the check-in fails, the hook denies with a reason and never lets the call through. If Claude Code still seems ungoverned, run `straza doctor` and the by-hand checks above. A Claude Code session that started before the install runs without the hooks until Claude Code reloads its settings, so restart it.


When the server cannot be reached, the hook keeps deciding for the offline grace and then denies, as [Working offline]({{< relref "guides/govern-an-agent/enroll-a-machine.md#working-offline" >}}) explains.

## Next {.nostep}


- [Write your first deny]({{< relref "guides/write-policy/first-deny.md" >}}) turns one dangerous command into a deny with your own reason.
- [What the agent reads back]({{< relref "guides/govern-an-agent/what-the-agent-reads-back.md" >}}) lists every answer the model can get from Straza.
- For work where an advisory hook is not enough, route the agent's tool calls through the gateway, as [Any MCP client]({{< relref "guides/govern-an-agent/any-mcp-client.md" >}}) shows, or run it in the sandbox image of [Hookless processes]({{< relref "guides/govern-an-agent/hookless-processes.md" >}}). [The trust model]({{< relref "concepts/trust-model-and-non-goals.md" >}}) explains why those two hold at a boundary.
