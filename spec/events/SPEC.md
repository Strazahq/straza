# Events, v1beta1 (revision 42)

Status: **beta**. The specification began as v1alpha1, revision 2 added
`straza.revocation.lift` and the push channel, and the move to beta
changed no wire format. Schema: `events.schema.json` (envelope). Examples:
`examples/valid-*.json`, `examples/invalid-*.json`.

Revision 42 (2026-10-01): the gateway serves one MCP server on its own endpoint,
`/mcp/{server}`, and serves the server's MCP Apps views there, so `straza.audit.mcp` records
the view answers. A `resources/list` answer writes one record with the new `event` value
`resources.list`, `effect: allow`, `reason: views served`, `app` the server's name and `count`,
the views in that answer. A `resources/read` writes one record with the new `event` value
`resources.read`, `app` the server's name and the new field `uri`, the address the caller
asked for. The reference producer cuts `uri` at 8 KiB, the cap of `arguments`, and adds
`uriTruncated: true` when it cut, and the refusal sentence quotes the same cut address. A view
the caller may see is `effect: allow` with `reason: view served`. Any other address is
`effect: deny` with the refusal sentence as `reason`. On the endpoint of a server in the caller's
catalog, a `resources/read` with no `uri` or with malformed params is refused with no record, as a
`tools/call` with no name is. On both events `tool`, `toolName`, `ruleId` and `setName` are
empty, and `granted` and `default` are false. A
`tools/list` answered on a server's endpoint names that server in `app`, where the combined
endpoint `/mcp` leaves `app` empty. A `tools/call` on a server's endpoint records `app` and
`toolName` as on `/mcp`. Its refusal of a name the server does not list carries `app`, the
server's name, and `toolName`, the name as sent. A request on the endpoint of a server that
the caller's catalog does not hold is refused with one sentence, and a `tools/call` or a
`resources/read` there still writes one deny record with that sentence as `reason`, `app` the
name in the address, and `toolName` or `uri` as sent. Under audit backpressure `block`, a
`resources/list` or an allowed `resources/read` whose record cannot be queued is refused, as a
`tools/list` is. Examples `valid-audit-mcp-resources-list.json` and
`valid-audit-mcp-resources-read.json` (derived from the reference producer and its test, not
captured from a live wire: [doc]). Additive for consumers: new values of an existing field and
two new fields, `uri` and `uriTruncated`, with no new event type and no schema change, so the
default sink list is unchanged. The unknown-field rule of §2 holds, and consumers MUST tolerate unknown `event`
values on `straza.audit.mcp`.

