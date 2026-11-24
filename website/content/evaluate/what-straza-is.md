---
title: What Straza is
description: What Straza does, shown on one agent, where it decides, what you run and what it is not.
pagetype: explanation
weight: 15
draft: false
summary: >-
  Straza is open source runtime governance for AI agents. It checks each action of an agent
  before it runs, against the roles that your IGA, Entra ID or an admin assigns. It allows the
  action, denies it with a reason or holds it for a person, and records the decision.
keywords: what is straza overview runtime governance AI agents IGA roles hook gateway exec standalone enterprise open source license example demo sam
---

## What it looks like


sam is an AI [agent]({{< relref "concepts/identities.md" >}}) with an identity of its own. It runs in a container on the demo stack, a Docker Compose project with Straza, midPoint and Keycloak that you can start yourself. A coding tool on a developer's laptop is governed the same way, under the developer's identity.

midPoint, the IGA of that stack, created sam, assigned it the [role]({{< relref "concepts/roles-and-access.md" >}}) `operator` and named alice as its sponsor, the person who answers for it. A [policy]({{< relref "concepts/policy-model.md" >}}) in Straza says what sam's roles may run.

| sam runs | Straza answers |
|---|---|
| `ls` | The command runs. A rule allows sam's shell. |
| `rm -rf build/` | `Straza: destructive command denied by the agent guardrails` |
| `./deploy prod` | `Straza: approval ticket … is pending. A person decides within 24 hours`, and the message says where. alice [approves]({{< relref "concepts/approvals-model.md" >}}) in the console, and the next run of the deploy goes through, once. |
| `ls`, after midPoint disabled sam | `Straza: this user is disabled, so Straza refuses this request.` |

The last answer took no change in Straza. An administrator disabled sam in midPoint, and Straza revoked sam's sessions. On the recorded run that took under two seconds.

Each of the four answers is a record on the [audit chain]({{< relref "concepts/evidence.md" >}}). [Try it on the demo stack]({{< relref "evaluate/try-it-on-the-demo-stack.md" >}}) runs the same commands on your machine.

## Where Straza decides


Straza checks an action in one of three places, and these docs call each place a lane. Two words first: strazad is Straza's server, and an MCP server is a connector that gives an agent tools, for example for GitHub or a ticket system.

| Lane | Can an agent get around it |
|---|---|
| The hook, on the agent's machine. Claude Code, Codex CLI and Gemini CLI call it before each tool runs: a shell command, a file write, a call to an MCP server that is not behind the gateway. The [Python kit]({{< relref "guides/govern-an-agent/python-agents.md" >}}) gives your own agent loop the same check. | No on a managed install, where only root or an administrator can change the hook. Yes where a user installed the hook, which is for trying Straza, and by default an enterprise server gives such an install no session. |
| The gateway, in strazad. Each call to an [MCP server]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}) that you registered in Straza goes through it, and an agent sees only the tools its roles grant. | No, as long as the MCP server's [credential]({{< relref "concepts/credentials.md" >}}) lives only in Straza. The gateway adds it to an allowed call, and the agent never holds it. |
| `straza exec`, for an agent that is none of the coding tools above. It checks the command it is given. | No inside the [sandbox image]({{< relref "guides/govern-an-agent/hookless-processes.md" >}}), where it decides each program outside a short allowlist. Yes on any other machine, where nothing makes an agent use it. |

[The security model]({{< relref "evaluate/security-model.md" >}}) says what Straza trusts, and [What each lane stops]({{< relref "evaluate/what-each-lane-stops.md" >}}) says how to test each lane.

## What you run


strazad is one static binary with two profiles. Enterprise is for a company. Standalone is for one person, a small team or a trial.

| | Enterprise | Standalone |
|---|---|---|
| Database | Postgres | SQLite |
| Deployment | Kubernetes with the Helm chart | One host, with nothing else to install |
| Sign-in | Your identity provider over OIDC, with guides for Entra ID, Okta and Keycloak | strazad's own sign-in |
| Role assignment | Your IGA over SCIM 2.0, for example midPoint or Okta. Entra ID over Microsoft Graph. An admin. | Usually an admin |
| A local action that no rule covers | Denied | Allowed |

On each machine where an agent works you install the `straza` client. It brings the hook for the coding tool and a background service that holds the person's key and decides each call. Admins work in the console that strazad serves, or with the CLI `strazactl`.

Roles are made in Straza, and the systems above assign them. [Identity and your IGA]({{< relref "evaluate/identity-and-your-iga.md" >}}) says which system checks what, and [Standalone and enterprise]({{< relref "reference/standalone-and-enterprise.md" >}}) lists every difference.


Straza is open source. The core is under AGPL-3.0-only. The code you build against, such as the specifications and the agent kits, is under Apache-2.0.

## What Straza is not


- Straza does not replace your IGA. Joiners, movers and leavers stay there or in Entra ID, and Straza applies the roles they assign.
- Straza does not sit between an agent and its model. It reads prompts and replies only when a policy turns on conversation recording at the hook.
- No AI model takes part in a decision. Policy decides, and where a rule asks for it, a person does.
- Straza decides the command that starts a program and not what the program then computes.
- Straza does not certify compliance. It enforces decisions and gives you the records to map to a control.

{{< next "evaluate/how-it-works.md" >}}
