---
title: Roles
description: You read what a role gives and who holds it on the role's own page, and you change its access, its composition and its holders from there.
summary: >-
  Each role has a page that shows what holding it gives and who holds it. From there you change
  what an application role reaches, compose a business role and give an approver or Straza role
  to a person. When your IGA writes membership over SCIM, a change you make here is drift.
pagetype: how-to
weight: 20
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: A throwaway standalone server on Linux with the MCP reference server registered as demo-tools, read and edited as the admin in headless Chromium. Assigning from a role's page and composing were set up with strazactl and read back in the console, and no role was saved from the New role wizard
  date: 2026-10-06
  except: assigning and composing in the console, set up with strazactl instead, and saving from the New role wizard
applies_to: both
who: Admin
where: Console
steps: true
keywords: console roles role page access edit access compose holders assign areas new role delete
---


Roles is where you read what holding a role gives a person or an agent. Straza has four kinds of role. An application role reaches tools on one MCP server, a business role composes other roles into one assignment, an approver role decides approval requests, and a Straza role administers Straza itself. Every role has its own page, and the page offers what fits its kind. [Roles and access]({{< relref "concepts/roles-and-access.md" >}}) explains the kinds, and the task guides linked below walk each change in full.

## Before you start {.nostep}


- A console sign-in that holds the `identity` area, which opens Users and Roles. The Reach and Tools columns also read the MCP servers, which needs `apps:read`, and a column your session may not read says so in its cells.
- When your identity manager writes role membership over SCIM, it masters who holds each role. Each assignment records where it came from: `scim`, `admin`, or `entra` for [the role's group in Entra]({{< relref "guides/connect-identity/entra-role-groups.md" >}}). A change you make here lasts until its next reconciliation.

## Find a role


{{< clicks "Roles" >}}

{{< shot name="demo-roles" caption="Roles on the demo stack, with a chip per kind and what each role reaches." >}}

The list opens on **All roles**, with a chip for each kind and its count: **Application roles**, **Business roles**, **Approver roles** and **Straza roles**. Search by name or description. The **Reach** column says what holding each role gives, in words such as `demo-tools: echo and get-sum`, `Includes demo-tools-readers`, `Decides approval requests`, `Administers demo-tools` or `All console areas`. **Holders** counts the people and agents who hold the role, directly or through a business role, and **Policies** counts the policies that name it.

Select a row to open the role's page.

## Read a role's page


The head shows the name, a badge with the kind, such as `application role` and `owned by demo-tools` for a role that belongs to a server, and the description. A line under it names the last publish, such as `Last published by admin at 2026-10-06 20:05:31 UTC, in draft 11.`, because a change to a role goes through a draft, even one made in a single step.

The tabs depend on the kind.

| Kind | Tabs |
|---|---|
| Application role | **Access**, **Holders**, **Policies** |
| Business role | **Composes**, **Holders**, **Policies** |
| Approver role | **Holders**, **Policies** |
| Straza role | **Areas**, **Administers** when it is a server's admin role, **Holders**, **Policies** |

The buttons in the head follow the kind as well. An application role offers **Edit access** when it belongs to a server or has an access row, a business role offers **Compose a role**, and an approver or Straza role offers **Assign to a user**. **Export YAML** downloads the role as the document `strazactl roles export` prints. **Delete role** is missing on a role the product made, such as `straza-admin`, and on a server's admin role, which goes with its server.

## Change what an application role reaches


{{< clicks "Roles" "demo-tools-readers" "Edit access" >}}

{{< shot name="role-edit-access" caption="A tool's row under **Choose per tool**, here `get-sum` of scout-tools-readers set to **Needs approval**." >}}

The sheet is titled `Edit demo-tools-readers's access to demo-tools`. **Which tools** chooses what the access row stores: **Only the tools you tick**, **Every tool it has today**, or **Every tool, and tools added later**. **On a call** chooses what a call does: **Allow every call**, **Require approval for every call**, or **Choose per tool**, which puts `Allowed`, `Needs approval` and `Denied` on each tool's row. A sentence under the choices says the result in words, such as `Holders of demo-tools-readers reach 2 of 14 tools of demo-tools. No call needs approval from this role.`


On a server that pins its objects, such as ghapp with its repositories, the sheet asks one more question under the tools, in the server's own words: **On which repos**. **Every repo** covers every repository the server lists, today and later, and its line gives today's count once Straza has listed them. **These repos** reads `Pick from ghapp's list. A repo added later stays out until you add it.` It opens the list Straza last fetched, with `Filter repos` to search it, and you tick each repository, so no name is ever typed. The picked ones show above the list, and **View as YAML** holds the access row as a role file writes it. While **These repos** is chosen, a tool that can reach every repository, such as `repo_list`, cannot be ticked, and its row says why. The role's **Access** tab then shows the answer in an **On which repos** column, such as `2 repos: Strazahq/pay-api and Strazahq/pay-web`. [Access per role]({{< relref "guides/serve-mcp-apps/catalogs-per-role.md#covers" >}}) explains both answers and what a draft refuses.

**Save draft** keeps the change in a draft, and **Save and publish** makes it live. The policy rules you pick here go into the role's own policy, named `demo-tools-readers-access`. [Access per role]({{< relref "guides/serve-mcp-apps/catalogs-per-role.md" >}}) walks this change end to end.

## Compose a business role


{{< clicks "Roles" "developer" "Compose a role" >}}

Pick a role in **Pick a role to compose**, then press **Compose role**. A business role composes application roles and the admin roles of MCP servers, so a person who needs several servers holds one role. The **Composes** tab lists what the role composes, with **Remove** on each row, and **What holders reach** lists the servers and tools that arrive through them, such as `2 tools via demo-tools-readers`. Holders gain or lose them within seconds, or at their next check-in without a connected push stream.

## Give the role to a person


The **Holders** tab lists everyone who holds the role, directly or through a business role, with the columns of Users: kind, sponsor, roles, source, last seen and status. A row opens the user's page.

An approver or Straza role is given from its own page. Press **Assign to a user**, pick the person under **User**, and press **Assign role**. An application or business role is given from the person's page under Users. Press **Assign a role** on the **Access** tab, pick the role, and press the button that names it.

{{< clicks "Users" "alice" "Assign a role" "dev" "Assign dev" >}}

{{< shot name="alice-assign" caption="alice's page under Users, with `dev` under her **Roles** and **Assign a role** beside the heading." >}}

The page then says `dev assigned to alice. Sessions with live updates get it in seconds, others within 5 minutes.` For a user your identity manager provisioned, the page and the dialog say `Your identity manager owns these assignments. A change made here lasts until its next sync.` Make that change in the identity manager for it to stay.

## See which areas a Straza role opens


{{< clicks "Roles" "straza-enroll-browser" "Areas" >}}

{{< shot name="demo-role-areas" caption="The **Areas** tab of the demo stack's `auditor` role, with the level it holds in each area." >}}

The **Areas** tab lists the twelve admin areas, from `identity` to `scim`, with what each covers and the level the role holds: `none`, `read` or `read+write`. Its map comes from `admin.roleAreas` in strazad's config, and the tab says `Set in Straza's config, admin.roleAreas.` [Delegated administration]({{< relref "guides/operate/delegated-admin.md" >}}) sets the map and makes the Straza role it names.

## Make a new role


{{< clicks "Roles" "New role" >}}

{{< shot name="new-role-1" caption="The first step for an application role on demo-tools, with the stored name and the name midPoint sees." >}}

The wizard asks one thing per step. For an application role the steps are **Kind, server and name**, **Access**, **Review** and **Done**. Pick **Application role**, **Business role** or **Approver role**, and for an application role pick its server. The name of a server's role starts with the server's name, which the box fixes before your word, and tags under it show the stored name and the IGA's name, such as `stored as demo-tools-video` and `identity manager: AR:demo-tools-video`. **Review** ends with **Save draft** and **Save and publish**.

{{< fails >}}
`No MCP server with tools is running. Add one under MCP servers first.`
: The wizard makes an application role on a server only. Add the server first, as [Add a server]({{< relref "guides/serve-mcp-apps/add-a-server.md" >}}) shows. An application role with no server, which policy rules match by name, comes from `strazactl roles create <name> --kind application`. A full administrator can also make one with its policy, as **A new role** in the New policy wizard of [Policies]({{< relref "guides/console/policies.md#build-a-policy-with-the-wizard" >}}).

`Straza roles are not made here.`
: The product makes `straza-admin` and the enroll roles. A delegated admin role comes from `strazactl roles create <name> --kind straza` and its line under `admin.roleAreas`.

`Names beginning with straza- are reserved for product-defined roles. Pick a name without the prefix.`
: Choose a name that does not start with `straza-`.
{{< /fails >}}

## Delete a role


**Delete role** asks first, and the question says who loses the role and what goes with it, such as `Nobody holds it. Your identity manager's writes to this role start failing.` Its assignments and access rows are deleted with it. A live policy that names only this role is turned off with the delete, and the message after the delete names it.

## Next {.nostep}

- [Access per role]({{< relref "guides/serve-mcp-apps/catalogs-per-role.md" >}}) gives a role a server's tools and gates them in policy.
- [Policies]({{< relref "guides/console/policies.md" >}}) walks the area where the rules that match a role live.
- [Propose and publish a change]({{< relref "guides/changes/propose-and-publish.md" >}}) follows a saved draft until it is live.
