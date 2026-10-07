# Okta

Okta provisions people into Straza the way it provisions any SCIM 2.0 application: it creates users, updates their attributes and deactivates them. Roles are different. Straza roles are created in Straza and appear to Okta as groups it imports, and the one thing Okta writes on a group is its membership, which is the role assignment. Deactivating a person in Okta cuts every Straza session that person has. The public page is https://docs.straza.ai/guides/connect-identity/okta/. Its Okta console steps are described from Okta's SCIM integration settings and were not clicked through, while the requests they produce were replayed with curl against strazad at `http://localhost:8420`. Treat the requests as ground truth and the console steps as Okta's documentation.

You need Okta administration for the app integration and a Straza administrator login for `strazactl`. The examples hold the token in `ADMIN_API_TOKEN`. That token is Okta's credential and never yours, because `scim:write` assigns roles through membership. The person mints it, revokes it and runs the requests that carry it in their own terminal, and inside a coding agent `strazactl` refuses the mint on the person's login.

## 1. Mint the token

Okta presents an admin API token on `/scim/v2`. Its scope carries `scim:read` for the imports and `scim:write` for the pushes:

```sh
strazactl api-token create --name <token name> --scope scim:read,scim:write
```

```text
id:      <token id>
name:    <token name>
scope:   scim:read,scim:write
expires: never
token:   wat_

Store this token now. It is not retrievable again.
```

The value is cut after `wat_`. Name your own token after the provider it serves, for example `okta-prod`.

## 2. Configure the Okta app integration

Create a SCIM 2.0 app integration in Okta, or enable provisioning on the app you already use for Straza logins, and set its integration fields as the table lists.

| Okta field | Value |
|---|---|
| SCIM connector base URL | your strazad address followed by `/scim/v2` |
| Unique identifier field for users | `userName` |
| Supported provisioning actions | Import New Users and Profile Updates, Push New Users, Push Profile Updates, Import Groups, Push Groups |
| Authentication mode | HTTP Header, the admin API token from the previous step |

Turn Push Groups on, and later link groups rather than create them. Group Push is the Okta feature that sends group membership, and a group Okta imported from an app is mastered by that app, so assigning people in Okta to the imported group alone sends Straza nothing. A pushed group that Okta would create answers `501`, because roles are created in Straza. This step follows Okta's documentation of Group Push and was not clicked through.

## 3. Map the attributes

| Okta attribute | SCIM attribute | Note |
|---|---|---|
| `userName` | `userName` | required, unique |
| Okta user id or `employeeNumber` | `externalId` | equal to the OIDC `sub` when Okta is also the login provider |
| `displayName` | `displayName` | |
| `title` | `title` | |
| `email` | `emails[0].value` | the primary email is stored |
| status | `active` | boolean or the strings `"True"` and `"False"` |

Attributes outside this set are ignored on write, so a richer Okta profile does no harm.

## 4. What Okta sends

Okta creates a user with a full document. Straza answers `201` and stamps `origin: scim`:

```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/scim+json' \
  -X POST http://localhost:8420/scim/v2/Users -d '{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"externalId":"<okta user id>","userName":"<username>","displayName":"<display name>","emails":[{"value":"<email>","primary":true}],"active":true}' \
  | jq -c '{id, userName, externalId, active, ext: .["urn:straza:params:scim:schemas:extension:2.0:User"]}'
```

```text
{"id":"<user id>","userName":"<username>","externalId":"<okta user id>","active":true,"ext":{"kind":"human","locked":false,"origin":"scim"}}
```

Profile updates arrive as a `PUT` of the whole document to `/scim/v2/Users/<user id>`. Straza replaces the mapped attributes and answers with the stored user. Deactivation is a `PATCH` of `active`, and Straza accepts the boolean and the string forms, so Okta's boolean and the string replayed here both work. Straza deactivates the user, revokes every session and removes its per-user grants:

```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/scim+json' \
  -X PATCH http://localhost:8420/scim/v2/Users/<user id> -d '{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Replace","path":"active","value":"False"}]}' \
  | jq -c '{id, userName, active}'
```

```text
{"id":"<user id>","userName":"<username>","active":false}
```

A `DELETE` answers `204` and deactivates as well. Straza never removes a row the identity manager created, so a later create with the same `userName` revives it with the same id and none of the old assignments, which is what happens when Okta re-assigns the app to a returning employee.

## 5. Roles as groups

Straza has no group object. Each role renders as a group whose `displayName` is the role name and whose `members` are its direct holders. Import the groups into Okta so the roles are visible there. Then, for each role, take an Okta group whose name is exactly the Straza role name, add the people to it, and push it under Push Groups with Link Group onto the imported group of that name. Okta then writes the linked group's membership, as a `PATCH` of `members` or as a full-document `PUT`. Straza applies both, and on a `PUT` it applies the members and ignores the name:

```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/scim+json' \
  -X PUT http://localhost:8420/scim/v2/Groups/<role id> -d '{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"displayName":"Okta Developers","members":[{"value":"<user id>"}]}' \
  | jq -c '{displayName, members: [.members[].display]}'
```

```text
{"displayName":"<role>","members":["<username>"]}
```

Group Push would create the group first, and that answers `501` with the reason in `detail`:

```text
{"detail":"roles are born in Straza, so the IdM cannot create or delete one over SCIM. Create the role in Straza, import it as a group, and assign membership from the IdM","schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"501"}
```

An explicit rename of a group answers `400` with `scimType` `mutability`, because policies reference roles by name and a rename in Okta would leave them dangling. A `PATCH` that replaces `displayName` answers `400` even when the name is unchanged. If Okta reports a `mutability` error on a push, Okta sent the group name in its update: tell the person, read the group's `members` to see what Straza holds, and do not work around it by assigning the role in Straza, because the next reconciliation removes that assignment.

## Verify

`strazactl users list` shows every Okta-born account with `ORIGIN` `scim`, and `strazactl catalog preview --user <username>` with an Okta-assigned user resolves the roles the group memberships assigned.

## Undo

Revoking the token stops provisioning at once. `strazactl api-token revoke <token id>` prints `revoked <token id>`, and the next request with the old token answers `401`.

## What a certifier sees

In Okta the person is a member of the imported group, one per Straza role. The group's SCIM read carries the read-only extension `urn:straza:params:scim:schemas:extension:2.0:Group` with `roleKind`, `plane` and the computed `apps`, `tools` and `policies`, so an Okta administrator reviewing membership can read what the group reaches from the same document. The person's own SCIM read lists the direct assignments under `groups` and carries `kind`, `origin` and `locked` as Straza's read-only facts.

## Caveats

- Straza roles render like every other group, so an Okta administrator who can assign group membership can assign `straza-admin`. Scope who may manage those groups in Okta the way you scope any admin role.
- Okta reactivation lifts what Okta deactivated and nothing else. A lock placed with `strazactl users lock` stays until a Straza administrator lifts it, and existing sessions stay revoked, so the person enrolls again.
- When Okta is also the login provider, map `externalId` to the value the ID token carries as `sub`, because strazad matches a verified token to the account by `externalId` first and by `preferred_username` against `userName` second.
