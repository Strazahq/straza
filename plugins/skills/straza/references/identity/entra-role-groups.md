# Role groups in Entra

Straza can write one security group into Microsoft Entra ID for each role and give the role to the members of that group, read over Microsoft Graph. With `people` in `identity.entra.read`, Entra owns every person in the deployment: Straza creates a member who has no Straza user, and disables and enables people as Entra says. With `agentUsers` or `agentIdentities`, Straza does the same for the agents in the groups and takes each agent's sponsor from Entra. The Graph requests it relies on ran against an Entra lab tenant on 2026-11-16 and 2026-11-17. The sync has run against that lab tenant for one role group and three people. Agents from Entra are built and tested against a stand-in for Graph and have not run against a live tenant end to end. Say so when you propose it. The public page is https://docs.straza.ai/guides/connect-identity/entra-role-groups/index.md.

The person does every step that needs a credential: the Entra admin center, the admin API token and the config file. You hold no Straza credential. The checks below need an admin API token with `identity:read` in `STRAZA_API_TOKEN`, and printing the certificate needs one with the scope `full`.

## 1. The service app

A second app registration, beside the sign-in app of Entra ID login. In the Entra admin center: New registration, single tenant, no redirect. Under API permissions, Microsoft Graph, Application permissions: `Group.Create` and `GroupMember.Read.All`, `User.Read.All` for people and agent users, and `AgentIdentity.Read.All` for agent users and agent identities, then Grant admin consent. Keep its client ID and the tenant ID. Without `User.Read.All` a member's enabled state does not come back, Straza counts each read of a group with a person in it as incomplete, nobody loses a role, and the read shows as stale with the sentence `Microsoft Graph answered the read of the members of group <id> without the accounts' enabled state, so Straza counts the read as incomplete. Grant the service app the application permission User.Read.All and give admin consent.` Without `AgentIdentity.Read.All` the read of the agents fails the same way: nobody loses a role, a status or a sponsor, and the sentence ends `Grant the service app the application permission AgentIdentity.Read.All and give admin consent.`

## 2. The certificate

The service app signs in with Straza's client assertion key, the key that also signs for an Entra agent blueprint. `strazactl signing-keys list` shows the active `client_assertion` key. When there is none, `strazactl signing-keys rotate client_assertion` stages one, and `strazactl signing-keys promote client_assertion <kid>` makes it sign at least a minute later, after the upload. Save the certificate and upload it to the service app under Certificates & secrets, Certificates:

```sh
strazactl signing-keys certificate client_assertion <kid> > straza-service.pem
```

## 3. The config

```yaml
identity:
  entra:
    tenant: <tenant id>
    clientId: <service app client id>
    read: [people, agentUsers, agentIdentities]
