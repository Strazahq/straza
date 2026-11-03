---
title: Drafts
description: Why each change to servers, roles and policy waits as a checked draft until a person publishes it, and who may propose one.
pagetype: explanation
weight: 50
draft: false
summary: >-
  Every change to MCP servers, roles and policy is a draft. Straza checks the draft against live state, and its documents go live together or not at all. A direct save is a draft of one item, published at once. Any other draft waits for a person to publish it, even when an AI agent or a token proposed it.
noun: draft
keywords: draft propose publish check widens access second person agent draft_submit straza-draft-config apps directory undo discard
---


A draft holds the documents of one change to [MCP servers]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}), [roles]({{< relref "concepts/roles-and-access.md" >}}) or [policy]({{< relref "concepts/policy-model.md" >}}), which alters what agents may do. Straza checks each draft against live state and shows what it would change. A [person]({{< relref "concepts/identities.md" >}}) then publishes it. Its documents go live together or not at all, and the record of the publish names that person. A direct save, such as **Save and publish** in the console, is a draft of one item that Straza checks and publishes in the same request.

## What a draft goes through


A new draft is checked at once. The check sorts what it finds into lines that refuse the draft, lines that widen access, warnings, and what it could not check. It also shows, for each role the draft touches, what a holder gets now and after publishing. A refused line blocks the publish and says what to fix. Warnings do not block it.

The person who publishes confirms each line that widens access by ticking it. For a change that publishing the old state again cannot undo, they type its name instead. Straza checks the draft again as it publishes. If any object changed since the last check, nothing is written, and the draft must be checked again against the new live state.


A published change is taken back with a new draft of the earlier state, checked like any other. Discarding a draft changes nothing live.

## Who may draft and who may publish


| Who | May draft | May publish |
|---|---|---|
| A person with rights over servers, roles or policy, or a server's admin | What those rights let them read | Yes, with standing over every object in the draft |
| An admin API token with the drafts scope | Yes, such as a pipeline that sends a folder of files | Never a draft. Its direct change of one object goes live in the same request |
| An AI agent that holds the Straza role `straza-draft-config` | Yes, through two built-in tools | Never |
| The apps directory | One draft for each manifest file that appears, changes or goes | Never |


A session that a coding tool checked in may not draft through the admin routes or publish, because the agent inside that tool holds the session. A deployment can also require a second person. Then a draft that widens access or removes a server cannot be published by anyone who wrote a revision of it, by whoever minted a token that wrote one, or by the sponsor of an agent that wrote one. [Who may draft and publish]({{< relref "guides/changes/who-may-draft.md" >}}) sets these rules up.

## How an agent proposes a change


An [agent]({{< relref "concepts/identities.md" >}}) proposes a change through the built-in straza MCP server, with a tool that submits a draft and a tool that reads the draft's check. Only a session that holds `straza-draft-config` sees them in the gateway. The agent sends the same documents a person would, with a note for the reviewer. The answer names the draft and says that a person publishes it on the console under **Drafts** and the agent cannot. Every admin route refuses an identity that is not a person, so an agent never reaches the publish route, whatever role it holds.

Agents may not publish because an agent that could publish could widen its own reach. The check of an agent's draft refuses a change that would let the agent reach more than its roles reach today, at any moment one of its assignments starts or ends. It also refuses a draft that adds or widens a rule of a policy set that applies to the agent. For example:


| | wiki, invented | postgres, adaptive-pg-mcp |
|---|---|---|
| The agent holds | wiki-eng, which covers the space eng | postgres-sales-eu, which covers the database sales_eu |
| Its draft adds | the space legal to wiki-eng | the database sales_us to postgres-sales-eu |
| The check answers | the draft would let it reach tools on spaces of wiki that its roles do not cover today | the same, on databases of postgres |


An agent also cannot propose a Straza role, a command or container server, a Rego module, a rule that lets a requester approve their own calls, or a change to conversation recording. Without an active sponsor, an agent cannot propose anything, because an agent's draft needs a person accountable for it.


The check does not refuse an agent's draft that loosens a policy set that applies to the agent, for example by turning it off. On the console, it shows under a warning that names the agent, as a line that widens access. The publisher types the set's name to accept it, so the record of the publish names who accepted. While a second person is required, the agent's sponsor cannot publish such a draft.

## The apps directory


The Straza server watches a directory for MCP server manifests, so you can keep them in git. A file that appears or changes there becomes a draft, and a deleted file becomes a draft that removes its server. No file installs, changes or removes a server by itself. Straza never writes to the directory. If a file holds a secret, its draft is refused and keeps none of the file's bytes. Straza reads only server manifests from the directory. Which objects a role covers is set in the role's own document, which you propose as a separate draft. [The apps directory]({{< relref "guides/serve-mcp-apps/gitops-apps-directory.md" >}}) sets it up.

## What your IGA sees


[Your IGA]({{< relref "evaluate/identity-and-your-iga.md" >}}) reads each role as a SCIM group and never sees a draft. A role appears there once the draft that creates it is published, and the IGA cannot create, rename or delete a role. Each group carries a read-only list of what its role grants: the servers, the tools, the objects it covers and the live policy sets that name it. A publish that changes a role marks it as changed, so the IGA reads the group again at its next poll.

A role assignment is not a draft. When your IGA, or an admin on standalone, assigns a role, the assignment takes effect at once and never waits for a second person. An assignment that needs review belongs in your IGA's own approval, and on standalone in the admin's own process.

{{< details summary="What an expert needs here" >}}

A draft holds at most 1 MiB of documents and note, and at most 1,000 items. A proposer keeps at most 10 open drafts, and a note holds at most 2,000 bytes. An agent submits at most one draft every 10 seconds, and its open draft expires 14 days after its latest revision. A draft from the console, an admin API token or the apps directory does not expire, as [Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md#changes-and-drafts" >}}) lists.


Straza does not connect to an address that only a draft names, so the check of a new remote server cannot list its tools at first. A person may ask Straza to contact it once, with no credential and no redirect, and the audit chain records who asked.


While a second person is required, a direct write whose check lists a risk is refused to every caller, and its refusal says to save the change as a draft. Pausing a server does not go through a draft, so incident response never waits for a second person.


The agent reads its draft's refused lines, risks, warnings and unchecked lines, so it can fix a refusal itself and submit a new revision. The reviewer sees a note from an agent as the agent's own words, marked unverified.


Every create, check, publish, discard and expiry of a draft writes an audit record. The record of a draft from the apps directory names the file and its SHA-256 but no actor. Its publish record names the person who published it.
{{< /details >}}

{{< next "concepts/identities.md" >}}
