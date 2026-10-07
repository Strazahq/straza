# SCIM Profile, v1beta1 (revision 20)

Status: **beta**. The profile began as v1alpha1, and the move to beta
changed no wire format. Conformance transcripts: `spec/conformance/scim/*.json`. The
platform's SCIM server MUST satisfy every transcript; they double as request
examples for the identity manager guides at
https://docs.straza.ai/guides/connect-identity/.

Revision 20 (2026-10-01, https://docs.straza.ai/guides/serve-mcp-apps/catalogs-per-role/):
every application role that reaches an MCP server is server-owned, so the
block carries `server` on each of them, whether the server's admin or a
global admin created it. An application role that reaches no server still
omits it. The attribute, its type and its mutability are unchanged, so an
identity manager that filters on `server` sees more roles under the same
filter and needs no change.

Revision 19 (2026-09-18, https://docs.straza.ai/guides/operate/delegate-one-server/):
a role may belong to one MCP server, and the read-only Group projection
(§4.1) says which. The block gains `server`, the owning server's name as
the admin API spells it, present only on a server-owned role and omitted
on every other role, so an identity manager delineates server-owned roles
by one attribute instead of parsing a name prefix, and an archetype per
server follows from one filter. A server-owned role is an application
role named `<server>-<suffix>`, defined by the server's admin or on the
server's page, and its membership is the identity manager's as on every
other wire-group. The attribute is read-only like the rest of the block,
so a PATCH against it answers `400` scimType `mutability` with the
existing detail. The global MCP admin role is renamed
`straza-global-mcp-admin` and keeps rendering no `administers`. Additive:
the revision 18 transcripts replay unchanged, the `group-enrichment`
transcript gains two steps and the `groups-roles` transcript gains two,
derived from the reference server and not captured from a live wire.

Revision 18 (2026-09-17, https://docs.straza.ai/guides/operate/delegate-one-server/):
every MCP server names the Straza role that administers it, minted at
registration as `mcp-admin-<namespace>-<name>`, and the read-only Group
projection (§4.1) says which servers a role administers. The block gains
`administers`, the sorted names of the live servers whose admin role is
in the role's implication closure, computed at read time like `apps` and
omitted when empty, so a minted role names its one server and a business
role that implies minted roles names theirs, which is the team an IdM
certifies. `straza-global-mcp-admin`, the product role whose fixed meaning
is every server, renders no `administers`: the block lists facts on rows,
never a fixed meaning. The new attribute is read-only like the rest of the
block, so a PATCH against it answers `400` scimType `mutability` with the
existing detail. Additive: the revision 17 transcripts replay unchanged,
and the `group-enrichment` transcript gains one step, derived from the
reference server and not captured from a live wire.

Revision 17 (2026-09-07): the `501` detail on `POST /Groups` and `DELETE
/Groups/{id}` says the rule and the next step in plain words instead of
citing this document. No field, code or behavior changes; the conformance
transcript pins the new text.

Revision 16 (2026-09-07, with no legacy period, before the first public
release): the SCIM token kind (`wst_`) is REMOVED, not deprecated. This surface takes exactly one credential, an admin API token
(`wat_`) whose scope carries the `scim` area, and the admin API routes that
minted, listed and revoked SCIM tokens are gone (openapi 0.90.0). The `401`
detail names that one mint. `scim:read` and `scim:write` are independent
verbs, as everywhere in the grant grammar, so an IdM that reads back what it
writes holds both. This is a breaking change to the previous revision's
authentication section, made as a stated exception to the additive rule of
the spec README before the first public release. Records this
plane emits carry `actorVia` `api-token` only (events revision 26).

Revision 15 (2026-09-07, https://docs.straza.ai/guides/connect-identity/generic-scim/):
one token kind for both planes. The admin API token's grant grammar gains
the area `scim`, and this surface accepts an admin API token (`wat_`) whose
scope carries `scim:read` for the GET routes and the discovery documents or
`scim:write` for every route, the verb derived from the HTTP method exactly
as on the admin plane. A SCIM token (`wst_`) keeps working unchanged and is
still minted; the CLI notes that the admin API token is the one credential
now. No field changes on any resource. The `401` detail for a missing
bearer names both mints, and a `wat_` whose scope lacks the grant answers
`403` naming it. Records this plane emits carry `actorVia` `api-token` for
a `wat_` (events revision 25). Conformance: the `auth` transcript is
unchanged; the wat_ lanes are pinned by the reference server's
TestSCIMPlaneAcceptsAdminAPITokenByScope.

Revision 14 (2026-08-25): two wire changes on the
Groups surface. ① The control-plane role kind renames on every wire:
`roleKind` (and the admin API `kind`) spell it `straza`, not `console`
(openapi 0.81.0; the plane value `control` is unchanged). ② Control-plane
roles now ALWAYS render, list and take membership writes like every other
role: the `scim.exposeControlPlaneRoles` knob is retired (a leftover
config key boots with one ignored-with-notice line, it no longer fails
boot or changes behavior). Security posture: a SCIM bearer token can now
grant `straza-admin` by default, because a members add on that group
creates the assignment. This is accepted because the IdM is the intended
master of every role, including the
product-defined `straza-*` roles (enrollment eligibility among them),
and a hidden-by-default admin group made the IdM-mastered story
unworkable. The revision 7 prefix-convention guard is unaffected
(historical; the prefix derivation itself retired at revision 12).

Revision 13 (2026-08-21): the `roleKind` vocabulary of the read-only
Group access projection (§4.1) gains `approver` (spec/policyset revision
15: the approver role kind, decide authority only, access plane). Such a
role renders, lists and takes membership writes exactly like any other
access-plane role: the IdM imports it, masters who holds it, and
certifies it as its own class ("who may approve agent actions"). No
other wire change; transcript `group-enrichment` gains the pin.

Revision 12 (2026-08-13, the unified role model): groups
and roles become ONE object. The `/Groups` surface renders **exported Straza roles** in the
protocol's Group costume (`id` = role id, `displayName` = role name,
`members` = direct holders), because that is the access dialect IdMs
speak; the platform-side group object, `scim.groupRoleMap`, the
`straza-` prefix derivation and `scim.autoCreateRoles` all retire
(config presence fails boot loudly). Membership IS role assignment: a
members add creates THE direct scim-origin `(user, role)` assignment, a
remove deletes it whatever origin wrote it (SCIM is the single writer
for exported-role holding; console grants are drift the IdM reconciles
away). Creating, renaming and deleting access stays Straza's act: POST
and DELETE answer `501`, `displayName` is `readOnly`. Control-plane
roles are NOT on this wire (absent from lists, `404` addressed, no
name/id oracle) unless `scim.exposeControlPlaneRoles` opts in: the
successor of the revision 7/8 gates, strictly stronger (a SCIM bearer
token physically cannot grant admin by default). The break-glass local
admin is invisible to this surface end to end. Group `externalId` and
its filter retire (nothing IdM-side creates the object that would carry
one). BREAKING: wire-group ids change from group ids to role ids
(re-import; the store migration fans memberships out to direct
assignments, so no grant is lost), and the `group` changes-feed type
retires (membership changes emit both the user id and the role id;
`role` events drive the wire-group render). §4 rewritten; group
transcripts rewritten (refusal pins, tolerant-PUT pin, projection pin);
`user-groups` re-anchored onto a seeded exported role, Users semantics
unchanged.

Revision 11 (2026-08-13): the User extension exposes the
two Straza-born identity facts, `kind` (`human | nhi`) and `origin`
(`local | scim`), always present and strictly read-only (§3.3). Rationale:
single-projection IdM modeling. One SCIM read now carries everything an
IdM needs to display and certify an identity, including what Straza itself
concluded about it, so a connector needs no second read-only object class
for the mirror. Additive: existing writers are unaffected;
writes against the new attributes answer `400` `mutability` exactly like
the revision-4 lock block beside them.

Revision 10 (2026-08-12): core `title` (RFC 7643 §4.1.1)
joins the mapped subset, the job-title/function line for humans and agents
alike ("Deploy bot", "Code reviewer"), useful context when agents propagate
through IGA views. Adopted as CORE rather than a Straza extension because
every connector already speaks it (and midPoint UserType carries `title`
natively). Additive: writable over POST/PUT/PATCH like `displayName`
(standard replace semantics: the typology PUT deviation does NOT apply),
omitted from responses when empty. Transcript `user-title` (new).

Revision 9 (2026-08-12): the Group access projection, and
typology as an equal NHI signal. ① A group carrying a §4 role mapping now
renders the read-only extension
`urn:straza:params:scim:schemas:extension:2.0:Group` (§4.1): the role's
identity (name, kind, plane, description) plus what membership effectively
grants (apps, tools, targeted PolicySets), so the entitlement an IGA
aggregates and certifies carries real meaning instead of a bare name.
Additive on the wire; any write against it is a `mutability` error. ② An
identity whose `userType` is `agent` or `service` (§3.2) is treated as
non-human platform-wide even when the agentic URN (§5) was never sent:
never an approval decider, eligible for the NHI assertion-key lanes,
reported `kind=nhi` on the admin surface and pull mirror. Rationale:
`attrs.kind` is create-only and most IdM connectors cannot emit the
agentic URN, so a typology-typed agent used to land formally human in
every kind-gated lane, where it could decide approvals. New
transcript `group-enrichment`; `service-discovery` gains the extension
pins.

Revision 8 (2026-08-11): role derivation goes
**explicit-only by default**. Both permissive defaults flip, wire-invisible
on the SCIM surface (groups sync identically; only platform-side role
derivation changes): ① `scim.groupRolePrefix` defaults to EMPTY, which
disables the group-name convention entirely; a deployment that wants
name-driven mapping opts in by setting the prefix (the revision-7
control-plane refusal still applies when enabled). ② `scim.autoCreateRoles`
defaults to `false` (the import-only posture): the IdM can never mint a
role unless a deployment opts in. Rationale: a group name in a foreign
system must not be an authorization statement, and role creation belongs
to the platform operator; both behaviors survive as explicit opt-ins.
BREAKING for deployments relying on either default: add
`scim.groupRolePrefix: "straza-"` and/or `scim.autoCreateRoles: true` to
restore the old behavior (release note carries the same line).

Revision 7 (2026-08-11): two role-derivation qualifications (§4), both
wire-invisible on the SCIM surface (the group syncs normally; the refusal
is a logged warning on the platform side, never a sync failure):
① the prefix convention never delivers a **control-plane** role (openapi
0.59.0 kind `console`, renamed `straza` at revision 14: straza-admin,
delegated admins, auditor): an IdM
group named `straza-straza-admin` would otherwise mint a root assignment;
the explicit `scim.groupRoleMap` stays the sanctioned spelling for
deliberate control-plane delivery. ② `scim.autoCreateRoles: false` (the
import-only posture) stops a mapped group from CREATING its role: roles
are Straza-born and the IdM only maps groups onto existing ones. Default
`true` preserves the historical behavior.

Revision 6 (2026-08-10): deactivated-row revive on create, closing a live
dead end (an IdM delete-then-reprovision cycle, a rehire or a provisioning
retry after an interrupted run, answered `409` forever because DELETE
deactivates and never purges). `POST /Users` whose `userName` conflicts
with a **disabled, scim-origin** row now revives that row in place: `201`
with the SAME `id` (audit history stays on one identity), the incoming
document overwrites the mapped attributes, any stale local credential is
wiped (deprovisioning must not resurrect credentials), and the scim-origin
revocation lift runs exactly as the revision-4 `active: true` semantics.
Fail-closed: an ACTIVE holder or a non-scim-origin holder still answers
`409` scimType `uniqueness`; an `externalId` held by another row still
conflicts. Transcript `users-lifecycle` (revive arc appended).

Revision 5 (2026-07-28): identity typology (spec/policyset revision 9
carries the policy-match half). The CORE
`userType` attribute is adopted with a Straza-enforced vocabulary
(`human|agent|service`), and the existing Straza extension URN gains the
writable typology attributes `agencyMode`/`sponsor`/`swarmId`/`ephemeral`
(§3.2) beside the revision-4 read-only lock block. Values outside the fixed
vocabularies are `invalidValue`. PUT deviation documented in §3.2: absent
typology attributes are left untouched. Transcript `user-typology`.

Revision 4 (2026-07-28): origin-selective reactivation. ONE
behavioral change: the `active: true` reactivation lift is now
**origin-selective**: it removes scim-origin revocations only, so a
Straza-side lock (console/admin API or SOAR automation) survives any IdM
enable or reconciliation (§3). New read-only Straza User extension
`urn:straza:params:scim:schemas:extension:2.0:User` renders the lock block
(§3.1); any PATCH against it is a `mutability` error. Transcript
`user-lock`. Additive on the wire except the lift semantics, which close a
precedence inversion (an IdM enable un-killing a console-locked user).

Revision 3 (2026-07-21): the `straza.dev/scim-profile` identifier, the
`straza-` default group prefix and the `straza.revocation.user` subject
replace the pre-release names; transcript shapes unchanged.

Revision 2 (2026-07-15): User responses now carry the read-only `groups`
attribute (RFC 7643 §4.1.2) so IdM connectors can diff desired-vs-current
memberships instead of re-sending adds on every recompute (additive; §3/§4,
transcript `user-groups`).

Words MUST/SHOULD/MAY are RFC-2119. Straza implements a **strict, documented
subset** of SCIM 2.0 (RFC 7642/7643/7644). Everything outside it is rejected
loudly with a proper SCIM error (§6). The database is never the
integration API; this endpoint is.

## 1. Endpoints & operations

Base path: `/scim/v2`. Content type: `application/scim+json` (plain
`application/json` is accepted).

| Endpoint | GET | POST | PUT | PATCH | DELETE |
|---|---|---|---|---|---|
| `/Users`, `/Users/{id}` | ✓ (+ filter) | create | replace | add/replace/remove | **deactivate** |
| `/Groups`, `/Groups/{id}` | ✓ (+ filter) | **501** | tolerant members replace | members ops only | **501** |
| `/ServiceProviderConfig` | ✓ | n/a | n/a | n/a | n/a |
| `/Schemas`, `/ResourceTypes` | ✓ | n/a | n/a | n/a | n/a |
| `/Bulk` | n/a | **501** | n/a | n/a | n/a |

- **Filters**: exactly `userName eq "..."`, `externalId eq "..."` (Users) and
  `displayName eq "..."` (Groups; the Groups `externalId` filter retired
  with revision 12). Anything else → `400` scimType `invalidFilter`.
  Unfiltered GET lists all resources with `startIndex`/`count` paging.
- **DELETE /Users/{id} deactivates** (soft): the user's status becomes
  `disabled`, sessions are revoked, and `straza.revocation.user` is emitted,
  identical to `active: false`. Every per-user OAuth grant of the user is
  removed as well, one `straza.audit.admin` record per row attributed to
  the admin API token that acted (events revision 24, actor value revision 26). SCIM-origin identities are never hard-deleted
  by the IdM. A later create with the same `userName` **revives** the
  deactivated row in place (revision 6): `201`, same `id`, the document's
  attributes overwrite, stale local credentials are wiped. Conflicts with an
  ACTIVE username or a non-scim-origin account stay `409`.
- `meta.version`/ETags, sorting, and `.search` are not supported.

## 2. Authentication

One credential opens this surface (revision 16): an admin API token, prefix
`wat_`, minted by `strazactl api-token create` (admin API
`POST /v1/admin/api-tokens`) with a scope that carries the `scim` area.
`scim:read` opens the GET routes and the discovery documents, `scim:write`
opens the mutating routes; the verb derives from the HTTP method and the
two are independent, so an IdM that reads back what it writes holds
`scim:read,scim:write`. A connector that also reads the admin API holds the
same token with the read grants it needs. A token whose scope lacks the
grant answers `403` naming it; a bearer without the `wat_` prefix is an
unknown credential and answers `401`.

The plaintext is shown exactly once; the server stores a SHA-256 hash.
Requests without a usable bearer → `401` (SCIM error body, detail
`valid token required: an admin API token whose scope carries the scim
area (strazactl api-token create --scope scim:read,scim:write for an IdM)`).

## 3. Attribute mapping (Users)

| SCIM | Straza `users` | Notes |
|---|---|---|
| `id` | `id` | server-assigned, immutable |
| `externalId` | `external_id` | IdM's identifier; enrollment links ID tokens by it |
| `userName` | `username` | required, unique → `409` scimType `uniqueness`; a conflict with a **disabled scim-origin** row instead revives it in place (revision 6, §1) |
| `displayName` (or `name.formatted`) | `display` | `displayName` wins when both present |
| `title` | `title` | CORE (RFC 7643 §4.1.1), revision 10: job title / human-readable function, for agents too ("Deploy bot"). Standard replace semantics like `displayName` (the §3.2 typology PUT deviation does NOT apply); omitted from responses when empty |
| `emails[primary].value` (or first) | `email` | single value stored |
| `active` | `status` | `false` ⇒ `disabled` + revocation cascade (rows carry `origin=scim`) + every per-user grant removed (events revision 24); `true` re-enables and lifts **scim-origin revocations only** (revision 4): a Straza-side lock (§3.1) survives, and while one does the user stays denied. The attempt lands on the audit chain as `user.lift.blocked`. Existing sessions STAY revoked, so the user re-enrolls |
| `groups` | `role_assignments` (derived) | **read-only** (RFC 7643 §4.1.2): every User response renders the user's DIRECT assignments of exported roles as `{value: <role id>, display: <role name>, type: "direct", $ref}`; omitted (not empty) when there are none. Membership WRITES go through `/Groups` only, and `groups` is not in the accepted PATCH subset |
| `userType` | `user_type` | revision 5 (§3.2): the coarse identity family, vocabulary-enforced `human\|agent\|service`; the structured typology rides the Straza extension |

Users created here get `origin=scim`. `active` MUST accept boolean `true/
false` and the string forms `"True"/"False"` (Azure AD compatibility).
PATCH with no `path` and an object value applies the mapped keys it carries.
Attribute names in `path` are case-insensitive.

### 3.1 Straza extension: the lock block (revision 4, read-only)

Every User response declares and carries
`urn:straza:params:scim:schemas:extension:2.0:User`:

```json
"urn:straza:params:scim:schemas:extension:2.0:User": {
  "locked": false
}
```

While a Straza-side lock exists (an `origin=admin|external` revocation
from the console/admin API `POST /v1/admin/users/{id}/lock`, or from a
SOAR/SIEM automation with a `wat_` admin API token), the block reads:

```json
"urn:straza:params:scim:schemas:extension:2.0:User": {
  "locked": true,
  "lockReason": "SOC hold pending review",
  "lockedAt": "2026-07-28T12:00:00Z",
  "lockOrigin": "admin"
}
```

- `locked` is always present; `lockReason`/`lockedAt`/`lockOrigin` ride only
  while locked (newest lock wins when several exist). `lockOrigin` ∈
  `admin | external`. A scim-origin revocation is NOT a lock: it is the
  mirror of `active: false`, which the IdM already owns.
- The whole block is **readOnly**: PATCH against the extension URN, its
  sub-attributes, or the bare names (`locked`, `lockReason`, `lockedAt`,
  `lockOrigin`) ⇒ `400` scimType `mutability`. PUT bodies never carry it
  (unknown keys are ignored per the mapped-subset rule).
- Effective access = IdM lane AND Straza lane: `active` and `locked` are
  independent attributes, so IdM reconciliation can never flip the lock,
  but it can INGEST it (inbound mapping, certification view) and MAY react
  (e.g. suspend its own focus so the IdP login is cut too). The lock lifts
  only via the Straza admin API (`POST /v1/admin/users/{id}/unlock`, or the
  console/admin user-enable which keeps its full-lift semantics).
- Discovery: the URN is listed in `/Schemas` and as a non-required
  `schemaExtensions` entry on the User resource type.

### 3.2 Identity typology (revision 5, read-write)

The IdM masters the classification the policy engine matches on
(spec/policyset revision 9 `match.identity`) and the approval semantics
consume (`agencyMode: autonomous` subjects are never
selfApproval-eligible, an engine invariant):

| Attribute | Where | Values | Consumer |
|---|---|---|---|
| `userType` | CORE (RFC 7643) | `human` \| `agent` \| `service` | policy match; console labels; `service` = technical accounts with no agency |
| `agencyMode` | extension | `interactive` \| `supervised` \| `autonomous` | approval semantics (the autonomous clamp; supervised→sponsor routing is a named follow-up) |
| `sponsor` | extension | username/opaque ref | accountable human: attribution + future push routing |
| `swarmId` | extension | deployment-defined | fleet policy match; future fleet kill |
| `ephemeral` | extension | boolean | future session-posture knobs; excluded from long-lived certification |

Rules:

- All five are **read-write** over POST/PUT/PATCH (bare or URN-qualified
  paths; case-insensitive; `ephemeral` accepts `"True"/"False"` like
  `active`). The revision-4 lock block beside them stays read-only:
  a whole-extension PATCH applies writable keys and refuses lock keys.
- `userType`/`agencyMode` values outside the vocabulary ⇒ `400`
  `invalidValue`, never stored (a typo must not silently unclassify).
  Values normalize to lowercase.
- **PUT deviation:** absent/empty typology attributes leave the stored
  value UNTOUCHED (standard PUT-as-replace would let an IdM that maps only
  the core attributes silently wipe classification on every recon).
  Clearing is explicit: PATCH `remove`, or the whole-extension `remove`.
- Unset = **unclassified**: the subject matches no `match.identity`
  selector (spec/policyset §1.4 documents the deny-unless-`<type>`
  pattern), and no approval clamp applies.

### 3.3 Straza-born identity facts (revision 11, read-only)

Two facts the server itself owns ride the same extension, ALWAYS present
(stable attribute names for IdM inbound mappings, like `locked`):

| Attribute | Values | Meaning |
|---|---|---|
| `kind` | `human` \| `nhi` | What Straza concluded the identity IS, fixed at creation: `nhi` iff the create carried the agentic extension URN (§5). It never changes; recreating the account is the only way to re-classify. |
| `origin` | `local` \| `scim` | How the account was born: created inside Straza (console/CLI) or provisioned over SCIM. Immutable for the row's lifetime; a revive (§1) keeps `scim`. |

Rules:

- Distinct from §3.2 on purpose: `userType` is what the IdM CLAIMS the
  identity is (read-write, IdM-mastered); `kind` is what Straza RECORDED at
  birth (read-only, server-mastered). The two can disagree (a typed-agent
  row created without the URN reads `userType: agent` with `kind: human`),
  and the disagreement itself is signal for certification views.
- Both are **readOnly**: PATCH against the bare names, the URN-qualified
  forms, or via keys inside a whole-extension or no-path PATCH object
  answers `400` scimType `mutability`. POST/PUT bodies never carry them
  (unknown keys are ignored per the mapped-subset rule), so a full IdM
  recon can never touch either.
- The NHI signal also stays visible in `schemas`: every rendered `nhi` user
  carries the agentic URN there (§5). `kind` is the mappable-attribute form
  of the same fact.

## 4. Groups ARE roles (revision 12)

The `/Groups` surface renders **exported Straza roles** as wire-groups.
There is no platform-side group object: `id` is the role id,
`displayName` is the role name, and `members` is the role's DIRECT
holder list (one entry per `(user, role)` assignment row). Holders via
the implication closure are deliberately absent: they are not removable
through this surface, and rendering them would make every IdM
desired-vs-current diff unconvergeable; what a role implies stays
visible in the §4.1 projection.

- **Exposure**: every role renders, whatever its plane (revision 14).
  Access-plane roles (kind `business|application|approver`) and
  control-plane roles (kind `straza`) list, address and take membership
  writes identically; the retired `scim.exposeControlPlaneRoles` knob is
  ignored with a boot notice. Stated posture consequence: an admin API
  token carrying `scim:write` can grant `straza-admin`; deployments that
  want the old
  hidden-admin posture must scope that at the IdM (which masters the
  group) rather than at this wire.
- **Membership IS assignment, single-writer**: PATCH members `add`
  creates (or adopts) THE direct scim-origin assignment, `remove` deletes
  the row whatever origin wrote it, `replace` converges the set; all
  idempotent, the value-filter remove form
  (`members[value eq "<id>"]`) included. A console/CLI grant of an
  exported role is **drift**: permitted, badged in the console, and
  realized away by the IdM's next reconciliation (standard non-tolerant
  target semantics). Every membership change emits identity events for
  BOTH the affected user (account shadow freshness, resolver bump) and
  the role (the wire-group render; `role` is the changes-feed type that
  drives it since the `group` type retired).
- **PUT is a tolerant members replace**: the body's `displayName`/
  `externalId` are IGNORED (Okta sends the full document, displayName
  included, on every membership update; erroring would break membership
  entirely), members applies as the full desired set (absent = empty),
  and the response re-renders server truth.
- **Refusals**: `POST /Groups` and `DELETE /Groups/{id}` answer `501`
  with the rule and the next step in `detail` ("roles are born in Straza,
  so the IdM cannot create or delete one over SCIM. Create the role in
  Straza, import it as a group, and assign membership from the IdM").
  501, not 403: RFC 7644 §3.12 defines
  403 as an authorization outcome and IdM runbooks map it to broken
  credentials; 501 is the code for an unsupported operation and the one
  shipped precedent (Genesys Cloud) uses it. An explicit PATCH against
  `displayName` answers `400` scimType `mutability` (`displayName` is
  published `readOnly` in `/Schemas`): roles are renamed only in Straza,
  so policy references can never dangle against a drifting name.
- **The break-glass admin does not exist on this wire**: never rendered
  (lists, filters, members), `404` when addressed, refused as a member
  value, unrevivable and undeactivatable through any SCIM op.
- Deprovisioning a member in the IdM removes the role at the next
  snapshot/checkin, and `active:false` kills sessions immediately,
  exactly as before.

### 4.1 Group extension: the access projection (revision 9, read-only)

Every rendered wire-group (exported role) declares and renders
`urn:straza:params:scim:schemas:extension:2.0:Group`:

```json
"urn:straza:params:scim:schemas:extension:2.0:Group": {
  "role": "dev",
  "roleKind": "business",
  "plane": "access",
  "description": "Developer access: demo-tools + midpoint read",
  "apps": ["demo-tools", "midpoint"],
  "tools": ["demo-tools:echo", "midpoint:search_users"],
  "policies": ["dev-guardrails"],
  "administers": ["finance/jira"]
}
```

- The block is the role's own data (revision 12): identity plus what
  holding it effectively grants. `role` trivially equals `displayName`;
  kept one revision for shape compatibility, retirement noted for v2.
- `role`/`roleKind`/`plane` speak the admin wire vocabulary (kind
  `business|application|approver|straza`, plane `access|control`;
  `approver` since revision 13 is decide authority only and always
  access-plane; `straza` since revision 14 is the control plane's
  spelling, `console` before it); `description` is the role's own and
  is omitted when empty.
- `apps`, `tools`, `policies` are computed over the role's implication
  closure. Apps come from tool bindings; tools come from the LIVE catalog
  only (running/degraded apps, the same glob semantics the session
  catalog enforces), spelled `app:tool`; policies are the ACTIVE
  PolicySets that NAME a projected role in `match.roles`. Subject-global
  sets are deliberately absent: the block answers "what does this grant
  add", not "what applies to everyone". Empty lists are omitted.
- `administers` (revision 18) is the sorted names of the live servers
  whose admin role is in the closure, the role a server names at its
  registration (`mcp-admin-<namespace>-<name>`). A minted role names its
  one server, a business role that implies minted roles names theirs, and
  `straza-global-mcp-admin` renders none because its meaning is every
  server. Omitted when empty.
- `server` (revision 19) is the name of the MCP server that owns the role,
  present only on a server-owned role, an application role named
  `<server>-<suffix>` that reaches that server alone, made by the server's
  admin or a global admin (revision 20), and omitted on every other role, the minted admin roles and
  `straza-global-mcp-admin` included. An identity manager delineates
  server-owned roles by this one attribute, never by parsing the name.
  Membership on a server-owned wire-group is the identity manager's like
  on every other.
- The whole block is **readOnly**: PATCH against the URN, its qualified
  sub-attributes, or the bare names (`role`, `roleKind`, `plane`,
  `description`, `apps`, `tools`, `policies`, `administers`, `server`) ⇒ `400` scimType
  `mutability`. PUT bodies carrying the URN are ignored (mapped-subset
  rule); the response re-renders server truth. Membership stays the only
  assignable fact on this wire: grants change through Straza roles and
  bindings, never through the projection.
- Freshness: computed at read time, so polling connectors are always
  current. A projection failure degrades to a smaller (or absent) block,
  never a failed sync: one broken policyset must not wedge an IdM
  reconciliation run.
- Discovery: the URN is listed in `/Schemas` and as a non-required
  `schemaExtensions` entry on the Group resource type.

Transcript `group-enrichment`.

## 5. Agentic extension

A `/Users` resource whose `schemas` contains a URN matching
`urn:ietf:params:scim:schemas:extension:agent*` (the IETF agentic-SCIM
draft; exact URN tracked until the draft stabilizes) is accepted and stored
as a user with `attrs.kind=nhi`. v1 stores and reports them; the autonomous
NHI lifecycle activates in v2.

Typology is an equal signal (revision 9): a user whose §3.2 `userType` is
`agent` or `service` is treated as non-human platform-wide even when this
URN was never sent (most IdM connectors cannot emit it): never an approval
decider, eligible for the NHI assertion-key lanes, and reported `kind=nhi`
on the admin surface and the pull mirror. `attrs.kind=nhi` keeps winning
where present; nothing is migrated or backfilled.

## 6. Errors

Errors use the SCIM error schema
(`urn:ietf:params:scim:api:messages:2.0:Error`) with `status` (string) and
`detail`; `scimType` is set for `invalidFilter`, `invalidValue`, `uniqueness`
and `mutability`. Unknown resource → `404`. Unsupported operations (bulk) →
`501`.

## 7. Conformance transcripts

Each file in `spec/conformance/scim/` is one scenario:

```json
{
  "name": "users-lifecycle",
  "steps": [{
    "note":    "human description",
    "request": {"method": "POST", "path": "/scim/v2/Users", "body": {...}, "noAuth": false},
    "expect":  {"status": 201, "subset": {"userName": "grace"}},
    "capture": {"uid": "id"}
  }]
}
```

`capture` binds response fields to variables (a bare name reads top-level;
a dotted path like `Resources.0.id` walks objects and array indices,
revision 12); `{var}` substitutes
into later paths and string body values. `subset` matches recursively:
objects require the listed keys to match; arrays require every expected
element to match SOME actual element. Implementations MUST pass every
transcript verbatim.

## 8. Versioning

`straza.dev/scim-profile/v1beta1`. Changes follow the additive-change rule
in the spec README: profile + transcripts + CHANGELOG + version bump in the
same PR.
