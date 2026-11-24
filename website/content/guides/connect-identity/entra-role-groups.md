---
title: Role groups in Entra
description: Straza syncs with Microsoft Entra ID over Microsoft Graph. It creates one security group in Entra for each role, assigns the role to the members of that group, and creates a Straza user or agent for each member.
seo_title: "Sync Straza with Microsoft Entra ID over Microsoft Graph"
summary: >-
  Straza syncs with Microsoft Entra ID over Microsoft Graph. It creates one security group in
  Entra for each role and reads its members. A member gets a Straza user or agent and the role,
  a removed member loses the role, and an account disabled in Entra is disabled in Straza. You
  register a second app in Entra and set identity.entra.
pagetype: how-to
weight: 33
draft: false
tested:
  version: v1.0.0-1967-ge0413d02
  platform: Not walked. Written on 2026-11-17 from the working tree at that version, in which people are created, disabled and enabled. That part ran on the eval stack against the Entra lab tenant on 2026-11-17. The Graph answers it quotes come from runs against an Entra lab tenant with no paid plan on 2026-11-16 and 2026-11-17, where a script signed in as a service app that held Group.Create, GroupMember.Read.All and AgentIdentity.Read.All, and for part of the first run also User.ReadBasic.All and User.Read.All. The sync ran against that tenant on 2026-11-17 for one role group and three people, and its tests run against a stand-in for Microsoft Graph built from the lab's recorded answers and, for the feed of changed users, from Microsoft's documentation. Agents from Entra ran on the eval stack against that tenant on 2026-11-17 for a takeover, the sponsor set, cleared and set again, a disable and an enable of the agent user and of the agent identity, a rename and the end of the role. A deleted agent identity, a nested group and three of the four cases without a sponsor did not run there, and the Graph answers their sections quote come from a lab run of 2026-11-17 with the same service app
  date: 2026-11-17
  except: every step from the restart on, because nobody has walked this page as it is written
applies_to: both
who: Entra admin and Straza admin
where: Microsoft Entra admin center, the strazad config file, and a terminal with strazactl and curl
steps: true
keywords: entra sync microsoft graph iga role group security group membership sync service app certificate removal guard stale people create disable enable user.read.all agent agentusers agentidentities sponsor agentidentity.read.all not-agent
---


With this sync, Entra can do the IGA's job for Straza. Entra decides who exists in Straza and who holds each [role]({{< relref "concepts/roles-and-access.md" >}}). When you add a person or an agent to a role's group in Entra, they get a Straza user or agent and the role. Remove them, and they lose the role. Disable an account in Entra, and Straza disables it too. Your Entra admins assign Straza roles where they already manage groups.

The sync has run against one Entra lab tenant, with people and with agents. A deleted agent identity, a nested group and three of the four cases without a sponsor have not run there.

## Before you start {.nostep}


- An Entra account that may register applications and grant admin consent to Microsoft Graph application permissions.
- A Straza admin API token with the scope `full` in `STRAZA_API_TOKEN`. The certificate step needs a full administrator.
- The decision which accounts Straza takes from Entra: people, agent users, agent identities.
- Write access to strazad's config file, and a restart of strazad.

## Register the service app


Straza signs in to Microsoft Graph as an app registration of its own. It is separate from the app of [Entra ID login]({{< relref "guides/connect-identity/entra-login.md" >}}), which signs people in. This app holds a certificate and Graph permissions.

In the Microsoft Entra admin center, open **App registrations** and press **New registration**.

1. Name it `Straza service`.
2. Under **Supported account types**, pick **Accounts in this organizational directory only**.
3. Leave **Redirect URI** empty and press **Register**.

Copy the **Application (client) ID** and the **Directory (tenant) ID** from its **Overview** page. The config in step 4 needs both.

{{< picture src="pictures/entra-role-groups/app-overview.png" alt="The Essentials block of the service app's Overview page in the Entra admin center, with the Application (client) ID and the Directory (tenant) ID" caption="The two IDs on the Overview page of a lab app." >}}

## Grant the permissions


Open **API permissions**, press **Add a permission**, pick **Microsoft Graph**, then **Application permissions**, tick `Group.Create`, `GroupMember.Read.All`, `User.Read.All` and `AgentIdentity.Read.All`, and press **Add permissions**. Then press **Grant admin consent** for your tenant.

