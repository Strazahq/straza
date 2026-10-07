---
title: Events and the audit record
description: List every audit event Straza emits and the fields of the audit record.
pagetype: reference
weight: 40
draft: false
keywords: events audit record subjects fields
---


An event is one CloudEvents 1.0 JSON object that Straza publishes when something it governs happens: a tool decision, an admin mutation, a login, a revocation, a policy activation or an MCP server lifecycle change. The type is also the subject. A consumer therefore filters by type alone. Every type under `straza.audit.` is appended to the hash chain after publication, and every type except the recorded prompts and replies reaches a sink that sets no filter. [Evidence]({{< relref "concepts/evidence.md" >}}) explains why the pipeline has this shape.

## The envelope


Every event carries the six CloudEvents fields `specversion` (always `1.0`), `id`, `type`, `source`, `time` and `data`, all required. The producer mints a unique `id`, delivery is at least once, and a consumer dedupes by that id. The `source` says which producer wrote the record and nothing more: `strazad` on a server-side decision, `strazad/` followed by the instance id on a control or admin event, `strazad-sentinel` on a sentinel verdict, and `straza` on every event a client uploaded, stamped by the server at ingest whatever the client sent. Consumers route on `type`, never on `source`, and they ignore fields they do not know, because payloads only ever grow. `time` is RFC 3339 in UTC.

## Three streams and one channel that is not a stream


The events ride JetStream streams inside the server's event bus. In either profile the bus runs in-process with no socket until it is pointed at an external NATS server, which several replicas share. `STRAZA_AUDIT` carries every `straza.audit.` type and is the stream the chain writer drains, bounded by age and size so a full disk can never stall a publish, with the chain and the outbox holding the durable copies. `STRAZA_EVENTS` carries the `straza.policy.`, `straza.revocation.`, `straza.apps.` and `straza.identity.` types, the control events that converge every server instance and feed the identity change feed, and none of them enters the chain. A third stream, `STRAZA_DEADLETTER`, holds the deliveries a sink refused, on `straza.deadletter.` followed by the sink name. It is not a spine subject, so a sink that filters `straza.>` cannot consume its own parked events, and it refuses new parks when full rather than shedding old ones. The client push channel is none of these. The `straza.push.` subjects and the approval resolution broadcast `straza.approval.resolved.` are core NATS messages rather than CloudEvents, delivered at most once, and a daemon that misses one falls back to its poll.

## Event types


Every type below has at least one emit site in the server or the client. The stream column says where the type rides, and only the audit stream is chained.

