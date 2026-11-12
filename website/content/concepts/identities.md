---
title: People, agents and sponsors
description: Who a governed action belongs to, how Straza tells a person from an AI agent, and which person is accountable for an agent.
pagetype: explanation
weight: 60
draft: false
summary: >-
  A person is a human who signs in. An AI agent either runs as its person, as a coding agent
  does, or is its own user with a key instead of a password. A sponsor is the person
  accountable for an agent, and decides its held calls. Your IGA sends them, or an admin
  creates them on standalone.
noun: [person, agent, sponsor]
keywords: identity person user agent nhi service account sponsor scim usertype agency mode accountable
---


Every governed action belongs to a [session]({{< relref "concepts/machines-and-sessions.md" >}}), and every session belongs to a single user of Straza. That user is a person or an AI agent. Straza applies the agent rules below to a service account, a technical account with no agency of its own. A sponsor is the person Straza records as accountable for another user.

## A person


A person is a human user. On enterprise they sign in at your [identity provider]({{< relref "evaluate/identity-and-your-iga.md" >}}), and on standalone strazad signs them in with a password. Only a person can enroll a [machine]({{< relref "concepts/machines-and-sessions.md" >}}), open the console or the self-service page, and decide an [approval]({{< relref "concepts/approvals-model.md" >}}). An agent's own sign-in is refused on the admin API. Straza checks these rights on the user itself and ignores the user's [roles]({{< relref "concepts/roles-and-access.md" >}}), so an admin or approver role assigned to an agent by mistake gives it none of them.


On enterprise Straza refuses a person's sign-in at the identity provider until your IGA has sent that person over SCIM. On standalone the first start creates the admin `admin` and prints its password once, and an admin creates everyone else. [Identity and your IGA]({{< relref "evaluate/identity-and-your-iga.md" >}}) explains which system checks what.

## An agent

Straza governs an agent that runs as its person and an agent with its own identity.


| | An agent that runs as its person | An agent with its own identity |
|---|---|---|
| Example | A coding agent such as Claude Code, Codex CLI or Gemini CLI on the person's laptop | A worker in CI, or an agent that runs with no person at the keyboard |
| Who it is in Straza | The person. It has no user of its own | A user of its own, typed agent or service |
| How it proves itself | Through the person's enrolled machine and its key | With its own Ed25519 key that an admin registered, or its own client at your identity provider |
| Whose roles apply | The person's | Its own |
| Who decides a held call whose rule names no approver role | The person, or the person's sponsor when they have one | Its sponsor. With no sponsor the call is denied at once |


An agent that runs as its person needs no user of its own, in your IGA or from an admin on standalone. Its sessions carry the person's identity and name the coding tool, so every decision and audit record shows both. Because such an agent can reach everything the person can on that machine, a person decides their own request, by default, only with a signature from a phone or browser they enrolled.


An agent with its own identity has no password and uses no browser. The Straza service on the machine where it runs keeps its key or its client secret and checks the agent in at each session start, as [Machines and sessions]({{< relref "concepts/machines-and-sessions.md#an-agent-with-its-own-identity" >}}) explains. Straza registers an agent's key only for an agent and enrolls a machine key only for a person, so neither kind of key can be used in place of the other.


On enterprise your IGA sends an agent as a SCIM user. Straza stores it as an agent when the user carries the IETF agent extension of SCIM, or when its user type is agent or service. A user sent with neither becomes a person, with everything a person may do. On standalone an admin creates the agent with its type and then registers its key. [AI agents as identities]({{< relref "guides/connect-identity/nhi-provisioning.md" >}}) shows what your IGA sends.

## A sponsor


A sponsor is the person accountable for an agent, or for any user who has one, such as a contractor. Straza keeps the sponsor as a username on the user. Your IGA sends the sponsor, or it sends the agent's manager and Straza makes that manager the agent's sponsor. When a held call's rule names no approver role, the request goes to the requester's sponsor. Straza reads the sponsor when the request is raised and keeps it on the request, so a later change of sponsor never moves an open request.


A usable sponsor is an active person other than the requester. An agent cannot sponsor an agent, because that leaves no person accountable.


When an agent has no usable sponsor and the rule names no approver role, Straza denies the call at once, because a request that nobody is told about could only expire. The reason names the cause and the fix. Set the sponsor before the agent goes to work.


A person needs no sponsor. When a rule names no approver role, a person with no sponsor decides their own request, and a person with a sponsor waits for that sponsor. [Approvals]({{< relref "concepts/approvals-model.md" >}}) covers who decides in full.


These rules do not depend on the identity provider. User type, agency mode and sponsor arrive from your IGA over SCIM, or from an admin on standalone, whether people sign in with Entra ID, Okta or Keycloak. Straza stores the sponsor name as it arrives and checks it only when a request needs it. The sponsor must therefore be the username of an existing user. A name that matches nobody denies the agent's first held call, and the reason says that the sponsor is not known to Straza. A manager is checked when it arrives instead. Straza refuses a `PATCH` that names a manager it does not know. Straza still saves a create or `PUT` with such a manager, keeps the agent's sponsor as it was and logs the warning `SCIM manager not applied`. So your IGA sends the manager before the agent, or sends the agent again once the manager exists.

{{< details summary="What your IGA records on each user" >}}

Besides the sponsor, your IGA, or an admin on standalone, keeps the attributes below on a user, and they can change at any time. Straza reads them again whenever a session checks in or renews. A [policy]({{< relref "concepts/policy-model.md" >}}) can match on each.

| Attribute | Values |
|---|---|
| User type | `human`, `agent` or `service` |
| Agency mode | `interactive`, `supervised` or `autonomous` |
| Swarm ID | An optional name for a fleet of agents |


SCIM also returns `kind`, either `human` or `nhi`. Only the agent extension sets it, and only when the user is created. An agent that your IGA typed by user type alone therefore shows `kind` `human` over SCIM, while Straza treats it as an agent on every route. A certifier who sorts people from agents reads `userType`.
{{< /details >}}

{{< next "concepts/machines-and-sessions.md" >}}
