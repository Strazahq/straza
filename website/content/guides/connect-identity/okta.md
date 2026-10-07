---
title: Okta
description: Okta provisions people and their Straza role membership over SCIM, and you know what each request Okta sends does in Straza.
pagetype: how-to
weight: 20
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: A throwaway standalone server in an Alpine 3.20 container on Linux, with the token minted with strazactl on the admin's login and every SCIM request the page shows replayed with curl on that token, the example role created there as an application role with no server. The Okta console steps were not walked, because no Okta tenant was available, and the Straza console forms of the token, verify and undo steps were checked against the console's source, not clicked
  date: 2026-10-06
applies_to: both
who: You, as the Okta admin and the Straza admin
where: The Okta admin console, and a terminal with strazactl and curl
steps: true
modes: [console, cli]
mode_default: cli
keywords: okta scim provisioning groups
---


You point Okta's SCIM provisioning at Straza, as the admin of both, from the Okta admin console and a terminal. At the end Okta provisions the people it manages into Straza with their Straza role membership. You also know what each request Okta sends does on the Straza side.


Okta provisions people into Straza the way it provisions any SCIM 2.0 application. It creates users, updates their attributes and deactivates them. Roles work differently. Straza roles are created in Straza and appear to Okta as groups it imports, and the one thing Okta writes on a group is its membership, which is the role assignment. Deactivating a person in Okta cuts every Straza session that person has.

## Before you start {.nostep}

- Okta administration for the app integration.
- A Straza administrator login for `strazactl`, or a console session that holds the tokens area.

The Okta console steps below follow Okta's SCIM integration settings and were not run against an Okta tenant. The requests Okta sends were replayed with curl against a standalone server at `http://localhost:8420`.

## Mint the token


Okta presents an admin API token on `/scim/v2`. Its scope carries `scim:read` for the imports and `scim:write` for the pushes. Straza shows the value once and keeps only a hash.

{{< console >}}
{{< clicks "Settings" "API tokens" "New API token" >}}

Enter a name such as `okta-prod` under **Name** and pick a **Lifetime**. Under **The job**, pick **Custom** and set **scim** to **read and write**. Press **Mint token**.

{{< see >}}The sheet `okta-prod is minted`, with the token and a **Copy** button.{{< /see >}}

The console's lifetime starts at `expires in 90 days`, and Okta's provisioning stops once the token expires.
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="Straza administration" >}}
```sh
strazactl api-token create --name idm-scim --scope scim:read,scim:write
```
{{< /command >}}

{{< see >}}`Store this token now. It is not retrievable again.` under the token.{{< /see >}}

```text
id:      8026b17c-3568-4018-b817-065b8ccf8e84
name:    idm-scim
scope:   scim:read,scim:write
expires: never
token:   wat_

Store this token now. It is not retrievable again.
```

The token is cut after its first four characters. Name your own token after the provider it serves, for example `okta-prod`.
{{< /cli >}}

{{< now title="Copy the token now" >}}Straza shows the value once. Paste it into the Okta app integration in the next step.{{< /now >}}

The examples below hold the token in `ADMIN_API_TOKEN`.

## Configure the Okta app integration


In Okta, create a SCIM 2.0 app integration, or enable provisioning on the app you already use for Straza logins. Set its integration fields as the table lists.

| Okta field | Value |
|---|---|
| SCIM connector base URL | your strazad address followed by `/scim/v2` |
| Unique identifier field for users | `userName` |
| Supported provisioning actions | Import New Users and Profile Updates, Push New Users, Push Profile Updates, Push Groups, Import Groups |
| Authentication mode | HTTP Header, the admin API token from the previous step |

## Map the attributes


Map the Okta profile onto the attributes Straza stores. Straza ignores every other attribute on write, so a richer Okta profile does no harm.

| Okta attribute | SCIM attribute | Note |
|---|---|---|
| `userName` | `userName` | required, unique |
| Okta user id or `employeeNumber` | `externalId` | equal to the OIDC `sub` when Okta is also the login provider |
| `displayName` | `displayName` | |
| `title` | `title` | |
| `email` | `emails[primary eq true].value` | the email marked primary is stored, or the first when none is |
| status | `active` | boolean or the strings `"True"` and `"False"` |

