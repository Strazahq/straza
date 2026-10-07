# Evidence from the audit chain

Use this reference when a task asks what Straza recorded, whether the record can be trusted, or what to hand an auditor. Start from records. Prose in a guide is never evidence.

## What the audit record is

Every decision, admin change, login and identity event becomes one CloudEvents 1.0 JSON object. The object carries `specversion`, `id`, `type`, `source`, `time` and `data`. The `type` is also the subject a consumer filters on. Every type under `straza.audit.` is appended to a hash chain after publication, never in the decision's path. A chain record has four fields: `seq`, `ce`, `prevHash` and `hash`. `hash` is the hex SHA-256 over `prevHash`, a newline and the exact `ce` bytes, and the first record's `prevHash` is the empty string. A duplicate event id never creates a second record.

Two things are not evidence. The server log is an operating record that you may rotate or lose, and deleting it loses nothing an auditor would ask for. The control events under `straza.policy.`, `straza.revocation.`, `straza.apps.` and `straza.identity.` converge the servers and feed the change feed, and none of them enters the chain. The chained fact behind a revocation is the `straza.audit.identity` record. The chain proves that no stored record was edited or removed in place. It does not defeat an attacker who owns the database and rewrites every later record too, which is why an off-box copy through a sink is part of the evidence. Background: https://docs.straza.ai/concepts/evidence/

## Event types and their data fields

Field names are the JSON keys inside `data`. A record written under an older revision may lack a field that is always present today. Never reconstruct a missing `setName` or `ruleId` from the policies of the day you read the record. A decision record carries the fields in this table and not the whole event it judged: it has no `argv`, no interpreter tag and no attestation, so a replay of a record through `strazactl policy simulate --event-json` rebuilds the call only as far as those fields go. Simulate detects the interpreter again from the record's command, so a rule that matches on the interpreter matches in a replay as it did live, unless the live event's argv named another program. Full table: https://docs.straza.ai/reference/events/

