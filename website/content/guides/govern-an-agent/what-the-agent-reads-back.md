---
title: What the agent reads back
description: The exact words an agent reads from Straza, from the session banner to a deny, a pending approval, a ticket and the built-in approval tools.
summary: >-
  An agent reads Straza's words in two places: a banner when its session starts, and an answer
  on each call that does not run at once. A deny carries the rule's own reason, and a held call
  names who decides and where. Straza fails closed, so a call it cannot decide is denied with
  what to fix.
pagetype: reference
weight: 80
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: An Alpine Linux container against a standalone server, as alice holding the application role dev, with hook payloads piped into straza hook, gateway calls sent through straza mcp, and an admin deciding with strazactl, which no longer decides, so a person now decides in the console, on the self-service page or on a phone. approval.gatewayHoldSeconds was 3, so a held gateway call answered pending after three seconds
  date: 2026-10-06
applies_to: both
keywords: deny reason banner pending approval ticket approval_await approval_request approval_status agent model
---


An agent under Straza reads Straza's words in two places: a banner when its session starts, and an answer on each call that does not run at once. This page quotes each one as the product writes it, so you can write the instructions your agent follows and recognise every answer in a transcript. Angle brackets mark the parts that change, such as `<ref>` for the id of an approval request.

## The session banner


When a session starts, the session-start hook checks in with the server and hands the harness a banner as context for the model. Claude Code and Codex read it from `hookSpecificOutput.additionalContext`, and Gemini reads it from a top-level `additionalContext`. This is the banner alice's agent read on a standalone server:

```text
Straza governance is active for this session. You are operating as "alice" (roles: dev) against http://127.0.0.1:8420; policy snapshot cde5c229f4fa, attestation advisory. Tool use is checked locally against signed policy and every decision is audited. A denied tool call always carries its reason: relay it to the user and do not retry or work around the denial.
```

Its last sentence tells the agent how to treat every deny on this page. The agent passes the reason on to its user and does not try again, unless the reason asks for a corrected call, as the object checks below do for a malformed argument.


When a policy records the session's conversations, the banner gains one sentence. With secrets masked it reads as below, and with recording word for word it ends `recorded word for word to the audit system.`

```text
Recording is on: by policy, the prompts and responses of this session are recorded with secrets masked to the audit system.
```


Knowledge packs bound to the session's roles follow the banner, as [Knowledge packs]({{< relref "guides/govern-an-agent/knowledge-packs.md" >}}) shows. For Claude Code and Codex the hook also returns `systemMessage`, one line for the person at the keyboard. It starts with a shield symbol and `Straza governance active:`, then names the user, the roles, the server, the policy and the attestation. Gemini gets the context only.


When the session cannot start, the session-start hook exits with code 2 and a reason that starts `Straza:`, followed by the sentence that says why. That is the server's own refusal, or the sentence of the Straza background service when it does not run or holds no enrollment for the user. Until a session starts, every tool call is denied with `Straza: no active Straza session. Restart the session so straza can check in`. [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) shows the refusal a server gives a machine below its attestation minimum.

## How each harness receives a deny


A hook answers in its harness's own format. The gateway answers every client in the same MCP format.

| Where the call ran | What the harness receives |
|---|---|
| Claude Code hook | `"permissionDecision":"deny"` with the reason in `permissionDecisionReason` on stdout, the reason again on stderr, and exit code 2. |
| Codex hook | The reason on stderr and exit code 2. Codex reads stderr on a block and ignores stdout. |
| Gemini hook | `{"decision":"deny","reason":"<reason>"}` on stdout. Gemini ignores the exit code. |
| The gateway | A tool result with `"isError":true` and the reason as its text. |

This is a deny as Claude Code receives it, from a rule that has no reason of its own:

```text
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Straza: blocked by policy rule dev-rules/no-force-push"}}
Straza: blocked by policy rule dev-rules/no-force-push
```

## The reason in a deny


A rule's own `reason` is passed on word for word, so the clearest denies come from rules that say what to do instead. Each case below names the reason the agent reads.

Deny rule with a `reason`
: The rule's reason. The standalone starter policy's reads `Straza starter policy: recursive force-delete is denied. Delete files individually, or an admin can edit the standalone-starter PolicySet.`

Deny rule without a `reason`
: `Straza: blocked by policy rule <set>/<rule>`

