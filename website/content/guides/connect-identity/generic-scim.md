---
title: Generic SCIM
description: Your SCIM 2.0 client holds a token for Straza, and you know each request Straza accepts, what it does and how Straza refuses the rest.
summary: >-
  Straza accepts a strict subset of SCIM 2.0 from a client that holds an admin API token with
  the `scim` area. The client creates, updates and deactivates users. Roles are made in Straza
  and reach the client as groups whose only writable fact is membership, and a request outside
  the subset is refused with a SCIM error.
pagetype: how-to
weight: 40
draft: false
tested:
  version: v1.1.0
  platform: A Linux container against the demo stack, with the requests sent with curl
  date: 2026-09-28
applies_to: both
who: You, as the Straza admin, for the SCIM client you build or configure
where: A terminal with strazactl and curl
steps: true
modes: [console, cli]
mode_default: cli
keywords: scim generic profile client
---


You connect a SCIM 2.0 client, such as a commercial IGA, an identity provider or a script, so that it masters who exists in Straza. You work from a terminal with `strazactl` and `curl`. At the end the client holds a token, and you know each request Straza accepts, what it does and how Straza refuses the rest.


Straza implements a strict, documented subset of SCIM 2.0. Your client creates, reads, updates and deactivates users. Roles are created in Straza and appear to your client as groups whose only writable fact is membership. Operations and filters outside the subset are refused with a SCIM error, and attributes outside the mapped set are ignored on write. The endpoint is the same in both profiles.

## Before you start {.nostep}

- `strazactl` with `STRAZA_SERVER` and an admin API token in `STRAZA_API_TOKEN`, or a console session that holds the tokens area. The steps mint another token, which takes `tokens:write`, a root-equivalent grant, so mint a **Full root** token for 30 days in the console as [Mint an admin API token]({{< relref "guides/console/settings.md#mint-an-admin-api-token" >}}) shows, and revoke it when you are done.
- `curl` and `jq` on a machine that reaches strazad.

The examples ran against the [demo stack]({{< relref "evaluate/try-it-on-the-demo-stack.md" >}}) at `http://localhost:8420`.

## Mint a token


Every request carries a long-lived admin API token whose scope names the `scim` area. `scim:read` opens the GET routes and the discovery documents, and `scim:write` opens the writes. The two are independent, so a client that provisions holds both. Straza stores the token's SHA-256 hash and shows the value once.

{{< console >}}
{{< clicks "Settings" "API tokens" "New API token" >}}

Enter `idm-scim` under **Name** and pick a **Lifetime**. Under **The job**, pick **Custom** and set **scim** to **read and write**. Press **Mint token**.

{{< see >}}The sheet `idm-scim is minted`, with the token and a **Copy** button.{{< /see >}}

The console's lifetime starts at `expires in 90 days`, and every request with the token is refused once it expires.
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="Straza administration" >}}
```sh
strazactl api-token create --name idm-scim --scope scim:read,scim:write
```
{{< /command >}}

{{< see >}}`Store this token now. It is not retrievable again.` under the token.{{< /see >}}

```text
id:      1316c606-8b9f-4f14-b590-a74f83297674
name:    idm-scim
scope:   scim:read,scim:write
expires: never
token:   wat_

Store this token now. It is not retrievable again.
```

The token is cut after its first four characters. A token minted without `--ttl` never expires.
{{< /cli >}}

{{< now title="Copy the token now" >}}Straza shows the value once. Put it in your client's configuration and nowhere else.{{< /now >}}

Name the token after the system that holds it. The examples below hold it in `ADMIN_API_TOKEN`. A request without a valid token answers `401`:

{{< command terminal="Terminal" purpose="SCIM, as the client" >}}
```sh
curl -s http://localhost:8420/scim/v2/Users
```
{{< /command >}}

```text
{"detail":"valid token required: an admin API token whose scope carries the scim area (strazactl api-token create --scope scim:read,scim:write for an IdM)","schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"401"}
```

{{< fails >}}
`token lacks scope scim:read (an identity manager needs scim:read,scim:write; strazactl api-token create --scope)`
: The token's scope lacks the `scim` area, and the request answers `403`. A write names `scim:write` in the same sentence. Mint a token with both scopes.
{{< /fails >}}

## What the server accepts {.nostep}


The base path is `/scim/v2`. The content type is `application/scim+json`, and plain JSON is accepted too.

| Endpoint | Accepted | Refused |
|---|---|---|
| `/Users`, `/Users/{id}` | GET with paging, POST, PUT, PATCH, DELETE | any filter other than `userName eq` or `externalId eq` |
| `/Groups`, `/Groups/{id}` | GET with the `displayName eq` filter, PATCH and PUT on members | POST and DELETE, both `501`, and a `displayName` change, `400` |