| Type | Fires when | Always present | Present when it applies |
|---|---|---|---|
| `straza.audit.tool` | a hook or `/v1/decide` decides a local tool call | `session`, `user`, `harness`, `event`, `tool`, `command`, `effect`, `ruleId`, `setName`, `reason`, `snapshot` | `toolName`, `paths`, `workspace`, `agentType`, `agentId` |
| `straza.audit.mcp` | the gateway decides or refuses a `tools/call`, or serves a `tools/list` | `session`, `user`, `harness`, `event`, `tool`, `app`, `toolName`, `effect`, `ruleId`, `setName`, `reason`, `snapshot`, `granted`, `default` | `arguments` (JSON text, cut at 8 KiB), `argumentsTruncated`, `bindingId`, `role`, `credentialId`, `count` (on a `tools.list` record) |
| `straza.audit.admin` | an admin object changes over the admin API or over SCIM, or a draft is created, checked, published or discarded | `action`, `target` | `actor`, `actorId`, `actorVia` (`login`, `session`, `api-token`, or `enroll-token` on `approver.enroll`); per action: `roles.assign` and `roles.unassign` carry `user`, `role`, `roleId`, `origin`, `reason`; `apps.install` carries `app`, `runtime`, `update`, and on an update `changed`, the manifest field paths it touched and never their values; `apps.remove` carries `app`, `roles`; `apps.disable` and `apps.enable` carry `app`, `status`, `adminRole`; `apps.binding.create` carries `app`, `role`, `tools`; `apps.binding.delete` carries `app`, `role`, `tools`, `bindingId`; `apps.secret.set` carries `app`, `role`, `scope` and never the value; `apps.secret.remove` carries `app`, `role`; `roles.create` and `roles.delete` carry `role` and, for a server-owned role, `server`; `roles.update` carries `role`; `policy.create` and `policy.update` carry `name`, `id`, `priority`; `policy.delete` carries `name`, `id`; `policy.activate` and `policy.deactivate` carry `name`, `id` and the resulting `snapshot`; `apps.grant.remove` carries `app`, `user`, `credentialId`, `reason`; `api-token.create` carries `name`, `id`, `scope`; `api-token.revoke` carries `id`; rev 37: `draft.create` and `draft.update` carry `draft`, `revision`, `door`, `items`, `digest`, and `reverts` on a draft that undoes another; `draft.check` carries `draft`, `revision`, `snapshot`, `refused`, `risks` and no actor; `draft.publish` carries `draft`, `revision`, `items`, `snapshot`, `riskDigest`, `reviewedDigest`, `acknowledged`, `typed` (risk keys, never the typed text), `proposer`, `proposerId`, `proposerVia`, `client`, and `reverts` on an undo; `draft.discard` carries `draft`, `revision`, and `reason` and `closedBy` when they apply; `roles.implication.create` and `roles.implication.delete` carry `role`, `implies`, `impliesId`; every record a publish writes, a direct admin write's included, carries `draft`; a `PUT` of a live set's text records `draft.create` or `draft.update` for its saved edit and no `policy.update`, a `PUT` of the published text records `draft.discard` of the saved edit with the reason `the saved text equals the published text`, and an activate that publishes a saved edit records `draft.publish` beside `policy.update` and `policy.activate`; rev 40, where `user` is the user id and `username` the username: `user.create` carries `user`, `username`, `kind`, `userType` when set and `origin`, `admin` or `scim`; `user.update` carries `user`, `username` and `changed`, the field names an admin PATCH or a SCIM write changed and never their values; `enroll-token.create` carries `user`, `username`, and `channel` and `self` on the self route, never the token; `approver.enroll` carries `user`, `username`, `device`, `platform` and `name`, with the person whose enroll token was consumed as the actor; `nhi-key.set` carries `user`, `username` and `fingerprint`, never the key; `nhi-key.removed` carries `user` and, on a user delete, `reason`; rev 41: `approver.revoke` carries `user`, `username`, `device` and `reason`, one record per approver device a user delete removes |
| `straza.audit.authn` | a login succeeds or fails, or a session ends | `action` (`login` or `session.end`), `outcome` | `via` (`id-token`, `session-token`, `device-token`, `password`, `client-assertion`, `slack`, `approver-token`), `user`, `userId`, `session`, `harness`, `reason`, `sourceIp`, `userAgent`, `approvalId` on a refused Slack tap or phone decision |
| `straza.audit.identity` | the kill switch fires, is lifted, or a lift from the identity manager is refused by a lock | `action` (`user.killed`, `user.reactivated`, `user.lift.blocked`), `user`, `origin` | `reason`, `sessionsRevoked`, `heldBy` |
| `straza.audit.approval` | a held call is requested, resolved, or its ticket is consumed | `phase` (`request`, `resolution`, `consumed`), `approvalId`, `session`, `user`, `rule`, `set`, `lane` (`hook` or `gateway`), `summary`, `state` (`pending`, `approved`, `denied`, `expired`) | `justification` (request phase, gateway lane, model-authored), `decidedBy`, `channel` (`console`, `slack`, `phone`, `browser`), `decidedReason`, `decidedDeviceId`, `consumedBy`, `consumedAt` |
| `straza.audit.sentinel` | the audit sentinel judges a session | `session`, `detector`, `severity`, `reason`, `evidence` | `user`, `window` |
| `straza.audit.prompt` and `straza.audit.reply` | a recorded session submits a prompt, or its model finishes a reply | `session`, `user`, `harness`, `content`, `mode`, `truncated`, `contentHash` | `agentType`, `agentId` |

Outcome values: `effect` is `allow` or `deny`, `outcome` on a login is `success` or `failure`, and `outcome` on a session end is `revoked-self`, `revoked-admin`, `idle-closed` or `lifetime-closed`. `ruleId` and `setName` are the empty string when no rule matched and a profile default decided. `sourceIp` is the transport peer, never a forwarded header, and it is absent when no client connection produced the event.

The two recording types are the exception in two ways. Before chaining, the writer removes `data.content` and adds `data.contentBytes` beside `contentHash`, so the chain witnesses a conversation by hash and size while the text lives only in the retention-bounded transcripts store. A sink with no `subjects` filter receives every other audit type and every control type, and leaves the two recording types out, so adding a sink never ships conversation content off the box by accident. The boot log names the two it leaves out.

## Reading records