MCP tool that no role of the session reaches, seen by a hook
: `Straza: no role of yours has access to MCP tool <server>/<tool>. Ask an admin to give a role you hold access to it, or to allow it by policy.`

Local tool such as `shell.exec` that no rule allows, on an enterprise server
: `Straza: <tool> is not permitted by default in this profile. Ask an admin for a policy rule that allows it.`

Harness tool that Straza does not classify, such as one a harness release added, that no rule names, on an enterprise server
: `Straza: <name> is a harness tool Straza does not classify, so this profile denies it. To allow it, an admin adds a policy rule with tools: [other] and toolNames: {allow: [<name>]}.`

The same tool from a client too old to send its name
: `Straza: this harness tool is one Straza does not classify, and the client sent no tool name, so no rule can allow it. Update the Straza client or the agent kit on this machine, then ask an admin to allow the tool by name.`

Classifier that says no, under a rule with `mode: classify`
: `Straza: classifier: <the classifier's reason>`

Confirmation rule met by an autonomous AI agent
: `Straza: rule <set>/<rule> requires the requester's confirmation; an autonomous session has no human to confirm`

Gateway call for a tool the session cannot see
: The JSON-RPC error `unknown tool "<name>"`, the same answer as for a tool that does not exist. A name over 256 bytes is quoted as its first 256 bytes followed by `... (<n> bytes)`, its full length.

Rate limit for that server at the gateway, used up
: `Straza: rate limit exceeded for the MCP server "<server>" (<n> rps). Retry shortly`

Gateway call whose request body is larger than `server.mcpMaxBodyBytes`, 4 MiB by default
: The JSON-RPC error -32600 `Straza: this MCP request was not read, because its body is larger than <limit> bytes, the most the Straza gateway reads in one request (server.mcpMaxBodyBytes). Send less in one call, for example a file's path or address instead of its content, or ask a Straza administrator to raise server.mcpMaxBodyBytes.` The Straza server never read the call, so it decided nothing and wrote no audit record.


The gateway leaves a tool that policy denies out of the session's tool list, so a call to it gets `unknown tool`. With `apps.catalog.policyFilter` set to false, the tool stays listed and the call gets the policy's reason instead.

## A call on a server that limits roles to some objects


A server can pin an object type in its manifest, such as ghapp with its repos, and each role on it then covers every repo or a named list. The gateway checks each call to such a server against every role of the session that binds the tool, after the rate limit and before any policy runs, so no rule can allow a call this check refuses. A session's tool list still shows every tool a held role binds, so an agent learns which repos its roles cover from the refusal. Like a policy deny, the refusal is a tool error. A hook never runs this check.

Object that none of the session's roles covers
: `Straza: <tool> on <value> was refused: none of your roles on <server> covers that <type>. Your roles cover <tool> on <v1>, <v2> and <n> more. Check the <type>'s name, or ask an admin for a role that covers it.` The middle sentence names up to ten objects the session's roles name, and it is left out when they name none.

Tool that can reach every object, with no role on every object
: `Straza: <tool> on <server> can reach every <type>, so only a role on every <type> may call it, and none of your roles on <server> is one. Ask for a role on every <type>, or use a tool that names the <type>.` A tool marked `credential`, and a tool the pinned block leaves out, read the same.

Call without the argument that names the object
: `Straza: <tool> needs the <type> it works on in its argument <argument>, and this call carries none. Send <argument> as a string, then try again.` When the argument is there but not a string, the sentence reads `Straza: <tool> needs the <type> in its argument <argument> as a string, and this call sends another type. Send <argument> as a string, then try again.`

Argument sent twice under two spellings, such as `repo` and `Repo`
: `Straza: <tool>'s arguments carry both <argument> and <other>, and <server> might read either. Send <argument> once, as its input schema spells it, then try again.`

Arguments that two readers could read apart
: `Straza: <tool>'s arguments could not be read the same way by Straza and by <server>: <fault>. Send each argument once, in one JSON object, then try again.` The fault is one of `a key appears twice`, `they are not valid UTF-8`, `they are not a JSON object`, `they nest deeper than 128 levels`, `there is data after the arguments object` and `they are not valid JSON`.

Tool whose mark or input schema changed since an admin pinned it
: `Straza: <tool> on <server> changed how it names its <type> since an admin pinned it, so roles on some <plural> cannot use it until an admin publishes the change. Ask an admin to review <server>'s <Plural> tab.`