`/ServiceProviderConfig`, `/Schemas` and `/ResourceTypes` answer GET only, and a POST to `/Bulk` answers `501`. Sorting, ETags and `.search` are outside the subset. The service provider document says the same in the protocol's own words: `patch` supported, `bulk`, `sort`, `etag` and `changePassword` unsupported, `filter` supported with `maxResults` 200.


A user carries these attributes. The extension is `urn:straza:params:scim:schemas:extension:2.0:User`.

| Attribute | Where | Your client | Meaning |
|---|---|---|---|
| `userName` | core | writes | required and unique |
| `externalId` | core | writes | your identifier for the person, which links their logins |
| `displayName` or `name.formatted` | core | writes | `displayName` wins when both are sent |
| `title` | core | writes | a job title or function |
| `emails` | core | writes | the primary value, or the first, is stored |
| `active` | core | writes | a boolean, or the strings `"True"` and `"False"` |
| `userType` | core | writes | `human`, `agent` or `service` |
| `groups` | core | reads | the person's direct role assignments |
| `agencyMode` | extension | writes | `interactive`, `supervised` or `autonomous` |
| `sponsor` | extension | writes | the accountable person behind an AI agent |
| `swarmId` | extension | writes | a fleet label policy can match |
| `ephemeral` | extension | writes | a boolean |
| `locked`, `lockReason`, `lockedAt`, `lockOrigin` | extension | reads | a lock placed in Straza |
| `kind`, `origin` | extension | reads | what Straza recorded at birth, and how the account was born |

One deviation from plain SCIM keeps a classification in place. A `PUT` that leaves out `userType`, `agencyMode`, `sponsor`, `swarmId` or `ephemeral` keeps the stored value, so a client that maps only the core attributes never wipes it. Clear one with a `PATCH` `remove`. Membership is written through `/Groups` only.

## Create, read and deactivate a user


A create needs `userName` and returns the stored resource, with Straza's read-only facts under its extension:

{{< command terminal="Terminal" purpose="SCIM, as the client" >}}
```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/scim+json' \
  -X POST http://localhost:8420/scim/v2/Users -d '{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"externalId":"idm-0001","userName":"mira-novak","displayName":"Mira Novak","emails":[{"value":"mira.novak@example.com","primary":true}],"active":true}'
```
{{< /command >}}

{{< see >}}`"kind":"human"` and `"origin":"scim"` under the Straza extension.{{< /see >}}

```text
{"active":true,"displayName":"Mira Novak","emails":[{"primary":true,"value":"mira.novak@example.com"}],"externalId":"idm-0001","id":"01a0e998-5022-7220-bb7f-f83dbd9c74aa","meta":{"created":"2026-09-28T19:57:48.45014Z","lastModified":"2026-09-28T19:57:48.45014Z","location":"/scim/v2/Users/01a0e998-5022-7220-bb7f-f83dbd9c74aa","resourceType":"User"},"schemas":["urn:ietf:params:scim:schemas:core:2.0:User","urn:straza:params:scim:schemas:extension:2.0:User"],"urn:straza:params:scim:schemas:extension:2.0:User":{"kind":"human","locked":false,"origin":"scim"},"userName":"mira-novak"}
```


`kind` records what Straza concluded at birth, `human` here because no agent schema was sent. `origin` records that the account was provisioned. Both are read-only, and a PATCH against either answers `400` with `scimType` `mutability`. Reading by id or by filter returns the same document:

{{< command terminal="Terminal" purpose="SCIM, as the client" >}}
```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -G http://localhost:8420/scim/v2/Users \
  --data-urlencode 'filter=userName eq "mira-novak"' | jq -c '{totalResults, first: .Resources[0].userName}'
```
{{< /command >}}

```text
{"totalResults":1,"first":"mira-novak"}
```


A PATCH of `active` to `false` deactivates the user. Straza revokes its sessions, removes its per-user grants on connected servers and emits the revocation event. The response shows the new state:

{{< command terminal="Terminal" purpose="SCIM, as the client" >}}
```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/scim+json' \
  -X PATCH http://localhost:8420/scim/v2/Users/01a0e998-5022-7220-bb7f-f83dbd9c74aa -d '{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":false}]}' | jq -c '{userName, active}'
```
{{< /command >}}

{{< see >}}`"active":false`.{{< /see >}}

```text
{"userName":"mira-novak","active":false}
```