`strazactl audit tail` prints the newest records, one per line, as `#<seq> [<username>] <event json>`. Its flags are `--limit` (the server's window, default 50, at most 1000), `--user` (an id or a username, applied before the window), `--type` (one CloudEvent type such as `straza.audit.mcp`, applied inside the window) and `--app` (the server's text search for the name before the window, then an exact match on `data.app`). Because `--type` filters inside the window, `--type straza.audit.approval --limit 50` shows the approval records among the newest 50, not the newest 50 approval records. A `--limit` above 1000 is not refused: the server silently answers with its default of 100. Reference: https://docs.straza.ai/reference/cli/strazactl/strazactl_audit_tail/

A person without admin rights cannot read the audit API, but their own machine keeps a decision journal that is always on. `straza trace show -n 20` prints its last 20 records, oldest first. A hook decision is one line with `effect`, `snapshot` and `escalation`, plus `rule` and `set` when a rule decided and `default` or `fail_closed` when either applies, and a call through the local MCP proxy is one line with the `tool`, the `outcome` the gateway answered (`ok`, `denied` or `error`), the HTTP `status` and the `correlation` id. The journal never holds the command, the paths, the arguments or the reason text. The `rule` and `set` of a hook deny are enough for an administrator to open the policy with `strazactl policy show <set>`, and for a proxied call the `tool` and the time let an administrator find the gateway record with `strazactl audit tail --user <username> --type straza.audit.mcp`. The reason itself is the sentence the agent received with the deny. Reference: https://docs.straza.ai/reference/cli/straza/straza_trace_show/

`strazactl audit verify` walks the whole chain in `seq` order, 1000 records per request, and prints `audit chain intact: 1234 records verified`, or fails with `audit chain BROKEN at seq 812 (1234 records checked)`, where the numbers are the counts of your chain. The console's audit screen runs the same check in the browser. Run verify before and after building an evidence pack, and quote the line.

`GET /v1/admin/audit` is the route both commands use. It returns records after a sequence number: `after=<seq>` is the cursor and `limit` is the page, 100 by default and at most 1000, and a value above 1000 falls back to 100 without an error. `order=desc` returns the newest window instead and refuses to combine with `after`. Each item is the chain record `seq`, `ce`, `prevHash` and `hash`, plus a `username` resolved at read time, which is absent when the record names no user that resolves, and an approval record can also carry `decidedByUsername`. `ce` is the event JSON as text, and the enrichment sits outside the hashed bytes, so it can never break a record. `user`, `q` (a case-insensitive substring of the raw event) and `effect` (`allow` or `deny`) filter before the page, so the cursor walks matches. `type` (a type prefix) and `session` filter inside the page. Route table: https://docs.straza.ai/reference/api/

Walking the chain without a SIEM. `strazactl audit tail` shows only the newest window and `audit verify` prints a verdict, not the records, so a full export pages the API itself. The person mints a token with `strazactl api-token create --name <token name> --scope audit:read --ttl 24h`, keeps its value in an environment variable they set in their own shell, and never pastes it into the chat. Then loop on the last `seq` of each page until a page comes back empty:

```sh
after=0
while :; do
  page=$(curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" "<strazad url>/v1/admin/audit?after=$after&limit=1000")
  [ "$(printf '%s' "$page" | jq 'length')" -eq 0 ] && break
  printf '%s' "$page" | jq -c '.[]' >> audit-export.jsonl
  after=$(printf '%s' "$page" | jq '.[-1].seq')
done
```

Run `strazactl audit verify` before and after the export and quote both lines, and revoke the token with `strazactl api-token revoke <token id>` when the pack is built.

## Sinks and SIEM

A sink is a durable consumer that forwards the stream to a system you run. `type: webhook` posts each event as one HTTP request, and `type: file` with a `path` appends one event per line and syncs before it acknowledges. A sink's `name` becomes the consumer name, so renaming replays the stream from the start of retention, which is also how you backfill a new receiver. `subjects` takes exact types or `.>` prefixes, `secret` or `secretFile` adds an HMAC-SHA256 signature, `headers` adds request headers, and `batch: 64` switches to one request or one fsync per batch. Writing `["straza.>"]` or `["straza.audit.>"]` in an explicit filter includes recorded conversation, which is the point of writing it. Guide: https://docs.straza.ai/guides/audit/sinks-and-siem/