{{< picture src="pictures/entra-role-groups/api-permissions.png" alt="The API permissions page of the service app with four Microsoft Graph application permissions, each granted for the tenant" caption="The four application permissions after admin consent." >}}

{{< details summary="What each permission allows, and what the lab showed" >}}
`User.Read.All` serves people and agent users, and `AgentIdentity.Read.All` serves agent users and agent identities. You may leave out a permission that no kind in `identity.entra.read` needs.

`Group.Create` lets the app create a security group that it owns, and rename and delete its own groups. With `GroupMember.Read.All` it reads the members of any group, and with `User.Read.All` it reads every user's names, mail and whether the account is enabled. With `AgentIdentity.Read.All` it reads each agent identity's name, whether it is enabled and its sponsors. In the lab the app created, renamed and deleted its own groups and read the members of other groups, and Entra refused it a change to a group it did not own. The lab also read members' display names and enabled state, and Graph gave the enabled state with `User.Read.All` and not with `User.ReadBasic.All`. An agent identity's sponsors came back with `AgentIdentity.Read.All` alone. That app held more permissions as well, so this set has not yet run on its own.
{{< /details >}}

{{< fails >}}
`Microsoft Graph answered the read of the members of group 5d2c8a41-7e3b-4f90-a6d2-1c8e9b7f3a05 without the accounts' enabled state, so Straza counts the read as incomplete. Grant the service app the application permission User.Read.All and give admin consent.`
: The service app lacks `User.Read.All`. Nobody loses a role while the read fails, and the read shows as stale. Grant the permission with its admin consent, and the next pass reads the group.

`Microsoft Graph answered the read of agent identity 7a3e9c15-2b4d-4f86-9e01-5c8d2a6b4f73 without its enabled state, so Straza counts the read as incomplete. Grant the service app the application permission AgentIdentity.Read.All and give admin consent.`
: The service app lacks `AgentIdentity.Read.All`. When Graph refuses the read outright, the message starts with Graph's answer and ends with the same instruction. No agent loses its role, its status or its sponsor while the read fails, and the read shows as stale. Grant the permission with its admin consent.
{{< /fails >}}

## Upload Straza's certificate


The service app signs in with a key that Straza keeps, the client assertion key. Entra needs that key's certificate. Run `strazactl signing-keys list` and note the ID of the active `client_assertion` key. When there is none, run `strazactl signing-keys rotate client_assertion` and note the new key's ID. Use that ID in the next command.

{{< command terminal="Terminal" purpose="as a full administrator" >}}
```sh
strazactl signing-keys certificate client_assertion 6f1c2f0e-3b7a-4d19-9a55-2f4c8e1b7d10 > straza-service.pem
```
{{< /command >}}

In the service app, open **Certificates & secrets**, then **Certificates**, press **Upload certificate** and add `straza-service.pem`. If you staged a new key, now run `strazactl signing-keys promote client_assertion <key-id>`, at least a minute after the rotate.

{{< picture src="pictures/entra-role-groups/certificates.png" alt="The Certificates tab of the service app's Certificates and secrets page, with the button Upload certificate and two uploaded certificates" caption="The **Certificates** tab with **Upload certificate**." >}}

## Turn on the sync


Add the `identity.entra` block to the config file of strazad, the Straza server, with your tenant ID and the service app's client ID in place of the two names in angle brackets.

```yaml
identity:
  entra:
    tenant: <tenant-id>
    clientId: <service-app-client-id>
    read: [people, agentUsers, agentIdentities]
```

