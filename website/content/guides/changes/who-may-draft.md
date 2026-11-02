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
  platform: A throwaway standalone server in an Alpine container on Linux, with admin.secondPerson turned on in its config for the second-person checks. The token commands and their refusals ran, and so did the refused direct write, the pause, the coding-agent refusal of a publish, and a publish in headless Chromium by the person who minted the token that wrote the draft. An AI agent's call to an admin route did not run, because it needs a session token that no command hands to another client. A publish on a person's strazactl login did not run either, and strazactl no longer signs in or publishes
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


Neither an admin API token nor an AI agent publishes a draft, whatever it holds. A token can create and check one, and it keeps every direct write it has today, such as `strazactl roles create` run on it: each is a one-item draft the token publishes in the same request, so automation never waits. The publish refuses a token with `An admin API token carries no person and cannot publish a draft. It may create drafts. A person publishes them on the console under Drafts.`.

A person publishes in the console under **Drafts** and needs standing over every object in the draft. strazad refuses any session a coding agent checked in.

## What a covers line may say


On a server that pins an object type, such as ghapp with its repos, each role's access row carries a covers line. Whoever may draft the role may write either form, `covers: every` or a list of names. That holds for every drafter, an agent that drafts through `straza__draft_submit` included, and the check refuses the same mistakes for each, as [Propose and publish a change]({{< relref "guides/changes/propose-and-publish.md#covers" >}}) lists. What differs is who may publish the line.

A global admin, a person who holds `apps:write` such as a holder of `straza-global-mcp-admin`, publishes any covers line. A server admin publishes the covers line of a role their server owns only while nobody holds that role, as with its tools. Otherwise the publish refuses with `You cannot publish draft 41: the role ghapp-write-payments has 1 holder. Its tools and the repos it covers change only by the global admin or by a new role.` They may name repos, and they may write `covers: every` while `admin.serverAdminsCoverEvery` is true, its default. With the setting false, the publish refuses in words such as `On this deployment only a global admin may make ghapp-write-payments cover every repo, including the repos ghapp lists later. Name the repos it covers, or ask a global admin.` The setting never refuses a role that reaches every repo already, such as a role that reached the server before the draft that pins it.

While any role on a server names repos, two changes to the server's pin stay with a global admin, because each would widen those roles: removing the pin, and turning a tool such a role binds to reach none. A change of the pinned type is refused for everyone, global admins too, while a role names repos of the old type. It passes only when the same draft gives those roles `covers: every`, or removes their access rows on the server or the roles themselves, because a binding left with no covers line is refused. A server admin who removes the pin reads, for example, `Only a global admin may remove ghapp's pinned repos while a role names repos on it, because it would then reach every repo: ghapp-write-payments. Ask a global admin to publish this draft.` A reach that the draft makes stricter passes. When a tool comes to reach every repo, or only what the credential reaches, the gateway refuses it to every role on some repos, and the check warns once for each such tool that one of those roles binds: `After this draft, pr_create reaches every repo on ghapp, so ghapp-write-payments, a role on some repos, can no longer call it. Take pr_create out of that role, or make it cover every repo, in this draft or later.` The warning is part of the draft's check, in `strazactl drafts check`, `strazactl drafts create` and the draft's page on the console, so a person who re-pins a server through a draft reads it before publishing. A direct `strazactl apps install` of an existing server shows none, and neither does the answer to a publish.

## Require a second person


Set `admin.secondPerson: true` in `straza.yaml` when a risky change, one that widens access or removes a server, must be published by someone other than who wrote it. Like `admin.roleAreas`, it has no environment variable, and `GET /v1/admin/config` answers it as `admin.second_person`. While it is on, a publish whose check lists a risk is refused to anyone who wrote a revision of the draft, to the person who minted an admin API token that wrote one, and to the sponsor of an AI agent that wrote one. So the sponsor cannot accept an agent's draft that loosens a policy set binding that same agent. The author reads `You changed this draft, and this deployment needs a second person to publish a change that widens access (admin.secondPerson). Ask another administrator with standing over every object in it to review and publish it.` The minter's sentence starts `You minted the admin API token ci-drafts, which changed this draft`, and the sponsor's starts `You sponsor joe, which proposed this draft`, with your own names.


A direct admin write whose check lists a risk is refused to every caller, root included, and its sentence says to save the change as a draft and ask another administrator to publish it. With the setting on, even `strazactl apps remove` goes through a draft and a second person. A change whose check lists no risk publishes by its author, and `strazactl apps disable` pauses a server at once, because pausing stays outside drafts, so an incident response never waits for a second person.


A covers line that makes a role someone holds reach more objects, such as more repos on ghapp, widens access even when its tools stay the same, so while the setting is on its author cannot publish it. While nobody holds the role, the line widens nothing yet, and its author may publish it. To keep `covers: every` on a server's roles away from that server's admins, set `admin.serverAdminsCoverEvery` to false as well.


The setting leaves assignments immediate. A person can create a role nobody holds with an ungated access row, which widens nothing, and then assign it. Have your identity manager approve assignments to close that path.

Any credential of the person that an agent can read on that machine also publishes as that person. strazactl keeps none, because it runs on an admin API token, and a token never publishes. The publish route accepts an unexpired ID token, as every admin route does. On Linux the one the straza client signs in with stays inside `straza service`, which uses it once to enroll and keeps no copy.
