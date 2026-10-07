---
title: Delegated admin
description: Give a person admin rights over chosen areas of Straza, such as the audit chain or sessions, without the root role.
pagetype: how-to
weight: 100
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: A throwaway standalone server in an Alpine 3.20 container on Linux, with the map entry added to its config file and the server restarted, and the refused entries checked by booting a second server on each. The admin's strazactl ran on an admin API token minted in the console, and omar and mara signed in with strazactl login from their own home directories, so every command on the page ran, the unassign included. In headless Chromium the console's assign step and the role's Areas tab were clicked as the root admin, and omar's sign-in, sidebar and locked panel were read. The refusal of AI agents on admin routes was checked against the code and not run, because no command hands an agent's session token to another client
  date: 2026-10-06
applies_to: both
who: You, as the root admin
where: The server's config file, then the console or a terminal with strazactl
steps: true
modes: [console, cli]
mode_default: cli
keywords: delegated admin role areas roleAreas scopes
---


You give a person admin rights over one area of Straza, such as reading the audit chain or revoking sessions, as the root admin. You edit the server's config file, then assign the role in the console or with strazactl. At the end the person administers that area and no other, and the root role `straza-admin` stays the one spelling of full power.

A map in the server configuration, `admin.roleAreas`, says which slice of the admin plane a role administers. Your identity manager decides who holds the role, the same way it decides every other membership.


To let people administer one MCP server and nothing else, give them that server's admin role instead, as [Delegate one MCP server]({{< relref "guides/operate/delegate-one-server.md" >}}) describes. The two compose: a person may hold a mapped role and a server's admin role at once, and the server unions everything they hold.

## Pick the areas


A scope is an area and a verb, written `area:read` or `area:write`, and the verb follows the HTTP method of the request: a GET is a read and everything else is a write. The twelve areas are the same ones that admin API tokens use, so a scope list reads the same wherever you meet it. Every admin route belongs to exactly one area, and a route that no area covers is refused, never let through.

| Area | What it covers |
|---|---|
| `identity` | users, roles, assignments, devices, locks and knowledge packs |
| `sessions` | listing sessions and revoking them, one or all, which is the kill switch |
| `transcripts` | recorded conversation content, kept apart from every other area on purpose |
| `audit` | the decision and admin event history, content free |
| `policy` | PolicySets: read, write, validate, simulate and activate |
| `apps` | MCP servers, access rows, secrets, health and the tool catalog |
| `drafts` | drafts of changes to MCP servers, roles, access rows and policy sets: `drafts:read` lists and reads them, and `drafts:write` checks, creates, changes, reverts, discards and publishes them, where a publish also needs a person with standing over every object in the draft, as [Who may draft and publish]({{< relref "guides/changes/who-may-draft.md" >}}) explains |
| `approvals` | pending approval requests, approval channels and enrolled approver phones |
| `config` | posture reads, the overview, sinks and their replay, the attestation hash registry |
| `changes` | the change feed a pull connector reads |
| `scim` | the SCIM plane at `/scim/v2`, where an identity manager writes users and role membership; `scim:read` opens the reads and the discovery documents, `scim:write` the writes |
| `tokens` | admin API tokens, included in no other scope |


Three scopes deserve a second look before you hand them out:

- `tokens:write` mints admin credentials, and a holder can mint a token with full scope. It is equal to root, and the server logs a warning at boot for every role that receives it.
- `sessions:write` revokes sessions, which stops agents mid-work.
- `scim:write` writes role membership, `straza-admin` included, so it sits at the same tier as `identity:write`.

The word `full` is not a scope you can write in this map. An entry for `straza-admin`, `straza-global-mcp-admin` or `straza-draft-config` refuses to boot, because each would blur a meaning the product fixes.

## Map the role


The map goes in `straza.yaml` on the server. It has no environment variable, because a map does not fit one, so on both profiles you edit the file. The enterprise demo stack ships this entry, which gives anyone holding the role `auditor` a read-only slice of the server:

```yaml
admin:
  roleAreas:
    auditor: [audit:read, sessions:read, transcripts:read]
```


Restart strazad after the edit. The server validates every entry when it starts and refuses to run on a bad one, naming the role and the scope in the error, so a typo cannot leave a delegation silently inert. In the Helm chart the file is the `configYaml` value, rendered into a ConfigMap, and a change to it rolls the pods for you.

## Give someone the role


The role itself is an ordinary role. You create it with `strazactl roles create <name> --kind straza`, and your identity manager then sees it as a SCIM group and writes its membership, where each membership write is an assignment. The console does not make this kind of role, and its New role page says `Straza roles are not made here.` The demo stack's seeder creates `auditor` as a Straza role, the control plane kind, which opens the console and never any agent tool. Assign it to a person as the root admin.