```

No key has an environment variable. With `people` in `read` Entra owns every person, with `agentUsers` or `agentIdentities` it owns the agents that are in Entra, and with none of them no member gets a role. Restart strazad on every replica. `roleGroups: false` and any `scopeGroups` entry are refused at the start.

## 4. Verify

Within a minute each role has a group named `Straza: <role>`, `straza-admin` included. Read the state:

```sh
curl -s -H "Authorization: Bearer $STRAZA_API_TOKEN" <server>/v1/admin/entra
curl -s -H "Authorization: Bearer $STRAZA_API_TOKEN" <server>/v1/admin/roles | jq '.[] | {name, entra}'
```

The first answers `enabled`, `tenant`, `last_good`, and `stale_since` with `error` while reads fail, and `held`, the number of roles with removals on hold. Each role's `entra` block carries `group_id`, `state` (`creating`, `linked`, `missing`, `deleting`, `held` or `released`), `read_at`, `error` and `not_applied`. The console shows the same on the role's page under Group in Entra, and Overview names a stale read and held removals.

A person added to a group in Entra holds the role within about a minute, with an assignment of the origin `entra`, and gets a Straza user in the same pass when they have none. An agent identity or an agent user added to a group gives its agent the role the same way. A person removed loses the role at the next pass, and each session at its next call.

## Who gets a role

Every person and every agent of a kind `read` lists in the group, guests included, and never the break-glass account. Straza finds the person's Straza user by the Entra object ID in its `externalId`, and takes over a user of the origin `local` or `scim` found that way once, so its origin becomes `entra`. When no user has the object ID, Straza links the user whose username is the member's user principal name only if it is an active person, is not of the origin `entra` and holds no Entra object ID. A disabled account is never linked by a name. When no user has the object ID or the name, Straza creates the user with the object ID as `externalId`, the user principal name as username, the display name, the mail, the user type `human` and the origin `entra`. Every other member sits in `not_applied` with one reason: `not-read`, `name-taken` (a Straza user that may not be linked holds the sign-in name, or another user holds the name of an agent Straza would create), `not-agent` (an application's service principal, which is no agent identity) or `unread` (another type with its ID only, an agent user without its agent identity, or an agent identity Graph does not find). The reason `agent` came from older servers only. A person's match never writes to an agent, a service or the break-glass account.

## Agents from Entra

An agent identity is an AI agent in Entra and gets its tokens. Its optional agent user is a user account for things that need a user, such as a mailbox. The two are one Straza agent, found by the agent identity's object ID, which the agent holds as its Entra agent ID, and never by a name. An agent with no Straza user is created with the type `agent`, the origin `entra`, and the agent identity's display name as username and display name. An agent of another origin that holds the Entra agent ID is taken over once, its origin becomes `entra`, and it takes the agent identity's name. A rename in Entra renames the agent. When another Straza user has that name, the agent gets the name with the first eight characters of the agent identity's ID, such as `ledger-agent (3a3a3a3a)`, and takes the plain name once it is free. Entra lists an agent identity only in the group it is a direct member of, so Straza reads each nested group for agent identities on its own.

The sponsor comes from Entra only when Entra lists exactly one sponsor, it is a user, and Straza has a person whose `externalId` is that object ID. Otherwise the agent has no sponsor, and `GET /v1/admin/users/{id}` gives an `entra` block with `sponsor_state` (`none`, `several`, `not-person` or `no-user`) and `sponsors`, each with `id` and `kind`. The same block gives `identity` and `agent_user`, each `enabled` or `disabled`.

The agent is disabled when its agent identity is disabled or deleted in Entra, or its agent user is disabled, and enabled again when neither is. A deleted agent user no longer counts. Straza reads every agent identity it owns in every pass, twenty to a Graph request, so a change arrives within about a minute by default. An agent user's change comes through the feed of changed users while `agentUsers` is listed.

## Disable and enable

A member disabled in Entra keeps the role, and Straza disables the Straza user with the kill switch of a SCIM deactivation: sessions ended, stored grants removed, origin `entra`. An enable in Entra enables the user, and a lock an admin set stays. A deleted account disables the user and the record stays, and a restore enables it. Only an explicit disabled or deleted state from Graph disables, never an account that a read leaves out. Two reads carry this: each role group's member read, and the feed of changed users, `users/delta`, asked in every pass, every minute by default, starting from the sync's first pass, with its link stored so a restart or another replica continues it. Straza drops every changed account it has no person for. The lab read that feed once with the enabled state. How a disable, a delete and a restore look in it comes from Microsoft's documentation and was not watched.

## SCIM and the admin API while Entra owns people or agents

- SCIM answers a create, replace, patch and delete of a person with 403 and the detail `Straza did not accept this person, because it takes people from Microsoft Entra ID: identity.entra.read lists people. Stop provisioning people to Straza in your identity manager, or take people out of identity.entra.read.` Reads stay, every Group operation still takes a person as a member, and agents and services are written as before. A revocation that a disable in Entra wrote is not a lock on the SCIM wire.
- While `read` lists `agentUsers` or `agentIdentities`, SCIM answers a create, replace, patch and delete of an agent of the origin `entra` or one that carries an `entraAgentId`, and a write that would give an agent one, with 403 and the detail `Straza did not accept this agent, because it takes agents that are in Microsoft Entra ID from Entra: identity.entra.read lists agentUsers or agentIdentities. Stop provisioning this agent to Straza in your identity manager, or take both kinds out of identity.entra.read.` Other agents and services are written as before.
- A SCIM write that renames a person moves the sponsor of every agent that person sponsors to the new name in the same write.
- The admin API and strazactl answer a disable, an enable, a change of the display name or the mail, and a delete of a person of the origin `entra` with 409 and, for the user ada, `ada was not changed here, because the account comes from Microsoft Entra ID and Straza would write Entra's state back at its next read. Change the account in Entra. To stop ada at once, lock the account.` Lock, unlock and ending sessions stay.
- For an agent of the origin `entra` while an agent kind is read, they answer a disable, an enable, a change of the display name, the mail, the sponsor or the Entra agent ID, and a delete with 409 and, for the agent ledger-agent, `ledger-agent was not changed here, because the agent comes from Microsoft Entra ID and Straza would write Entra's state back at its next read. Change the agent, or its sponsor, in Entra. To stop ledger-agent at once, lock the account.` Lock, unlock, ending sessions and the agent's key routes stay.
- On an Entra tenant issuer, a sign-in that finds the account by the token's `sub` while the token carries an `oid` rewrites the account's `externalId` to the `oid`, with one `user.update` record.

