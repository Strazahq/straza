# Generic SCIM

Straza implements a strict, documented subset of SCIM 2.0, and anything outside it is refused with a proper SCIM error rather than accepted and ignored. Users are created, read, updated and deactivated by your client. Roles are created in Straza and appear to your client as groups whose only writable fact is membership. The endpoint is the same in both profiles, so any SCIM 2.0 client, a commercial IGA, a script or an identity provider, can master who exists. The public page is https://docs.straza.ai/guides/connect-identity/generic-scim/, walked with curl against strazad at `http://localhost:8420`.

You need a Straza administrator login for `strazactl` and `curl` on a machine that reaches strazad. The examples hold the token in `ADMIN_API_TOKEN`. That token is the identity manager's credential and never yours, because `scim:write` assigns roles through membership. The person mints it, revokes it and runs the requests that carry it in their own terminal, and inside a coding agent `strazactl` refuses the mint on the person's login.

## 1. Mint the token

Every request carries a long-lived admin API token whose scope names the `scim` area. `scim:read` opens the GET routes and the discovery documents, `scim:write` opens the writes, and the two are independent, so a client that provisions holds both:

```sh
strazactl api-token create --name <token name> --scope scim:read,scim:write
```

The output is the token's id, name, scope, expiry and the value, shown once and cut after `wat_`. A request without a valid token answers `401`:

```sh
curl -s http://localhost:8420/scim/v2/Users
```

```text
{"detail":"valid token required: an admin API token whose scope carries the scim area (strazactl api-token create --scope scim:read,scim:write for an IdM)","schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"401"}
```

A token whose scope lacks the area answers `403` instead, and the detail names the missing scope: `scim:read` for a GET, `scim:write` for anything else.

## 2. What the server accepts

The base path is `/scim/v2` and the content type is `application/scim+json`, with plain JSON accepted too.

| Endpoint | Accepted | Refused |
|---|---|---|
| `/Users`, `/Users/{id}` | GET with paging, POST, PUT, PATCH, DELETE | any filter other than `userName eq` or `externalId eq` |
| `/Groups`, `/Groups/{id}` | GET with the `displayName eq` filter, PATCH and PUT on members | POST and DELETE, both `501`, and a `displayName` change, `400` |

`/ServiceProviderConfig`, `/Schemas` and `/ResourceTypes` answer GET only, and a POST to `/Bulk` answers `501`. Sorting, ETags and `.search` are outside the subset. The service provider document says the same in the protocol's own words: `patch` supported, `bulk`, `sort`, `etag` and `changePassword` unsupported, `filter` supported with `maxResults` 200. `/Schemas` lists the two Straza extensions, `urn:straza:params:scim:schemas:extension:2.0:User` and `urn:straza:params:scim:schemas:extension:2.0:Group`, and `/ResourceTypes` names each as a non-required `schemaExtensions` entry. Read these three before configuring a client, because they are the discovery documents the grounding rule asks for.

## 3. Create, read, update and deactivate a user

A create needs `userName` and returns the stored resource with Straza's read-only facts under its extension:

```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/scim+json' \
  -X POST http://localhost:8420/scim/v2/Users -d '{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"externalId":"<external id>","userName":"<username>","displayName":"<display name>","emails":[{"value":"<email>","primary":true}],"active":true}'
```

The answer carries `id`, `meta`, the mapped attributes and `"urn:straza:params:scim:schemas:extension:2.0:User":{"kind":"human","locked":false,"origin":"scim"}`. `kind` records what Straza concluded at birth, `human` here because no agent schema was sent, and `origin` records that the account was provisioned. Both are read-only, and a PATCH against either answers `400` with `scimType` `mutability`. Reading by id or by filter returns the same document:

```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -G http://localhost:8420/scim/v2/Users \
  --data-urlencode 'filter=userName eq "<username>"' | jq -c '{totalResults, first: .Resources[0].userName}'
```

