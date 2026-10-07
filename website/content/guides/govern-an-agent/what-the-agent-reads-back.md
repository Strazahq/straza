---
title: What the agent reads back
description: The exact words an agent reads from Straza, from the session banner to a deny, a pending approval, a ticket and the built-in approval tools.
pagetype: reference
weight: 80
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: An Alpine Linux container against a standalone server, as alice holding the application role dev, with hook payloads piped into straza hook, gateway calls sent through straza mcp, and an admin deciding with strazactl. approval.gatewayHoldSeconds was 3, so a held gateway call answered pending after three seconds
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

Its last sentence tells the agent how to treat every deny on this page. The agent passes the reason on to its user and does not try again.


When a policy records the session's conversations, the banner gains one sentence. With secrets masked it reads as below, and with recording word for word it ends `recorded word for word to the audit system.`

```text
Recording is on: by policy, the prompts and responses of this session are recorded with secrets masked to the audit system.
```


Knowledge packs bound to the session's roles follow the banner, as [Knowledge packs]({{< relref "guides/govern-an-agent/knowledge-packs.md" >}}) shows. For Claude Code and Codex the hook also returns `systemMessage`, one line for the person at the keyboard. It starts with a shield symbol and `Straza governance active:`, then names the user, the roles, the server, the policy and the attestation. Gemini gets the context only.


When the check-in fails, the session-start hook exits with code 2 and a reason that begins `Straza: checkin failed:`, followed by the server's refusal. Until a session starts, every tool call is denied with `Straza: no active Straza session. Restart the session so straza can check in`. [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) shows the refusal a server gives a machine below its attestation minimum.

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

Classifier that says no, under a rule with `mode: classify`
: `Straza: classifier: <the classifier's reason>`

Confirmation rule met by an autonomous AI agent
: `Straza: rule <set>/<rule> requires the requester's confirmation; an autonomous session has no human to confirm`

Gateway call for a tool the session cannot see
: The JSON-RPC error `unknown tool "<name>"`, the same answer as for a tool that does not exist.

Rate limit for that server at the gateway, used up
: `Straza: rate limit exceeded for the MCP server "<server>" (<n> rps). Retry shortly`


The gateway leaves a tool that policy denies out of the session's tool list, so a call to it gets `unknown tool`. With `apps.catalog.policyFilter` set to false, the tool stays listed and the call gets the policy's reason instead.

## When Straza cannot decide


Straza fails closed. When a hook cannot prove its decision, it denies, and the reason says what is wrong and what to do.

No session on the machine
: `Straza: no active Straza session. Restart the session so straza can check in`

Session that `straza daemon` dropped because the server revoked it or refused to renew it
: `Straza: session revoked (<reason>). Tool calls stay denied until a new session starts and checks in again. If only this session was revoked, that check-in starts a new session. If the device or user was disabled, the check-in is refused until an administrator re-enables it. Inform the user and stop.`

Token expired past the offline grace
: `Straza: session token expired <age> ago and the offline grace period (<grace>) is exhausted.`, then a sentence that says Straza cannot verify current policy and asks the agent to reconnect and run `straza doctor`

Renewal refused by the server
: `Straza: session renewal was refused: <the server's sentence>`

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


`straza__approval_request` refuses a call that a ticket cannot cover, and says why:

Already allowed by policy
: `Straza: no approval needed. The described call is already allowed by policy; just make the call`

Gated by a hold
: `Straza: that action is gated by an interactive approval (a hold), not a ticket. A hold is decided in-line while the real call blocks. Make the real call and a person is prompted; approval_request opens day-scale tickets only`

Denied by policy
: `Straza: denied by policy. The described call would be denied (<reason>); an approval ticket cannot override a deny`

Not a concrete call
: `Straza: a concrete call is required. approval_request needs action.tool plus an app+tool_name, a command, or argv (free-text requests are rejected)`

MCP call without its arguments, under a rule that covers the exact call
: `Straza: this rule's approval covers the exact call. approval_request needs action.args (the exact tool arguments the later real call will use); without them the approved ticket could never be consumed`

Reference the agent does not own, in any of the three tools
: `Straza: no such approval <ref>`


The gateway also serves `straza__draft_submit` and `straza__draft_status`, to a session whose roles include `straza-draft-config`, as [Propose and publish a change]({{< relref "guides/changes/propose-and-publish.md" >}}) shows. To any other session they do not exist, and a call gets `unknown tool "straza__draft_submit"`.