A webhook carries `X-Straza-Subject`, `X-Straza-Sink` and, with a secret, `X-Straza-Signature: sha256=<hex>`. A batch carries `X-Straza-Batch` and omits the subject header. Delivery has four outcomes. A 2xx or a 409 acknowledges. A 5xx, a 408, 425 or 429, a network error or a timeout redelivers with backoff for up to 4320 attempts. Any other 4xx gets three attempts. After the budget the event is parked in a dead-letter stream, counted in `straza_sink_deadletter_total`, and acknowledged at the source so nothing behind it stalls. A redirect is followed only within the scheme, host and port of the sink's url and only as a 307 or 308, and any other redirect is redelivered like a network error.

The documented Elasticsearch setup posts straight into an index, with an ingest pipeline that makes the CloudEvent `id` the document `_id` so a redelivery answers 409:

```yaml
sinks:
  - name: elastic
    type: webhook
    url: http://elasticsearch:9200/straza-events/_doc?pipeline=straza-id
    subjects: ["straza.audit.>", "straza.revocation.>"]
    headers:
      Content-Type: application/json
      Authorization: "Basic ZWxhc3RpYzpzdHJhemFzaWVt"
```

```text
PUT _ingest/pipeline/straza-id
{"processors":[{"script":{"source":"ctx._id = ctx.id"}}]}
```

For Splunk the documented paths are a Universal Forwarder on the file sink with `sourcetype = _json`, or a thin translator in front of HEC that verifies `X-Straza-Signature` and wraps each event as `{"event": <ce>, "sourcetype": "straza:ce"}`.

`strazactl sinks list` shows NAME, TYPE, TARGET, BACKLOG, PARKED, DELIVERED and LAST ERROR, the newest failure with its time in UTC, since boot, and a parked row carries `(replay needed)`. `strazactl sinks replay <name>` re-posts parked events oldest first, bounded by `--limit` (default 1000), stops at the first refusal, and answers `replayed 0 event(s) to sink elastic; 0 remain parked` when the lane is empty. A replay changes Straza, so the person runs it in their own terminal, as the SKILL.md section Reads, checks and changes says. The same views are `GET /v1/admin/sinks` and `POST /v1/admin/sinks/{name}/replay`, and the counters `straza_sink_replayed_total` and `straza_sink_duplicates_total` complete the picture.

## Query recipes

Each Elasticsearch document is the CloudEvent itself: top-level `type`, `time`, `id`, `source`, and the type's fields under `data`. The recipes assume the index `straza-events` with `time` as the time field, which is what the documented sink and the Kibana data view use, and the dynamic mapping in which string fields carry keyword subfields. `data.user` is a user id, not a username, so resolve the name first with `strazactl users list` or read `username` from the admin API. Splunk recipes assume `index=straza sourcetype="straza:ce"` with the same dotted field names, which the `_json` file monitor also yields.

Denies for one user in a window (ES|QL, then SPL):

```text
FROM straza-events
| WHERE type IN ("straza.audit.tool", "straza.audit.mcp") AND data.effect == "deny"
  AND data.user == "<user id>" AND time >= "2026-09-01T00:00:00Z" AND time < "2026-09-08T00:00:00Z"
| KEEP time, type, data.tool, data.command, data.app, data.toolName, data.ruleId, data.setName, data.reason
| SORT time
```

```text
index=straza sourcetype="straza:ce" (type="straza.audit.tool" OR type="straza.audit.mcp") data.effect=deny data.user="<user id>" earliest="09/01/2026:00:00:00" latest="09/08/2026:00:00:00"
| table _time type data.tool data.command data.app data.toolName data.ruleId data.setName data.reason
```

Every approval with its decider:

```text
FROM straza-events
| WHERE type == "straza.audit.approval" AND data.phase == "resolution"
| KEEP time, data.approvalId, data.state, data.decidedBy, data.channel, data.decidedReason, data.user, data.rule, data.set, data.lane, data.summary
| SORT time DESC
```