A DELETE answers `204` and deactivates too. The user stays addressable afterwards, so a GET on the same id answers `200` with `active: false`. Creating the same `userName` again answers `201` with the same id and the new attributes. The local password and every per-user grant went at deactivation, so the revived account returns with neither. Straza never removes an account the identity manager created, and the cost is a list of disabled rows that only an administrator's delete clears.

## Read a role and write its membership


Every Straza role renders as a group. Your client finds one by name and reads what it grants in a read-only extension:

{{< command terminal="Terminal" purpose="SCIM, as the client" >}}
```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -G http://localhost:8420/scim/v2/Groups --data-urlencode 'filter=displayName eq "scout-tools-readers"' | jq -c '.Resources[0] | {id, displayName, members, ext: .["urn:straza:params:scim:schemas:extension:2.0:Group"]}'
```
{{< /command >}}

{{< see >}}The group's `id`, which the membership writes below take, and its `tools`.{{< /see >}}

```text
{"id":"01a11225-2c29-708f-a8e7-69223aaba963","displayName":"scout-tools-readers","members":[],"ext":{"apps":["scout-tools"],"description":"Tool access to the scout-tools server","plane":"access","role":"scout-tools-readers","roleKind":"application","server":"scout-tools","tools":["scout-tools:echo","scout-tools:get-sum"]}}
```


The extension is `urn:straza:params:scim:schemas:extension:2.0:Group`. Straza computes it when your client reads the group, and it leaves out an empty list or an empty description.

| Attribute | What it holds |
|---|---|
| `role` | The role name, the same as `displayName`. |
| `roleKind` | `business`, `application`, `approver` or `straza`, fixed when the role is created. |
| `plane` | `access` for the first three kinds and `control` for a Straza role. |
| `description` | The role's description. `strazactl roles update <name> --description` changes it, and so does **Change the description** on the role's page in the console. |
| `apps` | The MCP servers the role reaches, through every role it composes. |
| `tools` | The tools it reaches, as `server:tool`, on servers that run now. |
| `policies` | The active PolicySets whose `match.roles` names the role or a role it composes. A set that matches every session is left out, because the list says what holding this role adds. |
| `administers` | The MCP servers whose admin role the role holds, directly or through composition. |
| `server` | The MCP server that owns the role, on a server-owned application role only. Tell such roles apart by this attribute, never by their name. |
| `covers` | What the role covers on each server that pins an object type, through every role it composes: `<server>:<type>` and then `every`, one named object in quotes, or `none`. |


A `covers` value says which objects one access row reaches. `every` covers every object of the type, and `none` stands for an access row without `covers` or with one Straza could not read, which covers nothing. An access row that names objects gives one value per object, each name a JSON string. A row on two repos gives `objects-demo:repo "Strazahq/docs-site"` and `objects-demo:repo "Strazahq/straza"`, and a repo named every reads `"every"`, so it never passes for every repo. Straza drops duplicate values and sorts the rest, and it never merges two rows, so a role that composes one row with `every` and another with a name shows both. Outside a failed read, Straza never leaves `covers` out on a role with access to a server with a pinned type, because your client would read a missing value as every object. If Straza cannot read what the role reaches, it leaves `covers` out together with `apps` and `tools`, and it renders the rest of the group. Like the rest of the extension, `covers` is read-only, and a PATCH on it answers `400` with `scimType` `mutability`.


A value changes when Straza publishes a change, or when a listing renames an object: a draft that changes the role's covers line, a draft that changes what a business role composes, or one that changes the object type a server pins, as [Propose and publish a change]({{< relref "guides/changes/propose-and-publish.md#covers" >}}) shows. Each value is the name the server's object list holds now. A rename that a listing picks up changes the value, and Straza puts the role and every role that composes it on the change feed. An object the list no longer holds keeps the name it had when the role was published.


Membership is role assignment. A members add creates the direct assignment, a remove deletes it, and a PUT converges the set. Each write is idempotent, so sending it twice changes nothing.

{{< command terminal="Terminal" purpose="SCIM, as the client" >}}
```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/scim+json' \
  -X PATCH http://localhost:8420/scim/v2/Groups/01a11225-2c29-708f-a8e7-69223aaba963 -d '{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"add","path":"members","value":[{"value":"01a0e998-c864-7011-a0fd-3f959fe0a63e"}]}]}' | jq -c '{displayName, members: [.members[].display], grants: .["urn:straza:params:scim:schemas:extension:2.0:Group"].tools}'
```
{{< /command >}}

{{< see >}}`scout-bot` under `members`.{{< /see >}}

```text
{"displayName":"scout-tools-readers","members":["scout-bot"],"grants":["scout-tools:echo","scout-tools:get-sum"]}
```

That member value is the id of `scout-bot`, the agent identity that [AI agents as identities]({{< relref "guides/connect-identity/nhi-provisioning.md" >}}) creates, which is why the answer displays that name.


