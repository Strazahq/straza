# Identity: mapping an identity manager onto Straza

Read this before proposing any role mapping. It covers the identity model, the role kinds, how membership arrives, the grounding steps, the one token, and the gotchas. The concrete walks per manager are the sibling files `identity/midpoint.md`, `identity/okta.md`, `identity/generic-scim.md` and `identity/agents.md`. The public pages are https://docs.straza.ai/concepts/identities/ and the guides under https://docs.straza.ai/guides/connect-identity/.

## The identity model

Users. Every action Straza governs is taken by a session, and every session belongs to a user. A user is human or non-human. Straza records that kind at creation and never changes it. A user is non-human when its SCIM create carried the agent schema `urn:ietf:params:scim:schemas:extension:agent:2.0:Agent`, or when an administrator created it with `strazactl users create-nhi <username> --type agent|service`. Beside that fixed kind the identity manager masters a typology it may change at any time: `userType` (`human`, `agent` or `service`), `agencyMode` (`interactive`, `supervised` or `autonomous`), `sponsor`, `swarmId` and `ephemeral`. A user typed `agent` or `service` counts as non-human everywhere the kind matters, even when the agent schema was never sent. An AI agent never holds a password, never sees a browser and never decides an approval.

Origin. `origin` is `scim` for a user provisioned over SCIM and `local` for one created inside Straza with the CLI or the console. It is read-only for the row's lifetime. `strazactl users list` prints it in the `ORIGIN` column.

Sponsor. The sponsor is the accountable human behind an agent. It is an edge the identity manager masters on the agent's user, the `sponsor` attribute of the Straza SCIM extension, holding a username. Straza resolves it when an approval is raised and stores it on the request, so a later change of sponsor never retargets an open request. A usable sponsor is an active human other than the requester. Straza refuses an agent as a sponsor.

Devices and sessions. A human enrolls a machine once and keeps a device credential that starts sessions without a browser, while an AI agent mints each session token from its registered key and runs deviceless sessions. Each harness run is one session with a token that lives 300 seconds and is named in every decision, approval and audit record. https://docs.straza.ai/security/credentials-and-sessions/ follows a credential through its life.

## The role kinds

Roles are the only thing that carries power: they carry policy, the tools a session can see and the instructions an agent is handed at start. Every role has a kind, set by `strazactl roles create <name> --kind <kind> --description "<text>"`. `--kind` is required, because the CLI takes no silent default, except with `--app <server>`, which makes an application role that belongs to that server. The kind cannot change after create.