Object checks turned off by `apps.objects.enabled`
: `Straza: Straza's <type> checks are off on this deployment, so only a role on every <type> may call <tool> on <server>. Ask an admin to turn apps.objects.enabled back on, or for a role on every <type>.`


The argument sentences come before any role is read, so a session with a role on every repo meets them too. The agent fixes the argument and sends the call again, as each of those sentences says. For an object no role covers, the agent relays the sentence to the person and stops, and it does not try the call on other repos. That sentence offers no retry. It asks the agent to check the repo's name or to ask for a role that covers it. A value the list does not hold still makes Straza list the server again, at most once a minute, so a call on a renamed repo passes after about a minute. Every other sentence here is for the person too: the agent relays it and stops, as the banner says.

## When Straza cannot decide


Straza fails closed. When a hook cannot prove its decision, it denies, and the reason says what is wrong and what to do.

No background service on the machine
: ``Straza: Straza's background service isn't running on this machine, so this command can't reach your Straza session. Run `straza doctor` to see why, or ask IT to reinstall Straza. On Linux, `systemctl status straza-service` shows why.`` On Windows the last sentence reads ``On Windows, `Get-Service straza-service` shows whether it runs, and the Application event log under straza-service shows why it stopped.`` A service started with `STRAZA_HOME` set is named by that path instead, with the hint to start it with `straza service` in a shell that has the same `STRAZA_HOME`.

No enrollment for the user in the service
: ``Straza: This machine isn't enrolled for you yet. Run `straza enroll` and sign in with your work account.``

No session on the machine
: `Straza: no active Straza session. Restart the session so straza can check in`

Session the server revoked over the kill-switch push
: `Straza: session revoked (<reason>). Tool calls stay denied until a new session starts and checks in again. If only this session was revoked, that check-in starts a new session. If the device or user was disabled, the check-in is refused until an administrator re-enables it. Inform the user and stop.`

Token expired past the offline grace
: `Straza: session token expired <age> ago and the offline grace period (<grace>) is exhausted.`, then a sentence that says Straza cannot verify current policy and asks the agent to reconnect and run `straza doctor`

Renewal refused by the server
: `Straza: session revoked (<the server's sentence>).`, then the same sentences as a revocation push gives, because a refused renewal marks the session revoked

Newer policy on the server that the machine could not fetch
: `Straza: this session's policy is out of date: the server holds a newer policy that straza could not fetch.`, then the cause of the failed fetch and a sentence that says tool calls stay denied until straza can fetch the current policy

Approval needed while the server cannot be reached
: `Straza: security layer unreachable for an approval-gated action. Denied (fail-closed)`

Database out of reach while the server looks up an approval
: `Straza: this call needs an approval, and the Straza server could not reach its database to check for one, so the call did not run. Denied (fail-closed). Retry the call in a moment, and if this keeps happening ask your Straza administrator to check the server's database connection.`

Nobody who can be asked for the approval
: `Straza: approval cannot be routed: <cause>. Denied (fail-closed)`

[Working offline]({{< relref "guides/govern-an-agent/enroll-a-machine.md#working-offline" >}}) explains the grace and its defaults.

## A call held for a person


A rule with `mode: approve` holds a call until a person decides. The hook and the gateway hold it in different ways, and the agent reads different answers.

{{< diagram name="approval-hold" caption="On the gateway a held call stays open while a person decides. In a hook the call is denied at once with a reference, and the same call passes once after approval." >}}

### In a hook


The hook never waits. It denies the call at once, names who was told and where a person decides, and gives the agent a reference:

```text
Straza: approval requested (notified roles [release-approvers]); a person decides on the self-service page (http://127.0.0.1:8420/self-service/), the console, or an enrolled phone. Wait for the decision with the straza__approval_await tool, then retry (ref 01a112cf-d923-7402-af47-66128f1954a3)
```

The page address comes from the server's `server.publicUrl`. The last sentence names `straza__approval_await` only when the session's policy allows that tool. Otherwise it reads `Retry this exact call after approval (ref <ref>)`.


After a person approves, the same call made again is allowed once, within the rule's `retryTTLSeconds`, and the harness receives a plain allow. A second retry opens a new request, because one approval runs one call. A denial is never reported by the hook, so the next retry opens a new request and reads the same answer with a new reference. The agent learns of a denial from `straza__approval_status` or `straza__approval_await`, which report `"state":"denied"`.