{{< console >}}
{{< clicks "Roles" "auditor" "Assign to a user" >}}

{{< shot name="role-assign-user" caption="The same dialog on the approver role release-approvers, with lena picked under User and the button Assign role." >}}

Pick omar under **User** and press **Assign role**. The role's **Areas** tab shows what it opens: `audit`, `sessions` and `transcripts` at `read`, and every other area at `none`, under the line `Set in strazad's config (admin.roleAreas). The console reads it and cannot change it.`
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="administration, as the root admin" >}}
```sh
strazactl assign auditor --user omar
```
{{< /command >}}

```text
assigned auditor to omar
```

Run the same command a second time and the answer is `strazactl: assignment already exists`, which is how you learn that a role is already held.
{{< /cli >}}

Omar is a person the identity manager wrote into Straza, and he holds no other role.


Give the role to people. Only a person uses the admin API, so an AI agent or a service account is refused on every admin route, whatever role it holds. The refusal says that only a person can use the admin API or decide a request, and names the two ways around it: an admin API token that a person mints for automation, and the drafting tools of the built-in straza MCP server, which the role `straza-draft-config` lists.

## Sign in as the holder and probe both sides


The holder signs in through the same device flow as any user. On every request the server resolves the session's roles, unions their scopes through the map and checks the area of the route. Inside the area everything works as it does for root.


{{< console >}}
Omar signs in at `/console/` with **Sign in with a code**. His sidebar holds **Sessions**, **Audit** and **Transcripts**, the three areas his role reads, and no other.
{{< /console >}}

{{< cli >}}
Omar signs in with `strazactl login --server`. This is omar, holding only `auditor`, listing sessions, trimmed to the first three rows:

{{< command terminal="Terminal" purpose="as omar" >}}
```sh
strazactl sessions list
```
{{< /command >}}

```table
ID                                    USER   HARNESS                         CLIENT                ATTESTATION  WIRING      STATUS   LAST SEEN
01a11331-3860-7408-94a6-ea1fbbb8cbb5  omar   strazactl/v1.1.0-117-g106081a8  v1.1.0-117-g106081a8  none         -           active   2026-10-06 21:49:15
01a11330-c0b1-7ddc-8fd6-99bc446e34e0  mara   claude-code                     v1.1.0-117-g106081a8  advisory     unmeasured  active   2026-10-06 21:48:44
01a11330-61e3-7c41-ac2b-4570d59264b7  carol  console/1                       -                     none         -           active   2026-10-06 21:48:23
```


Outside the area the server names the scope that is missing, and the command exits 1:

{{< command terminal="Terminal" purpose="as omar" >}}
```sh
strazactl users list
```
{{< /command >}}

```text
strazactl: session lacks scope identity:read
```

{{< command terminal="Terminal" purpose="as omar" >}}
```sh
strazactl policy list
```
{{< /command >}}

```text
strazactl: session lacks scope policy:read
```
{{< /cli >}}


{{< fails >}}
`requires role straza-admin, straza-global-mcp-admin or a role mapped in admin.roleAreas. The admin role of a server opens that server's own routes only.`
: None of the person's roles is in the map, so the delegation itself is missing, not one scope of it. Check the map entry, and that the person holds the role.
{{< /fails >}}

Every change a delegated admin makes lands on the audit chain under that person's own name, exactly as a root admin's would, so delegation never makes an admin action anonymous.

## What the console shows a delegated admin {.nostep}


The console sign-in page says `Admin roles only. Anyone else lands on the self-service page, still signed in.` After sign-in the sidebar shows only the areas the person's scopes cover, and an address outside them is answered by a locked panel that names what the account does hold and offers a button into the first area it can open. All of that is display only, because the server checks the authority again on every request, so a hand-edited console cannot reach a route that the map does not open.

## Undo it {.nostep}


To take the rights away from one person, delete their assignment of the role, or remove the membership in your identity manager. The next request from that person's session is refused. A membership the identity manager wrote is mastered there, so remove it there as well. That is the same move for an area role and for a server's own role.

{{< console >}}
On the person's sheet, press **Revoke** on the role's row, then confirm.

{{< clicks "Users" "omar" "Revoke" "Revoke role" >}}
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="administration, as the root admin" >}}
```sh
strazactl unassign auditor --user omar
```
{{< /command >}}

```text
unassigned auditor from omar
```
{{< /cli >}}

To retire an area delegation for everyone, remove the entry from the map and restart. The role then still exists and administers nothing. The kill switch that a `sessions:write` holder operates has its own guide, the [Kill-switch runbook]({{< relref "guides/operate/kill-switch-runbook.md" >}}).