| Kind | What it may carry | Who holds it |
|---|---|---|
| `business` | composes application roles, no access rows of its own | people and agents. The role the identity manager assigns and a certification campaign reviews |
| `application` | access rows to tools, the kind a PolicySet matches | composed by business roles |
| `approver` | decide authority for approval requests and nothing else, never tools | the people who answer approval requests |
| `straza` | capabilities in Straza itself, never agent tools | administrators: `straza-admin`, `straza-global-mcp-admin`, `straza-enroll-browser` (enroll a signed-in browser as an approval device) and `straza-enroll-mobile` (enroll one's own phone from the self-service page) |

Composition. `strazactl roles implications add <role> <implied>` makes a holder of `<role>` hold `<implied>` too, and `strazactl roles implications <role>` lists the edges. A session's effective roles are its direct assignments plus everything those roles compose. Policies match application roles, so one rule governs every path to a tool whichever business role reached it. An application role that reaches a server belongs to that server, so the identity manager shows it as that server's role. `strazactl roles create <server>-<word> --app <server> --tools "<tool>,<tool>"` creates it on the server in one step, with its access row: the name must begin with the server's name and a hyphen, it composes no other role, and it is removed with the server. `--tools '*'` gives it every tool, and tools added later, which only a global admin may send. An application role made without `--app` belongs to no server and is for policy rules only, so a new access row for it is refused. An application role reaches one server, so a role that reaches two servers is two application roles composed by one business role. A role's kind and name are fixed at create, so `strazactl roles update <name> --description` changes the description and nothing else. A role of the wrong kind is replaced: create the role you need, move its holders in the identity manager, then delete the old one.

The demo stack's seeded roles show the four kinds, ids trimmed:

```sh
strazactl roles list
```

```text
NAME                     KIND         SERVER      DESCRIPTION
analyst                  business     -           The analyst role: composes demo-tools-readers and midpoint-self-service. Nobody holds it at boot; assign it to nina in midPoint and watch her land in Straza.
auditor                  straza       -           Read-only oversight: audit, sessions and transcripts. Opens the console, never agent tools.
demo-tools-readers       application  demo-tools  Read-only reach into the demo-tools server: the tools that only read or compute, named one by one. Defined by that server's own admin and assigned by the identity manager.
demo-tools-sandbox       application  demo-tools  Every tool of the demo-tools server, tools added later included. Composed by the developer role; get-sum is a hold and get-env is a ticket in demo-tools-sandbox-access.
developer                business     -           The developer role: composes demo-tools-sandbox, every tool of the demo-tools server, and midpoint-self-service, the midPoint reads plus request_role. joe holds it from midPoint.
sec-approvers            approver     -           The deciders of approval requests for governed agents. Reaches no tools. The identity manager assigns and certifies its holders.
straza-admin             straza       -           Straza administration
straza-enroll-browser    straza       -           May enroll a signed-in browser as an approval device.
```

What a certifier sees. Every role renders on `/scim/v2/Groups` as a group whose `id` is the role id, whose `displayName` is the role name and whose `members` are the direct holders. Holders through composition are not listed, because the identity manager cannot remove them here. Each group carries the read-only extension `urn:straza:params:scim:schemas:extension:2.0:Group`: the role's `role`, `roleKind`, `plane` (`access` or `control`) and `description`, then what holding it reaches, `apps`, `tools` spelled `app:tool`, and `policies`, the active PolicySets that name the role. A role that one MCP server owns also carries `server`, that server's name, and a Straza role that is the admin role of servers carries `administers`, their names, so an identity manager reads both from the wire and never from a role's name. The three lists are computed over the implication closure at read time, so a business role shows the tools of the application roles it composes, and empty lists are omitted. A write against any of these attributes answers `400` with `scimType` `mutability`.

```json
"urn:straza:params:scim:schemas:extension:2.0:Group": {
  "role": "demo-tools-readers",
  "roleKind": "application",
  "plane": "access",
  "description": "Read-only reach into the demo-tools server: the tools that only read or compute, named one by one. Defined by that server's own admin and assigned by the identity manager.",
  "server": "demo-tools",
  "apps": ["demo-tools"],
  "tools": ["demo-tools:echo", "demo-tools:get-annotated-message", "demo-tools:get-env", "demo-tools:get-sum"],
  "policies": ["agent-guardrails", "demo-tools-readers-access"]
}
```

## How membership arrives

Membership is the role assignment, one row per user and role, and three writers reach it.

1. SCIM group membership. A PATCH on `/scim/v2/Groups/{id}` with `members` `add` creates the direct assignment, `remove` deletes it whatever wrote it, and `replace` converges the set. A PUT is a tolerant members replace: the body's `displayName` is ignored and `members` is the full desired set. All of it is idempotent.
2. The admin API. `POST /v1/admin/assignments` naming the user id and the role id.
3. The CLI. `strazactl assign <role> --user <username>` prints `assigned <role> to <username>`, and prints `strazactl: assignment already exists` when the role is already held. `strazactl unassign <role> --user <username>` deletes that row and prints `unassigned <role> from <username>`, and says `<username> does not hold <role> directly, so there is no assignment to remove` when the person holds the role only through another role.

What Straza owns and what the identity manager owns. Roles are born in Straza. `POST /Groups` and `DELETE /Groups/{id}` answer `501` with the detail `roles are born in Straza, so the IdM cannot create or delete one over SCIM. Create the role in Straza, import it as a group, and assign membership from the IdM`. A PATCH of `displayName` answers `400` with `scimType` `mutability`, because policies reference roles by name. Membership is mastered by the identity manager. SCIM is the single writer for an exported role's holders, and a console or CLI assignment is drift that the console badges and the next reconciliation removes. A role assigned or removed reaches a running session at its next check-in, and the gateway tells that session's MCP client to list its tools again.

The kill switch on deactivation. A SCIM `active: false` or a `DELETE /Users/{id}` sets the user to `disabled`, revokes every session, removes the user's per-user OAuth grants, adds the user to the in-memory denylist of every replica and pushes the revocation to enrolled machines. A machine whose `straza daemon` holds the push stream denies its next hook call within about two seconds. A machine without the daemon denies once its token expires, at most 300 seconds later in the enterprise profile. The audit chain records `user.killed` with `origin` `scim` and the reason `deactivated via SCIM`. Setting `active: true` again lifts only the revocations SCIM wrote: a `strazactl users lock` survives and the refused lift lands as `user.lift.blocked`, revoked sessions stay revoked, and the user enrolls again. Disabling a login at the identity provider alone stops new sign-ins and nothing more, so a leaver flow triggers both the login cut and the SCIM deactivation. https://docs.straza.ai/guides/operate/kill-switch-runbook/ walks every lever.

## The grounding rule

Never propose a mapping from memory. Before the first proposal, read what exists.

1. List the roles with `strazactl roles list` (reference: https://docs.straza.ai/reference/cli/strazactl/strazactl_roles_list/). The `NAME` column is the only source of role names. Never invent a role name. A role that does not exist is created in Straza by the person before it can be mapped, with `strazactl roles create <name> --kind <kind>`, or, for an application role that reaches a server, with `strazactl roles create <server>-<word> --app <server> --tools <tool>,<tool>`. The command prints `created role <name> (<role id>)`, and `created role <name> (<role id>) on the server <server>` for a role made with `--app`.
2. List the users with `strazactl users list` (reference: https://docs.straza.ai/reference/cli/strazactl/strazactl_users_list/). The columns are `USERNAME`, `STATUS`, `ORIGIN`, `EMAIL` and `ID`. A row with `ORIGIN` `local` was born in Straza and is not the identity manager's to master.
3. Read the SCIM discovery documents with the token: `GET /scim/v2/ServiceProviderConfig`, `GET /scim/v2/Schemas` and `GET /scim/v2/ResourceTypes`. They list the two Straza extension URNs and say in the protocol's own words that `patch` and `filter` are supported and `bulk`, `sort`, `etag` and `changePassword` are not. For what a role reaches, read its group with `filter=displayName eq "<role>"` on `/scim/v2/Groups`.
4. Present every mapping as a proposal, one row per identity manager role or group: the Straza role it maps to, that role's kind, what the role's SCIM render says it reaches, and who will hold it. Stop and wait for the person to confirm. Write no membership before the confirmation. The person then writes it, in the identity manager or with `strazactl assign` in their own terminal, as the SKILL.md section Reads, checks and changes says.
5. Verify after the write. The user's SCIM render lists direct assignments under `groups` as `{value: <role id>, display: <role name>, type: "direct"}`, and the admin read `GET /v1/admin/users/{id}` shows `effective_roles`. Then resolve the tools:

```sh
strazactl catalog preview --user <username>
```

```text
subject: user=<username> roles=<role>
SERVER  TOOL     STATUS      REASON
<app>   echo     visible
<app>   get-sum  visible
```

`strazactl catalog preview --role <role>` previews a role with no holder yet, and `--app <app>` filters to one server.

## The one admin API token

The identity manager holds one long-lived admin API token, prefix `wat_`, for both planes. Its scope carries the `scim` area: `scim:read` opens the GET routes and the discovery documents, `scim:write` opens the writes, and the two are independent, so a manager that reads back what it writes holds both. A connector that also reads the admin API adds those read scopes to the same token. Straza stores the SHA-256 hash and prints the value once:

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

The token is cut after its first four characters. Name it after the system it serves, for example `okta-prod` or `midpoint-prod`. `--ttl 720h` bounds its life and `0` means never. The console mints the same token under Settings, tab "API tokens", where the IGA connector preset fills in the scope. `strazactl api-token list` shows the metadata, and `strazactl api-token revoke <token id>` prints `revoked <token id>`, after which the next request answers `401`. A request without a usable token answers `401` with the detail `valid token required: an admin API token whose scope carries the scim area (strazactl api-token create --scope scim:read,scim:write for an IdM)`, and a token whose scope lacks the area answers `403` naming the missing scope. Every role renders on the wire, `straza-admin` included, so a token with `scim:write` can assign administration through a members add. Treat it as an admin credential and guard that membership at the identity manager.

## Human sign-in

On a standalone host strazad is its own sign-in page. `strazactl login --server <url>` prints a link and a code, and the person signs in there as `admin` with the password strazad printed once in its log. In the enterprise profile people sign in at your identity provider, and the operator sets that up in the server config before anyone can log in:

| Key | Environment | What it does |
|---|---|---|
| `oidc.issuer` | `STRAZA_OIDC_ISSUER` | The provider's discovery base URL, written exactly as the provider writes it: a Keycloak realm path and a trailing slash are kept. |
| `oidc.clientId` | `STRAZA_OIDC_CLIENT_ID` | The client at the provider. Straza expects it as the audience of ID tokens and hands it to `strazactl login` and `straza enroll`, which run the OAuth 2.0 device authorization grant with it, so that grant must be enabled on the client. |
| `oidc.bootstrapAdmin` | `STRAZA_OIDC_BOOTSTRAP_ADMIN` | A username. The first provider-verified login whose `preferred_username` equals it becomes `straza-admin`, but only while no `straza-admin` assignment exists. Remove the line once the first admin is in. |
| `oidc.jitProvision` | `STRAZA_OIDC_JIT` | Off by default in the enterprise profile, because SCIM is the source of truth. A person must then arrive over SCIM before they can sign in. |

`strazactl login` reads the issuer and the client from strazad's `/.well-known/straza/idp.json` and then fetches the provider's discovery document itself, so the issuer URL must resolve on every machine that runs `strazactl` or `straza`, not only inside strazad's network. A provider without a device endpoint fails the login with `does not advertise a device_authorization_endpoint`. strazad matches a verified token to an account by `externalId` equal to the token's `sub` first, and by `preferred_username` against `userName` second.

The emergency door. strazad creates the `break-glass` admin at first boot and prints its password once in the log. `strazactl login --break-glass` signs in at the server's own emergency page, which accepts that account only and raises an alarm on every use. The person stores that password in their vault and rotates it with `strazactl users set-password break-glass`. Never ask for it in the chat.

## Gotchas

- A business role holds no access rows of its own. Give an application role access to the tools and have the business role compose it. The business role's SCIM render then shows those tools, because the projection walks the implication closure.
- An approver role never holds tools. It is the only kind a policy may name in `approve.roles` besides `straza-admin`, and its members are the people who decide.
- An AI agent never decides an approval, whatever role it holds. An `autonomous` agent is never allowed to approve its own calls, whatever a policy says.
- The sponsor requirement for holds. An approve block with no `roles`, no `deciders` and no `selfApproval` routes the request to the sponsor. A person with no sponsor confirms their own call on an enrolled phone or browser. When the sponsor does not resolve, because an agent has none or the named sponsor is unknown, inactive, an agent or the requester, and the rule names no approver roles, the call is denied at request time with a reason that names the fix, rather than parked where nobody would see it. When the rule also names approver roles, those roles carry the request even when the sponsor does not resolve. Model the accountable human before the agent goes to work.
- Deleted users are deactivated, never removed. `DELETE /Users/{id}` answers `204`, the row stays `disabled` with its id, and a later create with the same `userName` revives that row with the same id, the new attributes, no old assignments and no local password. An active holder of the name, or a `local` account, still answers `409`.
- Re-enabled users come back without their sessions. `active: true` lifts SCIM-origin revocations only, a Straza-side lock survives it, and the person or agent enrolls again.
- `kind` is fixed at birth. A create without the agent schema and without `userType` `agent` is `human` forever, and only creating the account again changes it, so send the type on the first create. A row created with `userType` `agent` alone reads `kind` `human` on the SCIM render while the admin surface and the key lane treat it as non-human. The disagreement is a signal for certification, not an error.
- The `groups` attribute of a User is read-only. Membership writes go through `/Groups` only.
- On a standalone host a SCIM-born person who needs to sign in also needs `strazactl users set-password <username> --password <password>`, because the built-in issuer holds the password there.
- `strazactl users enable` lifts every revocation and sets the status back to active, while `strazactl users unlock` lifts the lock and leaves the status as it is, so a user who is both disabled and locked needs both.