A remove takes one of two forms. The value form sends the member in `value`, like the add above. The filter form names the member in the path as `members[value eq "<id>"]`, and it is the form an identity manager often sends to remove one member. Straza accepts the filter form with `remove` only. A `remove` on `members` with no value and no filter removes every member, as the protocol defines.


Your client is meant to be the only writer of membership for the roles it manages. A role granted to a person in the console or with `strazactl assign` works at once, but it is drift. The console badges it, and a `PUT` of the group or a `PATCH` that replaces its members removes it, whatever wrote the grant. A `PUT` ignores the `displayName` and `externalId` in its body and applies `members` as the whole set. A remove takes the role away at the person's next check-in, and `active` `false` cuts their sessions at once.

Creating a group answers `501` with `"roles are born in Straza, so the IdM cannot create or delete one over SCIM. Create the role in Straza, import it as a group, and assign membership from the IdM"` in `detail`, and so does deleting one. The code is `501` rather than `403` because identity manager runbooks read `403` as broken credentials.

## Errors {.nostep}


Errors use the SCIM error schema, with `status` as a string and a `detail` sentence. `scimType` is set for four kinds of refusal.

| Status | `scimType` | When |
|---|---|---|
| `400` | `invalidFilter` | A filter other than one equality on `userName` or `externalId`. |
| `400` | `invalidValue` | A create without `userName`, with the detail "userName is required", or a malformed PATCH. |
| `400` | `mutability` | A write to `kind`, `origin`, a lock attribute, a group's `displayName` or the group extension. |
| `401` | none | No token, or one that is unknown, expired or revoked. |
| `403` | none | A token whose scope lacks `scim:read` or `scim:write`. |
| `404` | none | An unknown id. |
| `409` | `uniqueness` | A duplicate `userName` or `externalId`, with the detail "userName or externalId already exists". |
| `501` | none | A POST or DELETE on `/Groups`, and a POST to `/Bulk`. |

The detail of a refused filter names the form the server accepts:

{{< command terminal="Terminal" purpose="SCIM, as the client" >}}
```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -G http://localhost:8420/scim/v2/Users --data-urlencode 'filter=userName co "mira"'
```
{{< /command >}}

```text
{"detail":"unsupported filter \"userName co \\\"mira\\\"\": only `attr eq \"value\"` on [userName externalId] is supported, because Straza looks identities up by exact match only. Send one equality filter on one of those attributes, or list without a filter","schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"scimType":"invalidFilter","status":"400"}
```

## Undo


Remove the membership you added, with the filter form:

{{< command terminal="Terminal" purpose="SCIM, as the client" >}}
```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/scim+json' \
  -X PATCH http://localhost:8420/scim/v2/Groups/01a11225-2c29-708f-a8e7-69223aaba963 -d '{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"remove","path":"members[value eq \"01a0e998-c864-7011-a0fd-3f959fe0a63e\"]"}]}' | jq -c '{displayName, members: [.members[].display]}'
```
{{< /command >}}

{{< see >}}The group answers with `scout-bot` gone from `members`.{{< /see >}}

Revoke the token when the client is retired.

{{< console >}}
{{< clicks "Settings" "API tokens" "Revoke" "Revoke token" >}}

Press **Revoke** on the `idm-scim` row, then **Revoke token** in the question `Revoke idm-scim?`.
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="Straza administration" >}}
```sh
strazactl api-token revoke 1316c606-8b9f-4f14-b590-a74f83297674
```
{{< /command >}}

{{< see >}}`revoked 1316c606-8b9f-4f14-b590-a74f83297674`.{{< /see >}}
{{< /cli >}}

The next request with that token answers `401` with the detail `admin API token rejected: unknown, expired or revoked (strazactl api-token list)`.

## Caveats {.nostep}


A token with `scim:write` can assign `straza-admin`, because Straza roles render as groups like every other role, and a members add on that group is an assignment. That makes a person an administrator. An AI agent that holds the role still gets no admin route, because only a person can use the admin API. The identity manager is meant to master every role, so this is deliberate. Give the token the protection of an admin credential, and let the approval your identity manager puts on those memberships be your guard.

## Next {.nostep}


- [midPoint]({{< relref "guides/connect-identity/midpoint.md" >}}) and [Okta]({{< relref "guides/connect-identity/okta.md" >}}) apply this profile to one product each.
- [AI agents as identities]({{< relref "guides/connect-identity/nhi-provisioning.md" >}}) provisions an agent with the agent schema.
- [Roles and access]({{< relref "concepts/roles-and-access.md" >}}) explains the four role kinds.
