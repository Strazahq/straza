# AI agents with sponsors

An agent is an identity like a person, born in your identity manager and provisioned over SCIM, with two differences. Its record says it is an agent and names the human who answers for it, so policy can treat it as one and approvals can reach that human. Its credential is an Ed25519 key registered on the identity, so it enrolls with no browser and no password, and the identity manager cuts it the same way it cuts a person. When the record is deactivated, the next token grant fails and every running session is revoked. The public page is https://docs.straza.ai/guides/connect-identity/nhi-provisioning/index.md.

You need an admin API token for the SCIM request and the admin API reads, minted with `strazactl api-token create --name <token name> --scope scim:read,scim:write,identity:read` and held in `ADMIN_API_TOKEN`, an admin API token for `strazactl` in `STRAZA_API_TOKEN`, and the `straza` binary where the agent runs. The walk ran against strazad at `http://localhost:8420`. The person holds both tokens and runs this page's writes in their own terminal: the SCIM request, `strazactl users create-nhi`, `strazactl users nhi-key set`, `strazactl users nhi-key unset` and `strazactl assign`. They need `scim:write` or `identity:write`, which are never yours, and they run the reads too, because you hold no Straza credential.

## 1. Provision the identity

Send the user with the agent schema in `schemas`, `userType` `agent`, and the Straza extension carrying `agencyMode` and `sponsor`. An identity manager that holds the sponsor as the agent's manager can send that instead, as the end of this section says. The agent schema makes Straza record the identity as non-human at birth, and `userType` `agent` has the same effect for a connector that cannot emit the schema, so either one is enough. One difference stays visible on the wire: the read-only `kind` is `nhi` only when the create carried the agent schema, so a row created with `userType` `agent` alone reads `kind` `human` there, while the admin surface and the key lane treat it as non-human.

```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/scim+json' \
  -X POST http://localhost:8420/scim/v2/Users -d '{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User","urn:ietf:params:scim:schemas:extension:agent:2.0:Agent","urn:straza:params:scim:schemas:extension:2.0:User"],"externalId":"<external id>","userName":"<agent username>","displayName":"<display name>","title":"<job title>","userType":"agent","active":true,"urn:straza:params:scim:schemas:extension:2.0:User":{"agencyMode":"autonomous","sponsor":"<sponsor username>"}}' \
  | jq -c '{id, userName, userType, active, ext: .["urn:straza:params:scim:schemas:extension:2.0:User"]}'
```

```text
{"id":"<user id>","userName":"<agent username>","userType":"agent","active":true,"ext":{"agencyMode":"autonomous","kind":"nhi","locked":false,"origin":"scim","sponsor":"<sponsor username>"}}
```

`agencyMode` says how the agent works. `autonomous` agents are never allowed to approve their own calls, whatever a policy says, and `supervised` and `interactive` agents work under a human who confirms. `sponsor` is that human's username. The sponsor must be an active human other than the agent, because an agent sponsoring an agent is no accountability at all, and Straza refuses it. An approval rule that names no deciders and no approver roles falls on the sponsor by default, and a rule can also name the sponsor beside approver roles with `deciders: [sponsor]`. An agent with no usable sponsor cannot raise an approval until the edge exists, and the request is denied at request time with a reason that names the fix. `strazactl users list` lists the identity like any other, with `STATUS` `active`, `ORIGIN` `scim` and an empty `EMAIL` column.

The `manager` of the SCIM enterprise User extension, `urn:ietf:params:scim:schemas:extension:enterprise:2.0:User`, becomes the sponsor of a user whose `userType` is `agent` after the write. Straza looks the manager up as a Straza id, then as a `userName`, and stores the manager's `userName`. In a PATCH, a manager Straza does not know answers `400` with `invalidValue` and nothing is saved. The break-glass account answers as an unknown manager, and on an agent so does a manager in a shape Straza does not read, such as a number or an object without a `value`. A POST or PUT with such a manager is not refused. The user is saved and its sponsor stays as it was, so a new agent has none. Then strazad logs one WARN line, `SCIM manager not applied`, that names the agent and the reason. The identity manager's next PUT, or a PATCH of the manager, sets the sponsor once the manager exists in Straza. So the manager is still provisioned before the agent. If the agent arrived first, send it again, and look for `SCIM manager not applied` in strazad's log to find agents left without a sponsor. Microsoft Entra ID is unaffected, because it sends the manager only in a PATCH and only once the manager exists in Straza. A `sponsor` in the same write wins over the manager. A PATCH `remove` of the manager clears the sponsor. On a user of any other type the manager is accepted and not stored. Microsoft Entra ID sent the manager on 2026-11-10 as a PATCH with op `Add` on the URN-qualified path, with the manager's Straza id as a plain string, right after the create. Whoever can change an agent's manager in the identity manager therefore chooses its sponsor in Straza. A sponsor who is a person can connect the agent to a server, and the agent's calls can run on the sponsor's own connection where the sponsor has allowed agents on it. Straza does not check that the manager is a person when it stores the sponsor, so say this to the person before they map the manager.

