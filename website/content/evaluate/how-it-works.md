---
title: How it works
description: The parts of Straza, and how Straza decides each tool call before the tool runs.
aliases:
  - /concepts/how-straza-works/
  - /get-started/how-that-worked/
pagetype: explanation
weight: 20
draft: false
summary: >-
  Your IGA, or an admin on standalone, decides who holds which role. Straza decides what each
  role may do. It checks each governed tool call before the tool runs, on the agent's machine
  or at the gateway in front of your MCP servers. A rule can ask a person first. Straza writes
  an audit record of every decision.
keywords: how it works model person agent sponsor role server tool object policy approval draft audit record hook gateway snapshot fail closed kill switch
---

{{< diagram name="model" caption="The core parts of Straza. Each concept page opens with this diagram and highlights the part it explains." >}}


On the enterprise profile, your identity provider checks who signs in, and your IGA says who holds which role. On standalone, strazad signs people in, and an admin in Straza assigns the roles.

## The parts


| Part | What it is | Where it comes from |
|---|---|---|
| [Person]({{< relref "concepts/identities.md" >}}) | A human user. On enterprise they sign in at your identity provider, and on standalone strazad signs them in with a password. | Your IGA over SCIM, or an admin on standalone |
| [Agent]({{< relref "concepts/identities.md" >}}) | An AI agent. A coding agent such as Claude Code runs as its person. An agent with its own identity is a user of its own, with a key instead of a password. | Your IGA over SCIM, or an admin on standalone |
| [Sponsor]({{< relref "concepts/identities.md" >}}) | The person accountable for an agent, or for any user who has one. A held call whose rule names no approver role goes to the requester's sponsor. | Your IGA, or an admin on standalone |
| [Role]({{< relref "concepts/roles-and-access.md" >}}) | What its holders may do. An application role grants tools on one server, or picks which policy applies to local tools. Business roles group application roles, approver roles decide held calls, and Straza roles carry rights in Straza itself. | Made in Straza. Your IGA, or an admin on standalone, assigns it |
| [Server]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}) | An MCP server behind Straza's gateway, described by its manifest. | Added in Straza |
| Tool | One action an agent can take: a tool of a server, or a local one such as a shell command. | The server, or the agent's coding tool |
| [Object]({{< relref "concepts/objects.md" >}}) | One thing a server's tools act on, such as a repository or a database, on a server that declares them. | The server's own list |
| [Policy]({{< relref "concepts/policy-model.md" >}}) | Rules that allow a call, deny it, or ask a person first. | Written in Straza |
| [Approval]({{< relref "concepts/approvals-model.md" >}}) | A person's yes or no on a held call. | The console, a browser, a phone or Slack |
| [Draft]({{< relref "concepts/drafts.md" >}}) | A proposed change to servers, roles or policy. A draft an agent proposes waits until a person publishes it. | A person or an agent |
| [Audit record]({{< relref "concepts/evidence.md" >}}) | One record for each decision, sign-in and change, linked to the one before it in a hash chain. | Written by Straza, and sent on to your SIEM |

## How a tool call is decided


Straza decides each governed tool call before the tool runs. The same policy engine decides on the agent's machine and at the gateway, so a rule means the same thing in both places.

| | On the agent's machine | At the gateway |
|---|---|---|
| The tool | A local tool, such as a shell command or a file write | A tool of an MCP server behind Straza |
| Who asks | The coding tool asks the straza hook | The agent's MCP client calls Straza's gateway instead of the server |
| Who decides | The Straza service on the same machine, from a signed copy of your policy, so the decision does not wait on the network | strazad, which lists only the tools the session's roles reach and checks the object a call names before the policy runs |
| After an allow | The tool runs | strazad adds the server's [credential]({{< relref "concepts/credentials.md" >}}) and passes the call on, so the agent never holds it |
| After a deny | The agent reads the rule's reason | By default a tool denied for the whole session is hidden and reads as unknown |


A call to a server behind the gateway also passes the hook. The hook leaves the whole decision to the gateway, so the call is not decided twice.


An agent with no hooks can run each command through `straza exec`, which decides that command the same way before it runs. It does not decide what the command then starts. Straza does not see an action that takes none of these paths, as [The security model]({{< relref "evaluate/security-model.md" >}}) sets out.

## When Straza cannot decide


Straza fails closed. A call ends in a deny when the service does not answer, or when a rule needs strazad and strazad cannot be reached. The deny names the cause and the next step.

When strazad cannot be reached, a session keeps deciding from its verified copy of the policy until its token runs out. Standalone then adds a grace period of 15 minutes, and enterprise adds none. After that, the session denies its calls. A bad deploy of strazad therefore stops every enterprise agent within five minutes. Allowing calls on error would leave agents ungoverned for the whole outage.

## When access is taken away


Disabling a user, over SCIM from your IGA or by an admin in Straza, revokes every session of that user and refuses new ones. The gateway refuses a revoked session on its next call. On the agent's machine, the service learns of it at once over the session's push stream. While that stream is down, it learns at the next renewal of the session's 300-second token.

{{< details summary="The outcomes a rule can pick" >}}

The rule that wins decides the call. A deny is final. A winning allow can carry a mode that adds one more step before the tool runs.