[Generic SCIM]({{< relref "guides/connect-identity/generic-scim.md#what-the-server-accepts" >}}) lists every user attribute Straza accepts, the AI agent fields included.

## What Okta sends for a user {.nostep}


Okta creates a user with a full document. Straza answers `201` and stamps `origin: scim`:

{{< command terminal="Terminal" purpose="SCIM, replaying Okta" >}}
```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/scim+json' \
  -X POST http://localhost:8420/scim/v2/Users -d '{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"externalId":"00ub7k2f9qLm3Xr4W5d6","userName":"tom-hall","displayName":"Tom Hall","emails":[{"value":"tom.hall@example.com","primary":true}],"active":true}' \
  | jq -c '{id, userName, externalId, active, ext: .["urn:straza:params:scim:schemas:extension:2.0:User"]}'
```
{{< /command >}}

```text
{"id":"01a1130c-b29a-7136-a297-700d5d59af1b","userName":"tom-hall","externalId":"00ub7k2f9qLm3Xr4W5d6","active":true,"ext":{"kind":"human","locked":false,"origin":"scim"}}
```


Profile updates arrive as a `PUT` of the whole document. Straza replaces the mapped attributes and answers with the stored user:

{{< command terminal="Terminal" purpose="SCIM, replaying Okta" >}}
```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/scim+json' \
  -X PUT http://localhost:8420/scim/v2/Users/01a1130c-b29a-7136-a297-700d5d59af1b -d '{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"id":"01a1130c-b29a-7136-a297-700d5d59af1b","externalId":"00ub7k2f9qLm3Xr4W5d6","userName":"tom-hall","displayName":"Tom Hall (renamed)","name":{"givenName":"Tom","familyName":"Hall"},"emails":[{"value":"tom.hall@example.com","primary":true,"type":"work"}],"active":true}' \
  | jq -c '{id, userName, displayName, active}'
```
{{< /command >}}

```text
{"id":"01a1130c-b29a-7136-a297-700d5d59af1b","userName":"tom-hall","displayName":"Tom Hall (renamed)","active":true}
```


Deactivation is a `PATCH` of `active`. Straza accepts the boolean and the strings `"True"` and `"False"`, which it takes for Azure AD compatibility, so Okta's boolean and the string replayed below both work. Straza deactivates the user, revokes every session and removes its per-user grants:

{{< command terminal="Terminal" purpose="SCIM, replaying Okta" >}}
```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/scim+json' \
  -X PATCH http://localhost:8420/scim/v2/Users/01a1130c-b29a-7136-a297-700d5d59af1b -d '{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Replace","path":"active","value":"False"}]}' \
  | jq -c '{id, userName, active}'
```
{{< /command >}}

```text
{"id":"01a1130c-b29a-7136-a297-700d5d59af1b","userName":"tom-hall","active":false}
```


A `DELETE` answers `204` and deactivates as well. Straza never removes a row the identity manager created. A later create with the same `userName` revives it with the same id and none of the old per-user grants, which is what happens when Okta re-assigns the app to a returning employee.

## Link Okta groups to Straza roles


Straza exposes each role as a SCIM group. Its `displayName` is the role name, and its `members` are the direct holders. Create the role in Straza first, because Straza refuses a group that Okta tries to create. The example role `scout-tools-readers` comes from [Access per role]({{< relref "guides/serve-mcp-apps/catalogs-per-role.md" >}}).

