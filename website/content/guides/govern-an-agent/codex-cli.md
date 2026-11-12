---
title: Codex CLI
description: Codex CLI on this machine hands every tool call to your Straza policy, and you have seen a destructive command denied with a reason.
summary: >-
  Codex CLI passes every tool call to `straza hook`, and the Straza background service decides
  it against a signed policy snapshot before the tool runs. Codex runs user-mode hooks only
  after you trust them in `/hooks`. The managed install needs no trust step, a user cannot
  switch it off, and an enterprise server requires it by default.
pagetype: how-to
weight: 20
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: An Alpine 3.20 container against a standalone server, as the local user alice enrolled by the device flow, where the SessionStart, deny, allow and no-session payloads and the managed-attestation refusal were fired, while straza install, the trust step in Codex, the doctor's codex-hooks line and straza uninstall were not run because this box never runs straza install and has no Codex, and the demo stack's guardrails reason was read from its seed policy and the agent-sam log instead of fired, because the demo stack takes no writes
  date: 2026-10-06
  except: straza install, the trust step in Codex, the doctor's codex-hooks line and straza uninstall, which were not run
applies_to: both
who: The person at the machine where Codex CLI runs
where: Terminal on that machine
steps: true
keywords: codex cli hooks enroll install harness
---


You wire Codex CLI to Straza on the workstation where it runs. At the end, Codex hands every tool call to Straza before it runs it, and you have seen a destructive command denied with a reason.


Codex uses the same hook contract as Claude Code. Before Codex runs a tool, it passes the call to `straza hook`, which hands it to `straza service`, the Straza background service on the machine, to decide against a signed policy snapshot the service holds. Its answer is an allow, or a deny with a reason that the model reads back. Codex also skips a hook until you trust its exact definition, so this page has one step more than the Claude Code page.

## Before you start {.nostep}


- Codex CLI on this machine.
- A role whose policy allows your local tools. A fresh enterprise server holds no PolicySet and denies every local tool that no rule allows, so an admin first sets up the role and the PolicySet of [Give agents their local tools]({{< relref "guides/operate/enterprise-shape.md#give-agents-their-local-tools" >}}) and gives you its `developer` role. The seeded `developer` role of the [demo stack]({{< relref "evaluate/try-it-on-the-demo-stack.md" >}}) is such a role already. A standalone server needs no role, because its seeded `standalone-starter` PolicySet applies to every identity.

## Enroll this machine


Enroll the machine once, as [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) shows. The machine is ready for the next step when `straza status` names your identity and `straza doctor` warns only about the session and the wiring. Start Codex from a shell that exports the same `STRAZA_HOME` as a `straza service` you started yourself, because its hooks look for that service through it. On a machine with a system service or a managed install, the hooks ignore `STRAZA_HOME` and reach the system service. On an enterprise server the service also has to hold your key under its own OS account, as that page explains.

## Install the hook wiring


