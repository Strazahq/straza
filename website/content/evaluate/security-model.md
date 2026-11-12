---
title: The security model
description: What Straza enforces and where, what it trusts, and which actions it does not see.
aliases:
  - /concepts/trust-model-and-non-goals/
pagetype: explanation
weight: 30
draft: false
summary: >-
  Straza decides the actions that reach its hook, its MCP gateway or `straza exec`, and it does
  not see any other action. The gateway is a security boundary, because the decision and the
  server's credential stay with strazad. A hook installed for the user, on a machine the agent
  controls, is advisory.
keywords: security model trust model threat model boundary advisory hook managed install gateway sandbox trusts non-goals no AI Claude Code managed settings MDM Intune Jamf
---


Straza enforces a decision on each action that reaches it. Whether an agent can bypass that decision depends on where it is made. [The security model in depth]({{< relref "security/security-model.md" >}}) explains the mechanism behind each row below, and [Known limits]({{< relref "security/known-limits.md" >}}) lists every risk Straza leaves with you.


| Question | Answer |
|---|---|
| What Straza enforces | Each tool call that reaches it is allowed, denied or [held for a person]({{< relref "concepts/approvals-model.md" >}}), against signed [policy]({{< relref "concepts/policy-model.md" >}}). Every decision is recorded for the [hash chain]({{< relref "concepts/evidence.md" >}}). |
| Where it is a security boundary | At the [gateway]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}), for a server the agent cannot call directly. Inside the sandbox image, where `straza exec` is the only way to run a program. |
| Where it is advisory | A hook installed for the user, and `straza exec` on an open machine. An agent that can run anything there can go around either. |
| What it trusts | Your IGA, or an admin on standalone, on who holds which [role]({{< relref "concepts/roles-and-access.md" >}}). The operating system of a managed install. The transport to strazad. The servers you register. |
| What is outside its view | An action that reaches none of its enforcement points, what a model computes inside its own process, model traffic, and root on the machine. |
| The main limit on a managed fleet | Claude Code reads only its highest managed source, so a device management profile, server-managed settings or a registry policy without Straza's hooks makes it skip them. |

{{< details summary="What Straza defends against" >}}

An agent runs with the shell, the files and the credentials of the person or the workload behind it, and a model can be persuaded to use them. The attacks that follow from this are a prompt that turns into a destructive command, a session token stolen from a laptop, a policy swapped on its way to the machine, a hook edited out of the harness, an API key read out of the agent's environment, and a log changed after the fact.

Policy can deny destructive commands, limit which tools a session reaches, or ask a person first, and the gateway enforces the rate limits you set. Session tokens are valid for 300 seconds and can be revoked, and a session ends at its maximum lifetime, 12 hours by default. The system service on Linux, macOS and Windows keeps each machine's key under its own OS account, so the person's own files hold no Straza credential and a session token copied from the service works nowhere else. A service started with `STRAZA_HOME` keeps the key in the person's own files, which standalone accepts and enterprise refuses by default. strazactl keeps no sign-in and runs on an admin API token that a person mints, a bearer secret that whoever holds it can use until it expires or is revoked. No admin credential sits on a machine unless someone puts a token there, as [Credentials and sessions]({{< relref "security/credentials-and-sessions.md" >}}) explains.

Clients check each policy snapshot against keys pinned when the machine enrolled, and never enforce one that fails the check. `straza service` also measures its own binary and, on a managed install, the root-owned files, so strazad can compare them with its registry when a session starts. The gateway adds stored credentials without returning them to the agent. Stored credentials are sealed at rest, and the audit chain can be checked for tampering.
{{< /details >}}

## What each enforcement point is worth


A hook installed for the user is advisory, because an agent that can run anything on its machine can bypass it. A managed install keeps the hook in root-owned files whose hashes strazad checks when a session starts, and by default the enterprise profile starts sessions only from such an install.