Okta documents Group Linking to existing target groups through its Push Groups workflow, where the integration supports it. See [Okta's Group Linking instructions](https://help.okta.com/en-us/content/topics/users-groups-profiles/usgp-configure-enhanced-group-push.htm). The Okta group-linking setup was not verified against a tenant for this page, so check the integration's linking and membership behavior before you rely on it for assignments.

The request below replays a membership update of an existing role. It does not show that an Okta import alone sets up outbound membership updates. The user is the one created above, whom the replay deactivated, and Straza still records the membership:

{{< command terminal="Terminal" purpose="SCIM, replaying Okta" >}}
```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/scim+json' \
  -X PUT http://localhost:8420/scim/v2/Groups/01a1130c-ecf3-70e0-9512-cc81b878c3db -d '{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"displayName":"Okta Developers","members":[{"value":"01a1130c-b29a-7136-a297-700d5d59af1b"}]}' \
  | jq -c '{displayName, members: [.members[].display]}'
```
{{< /command >}}

{{< see >}}The group keeps its role name, and `tom-hall` is its member.{{< /see >}}

```text
{"displayName":"scout-tools-readers","members":["tom-hall"]}
```

Straza ignores the `displayName` in the document, so the group keeps its role name. A `PUT` converges the whole set, so a member the document leaves out loses the assignment, whatever wrote it. Straza also accepts the remove that names one member in the path, `members[value eq "<id>"]`, as [Generic SCIM]({{< relref "guides/connect-identity/generic-scim.md#read-a-role-and-write-its-membership" >}}) shows.


Okta is meant to be the only writer of membership for the roles it pushes. A role you grant a person in the console or with `strazactl assign` works at once, but it is drift. The console badges it, and the next `PUT` of the group, or a `PATCH` that replaces its members, removes it.

{{< fails >}}
`roles are born in Straza, so the IdM cannot create or delete one over SCIM. Create the role in Straza, import it as a group, and assign membership from the IdM`
: A Group Push that is not linked to an existing group tries to create one, and Straza answers `501` with this detail. Create the role in Straza, then link the Okta group to it.
{{< /fails >}}

An explicit rename of a group answers `400` with `scimType` `mutability`. A role's name is fixed when the role is created, because policies reference roles by name. A new name means a new role, created in Straza.

## Verify


Check that the people Okta pushed arrived, and what their roles give them.

{{< console >}}
{{< clicks "Users" >}}

{{< see >}}Each Okta-born account with the origin badge **SCIM**.{{< /see >}}

The console previews what a role reaches, never what one person reaches. That check runs with `strazactl catalog preview --user`.
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="Straza administration" >}}
```sh
strazactl users list
strazactl catalog preview --user <username>
```
{{< /command >}}

{{< see >}}`ORIGIN` `scim` on each Okta-born account, and the preview's `subject` line with the roles the group memberships assigned.{{< /see >}}
{{< /cli >}}

## Undo


Revoking the token stops provisioning at once.

{{< console >}}
{{< clicks "Settings" "API tokens" "Revoke" "Revoke token" >}}

Press **Revoke** on the token's row, then **Revoke token**.
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="Straza administration" >}}
```sh
strazactl api-token revoke 8026b17c-3568-4018-b817-065b8ccf8e84
```
{{< /command >}}

{{< see >}}`revoked 8026b17c-3568-4018-b817-065b8ccf8e84`.{{< /see >}}
{{< /cli >}}

The next request with the old token answers `401` with the detail "admin API token rejected: unknown, expired or revoked (strazactl api-token list)".

## Caveats {.nostep}


Straza roles render like every other group, so an Okta administrator who can assign group membership can assign `straza-admin`. That makes a person an administrator, while an AI agent that holds the role still gets no admin route, because only a person can use the admin API. The same holds for `straza-global-mcp-admin`, which administers every MCP server, and for each server's admin role, whose group is the server's name behind the prefix `mcp-admin-`. Scope who may manage those groups in Okta the way you scope any admin-grade role.


Okta reactivation lifts what Okta deactivated and nothing else. A lock placed with `strazactl users lock` stays until a Straza administrator lifts it. Existing sessions stay revoked, so the person enrolls again.

## Next {.nostep}

- [Generic SCIM]({{< relref "guides/connect-identity/generic-scim.md" >}}) lists every request Straza accepts and every refusal.
- [Keycloak login]({{< relref "guides/connect-identity/keycloak-login.md" >}}) shows how strazad trusts a login provider and links a person's login to their account.