Run the installer as yourself, on the machine where Codex runs. It takes no server address. The hooks reach `straza service`, which keeps the server that `straza enroll --server` named.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza install codex
```
{{< /command >}}

{{< see >}}A line that starts `installed Straza hooks for codex into`, a line that starts `registered Straza MCP server for codex in`, and a notice that starts `ACTION REQUIRED`, which the next step answers.{{< /see >}}

The installer writes the Straza hooks to `$CODEX_HOME/hooks.json`, the file Codex reads its hooks from. It also registers the `straza mcp` gateway server in a block of its own in `$CODEX_HOME/config.toml`. Without `CODEX_HOME`, both files sit in `~/.codex`.


{{< details summary="What the installer writes" >}}
The wiring registers `straza hook --harness codex` on the seven Codex events Straza governs, under Codex's own names: SessionStart, PreToolUse, UserPromptSubmit, Stop, SessionEnd, SubagentStart and SubagentStop. Together they cover enforcement and conversation recording. These user-scope entries carry no matcher, because Codex matchers are regular expressions and an absent matcher matches every tool. The session-end entry carries a three-second timeout for the audit drain.
{{< /details >}}


The command above is the user-mode install. A fleet machine takes the managed install instead. So does any machine whose server runs the enterprise profile, because an enterprise server refuses a user-mode session by default. Either install works against a standalone server and the demo stack.

{{< command terminal="Terminal" purpose="as an administrator" >}}
```sh
sudo straza install --managed --server https://straza.example.com codex
```
{{< /command >}}

`--server` names the server that the root-owned layout pins for every user of the machine, and the install refuses to run without it. Use your own address in place of the example. Each person still enrolls once as themselves, so every call is decided and audited as the person who made it. The background service reads that server from the managed layout when a person enrolls, so after an administrator points the layout at another server, each person runs `straza enroll` again, as [Move a machine to another server]({{< relref "guides/govern-an-agent/enroll-a-machine.md#move-a-machine-to-another-server" >}}) shows. It reads the snapshot keys the layout pins each time it fetches a new policy and when it starts.

| Install | Who can change the wiring | Sessions attest as |
|---|---|---|
| User mode | You, and Codex runs it only after you trust it | `advisory` |
| Managed | An administrator only. Codex trusts these hooks with no `/hooks` step, and a user cannot switch them off | `managed`, when the server holds the hashes of its files |

A user-scope Codex hook is advisory, because its user can always switch it off. The managed install writes `/etc/codex/requirements.toml`, or its Windows equivalent. It also pins `[features] hooks = true`, so a user's own `hooks = false` cannot disable the hooks. [Attestation levels]({{< relref "guides/govern-an-agent/enroll-a-machine.md#attestation-levels" >}}) explains how the server sets the level.

## Trust the hooks in Codex


Codex skips a hook from a user-scope file until you review and trust its exact definition. Open Codex, run `/hooks`, and trust the Straza entries. Codex keys that trust to a hash of the definition, so a later re-install asks for it again. Writing `hooks.json` is not enough, which is why every user-mode install ends with this notice:

```text
ACTION REQUIRED (codex will NOT run these hooks until you trust them):
  open codex and run /hooks to review + trust the Straza hooks
Codex skips hooks from a non-managed source until an operator trusts the exact
definition (trust is keyed to its hash, so a later re-install re-gates it).
Until you do that, the codex hook lane is NOT enforcing; codex governance is
the MCP gateway lane only. Nothing outside codex can verify the trust state;
once a governed codex session starts, it shows up in `straza status`.
```


After you trust the hooks, check what Straza can see of the trust state. The doctor reads the trust records Codex keeps in `$CODEX_HOME/config.toml` when it can.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza doctor
```
{{< /command >}}

{{< see >}}`[ ok ]` on the `codex-hooks` line, which says that Codex records the Straza hooks as trusted.{{< /see >}}

{{< fails >}}
`codex has no trust record for the Straza hooks`
: Codex skips them. Open Codex, run `/hooks` and trust the Straza entries.

`but hooks are switched OFF in`
: Your Codex config sets `[features] hooks = false`. Remove that setting, or use the managed install, which pins it to `true`.

`trust cannot be verified from outside`
: Codex keeps no trust record that Straza can read. Trust the hooks in `/hooks` and start Codex. Once its session checks in, `straza status` shows it.
{{< /fails >}}

## Open a session


Straza decides a tool call inside a session. Codex opens one with a SessionStart event each time it starts. To check the wiring without Codex, send that event to the hook by hand.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
printf %s '{"hook_event_name":"SessionStart"}' | straza hook --harness codex
```
{{< /command >}}

{{< see >}}Exit code 0, and a `systemMessage` with `Straza governance active:` followed by your identity, roles, server, policy snapshot and attestation.{{< /see >}}

The same answer carries the banner the model reads at the start of each session. [What the agent reads back]({{< relref "guides/govern-an-agent/what-the-agent-reads-back.md" >}}) quotes it in full.

{{< fails >}}
`attestation level "advisory" is below the required level "managed"`
: The server requires a managed install. Run `sudo straza install --managed --server <server-url> codex`, as the install step shows.
{{< /fails >}}

## Check a deny and an allow


Send the hook a `Bash` call of a destructive command, the way Codex sends it.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
printf %s '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /tmp/x"}}' | straza hook --harness codex
```
{{< /command >}}