## Records

`roles.assign` and `roles.unassign` with `origin` `entra`, a reason and no actor. Nine actions under `straza.audit.admin`: `entra.group.create`, `entra.group.update`, `entra.group.delete`, `entra.group.missing`, `entra.read.stale`, `entra.read.recovered` and `entra.pass.held` with no actor, and `entra.pass.released` and `entra.group.recreate` naming the admin (events revision 66). For people, with `origin` `entra` and no actor (events revision 67): `user.create`, `user.update` with `changed` and a `reason`, `user.killed`, `user.reactivated` and `user.lift.blocked`, and the revocation events. For agents the same records, where `user.update` names `username`, `sponsor`, `status` or `origin` (events revision 68), and two more actions with no actor: `entra.sponsor.unresolved`, once when an agent enters the state of having no single sponsor, with `state`, `sponsors` and `reason`, and `entra.sponsor.resolved`, once when it leaves it, with `sponsor`.

## Gotchas

- A failed read ends no role. Every role keeps its last good holders until an admin fixes the read, and one `entra.read.stale` record marks it.
- A pass that would end more than half of a role's Entra holders, and at least five, holds the removals. The person releases them with `POST /v1/admin/roles/{id}/entra-group/release`, and Straza applies them at its next pass.
- A group deleted in Entra keeps the role's holders and stops the role's sync. The person either deletes the role or posts to `/v1/admin/roles/{id}/entra-group/recreate`, which ends every Entra holder of the role and creates an empty group under a new name key.
- A group renamed in Entra gets Straza's name back at the next full read.
- SCIM neither lists nor changes an assignment of the origin `entra`, so an identity manager beside it sees only its own members. The admin API, and `strazactl unassign` with it, refuses to delete one: remove the person from the group in Entra.
- Whoever may manage groups in the tenant can create a Straza account by adding a member to a role group, a guest included, and a guest in the group of `straza-admin` becomes a Straza admin. Whoever takes the client assertion key can add any account of the tenant to any role group, `straza-admin` included, and Straza creates and applies it within a minute. The key also reads every user's name, mail and enabled state, and every agent identity's sponsors. Whoever may change an agent identity's sponsors in Entra decides who answers for that agent in Straza. In the lab the service app could change its own groups and no other group.
- When the sync is down for more than seven days, the feed starts again from now. A person deleted or disabled in Entra in that gap who is in no role group stays active in Straza, although Entra no longer signs them in.