```text
index=straza sourcetype="straza:ce" type="straza.audit.approval" data.phase=resolution
| table _time data.approvalId data.state data.decidedBy data.channel data.decidedReason data.user data.rule data.set data.lane data.summary
```

Every MCP call to one server (a `tools.list` record has an empty `app`, so it drops out):

```text
FROM straza-events
| WHERE type == "straza.audit.mcp" AND data.app == "<app>"
| KEEP time, data.user, data.session, data.toolName, data.effect, data.ruleId, data.setName, data.granted, data.default, data.role, data.credentialId
| SORT time
```

```text
index=straza sourcetype="straza:ce" type="straza.audit.mcp" data.app="<app>"
| table _time data.user data.session data.toolName data.effect data.ruleId data.setName data.granted data.default data.role data.credentialId
```

Revocations, chained and control:

```text
FROM straza-events
| WHERE type == "straza.audit.identity" OR type LIKE "straza.revocation.*"
| KEEP time, type, data.action, data.user, data.origin, data.reason, data.sessionsRevoked, data.session, data.sessions, data.device
| SORT time DESC
```

```text
index=straza sourcetype="straza:ce" (type="straza.audit.identity" OR type="straza.revocation.*")
| table _time type data.action data.user data.origin data.reason data.sessionsRevoked data.session data.sessions data.device
```

Chain gaps. The sink copy carries no `seq` or `hash`, so the chain's own integrity comes from `strazactl audit verify`. Two checks find a gap in the off-box copy. First, take the `id` of every `ce` on one admin API page and ask the receiver for those ids; an id the receiver lacks is a delivery gap to replay or explain. Second, count records per hour and look for a silent window while sessions were active.

```text
FROM straza-events
| WHERE id IN ("<id 1>", "<id 2>", "<id 3>")
| KEEP id, type, time
```

```text
FROM straza-events
| WHERE type LIKE "straza.audit.*"
| STATS records = COUNT(*) BY hour = DATE_TRUNC(1 hour, time)
| SORT hour
```

```text
index=straza sourcetype="straza:ce" id IN ("<id 1>", "<id 2>", "<id 3>") | table id type _time
index=straza sourcetype="straza:ce" type="straza.audit.*" | timechart span=1h count
```

Sessions per role. Only a gateway record names the role that admitted the call, so this counts the sessions that exercised each role through the gateway. Hook-lane records carry no role.

```text
FROM straza-events
| WHERE type == "straza.audit.mcp" AND data.role IS NOT NULL
| STATS sessions = COUNT_DISTINCT(data.session), users = COUNT_DISTINCT(data.user), calls = COUNT(*) BY data.role
| SORT sessions DESC
```

```text
index=straza sourcetype="straza:ce" type="straza.audit.mcp" data.role=*
| stats dc(data.session) AS sessions dc(data.user) AS users count AS calls BY data.role
```

Failed logins by source address:

```text
FROM straza-events
| WHERE type == "straza.audit.authn" AND data.action == "login" AND data.outcome == "failure"
| STATS failures = COUNT(*) BY data.sourceIp, data.via, data.reason
| SORT failures DESC
```

```text
index=straza sourcetype="straza:ce" type="straza.audit.authn" data.action=login data.outcome=failure
| stats count AS failures BY data.sourceIp data.via data.reason | sort - failures
```

Admin changes by actor and credential:

```text
FROM straza-events
| WHERE type == "straza.audit.admin"
| KEEP time, data.actor, data.actorId, data.actorVia, data.action, data.target, data.user, data.role, data.origin
| SORT time DESC
```

```text
index=straza sourcetype="straza:ce" type="straza.audit.admin"
| table _time data.actor data.actorId data.actorVia data.action data.target data.user data.role data.origin
```

## Control mapping

The public mapping lives at https://docs.straza.ai/security/compliance/ and no product is compliant by itself, so a pack never claims otherwise. For each mapped control the row names the records that carry the evidence and one KQL filter for the `straza-events` data view, unless marked ES|QL. Where the evidence is configuration or build practice rather than a record, the row says so.

