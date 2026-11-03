---
title: Claude Code
description: Claude Code on this machine hands every tool call to your Straza policy, and you have seen a destructive command denied with a reason.
summary: >-
  Claude Code passes every tool call to `straza hook`, and the Straza background service
  decides it against a signed policy snapshot before the tool runs. A user-mode install is
  advisory, because an agent that can run any command can turn it off. An enterprise server
  requires the root-owned managed install by default.
pagetype: how-to
weight: 10
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: An Alpine 3.20 container against a standalone server, as the local user alice enrolled by the device flow, where the SessionStart, deny, allow and no-session payloads and the managed-attestation refusal were fired and the hook entries were read from the server's rendered managed config, while straza install, both managed steps and straza uninstall were not run because this box never runs straza install, and the demo stack's guardrails reason was read from its seed policy and the agent-sam log instead of fired, because the demo stack takes no writes
  date: 2026-10-06
  except: straza install, the managed install and straza uninstall, which were not run
applies_to: both
who: You, on the machine where Claude Code runs
where: A terminal on that machine
steps: true
keywords: claude code hooks enroll install harness
---


You wire Claude Code to Straza on the workstation where it runs. At the end, Claude Code hands every tool call to Straza before it runs it, and you have seen a destructive command denied with a reason.


Before Claude Code runs a tool, it passes the call to `straza hook`. The hook hands it to `straza service`, the Straza background service on the machine, which decides the call against a signed policy snapshot it holds, with no network call in the common case. Its answer is an allow, or a deny with a reason that the model reads back.

## Before you start {.nostep}


- Claude Code on this machine.
- A role whose policy allows your local tools. A fresh enterprise server holds no PolicySet and denies every local tool that no rule allows, so an admin first sets up the role and the PolicySet of [Give agents their local tools]({{< relref "guides/operate/enterprise-shape.md#give-agents-their-local-tools" >}}) and gives you its `developer` role. The seeded `developer` role of the [demo stack]({{< relref "evaluate/try-it-on-the-demo-stack.md" >}}) is such a role already. A standalone server needs no role, because its seeded `standalone-starter` PolicySet applies to every identity.

## Enroll this machine


Enroll the machine once, as [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) shows. The machine is ready for the next step when `straza status` names your identity and `straza doctor` warns only about the session and the wiring. Start Claude Code from a shell that exports the same `STRAZA_HOME` as a `straza service` you started yourself, because its hooks look for that service through it. On a machine with a system service or a managed install, the hooks ignore `STRAZA_HOME` and reach the system service. On an enterprise server the service also has to hold your key under its own OS account, as that page explains.

## Install the hook wiring


