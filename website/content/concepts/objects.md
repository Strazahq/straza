---
title: Objects
description: How a role can be limited to some of a server's objects, on any server that declares its objects.
pagetype: explanation
weight: 20
draft: false
summary: >-
  An MCP server often reaches many databases or repositories with a single credential. With
  objects, a role covers every object or only the ones it names. A call such as
  `read_rows(database: "sales_us")` then passes only if a role of the caller covers sales_us.
  Otherwise the gateway refuses the call before any policy rule runs.
noun: object
keywords: objects object scope covers role database repository listing type
---


An object is a thing that a server's tools act on, such as a database or a repository. Straza can limit a role to some objects of a server only when the server declares them.

## Why a role needs objects


A [role]({{< relref "concepts/roles-and-access.md" >}}) names the [tools]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}) its holders may call on one server. With a single credential, a server often reaches many things, such as every repository a GitHub App is installed on, or every database in the server's configuration. A role that holds a write tool could then write in all of them. Objects limit the role to the ones it names.

## What a server declares


A server declares one object type. For each tool it says which argument names the object, or that the tool reaches every object or none of them. A read-only tool lists the objects, and Straza calls it itself, with a credential kept only for that call when the server takes one.

{{< diagram name="objects-two-servers" caption="The wiki server is invented, so the idea does not lean on one product. postgres is adaptive-pg-mcp with its real databases and role, as the demo stack runs it with the profile pg." >}}


| | wiki, invented for this page | postgres, which runs adaptive-pg-mcp |
|---|---|---|
| Object type | space, plural spaces | database, plural databases |
| Tools that name one | `page_read` and `page_edit`, in the argument `space` | `read_rows`, `insert_rows` and 12 more, in the argument `database` |
| The tool that lists them | `space_list` | `list_databases` |
| Tools that reach every one | `space_list` and `search` | `list_databases` |


Pinning a declaration keeps a copy of it in Straza. A person publishes that copy in a [draft]({{< relref "concepts/drafts.md" >}}), and Straza decides with it. On the server's page in the console, the button named with the server's own word, such as **Pin databases**, makes that draft. What the server reports while it runs can only narrow a call, so a server cannot widen its own reach. If a tool later differs from the pinned copy, roles on some objects cannot use that tool until a person publishes a new copy. [When the server's tools change]({{< relref "guides/serve-mcp-apps/add-a-server.md#tool-changes" >}}) says when Straza notices.

## What a role covers


Once a server's object type is pinned, each role on that server says which objects it covers. A role on every object covers the ones the server lists today and the ones it lists later. A role that names its objects covers only those, and an object added later is not covered until a person adds it. Tools that reach every object at once, such as the list tool, belong only in a role on every object, because nothing limits them to some objects.


Straza keeps each named object with its id from the list, so a role still covers an object that is renamed. An object deleted and made again under the old name is a new object. A role that named the old one does not cover it, but a role on every object does. When a server with roles is pinned for the first time, those roles cover every object, so nobody gains or loses access at that moment.

## How the gateway checks a call


1. Straza finds the tool among the tools the session's roles reach.
2. It applies the server's rate limit.
3. It reads the object the call names and looks for a held role that covers it.
4. The [policy]({{< relref "concepts/policy-model.md" >}}) decides the call.
5. If a rule asks, a person [approves]({{< relref "concepts/approvals-model.md" >}}) it, and the approval applies only to that object.


The object check runs before the policy, so no rule can allow a call the check refused, and nobody is asked to approve one. The [agent]({{< relref "concepts/identities.md" >}}) gets a message that names only the objects its own roles cover:

```text
Straza: page_edit on legal was refused: none of your roles on wiki covers that space. Your roles cover page_edit on eng. Check the space's name, or ask an admin for a role that covers it.
```

A name that no role covers and a name the list does not hold get the same answer, so an agent learns nothing about objects it cannot use.

## What your IGA sees


Each role appears in [your IGA]({{< relref "evaluate/identity-and-your-iga.md" >}}) as a SCIM group that says what it covers, in the form the table shows. A certifier can read these values but cannot change them.

| Role | Covers, as your IGA shows it |
|---|---|
| wiki-eng | `wiki:space "eng"` |
| a role on every space | `wiki:space every` |
| postgres-sales-eu | `postgres:database "sales_eu"` |


[midPoint]({{< relref "guides/connect-identity/midpoint.md#check-the-imported-roles" >}}) shows these values in the Covers column of its Straza application roles.

## Where objects are checked


Only the gateway checks objects. A local tool that a hook decides names no object. The server makes three promises that Straza cannot check: a call touches only the object its argument names, the name means exactly one object at the moment of the call, and the server accepts calls only from the gateway, or checks a credential that only the gateway holds. [Object marks for server authors]({{< relref "reference/object-marks.md#promises" >}}) says how a server keeps them, and [Limit a role to some objects]({{< relref "guides/serve-mcp-apps/limit-a-role-to-some-objects.md" >}}) shows the setup.

{{< details summary="The four reaches a pinned tool can have" >}}

The reach of a tool decides which held roles may call it.

| Reach | What the tool does | Which held role may call it |
|---|---|---|
| Argument | Names one object in an argument | A role that covers that object |
| None | Touches no object, such as a tool that says who the caller is | Any role that may call the tool |
| All | Can reach every object at once, such as a search across them | Only a role on every object |
| Credential | Names no object and leaves the limit to the credential the call carries | Only a role on every object, because Straza does not yet check what a role's own credential reaches |

A tool the pinned copy does not name counts as reach all until a person decides, so a role on some objects cannot call a tool the server adds later until a person has decided what it reaches. Drafts refuse a tool of reach all or credential in a role on some objects, before anyone can publish it.
{{< /details >}}


{{< details summary="Edge cases an expert asks about" >}}

An object created a minute ago, before any listing has seen it, is covered at once by a role on every object. A role that names its objects does not cover it until a person adds it. Straza lists each server every 10 minutes and keeps the last good list. Every replica holds the list in memory, so a decision never reads the database for it. When a refused call names a value the list does not hold, Straza asks for a listing sooner, at most once a minute per server.


Straza tries the held roles in name order, and the first role that covers the object grants the call. The [audit record]({{< relref "concepts/evidence.md" >}}) and the credential lookup name that role. A refusal lists at most ten covered objects, then how many more there are.


Straza refuses a call whose object argument it cannot read, such as a missing argument or one that is not a string. This refusal applies to every role, including a role on every object. After the first pin, every approval names the object, so an approval given before the pin is asked for again. When a role on some objects grants a call, Straza sends the object's id from its list along with the call. The server can then refuse an object that was deleted and made again under the same name.


An access row on a pinned server that says nothing about objects covers nothing, and a draft refuses to add one. Removing the pin makes every role on that server reach every object again. A server's own admin may give a role every object unless the deployment reserves that for a global admin. An operator can turn the object checks off for a deployment. Then only roles on every object may call a tool that names an object, so turning the checks off never widens access.
{{< /details >}}

{{< next "concepts/policy-model.md" >}}
