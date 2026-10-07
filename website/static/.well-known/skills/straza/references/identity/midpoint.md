# midPoint

midPoint stays the master of who exists, and Straza is one resource in it. A person or an agent gets a Straza account by holding a midPoint role that carries the account, and gets access by holding an imported Straza role, because assigning that role writes the membership over SCIM. Deactivating the user in midPoint deactivates the account in Straza, revokes every session it has and removes its per-user grants. Deleting the user deactivates the account too, and Straza keeps the row so the audit history stays whole. The public page is https://docs.straza.ai/guides/connect-identity/midpoint/, walked against midPoint 4.10 over its REST interface.

You need a Straza administrator login for `strazactl`, a midPoint administrator, and a midPoint that can reach your strazad address. The walk's midPoint answered at `http://localhost:8087/midpoint` and reached strazad as `http://strazad:8420`. On a standalone host a midPoint-born person who needs to sign in also needs `strazactl users set-password`. The token below is midPoint's credential and never yours, because `scim:write` assigns roles through membership. The person mints it and runs every `strazactl` change on this page in their own terminal, and inside a coding agent `strazactl` refuses those on the person's login. Angle-bracket values below are placeholders. The XML tags around them are real.

## 1. Mint the token

midPoint holds one long-lived admin API token for both directions. `scim:read,scim:write` opens `/scim/v2` for the users and the membership writes, and the read scopes open the evidence classes on the admin API:

```sh
strazactl api-token create --name <token name> --scope scim:read,scim:write,identity:read,apps:read,changes:read,config:read
```

The output is the token's id, name, scope, expiry and the value, shown once and ending with `Store this token now. It is not retrievable again.` Name it after the provider, for example `midpoint-prod`. Paste it into the resource definition and nowhere else. The console mints the same token under Settings, tab "API tokens", where the IGA connector preset fills in this scope.

## 2. Import the Straza resource

The reference integration is one midPoint resource on universal-rest-connector, a separate ConnId connector, with the connector class `ai.straza.connector.rest.UniversalRestConnector`, configured with `baseUrl` (your strazad address) and `apiToken` (the value above). The connector presents the token on `/scim/v2` for users and role membership and on the admin API for the evidence classes that read MCP servers, tools and the change feed. Roles reach midPoint as SCIM groups and never cross the admin API. The account object type maps:

| midPoint item | SCIM attribute | Note |
|---|---|---|
| `name` | `userName` | required, the account identifier |
| `fullName` | `displayName` | |
| `title` | `title` | job or function line |
| `emailAddress` | `emails[0].value` | |
| extension `strazaUserType` | `userType` | `human` or `agent`, induced by the persona archetype |
| extension `strazaAgencyMode` | `agencyMode` | `autonomous`, `supervised` or `interactive` |
| extension `strazaSponsor` | `sponsor` | the accountable human behind an agent |
| activation `administrativeStatus` | `active` | native activation, the kill switch |

Roles are Straza-born and midPoint imports them. The same resource reads every Straza role through the SCIM Groups surface and materializes it as a midPoint role whose name is a code plus the role name, carrying the Straza role metarole, whose construction turns an assignment of the imported role into a members write on the wire group. The code is midPoint's own word for what a role does: a business role reads `BR:` because it composes other roles, and every other kind reads `AR:` because it is an entitlement inside one system. That Straza sent the role is the archetype's job to say, one of Straza business role, Straza application role, Straza approver role and Straza role. One more midPoint role, `AR:Straza:Account`, owns the account construction itself, so a person keeps the account while roles come and go and loses it with the last role. Import the resource, the metarole and the account role, then run a reconciliation of the group object class once so the imported roles exist before you assign any.

## 3. Create a person and watch it arrive

A user needs two assignments: the account role and a persona archetype. On the demo stack the two object identifiers are the seeded `AR:Straza:Account` role and the `employee` archetype. Use your own.

```sh
cat > <username>.xml <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<user xmlns="http://midpoint.evolveum.com/xml/ns/public/common/common-3">
    <name><username></name>
    <fullName><display name></fullName>
    <emailAddress><email></emailAddress>
    <title><job title></title>
    <assignment>
        <targetRef oid="<account role oid>" type="RoleType"/>
    </assignment>
    <assignment>
        <targetRef oid="<archetype oid>" type="ArchetypeType"/>
    </assignment>
</user>
EOF
curl -s -D - -o /dev/null -u '<administrator>:<password>' -H 'Content-Type: application/xml' \
  -X POST --data-binary @<username>.xml http://localhost:8087/midpoint/ws/rest/users
```