| Control | Records and fields | One query |
|---|---|---|
| NIS2 21(2)(a) policies | `straza.audit.tool` and `straza.audit.mcp`: `snapshot`, `setName`, `ruleId` name the policy state that governed | `data.snapshot:"<id>"` |
| NIS2 21(2)(b) incident handling | `straza.audit.identity` `user.killed` with `sessionsRevoked`; `straza.audit.authn` `session.end` with `revoked-admin` | `type:straza.audit.identity and data.action:user.killed` |
| NIS2 21(2)(c) continuity | configuration, not a record; the chain shows decisions continuing across a restart by `time` and `snapshot` | `type:straza.audit.tool` over the restart window |
| NIS2 21(2)(d) supply chain | `straza.audit.mcp`: `app`, `toolName`, `granted`, `credentialId`; `straza.audit.admin` `apps.install` and `apps.remove` | `type:straza.audit.admin and data.action:apps.install` |
| NIS2 21(2)(e) development | `straza.audit.tool` denies with an empty `ruleId`, the profile default | ES|QL `type == "straza.audit.tool" AND data.effect == "deny" AND data.ruleId == ""` |
| NIS2 21(2)(f) effectiveness | the decision records of a known test call, plus the verify line | `data.session:"<test session>"` |
| NIS2 21(2)(g) hygiene and training | no pack-specific type; the binding change rides `straza.audit.admin`, found by `target` and time | `type:straza.audit.admin and data.target:"<pack or role>"` |
| NIS2 21(2)(h) cryptography | configuration; the one record-borne fact is the AI agent lane `via` `client-assertion` | `type:straza.audit.authn and data.via:client-assertion` |
| NIS2 21(2)(i) HR and access | `straza.audit.admin` `roles.assign` and `roles.unassign`: `user`, `role`, `origin`, `reason`; `user.create` and `user.update` for the joiner and mover steps done on the admin API or over SCIM; `straza.audit.identity` | `type:straza.audit.admin and data.action:(roles.assign or roles.unassign or user.create or user.update) and data.user:"<id>"` |
| NIS2 21(2)(j) authentication | `straza.audit.authn` `login`: `via`, `outcome`, `sourceIp`, `userAgent` | `type:straza.audit.authn and data.action:login` |
| NIS2 Art. 23 reporting | every chained record with `time` and its principal, plus the sink delivery state | `type:straza.audit.*` in the incident window |
| A.5.3 segregation of duties | `straza.audit.admin` `draft.publish`: `actor` is the person who published and `proposer` the author of the first revision, and the `draft.create` and `draft.update` records of the same `draft` name every author. A direct admin write has no `draft.publish` and names one actor, except an activate that publishes a set's saved edit, whose `draft.publish` names the activator as actor and the person who saved the edit as proposer | `type:straza.audit.admin and data.action:draft.publish` |
| A.5.15 to A.5.18 access rights | as (i), plus `roles.unassign` with the reason `user deleted by admin` on a delete | `data.action:roles.unassign and data.reason:"user deleted by admin"` |
| A.5.16 identity management | `straza.audit.authn` `session.end`; `straza.revocation.device`; the `straza.identity.` feed | `type:straza.audit.authn and data.action:session.end` |
| A.5.17 authentication information | `straza.audit.admin` `api-token.create` (`name`, `id`, `scope`), `api-token.revoke` (`id`), `enroll-token.create`, `approver.enroll`, `nhi-key.set` (`fingerprint`) and `nhi-key.removed` | `type:straza.audit.admin and data.action:(api-token.create or api-token.revoke or enroll-token.create or approver.enroll or nhi-key.set or nhi-key.removed)` |
| A.5.19 to A.5.23 suppliers | `straza.audit.admin` `apps.*` actions; `straza.audit.mcp` `credentialId` | `type:straza.audit.admin and data.action:apps.*` |
| A.8.2 to A.8.5 privileged access | `straza.audit.mcp` `granted`, `default`, `role`, `bindingId`; `straza.audit.admin` `actorVia` | `type:straza.audit.mcp and data.effect:allow and data.default:true` |
| A.8.9 configuration management | `straza.audit.authn` login failures whose `reason` names an attestation refusal | `type:straza.audit.authn and data.outcome:failure` |
| A.8.15 and A.8.16 logging | the chain and the verify line; every `straza.audit.admin` record with its actor triple | `type:straza.audit.admin and not data.actor:*` (background writes only) |
| A.8.15 and A.8.16 change records | `straza.audit.admin` with `actor`, `actorVia` and the action: `policy.create`, `policy.update`, `policy.activate` and `policy.deactivate` with the resulting `snapshot`, `roles.create`, `roles.delete`, `roles.assign`, `apps.install`, `apps.binding.create`, `apps.binding.delete` and `apps.secret.set`. The `snapshot` on `policy.activate` is the one later decision records name, which ties who changed the policy to what it then decided | `type:straza.audit.admin and data.action:(policy.* or roles.* or apps.*)` |
| A.8.24 cryptography | as (h) | `type:straza.audit.authn and data.via:client-assertion` |
| A.8.28 secure coding | build practice, no record | none |
| A.8.32 change management | `draft.publish` with `revision`, `items`, `snapshot`, `reviewedDigest` and `acknowledged`, and every record with the same `draft`, the objects one publish changed in one transaction | `data.draft:"<draft id>"` |