The gateway is a security boundary for a server the agent cannot call directly, because the decision, the session's tool list and the server's [credential]({{< relref "concepts/credentials.md" >}}) stay with strazad. A server that a person adds to their own harness never reaches the gateway, so register every server that holds a credential in Straza.


`straza exec` decides each command it is given and none of the programs that command starts. On an open machine it is also advisory. Inside the documented sandbox image it is the only way to run a program, with the gaps described below.

[What each lane stops]({{< relref "evaluate/what-each-lane-stops.md" >}}) says what counts as a finding.

{{< details summary="The sandbox image, the Python kit and the console" >}}

Inside the sandbox image the path holds only the shim, the wrapper shell and the agent's interpreter. Every other file loses its execute bit at build time. A file that keeps it fails the build. With the documented runtime flags, the root filesystem is read-only, every writable mount is noexec, all capabilities are dropped and no process can gain privileges. Computation inside the interpreter stays ungoverned, because the interpreter is the agent. Every binary on the image's allowlist can still run without a decision, so a rule on a tool's arguments is enforced at the boundary only for a tool that is absent from the image. A tool whose arguments must be enforced belongs behind the gateway.

Network isolation depends on how you run the image. The shipped compose file leaves its network routable, so that a strazad on the host can be reached. Setting `internal: true` on that network, with only strazad attached, lets the agent reach only strazad.

The gateway sees only MCP calls. A deployment with the gateway alone therefore leaves local commands ungoverned. The Python kit uses the hook's decision path and shares its limit. It governs the tool calls that a framework routes through it and not the computation inside the process.

The console and the self-service page can start a session below the attestation minimum, so an admin can always sign in. Such a session still cannot call MCP tools, because the gateway checks the minimum again from the token on every call. strazactl needs no session at all, because it runs on an admin API token. [Hookless processes]({{< relref "guides/govern-an-agent/hookless-processes.md" >}}) shows how to run the sandbox image.
{{< /details >}}

## Claude Code on a managed fleet


A managed install writes Straza's hooks into Claude Code's managed settings file, and by default Claude Code uses only the highest of its managed sources. Server-managed settings, a Claude Code profile from a device management tool such as Intune or Jamf Pro, and a registry policy on Windows all rank above that file. On a machine that gets any of them without Straza's hooks, Claude Code skips the hooks with no warning. [Known limits]({{< relref "security/known-limits.md" >}}) lists what reduces this risk, in the row "Claude Code skips Straza's hooks under a higher managed source".

## What Straza trusts


Straza relies on these without checking them:

- Your IGA, or an admin on standalone, is right about who holds which role. A wrong role assignment leads to a wrong decision.
- The operating system keeps a managed install's root-owned files safe. An agent with sudo or administrator rights there is outside the threat model.
- A client reports its install and the custody of its key truthfully. strazad checks the report and cannot see which program sent it.
- The transport to strazad is sound. Enterprise warns when it serves plain HTTP, and no setting skips certificate checks.
- A session's claim to come from the sandbox image is trusted, because strazad cannot check it yet.
- An operator who registers a server is trusted with its manifest. A server whose roles cover only some of its [objects]({{< relref "concepts/objects.md" >}}) keeps three promises that Straza cannot check.

## What Straza does not try to do


- An interpreter can compute anything inside its own process without a decision from Straza.
- A script an agent writes and then runs is decided only as the interpreter call that starts it, which a rule can deny or send to the classifier.
- Root on a machine controls every process on it, including the hook.
- Straza does not replace your IGA.
- Using Straza does not certify compliance. It produces evidence you can map to a control, and you assess whether that meets a regulation.

## How Straza makes decisions


Every decision Straza makes is a deterministic policy evaluation, and no AI model takes part in a decision. The classify check is a heuristic with no model, no network and no state. The audit sentinel, off by default, runs fixed rules over the record after the fact, for patterns such as a written file that is then run, and blocks nothing. Straza does not proxy model traffic, because a proxy there would see prompts and miss the tool calls that policy decides.

{{< next "evaluate/what-each-lane-stops.md" >}}