midPoint answers `HTTP/1.1 201` with a `Location` header that carries the new user's oid, and provisions the account while it answers, so `strazactl users list` already shows the row with `STATUS` `active` and `ORIGIN` `scim`. The SCIM render of the same account shows what midPoint sent and what Straza concluded, with `kind` and `origin` as read-only facts of Straza's own:

```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -G http://localhost:8420/scim/v2/Users \
  --data-urlencode 'filter=userName eq "<username>"' | jq -c '.Resources[0] | {userName, title, userType, active, ext: .["urn:straza:params:scim:schemas:extension:2.0:User"]}'
```

```text
{"userName":"<username>","title":"<job title>","userType":"human","active":true,"ext":{"kind":"human","locked":false,"origin":"scim"}}
```

## 4. Assign a role in midPoint

Access is a midPoint assignment of the imported role, `BR:<role>` when its kind is business and `AR:<role>` otherwise, added with a modification body:

```sh
cat > assign.xml <<EOF
<objectModification xmlns="http://midpoint.evolveum.com/xml/ns/public/common/api-types-3"
    xmlns:c="http://midpoint.evolveum.com/xml/ns/public/common/common-3"
    xmlns:t="http://prism.evolveum.com/xml/ns/public/types-3">
  <itemDelta>
    <t:modificationType>add</t:modificationType>
    <t:path>c:assignment</t:path>
    <t:value><c:targetRef oid="$AR_ROLE_OID" type="c:RoleType"/></t:value>
  </itemDelta>
</objectModification>
EOF
curl -s -o /dev/null -w 'HTTP %{http_code}\n' -u '<administrator>:<password>' -H 'Content-Type: application/xml' \
  -X PATCH --data-binary @assign.xml http://localhost:8087/midpoint/ws/rest/users/<user oid>
```

It answers `HTTP 204`. `AR_ROLE_OID` is the identifier of the imported role in your midPoint. Within seconds the person's SCIM render lists the role under `groups`, and `strazactl catalog preview --user <username>` resolves the same person to the tools that role reaches, each as `visible`, with the subject line `subject: user=<username> roles=<role>`. Removing the assignment is the same body with `delete` as the modification type, and the render's `groups` list is empty again.

## 5. Cut access from midPoint

Disabling the user in midPoint sends `active: false`. The body is the same modification shape with the path `c:activation/c:administrativeStatus` and the value `disabled`, and it answers `HTTP 204`. Straza sets the account to `disabled`, revokes its sessions, removes its per-user grants and records `user.killed` with `origin` `scim` and the reason `deactivated via SCIM` on the audit chain. `strazactl users list` then shows `STATUS` `disabled`. Setting the status back to `enabled` re-activates the account and the render returns to `active: true`. Sessions revoked in between stay revoked, so the person enrolls again. A lock placed with `strazactl users lock` survives the enable, and midPoint sees it read-only in the user's extension. Deleting the user in midPoint (`DELETE` on the same REST path, `HTTP 204`) deprovisions the account, and Straza keeps the row `disabled`, origin `scim`, with its id, so a person created later with the same name revives it. When midPoint masters the login too, one status change disables both the Straza account and the login, which is the leaver flow.

## What a certifier sees

In midPoint the person holds `AR:Straza:Account` plus one imported role per Straza role, so a certification campaign reviews Straza access as ordinary role assignments. The imported role's SCIM read carries the read-only group extension with `roleKind`, `plane` and the computed `apps`, `tools` and `policies`, so the role has its meaning attached. The account's read carries `kind`, `origin` and `locked` as Straza's own facts beside the `userType`, `agencyMode` and `sponsor` midPoint wrote. The pull direction runs as LiveSync tasks that read `GET /v1/admin/changes` with the same token.

## Caveats

- Every Straza role renders on the wire, so `AR:straza-admin` exists in midPoint and the token's `scim:write` scope can assign administration. Guard that assignment with the same approval and certification policy as any admin-grade role.
- The LiveSync tasks suspend after a strazad restart with a connection error, and midPoint does not resume a task after a fatal error. Check the tasks after any restart. If a new Straza role has not appeared as an imported role, run an import of the group object class from the resource once.
- A midPoint-born agent needs a persona archetype and a sponsor reference, which produce `userType`, `agencyMode` and `sponsor` with no per-agent mapping. `identity/agents.md` covers the agent side.
