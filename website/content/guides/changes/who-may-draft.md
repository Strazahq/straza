---
title: Who may draft and publish
description: Decide who may draft a change to servers, roles, access rows and policy sets, who publishes it, and when a second person must.
pagetype: how-to
weight: 20
who: You, as the admin who delegates admin rights or runs automation that proposes changes
where: The config file and a terminal
draft: false
aliases:
  - /guides/operate/drafts-and-publishing/
tested:
  version: v1.1.0-117-g106081a8
  platform: A throwaway standalone server in an Alpine container on Linux, with admin.secondPerson turned on in its config for the second-person checks. The token commands and their refusals ran, and so did the refused direct write, the pause, the coding-agent refusal of a publish, and a publish in headless Chromium by the person who minted the token that wrote the draft. Two things did not run. A publish on a person's strazactl login needs a login that the coding-agent guard refuses in this session, and an AI agent's call to an admin route needs a session token that no command hands to another client
  date: 2026-10-06
applies_to: both
keywords: drafts publish second person secondPerson admin api token agent drafting tools
---


Decide who may draft a change to MCP servers, roles, access rows and policy sets, who publishes it, and when a second person must. The page is for the administrator who delegates admin rights or runs automation that proposes changes. Every such change can go through a [draft]({{< relref "reference/glossary.md" >}}), which strazad checks against live state and a person then publishes in one step.

## Who reaches the drafts routes


People who hold `apps`, `identity` or `policy` at a verb, or administer a server, reach the drafts routes at that verb without the `drafts` area. They draft only what their read grants show: a server needs `apps:read` or that server's admin role, a global role needs `identity:read`, and a policy set needs `policy:read`. Without `drafts:read`, a person lists only the drafts they wrote. A server admin also lists the drafts that touch nothing but their own server, and a holder of `apps:write` also the drafts that touch only servers and the roles servers own.


An admin API token reaches drafts only through the `drafts` area, whatever else it holds, such as a token that checks a folder of documents in a pipeline. Such a token needs both verbs to change a draft, because `strazactl drafts update` reads the draft before it sends the new revision. It also needs the read grant of every kind of object it drafts:

```sh
strazactl api-token create --name ci-drafts --scope drafts:read,drafts:write,apps:read,identity:read,policy:read --ttl 720h
```

Holding only one of the two verbs, the token is told what it holds and what the call needs, for example `This token holds drafts:write, and reading drafts needs drafts:read. Mint a token with the scopes drafts:read and drafts:write with strazactl api-token create.`


An AI agent never reaches the drafts routes. Only a person uses the admin API, so an agent or a service account is refused on every admin route, the drafts routes included, whatever role it holds. An agent proposes a change through the two drafting tools of the built-in straza MCP server instead, which the gateway lists only to holders of the role `straza-draft-config`.

## Who publishes


Neither an admin API token nor an AI agent publishes a draft, whatever it holds. A token can create and check one, and it keeps every direct write it has today, such as `strazactl roles create` run on it: each is a one-item draft the token publishes in the same request, so automation never waits. The publish refuses a token with `An admin API token carries no person and cannot publish a draft. It may create drafts. A person publishes them on the console or with strazactl.`, and strazactl adds the way forward: unset `STRAZA_API_TOKEN`, run `strazactl login`, and publish again.

The person publishes with `strazactl drafts publish 41` in their own terminal, or on the console, and needs standing over every object in the draft. Inside a coding agent, strazactl refuses that publish on the person's login before anything is sent, which stops an honest mistake, and strazad refuses any session a coding agent checked in.

## Require a second person


Set `admin.secondPerson: true` in `straza.yaml` when a risky change, one that widens access or removes a server, must be published by someone other than who wrote it. Like `admin.roleAreas`, it has no environment variable, and `GET /v1/admin/config` answers it as `admin.second_person`. While it is on, a publish whose check lists a risk is refused to anyone who wrote a revision of the draft, to the person who minted an admin API token that wrote one, and to the sponsor of an AI agent that wrote one. The author reads `You changed this draft, and this deployment needs a second person to publish a change that widens access (admin.secondPerson). Ask another administrator with standing over every object in it to review and publish it.` The minter's sentence starts `You minted the admin API token ci-drafts, which changed this draft`, and the sponsor's starts `You sponsor joe, which proposed this draft`, with your own names.


A direct admin write whose check lists a risk is refused to every caller, root included, and its sentence says to save the change as a draft and ask another administrator to publish it. With the setting on, even `strazactl apps remove` goes through a draft and a second person. A change whose check lists no risk publishes by its author, and `strazactl apps disable` pauses a server at once, because pausing stays outside drafts, so an incident response never waits for a second person.


The setting leaves assignments immediate. A person can create a role nobody holds with an ungated access row, which widens nothing, and then assign it. Have your identity manager approve assignments to close that path.

Any credential of the person that an agent can read on that machine also publishes as that person. strazactl's refusal inside a coding agent reads variables the agent can unset, so it stops an honest mistake and not an agent that means to act as its person, and the stored strazactl login then opens a strazactl session, which strazad accepts. The publish route also accepts an unexpired ID token, the one the straza client signs in with included, as every admin route does.