{{< see >}}The policy's reason on stderr, and exit code 2. Codex reads only stderr on a block and ignores the JSON that goes to stdout first.{{< /see >}}

```text
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Straza: destructive command denied by the agent guardrails"}}
Straza: destructive command denied by the agent guardrails
```


That reason comes from the demo stack's guardrails policy. On a standalone server, the seeded `standalone-starter` PolicySet denies the same command for every identity, with the reason `Straza starter policy: this sample rule denies commands that start with rm -rf. Delete files individually, or an admin can edit the standalone-starter PolicySet.`


Now send a safe command.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
printf %s '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status"}}' | straza hook --harness codex
```
{{< /command >}}

{{< see >}}No output, and exit code 0. That silence is the allow.{{< /see >}}

Codex checks hook output strictly for each event, so the only valid allow is no output and exit code 0. A standalone server allows the command too, because its default allows a local tool that no rule matches. Both decisions land in the session's audit trail.

{{< fails >}}
`Straza: no active Straza session. Restart the session so straza can check in`
: No session is open on this machine. Send the SessionStart event of the step before, or restart Codex.
{{< /fails >}}

## Remove the wiring {.nostep}


Run `straza uninstall codex` to remove the hook entries and the Straza block in `config.toml`. An MCP registration you wrote yourself stays. A managed install comes off with `sudo straza uninstall --managed codex`.

## When a hook seems silent {.nostep}


The most common cause on Codex is an untrusted hook. Open `/hooks` and confirm that the Straza entries are trusted. The doctor reports the trust state where it can read it, and it never calls the hooks enforcing because the file exists. Straza fails closed, so a missing snapshot or a background service that does not answer denies with a reason and never lets a call through.


When the server cannot be reached, the service keeps deciding for the offline grace and then denies, as [Working offline]({{< relref "guides/govern-an-agent/enroll-a-machine.md#working-offline" >}}) explains.

## When Codex adds a tool {.nostep}


When a Codex release adds a tool that this Straza client does not know yet, the hook sends the tool as `other` with its own name and its input. Its audit record keeps that input as sent in `arguments`, as every tool's record does, so it can hold a secret the tool takes. An enterprise server denies it with a reason that names the tool and the rule that allows it, and a standalone server allows it by default. An admin allows, holds or denies exactly that tool with a rule of `tools: [other]` and `toolNames: {allow: [<name>]}`, with no Straza release, as [Harness tools by name]({{< relref "reference/policyset-grammar.md#harness-tools-by-name" >}}) shows. A later Straza release puts the tool into its kind, and the rule keeps governing it.

{{< details summary="Write the rule in the console" >}}

In Policies, the **Denied by default** view lists the tool when a call to it was denied in the last 24 hours. **Write a rule** on its row opens **New policy** with **Harness tool** and the tool's name answered under **Which calls**, and the wizard writes the same rule, as [Policies]({{< relref "guides/console/policies.md#read-the-policy-list" >}}) shows.
{{< /details >}}

## Next {.nostep}


- [Write your first deny]({{< relref "guides/write-policy/first-deny.md" >}}) turns one dangerous command into a deny with your own reason.
- [What the agent reads back]({{< relref "guides/govern-an-agent/what-the-agent-reads-back.md" >}}) lists every answer the model can get from Straza.
- For work where an advisory hook is not enough, route the agent's tool calls through the gateway, as [Any MCP client]({{< relref "guides/govern-an-agent/any-mcp-client.md" >}}) shows, or run it in the sandbox image of [Hookless processes]({{< relref "guides/govern-an-agent/hookless-processes.md" >}}). [The trust model]({{< relref "evaluate/security-model.md" >}}) explains why those two hold at a boundary.