```text
{"totalResults":1,"first":"<username>"}
```

A PATCH of `active` to `false` deactivates the user, revokes its sessions, removes its per-user grants on connected servers and emits the revocation event:

```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/scim+json' \
  -X PATCH http://localhost:8420/scim/v2/Users/<user id> -d '{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":false}]}' | jq -c '{userName, active}'
```

```text
{"userName":"<username>","active":false}
```

A DELETE answers `204` and deactivates too. The user stays addressable afterwards, so a GET on the same id answers `200` with `active: false`, and creating the same `userName` again answers `201` with the same id and the new attributes. The local password and every per-user grant went at deactivation, so the revived account returns with neither.

## 4. Errors

Errors use the SCIM error schema with `status` as a string and a `detail` sentence, and `scimType` is set for `invalidFilter`, `invalidValue`, `uniqueness` and `mutability`. An unsupported filter answers `400` with the detail `unsupported filter "userName co \"b2\"": only `attr eq "value"` on [userName externalId] is supported, because Straza looks identities up by exact match only. Send one equality filter on one of those attributes, or list without a filter`. A duplicate `userName` answers `409` with `scimType` `uniqueness`, a create without `userName` answers `400` with `invalidValue`, and an unknown id answers `404`.

## 5. Roles as groups

Every Straza role renders as a group. Your client discovers one by name and reads the access it gives in a read-only extension:

```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -G http://localhost:8420/scim/v2/Groups --data-urlencode 'filter=displayName eq "<role>"' | jq -c '.Resources[0] | {id, displayName, members, ext: .["urn:straza:params:scim:schemas:extension:2.0:Group"]}'
```

```text
{"id":"<role id>","displayName":"<role>","members":[],"ext":{"description":"<role description>","plane":"access","role":"<role>","roleKind":"application"}}
```

Membership is role assignment. A members add creates the direct assignment, a remove deletes it, and a PUT converges the set:

```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/scim+json' \
  -X PATCH http://localhost:8420/scim/v2/Groups/<role id> -d '{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"add","path":"members","value":[{"value":"<user id>"}]}]}' | jq -c '{displayName, members: [.members[].display], grants: .["urn:straza:params:scim:schemas:extension:2.0:Group"].tools}'
```

```text
{"displayName":"<role>","members":["<username>"],"grants":["<app>:echo","<app>:get-sum"]}
```

Creating a group answers `501` with `"roles are born in Straza, so the IdM cannot create or delete one over SCIM. Create the role in Straza, import it as a group, and assign membership from the IdM"` in `detail`, and so does deleting one. The code is `501` rather than `403` because identity manager runbooks read `403` as broken credentials.

## Undo

Revoke the token when the client is retired. `strazactl api-token revoke <token id>` prints `revoked <token id>`, and the next request with that token answers `401` with the detail `admin API token rejected: unknown, expired or revoked (strazactl api-token list)`.

## What a certifier sees

A group read is what a certifier reviews: `displayName` is the role, `members` are the direct holders, and the extension carries `roleKind`, `plane`, `description` and the computed `apps`, `tools` and `policies`. The `tools` list above is the same access `strazactl catalog preview --user <username>` shows as `visible` for any holder. A user read carries the direct assignments under `groups`, the `userType`, `agencyMode` and `sponsor` the identity manager writes, and Straza's own `kind`, `origin` and `locked`.

## Caveats

- A token with `scim:write` can assign `straza-admin`, because Straza roles render as groups like every other role and a members add on that group is an assignment. The token deserves the protection of an admin credential, and the approval on those memberships in the identity manager is your guard.
- Straza never removes an account the identity manager created. The cost is a list of disabled rows that only an administrator's delete clears.
- The PUT of a user leaves absent typology attributes untouched, so a client that maps only the core attributes cannot wipe `userType`, `agencyMode` or `sponsor` on a reconciliation. Clearing one is an explicit PATCH `remove`.