| Type | Fires when | Actor in the payload | Stream |
|---|---|---|---|
| `straza.audit.tool` | a hook decides a local tool call, or `/v1/decide` does | `user`, the session's subject, with `session` and `harness` | audit |
| `straza.audit.mcp` | the gateway decides a `tools/call`, serves a `tools/list`, or lists or reads the views of one MCP server | `user` and `session` of the calling agent | audit |
| `straza.audit.admin` | an admin object changes over the admin API or over SCIM, a role assignment that starts or ends included, a user is created or changed, an approver enroll token is minted, an approver device enrolls or goes with its deleted user, an agent's key is set or removed, or a config draft is created, checked, published, discarded or expires, or a server it proposes is contacted | `actor`, `actorId` and `actorVia` (login, session, api-token, or enroll-token on an approver enrolment, whose actor is the person whose enroll token was consumed), absent when no principal acted, as on a `draft.check`, which strazad writes for its own check | audit |
| `straza.audit.authn` | a login succeeds or fails, a session ends, a Slack tap by a disabled or locked person is refused, or the phone lane refuses a person who is disabled, locked or no longer a person | `user` and `userId` when known, never fabricated | audit |
| `straza.audit.identity` | the kill switch fires, is lifted, or an identity manager lift is refused by a lock | `origin`, one of scim, admin or external, plus the affected `user` | audit |
| `straza.audit.prompt` | a recorded session submits a prompt | `user` and `session`, with `agentType` when a delegate wrote it | audit |
| `straza.audit.reply` | a recorded session's model finishes a reply | the same as the prompt | audit |
| `straza.audit.sentinel` | the audit sentinel judges a session | none, the server's sentinel is the author | audit |
| `straza.audit.approval` | an approval is requested or resolved, or a call uses its approval, a ticket's grant or a hold's one run | `user` at request, `decidedBy` and `channel` at resolution | audit |
| `straza.policy.updated` | a snapshot activates | none, the activating admin is on the matching admin event | events |
| `straza.revocation.user` | a user is disabled, deactivated or locked | `origin` names the authority | events |
| `straza.revocation.session` | one session is revoked | none | events |
| `straza.revocation.sessions` | a bulk stand-down revokes a set, at most 1000 ids per event | none | events |
| `straza.revocation.device` | a device enrollment is revoked | none | events |
| `straza.revocation.lift` | a user is fully reactivated and no revocation lane remains | `origin` | events |
| `straza.apps.deployed` | an MCP server is installed or updated and serving | none, the server manager | events |
| `straza.apps.removed` | an MCP server is removed or stopped | none | events |
| `straza.apps.drift` | an MCP server's upstream tool inventory changed against the cache | none | events |
| `straza.apps.updated` | an access row, an MCP server secret or a per-user grant changed, or a draft was published | none | events |
| `straza.identity.created` | a user is created over SCIM or through the admin API | none | events |
| `straza.identity.updated` | an identity fact changes: enroll, session start, role edits, device, key or approver changes | `action` names the change where the emitter sets one, such as `enroll`, `session.start`, `renew`, `oauth.connect`, `nhi-key.set` or `approver-enroll`. `approver-device-revoked` carries `device`, `user`, `via` `admin` or `self`, and `reason` when a user delete revoked the device. Role edits, user creates and the SCIM lifecycle carry only the user's `id` | events |
| `straza.identity.deactivated` | a user is deactivated over SCIM or deleted by an admin | none | events |


A sink with no `subjects` filter receives `straza.audit.tool`, `straza.audit.mcp`, `straza.audit.admin`, `straza.audit.authn`, `straza.audit.identity`, `straza.audit.approval` and `straza.audit.sentinel`, plus every type under `straza.policy.`, `straza.revocation.`, `straza.apps.` and `straza.identity.`, and the boot log names the two it leaves out. Recorded conversation content leaves the box only when a sink names `straza.audit.prompt` and `straza.audit.reply`, or `straza.>`, in its own filter.

## Payload fields


The first column of fields is present on every record of the type from the current revision on, and the second appears when the event carried it. Field names are the JSON keys inside `data`.

