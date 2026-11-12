---
title: What Straza is
description: What Straza does, where it decides, who it is for, and what it does not try to do.
pagetype: explanation
weight: 15
draft: false
summary: >-
  Straza decides each action an AI agent takes against the roles that your IGA, or an admin on
  standalone, assigns, and records every decision. The same server binary runs on a laptop and
  on a fleet. Straza sees only the actions that pass through its hook, its gateway or
  `straza exec`.
keywords: what is straza overview runtime governance AI agents IGA roles hook gateway exec standalone enterprise open source license non-goals
---


Straza is open source runtime governance for AI agents. Your IGA, or an admin on the standalone profile, says who [people and agents]({{< relref "concepts/identities.md" >}}) are and which [roles]({{< relref "concepts/roles-and-access.md" >}}) they hold. Straza decides each tool call that passes through it against those roles, on the agent's machine and at the gateway in front of your MCP servers. A rule can [ask a person first]({{< relref "concepts/approvals-model.md" >}}). Every decision becomes an [audit record]({{< relref "concepts/evidence.md" >}}).

## The problem it answers


An agent runs with the shell, the files and the credentials of the person or the workload behind it, and a model can be talked into using them. A control inside the model's own process cannot stop that, because that process is the thing under attack. Straza decides each action outside the model, against a signed [policy]({{< relref "concepts/policy-model.md" >}}), and refuses to act when it cannot tell what the current state is. It keeps the [credentials]({{< relref "concepts/credentials.md" >}}) of your MCP servers out of the agent's reach.

## Where Straza decides


Straza decides at these enforcement points, and each one sees a different kind of action:

- The hook runs on the agent's machine. Claude Code, Codex CLI and Gemini CLI call it before each local tool runs, such as a shell command or a file write, and the Straza service on that machine decides. A plain tool loop gets the same decision through the Python kit.
- The gateway sits in front of your [MCP servers]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}). Any MCP client can reach the servers registered behind it without code on the agent's side, and strazad decides each call there.
- `straza exec` covers a process with no hook. It decides the command it is given before that command runs. It does not decide what that command starts.


An action that bypasses all of them is outside Straza's view. The gateway enforces a boundary in front of a server that the agent cannot call directly, because the decision and the server's credential stay with strazad. A hook installed for the user is advisory, and an agent that can run any command can skip it, as [The security model]({{< relref "evaluate/security-model.md" >}}) explains. [How it works]({{< relref "evaluate/how-it-works.md" >}}) traces a call through the hook and through the gateway.

## Who Straza is for


Straza is for a company that runs AI agents against real systems and wants each agent's actions to follow the roles the company already governs. Its server, strazad, is one static binary with an enterprise and a standalone profile.

The enterprise profile is the one a company runs. It runs on Postgres and scales out on Kubernetes with the Helm chart. Your identity provider checks sign-in, and your IGA sends people, agents, [sponsors]({{< relref "concepts/identities.md" >}}) and role membership over SCIM 2.0.

The standalone profile runs on one host, with SQLite and an event bus inside the process. strazad signs people in itself, and an admin assigns roles and sponsors. It is how you try Straza on a laptop, and it also suits a developer or a small team.


Neither profile ties you to a vendor. Sign-in is standard OIDC, with guides for Entra ID, Okta and Keycloak. Provisioning is standard SCIM 2.0, and the guides cover midPoint, Okta and any other SCIM client. Roles are made in Straza, and your IGA, or an admin on standalone, assigns them. [Identity and your IGA]({{< relref "evaluate/identity-and-your-iga.md" >}}) says which system checks what.


Straza is open source. Its core is under AGPL-3.0-only, and the code you build against, such as the specifications and the agent kits, is under Apache-2.0.

## What Straza does not try to do


- Straza does not replace your IGA. On enterprise, joiners, movers and leavers stay in your IGA, and Straza applies the roles your IGA assigns.
- Straza does not proxy model traffic, so prompts and completions stay on the stack you already run unless a policy turns on conversation recording at the hook. Every decision is deterministic policy evaluation, and no AI model takes part in it.
- A model's own computation is outside Straza's view. An interpreter can compute anything inside its process without a decision. A script an agent writes and then runs is decided only as the interpreter call that starts it.
- Root on a machine controls every process on it, including the hook. An agent with sudo or administrator rights on a managed machine is outside the threat model.
- Straza does not certify compliance. It enforces decisions and produces evidence you can map to a control. Whether that meets a regulation is your assessment, made with the people who answer for it.

{{< next "evaluate/how-it-works.md" >}}
