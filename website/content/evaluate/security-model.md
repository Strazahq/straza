---
title: The security model
description: What Straza stops, where it holds against an agent that tries to get around it, what it relies on and what it does not cover.
aliases:
  - /concepts/trust-model-and-non-goals/
pagetype: explanation
weight: 30
draft: false
summary: >-
  Straza assumes that an agent can be turned against you. Such an agent cannot get around the
  gateway or the hook of a managed install, because the decision, the policy and the credentials sit
  where it cannot change them. Straza relies on your role assignments, on the operating system
  and on the key file of strazad.
keywords: security model trust model threat model boundary advisory hook managed install gateway sandbox trusts non-goals no AI Claude Code managed settings MDM Intune Jamf
---


An agent runs with the shell, the files and the credentials of the person or the workload behind it, and a model can be persuaded to use them. So Straza treats the agent as the party it defends against, and it decides each action outside the agent's process.

## What Straza defends against


| The attack | What Straza does about it |
|---|---|
| A prompt turns into a destructive command. | [Policy]({{< relref "concepts/policy-model.md" >}}) denies the command or [holds it for a person]({{< relref "concepts/approvals-model.md" >}}) before it runs. |
| An agent reads an API key from its environment. | The [credential]({{< relref "concepts/credentials.md" >}}) of an MCP server stays in strazad, sealed at rest. The gateway adds it to an allowed call and never returns it. |
| An agent calls a tool that its roles do not include. | The gateway lists only the tools the session's roles grant, and it answers `unknown tool` for every other one. |
| An agent removes the hook from its coding tool. | On a managed install only root or an administrator can change the hook, and strazad checks the install's hashes each time a session starts. |
| Someone copies a session token from a laptop. | The token of a session on a machine lives 300 seconds and works only with a proof from that machine's key. The system service keeps the key under its own OS account. |
| Someone swaps the policy on its way to a machine. | The client uses a policy snapshot only after it has verified strazad's signature, with keys it pinned at enrollment or took from the managed configuration. |
| A copy of the database leaks. | Stored credentials and the signing keys are sealed with a key file that is not in the database, so a copy taken on 2.3.0 or later signs no token and no policy. |
| An agent floods a server with calls. | The gateway limits each session, to 20 calls a second on a remote server by default, and all sessions of one user to five times that. Each strazad replica counts on its own. |
| Someone changes a log after the fact. | Every [audit record]({{< relref "concepts/evidence.md" >}}) is linked to the one before it in a hash chain, which `strazactl audit verify` recomputes on your machine. A sink keeps a copy of every record off the box, where a database owner cannot rewrite it. |
| An agent rewrites the policy that governs it. | An agent can propose a change only as a [draft]({{< relref "concepts/drafts.md" >}}), and a person publishes it. |
| A person leaves, or an agent must be stopped. | Disabling the user in your IGA, in Entra ID or in Straza revokes every session of that user and refuses new ones. |

## Which lanes an agent cannot get around


An agent cannot get around the gateway for an MCP server whose credential lives only in Straza, because the decision, the tool list and the credential stay with strazad. Without root it cannot get around the hook of a managed install either. In the sandbox image it cannot start a program outside the allowlist without a decision.

A hook that a user installed and `straza exec` on an open machine are for trying Straza. An agent can get around both, and by default strazad on the enterprise profile gives them no session. [What each lane stops]({{< relref "evaluate/what-each-lane-stops.md" >}}) says what counts as a finding in each lane and how to test it.

A server that a person adds to their own coding tool never reaches the gateway. The hook denies its calls unless a rule allows that server. Its credential still sits on that machine, so register in Straza every MCP server that needs a credential.

{{< details summary="The sandbox image, the Python kit and the console" >}}

`straza exec` decides the command it is given and none of the programs that command starts. In the sandbox image only the shim, the wrapper shell, the agent's interpreter and a short allowlist keep their execute bit. Every other file loses it at build time, and a file that keeps it fails the build. With the documented runtime flags the root filesystem is read-only, every writable mount is noexec, all capabilities are dropped and no process can gain privileges.

A binary on the allowlist still runs without a decision, and so does anything the interpreter computes. A tool whose arguments must be enforced therefore belongs behind the gateway.

The stock image's sessions reach only the attestation level `advisory`. By default an enterprise server refuses them until you give the image a managed configuration and [register it]({{< relref "guides/operate/best-practices.md#register-the-agent-image-where-the-server-requires-managed" >}}). The shipped compose file leaves the image's network routable, so that a strazad on the host can be reached. With `internal: true` on that network and only strazad attached, the agent reaches only strazad and its own Straza service. [Hookless processes]({{< relref "guides/govern-an-agent/hookless-processes.md" >}}) shows how to run the image.

The Python kit uses the hook's decision path. It governs the tool calls that a framework routes through it and not the computation inside the process.

The gateway sees MCP calls, and the git fetches and pushes that go through a Straza grant.

The console and the self-service page can start a session below the attestation minimum, so that an admin can always sign in. Such a session cannot call MCP tools, because the gateway checks the minimum again on every call. strazactl needs no session. It runs on an admin API token that a person mints, and whoever holds that token can use it until it expires or is revoked, as [Credentials and sessions]({{< relref "security/credentials-and-sessions.md" >}}) explains.
{{< /details >}}

## What Straza relies on


- Your IGA, Entra groups or an admin is right about who holds which role.
- The operating system keeps a managed install's root-owned files safe.
- strazad's key file, `secret.key`, stays apart from the database and its backups. The two together can sign any token and any policy.
- The transport to strazad is sound. Enterprise warns on plain HTTP, and no setting skips certificate checks.
- A client reports its install and the custody of its key truthfully. strazad checks the report against its registry and cannot see which program sent it.
- An operator who registers a server is trusted with its manifest.
- Your device management leaves Straza's hooks in place. Claude Code reads only its highest managed settings source, so your own settings belong in `managed-settings.d`, as the [Claude Code]({{< relref "guides/govern-an-agent/claude-code.md#install-the-hook-wiring" >}}) guide shows.

## What Straza does not cover


- An action that passes none of the three lanes is outside Straza's view.
- A program's own computation is not decided. A script that an agent writes and then runs is decided as the interpreter call that starts it, which a rule can deny or send to the [classifier]({{< relref "guides/write-policy/classify.md" >}}).
- Root on a machine controls every process on it, the hook included.

[Known limits]({{< relref "security/known-limits.md" >}}) names the limits that stay with you, and [The security model in depth]({{< relref "security/security-model.md" >}}) explains each mechanism.

{{< next "evaluate/what-each-lane-stops.md" >}}