| Type | Always present | Present when it applies |
|---|---|---|
| `straza.audit.tool` | session, user, harness, event, tool, command, effect, ruleId, setName, reason, snapshot | toolName, paths, workspace, agentType, agentId |
| `straza.audit.mcp` | session, user, harness, event, tool, app, toolName, effect, ruleId, setName, reason, snapshot, granted, default | bindingId and role (the access row that admitted the call), credentialId (the credential row the upstream call used), credentialSource (own, sponsor, shared or client_credentials), credentialOwner (the user id of the person whose connection an agent used), arguments (the verbatim call arguments as JSON text, cut at 8 KiB), argumentsTruncated, argumentsDigest and argumentsSize (the hex sha256 and the length in bytes of the arguments, written in place of arguments on a call of `straza__draft_submit`), count (the tools or views served, on a tools.list or resources.list record), uri (the view's address, on a resources.read record) |
| `straza.audit.admin` | action, target | actor, actorId, actorVia, draft (the draft a publish came from, on every record that publish writes), for `roles.assign` and `roles.unassign` the user, role, roleId, origin and reason, for `apps.grant.remove` and `nhi-key.removed` the user and reason, and the user, username and other fields of the identity setup records below |
| `straza.audit.authn` | action (login or session.end), outcome | via, user, userId, session, harness, reason, sourceIp, userAgent, approvalId (a refused Slack tap or phone decision) |
| `straza.audit.identity` | action (user.killed, user.reactivated or user.lift.blocked), user, origin | reason, sessionsRevoked, heldBy |
| `straza.audit.prompt` and `straza.audit.reply` | session, user, harness, content, mode, truncated, contentHash | agentType, agentId |
| `straza.audit.sentinel` | session, detector, severity, reason, evidence | user, window |
| `straza.audit.approval` | phase, approvalId, session, user, rule, set, lane, summary, state | justification, decidedBy, channel (console, slack, phone or browser), decidedReason, decidedDeviceId, consumedBy, consumedAt |
| `straza.policy.updated` | snapshot, sets | draft, when a publish wrote the event |
| `straza.revocation.user` | user, reason, origin | none |
| `straza.revocation.session` | session | none |
| `straza.revocation.sessions` | sessions | reason |
| `straza.revocation.device` | device | none |
| `straza.revocation.lift` | user, origin | none |
| `straza.apps.deployed` | app, name, version, runtime, source | none |
| `straza.apps.removed` | app, name | draft, when a publish wrote the event |
| `straza.apps.drift` | app, name, added, removed | none |
| `straza.apps.updated` | change (binding, secret, grant or publish) | app, draft on every event a publish writes, and on the publish event itself snapshot and apps, the servers the publish created, changed or removed |
| `straza.identity.created`, `straza.identity.updated` and `straza.identity.deactivated` | the user id, as `id` or `user` depending on the emitter | `action` where the emitter sets one, the session, device, harness or attestation it touched, and draft on a `straza.identity.updated` that a publish wrote |


An admin write under `/v1/admin/`, other than the drafts routes and the approve and deny routes, runs to its end once strazad has authorized it, also when the client disconnects, so a disconnect no longer separates the change from its `straza.audit.admin` record. A write that runs past five minutes answers 503 with a sentence that says the change may or may not have been applied, and until the row and the record commit in one transaction it can keep the change without its record. A SCIM write runs the same way: it runs to its end when the identity manager disconnects, and a write that runs past five minutes answers `503` in the SCIM error shape. The `straza.audit.authn` and `straza.audit.identity` records and the `straza.identity.*` events are written also when the client disconnects, and each write gives up after five seconds on a store that does not answer. Each identity setup write that succeeds chains one `straza.audit.admin` record, also when the client disconnects after the write. A refused write, an enrolment refused before its device is stored and a PATCH, PUT or SCIM DELETE that changes nothing chain none. On every one of these records `user` is the user's id, as on every earlier admin record that carries it. No record carries a password, a password hash, an enroll token or a key.

| Action | When | Fields beside `target` |
|---|---|---|
| `user.create` | a user is created through the admin API, or over SCIM, where a create that revives a deactivated row counts as a create | `user`, the new user's id, `username`, `kind` (human or nhi), `userType` when set, and `origin`, admin or scim, the lane that created it |
| `user.update` | an admin API PATCH, or a SCIM PUT, PATCH or DELETE, changes at least one field of a user. A status change sits beside its `user.killed`, `user.reactivated` or `user.lift.blocked` record, before it on the admin API and after it over SCIM, so pair the two by `user` | `user`, `username`, and `changed`, the names of the fields that write changed, never their values; `username` and `external_id` occur only on SCIM writes |
| `enroll-token.create` | an approver enroll token is minted for a user, on the admin route or the self route | `user`, `username`, and on the self route `channel` and `self: true` |
| `approver.enroll` | an approver phone or browser enrolls with its enroll token; the actor is that person, with actorVia enroll-token | `user`, `username`, `device`, `platform` and `name` |
| `nhi-key.set` | an AI agent's assertion key is registered or replaced | `user`, `username`, and `fingerprint`, sha256 and the hex digest of the public key |
| `nhi-key.removed` | an AI agent's key is removed by hand or with its user | `user`, and `reason` on a user delete |
| `approver.revoke` | an admin deletes a person who holds approver phones or browsers, one record per device | `user`, `username`, `device`, and `reason`, user deleted by admin |


Config drafts write `straza.audit.admin` records whose `action` starts with `draft.`, and each names its draft in `draft`. A note, a typed text and a document's text never enter a record.

| Action | When | Fields beside `draft` |
|---|---|---|
| `draft.create`, `draft.update` | a revision is stored | `revision`, `door` (the way the draft came in: `console`, `strazactl`, `apps-directory`, `straza-app` or `api`), `items` as kind, name and op, and `digest`, the hex SHA-256 of the revision's items. When they apply: `source` and `sourceHash`, the apps directory file and the SHA-256 of its bytes, `reverts` on a draft that undoes another, `rebase: true` on a revision made by Check again, and `client`, `sponsor` and `sponsorId` on a draft from an agent's drafting tools |
| `draft.check` | strazad stamps a check on a revision, with no actor | `revision`, `snapshot`, and the finding codes in `refused` and `risks` |
| `draft.contact` | a person asks Straza to contact a server that a draft proposes, to read its tools | `app`, `host`, `outcome` (answered, refused or failed) and `tools`, the number of tools the server listed |
| `draft.publish` | a person publishes, as the actor | `revision`, `items`, the resulting `snapshot`, `riskDigest` and `reviewedDigest`, the keys of the risks acknowledged in `acknowledged` and `typed`, the `proposer` with `proposerId` and `proposerVia`, the `client` the publisher used, and `reverts` on an undo |
| `draft.discard` | a draft is discarded | `revision`, `reason` when one was given, `closedBy` when another draft's publish closed a set's saved edit, and `unlinked`, the server a discarded removal from the apps directory left in place |
| `draft.expire` | an open draft passes its expiry, with no actor | `revision` |

Each composition edge a change adds or removes is one `roles.implication.create` or `roles.implication.delete` with `target`, `role`, `implies` and `impliesId`. Every record a publish writes carries `draft`, and so do the records of a direct admin write, which publishes a one-item draft in the same request and writes no `draft.*` record for it.


The policy routes differ. A `PUT` of a live set's text records `draft.create` or `draft.update` for the set's saved edit, and no `policy.update`. A `PUT` of the published text records `draft.discard` of the saved edit with the reason `the saved text equals the published text`. An activate that publishes a saved edit records `draft.publish` beside `policy.update` and `policy.activate`, so a rule keyed on `policy.update` does not fire on a saved edit.


`ruleId` and `setName` name the rule and the policy set that decided, exactly as the engine reported them, and both are the empty string when no rule matched and a profile default decided. The gateway's own refusals before any decision, an unknown tool or a throttled call, carry no rule either. When a rule allowed a call and a person then denied it, the hold ran out of time, or no credential could be resolved for it, the record is a deny with the allowing rule, its set and the refusal sentence as the reason, on the gateway record and on the hook lane's tool record alike. A record written under an older revision may lack a field that is always present today, and a consumer never reconstructs a missing name from the policies of the day it reads the record. `sourceIp` on an authentication event is the transport peer and never a forwarded-for header, and it is absent when no client connection produced the event.

## The chain record


A chain record has four fields. `seq` is the monotonic sequence number, `ce` is the exact event JSON as published, `prevHash` is the previous record's hash, and `hash` is the hex SHA-256 over `prevHash`, a newline and `ce`. The first record's `prevHash` is the empty string. The prompt and reply events are the one exception to "as published": before chaining, the writer removes `data.content` and adds `data.contentBytes` beside the client's `contentHash`, so the chain witnesses a conversation by hash and size while the text lives only in the retention-bounded transcripts store. Batches of 64 are the writer's unit: it appends each batch in one transaction with duplicate ids filtered inside it, and leaves linearity to a store-level lock, so several server instances may append at once. When the admin API serves a record it adds `username`, resolved from `data.user` at read time, and that field sits outside the hashed bytes so enrichment can never break a record.

## Verification


A verifier walks the records in `seq` order and reports the first record whose `prevHash` differs from its predecessor's `hash`, or whose `hash` does not recompute from its own `prevHash` and `ce`, and a chain with no such record is intact. `strazactl audit verify` runs that walk over the whole chain and prints `audit chain intact: 1234 records verified`, or fails with `audit chain BROKEN at seq 812 (1234 records checked)`, where the numbers are the counts of your chain. The console's Audit screen runs the same walk in the browser over the records it has loaded, and names the sequence number of the first record that fails. The chain proves that no stored record was edited or removed in place. It proves nothing against an attacker who rewrites every later record too, which is why the copy a sink forwards off the box is part of the evidence, as [Evidence]({{< relref "concepts/evidence.md" >}}) explains.