A rule that names no approver role sends the request to the person behind the agent, its sponsor. A person with no sponsor confirms their own call, and the answer reads:

```text
Straza: this call needs the requester's confirmation, on their enrolled phone, or the self-service page under This browser (http://127.0.0.1:8420/self-service/); wait for it with the straza__approval_await tool, then retry (ref 01a112cf-d957-713d-a2ab-3be6a3426a6e)
```

With `approval.unsignedOwnDecisions` on, the places named are the same as for a request to an approver.

### On the gateway


The gateway keeps the call open while a person decides, until the rule's decision window ends or `approval.gatewayHoldSeconds` passes, 120 seconds by default. An approval in that time runs the tool, and its result comes back like any other. Every other ending is a tool error.

Denied by a person
: `Straza: approval denied by <name> (ref <ref>)`

Window over with no decision
: `Straza: approval request expired after <n>s with no decision (ref <ref>)`

Hold over first, while the window is still open
: `Straza: approval pending (ref <ref>), <n>s left in the decision window; a person decides on <where>. Wait for the decision with the straza__approval_await tool, then retry the call. An approval within the window lets the retry proceed.`

Approval already used for this call
: `Straza: approval <ref> allows one run of this call, and that run already happened or its time ran out, so this call did not run. Send the call again to ask for a new approval.`

The pending answer of a call held three seconds read:

```text
Straza: approval pending (ref 01a112c9-5dc5-7795-a0fc-944f3d0873ba), 297s left in the decision window; a person decides on the self-service page (http://127.0.0.1:8420/self-service/), the console, or an enrolled phone. Wait for the decision with the straza__approval_await tool, then retry the call. An approval within the window lets the retry proceed.
```

The await sentence appears only when the session's policy allows `straza__approval_await` and the endpoint lists it. `straza mcp <server>`, which serves one server's own endpoint, does not list it, so there the answer says to retry after approval. A retry of the same call within the window runs once after the approval.

## A ticket


A ticket rule, `class: ticket`, never holds a call. The hook and the gateway both answer at once, and the window is the rule's `ticketTTLSeconds` in words:

```text
Straza: approval ticket 01a112cf-d975-7948-b32d-d685093bcab1 is pending. A person decides within 24 hours, on the self-service page (http://127.0.0.1:8420/self-service/), the console, or an enrolled phone. Wait for the decision with the straza__approval_await tool (ref 01a112cf-d975-7948-b32d-d685093bcab1), or retry this exact call after approval; it fails if denied or expired.
```

After an approval, the next call that matches the ticket exactly runs once, from any session of the same person, within the rule's `grantTTLSeconds`. After a denial, the same call stays denied until the ticket's window closes:

```text
Straza: ticket 01a112c8-cd1f-7512-a37e-66adb0e987b6 was denied by admin; this call stays denied until 2026-10-07T19:55:12Z
```

Once that window has closed, or a ticket expired with no decision, the next call opens a new ticket.

## The built-in approval tools


The gateway serves three tools of its own, under the server name `straza`, which an agent sees as `straza__approval_request`, `straza__approval_status` and `straza__approval_await`. None of them decides a request, because deciding stays with a person. An agent reaches them through the gateway, with `straza mcp` or at `/mcp`, and only when a policy rule allows them, since no access row reaches them. This rule allows all three:

```yaml
- id: straza-approval-tools
  events: [tool.pre]
  tools: [mcp.call]
  apps: [straza]
  toolNames:
    allow: ["approval_request", "approval_status", "approval_await"]
  effect: allow
```

| Tool | Arguments | What it answers |
|---|---|---|
| `straza__approval_request` | `action`, the concrete call: `tool` with `app`, `tool_name` and the exact `args` for an MCP call, or `tool` with `command` or `argv` for a command. `reason`, optional words for the approver. | It opens a ticket for that call, or joins the one already pending, and answers `ref`, `state` and `expires_at`. Only a ticket rule accepts it. |
| `straza__approval_status` | `ref` | The request's `state`, `class` and `expires_at`, and once they are set `decided_by`, `grant_expires_at`, `consumed_at` and `consumed_by`. It changes nothing. |
| `straza__approval_await` | `ref`, and `max_wait_seconds` from 1 to 60, 30 by default | The same fields and `timed_out`, after waiting up to that bound for a decision. A timeout is not a deny, and the request stays open. |