An agent that calls a server behind Microsoft Entra acts there as an Entra agent identity, whose client id sits on its record as `entraAgentId` in the Straza extension. When Microsoft Entra ID is the identity manager, the person adds the target attribute `urn:ietf:params:scim:schemas:extension:straza:2.0:User:entraAgentId` with type String in the provisioning app's attribute mapping, and maps the agent user's `identityParentId` to it. They map that name only, and do not also map `urn:straza:params:scim:schemas:extension:2.0:User:entraAgentId`. Entra's create then carries the link, and Straza stores it with the create. Under Straza's own name Entra leaves the attribute out of the create and sends it one pass later in a PATCH, as it did on 2026-11-10. A tenant that already maps the `urn:straza:` name can delete that mapping, and nothing breaks while both mappings exist. Straza never renders the second name, so Entra may send the value again on a later pass, and Straza answers that repeat with `200` and changes no attribute. `identity/generic-scim.md` has the rules of the second name.

## 2. Generate and register the key

On the agent's machine, where `straza service` runs, ask the service for the key as the agent's own OS account. The service keeps the private half under its own OS account, and the command prints the exact registration line. On an OS account that holds a person's enrollment, the service refuses both `straza keygen` and `straza enroll --headless`, so a person's machine key is never replaced by an agent's:

```sh
straza keygen --user <agent username>
```

```text
NHI key generated (the Straza background service holds the private half).
Public key: <public key>

An administrator registers it with:
  strazactl users nhi-key set <agent username> <public key>
(or: PUT /v1/admin/users/{id}/nhi-key {"public_key":"<public key>"})
Then enroll headless:
  straza enroll --server <strazad-url> --headless --user <agent username>
```

An administrator registers the public half:

```sh
strazactl users nhi-key set <agent username> <public key>
```

```text
key registered for <agent username>. The agent can now `straza enroll --headless --user <agent username>`
```

One key per identity, so registering again rotates it, and the endpoint refuses a human identity. `GET /v1/admin/users/{id}/nhi-key` returns `registered`, `fingerprint` and `created`, never the key itself.

## 3. Assign a role and enroll

Roles arrive the way they do for a person: the identity manager adds the identity to the role's group with a members PATCH on `/scim/v2/Groups/<role id>`, and the identity's `groups` then lists it. Enrollment needs no human, because the key signs a short-lived token and strazad's discovery document at `/.well-known/straza/idp.json` advertises where the key lane lives as `nhi_issuer`:

```sh
straza enroll --server http://localhost:8420 --headless --user <agent username>
```

```text
Enrolled headless as <agent username>. The Straza background service holds this agent's key and starts each of its sessions.
```

`straza status` then shows the identity as an AI agent with no device, which is expected, because the key mints every session token and the service binds each session to it.

## 4. Verify

An administrator's view of the identity shows every fact in one record:

```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" http://localhost:8420/v1/admin/users/<user id> \
  | jq -c '{username, kind, user_type, origin, sponsor, agency_mode, effective_roles, nhi_key_registered}'
```

```text
{"username":"<agent username>","kind":"nhi","user_type":"agent","origin":"scim","sponsor":"<sponsor username>","agency_mode":"autonomous","effective_roles":["<role>"],"nhi_key_registered":true}
```

`strazactl catalog preview --user <agent username>` resolves the tools the roles reach.

## Undo

Revoking the key is the cut for the credential, and the next enrollment attempt fails at once. The service keeps the agent's last ID token until 30 seconds before it expires, so new sessions can start with it for up to 10 minutes at the built-in issuer, and a session that already runs keeps going until it is revoked or the identity is deactivated:

```sh
strazactl users nhi-key unset <agent username>
straza enroll --server http://localhost:8420 --headless --user <agent username>
```

```text
key revoked for <agent username>
straza: the identity provider refused the agent's sign-in (invalid_client): client authentication failed. Check the registered key or client, and that the user is an active agent
```

Retiring the identity is the identity manager's act: a SCIM deactivation or delete disables the account, revokes its sessions and removes its per-user grants. An administrator can also soft-delete it with `DELETE /v1/admin/users/{id}`, which answers `{"status":"deleted"}` and removes the row from `strazactl users list`.

## From midPoint

In midPoint the persona archetype and a sponsor reference on the user produce `userType`, `agencyMode` and `sponsor` through the resource's extension mappings `strazaUserType`, `strazaAgencyMode` and `strazaSponsor`, so an agent arrives this way with no per-agent mapping. `identity/midpoint.md` has the resource and the account role.

## What a certifier sees

The agent's SCIM read carries what the identity manager claims, `userType`, `agencyMode` and `sponsor`, beside what Straza recorded, `kind`, `origin` and `locked`, and the direct role assignments under `groups`. The manager is never read back, only the sponsor it set. A `userType` `agent` with `kind` `human` means the create carried no agent schema, and that disagreement is itself a certification signal. The admin read adds `effective_roles` and `nhi_key_registered`.

## Caveats

- Humans are excluded from the key grant on purpose, so a stolen key can never become a password-equivalent login for a person.
- In the enterprise profile an agent has a second lane: the identity provider's own client-credentials grant, where the agent holds a client id and secret at the identity provider and strazad resolves the token's subject to the provisioned identity's `externalId`. An agent with a registered key uses the key, an agent without one uses the identity provider.
- `kind` is fixed at creation. An identity created without the agent schema and without `userType` `agent` is recorded as `human`, and the only way to change that is to create the account again, so send the type on the first create.
- An AI agent never decides an approval, whatever role it holds, and never sponsors another agent.
- On a standalone server there is no identity manager feeding SCIM, so `strazactl users create-nhi <username> --type agent --display "<display name>"` creates the identity instead, and the role is assigned with `strazactl assign <role> --user <username>`.