Revision 41 (2026-09-30, https://docs.straza.ai/reference/events/): a user created or changed
over SCIM chains the same records as a user created or changed through the admin API. Before
this revision a SCIM create, PUT, PATCH or DELETE of a user chained no `user.create` or
`user.update` record. The write left `straza.identity.*` events, which carry no actor and stay
off the chain, a deactivation or a reactivation chained its `user.killed`, `user.reactivated`
or `user.lift.blocked` record of `straza.audit.identity`, which names no actor, and a
deactivation chained one `apps.grant.remove` record per grant it removed, which names the
token. Each of these writes now also writes one `straza.audit.admin` record whose actor is the
admin API token that acted, with `actorVia` `api-token`. A create, and the revive of a
deactivated row that a create with the same `userName` makes, writes `user.create`, and
`origin` on `user.create` is `admin` or `scim`, the lane that wrote the user. A PUT, a PATCH or
a DELETE that changes at least one field writes `user.update` with `changed[]`, the names of
the fields the write changed and never their values. A deactivation or a reactivation lists
`status`, beside the identity record the status change already chains: `user.killed`,
`user.reactivated`, or `user.lift.blocked` when a lock still holds a user the identity manager
reactivates. On the SCIM lane `user.update` follows that identity record and any
`apps.grant.remove` records of the same write, and on the admin API it precedes them, so a
consumer pairs them by `user` and never by position. A revive is the exception: its
`user.reactivated` or `user.lift.blocked` record is followed by its `user.create`, which
carries no `changed[]`. The names in `changed[]` are the admin API's user fields, and two of
them, `username` and `external_id`, occur only on SCIM writes. A write that changes nothing, a
DELETE of a user already deactivated and a refused write write none. A PUT or a PATCH whose
status write fails after its field write answers `500` and writes `user.update` with the
fields it stored. Examples `valid-audit-admin-user-create-scim.json` and
`valid-audit-admin-user-update-scim.json` (derived from the reference producer and its test,
not captured from a live wire: [doc]). Additive for consumers: a new value in an existing field
and new values in an existing list, with no new event type, field or schema change. The
unknown-field rule of §2 holds, and consumers MUST tolerate unknown `origin` values on
`user.create` and unknown names in `changed[]`. The `straza.identity.*` events of these writes
and the SCIM wire are unchanged, so an identity manager sees no difference.

A user delete revokes the person's approver devices. `straza.audit.admin` gains the action
`approver.revoke`, one record per approver device that the admin API's user delete removes,
also when the client disconnects after the delete. It carries `target` and `device`, the
device's id, `user`, the deleted user's id, `username`, the user's name as the delete read it
before it soft-deleted the row, and `reason`, the value `user deleted by admin`, with the actor
fields of §2 naming the person or admin API token that deleted the user. For each of these
devices the reference producer also writes the `straza.identity.updated` event of the admin
device revoke, action `approver-device-revoked` with `device`, `user` and `via` `admin`, and it
now carries `reason` too. Example `valid-audit-admin-approver-revoke.json` (derived from the
reference producer and its test, not captured from a live wire: [doc]). Additive for
consumers: a new value in an existing field and a new field on an identity event, with no new
event type and no schema change. The revision 40 example `valid-audit-admin-approver-enroll.json`
now shows the device id with the `apd_` prefix the reference producer writes, and the wire did
not change.

The signed phone lane writes its refusals of who the user is. `login` gains the `via` value
`approver-token`, the approver token the phone app or the browser approver holds for its
enrolled device key. When one of the routes that take an approver token,
`GET /v1/approver/pending`, `POST /v1/approver/decide`, `PUT` and `DELETE /v1/approver/push`,
`GET /v1/approver/history` and `DELETE /v1/approver/enrollment`, refuses a user who is disabled
or on the lock denylist with 401 `user_inactive`, strazad writes a `straza.audit.authn` login
failure for the first refused request of an approver token, once per approver token and reason.
Each replica remembers the token once the record is in the outbox, so the phone's polls while
its user is suspended write no more, and a request of that token that is accepted, or a
restart, lets the next refusal write again.
`POST /v1/approver/refresh`, which answers the same code to a disabled or locked user, writes
no record, before and after this revision. When `POST /v1/approver/decide` refuses a user who
is typed as an AI agent or a service account with 403 `not_authorized`, strazad writes one such
record per refused decision, and that record carries `approvalId`, the request the decision
tried to decide. Every record carries `userId`, `user` when the user row was read, `reason`
(`user is disabled` or `user is locked`, the reasons of the ID-token lane, or `only a person
can decide a request`), `sourceIp`, and `userAgent` when the request carried one, because the
device's own connection produced the request. It carries no `session` or `harness`. The
`user_inactive` record carries no `approvalId`, because the refusal comes before the request
body is read. A request by an active person writes no authn record, as before. Example
`valid-audit-authn-login-approver.json` (derived from the reference producer and its test, not
captured from a live wire: [doc]). Additive for consumers: a new value in an existing field,
and an existing field on a new value (unknown-field rule §2; consumers MUST tolerate unknown
`via` values).

Revision 40 (2026-09-29, https://docs.straza.ai/reference/events/): the identity setup writes
join the hash chain. Before this revision, creating a user, changing a user, minting an approver
enroll token, enrolling an approver device and registering an agent's key wrote only
`straza.identity.*` events, which carry no actor and stay off the chain. Each of these writes now
also writes one `straza.audit.admin` record when it succeeds, also when the client disconnects
after the write. A refused write, an enrolment refused before its device is stored and a PATCH
that changes nothing write none. `straza.audit.admin` gains five actions. On all of them `user`
is the user's id, as on every earlier action that carries `user`, and `username` is the
username. `user.create` carries `target` and `user`, the new user's id, `username`, `kind`,
`human` or `nhi`, `userType` when the user has one, and `origin`, the value `admin`.
`user.update` carries `target`, `user`, `username` and `changed[]`, the names of the fields the
PATCH changed, such as `sponsor`, `status` or `password`, and never their values, the convention
`apps.install` follows since revision 30. `enroll-token.create` carries `target` and `user`, the
id of the user the token is for, `username`, and on the self route `channel`, the channel the
token is scoped to, and `self: true`. `approver.enroll` carries `target` and `device`, the new
approver device's id, `user`, the enrolled user's id, `username`, `platform` and `name`.
`nhi-key.set` carries `target` and `user`, the agent's id, `username`, and `fingerprint`,
`sha256:` followed by the hex digest of the public key as the key read renders it. No record
carries a token, a password, a password hash or a key. The action `nhi-key.removed` (`target` and `user`, the agent's id, and `reason` when a user
delete removed the key) was written by the reference producer on a user delete before this
revision and is listed from it, and the explicit key removal now writes it too. Every record
carries the actor fields of §2. The enrolment route has no admin principal, so the actor of
`approver.enroll` is the person whose one-time enroll token was consumed, and `actorVia` gains
the value `enroll-token`. On the self route the actor is the person who minted the token, with
`actorVia` `login` or `session`. None of these routes takes a reason. Examples
`valid-audit-admin-user-create.json`, `valid-audit-admin-user-update.json`,
`valid-audit-admin-enroll-token-create.json`, `valid-audit-admin-approver-enroll.json` and
`valid-audit-admin-nhi-key-set.json` (derived from the reference producer and its test, not
captured from a live wire: [doc]). Additive for consumers: a new value in an existing field and
new fields, with no new event type and no schema change. The unknown-field rule of §2 holds, and
consumers MUST tolerate unknown `actorVia` values. The `straza.identity.*` events of these
writes are unchanged, so an identity manager that reads them sees no difference.

Revision 39 (2026-09-28, https://docs.straza.ai/guides/approve/slack/): the Slack approval
channel judges the person behind a tap. `login` gains the `via` value `slack`. The Slack lane
maps a tap to a Straza user by the email on the tapping Slack profile, and when that user is
disabled or on the lock denylist the tap is refused before any decision and the lane writes
one `straza.audit.authn` login failure. The record carries `user`, `userId`, `reason`
(`user is disabled` or `user is locked`, the reasons of the ID-token lane) and the new field
`approvalId`, the request the tap tried to decide. It carries no `session` or `harness`,
because a tap has none, and no `sourceIp` or `userAgent`, because the request comes from
Slack's servers and not from the person's client. From this revision the absence of those
two fields means that no connection from the person's own client produced the event, which
still covers the janitor's events. A tap by an active person and a tap that maps to no user
write no authn record, as before. Example `valid-audit-authn-login-slack.json` (derived from
the reference producer and its lane test, not captured from a live wire: [doc]). Additive for
consumers: a new value in an existing field and a new field (unknown-field rule §2; consumers
MUST tolerate unknown `via` values). The same revision renames the envelope schema's `title`
to `StrazaEvent`; a title is an annotation, so no event, field or validation result changes
and every example still validates, and a consumer that generates types from the schema gets
the new type name.

Revision 38 (2026-09-27): **a hold writes the `consumed` phase too**
(payload semantics only, with no envelope, field or enum change, so every
revision-37 consumer keeps parsing). Before this revision an approved hold
left a single-use retry pass in the memory of every replica, the held
gateway call ran on the approval as well, and the run that used a pass
wrote no approval record, so one approval could run the identical call more
than once with nothing on the chain to say which approval let it through.
The use of a hold's approval is now one atomic write on the persisted
record, the same write a ticket's grant uses, and at most one caller on any
replica wins it. The winning run writes at most one `straza.audit.approval`
record with `phase: consumed`, `consumedBy`, the session that used the
approval, and `consumedAt`, whether the held call used it or one identical
retry of the same session did within `retryTTLSeconds`. A hold whose
approval is used therefore has three records, and a hold that is denied, expires or goes
unused keeps two. The resolution broadcast of §5.1 keeps its payload and no
longer grants anything. The example
`valid-audit-approval-consumed-hold.json` is derived from this document,
not captured from a live wire: [doc]. Section 2.2 holds the rule.

Revision 37 (2026-09-24, https://docs.straza.ai/guides/operate/drafts-and-publishing/):
every change to MCP servers, roles, access rows, implications
and policy sets is a draft first, and a person publishes it in one step.
`straza.audit.admin` gains nine actions. `draft.create` and `draft.update`
(`draft`, `revision`, `door`, `items[]` as `{kind, name, op}`, `digest`,
the hex sha256 of the revision's items, and when they apply `source` and
`sourceHash`, the apps directory file and the sha256 of its bytes,
`reverts`, the draft an undo reverses, `rebase: true` on a revision made
by Check again, and on the straza-app door `client`, `sponsor` and
`sponsorId`). `draft.check` (`draft`, `revision`, `snapshot`, `refused[]`
and `risks[]`, the finding codes, never a sentence), written once for every
check the server stamps on a revision, with no actor. `draft.contact`
(`draft`, `app`, `host`, `outcome` answered, refused or failed, `tools`,
the number of tools the server listed). `draft.publish` (`draft`,
`revision`, `items[]`, `snapshot`, the active snapshot after the publish,
`riskDigest` and `reviewedDigest`, `acknowledged[]` and `typed[]`, the keys
of the risks acknowledged by a tick and by typing, never the typed text,
`proposer`, `proposerId`, `proposerVia`, `client`, the client the publisher
used, and `reverts` on an undo). `draft.discard` (`draft`, `revision`,
`reason` when the person gave one or the publish that closed the draft
wrote one, `closedBy`, the draft whose publish closed a set's saved edit,
and `unlinked`, the server a discarded removal of the apps directory left
in place). `draft.expire` (`draft`, `revision`), with no actor.
`roles.implication.create` and `roles.implication.delete` (`target`, the id
of the role that implies, `role`, `implies`, `impliesId`), one record per
edge, over the admin API or by a publish. Every record a publish writes
names the draft in `draft`: `apps.install`, `apps.remove`,
`apps.binding.create`, `apps.binding.delete`, `roles.create`,
`roles.update`, `roles.delete`, `roles.unassign`,
`roles.implication.create`, `roles.implication.delete`, `policy.create`,
`policy.update`, `policy.delete`, `policy.activate` and
`policy.deactivate`. So does every other event a publish writes:
`straza.identity.updated`, `straza.apps.updated`, `straza.apps.removed`
and `straza.policy.updated`. A consumer that applies config from these
events applies a publish from its one `straza.apps.updated` event with
`change` `publish` and may skip the others that carry `draft`, as the
reference producer's replicas do. A direct admin route is a draft
published in the same request, so its records carry `draft` too, and it
writes no `draft.*` record for that draft. `policy.update` now records a
text a publish makes the set's stored text, and `policy.activate` every
publish that places a set's text in the active snapshot, a changed text of
a set that stays on included. A saved edit of a set that is on is the
`draft.create` or `draft.update` of that set's draft, the one direct route
that publishes nothing. A note, a typed text and a document's text never
enter a record, because the chain keeps what lands in it. The sentence
that admin records come over the admin API or over SCIM gains "or from
the gateway's drafting tools, or from strazad's own background work, which
names no actor". `straza.apps.updated` gains the `change` value `publish`
with `draft`, `snapshot` and `apps[]`, the servers the publish created,
changed or removed: every replica applies a publish in one order when it
reads it, and the change feed of the reference producer skips it, as it
skips every change but `binding`. `straza.audit.mcp`: a call of
`straza__draft_submit` carries `argumentsDigest`, the hex sha256 of the
verbatim arguments, and `argumentsSize`, their length in bytes, in place
of `arguments` and `argumentsTruncated`, because the record is written
before the tool can refuse a secret. Consumers MUST tolerate the new
actions and fields (additive under the unknown-field rule of §2). Examples
`valid-audit-admin-draft-create.json`,
`valid-audit-admin-draft-check.json`, `valid-audit-admin-draft-publish.json`,
`valid-audit-admin-roles-implication-create.json`,
`valid-audit-admin-apps-install-draft.json`,
`valid-audit-mcp-draft-submit.json` and `valid-apps-updated-publish.json`
(values synthetic, the field sets taken from this revision's list above
and not yet read back from a live record: [doc]).

Revision 36 (2026-09-21): a refused sign-in leaves a record.
`straza.identity.updated` gains the action `oauth.connect.refused`. A
caller's own sign-in at an OAuth provider is finished by a signed-in
request that carries the provider's code and the signed state of the
sign-in, and the reference producer stores the grant only when the state
names the user of that session. When it names another user, nothing is
redeemed at the provider, nothing is stored, and one record is emitted with
`user` (the id of the user the state names, who started the sign-in),
`setBy` (the id of the signed-in user who presented it, the actor, as on
`token.connect`), `reason` (one sentence, today `the state names another
user`), `app` when the MCP server of the state still exists and `provider`
when that server still uses a provider sign-in. The action name carries the
decision. The record changes no identity object, so the change feed of the
reference producer skips it the way it skips a refused check-in. The
`oauth.connect` record of a stored sign-in is unchanged, and its `user` is
now always the user of the session that finished it. Consumers MUST
tolerate the new action (additive under the unknown-field rule of §2). No
new event type, so the default sink list is unchanged. Example
`valid-identity-updated-connect-refused.json` (values synthetic, the field
set equal to the one record read back from the eval stack at 67adf102 on
2026-09-21: [live]).

Revision 35 (2026-09-21, https://docs.straza.ai/guides/serve-mcp-apps/caller-credentials/):
the gateway record names a fourth credential source. `credentialSource` on
`straza.audit.mcp` gains the value `client_credentials`: the call ran on a
token of the calling agent's own client at the server's OAuth provider,
which the gateway got with the client credentials grant (app-manifest
revision 5). `credentialId` is then `client_credentials:<server id>:<user
id>`. It names no stored row, because the token lives in memory only, and it
stays the same when the token is renewed, so one id follows one agent on one
server. `credentialOwner` stays absent, as with `own`, because the client is
the caller's own and `user` already names the agent. A call refused because
no token could be got, for want of a signing key or because the provider
refused the client or did not answer, is the credential refusal of revision
31: `effect: deny` with the allowing rule, the refusal sentence as `reason`
and no credential fields. That sentence never carries a token, a client
assertion or the provider's free text. The fetch itself leaves no event: it
happens once per token lifetime per agent and server, and the record of each
call already names the source. No field is added, revision 29 told
consumers to tolerate unknown source values, and there is no new event type,
so the default sink list is unchanged. Example
`valid-audit-mcp-client-credentials.json` (values synthetic, the field set
equal to the six allow records read back from the eval stack at 598bef82 on
2026-09-21, where the fourteen refused calls carried no credential field:
[live]).

Revision 34 (2026-09-21, https://docs.straza.ai/security/keys-certificates-and-tokens/):
the life cycle of a signing key is on the chain. `straza.audit.admin`
gains the field `purpose`, the purpose of the key a record is about, and
lists five actions. `signing-keys.rotate` (`kid`) is the action the
reference producer has recorded since the session key rotation without
this document naming it: one record per call of the session rotate route,
with no `purpose`, which a consumer reads as `session`. For the purpose
`client_assertion`, the key that signs the client assertion of an agent's
client at the customer's identity provider, the actions are
`signing-keys.create` (`purpose`, `kid`; a key was staged while no key of
the purpose signs), `signing-keys.rotate` (`purpose`, `kid`; a key was
staged beside the one that signs), `signing-keys.promote` (`purpose`,
`kid`; the staged key became the signing key), and `signing-keys.retire`
(`purpose`, `kid`; the key left the key document, and `was`, the status it
held, is present when an administrator retired it by hand). Each of these
is recorded once for the whole deployment, by the replica whose store
write made the change, so a repeated call that changed nothing leaves no
record. Create, rotate and the retirement by hand carry the actor fields
of revision 18. Promote and the retirement that the rotation janitor makes
carry none, because no authenticated principal produced them, and the
absence is the statement. No record carries key material of either half.
Consumers MUST tolerate the new field and the new actions (additive under
the unknown-field rule of §2). No new event type, so the default sink
list is unchanged. Examples `valid-audit-admin-signing-keys-create.json`
and `valid-audit-admin-signing-keys-promote.json` (values synthetic, the
field sets equal to the five records read back from the eval stack at
9d6d8da3 on 2026-09-21: [live]).

Revision 33 (2026-09-18, https://docs.straza.ai/guides/operate/delegate-one-server/):
a role that is born or that goes leaves a `straza.audit.admin` record, so
the subject gains the actions `roles.create` and `roles.delete`, one
record per role, with `target` the role's id, `role` its name and
`server` the owning MCP server's name when the role is server-owned, a
role a server admin defines for their own server and named
`<server>-<suffix>`, absent on a global role. The emit sites are the
admin API role create and delete routes under either standing, and the
server removal, which deletes every role the server owns after its minted
admin role: each holder's membership chains one `roles.unassign` with the
reason `server removed`, then the role's `roles.delete` follows. The mint
of a server's admin role stays as revision 32 describes, an identity
event and no admin record. The same revision lists the actions the
reference producer has recorded without this document naming them:
`roles.update` (`target` the role id, `role`), `apps.binding.create`
(`app`, `role`, `tools[]`), `policy.create` and `policy.update` (`name`,
`id`, `priority`), `policy.delete` (`name`, `id`), and `policy.activate`
and `policy.deactivate` (`name`, `id`, `snapshot`, the snapshot that
resulted from the recompile). Consumers MUST tolerate the new fields
(additive under the unknown-field rule of §2). No new event type, so the
default sink list is unchanged. Examples
`valid-audit-admin-roles-create.json` and
`valid-audit-admin-roles-delete.json` (derived from the reference
producer and its lane tests, not captured from a live wire: [doc]).

Revision 32 (2026-09-17, https://docs.straza.ai/guides/operate/delegate-one-server/):
every MCP server names the Straza role that administers it, and the chain
says which. The `straza.audit.admin` action `apps.install` gains
`adminRole`, the role's name, on every record: a first install mints the
role, named `mcp-admin-<namespace>-<name>`, and an update names the one
the server has had since its mint, because the field is set once and
never changes. The mint itself, at registration and in the boot backfill
that covers servers from before the field, announces itself exactly as a
role create does, with a `straza.identity.updated` event carrying the
role's id and no admin record. A server's removal takes its minted role
with it, memberships included: `apps.remove` gains `adminRole`, the name
of the role that went, and every membership that ends with it chains one
`roles.unassign` record (revision 27) with `origin` as stored and the
`reason` `server removed`, before the role's own identity event. The pause
and the resume of a server, which the reference producer has recorded
without this document listing them, are the actions
`apps.disable` and `apps.enable` (`app`, `status`, and from this revision
`adminRole`), one record per call under the caller's name, so a server
admin's pause lands on the chain like every other delegated change.
Consumers MUST tolerate the new fields (additive under the unknown-field
rule of §2). No new event type, so the default sink list is unchanged.
Example
`valid-audit-admin-apps-install-minted.json` (derived from the reference
producer and its lane test TestServerAdminScope, not captured from a live
wire: [doc]).

Revision 31 (2026-09-15): a call that a rule allowed and a later gate
refused is recorded as the refusal, with the allowing rule. Revision 24
defined the gateway's own refusals before any decision, an unbound tool
name, a hidden tool and a throttled call, as `effect: deny` with no rule,
and that stays true. A hold a person denied, a hold that ran out of its
decision window, a bounded hold that ended while the window was still
live, and a call refused because no credential could be resolved for the
caller are a different case: a rule allowed the call, so the
`straza.audit.mcp` record is `effect: deny` with that rule's `ruleId` and
`setName`, `default` as the decision reported it, and the sentence the
model read as `reason`: `Straza: approval denied by NAME (ref ID)`,
`Straza: approval request expired after Ns with no decision (ref ID)`,
the pending sentence, or the credential sentence that says where a token
is set. A record refused for want of a credential carries no
`credentialId`, `credentialSource` or `credentialOwner`, because the call
never ran. An approved hold stays one `effect: allow` record with the
rule's own reason. The hook lane's `straza.audit.tool` record for the same
hold carries the rule the same way. No field is added or removed, the
envelope schema is unchanged and there is no new event type, so the
default sink list is unchanged. A gateway record written before the
producer change may say `allow` for a hold the person then denied; the
`straza.audit.approval` resolution record (§2.2) holds the verdict of
those. Example `valid-audit-mcp-hold-denied.json` (derived from the
reference producer and its lane test TestGatewayHoldRecordsVerdict, not
captured from a live wire: [doc]).

Revision 30 (2026-09-11): a change to an installed MCP server says what it
changed. The `straza.audit.admin` action `apps.install` gains `update`, a
boolean on every record: true when an app of that name was installed and
live, false for a first install and for an install under the name of a
removed app, which starts a new server. When `update` is true the record
also carries `changed[]`, the sorted manifest field paths that differ
between the stored manifest and the new one, dot-joined from the manifest's
own keys (`straza.runtime.remote.url`, `straza.limits.rps`,
`metadata.description`). An array counts as one path, a key present on one
side only counts as changed at its own path, and a block present on one
side only counts at each of its leaf paths, so a first rate limit reads
`straza.limits.rps`. A change anywhere inside the verbatim registry record
counts as the one path `server`, so every path comes from the manifest's
own grammar, and an identical manifest gives an empty list. The record never carries a value, because an address
or an environment entry can hold a token. `changed` is absent when the
stored manifest cannot be decoded, and consumers MUST tolerate unknown
paths (additive under the unknown-field rule of §2). No new event type, so
the default sink list is unchanged. Example
`valid-audit-admin-apps-install-update.json` (derived from the reference
producer and its lane test TestAppsInstallRecordsWhatChanged, not captured
from a live wire: [doc]).

Revision 29 (2026-09-10, https://docs.straza.ai/guides/serve-mcp-apps/caller-credentials/):
the gateway record says whose credential a call ran on. `straza.audit.mcp`
gains `credentialSource` beside `credentialId`, with the values `own` (the
caller's own row), `sponsor` (the sponsor's own row, under the sponsor's
opt-in) and `shared` (the server's static row), and `credentialOwner`, the
sponsor's user id, present only when the source is `sponsor`. Both are
absent when the call used no credential (additive under the unknown-field
rule of §2; consumers MUST tolerate unknown source values). A SIEM can now
tell an agent acting on its sponsor's account from a person acting on their
own. `straza.identity.updated` gains the actions `token.connect` (with
`user`, `app`, `setBy`, `credentialId`, `fingerprint`, `probe` and an
optional `expiresAt`), `token.disconnect` and `connection.agents` (with
`allow`), one record per act, the actor as `setBy`; `straza.audit.admin`
`apps.grant.remove` gains `kind`, `oauth` or `token`. No new event type, so
the default sink list is unchanged. Example `valid-audit-mcp-sponsor.json`
(derived from the reference producer and its lane test
TestConnectTokenLane, not captured from a live wire: [doc]).

Revision 28 (2026-09-07): a session that reaches its absolute lifetime ends
with a record, so `straza.audit.authn` `session.end` gains the `outcome`
value `lifetime-closed` beside `revoked-self|revoked-admin|idle-closed`
(additive under the unknown-field rule of §2, and consumers MUST tolerate
unknown `outcome` values). The envelope schema is unchanged. The producer
is the reference implementation's session janitor: once a minute it closes
every active session row started longer ago than
`governance.sessionMaxLifetime` (12 hours when unset), refreshed or not,
and emits one record per closed session with `userId`, `session` and a
`reason` that names the configured lifetime (`session older than the
maximum lifetime of 12h0m0s; the client starts a new session from its
device credential`). Like the idle close, the record carries no `sourceIp`
and no `userAgent`, because no client connection produced it. The next
refresh of the closed session answers 401 `session is no longer active`
and chains the login failure revision 21 defined. The refresh lane of the
reference producer now also consults the in-memory denylist before any
store read and chains that failure on a hit: a session entry answers 401
with the reason `session is no longer active`, a user or device entry
answers 403 with the reason `device or user revoked`, both reasons the
revision 21 producers already emitted. No new event type, so the default
sink list is unchanged. Example
`valid-audit-authn-session-end-lifetime.json` (derived from the reference
producer and its lane tests, not captured from a live wire: [doc]). In the
same revision the reference producer stops writing the `obligations[]`
field of `straza.audit.mcp` (revision 24): policyset revision 18 retired
the obligations list, no decision carries one, and a consumer keeps
tolerating the field on older records.

Revision 27 (2026-09-07): a role grant that starts or ends leaves a
`straza.audit.admin` record on every lane that writes one, so the subject
gains the actions `roles.assign` and `roles.unassign` (`target` the
assignment id, `user`, `role`, `roleId`, `origin` ∈ `scim|admin`, `reason`
when the caller has one). The emit sites are the admin API assignment
routes, a SCIM Groups membership write (one record per member that
changes, none for a write that changes nothing) and the admin API user
delete, which ends the user's grants with the reason `user deleted by
admin`. The envelope schema is unchanged.

Revision 26 (2026-09-07, no legacy period): the SCIM token kind
is removed from the product, so `actorVia` loses the value `scim-token`
and `straza.audit.admin` loses the actions `scim-token.create` and
`scim-token.revoke`; a SCIM-plane record carries `actorVia` `api-token`
with the admin API token's name and id. The envelope schema is unchanged.
This retires values the previous revision documented, as a stated exception
to the additive rule before the first public release.
Examples: `valid-audit-admin-scim-token-create.json` and the transitional
`valid-audit-admin-grant-remove-api-token.json` are deleted, and
`valid-audit-admin-grant-remove.json` now carries `actorVia` `api-token`
(shape from the reference producer and its test
TestSCIMPlaneAcceptsAdminAPITokenByScope: [doc]).

Revision 25 (2026-09-07): the token lifecycle joins the admin subject's
vocabulary and the subject row says which planes it records (additive
under the unknown-field rule of §2; the envelope schema is unchanged).
`straza.audit.admin` records `scim-token.create` (`name`, `id`),
`scim-token.revoke` (`id`), `api-token.create` (`name`, `id`, `scope`) and
`api-token.revoke` (`id`), which the reference producer has emitted since
the two mints shipped; listing leaves no record. The row's "emitted when"
column now says a mutation of an admin object over the admin API or over
SCIM, because a SCIM deactivation emits `apps.grant.remove` on this subject
with `actorVia` `scim-token`. With the one-token decision
(scim-profile revision 15) an admin
API token can act on the SCIM plane, so `actorVia` `api-token` may appear
on a SCIM-plane record too. No new event type, so the default sink list is
unchanged. Examples `valid-audit-admin-scim-token-create.json` and
`valid-audit-admin-api-token-create.json` (shape read back from the eval
stack's audit API on 2026-09-06 after minting both kinds: [live]) and
`valid-audit-admin-grant-remove-api-token.json` (derived from the reference
producer and its lane test TestSCIMPlaneAcceptsAdminAPITokenByScope:
[doc]).

Revision 24 (2026-09-04): the gateway's refused calls, its catalog reads
and the IdM's deprovisioning wipe leave records (additive under the
unknown-field rule of §2; the envelope schema is unchanged).
`straza.audit.mcp` now records every `tools/call` the gateway refuses
before a policy decision, with `effect: deny` and no rule: an unknown or
unbound tool name (reason `unknown tool "NAME": no access row for this
session's roles admits it`, `toolName` the requested name, `app` empty),
a tool the session's catalog overlay hides (the engine's own deny with
its `ruleId`, `setName` and `reason`, while the client still reads
unknown tool), and a throttled call (the rate-limit sentence as the
reason). The same type records every `tools/list` with the new `event`
value `tools.list`, `effect: allow`, `tool`, `app` and `toolName` empty,
reason `catalog served` and a numeric `count` of the tools in that
response, one record per page. A record whose firing rule carries
obligations now lists them in `obligations` (the rule's `redact` and
`notify` values, absent otherwise); obligations are recorded, not
executed. `straza.audit.admin` gains the action `apps.grant.remove`
(with `app`, `user`, `credentialId`, `reason`): a SCIM deactivation
removes every per-user grant of the user, one record per row, and
`actorVia` gains the value `scim-token` with the SCIM token's name as
`actor` and its id as `actorId`. No new event type, so the default sink
list is unchanged. Examples `valid-audit-mcp-list.json` and
`valid-audit-admin-grant-remove.json` (derived from the reference
producer and its lane tests, the shapes read back from the eval stack's
audit API the same day: [live]).

Revision 23 (2026-09-04): the access facts on the gateway record and the
app-lifecycle admin actions (additive under the unknown-field rule of §2;
the envelope schema is unchanged). `straza.audit.mcp` gains `granted`
(boolean, the spec/policyset revision 17 fact: the subject held a role
whose access row admitted the tool), `default` (boolean, the decision's
default marker: no rule fired), `bindingId` and `role` (the access row
that admitted the call and the role holding it) and `credentialId` (the
credential row the upstream call used). `granted` is false and the three
ids absent on the built-in `straza` app; `credentialId` is absent when the
call used none. A SIEM can now tell an allow by rule from an allow on the
grant alone. `straza.audit.admin` gains the actions `apps.install` (with
`app`, `runtime`), `apps.remove` (with `app`, `roles[]`: the roles whose
access rows went), `apps.binding.delete` (with `app`, `role`, `tools[]`,
`bindingId`) and `apps.secret.remove` (with `app`, `role`, empty for the
server's own secret), each with the revision 18 actor triple. No new
event type, so the default sink list is unchanged. Example
`valid-audit-mcp-granted.json` (derived from the reference producer and
its lane tests, not captured from a live wire: [doc]).

Revision 22 (2026-08-31): `login` gains the `via` value `client-assertion`
and its producer: the built-in issuer's client_credentials grant, the AI
agent sign-in, emits a login success or failure at token mint, in BOTH profiles
(the grant was silent before this revision; the later deviceless checkin
still emits its own `id-token` login, a separate true fact). The rev 21
honesty rules carry over unchanged: an attacker-chosen `client_id` is never
echoed, so a failure for an unknown or ineligible client carries no `user`
and no `userId`, while a failure judged against a known NHI's key names it.
`reason` on failures is one of `unknown or ineligible client`,
`client assertion rejected`, `client assertion replayed`. A malformed token
request (missing assertion) and a mint outage emit nothing: no credential
was judged. Example `valid-audit-authn-login-nhi.json` (derived from the
reference producer implementation and its lane tests, not captured from a
live wire: [doc]). Additive for consumers: a new value in an existing
field (unknown-field rule §2; consumers MUST tolerate unknown `via`
values).

Revision 21 (2026-08-26): `straza.audit.authn` is elaborated and gains its
first producers (the subject has been in the taxonomy and the default sink
list since v1alpha1 with ZERO emit sites; web logins left no chained trace
at all). Payload: `action` ∈ `login|session.end`. For `login`, `outcome` ∈
`success|failure` and `via` ∈ `id-token|session-token|device-token|password`
names the credential lane. For `session.end`, `outcome` ∈
`revoked-self|revoked-admin|idle-closed` (revision 28 adds
`lifetime-closed`). `reason` rides failures and ends.
`user` (username) and `userId` appear when the producer actually knows them
and are NEVER fabricated: a failed password submit for an unknown username
carries neither, and janitor closes carry `userId` only. `harness` (the
`name/version` form) appears when a harness-bearing request produced the
event. `session` names the session started or ended when one exists.
`sourceIp` (the transport peer, never X-Forwarded-For) and `userAgent`
(producer-capped at 256 bytes) are a documented doctrine exception scoped
to authn events, present ONLY when a client connection produced the event;
janitor events carry neither, and absence means exactly "no client
connection produced this event". Producers (reference implementation):
checkin login success on the id-token lane only (a refresh success is
DELIBERATELY unlogged: a 30-second daemon tick per session would flood the
chain with no authn information; the session lifecycle is already
witnessed by `straza.identity.updated` session.start), every checkin
401/403 refusal on all three lanes (400s and outages are excluded: a
malformed body or a store blip is not an authentication outcome), the
built-in issuer's failed password submit via `password` (post-zeroing
username; unknown or expired device codes emit nothing, they carry no
credential), self and admin single session revokes on the real row
transition only, and the idle janitor per closed session. The minimum for
this subject accordingly relaxes from `user,action,outcome` to
`action,outcome` (user fields are when-known). The un-pinned
`checkin.denied` action on `straza.identity.updated` RETIRES: its one emit
site (the minAttestation refusal) now emits a chained authn login/failure
instead; identity.updated is not hash-chained, so the replacement is a
strict audit upgrade. Consumers of historical records keep accepting
`checkin.denied` (records keep their truth). Examples
`valid-audit-authn-login.json` + `valid-audit-authn-session-end.json`
(derived from the reference producer implementation and its lane tests,
not captured from a live wire: [doc]). Additive for consumers
(unknown-field rule §2).

Revision 20 (2026-08-24): the decision names its policy.
`setName` joins the `straza.audit.tool` and `straza.audit.mcp` minimum:
the name of the PolicySet whose rule decided the call, exactly as the
engine reported it, so a consumer can say "decided by rule X in policy
Y" from the record alone. Producers always emit the field from this
revision; a profile-default decision (no rule matched) carries the
empty string, the same honesty contract as its empty `ruleId`.
Consumers stay absent-safe forever: records written before this
revision lack the field, and a consumer MUST NOT reconstruct a missing
name by looking the rule id up in today's policies (a rule id can move
between sets; the record keeps its own truth). Additive for consumers
(unknown-field rule §2).

Revision 19 (2026-08-14): decided attribution on `straza.audit.approval`
`phase: resolution` (openapi 0.63.0). `channel`
becomes the decider's honest SURFACE: `console|slack|phone|browser`
(device-signed decisions carry the enrolled device's platform family
instead of the retired transport constant `api`; records resolved before
this revision keep `api`, consumers keep accepting it). Two additive
fields, both omitted when empty. `decidedReason` carries the decider's
own words (≤500 bytes, both verdicts, validated fail-closed at intake; on
the device-signed lanes its sha256 is the signed string's 5th line, so the
words carry the same key binding as the verdict). `decidedDeviceId` names
the enrolled device whose key signed the decision (absent on
console/Slack lanes). The reason is the deliberate human-authored
exception to the model-text-off-resolution privacy rule: `justification`
stays request-phase-only precisely BECAUSE it is model-authored, while
`decidedReason` is a human's key-bound statement and belongs on the
resolution record an auditor reads. Additive for consumers
(unknown-field rule §2).

Revision 18 (2026-08-01): admin action attribution. `straza.audit.admin`'s
minimum payload has named `actor` since v1alpha1, but the reference
producer never emitted it: every admin mutation was anonymous on the
chain (found by the 2026-08 enterprise-readiness audit; the fix is a
conformance repair, not a new promise). The field is now elaborated and
emitted: `actor` = the authenticated principal's human-readable handle
(username, or admin-API-token name), `actorId` = its durable id (user id /
token id), `actorVia` ∈ `login|session|api-token`, the credential lane
that performed the mutation, so a leaked-token incident reads WHICH
credential acted. Producers MUST omit the fields entirely when no
authenticated principal exists (internal/background producers): an
unknown actor is absent, never fabricated. Additive for consumers
(unknown-field rule §2).
Example `valid-audit-admin.json` (shape mirrors the reference producer's
emit; derived from the implementation + its lane test, not captured from a
live wire: [doc]).

Revision 17 (2026-07-29): bulk session stand-down.
New subject `straza.revocation.sessions`: ONE control event carries a SET
of revoked session ids (`data.sessions[]`, optional `data.reason`). A
fleet-scale kill no longer emits one control event per session (100k
sessions were 100k outbox rows queued ahead of every later control event).
Producer-capped: the reference producer chunks at 1000 ids per event, so a
larger set is O(set/1000) events, never O(set). Consumers MUST apply each
listed id exactly as they apply a `straza.revocation.session` event; ids
they have already applied are no-ops (the denylist is a set). The §5 push
rule is UNCHANGED: push subjects stay scoped to exactly one target id;
the bulk envelope exists only on the durable JetStream path. Per-target
`straza.revocation.session` remains valid and is still what single
revocations emit. Additive; example `valid-revocation-sessions.json`.

Revision 16 (2026-07-29): capture events are chained content-free.
NO wire change: `straza.audit.prompt`/`reply` are
published exactly as before. What changed is §3's storage rule for those two
types: the chain writer strips `data.content` and records
`data.contentBytes` (the stored-content length) beside the existing
`data.contentHash`, so the chain witnesses the conversation by hash and size
while the content itself lives only in retention-bounded stores (the
JetStream stream, the outbox window, the `conversation_turns` read model).
Without this, verbatim transcripts outlived every retention knob inside the
append-only chain. Stored-form fixture:
`examples/valid-audit-prompt-chained.json` (the chained twin of
`valid-audit-prompt.json`; the wire schema is unchanged).

Revision 15 (2026-07-28): origin on revocation and lift (spec/scim-profile
revision 4). `straza.revocation.user` and `straza.revocation.lift` gain
`origin` (`scim|admin|external` on revoke; on lift, the authority whose
action produced the full lift). A lift event still means exactly "this user
is no longer denied": an origin-selective SCIM lift that leaves a
surviving lock emits NO lift event (enforcement is unchanged on every pod;
replayed history converges). `straza.audit.identity` actions gain
`user.lift.blocked` (an IdM reactivation attempted while a Straza lock
survives; carries `heldBy[]` origins) and `user.killed`/`user.reactivated`
gain `origin`. Envelope unchanged; additive. Examples updated +
`valid-audit-identity-lift-blocked.json`.

Revision 14 (2026-07-27): client binary rename. The CloudEvents `source` on
client-emitted audit events becomes `straza` (was `straz`, revision-10 value).
The envelope schema never constrained the value (`minLength: 1`) and the
server stamps `source` at ingest; consumers MUST NOT key logic on the source
string (it says where the event came from, not where it goes; routing is
the subject taxonomy). No legacy emitters exist (the rename precedes the
first release). Examples updated; no schema change; additive.

Revision 13 (2026-07-26): delegate attribution (subagent capture, tagged
with the delegate). `straza.audit.reply`, `straza.audit.prompt`
and `straza.audit.tool` gain OPTIONAL `agentType` (delegate class, e.g.
"researcher") and `agentId` (delegate instance), written only when the
harness attributed the payload to a subagent; ABSENCE means the main agent's
own lane, so consumers must not default them. A reply captured at
`subagent.stop` (the delegate's own transcript delta, or its payload-borne
final message when that file is unavailable) is attributed to the PARENT
session id with `agentType` as the tag; the conversation read model stores
`agent_type` (openapi 0.31.0). Envelope unchanged; additive. Example:
`valid-audit-reply-subagent.json`.

Revision 12 (2026-07-25): approval fingerprint v2 (spec/policyset revision
7). Everywhere an `argvHash` travels (approval audit payloads, the
resolution broadcast), newly minted
values use the versioned key format `v2:<call|tool>:sha256:<hex>`. The
scope tag is the per-record truth of what the fingerprint covers. Records
minted before this revision keep their `sha256:<hex>` (v1) values; consumers
MUST treat the field as an opaque string (it already was one). No envelope
or field-name change; additive.

Revision 11 (2026-07-23): the `straza.audit.approval` `phase` enum gains
`consumed`, emitted when a **ticket** grant (spec/policyset revision 6
`class: ticket`) is cashed by a later call (§2.2). It carries the consuming
session and the consume timestamp, the durable proof that a day-old grant was
actually used and by whom. Envelope and `data` shapes are unchanged (the
`phase`/`state` enums live in the payload, which the schema treats as an
opaque object); additive, the same footing as the revision-7 push note.

Revision 10 (2026-07-21): the `straza.*` subject taxonomy, the `straza.dev`
schema id and the sources `strazad`/`straz` replace the pre-release names;
envelope and payload shapes unchanged.

Revision 9 (2026-07-18): added `straza.audit.approval`, request and
resolution records for the human-approval workflow (`mode: approve`;
spec/policyset §2 item 8). Emitted once
at approval request and again at resolution (approved/denied/expired);
hash-chained like every audit event; forgery guard: a client-submitted CE of
this type coerces to `straza.audit.tool` (the sentinel precedent, §2.2). The
in-cluster coordination subject `straza.approval.resolved.<id>` (§5.1) is
core-NATS, notify-only, and never client-facing (additive).

Revision 8 (2026-07-17): added `straza.audit.sentinel`, verdicts from the
strazad in-process audit sentinel, an asynchronous consumer of the audit
stream that judges sessions rather than events, entirely off the request
path. Alert-only in this
revision: the sentinel is detection and fails open-with-alarm
(deliberately opposite the classify lane's fail-closed posture) and its
verdicts ride `straza.audit.>` into the hash chain, so the judge is
judged. Auto-revoke does not exist in this revision (§2.1) (additive).

Revision 6 (2026-07-16): audit decision payloads document the full "what"
(additive; producers shipped 2026-07-15). `straza.audit.tool` gains
conditional `toolName`, `paths[]`, `workspace`, and clients now actually
send `snapshot`, which the minimum always required. `straza.audit.mcp`
gains `arguments` (the verbatim `tools/call` arguments as JSON text,
producer-capped; reference producer 8 KiB) with `argumentsTruncated: true`
when cut; like capture content, what was recorded is bounded and the cut is
witnessed.

Revision 5 (2026-07-15): added `straza.audit.prompt` / `straza.audit.reply`
for policy-opted-in conversation capture (spec/policyset revision 2
`capture:` block). Content is
capped per event; a truncated event carries the full-content SHA-256 so
the chain witnesses what it did not store (additive).

Revision 4 (2026-07-15): added `straza.audit.identity`: kill-switch and
reactivation actions now leave hash-chained audit entries. The
`straza.revocation.*` events converge enforcement but only `straza.audit.>`
enters the chain, so a killed user previously left no verifiable trace
(additive).

Revision 3 (2026-07-14): added `straza.apps.updated`, the multi-pod
convergence hint for catalog/credential-affecting control-plane changes
(P5.5b; additive).

Revision 2 (2026-07-13): added `straza.revocation.lift` and documented the
`straza.push.>` client push channel (§5).

Revision 7 (2026-07-17): added the `straza.push.policy` notify-only
broadcast subject (§5, policy-update push); revocation-subject semantics
unchanged. No schema or fixture change: push messages are not CloudEvents,
same footing as revision 2.

Words MUST/SHOULD/MAY are RFC-2119.

## 1. Envelope

Every event on the Straza spine is a CloudEvents 1.0 JSON object with
`specversion`, `id`, `type`, `source`, `time`, `data` (all required). The
NATS subject a message is published on equals its CE `type`. Producers MUST
mint a unique `id`; delivery is **at-least-once** and every consumer MUST
dedupe by `id`.

## 2. Subject taxonomy

| Type / subject | Emitted when | data payload (minimum) |
|---|---|---|
| `straza.audit.tool` | a local-tool decision (hook PDP or `/v1/decide`) | `session,user,harness,event,tool,command,effect,ruleId,setName,reason,snapshot` (rev 20: `setName` = the deciding PolicySet, empty when no rule matched; absent on pre-rev-20 records); adds `toolName` (mcp.call upstream tool), `paths[]` (file/url targets), `workspace` when the event carries them; adds `agentType,agentId` (rev 13) when the harness attributed the call to a delegate, so a deny inside a subagent is then distinguishable from the main agent's |
| `straza.audit.mcp` | a gateway `tools/call` decision or refusal, or a served `tools/list` (rev 24), or a view list or read on a server's endpoint (rev 42) | `session,user,harness,event,tool,app,toolName,effect,ruleId,setName,reason,snapshot` (rev 20: `setName` as on `straza.audit.tool`); adds `arguments` (verbatim `tools/call` arguments as JSON text, producer-capped; reference producer 8 KiB) + `argumentsTruncated: true` when cut; rev 23: `granted` and `default` (booleans, always present), `bindingId` + `role` (the access row that admitted the call, absent on the built-in `straza` app) and `credentialId` (the credential row the upstream call used, absent when none); rev 24: a refused call (unbound name, hidden tool, throttle) is `effect: deny` with the refusal as `reason` and no rule, a served `tools/list` is `event: tools.list` with `effect: allow`, `reason: catalog served` and `count` (the tools in that response), and `obligations[]` lists the firing rule's obligations (absent when none; never written since rev 28, policyset revision 18 having retired the list); rev 31: a call a rule allowed and a later gate refused (a denied, expired or pending hold, or no credential resolvable for the caller) is `effect: deny` with the allowing `ruleId` and `setName` and the refusal sentence as `reason`, and a credential refusal carries no credential fields; rev 35: `credentialSource` gains `client_credentials` (a token of the agent's own client at the server's provider), with `credentialId` of the form `client_credentials:<server id>:<user id>` and no `credentialOwner`; rev 37: `argumentsDigest` and `argumentsSize` in place of `arguments` on `straza__draft_submit`; rev 42: a served view list on a server's endpoint `/mcp/{server}` is `event: resources.list` with `effect: allow`, `reason: views served`, `app` the server and `count` (the views in that response), a view read is `event: resources.read` with `app` and `uri` (the address asked for, producer-capped like `arguments`, with `uriTruncated: true` when cut), `effect: allow` with `reason: view served` or `effect: deny` with the refusal sentence, and a `tools/list` on a server's endpoint names the server in `app` |
| `straza.audit.admin` | a mutation of an admin object, over the admin API or over SCIM, or from the gateway's drafting tools, or from strazad's own background work, which names no actor, or (rev 40) an approver device enrolled with a person's enroll token (rev 25: `actorVia` names the credential, `action` names the object) | `actor,action,target`; rev 18: `actor` (username or admin-API-token name) + `actorId` (user/token id) + `actorVia` ∈ `login\|session\|api-token\|enroll-token` (rev 40: `enroll-token` on `approver.enroll`, whose actor is the person whose one-time enroll token was consumed, and consumers MUST tolerate unknown `actorVia` values; rev 26: `scim-token` retired with the SCIM token kind; a SCIM-plane record carries `api-token` with the admin API token's name as `actor` and its id as `actorId`); all three OMITTED when no authenticated principal produced the event (never fabricated); rev 23 actions: `apps.install` (`app,runtime`; rev 30 adds `update` on every record and `changed[]`, field paths and never values, when `update` is true; rev 32 adds `adminRole`, the name of the role that administers the server), `apps.remove` (`app,roles[]`; rev 32 adds `adminRole`, the minted role that went with the server, its memberships each chaining a `roles.unassign` with reason `server removed`), rev 32 actions `apps.disable` and `apps.enable` (`app,status,adminRole`; recorded by the reference producer before rev 32 and listed from it), `apps.binding.delete` (`app,role,tools[],bindingId`), `apps.secret.remove` (`app,role`); rev 24 action: `apps.grant.remove` (`app,user,credentialId,reason`, one per per-user grant a SCIM deactivation removes); rev 25 actions: `api-token.create` (`name,id,scope`), `api-token.revoke` (`id`), and listing leaves no record (rev 26 retired `scim-token.create` and `scim-token.revoke` with the kind); rev 27 actions: `roles.assign` and `roles.unassign` (`target` the assignment id, `user`, `role`, `roleId`, `origin` ∈ `scim\|admin`, `reason` when the caller has one; one record per membership that starts or ends, over the admin API or over SCIM); rev 33 actions: `roles.create` and `roles.delete` (`target` the role id, `role` its name, `server` the owning server's name when the role is server-owned; one record per role that is born or that goes, the server removal cascade included); listed from rev 33 and recorded by the reference producer before it: `roles.update` (`target` the role id, `role`), `apps.binding.create` (`app,role,tools[]`), `policy.create` and `policy.update` (`name,id,priority`), `policy.delete` (`name,id`), `policy.activate` and `policy.deactivate` (`name,id,snapshot`, the snapshot that resulted); rev 34: the field `purpose` and the signing key actions `signing-keys.create`, `signing-keys.rotate`, `signing-keys.promote` and `signing-keys.retire` (`purpose,kid`, plus `was` on a retirement by hand; one record per change for the whole deployment, promote and the janitor's retire without the actor fields), and `signing-keys.rotate` (`kid`, no `purpose`) for the session key, recorded by the reference producer before rev 34 and listed from it; rev 37 actions: `draft.create`, `draft.update`, `draft.check`, `draft.contact`, `draft.publish`, `draft.discard`, `draft.expire`, `roles.implication.create` and `roles.implication.delete`, and `draft` on every record a publish writes; rev 40 actions, one record per identity setup write that succeeds and none for a refused one, each with `user` the user's id and all but `nhi-key.removed` with `username`: `user.create` (`target` and `user` the new user's id, `username`, `kind` ∈ `human\|nhi`, `userType` when set, `origin` ∈ `admin\|scim`, rev 41: `scim` on a create or a revive over SCIM, and the revive's `user.create` follows its `user.reactivated` or `user.lift.blocked` record), `user.update` (`target`, `user`, `username`, `changed[]`, the field names a write changed and never values, rev 41: also a SCIM PUT, PATCH or DELETE, whose `changed[]` can carry `username` and `external_id`, and whose record of a status change follows the `user.killed`, `user.reactivated` or `user.lift.blocked` record of that change where the admin API's precedes it, so a consumer pairs them by `user`; a write that changes nothing writes no record), `enroll-token.create` (`target` and `user` the id of the user the token is for, `username`, plus `channel` and `self: true` on the self route; never the token), `approver.enroll` (`target` and `device` the device id, `user` the user id, `username`, `platform`, `name`; actor the person, `actorVia` `enroll-token`), `nhi-key.set` (`target` and `user` the agent's id, `username`, `fingerprint` `sha256:<hex>` of the public key; never the key), and `nhi-key.removed` (`target` and `user` the agent's id, `reason` on a user delete), recorded by the reference producer on a user delete before rev 40 and listed from it; rev 41 action: `approver.revoke` (`target` and `device` the device id, `user` the user id, `username`, `reason` `user deleted by admin`; one record per approver device a user delete removes) |
| `straza.audit.authn` | login and session-end outcomes (rev 21, 22, 28, 39, 41) | `action,outcome` with `action` ∈ `login\|session.end`; `login` adds `via` ∈ `id-token\|session-token\|device-token\|password\|client-assertion\|slack\|approver-token` and `outcome` ∈ `success\|failure`; `session.end` has `outcome` ∈ `revoked-self\|revoked-admin\|idle-closed\|lifetime-closed` (rev 28: the janitor's absolute-lifetime close); `user`/`userId`/`session`/`harness` when known (never fabricated); `reason` on failures and ends; `sourceIp` (transport peer, never XFF) + `userAgent` (capped 256 B) ONLY when a client connection produced the event (janitor events and a `slack` login failure carry neither: from rev 39 their absence means that no connection from the person's own client produced the event); rev 39: a `slack` login failure adds `approvalId`, the request the refused tap tried to decide, and carries no `session`, `harness`, `sourceIp` or `userAgent`; rev 41: an `approver-token` login failure carries `sourceIp`, `userAgent` when sent, and no `session` or `harness`, and on a decide refusal `approvalId` |
| `straza.audit.identity` | the identity kill switch fires, is lifted, or an IdM lift is blocked by a surviving lock (SCIM `active:false`/DELETE, admin disable/lock, reactivation/unlock) | `user,action,origin`: `action` ∈ `user.killed\|user.reactivated\|user.lift.blocked` (rev 15); `user.killed` adds `reason,sessionsRevoked`; `user.lift.blocked` adds `heldBy[]` (the surviving lock origins) |
| `straza.audit.prompt` | capture-enabled session: the user submitted a prompt | `session,user,harness,content,mode,truncated,contentHash`: `mode` ∈ `verbatim\|redact`; `contentHash` = SHA-256 of the FULL original content; `content` capped (client knob, default 64 KiB); adds `agentType,agentId` (rev 13) when a delegate produced it |
| `straza.audit.reply` | capture-enabled session: the model (or, rev 13, one of its delegates at `subagent.stop`) finished a response | same shape as `straza.audit.prompt`, and a delegate's reply carries `agentType` (+`agentId`) and rides the PARENT session id |
| `straza.audit.sentinel` | the in-process audit sentinel emits a verdict about a session (§2.1) | `session,detector,severity,reason,evidence[]`: `detector` ∈ `denyBurst\|denyThenVariant\|writeThenExecute\|captureContent\|toolMixAnomaly`; `severity` ∈ `info\|warn\|critical`; `reason` = one human-readable sentence naming the evidence; `evidence[]` = CE `id`s of the triggering audit events; adds `user` when known and `window` (e.g. `"60s"`) when the detector is windowed |
| `straza.audit.approval` | a `mode: approve` gate is requested, when it resolves, and when its approval is used, a ticket's grant or, since revision 38, a hold's approval (§2.2) | `phase,approvalId,session,user,rule,set,lane,summary,state`: `phase` ∈ `request\|resolution\|consumed`; `lane` ∈ `hook\|gateway`; `state` ∈ `pending\|approved\|denied\|expired`; adds `justification` (gateway lane, `phase=request` only), `decidedBy,channel` plus the optional `decidedReason,decidedDeviceId` (revision 19) on `phase=resolution`, and `consumedBy,consumedAt` on `phase=consumed` (both classes since revision 38) |
| `straza.policy.updated` | a snapshot activates | `snapshot,sets` |
| `straza.revocation.user` | user disabled/deactivated/locked | `user,reason,origin`: `origin` ∈ `scim\|admin\|external` (rev 15: which authority created the row; the SCIM lift removes scim-origin rows only) |
| `straza.revocation.session` | session revoked | `session` |
| `straza.revocation.sessions` | bulk session stand-down: ONE control event carries the whole set (rev 17) | `sessions[],reason?`, producer-capped (reference producer: 1000 ids/event, larger sets chunk); consumers apply each id as a `straza.revocation.session` |
| `straza.revocation.device` | device disabled | `device` |
| `straza.revocation.lift` | user fully reactivated: NO revocation lane remains (rev 15: a scim-selective lift that leaves a lock emits no lift) | `user,origin` |
| `straza.apps.deployed` | app installed/updated & serving | `app,name,version,runtime,source` |
| `straza.apps.removed` | app removed/stopped | `app,name` |
| `straza.apps.drift` | upstream tool inventory changed vs. cache | `app,name,added[],removed[]` |
| `straza.apps.updated` | a catalog/credential-affecting change that is not a deploy/remove: tool-binding CRUD, app secret set, per-user grant connect/disconnect, and (rev 37) every publish of a draft | `change` ∈ `binding\|secret\|grant\|publish` (+ `app` when known; rev 37: `publish`, one per publish, carries `draft`, `snapshot` and `apps[]`). Advisory reload hint: consumers re-read the store rather than trusting the payload |
| `straza.identity.created/updated/deactivated` | identity lifecycle | `user`-scoped fields |

Payloads MAY carry additional fields; consumers MUST ignore unknown fields.

### 2.1 Sentinel verdicts (`straza.audit.sentinel`)

Emitted by the strazad in-process audit sentinel: an asynchronous consumer
of the `straza.audit.>` stream that judges sessions rather than events. It
runs entirely off the request path: a verdict never blocks or gates a tool
call, and a deployment without the sentinel pays nothing.

The sentinel is **detection, not prevention**, and its availability posture
is deliberately the opposite of the classify lane's: prevention fails
closed; the sentinel **fails open-with-alarm**. A down sentinel blocks
nothing: it raises an operational alarm and leaves a gap in detection, like
any sensor. Stating this pair explicitly is part of the honesty contract
(https://docs.straza.ai/concepts/evidence/).

Verdicts are themselves first-class audit events: subject == type (§1), so
they ride the audit stream and the existing audit-chain consumer hash-chains
them like every other `straza.audit.*` event (§3), so the judge is judged by
the same chain it reads. `evidence[]` carries the CE `id`s of the triggering
audit events, so a verifier can walk from a verdict back to its chained
evidence.

This revision is **alert-only**: consumers MAY alert on, surface, or page
from a verdict, but auto-revoke does not exist in this revision. A future
revision MAY add a policy-gated knob routing critical verdicts into the
existing revocation machinery; it will ship off by default.

### 2.2 Approval records (`straza.audit.approval`)

Emitted by the strazad approval service for the human-approval workflow
(`mode: approve`; spec/policyset §2 item 8; https://docs.straza.ai/concepts/approvals-model/).
Two records mark each gate, and a third marks the one use of an approval:

- `phase: request`, written when a gated allow first needs a human: carries
  `approvalId,session,user,rule,set,lane,summary` and, on the **gateway**
  lane only, the agent-supplied `justification` (a model-generated,
  unverified string: consumers MUST NOT treat it as ground truth; the hook
  lane has no schema to inject into and omits it).
- `phase: resolution`, written when the gate resolves: same identifying
  fields plus the terminal `state` (`approved|denied|expired`), `decidedBy`
  (the deciding user), and `channel`, the decider's surface,
  `console|slack|phone|browser` (revision 19; pre-revision records carry
  the retired transport constant `api`). When the decider gave one,
  `decidedReason` carries their own words (key-bound on the device-signed
  lanes) and `decidedDeviceId` the enrolled signing device; both are
  omitted when empty (revision 19).
- `phase: consumed` (revision 11), written when a **ticket** grant
  (spec/policyset revision 6 `class: ticket`) is cashed by a later call, on
  the atomic consume win: same identifying fields (with `state` still
  `approved` because consumption does not change the terminal decision) plus
  `consumedBy` (the consuming session) and `consumedAt` (when the grant was
  cashed). Since revision 38 a **hold** emits it too, when its approval is
  used by the held call or by one identical retry of the same session
  within `retryTTLSeconds` (spec/policyset revision 23). The use is one
  atomic write on the persisted record, so at most one record is written per
  approval on any number of replicas, and an approval nobody used writes
  none. This record is the durable proof that an approval was actually used,
  by which session and when.

`summary` is the human-readable label of the gated call (`mcp.call
<app>:<tool>` or a command summary), never the full arguments or
transcript, honoring the workflow's privacy rule. Like every `straza.audit.*`
event these records ride the hash chain (§3) and existing SIEM sinks.

**Forgery guard.** `straza.audit.approval` is a server-authored subject. A
client that submits a CloudEvent of this type over the audit-ingest path is
coerced to `straza.audit.tool` by the existing producer allowlist (the same
guard that protects `straza.audit.sentinel`), so a compromised agent cannot
mint or overwrite an approval record. This is enforced by the server, not by
this envelope profile; it is documented here so the trust boundary is legible.

## 3. Audit hash chain

The audit consumer mirrors every `straza.audit.*` event into an append-only
chain. Record: `{seq, ce, prevHash, hash}` where `seq` is monotonic, `ce` is
the exact CloudEvent JSON as published, EXCEPT `straza.audit.prompt` /
`straza.audit.reply` (revision 16): the chain writer removes `data.content`
and adds `data.contentBytes` before chaining, so the append-only chain
witnesses conversation content by `contentHash` + size without retaining the
text past the retention-bounded stores. The hash covers the stored (stripped)
bytes:

    hash = hex(sha256(prevHash || "\n" || ce))

with `prevHash = ""` (genesis) for the first record. A verifier walks records
in `seq` order and reports the first record whose `prevHash` mismatches its
predecessor's `hash` or whose `hash` mismatches the recomputation. Duplicate
CE `id`s MUST NOT create additional chain entries.

## 4. Versioning

`straza.dev/events/v1beta1`. Changes follow the additive-change rule in the
spec README: schema + fixtures + version bump in the same PR, named in the
release notes.

## 5. Client push channel

The push channel is deliberately **not** part of the CloudEvents spine:
messages are core-NATS (at-most-once, fire-and-forget); the durable paths
remain the JetStream events. It carries two subject classes with DIFFERENT
semantics: a daemon classifies by exact subject, never by payload.

**Revocation subjects** are `straza.push.<kind>.<id>` (kind ∈
`user|session|device`): the low-latency kill-switch push. The subject itself
carries the routing (a daemon subscribes only to its own principal's
subjects); the durable path remains the `straza.revocation.*` JetStream
events. Payload: `{"kind": "<kind>", "id": "<id>"}`, informational only. A
daemon MUST treat **any** message on one of its subscribed revocation
subjects as a revocation of that principal and MUST NOT rely on the payload.
Producers MUST scope every revocation publish to exactly one target id;
there is no broadcast revocation subject. Reactivation is never pushed: a
lifted user re-enrolls, so daemons have no un-revoke path.

**The policy nudge**, `straza.push.policy` (revision 7), is the one
**broadcast, notify-only** subject: the pod that recompiles and activates a
policy snapshot publishes `{"kind": "policy", "snapshot": "<id>"}` once per
activation. A daemon subscribed to it MUST treat a message there ONLY as a
hint to refresh through its normal authenticated channel: fetch the
snapshot and **verify it against the pinned keys** before adopting, exactly
as on a poll tick. The payload is informational; enforcement never trusts
it, so a forged or replayed nudge can cause at most a verified fetch. The
revocation rule above does NOT apply to this subject; a policy nudge MUST
NOT drop session state. Poll-refresh remains the fallback: the nudge only
tightens propagation latency for idle sessions (target: adopt within 5 s of
activation). Snapshot ids are not secrets (every enrolled client can read
`/v1/snapshot`), so broadcasting them leaks nothing.

### 5.1 In-cluster approval coordination (`straza.approval.resolved.<id>`)

A **separate** core-NATS subject class, server-to-server, never
client-facing. When a `mode: approve` gate resolves on any pod, that pod
publishes once on `straza.approval.resolved.<id>` (`<id>` = the approval
record id) so the pod holding a blocked gateway call reacts without
cross-pod DB polling (stateless data plane). Since revision 38 no pod of
this revision grants anything from this message: the use of an approval is
an atomic write on the persisted record (§2.2), so a lost or late broadcast
cannot let a call run twice, and a held call that misses it and wakes on
its own timer still uses its approval (spec/policyset revision 23). Until
it is replaced, a replica of an older build runs every held call it holds
on the approval and grants one retry from this message. So during a
rolling upgrade one approval can run once as each held call on an older
replica, once more on each older replica, and once on the replicas of this
revision. `retryTTLSeconds` stays in the payload for those older replicas.
No daemon subscribes; this is not part of the `straza.push.>` client channel
and the revocation rule above does not apply. Payload (informational; the
authoritative state is the persisted record):

```json
{
  "id": "<approvalId>",
  "state": "approved|denied|expired",
  "decidedBy": "<userId>",
  "session": "<sessionId>",
  "ruleId": "<ruleId>",
  "argvHash": "v2:<call|tool>:sha256:<hex> (revision 12; pre-revision-12 records carry sha256:<hex>)",
  "retryTTLSeconds": 60,
  "decidedAt": "<RFC3339>"
}
```

Like the push channel these messages are core-NATS (at-most-once,
fire-and-forget); a missed broadcast just means the blocked waiter times out
and denies (fail-closed, acceptable). The durable record of the resolution is
the `straza.audit.approval` `phase: resolution` CloudEvent (§2.2).