Both readers take the reference of a hold as well as a ticket. This is `straza__approval_status` on a ticket that was approved and then used, where `consumed_by` names the session that ran the call:

```text
{"class":"ticket","consumed_at":"2026-10-06T19:55:04Z","consumed_by":"01a112c8-36bd-72b7-9cee-6ec95f436c46","decided_by":"admin","expires_at":"2026-10-07T19:54:33Z","grant_expires_at":"2026-10-06T20:55:04Z","ref":"01a112c8-3763-7b54-b66d-076b8d98aabc","state":"approved"}
```


`straza__approval_request` decides an MCP call it describes the way the gateway decides the real call, so it refuses what the real call would be refused. It also refuses a call that a ticket cannot cover. Each answer says why:

Already allowed by policy
: `Straza: no approval needed. The described call is already allowed by policy; just make the call`

Gated by a hold
: `Straza: that action is gated by an interactive approval (a hold), not a ticket. A hold is decided in-line while the real call blocks. Make the real call and a person is prompted; approval_request opens day-scale tickets only`

Denied by policy
: `Straza: denied by policy. The described call would be denied (<reason>); an approval ticket cannot override a deny`

Not a tool this session can call
: `Straza: <app>:<tool> is not a tool this session can call, so no approval can be requested for it. Check the app and tool name, or ask an admin for access to the tool.` Here and in the reason of a deny, a name over 256 bytes is quoted as its first 256 bytes followed by `... (<n> bytes)`.

Too many requests for one server
: `Straza: rate limit exceeded for the MCP server "<app>" (<n> rps). Retry shortly`

A person denied a ticket for this call, and its window is still open
: `Straza: ticket <ref> was denied by <name>; this call stays denied until <time>`

Nobody who can be asked for the approval
: `Straza: approval cannot be routed: <cause>. Denied (fail-closed)`

Database out of reach while the server looks up an approval
: `Straza: this call needs an approval, and the Straza server could not reach its database to check for one, so the call did not run. Denied (fail-closed). Retry the call in a moment, and if this keeps happening ask your Straza administrator to check the server's database connection.`

Not a concrete call
: `Straza: a concrete call is required. approval_request needs action.tool plus an app+tool_name, a command, or argv (free-text requests are rejected)`

Server or tool missing from an MCP call
: `Straza: approval_request needs action.app and action.tool_name for an mcp.call: the server and the tool of the later real call. Add both, then request again.`

Command or argv on an MCP call
: `Straza: an mcp.call action takes no action.command or action.argv, because the later real call is an MCP tool call and carries neither. Remove them, then request again.`

MCP call without its arguments, under a rule that covers the exact call
: `Straza: this rule's approval covers the exact call. approval_request needs action.args (the exact tool arguments the later real call will use); without them the approved ticket could never be consumed`

Reference the agent does not own, in any of the three tools
: `Straza: no such approval <ref>`

The not-a-tool answer is the same for a tool that no access row of the session's roles admits, a server that does not exist or is stopped, and a tool that the session's policy hides, so the answer does not tell them apart. With `apps.catalog.policyFilter` on, the default, policy hides every tool that a rule denies, so a call to such a tool gets the not-a-tool answer and not the denied-by-policy one. Each request for an MCP call spends one call of that server's rate limit, as the real call would. When the server's object checks refuse the call, or its arguments break the tool's input schema, the request is refused as the real call would be, and nobody is asked to approve it. Policy alone decides a described command.

A ticket rule that names no approver role goes to the person behind the agent, its sponsor. A confirmation rule, `mode: confirm`, and a rule with `selfApproval: true` go to the requester instead. When the agent has no sponsor, or its sponsor cannot decide, the request is refused, and `<cause>` names what failed and the fix, such as `agent "<name>" has no sponsor, and rule "<rule>" names no approver roles. Fix: set the requester's sponsor to a person in your identity manager, or add approve.roles / approve.deciders to the rule`. The audit record of that `straza__approval_request` call is then a deny with the answer as its reason. No ticket opens and nobody is notified. `straza_approvals_unroutable_total` counts the refusal once.


The gateway also serves `straza__draft_submit` and `straza__draft_status`, to a session whose roles include `straza-draft-config`, as [Propose and publish a change]({{< relref "guides/changes/propose-and-publish.md" >}}) shows. To any other session they do not exist, and a call gets `unknown tool "straza__draft_submit"`.