| Outcome | What happens | What the agent sees |
|---|---|---|
| <span class="chip allow">allow</span> | The tool runs. | Nothing. The call goes through. |
| <span class="chip deny">deny</span> | The tool does not run. | On the machine, the rule's reason, which it should pass on to you instead of retrying. At the gateway, by default, a tool that a rule denies for the whole session is missing from the session's list. |
| <span class="chip hold">approve</span> | A person decides first: a holder of an approver role the rule names, or else the requester's sponsor. A person with no sponsor decides their own request on an enrolled phone or browser. | A reference to the request. The gateway holds the call open for the rule's decision window first, 90 seconds by default and at most 120, and on a hook the agent calls again after the decision. |
| <span class="chip hold">confirm</span> | Only the person behind the request can say yes. An agent with no person behind it is denied. | The same as for approve, until that person confirms. |
| <span class="chip plain">serverCheck</span> | strazad decides the call again with the live policy, and an unreachable server means a deny. | Allow or deny, as above. |
| <span class="chip plain">classify</span> | A built-in check reads the command first, such as a download piped into a shell, and denies when it cannot answer within one second. | Allow, or a deny that names the signal it found. |

When no rule fires, the default depends on where the call runs. A local tool is allowed on standalone and denied on enterprise. At the gateway, a tool a role grants runs unless a rule denies it or holds it for a person. [Policies]({{< relref "concepts/policy-model.md" >}}) explains how rules match and combine, and [Approvals]({{< relref "concepts/approvals-model.md" >}}) covers who decides. [Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md#approvals" >}}) lists how long each wait lasts.
{{< /details >}}

{{< details summary="How the policy reaches each machine" >}}

A person publishes a change to a policy set, or an admin activates one. strazad compiles every live set into a single snapshot and signs it with Ed25519. The snapshot's id is the SHA-256 of the signed bytes, so the id proves which bytes a machine holds. The service on each machine fetches the snapshot. It checks the signature against the keys it pinned when the machine enrolled, and it checks the id against the bytes. It uses the snapshot only after both checks pass. A snapshot that fails a check is never used. The service keeps deciding with the last one that passed until the session's time runs out, and then it denies.

The service looks for a new snapshot when a session starts, when its token renews, at once when a policy event arrives on the session's push stream, and after a decision once its last look is 30 seconds old.
{{< /details >}}

{{< details summary="Machines and sessions" >}}

A person enrolls a machine with `straza enroll`. The service makes an Ed25519 key that never leaves it and opens the person's browser at your identity provider, or at strazad's own sign-in on standalone. strazad registers the machine with that key only when the browser sign-in names the key and is at most 10 minutes old. A machine with no display can sign in with a code instead, which names no key, so the enterprise profile refuses it by default. The enrollment lasts 7 days from the sign-in by default, and the person then enrolls again.

When a session starts, the service checks in with a proof from that key over a nonce from strazad, a counter above the last one and the hashes it measured of its install. strazad checks its denylist, the user's status and the sign-in deadline, computes the attestation level, resolves the user's roles, and mints a session token signed with Ed25519. The level is `advisory` for an install in the user's own files, `managed` for a root-owned install whose hashes match the ones registered, and `none` when the client sent no hashes or a managed install reported a file whose hash strazad does not hold. By default the enterprise profile mints no token below `managed`, except for the console and the self-service page, which cannot call MCP tools.

The token binds the user, the session, the device, the harness, the attestation level, a hash of the roles, the snapshot id and the key's thumbprint, so it works only with a fresh proof from that key. It lives 300 seconds, and every server path verifies it without a database read. The service holds it, so the hook never sees it, and renews it when a call finds 120 seconds or less left. The check-in answer also carries the knowledge packs bound to the user's roles, and the hook tells the agent in its own context that governance is active. An AI agent with its own identity starts headless sessions with its own key and no enrolled device.

On the machine, the coding tool passes the hook an event in its own format. The hook turns it into a canonical event and passes it to the service over a local socket, or a named pipe on Windows. On an allow, the hook exits 0 and the tool runs. On a deny, the hook writes the reason to standard error and exits 2, so the coding tool does not run the tool and the model reads the reason. Gemini CLI reads a JSON decision on standard output instead. A hook that cannot read its event or reach the service denies the call. [Credentials and sessions]({{< relref "security/credentials-and-sessions.md" >}}) describes each credential from enrollment to lockout.

[Machines and sessions]({{< relref "concepts/machines-and-sessions.md" >}}) covers the details.
{{< /details >}}

{{< details summary="Why the decision is local" >}}

An API gateway asks a server on every call. Straza decides on the agent's machine instead, because a tool call happens inside a person's editor and inside loops that run thousands of times an hour. A round trip on each call would make governance the slowest step in the loop, and people would turn it off. Deciding locally means that policy and revocations have to reach every machine. Policy is sent as a signed snapshot, and revocations are pushed as events into in-memory denylists instead of being looked up. strazad applies the same approach on the paths it owns. No decision on the gateway or the decide endpoint reads the database. Any strazad instance can serve any session once the client has checked in there, and the client checks in on its own.

Straza's speed budget for the local decision is under 100 microseconds at the 99th percentile on a snapshot of 10,000 rules, and under 25 milliseconds at the 95th percentile for the whole hook, process start included. The load rig under `test/load` reproduces both numbers on your own hardware. Classify and approve rules are exempt, because they are meant for a few chosen calls.

Straza writes audit records after the decision is made. The service spools its records and uploads them in batches, and strazad passes its own records through an in-memory queue to the database and into the hash chain. A record you look for right after a decision can therefore arrive a moment later. On enterprise a full queue can make a decision wait, and the client's spool is bounded. [Evidence and audit]({{< relref "concepts/evidence.md" >}}) explains the delivery path, and [The security model in depth]({{< relref "security/security-model.md#audit-records-can-be-lost" >}}) lists when a record can be lost.


A test in the repository fails when a call through the gateway or the decide endpoint reads the database.
{{< /details >}}

{{< next "evaluate/security-model.md" >}}
