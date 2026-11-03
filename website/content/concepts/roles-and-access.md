---
title: Roles and access
description: What a role lets its holders do, and who makes and assigns roles.
pagetype: explanation
weight: 10
draft: false
summary: >-
  A role says what its holders may do. All roles are made in Straza. An application role grants
  tools on one MCP server, a business role groups application roles, an approver role may
  decide held calls, and a Straza role carries rights in Straza itself. Your IGA, or an admin
  on standalone, decides who holds each role.
noun: role
keywords: role roles access application business approver straza assignment scim group catalog
---


A role is a named set of rights that [people and agents]({{< relref "concepts/identities.md" >}}) hold. Straza decides each tool call from the session's roles.

## What a role grants on a server


An application role reaches one [MCP server]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}) through one access row. The row names the tools its holders may call there: tools named one by one, every tool the server has today, or every tool including the ones it adds later. A tool the row names runs unless a [policy]({{< relref "concepts/policy-model.md" >}}) denies the call or holds it for a person. No allow rule is needed, so rules are written only for the tools that need a deny or a person's approval.


On a server that declares its [objects]({{< relref "concepts/objects.md" >}}), the same row also says which objects the role covers. The demo stack's role postgres-sales-eu reaches seven tools of the server postgres, such as `read_rows`, and covers only the database sales_eu.


A session sees a tool only when the server lists it, the server's manifest exposes it, and one of the session's roles has an access row that names it. Policy then hides the tools it denies. If none of the session's roles reaches a server, a call to one of that server's tools gets the same unknown-tool answer as a misspelled tool name. An agent cannot learn which tools it was not given. A missing access row also looks like a missing tool, and it gives no deny reason.

## The kinds of role


Each role has a kind, set when the role is made.

| Kind | What its holders get | On the demo stack |
|---|---|---|
| Application | The tools of one server. One that reaches no server only picks which policy applies | postgres-sales-eu |
| Business | Everything the roles it composes grant, such as one application role per server | analyst, which composes postgres-sales-eu |
| Approver | The right to decide a held call when a rule names the role. No tools | sec-approvers |
| Straza | Rights in Straza itself, and no tools on an MCP server | straza-admin, and mcp-admin-postgres for the server postgres |


A session holds its own roles and every role they compose. A person who works with three servers can therefore hold a business role, and policy names the application roles it composes. An approver role composes no other role, and no role composes it. That way the people who may decide are exactly its direct holders, the list that [your IGA]({{< relref "evaluate/identity-and-your-iga.md" >}}) certifies or an admin keeps on standalone. [Approvals]({{< relref "concepts/approvals-model.md" >}}) explains who decides when a rule names no approver role.


There are no access rows on the agent's own machine. A hook there decides each local tool call, such as a shell command, by policy alone. Policy sets select sessions by application role, so the session's roles still pick which rules apply. An application role that reaches no server exists for this case. It has no access row and only names a set of people and agents for policy.

## Where roles come from


Roles are made in Straza, and your IGA cannot create them. A global admin can make any role. A server's own admin makes the roles of that server, and each such role belongs to the server. Its name starts with the server's name and a hyphen, it reaches only that server, and it lists its tools by name. Only a global admin may give a role every tool including the ones a server adds later. Straza makes some roles itself, such as straza-global-mcp-admin at each start when it is missing, and an admin role for each server it adds.


Every change to a role, including its access row and the roles it composes, is a [draft]({{< relref "concepts/drafts.md" >}}). An agent may propose one, which waits until a person publishes it.

## Who decides who holds a role


On enterprise, your IGA assigns roles. Each role appears there as a SCIM group, and membership of that group is the assignment. The group also carries a read-only view of what the role grants: its server, its tools, the policies that name it and the objects it covers. A certifier who reviews a business role therefore sees the reach of the roles it composes on the business role itself. On standalone there is no IGA, and an admin assigns roles in the console or the CLI.


Roles do not depend on the identity provider that checks sign-in. They work the same when people sign in with Entra ID, Okta or Keycloak, because role membership comes from your IGA over SCIM, or from an admin on standalone.


A new assignment reaches a running session when it next checks in, as [Machines and sessions]({{< relref "concepts/machines-and-sessions.md" >}}) explains. The gateway then tells the agent's client that its tool list changed, and the client fetches the list again without a restart. Before any agent starts, an admin can preview what a role or a person would see, with a reason for each tool. [Access per role]({{< relref "guides/serve-mcp-apps/catalogs-per-role.md" >}}) shows how to give a role its access and read that preview.

{{< details summary="What an expert asks about roles" >}}

Role names that start with straza- or mcp-admin- are reserved for the product, and nobody can make one. A role's kind never changes after it is made. To change it, make a role of the new kind, move its holders there, then delete the old one.


An application role has at most one access row, so it reaches one server. A role made without a server cannot be given access to one later. Make a role of that server instead, and compose both into a business role. A role that belongs to a server composes no other role. People who need two servers hold a business role that composes one role of each.


Policy selects sessions by application role only. The check of a policy set refuses any other kind of role there, and for a business or approver role its message names the fix. A rule that asks a person names approver roles or straza-admin.


Deleting a role ends every assignment of it and removes its access row. It also turns off each live policy set that matched that role alone, because such a set would match nobody. Straza refuses the delete while a live policy set names the role as an approver. It never deletes a role whose name starts with straza-, so to take such a role from a holder, remove the assignment.


A role can also carry a [knowledge pack]({{< relref "guides/govern-an-agent/knowledge-packs.md" >}}), text that every session of the role starts with.
{{< /details >}}

{{< next "concepts/objects.md" >}}