`read` names the kinds of account Straza takes from Entra, as [People from Entra](#people-from-entra) and [Agents from Entra](#agents-from-entra) explain. SCIM can no longer write a kind listed here. Without `read`, Straza creates the groups and assigns no role. The section cannot be set through environment variables. Restart strazad on every replica. One replica at a time runs the sync, in a pass every minute by default. [Configuration]({{< relref "reference/configuration.md#config-identity" >}}) lists the name prefix, the two intervals and the removal guard.

{{< fails >}}
`identity.entra.tenant is empty while the identity.entra section is set, and Straza signs in to Microsoft Graph in the tenant whose ID this key holds. Write the directory (tenant) ID from the overview page of the Entra admin centre, or delete the identity.entra section to leave Entra off`
: strazad did not start. Copy the **Directory (tenant) ID** from the service app's Overview page. A missing `clientId` stops the start the same way.

`identity.entra.read lists "users", which is not a kind of account Straza reads from Entra. Write people, agentUsers or agentIdentities`
: Write the kinds Entra owns: `people`, `agentUsers` or `agentIdentities`.
{{< /fails >}}

## See the groups in Entra


Within one minute of the restart, Straza creates a group for each role, `straza-admin` included. In the admin center, open **Groups** and search for `Straza:`. The role `finance-readers` gets the group `Straza: finance-readers`, owned by the service app. A role published later gets its group within a minute. Deleting a role deletes its group.

{{< picture src="pictures/entra-role-groups/groups-list.png" alt="All groups in the Entra admin center, searched for Straza:, with one security group for each Straza role" caption="The groups Straza created, one for each role." >}}

The console shows each role's group on the role's page.

{{< picture dark="pictures/entra-role-groups/role-group-dark.png" light="pictures/entra-role-groups/role-group-light.png" alt="The page of the role invoice-tools in the console, with the block Group in Entra, the chip linked, the group Straza: invoice-tools, its last read and three members, and three holders whose source is From Entra" caption="A role's page with its **Group in Entra**, here after three members were added." >}}

The admin API gives the sync's state.

```sh
curl -s -H "Authorization: Bearer $STRAZA_API_TOKEN" https://straza.example.com/v1/admin/entra
```

{{< details summary="What the console and the admin API show" >}}
A group's description starts with `Members of this group hold the Straza role finance-readers.` Then come the role's own description and `Straza created this group and manages it. Do not rename or delete it.`

On a role's page the console shows the group above the tabs under **Group in Entra**, with its state, its last read and how many members were not applied. It offers **Apply the removals** while removals are held and **Create the group again** once the group is deleted in Entra, and Overview names a stale read and held removals.

`GET /v1/admin/entra` names the `tenant` and gives `last_good`, the end of the last pass in which every request succeeded. While reads fail it gives `stale_since` and the message `error`, and `held` counts the roles whose removals wait for an admin. The roles list, `GET /v1/admin/roles`, gives each role an `entra` block with the group's object ID, its `state`, the last complete read as `read_at`, an `error` message when something is wrong, and the members that did not get the role under `not_applied`.

The group's unique name is `straza-`, 12 hex digits of your deployment, `-role-` and the role's ID. It never changes, and it keeps two Straza deployments in one tenant apart.
{{< /details >}}

## Assign a role to a person


In the admin center, open the group `Straza: finance-readers`, then **Members**, press **Add members** and pick the person.

{{< picture src="pictures/entra-role-groups/group-members.png" alt="The Members page of the group Straza: invoice-tools in the Entra admin center, with the button Add members and three agent identities as members" caption="The **Members** page of a role's group, here with three agents." >}}

An agent is added the same way. Within about a minute the person holds the role. In the lab, Graph showed the change after 6 to 16 seconds. To check, take the person's ID from `strazactl users list` and read their assignments. The role shows with `"origin":"entra"`.

```sh
curl -s -H "Authorization: Bearer $STRAZA_API_TOKEN" "https://straza.example.com/v1/admin/assignments?subject_kind=user&subject_id=0197fa3d-5555-7abc-9def-000000000015"
```

Remove the person from the group, and the next pass ends the assignment. The audit trail records a `roles.assign` or `roles.unassign` with the origin `entra` and no actor, because Graph does not say who changed the group. Entra's own audit log names that person.

{{< fails >}}
`this assignment was not removed, because it comes from the role's group in Entra and Straza would write it again at its next read. Remove the user from the group in Entra, and Straza ends the assignment within its next pass`
: The admin API, and with it `strazactl unassign`, refuses to end an assignment of the origin `entra`. Remove the person from the group in Entra.
{{< /fails >}}

{{< details summary="How fast a running session follows, and when a change takes longer" >}}
A running session gets a new role within seconds, or at its next check-in without a connected push stream, and loses a removed role the same way. When strazad cannot list the person's sessions, a session keeps the role until its token ends, at most 300 seconds. A member listed as `name-taken` gets the role at the first full read after the sign-in name is free. A full read comes every `identity.entra.reconcileInterval`, one hour by default, or sooner when the group changes.
{{< /details >}}

## Who gets a role {.nostep}


Straza reads each group's members with nested groups resolved. A person or an agent gets the role, and so does a guest. Straza's break-glass account never does. Every other member is listed in `not_applied` with one reason.

{{< details summary="Agents, agent identities in nested groups, and the reasons" >}}
| Reason | Why the member holds no role |
|---|---|
| `not-read` | `identity.entra.read` does not list the member's kind. |
| `name-taken` | A Straza user that Straza cannot link holds the member's sign-in name. |
| `not-agent` | An application's service principal, which is no agent identity. |
| `unread` | Graph returned a member of another type, with its ID only. |

Entra lists an agent identity only in the group it is a direct member of, so Straza reads each nested group for agent identities on its own. An agent user is read through nested groups as a person is. A member listed as `name-taken` may also be an agent whose name another Straza user has. A member listed as `unread` may also be an agent user without its agent identity, or an agent identity Graph does not find. Straza assigns roles to people and agents only, so an application is listed as `not-agent`.
{{< /details >}}

{{< details summary="Disabled accounts" >}}
A member whose account is disabled in Entra keeps the role, and Straza disables the member's Straza user, as [Disabled and deleted accounts](#disabled-and-deleted-accounts) shows. The role therefore needs no new assignment when Entra enables the account again, and a mass disable in Entra never trips the removal guard.
{{< /details >}}

## People from Entra {.nostep}


A member who has no Straza user gets one, with the origin `entra`, in the pass that assigns the role. A person who is already in Straza is matched by the Entra object ID, or else by the sign-in name.

{{< details summary="What the new user carries, and how a match works" >}}
The new user carries the Entra object ID as its `externalId`, the user principal name as its username, the display name and the mail from Entra, and the user type `human`. When the user principal name, the display name or the mail changes in Entra, Straza writes the change to the user.

Straza finds a person who is in Straza already by the Entra object ID in the `externalId`, which a sign-in through [Entra ID login]({{< relref "guides/connect-identity/entra-login.md" >}}) stores. A user of the origin `local` or `scim` found that way is taken over once, and its origin becomes `entra`. When no user has the object ID, Straza links the user whose username is the member's user principal name, but only an active person who is not of the origin `entra` and holds no Entra object ID yet. A disabled account is never linked by a name. When a newcomer gets the name of a leaver Entra owned, the leaver's user is renamed with ` (left)`, keeps the agents it sponsors, and the newcomer gets a user of their own. When any other user has that name, the member is listed as `name-taken` and gets the role at the first full read after no such user holds the name. A person from Entra is never matched to the break-glass account, an agent or a service.
{{< /details >}}

{{< details summary="What Straza records for a person" >}}
Every record the sync writes for a person names no actor, because Graph does not say who changed the account, and Entra's own audit log names that person. Each carries the origin `entra`: `user.create` for a person Straza created, and `user.update` with the changed fields and a reason. Its `changed` names `status` for a disable or an enable, `username`, `display` or `email` for a name that changed in Entra, and `origin`, alone or with `external_id`, for a person Entra took over. A disable or an enable also writes `user.killed`, `user.reactivated` or `user.lift.blocked`, and the revocation events. [Events and the audit record]({{< relref "reference/events.md" >}}) lists their fields.
{{< /details >}}

## Agents from Entra {.nostep}


An agent identity is an AI agent's account in Entra, and Entra issues its tokens. Its optional agent user is a user account for things that need a user, such as a mailbox. Straza treats the two as one agent.

With `agentUsers` or `agentIdentities` in `identity.entra.read`, an agent in a role group gets a Straza agent and the role.

{{< details summary="What the new agent carries, and how a match works" >}}
The agent has the origin `entra` and holds the role from the same pass. Straza finds the agent by the agent identity's object ID, which the agent keeps as its Entra agent ID, and never by a name.

The new agent takes the agent identity's display name as its username and display name, and the user type `agent`. When the agent identity is renamed in Entra, Straza renames the agent. When another Straza user has that name, the agent gets the name with the first eight characters of the agent identity's ID, such as `ledger-agent (3a3a3a3a)`, and takes the plain name once it is free. An agent of another origin that holds the agent identity's object ID as its Entra agent ID, such as one an IGA provisioned, is taken over once, its origin becomes `entra`, and it takes the agent identity's name.

With `agentUsers`, an agent user in the group stands for the agent identity behind it, and Straza reads whether the agent user is enabled. With `agentIdentities`, an agent identity in the group stands for itself. When both are in the group, the one agent gets the role once.
{{< /details >}}

{{< details summary="The sponsor, and what Straza shows when there is no single one" >}}
{{< picture src="pictures/entra-role-groups/agent-sponsors.png" alt="The Owners and sponsors page of the agent identity invoice-triage-agent in the Entra admin center, with one user listed as sponsor" caption="An agent identity's **Owners and sponsors** page with one sponsor." >}}

The agent's sponsor comes from Entra. Straza takes it only when Entra lists exactly one sponsor for the agent identity, that sponsor is a user, and Straza has a person whose `externalId` is that user's object ID. People from Entra have it, and so does a person who signed in through [Entra ID login]({{< relref "guides/connect-identity/entra-login.md" >}}). The sponsor is then that person's username, and it follows a change in Entra. In every other case the agent has no sponsor in Straza, and whatever needs one is refused.

The admin API's read of the agent, `GET /v1/admin/users/{id}`, gives an `entra` block. Its `sponsor_state` says why the agent has no sponsor, and `sponsors` lists what Entra names, each with its object ID and its kind.

| `sponsor_state` | What Entra lists | What to do in Entra |
|---|---|---|
| `none` | No sponsor. | Add one person as the agent's sponsor. |
| `several` | More than one sponsor. | Leave one person. |
| `not-person` | One sponsor that is no person, such as a group or an agent. | Name one person instead. |
| `no-user` | One person who has no Straza user. | Assign that person a role in Straza, or name another sponsor. |

The audit trail gets one `entra.sponsor.unresolved` record when the agent enters this state, with the state, what Entra lists and a sentence that says what to do. It gets one `entra.sponsor.resolved` when the agent leaves the state, with the sponsor's username. A pass that finds the state unchanged writes neither. The same block gives `identity` and `agent_user`, each `enabled` or `disabled`.
{{< /details >}}

{{< details summary="Disable and enable, which reads carry them, and what Straza records" >}}
An agent is disabled in Straza when its agent identity is disabled or deleted in Entra, or its agent user is disabled. It is enabled again when neither is disabled. A deleted agent user no longer counts, because the agent identity is what acts.

The disable runs the same kill switch as for a person, and the agent keeps its roles. A lock an admin set stays through an enable. Straza reads every agent identity it owns in every pass, twenty to a Graph request, so a disable, a delete, a rename or a new sponsor in Entra reaches Straza within about one `identity.entra.deltaInterval`, a minute by default. An agent identity that Graph no longer finds disables its agent only when Straza has read it before. An agent user's change comes through the feed of changed users, which the sync reads while `agentUsers` is listed.

In the lab, the read of an agent identity showed a disable after 6 seconds, an enable after 11 and a rename after 5 to 11. The feed showed an agent user's disable after 8 seconds and its enable after 17.

Every record the sync writes for an agent names no actor and carries the origin `entra`: `user.create` for an agent Straza created, and `user.update` with the changed fields and a reason. Its `changed` names `status` for a disable or an enable, with a reason that names the agent identity or the agent user, `username` for a rename, `sponsor` for a new sponsor, and `origin` for an agent Entra took over.
{{< /details >}}

{{< fails >}}
`ledger-agent was not changed here, because the agent comes from Microsoft Entra ID and Straza would write Entra's state back at its next read. Change the agent, or its sponsor, in Entra. To stop ledger-agent at once, lock the account.`
: For an agent of the origin `entra`, the admin API and strazactl answer 409 to a disable, an enable, a delete, and a change of the display name, the mail, the sponsor or the Entra agent ID. Make the change in Entra. To stop the agent before that change arrives, run `strazactl users lock ledger-agent` or end its sessions. Its key can still be set and removed.
{{< /fails >}}

## Disabled and deleted accounts {.nostep}


A person you disable or delete in Entra is disabled in Straza and keeps their roles. An account that is only missing from a read is never disabled.

{{< details summary="What a disable does, what an enable lifts, and which reads carry them" >}}
The disable ends every session, refuses the person on every replica, tells their enrolled machines and removes their stored grants. A deleted person's record stays. An enable or a restore in Entra enables the user again, and a lock an admin set stays. Sessions the disable ended stay ended.

Two reads carry these changes. The read of each role group's members gives each member's enabled state. A feed of changed users, Graph's `users/delta`, is asked in every pass, every minute by default, and starts at the sync's first pass. Straza stores the feed's link, so a restart or another replica continues it, and it drops every changed account it has no person for.

In the lab, a disable showed in that feed after 10 seconds, an enable and a delete after 16, and a restore after 10. A deleted user did not show in a group's own delta within 180 seconds, which is why the feed exists.
{{< /details >}}

{{< fails >}}
`ada was not changed here, because the account comes from Microsoft Entra ID and Straza would write Entra's state back at its next read. Change the account in Entra. To stop ada at once, lock the account.`
: For a person of the origin `entra`, the admin API and strazactl answer 409 to a disable, an enable, a delete, and a change of the display name or the mail. Make the change in Entra. To stop the person before that change arrives, run `strazactl users lock ada` or end the person's sessions, which both still work.
{{< /fails >}}

{{< details summary="When the sync was down for more than seven days" >}}
Graph keeps the feed's link for seven days, as Microsoft documents. When the sync was down for longer, Entra no longer knows the link, and the feed starts again from now. A person deleted or disabled in Entra in that gap who is in no role group then stays active in Straza, although Entra no longer signs them in. A person deleted in the gap who was in a role group loses that role at the next full read, and also stays active. After such a gap, compare the active users of the origin `entra` in `strazactl users list` with Entra, and lock each one that Entra has deleted or disabled.
{{< /details >}}

## When Entra cannot be read {.nostep}


Straza ends nobody's role on a failed read. Every role keeps its last good holders while the read fails, and `GET /v1/admin/entra` gives the reason in `error`.

{{< details summary="What Straza shows and records while the read fails" >}}
When a request fails for a reason beyond one group, the pass stops and `GET /v1/admin/entra` shows `stale_since` with the message of the failure. The audit trail gets one `entra.read.stale` record, not one for each pass, and one `entra.read.recovered` when a pass succeeds again. When Graph slows Straza down, the next pass waits the time Graph named.
{{< /details >}}

{{< fails >}}
`Entra refused the Straza service app 3b2f6c1d-8a47-4e0b-9c15-7d2e4f6a8b90 (AADSTS700027), because the app holds no certificate of the client assertion key 6f1c2f0e-3b7a-4d19-9a55-2f4c8e1b7d10. An administrator uploads the certificate that strazactl signing-keys certificate client_assertion 6f1c2f0e-3b7a-4d19-9a55-2f4c8e1b7d10 prints to that app registration, then Straza tries again.`
: Upload the certificate as the step Upload Straza's certificate shows.
{{< /fails >}}

## Held removals {.nostep}


When a pass would remove more than half of a role's holders from Entra, and at least five, Straza adds the new members and holds the removals. Check the group in Entra. If the removals are right, press **Apply the removals** on the role's page in the console, or release them with the role's ID from the roles list.

```sh
curl -s -X POST -H "Authorization: Bearer $STRAZA_API_TOKEN" https://straza.example.com/v1/admin/roles/0197fa3d-7777-7abc-9def-000000000017/entra-group/release
```

{{< details summary="What a hold records, and when a release lands" >}}
The hold writes one `entra.pass.held` record, sets the role's `state` to `held`, and its `error` says how many holders the group no longer lists. `identity.entra.removalGuard` sets both numbers. The release answers `{"status":"released"}` and writes one `entra.pass.released` record that names you. Straza applies the removals at its next pass.
{{< /details >}}

{{< fails >}}
`nothing changed, because no removals of role finance-readers are on hold. Read the role again to see its group's state`
: The role's `state` is not `held`.
{{< /fails >}}

## A group deleted or renamed in Entra {.nostep}


When a role's group is deleted in Entra, the role keeps its holders. Delete the role, or have its group created again with this call. The call ends every assignment that came from Entra and creates an empty group, so you add the members again.

```sh
curl -s -X POST -H "Authorization: Bearer $STRAZA_API_TOKEN" https://straza.example.com/v1/admin/roles/0197fa3d-7777-7abc-9def-000000000017/entra-group/recreate
```

{{< details summary="What each way does" >}}
Straza marks the role's `state` as `missing`, stops reading the group and writes one `entra.group.missing` record. A recreate ends every assignment of the role that came from Entra, each with a `roles.unassign` that names you, and the next pass creates an empty group under a new unique name. A group restored in Entra is not linked again, because the new group has another unique name. Deleting the role ends its assignments and removes its link.

A group renamed in Entra breaks nothing, because Straza keeps the group's object ID. The next full read writes Straza's name and description back with one `entra.group.update` record.
{{< /details >}}

## Beside an IGA {.nostep}


Your IGA can keep writing role membership over SCIM, also for accounts Entra owns. It can keep writing agents that are not in Entra. A person may hold a role from a group in Entra and from SCIM or an admin at once, as two assignments. A removal by one source never ends the other's assignment. SCIM neither lists nor changes an assignment from Entra, so the IGA sees only the members it wrote.

{{< fails >}}
`Straza did not accept this person, because it takes people from Microsoft Entra ID: identity.entra.read lists people. Stop provisioning people to Straza in your identity manager, or take people out of identity.entra.read.`
: SCIM answers 403 to a create, replace, patch or delete of a person while Entra owns people, and writes nothing. Reads over SCIM still answer, and they do not show a disable from Entra as a lock. Turn off the provisioning of people to Straza in the IGA, and keep its group memberships and agents.

`Straza did not accept this agent, because it takes agents that are in Microsoft Entra ID from Entra: identity.entra.read lists agentUsers or agentIdentities. Stop provisioning this agent to Straza in your identity manager, or take both kinds out of identity.entra.read.`
: While Entra owns agents, SCIM answers 403 to a create, replace, patch or delete of an agent of the origin `entra` or one that carries an Entra agent ID, and to a write that would give an agent an Entra agent ID. It writes nothing. Every other agent and every service is written as before. Stop provisioning that agent in the IGA.
{{< /fails >}}

{{< details summary="A person renamed over SCIM" >}}
When the IGA renames a person, the agents that person sponsors move to the new name in the same write. No agent keeps the old name, which a newcomer could get later.
{{< /details >}}

## The certificate and its key {.nostep}


Whoever may manage groups in your tenant can create a Straza account by adding a member to a role's group, a guest included. A guest added to the group of `straza-admin` becomes a Straza admin. Whoever takes Straza's client assertion key can add any account of your tenant to any role group, `straza-admin` included. Straza applies that within a minute. The key also reads every user's name, mail and enabled state.

{{< details summary="What else the key reaches, and how to rotate it" >}}
In the lab the service app could change the members of its own groups and of no other group. Guard who may change the role groups in Entra as you guard an admin role. With these permissions the key also creates security groups, reads the members of every group and reads every agent identity's sponsors. It stays sealed on the server, each assignment leaves a `roles.assign` with the origin `entra`, and Entra's audit log names the service app. When the key may have been copied, run `strazactl signing-keys retire <kid>` and remove its certificate from the service app.

An agent that Straza creates from a role group cannot start a session until an admin registers its key or it enrolls a client. Whoever may change an agent identity's sponsors in Entra decides who answers for that agent in Straza: the sponsor approves the agent's held calls, and the agent may use the sponsor's own connection to a server where that server allows it. Guard that right as you guard the role groups.

The certificate lasts 730 days, and the console's Overview warns 30 days before its end. Rotate in three steps: `strazactl signing-keys rotate client_assertion`, then upload the new certificate to the service app and to every agent blueprint that holds the old one, then `strazactl signing-keys promote client_assertion <kid>`. A key promoted before its upload makes every read fail with the refusal above, and the roles keep their holders until the upload.
{{< /details >}}

## What is not built yet {.nostep}


strazad does not start when `identity.entra` sets `roleGroups: false` or `scopeGroups`.

## Next {.nostep}

- [Entra ID login]({{< relref "guides/connect-identity/entra-login.md" >}}) signs people in with Entra and gives each of them their object ID.
- [Roles and access]({{< relref "concepts/roles-and-access.md" >}}) explains what a role grants.
- [Events and the audit record]({{< relref "reference/events.md" >}}) lists the records of the role groups and of the accounts from Entra.
