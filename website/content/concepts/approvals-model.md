---
title: Approvals
seo_title: "Approval model: deciders, holds and retries"
description: Who decides a call that a rule holds, where they decide it, how long it waits, and what the agent meets in a hook and at the gateway.
pagetype: explanation
weight: 40
draft: false
summary: >-
  An approval is a person's yes or no on a call that a rule holds. The deciders are holders of an approver role the rule names, or else the agent's sponsor. They decide in the console, a browser, a phone or Slack. An AI agent never decides. A no, a timeout or an unreachable service ends in a deny.
noun: approval
keywords: approval approve hold ticket confirm approver sponsor decider timeout retry phone slack self-service
---


An approval is a [person's]({{< relref "concepts/identities.md" >}}) yes or no on a held call. Some actions, such as a deploy or a write to a production database, are safe only when a person meant them. A deny would stop that work, and an allow would leave the choice to the model. A [policy]({{< relref "concepts/policy-model.md" >}}) rule with the mode approve holds such a call until a person decides, and Straza tells the people who may decide it.

{{< diagram name="approval-hold" caption="On the gateway a held call stays open while a person decides. In a hook the first call is denied with a reference, and the retry of the same call passes once after the approval." >}}

## Who decides


Each rule names its deciders. It can name approver roles, a kind of [role]({{< relref "concepts/roles-and-access.md" >}}) that carries the right to decide and never a tool. It can instead name the sponsor, the person accountable for the [agent]({{< relref "concepts/identities.md" >}}) that made the call. A rule that names nobody also goes to the sponsor. [Your IGA]({{< relref "evaluate/identity-and-your-iga.md" >}}), or an admin on standalone, sets each sponsor and assigns the approver roles. The table shows both kinds of decider.


| | wiki, invented | postgres, adaptive-pg-mcp |
|---|---|---|
| The held call | `page_edit` | `execute_sql`, free SQL that changes rows |
| Who decides | holders of an approver role the rule names | the sponsor of the agent that called |
| How long it waits | 90 seconds, the default | 120 seconds, set on the rule |
| What a yes covers | that call, with its arguments, on that space | that statement on that database |


When a rule goes to the sponsor and the requester is a person with no sponsor, that person decides their own request. A person who has a sponsor waits for that sponsor. An agent whose rule names no approver role and whose sponsor is missing, inactive or not a person is denied at once, with the cause and the fix in the reason, because a request nobody is told about could only expire.


Some limits apply whatever a rule says. Straza reads the decider's roles at the moment of the decision, so a role removed before the decision does not count. Apart from the case above and a confirm rule, a requester never decides their own request unless the rule allows it, and an autonomous agent never does. An AI agent or a service account never decides on any surface, even when it was given an approver role by mistake. A session that a coding tool checked in decides nothing either, because the agent inside that tool holds the session.

A person deciding their own request uses an enrolled phone or browser, which signs the decision. The console refuses such a decision by default, because an agent on the same machine can reach the console as well as the person can. The signature proves which device decided, but not that the person acted, as [the security model]({{< relref "security/security-model.md#an-agent-that-holds-its-persons-login-can-enroll-an-approval-device" >}}) explains.

## Where they decide


Each place below decides the same request, and the first decision wins.

- In the console, an admin among the deciders sees the request under **Approvals**, with no setup.
- On the self-service page, a person who is not an admin decides from a browser they enabled once, which signs each decision with a key it keeps.
- With phone approval, the Straza approver app gets a push notification that carries only a reference, fetches the request, and signs the decision with the phone's key.
- Slack shows a card in a channel you configure, and Straza checks the signature of every button press. The agent's justification goes there only if you turn that on.

An admin API token cannot decide, because a decision must name the person who made it. [Approve a request]({{< relref "guides/approve/_index.md" >}}) sets up each place.

## The hook path and the gateway path


What the agent meets depends on where Straza checks the call.

| | In a hook, a local tool | At the gateway, a tool of an MCP server |
|---|---|---|
| The first call | Denied at once, with a reference and where a person decides | Stays open while the person decides |
| After a yes | The identical retry runs once, within 60 seconds by default | The call goes on to the server |
| If nobody decides in time | The request expires, and a retry opens a new one | The call is denied when the window ends |
| A window over 120 seconds | No difference | After 120 seconds the call answers that it is still pending, with the time left |

A coding tool stops a hook after its own timeout, so a hook never waits. A gateway call held past its client's deadline looks like a network timeout, and a model that meets one can invent the tool's output. The gateway therefore answers a held call when its hold cap runs out, with the reference, and the agent waits with a built-in tool or sends the call again.


On the gateway, a held tool gains a required justification field, so the agent states in its own words why it needs the call. The person reads that statement on the request. Straza records it as the agent's own words, unchecked, and strips it from the arguments before they reach the audit record or the MCP server.

## How long a request waits


A hold fits a decision that a person makes within minutes. Its request stays open for 90 seconds by default, and a rule may set up to an hour. A ticket fits a decision that takes hours or days, such as a deploy that waits for a change board. It never holds a call. The first call is denied with a ticket reference, and the person has 24 hours to decide by default, up to 30 days. An approved ticket becomes a grant that one later call of the same requester can use from any session, within an hour by default.

Every 15 seconds Straza marks each request past its window as expired, which the agent meets as a deny. A ticket that a person denied stays denied for the rest of its window, so asking again does not reach the deciders. [Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md#approvals" >}}) lists every window.

## What one approval covers


An approval covers a single call. It is tied to the session, the rule and a fingerprint of the call, so another command, another session or a second run needs a new approval. For a tool of an MCP server the fingerprint covers the tool and its exact arguments. A rule can loosen that to the tool alone, for a harmless tool whose arguments change on every call. A ticket's grant follows the requester instead of the session. Every phase leaves an [audit]({{< relref "concepts/evidence.md" >}}) record: the request, the decision with who made it and where, and each call that used it.

## Confirm: the requester decides


A rule with the mode confirm lets only the requester confirm the call, because a confirmation says that the person behind the session meant the call, and nobody else can say that for them, an admin included. It protects against an agent's mistake and a prompt injection. It does not protect against that person. An autonomous session has no person behind it, so Straza denies its call at once instead of opening a request nobody could answer.

{{< details summary="What the agent reads on a confirm rule" >}}

In a hook, the first call is denied with this sentence, where the address is your server's self-service page and the reference is the request's id:

```text
Straza: this call needs the requester's confirmation, on their enrolled phone, or the self-service page under This browser (…/self-service/); retry after it is confirmed (ref …)
```

When the session can see the built-in await tool, the end reads `wait for it with the straza__approval_await tool, then retry (ref …)` instead. When the deployment lets people decide their own requests without a signature, the places read `the self-service page (…/self-service/), the console, or an enrolled phone`. The retry that runs after the confirmation carries the reason `Straza: confirmed by`, then the person's name and the reference.

An autonomous session meets this deny at once, with the set and the rule in place of the dots:

```text
Straza: rule …/… requires the requester's confirmation; an autonomous session has no human to confirm
```
{{< /details >}}

{{< details summary="What an expert needs here" >}}

When a person approves a hold within its window, the server stores one run of that call, keyed by the session, the rule and the call's fingerprint, with its use deadline written in the same step. A held call that wakes, or a retry that matches, uses that run up, on any replica. A waiting gateway call wakes on the broadcast that a request was resolved, or on its own timer. If a broadcast is missed, the wait runs to the timer and denies.


Before it asks anyone, the gateway checks the call's arguments, without the justification, against the input schema the MCP server published. Arguments that break it go back to the agent as an error to fix, and no request opens. A schema the check cannot read, or one that refers to a document outside itself, skips the check. Straza keeps at most 4 KiB of the justification and of the request's summary, with the full length after a cut.


In a hook, the retry that runs on a hold's approval carries the reason `Straza: approved by`, then the decider's name and the reference. A later identical call asks again.


The hold cap is a strazad setting, 120 seconds by default, as [Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md#approvals" >}}) lists, and it never extends a rule's window. If your clients give up sooner, such as the Python kit at 30 seconds or common MCP clients at 30 to 60 seconds, set it below their deadline. A lower cap means one more round trip when an approver is slow.


Three built-in gateway tools let an agent request a ticket for a described call, read its state and wait for it. The request tool decides the described call as the real call would be decided, so it cannot reopen a denied call inside its window. No tool can decide a request. A ticket's grant is a stored row that the server uses up, so a client that is offline can never use it.


The `approve.notify` list of a rule narrows which places announce its requests. A request keeps the list its rule had when the request opened. The list limits only where requests are announced. Every place can still decide. Self-approval on a rule that names no deciders lets the requester or a holder of `straza-admin` decide.


The resolution record names the decider, the place they used and their own words, at most 500 bytes. Decided and expired requests are kept for 30 days by default, and a janitor that runs once an hour deletes older ones.
{{< /details >}}

{{< next "concepts/drafts.md" >}}