Run the installer as yourself, on the machine where Claude Code runs. It takes no server address. The hooks reach `straza service`, which keeps the server that `straza enroll --server` named.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza install claude-code
```
{{< /command >}}

{{< see >}}A line that starts `installed Straza hooks for claude-code into` and a line that starts `registered Straza MCP server for claude-code in`, each naming the file it changed.{{< /see >}}

The installer merges the Straza hook entries into `~/.claude/settings.json` and registers the `straza mcp` gateway server in `~/.claude.json`. On Linux and macOS it also sets three git entries in the `env` table of the settings file, `GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_0` and `GIT_CONFIG_VALUE_0`, which point git in the processes Claude Code starts at the host file of `straza service`, and the managed install below sets the same entries in its managed settings file. On Windows only the managed install sets them, because git there needs the MSI's `git-remote-straza.exe`, and a git folder named before the service's own state folder exists could be created by any local user. The host file of `straza service` is `/run/straza-service/git/hosts.conf` on Linux, `/opt/straza/run/git/hosts.conf` on macOS and `C:\ProgramData\straza-service\git\hosts.conf` on Windows, the path that `GIT_CONFIG_VALUE_0` names. Everything else in both files stays as it was. [Git through Straza]({{< relref "guides/serve-mcp-apps/git-through-straza.md#how-git-reaches-straza" >}}) says what the git entries do. Restart Claude Code so it loads the new settings.


{{< details summary="What the installer writes" >}}
The wiring registers `straza hook --harness claude-code` on the seven Claude Code events Straza governs: SessionStart, PreToolUse, UserPromptSubmit, Stop, SessionEnd, SubagentStart and SubagentStop. Together they cover enforcement and conversation recording. A user-mode install points each entry at the `straza` binary you ran. The server renders the same entries for a managed install, where the binary is the `straza` package's `/usr/bin/straza` on Linux and the Straza package's `/opt/straza/bin/straza` on macOS. On Linux, trimmed to two events, they read like this:

```json
{
  "hooks": {
    "PreToolUse": [
      { "hooks": [ { "command": "/usr/bin/straza hook --harness claude-code", "type": "command" } ], "matcher": "*" }
    ],
    "SessionStart": [
      { "hooks": [ { "command": "/usr/bin/straza hook --harness claude-code", "type": "command" } ] }
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

`--server` names the server that the root-owned layout pins for every user of the machine, and the install refuses to run without it. Use your own address in place of the example. Each person still enrolls once as themselves, so every call is decided and audited as the person who made it. The background service reads that server from the managed layout when a person enrolls, so after an administrator points the layout at another server, each person runs `straza enroll` again, as [Move a machine to another server]({{< relref "guides/govern-an-agent/enroll-a-machine.md#move-a-machine-to-another-server" >}}) shows. It reads the snapshot keys the layout pins each time it fetches a new policy and when it starts.

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

A standalone server allows it too, because its default allows a local tool that no rule matches. Both decisions land in the session's audit trail, and `straza status` now shows the live session.

{{< fails >}}
`Straza: no active Straza session. Restart the session so straza can check in`
: No session is open on this machine. Send the SessionStart event of the step before, or restart Claude Code.
{{< /fails >}}

## Remove the wiring {.nostep}


Run `straza uninstall claude-code` to remove the hook entries and the MCP registration. It takes back only what Straza wrote and leaves the rest of your settings untouched. A managed install comes off with `sudo straza uninstall --managed claude-code`.

## When a hook seems silent {.nostep}


Straza fails closed. When the background service does not answer, the snapshot is missing or the check-in fails, the hook denies with a reason and never lets the call through. If Claude Code still seems ungoverned, run `straza doctor` and the by-hand checks above. A Claude Code session that started before the install runs without the hooks until Claude Code reloads its settings, so restart it.


When the server cannot be reached, the service keeps deciding for the offline grace and then denies, as [Working offline]({{< relref "guides/govern-an-agent/enroll-a-machine.md#working-offline" >}}) explains.

## When Claude Code adds a tool {.nostep}


When a Claude Code release adds a tool that this Straza client does not know yet, the hook sends the tool as `other` with its own name. An enterprise server denies it with a reason that names the tool and the rule that allows it, and a standalone server allows it by default. An admin allows, holds or denies exactly that tool with a rule of `tools: [other]` and `toolNames: {allow: [<name>]}`, with no Straza release, as [Harness tools by name]({{< relref "reference/policyset-grammar.md#harness-tools-by-name" >}}) shows. A later Straza release puts the tool into its kind, and the rule keeps governing it.

## Next {.nostep}


- [Write your first deny]({{< relref "guides/write-policy/first-deny.md" >}}) turns one dangerous command into a deny with your own reason.
- [What the agent reads back]({{< relref "guides/govern-an-agent/what-the-agent-reads-back.md" >}}) lists every answer the model can get from Straza.
- For work where an advisory hook is not enough, route the agent's tool calls through the gateway, as [Any MCP client]({{< relref "guides/govern-an-agent/any-mcp-client.md" >}}) shows, or run it in the sandbox image of [Hookless processes]({{< relref "guides/govern-an-agent/hookless-processes.md" >}}). [The trust model]({{< relref "evaluate/security-model.md" >}}) explains why those two hold at a boundary.