## The evidence pack rule

Start from records, never from prose. Every claim in a pack names three things: the sequence range the claim rests on, the verify result that covered it, and the query that produces the records. A claim that cannot be regenerated from a query is a sentence, not evidence.

1. Run `strazactl audit verify` and quote its line, with the record count and the time you ran it.
2. State the sequence range: the `seq` of the first and last record the claim uses, from `strazactl audit tail` or the admin API.
3. Give the query, the index or sourcetype it assumes, and the record count it returned.
4. Quote the records, not a summary of them. Trim fields, never values.
5. Name the sink copy: `strazactl sinks list` with a zero parked count, or the replay line that emptied the lane.
6. For an approval, quote both phases: the `request` record and the `resolution` record with `decidedBy`, `channel` and `decidedReason` when present. `justification` is model-authored and never ground truth.
7. For a kill switch, quote the `straza.audit.identity` record and the `session.end` records it caused, never the log line.

## Gotchas

- Default sinks exclude `straza.audit.prompt` and `straza.audit.reply`. Recorded turns reach a receiver only when the filter names them or uses `straza.>` or `straza.audit.>`. In the chain those records carry `contentHash` and `contentBytes` and no content.
- Sink and bus URLs are redacted in the server log: the query string, where a credential may sit, is replaced by a marker. Confirm a credential at the receiver, never from the log.
- The demo stack's SIEM overlay runs an Elasticsearch that keeps no volume. A recreate starts empty and without the `straza-id` pipeline, the index then answers 400 to every event, and each parks after three attempts. Recreate the pipeline, then run `strazactl sinks replay elastic` until it reports `0 remain parked`.
- A held call on the hook lane leaves four records, not one: a `straza.audit.tool` deny whose `reason` carries the reference, a `straza.audit.approval` `request`, a `resolution`, and a `straza.audit.tool` allow for the retry whose `reason` names the approver. Count decisions, not records, and never read the first deny as a denial that stood. Walk: https://docs.straza.ai/guides/write-policy/hold-for-a-human/
- `data.user` is an id. `strazactl audit tail --user` accepts a username, but the sink copy carries the id, and the admin API's `username` lives outside the hashed bytes.
- A gateway call that a rule allowed and a later gate refused, a denied, expired or pending hold or no usable credential, is recorded as `effect: deny` with the allowing rule's `ruleId` and `setName` and the refusal sentence as `reason`. Read the `reason`, not the rule, to know why it was refused.
- `arguments` on a gateway record is cut at 8 KiB with `argumentsTruncated: true`. The record witnesses the cut, and the full text is not on the chain.
- Records written under older revisions lack fields added later, and `channel` on an old resolution may read `api`. Read the record's own truth.
- Kibana's time picker defaults to the last 15 minutes. A window that looks empty usually is not.
- The record trails the decision by the client drain and the server relay tick. A query run seconds after an action may miss it. Wait, query again, and quote the `time` on the record.
- The sentinel fails open with an alarm. A missing verdict proves nothing. A present verdict lists the triggering event ids in `evidence`, so a verifier can walk from the verdict to its chained evidence.
