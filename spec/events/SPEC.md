# Events, v1beta1 (revision 59)

Status: **beta**. The specification began as v1alpha1, revision 2 added
`straza.revocation.lift` and the push channel, and the move to beta
changed no wire format. Schema: `events.schema.json` (envelope). Examples:
`examples/valid-*.json`, `examples/invalid-*.json`.

Revision 59 (2026-11-05): a straza client reports the audit its spool discarded, and its tool
records keep a bounded part of a long command and of a long list of paths. Once the client has
lost records before they reached the reference producer, its next drain sends one loss record, a
`straza.audit.tool` record with `event` `audit.dropped`, as an upload of its own ahead of any
spooled record. The client counts three kinds of loss: a spooled record larger than one upload
can carry, the oldest unsent spool files it discards once an upload failed and the unsent
backlog passed the spool cap, and a decide or capture call that its background service refused
before it could record it, such as a call past its bound on calls in flight or a malformed
request. The record carries `droppedRecords`, the sum of `oversizeRecords`, `evictedRecords` and
`refusedCalls`, and `evictedFiles`, `dropEvents`, the count of drops it sums, `firstDrop` and
`lastDrop`, RFC 3339 times present when the client knew them, and `reason`, sentences that name
the counts and the times. It names no session of its own, so the reference producer binds it to
the uploading session and its user as it binds every client record. It carries no `harness`,
`tool`, `command`, `effect`, `ruleId`, `setName` or `snapshot`, so it is the second exception to
the minimum payload of `straza.audit.tool` in §2, after revision 53's `dataOmitted` form, and a
consumer that counts decisions skips `event: audit.dropped`. The client removes the ledger it
reported only once the reference producer accepted that upload, and a retry sends the same
record under the same CloudEvents `id`, so the chain holds it once. A drop noted while a report
is out waits for the next one. A client can forge a loss record only about its own uploads.

A straza client's `straza.audit.tool` record keeps at most 64 KiB of `command`, cut on a UTF-8
character boundary. When it cuts, it adds `commandTruncated: true` and `commandHash`, `sha256:`
followed by the hex SHA-256 of the whole command. It keeps `paths` in order while their bytes,
counting one byte past each path, fit in 64 KiB, cuts the path that crosses the bound on a
character boundary and drops the paths after it. When it cuts, it adds `pathsTruncated: true` and
`pathsHash`, `sha256:` followed by the hex SHA-256 over every path sent, each preceded by its
length in bytes as an unsigned 64-bit big-endian integer, so two different lists never hash
alike. The four fields are absent on a record that was not cut, and the reference producer's own
bound of revision 53 can cut such a record again. Additive: one new `event` value with eight
fields of its own, four optional fields on a tool record, and no change to the schema,
the client type allowlist or the default sink list. `valid-audit-tool-audit-dropped.json` pins a
loss record after an outage and refused calls, and `valid-audit-tool-command-truncated.json` a
tool record whose command and paths were cut (values synthetic, with the kept command and
paths shortened from 64 KiB so the file stays small).

Revision 58 (2026-11-05): the gateway withholds a reply that holds the credential it injected,
and several refusals that left no record now leave one. Every change is additive: one new
detector, one new `straza.audit.mcp` event, one new `straza.audit.admin` action, one new
`session.end` outcome, new reason values and new triggers of existing records, and no new field.

A `tools/call` reply on `/mcp` or on a server's endpoint `/mcp/{server}` that holds the
credential the reference producer injected for the call is withheld. The check reads every
string and number of the reply as the agent would receive it, the error text and every `_meta`
included. It matches the credential as stored and as its template renders it, whole, in base64
or in hex, and inside the text it decodes, up to four levels deep: percent escapes, form
encoding with a plus for a space, JSON escapes wherever they stand, and base64 runs, a run glued
to a word by a hyphen or an underscore included. A credential shorter than 8 bytes, and a fragment of one, never match. The call's
`tool.pre` allow record stays, because it entered the audit queue before the call ran. A second
`straza.audit.mcp` record follows under its own `id`: `event` `tool.post`, `effect` `deny`, the
allowing `ruleId` and `setName`, the call's other fields with its credential fields and
`arguments`, and as `reason` the sentence the agent reads, today `Straza: the reply of the tool
<tool> on the MCP server <server> was withheld, because it held the credential Straza injects
for that server. The same call will be withheld again. Tell the user.` One
`straza.audit.sentinel` verdict follows it, which the gateway raises itself, as §2.1 says. A
tool whose name, description, schema or `_meta` holds the credential is left out of the server's
tool inventory, and a view whose text holds it is not served. Neither writes an audit record,
and a tool that was listed before reads as drift on `straza.apps.drift`, where a tool whose name
holds the credential is named `(a tool whose name holds the credential)` in `removed[]`.

`straza.audit.mcp` gains the event `request`: one record per gateway request that the reference
producer refuses after its session token verified and before it reads the JSON-RPC message,
because the session, its device or its user is revoked (`ruleId` `revoked`) or because the
token's attestation ranks below `governance.minAttestation` (`ruleId` `attestation`). It is
written on `/mcp`, on `/mcp/{server}` and on the git grant routes `POST /v1/grants` and
`POST /v1/grants/resolve`, as `/v1/decide` already records the same refusals. The record is
`effect: deny` with the 403 sentence as `reason`, `app` the server a `/mcp/{server}` request
names, cut as revision 52 cuts a name the catalog did not resolve, and empty on the other routes,
empty `tool`, `toolName` and `setName`, and `granted` and `default` false. A request whose body
arrives too slowly is cut at its read deadline, is never read and writes no record. On `/mcp` the
record of a `tools/call` refused as an unknown tool whose name holds `__` now names the part
before the first `__` in `app` and the part after it in `toolName`, each cut on its own as
revision 52 says, as the record of a resolved call names them. Its `reason` still quotes the
whole name, cut, and a name without `__` keeps `app` empty.

Revision 53's `dataOmitted` form has two more causes. A record over 256 KiB of JSON that holds
more than 65,536 JSON values takes the form at once, without the string cut, even where the cut
would have made it fit. The count is an upper bound: one more than the commas, brackets and
braces outside strings. On `/v1/audit/batch` the records of one upload are decoded in full, in
order, while their values add up to at most 65,536, and a record that would pass that total takes
the form whatever its length, so a client record under 256 KiB can carry `dataOmitted`. Its n and
its hash describe the record as the client sent it, and the reference producer then binds it to
the uploader as it binds every client record. An upload of more than 1000 records is refused
with 413 and stores none of them, and the straza client keeps them. The per-upload cap on the
sessions one upload may name is gone, because 1000 records name at most 1000 sessions. An upload
refused because its session, its device or its user is revoked writes one `straza.audit.authn`
`login` failure with `via` `session-token`, `userId`, `session`, `harness`, `sourceIp`,
`userAgent` when sent, and the `reason` the refresh lane writes for the same state: `session is
no longer active` for a revoked session, and `device or user revoked` for a revoked device or
user.

An admin device revoke and the revoke of a reused key now end the device's active sessions in
the transaction that deletes the device. Each session a revoke ends writes one
`straza.audit.authn` `session.end` record with `userId`, `session` and `reason`, before the
revoke's own `device.revoke` or `device.key.reused` record. The records of an admin revoke carry
`outcome` `revoked-admin`, the reason `device revoked by admin`, and the `sourceIp` and
`userAgent` of the admin's request. The records of a reuse revoke carry the new `outcome`
`revoked-key-reuse`, because no admin acted, the reason `device key reused`, and the `sourceIp`
and `userAgent` of the check-in that used the copied key, as evidence of where the copy was used.
A consumer that matches `outcome` against a fixed list needs the new value. A revoke whose answer
was lost, which the reference producer then finds committed, still writes these records for the
sessions that were active before it, unless it could not read those sessions, and then it writes
no `session.end` record for them. `straza.audit.admin` gains
the action `user.delete`: one record per admin delete that removes a user, with the actor fields
of §2, `target` and `user` the user's id, `username`, and `reason` `user deleted by admin`. It is
written before the records of the grants, assignments, key and approver devices that end with
the user. A delete that answers 404, the refused delete of the break-glass account and a delete
the database refused write none, and a delete whose answer was lost, which the reference producer
then finds committed, writes one. A `login` failure with `via` `password` carries the new
`reason` `an AI agent or a service account cannot sign in with a password` when an AI agent or a
service account typed its right password on a built-in sign-in page. It names the account in
`user`, one record per submit, and no ID token is minted. A wrong password keeps the reason
`invalid username or password` for every account. A write that leaves a user an AI agent or a
service account clears its stored password, over the admin API and over a SCIM PUT or PATCH, and
its `user.update` lists `password` in `changed[]`, a value a SCIM write did not list before.

`straza.identity.updated` `connection.refused` gains triggers and reasons. On `/v1/connect` an AI
agent or a service account acts only for itself, whatever roles or sponsor link it holds, and
naming any other user answers 403. A person who is neither the named user's sponsor nor an
administrator is refused as before. Each such refused write records the `reason` `only a person
who is the user's sponsor or an administrator can change another user's connection`, and the
refusal of a person without standing, which wrote no record before, now writes one. An agent or
a service account that turns the opt-in for agents on for its own connection records `an agent or
a service account cannot allow agents`. On an oauth-kind server an admin API token that starts a
sign-in for the user it names records `an admin API token cannot start a sign-in`, and an agent
or a service account that starts its own records `an agent or a service account cannot start a
sign-in`, with `user` and `setBy` both the agent. With the two reasons of revision 54 the action
has six reasons today. A token's record carries `setBy`, the token's id, and `actorVia`
`api-token`, as revision 55 stamps a token's connection records. Revision 54's rule that only a
session a machine enrollment started meets the refusal no longer holds, and neither does revision
55's sentence that a token's refusal to start a provider sign-in writes no record. A refused read,
a token whose scope does not cover the request, a token that names no user and a session that
sends `user` on an oauth-kind sign-in still write no record.

No approval opens for a call whose hold a person denied while that hold's decision window, its
creation plus the rule's `timeoutSeconds`, is still open. The same user, session, rule and
arguments are refused on the hook lane and on the gateway, as a denied ticket already is, and no
`straza.audit.approval` `phase: request` record follows. The call's own record is a deny with the
allowing `ruleId` and `setName`, as revision 31 records a denied hold, and a reason that names
who denied it. The reference producer also bounds the new approvals that one session and one user
open, today 5 at once and then 5 a minute per session, and 10 at once and then 10 a minute per
user, on each replica. A call past either bound opens nothing and writes no approval record, and its own record is
a deny whose reason says so. A request whose client leaves during its insert keeps its
`phase: request` record.

The examples pin the new record shapes with synthetic values.
`valid-audit-mcp-reply-withheld.json` and `valid-audit-sentinel-credential-echo.json` pin a
withheld reply's deny record and its verdict, `valid-audit-mcp-request-refused.json` the refused
`/mcp` request of a revoked session, `valid-audit-mcp-unknown-tool-split.json` an unknown tool
named with its server, and `valid-audit-tool-data-omitted-budget.json` a client record past its
upload's value budget. `valid-audit-authn-session-end-device-revoke.json` and
`valid-audit-authn-session-end-key-reuse.json` pin the session ends of the two device revokes,
`valid-audit-admin-user-delete.json` a user delete, `valid-audit-authn-login-password-nonperson.json`
an agent's refused password, and `valid-identity-updated-connection-refused-standing.json` and
`valid-identity-updated-connection-refused-api-token.json` an agent that named a person and a
token that started a sign-in.

Revision 57 (2026-11-03): an admin device revoke leaves a record on the chain.
`DELETE /v1/admin/users/{id}/devices/{deviceId}` deletes the device row and writes its
`straza.revocation.device` event in one transaction, and that event is no `straza.audit.*`
subject, so the hash chain kept no record of who revoked the machine. `straza.audit.admin`
gains the action `device.revoke`, one record per admin revoke that deletes the device. The
record carries the actor fields of §2 for the credential that asked, `target` and `device`,
both the device id, `user`, the id of the user the device was enrolled for, and `reason`, today
`device revoked by admin`, and no `username`, because the route reads the device row only. A
revoke that answers 404, because the device is unknown, belongs to another user or was deleted
by another revoke first, and a revoke whose write the database refused, write no record. A
device that strazad revokes on its own, when a key check-in repeats its counter, keeps its one
`straza.audit.identity` record `device.key.reused` and writes no `device.revoke`. A call of a
built-in `straza` tool that the tool itself refuses before its record is written is now
`effect: deny`, as revision 31 records a later gate's refusal. A refusal that comes once the
record is written keeps its allow record: an `approval_request` whose ticket cannot open once its
record is written, and a
`straza__draft_submit` whose store write fails or whose draft another revision moved. The
`reason` of a refused `straza__draft_submit` is the refusal's fixed sentence, the no-open-draft
sentence with the draft id when that id is a plain number and a fixed sentence when it is not,
or `Straza: the draft was not saved. Refused: ` and each finding's code, with its document's
number when the finding has one, because the documents reach the record only as
`argumentsDigest`. The `reason` of a refusal of `approval_status`, `approval_await` or
`draft_status` for a ref or draft id that names nothing of the caller's is
`Straza: no such approval.` or `Straza: no such draft.`, without the value, which `arguments`
keeps. Additive: one new action with fields the subject already carries, and no new field or
event type. `valid-audit-admin-device-revoke.json` pins the record of a console session that
revoked a person's machine, with synthetic values.

Revision 56 (2026-11-01): no route enrolls an approver phone or browser for someone else. An enrolled
approver device is how a person decides, so the reference producer mints its one-time enroll token
only on the person's own self route, `POST /v1/approvals/self/enroll-token`. The admin route
`POST /v1/admin/approvers/enroll-token` stays for one release as a refusal. It answers 403 to every
caller that reaches it, whoever the body names, the caller included, and it mints and stores
nothing. `straza.audit.admin` gains the action `enroll-token.refused`, one record per request that
reaches that route, so an attempt to enroll a device for another person stays on the chain. The
record carries the actor fields of §2 for the credential that asked and `reason`, today `only the
person enrolls their own approver phone or browser`. It carries `target` and `user`, the user's
id, and `username` only when the body names a user that exists, by `user_id` or by `username`. A
request that the admin plane refuses before the route, such as an admin API token whose scope
lacks the approvals area, keeps its 401 or 403 and writes no record, as on every other admin
route. Revision 40 wrote no record for a refused identity setup write. That no longer holds for
this one action, and it still holds for every other. Since only the self route mints,
`enroll-token.create` now always carries `channel` and `self: true`. Additive: one new action with
fields the subject already carries. `valid-audit-admin-enroll-token-refused.json` pins the record
of an admin API token that named another user (values synthetic, the field set read back key for
key from the two records the eval stack wrote on 2026-11-01, an admin API token's that named an
unknown user and a console session's that named an existing one: [live]).

Revision 55 (2026-11-01): strazactl no longer signs in, and an admin API token may change a user's
connection. strazactl's sign-in kept a renewing device credential and a session in a file that any
program running as the administrator could copy, so the reference producer refuses it, and strazactl
runs on an admin API token instead. `straza.audit.authn` gains the `login` failure `reason`
`strazactl no longer signs in`, written once per refused request. An enrollment with `client_kind`
`human` gets it with `via` `id-token` once its ID token verified. A check-in under the harness name
`strazactl` gets it on the `id-token`, `device-token`, `session-token` or `device-key` lane, and so
does the `device-token` check-in of a credential strazactl enrolled, under any harness name. A
strazactl session that was alive at the upgrade gets it with `via` `session-token` on its next
request, whether that is its refresh, an admin API call, a connect route, a route that decides a
request or the self route. The record carries `userId`, and `user`, `session` and `harness` where
the lane knows them. A `straza.identity.updated` connection record that an admin API token writes
on `/v1/connect`, with the action `token.connect`, `token.disconnect`, `oauth.disconnect` or
`connection.agents`, carries `setBy` with the token's id and `actorVia` `api-token`, the field
name and the value `straza.audit.admin` records already use for a token. A record written by a
person is unchanged and carries no `actorVia`, so a consumer tells a token's write by the field's
presence. A token's refusal on these routes stores nothing and writes no record, whether its
scope does not cover the request, it names no user, or it tried to start a provider sign-in.
Additive: one new reason, and one field the subject already carries on four existing actions.
`valid-audit-authn-login-strazactl.json` pins the refusal of a strazactl device credential and
`valid-identity-updated-token-connect-api-token.json` pins a token's paste (values synthetic, the
field sets read back key for key from the records the eval stack wrote on 2026-11-01: [live]).

Revision 54 (2026-10-31): a connection write that needs the person leaves a record when a machine
asks for it. `straza.identity.updated` gains the action `connection.refused`. The reference producer
refuses two kinds of write on a caller's connections from a session that a machine enrollment started,
which the background service or strazactl's login holds and any program running as the person can use:
turning the opt-in for sponsored agents on, and any write on another user's connection. It stores
nothing and emits one record with `user` (the id of the user whose connection the write named),
`setBy` (the id of the signed-in user who asked, the actor), `app` (the MCP server) and `reason`
(one sentence, today `a session a machine started cannot allow agents` or `a session a machine
started cannot change another user's connection`). The action name carries the decision. Like
`oauth.connect.refused`, the record changes no identity object, so the change feed of the reference
producer skips it. A session that began with the person's own sign-in still makes both writes, and
the record of a write that lands is unchanged. Additive: one new action with fields the subject
already carries. `valid-identity-updated-connection-refused.json` pins the record (values synthetic,
the field set read back from the two records the eval stack wrote on 2026-10-31: [live]).

Revision 53 (2026-10-31): a record is at most 256 KiB of JSON once the reference producer writes it
to its outbox, a quarter of the event bus's default maximum payload of 1 MiB. A larger record
could never be published, and it held back every later record of its class from the chain and the
sinks. A record within the bound is stored byte for byte as its producer wrote it. Past the bound
the producer cuts the longest strings inside `data` to one common length, the largest at which the
record fits. Each cut string keeps a prefix on a UTF-8 character boundary followed by
`... (<n> bytes, sha256 <hex>)`, where n is the string's length in bytes before the cut and the
hash is the hex SHA-256 of the whole string, so a cut command can be checked against the client's
own copy. Strings inside nested objects and arrays count. Numbers, booleans, object keys and the
envelope are not cut at this step, and a string no longer than its cut text stays whole. When
cutting cannot bring the record under the bound, the record keeps the envelope attributes
`specversion`, `id`, `type`, `source` and `time` and drops any other, and its `data` keeps only
the fields that name who acted, on what, with what outcome and reason, and what ran: `action`,
`actor`, `actorId`, `actorVia`, `agentId`, `agentType`, `app`, `approvalId`, `arguments`,
`command`, `decidedBy`, `device`, `effect`, `event`, `harness`, `outcome`, `phase`, `reason`,
`ruleId`, `session`, `setName`, `snapshot`, `state`, `target`, `tool`, `toolName`, `user`,
`userId`, `via` and `workspace`. Each of these, in the envelope and in `data`, is kept when it is a
string, a number or a boolean, and each such string over 256 bytes of JSON is cut the same way to
fit in 256. A client upload's own `id` of megabytes is therefore cut too, and the cut is the same
on every upload of the record, so the record still dedupes by its `id`. Such a record adds
`dataOmitted`, a string of the form `<n> bytes, sha256 <hex>`, where n is the length of the whole
record as its producer wrote it and the hash is over those bytes. It is the one exception to the
minimum payloads of section 2: it holds only the fields listed here, so `paths` for one is gone.
`dataOmitted` is the one new field. The schema is unchanged, because payloads may carry
additional fields, and so is the default sink list. The chain hashes the record as published,
which is the bounded record. A row that the bus still refuses as over its maximum payload is set
aside: it stays in the outbox unpublished, it reaches neither the chain nor a sink, and every later
record is published. Only a bus whose max_payload is below 256 KiB, or a row written to the outbox
past strazad, makes one. A producer's own caps of earlier revisions still apply first, and the records of earlier
revisions replay unchanged. The example `valid-audit-tool-command-cut.json` pins a hook record
whose command was 921,600 `<` characters (values synthetic, the field set, the cut text and the
hash from the reference producer's boundCE, the prefix shortened from the 43,592 characters it
keeps to 64 so the file stays small: [doc]). The example `valid-audit-tool-data-omitted.json`
pins the record of a client decision whose 40,000 paths left it at 1,080,520 bytes (values
synthetic, the record as boundCE wrote it, not read back from a live record: [doc]).

Revision 52 (2026-10-31): a record quotes only a bounded part of a string the caller sent where
only the request body bounds that string. Past its bound the reference producer cuts the string
on a UTF-8 character boundary and appends `... (<n> bytes)`, where n is its length before the
cut. A `straza.audit.mcp` refusal quotes at most 256 bytes of a server or tool name that the
caller's catalog did not resolve. That cut applies to `toolName` and to the name quoted in
`reason` of a `tools/call` refused as an unknown tool on `/mcp` or on a server's endpoint. It
applies to `app`, `toolName` and the name quoted in `reason` of a request on the endpoint of a
server that the caller's catalog does not hold. Straza's own refusal sentences of an
`approval_request` quote the server, the tool and the tool name it describes cut the same way,
and that sentence is the record's `reason`, while a set's Rego deny message is used as its admin
wrote it. The described call is still decided on the names as sent, because a rule may match a
long name. The refusal of an approval-gated call whose arguments break the tool's input schema
quotes at most 512 bytes of the schema check's detail, which can repeat the arguments, on a
direct call and on an `approval_request` alike. A `straza.audit.approval` record carries at most
4 KiB of the `summary` and of the `justification`, because strazad cuts both when it stores the
request, and the stored request holds the same text. The client's answer quotes the same cut
text, and the `arguments` of a record still carry the call as sent, within their 8 KiB cap. A
name the catalog resolved is never cut, so the record of a hidden or a throttled call still
names its tool whole. The rules of revisions 24 and 42 that `toolName` and `app` carry the name
as sent now hold for a name of up to 256 bytes. A refused call still writes exactly one record.
No field is new and the schema is unchanged, so the default sink list is unchanged. The example
`valid-audit-mcp-unknown-tool-cut.json` pins the refusal of an unknown tool name of 65,536 `<`
characters on `/mcp` (values synthetic, the cut and the field set taken from the reference
producer and its test TestGatewayRefusalsAudited, not read back from a live record: [doc]).

Revision 51 (2026-10-30): a person may publish an AI agent's draft that loosens a policy set that
binds the same agent, which the check refused until now, once they type the set's name to confirm
it (openapi 0.149.0). `straza.audit.admin` `draft.publish` gains the optional field
`agentLoosened[]`, the sorted names of the sets whose `agent.guardrail` risk the publish confirmed.
It is present only when the publish confirmed at least one such risk. Its names come from the
verdict, never from the request body, so it never holds what the publisher typed. The record's
`actor` is the person who accepted the loosening. The `draft.check` record of such a draft lists
`agent.guardrail` under `risks[]` instead of `refused[]`. A direct admin route
still refuses an agent's loosening, so a record it writes never carries the field. Consumers MUST
tolerate the new field (additive under the unknown-field rule of §2). No action or event type is
new, so the default sink list is unchanged. The example
`valid-audit-admin-draft-publish-agent-loosened.json` pins the record (values synthetic, the field
set taken from this revision's text and not yet read back from a live record: [doc]).

Revision 50 (2026-10-30): a `straza.audit.tool` record of a harness tool Straza does not classify
names the tool. Its `tool` is `other`, and its `toolName`, which until now only an `mcp.call` record
carried, holds the harness's own name of the tool (spec/hook-profile revision 2026-10-30), so an
admin finds the tools a harness added by querying the records of `tool` `other`. A client that
sends no name writes no `toolName`, as before. Additive: no new field and no schema change, and
the example `valid-audit-tool-other-named.json` pins the record.

Revision 49 (2026-10-27): an AI agent's ID token from the built-in issuer names the key it was
issued for, and a change of that key ends the agent's sessions (spec/attestation, section Agent
proofs at check-in, and spec/session-token, section Built-in ID tokens). `straza.audit.authn`
`login` failures gain two `reason` values with `via` `id-token`. `the ID token was issued for a key
that is no longer registered` marks an agent's ID token whose `straza_jkt` names a key that an
admin replaced or removed, at a check-in, an enrollment, `GET /v1/self` or any other route that
takes a built-in ID token. `the DPoP proof key is not the key the ID token was issued for` marks an
agent's check-in whose DPoP proof comes from another key than the one its ID token names. Each
refused request writes exactly one such record, with the agent's `user` and `userId`, and
`harness` on a check-in. When an admin replaces an agent's key with another or removes it, strazad
writes one `session.end` record with `outcome` `revoked-admin` for each active session of the
agent that it ends, with `user`, `userId`, `session`, and the `reason` `the agent's key was
replaced` or `the agent's key was removed`, and the one `straza.revocation.sessions` event of those
sessions carries the same `reason`. A first key and the same key registered again end nothing and
write no such record. A check-in that reads the agent's key again once its session row exists, and
finds it replaced, removed or unreadable, ends that session before handing it out, with no
`session.start` and no `session.end`. Its `straza.revocation.sessions` event then carries the
`reason` `the ID token was issued for a key that is no longer registered` or `the agent's key could
not be read when the session started`. No example is added, because each refusal has the shape of
`valid-audit-authn-login-device-key.json` with `via` `id-token` and another `reason`, and each end
has the shape of `valid-audit-authn-session-end.json` (derived from this revision's text, not
captured from a live wire: [doc]). Additive for consumers: new values of a free-text field and a
known outcome from a new cause, with no new action, `via`, field or event type and no schema change,
so the default sink list is unchanged.

Revision 48 (2026-10-24): Straza gives git grants through `POST /v1/grants` (openapi 0.146.0). A git
grant is a short signed token that lets a resource server serve one fetch or one push of one repo
(spec/grant v1alpha1), and the chain records each grant and each refusal. `straza.audit.mcp` gains
the `event` value `grant` and the field `grant`. The route decides a grant as a call of the tool
that the server's `straza.git.ops` maps the operation to (app-manifest revision 9), so its record
has the fields of a `tools/call` record of that tool: `session`, `user` and `harness` from the
session, `tool` `mcp.call`, `app` the server's name, `toolName` the mapped tool, and `effect`,
`ruleId`, `setName`, `reason`, `snapshot`, `granted`, `default`, `bindingId`, `role`, `object` and
`rolesChecked` as revisions 23, 31 and 44 define them. `arguments` is the JSON text of the
arguments the decision ran with, cut at 8 KiB with `argumentsTruncated` as before. For a fetch, and
for a push without refs, which asks for the grant that opens the push advertisement, it is
`{"<pinned argument>": "<owner/name>"}`. The pinned argument is the one `objects.tools` names for
the mapped tool, and owner/name is the repo's canonical name from the server's object list. A push
with refs adds `refs`, the array of updates `{ref, old, new}` the push names, and, when exactly one
of them is under `refs/heads/`, `branch`, that ref's short name. A grant record carries no
`credentialId`, `credentialSource` or `credentialOwner`, because a grant resolves no credential.
strazad writes exactly one such record for each grant it issues and for each refusal the decision
makes: a tool the session cannot see, a tool the session's policy overlay hides, the rate limit,
the object gate, the policy engine, an approval that ended without allowing the grant, such as
one a person denied or one that expired, and an approval of the ticket class that is still open.
A ticket never holds the request: it is refused at once with the ticket's sentence in git's words,
as `tools/call` refuses it on `/mcp`, and the same git command after an approver allows the ticket
gets the grant, whose `approval` is the ticket's id. A refusal is `effect: deny` with the refusal sentence as `reason`, with the allowing rule or without
one as on a `tools/call`. An issued grant is `effect: allow` and adds `grant`, an object with `jti`,
the grant's id, `op` ∈ `fetch|push`, `aud`, the grant's audience, which is the server's canonical
`straza.git.url`, `exp`, the grant's expiry as an RFC 3339 UTC time, and `approval`, the approval
request id, present only when an approval ran for this grant. The grant's `dec` claim is the
CloudEvents `id` of its record, so a resource server's log line joins the chain. The record enters
the audit queue before the answer is written, and under audit backpressure `block` a grant whose
record cannot be queued is refused with 503 and never issued. A 202 answer, which says a hold, an
approval that keeps the request waiting, is still open, writes no `straza.audit.mcp` record, because
the request phase of
`straza.audit.approval` records it, and the later request that the person's decision answers
writes the one record. The route's other answers write none, because each comes before the
decision or after it failed to produce a grant: a malformed body (400), a push that names more ref
updates than one grant can carry (400), a session whose token is bound to no key (403), a repo that
no server lists (404 `not_listed`), a repo that two servers list (409 `ambiguous`), an op that the
server's git block maps to no tool (403), and a grant that strazad could not sign (500). A 401
writes what every session site writes (revision 45). `POST /v1/grants/resolve` decides nothing and writes no record. Examples
`valid-audit-mcp-grant-issued.json`, a push grant issued after an approval, and
`valid-audit-mcp-grant-refused.json`, a fetch the object gate refused (derived from this revision's
text and the grant route's contract, not captured from a live wire: [doc]). Additive for
consumers: a new value of an existing field and a new field, with no new event type and no schema
change, so the default sink list is unchanged. Consumers MUST tolerate unknown `event` values on
`straza.audit.mcp`, as revision 42 says. A consumer that ignores `event` counts an issued grant as
a call of the mapped tool, which it is in effect.

Revision 47 (2026-10-22): a browser sign-in enrolls a person's machine key only when it was made for
that key, the server judges the sign-in lane of every person's key enrollment, and the enterprise
profile refuses two `id-token` check-ins (spec/attestation, sections Person sign-in at key
enrollment and Enterprise ID-token check-ins). `straza.audit.identity` gains the action
`device.enrolled`. strazad writes one such record for each person's key enrollment that succeeds, a
re-enrollment that replaces the key included, with `user`, the user's id, `device`, the device's id,
`signIn` ∈ `browser|code`, the sign-in lane strazad judged from the ID token's nonce, `custody` ∈
`account|user`, the custody kind the enrollment reported, and `authTime`, the ID token's `auth_time`
as an RFC 3339 time, omitted when the token carried none. The record carries no `origin`, because no
identity manager or admin produced it. An enrollment of strazactl or the console, `client_kind`
`human`, writes none, and the `straza.identity.updated` event of an enrollment is unchanged.
`straza.audit.authn` `login` failures gain eight `reason` values and list a ninth. With `via`
`device-key`, a person's key enrollment writes `the sign-in was made for another key` when the ID
token's nonce is neither empty nor names the key that signs the enrollment's proof, `the sign-in
carries no auth_time`, `the sign-in is older than 10 minutes` and `the sign-in time is ahead of the
server clock` when a browser sign-in fails the freshness rule, whose `auth_time` must be at most 10
minutes old and at most 1 minute ahead, `the sign-in lane is below governance.minSignIn` when its
lane ranks below the server's `governance.minSignIn`, and on the built-in issuer `the sign-in was
made for another Straza program` when the ID token's audience is not `straza`. A key enrollment
whose `auth_time` is already older than `governance.signInMaxAge`, on either lane, writes `the last
sign-in on this machine is older than governance.signInMaxAge`, the reason a key check-in or a
refresh past the sign-in deadline has written since revision 45, whose example
`valid-audit-authn-login-device-key.json` carries it and whose text did not list it. `the sign-in
lane is below governance.minSignIn` also marks a key check-in, with `via` `device-key`, and the
refresh of a session bound to a keyed device, with `via` `session-token`, whose stored lane ranks
below the setting, a device enrolled before this revision counting as `code`. With `via` `id-token`,
on the enterprise profile only, `a person's ID token alone does not start a session on enterprise`
marks a person's check-in under a coding harness name, any name but `strazactl`, `console` and
`self-service`, and `an agent check-in without a key proof is refused on enterprise` marks an AI
agent's or a service account's check-in without a DPoP proof. On the enterprise profile the person
rule answers before the device checks, so a person's check-in under a coding harness name that names
a `kit` or kindless device without a key writes `a person's ID token alone does not start a session
on enterprise` instead of revision 46's `the client's bearer device credential is retired`. Each
refused request writes exactly one such record, with `user` and `userId` once the ID token or the
session names them, `session` on a refresh and `harness` on a check-in and a refresh. Example
`valid-audit-identity-device-enrolled.json` (derived from this revision's text, not captured from a
live wire: [doc]). No example is added for the new reasons, because each refusal has the shape of
`valid-audit-authn-login-device-key.json` with another `reason`, and with `via` `id-token` on the
enterprise refusals. Additive for consumers: a new action with three new fields on a subject whose
consumers MUST tolerate unknown actions since revision 45, and new values of a free-text field, with
no new event type and no schema change, so the default sink list is unchanged.

Revision 46 (2026-10-21): the straza client checks a person's machine in on the key lane only, and
the server checks where a machine key is kept (spec/attestation, sections Retired kit bearer lane
and Key lane). `straza.audit.authn` `login` failures gain two `reason` values. `the client's bearer
device credential is retired` marks a request of a straza client older than the key lane: an
enrollment of `client_kind` `kit` or none without a DPoP proof and an `id-token` check-in that
names a `kit` or kindless device without a key, both with `via` `id-token`, a `device-token`
check-in on such a device with `via` `device-token`, and the refresh of a session bound to one with
`via` `session-token`. `key custody is below governance.minCustody` marks a person's key enrollment
or a key check-in, with `via` `device-key`, and the refresh of a session bound to a keyed device,
with `via` `session-token`, whose key custody, as the client reported it at enrollment, ranks below
the server's `governance.minCustody`. An AI agent's key carries no custody, so no agent record
carries this reason. Each refused request writes exactly one such record, with `user` and `userId`
once its credential names them, `session` on a refresh and `harness` on a check-in and a refresh.
Rows of kind `human`, the lane of strazactl and the console, write neither reason. A check-in under
a human client's harness name on a `kit` or kindless row without a key keeps the client binding
reason it wrote before, such as `device credential is bound to the enforcement kit`. An AI agent's
deviceless `id-token` check-in without a proof writes no new reason, because the cut does not reach
it. An AI agent's `id-token` check-in may carry a DPoP proof (spec/session-token). When that proof
fails, strazad writes one login failure with `via` `id-token`, the agent's `user` and `userId`,
`harness`, and the reasons the key lane writes, `the DPoP proof was replayed` for a replayed proof
and `the DPoP proof did not verify`, named here, for any other failure. Its nonce challenge writes
none. A person's `id-token` check-in with a DPoP proof is now judged after its ID token verifies: a
token that verifies answers 400 with no record, as before, and one that does not verify writes the
`id-token` lane's login failure, such as `login not accepted`, where revision 45's producer wrote
none. In the same revision the `reason` of a `session.end` `lifetime-closed` record ends `the
client starts a new session from its enrollment` instead of `the client starts a new session from
its device credential`, because the client that starts the new session may hold a key instead of a
device credential, and the revision 28 example `valid-audit-authn-session-end-lifetime.json`
carries the new text (derived from the reference producer's reason in
internal/server/admin_sessions.go, not captured from a live wire: [doc]). No new example, because
each new record has the shape of the revision 45 example `valid-audit-authn-login-device-key.json`
with another `reason`. Additive for consumers: new values of a free-text field, with no new action,
`via`, field or event type and no schema change, so the default sink list is unchanged.

Revision 45 (2026-10-20): a device can enroll with a key that never leaves the machine, and the
chain says when a copy of that key or of its session token is used (spec/attestation key lane,
spec/session-token `cnf`). `straza.audit.identity` gains the action `device.key.reused`. A
key-lane check-in whose `counter` is at or below the last one the server accepted for the device,
under a valid proof from the device's key, means a copy of the key checked in. strazad then revokes
the device as an admin revoke does, with its `straza.revocation.device` event, and writes one
record with `user`, the id of the device's user, `device`, the device's id, `counter`, the counter
the check-in sent, `storedCounter`, the last counter the server accepted, and `reason`, the value
`check-in counter at or below the last one accepted`. `counter` and `storedCounter` are integers.
The record carries no `origin`, because no identity manager or admin produced it, and that
check-in writes no `straza.audit.authn` record. `straza.audit.authn` gains the action `proof`,
whose only `outcome` is `failure`. strazad writes one such record for each request whose session
token is bound to a key and that carries no valid DPoP proof from that key: no proof, more than
one, a proof that does not verify, or the token sent under the `Bearer` scheme. It carries
`userId`, `session` and `harness`, read from the token, `reason`, and `sourceIp` and `userAgent`
as a login record does, and no `via` or `user`. A request refused only because its proof's nonce
was missing or stale writes no record, because the client signs again with the nonce the answer
gave it. `login` gains the `via` value `device-key`, the key lanes. The 401 and 403 refusals of a
key-lane check-in write a login failure as the other lanes' do, a replayed proof with the `reason`
`the DPoP proof was replayed`, except the nonce challenge, which writes none, and a key reuse,
which writes the identity record above instead. A key enrollment whose user is not a person is
refused with one login failure with `via` `device-key` and the `reason` `only a person can enroll
a machine key`. A device enrolled with a
key accepts only its key, so an `id-token` check-in that names it and a `device-token` check-in on
it write a login failure with their own `via` and the `reason` `the device checks in with its
key`. A key enrollment refused for its proof writes no record. A proof that fails is judged
before the ID token names a user, and a replayed proof writes none either. Examples `valid-audit-identity-device-key-reused.json`,
`valid-audit-authn-proof.json` and `valid-audit-authn-login-device-key.json` (derived from this
revision's text and the reference producer's code, not captured from a live wire: [doc]).
Additive for consumers: a new action on each of two subjects, a new `via` value and two new
integer fields that no earlier record carries, with no new event type and no schema change, so
the default sink list is unchanged. Consumers MUST tolerate unknown `action` values on
`straza.audit.identity` and `straza.audit.authn`, as well as unknown `via` values.

Revision 44 (2026-10-19): a role's binding on a server that pins an object type covers every object
of the type or some objects by id, and the gateway checks every `tools/call` to such a server
against the roles the caller holds before the policy engine runs. `straza.audit.mcp` gains `object`
on every call the object check decides whose tool reaches objects by `argument` or `credential`,
and on every refusal of that check. A call refused before the check, as an unknown tool, a tool the
session's catalog hides or by the rate limit, carries no `object`. `object` carries `type`, the
pinned object type, and `reach` ∈ `argument|credential|all|none`, the pinned reach of the tool, a
tool the pin does not name reading `all`. It adds `value`, the object the call names, when the
call's arguments yielded one, and `id`, the object list's id for that value, when the list holds
it. A value longer than 512 bytes is cut on a character boundary, and `object` then adds
`valueTruncated: true` and `valueSha256`, the hex SHA-256 of the whole value. A refusal of the
check is `effect: deny` with the refusal sentence as `reason`, empty `ruleId` and `setName`,
`granted: false`, no `role` and no `bindingId`, and `rolesChecked`, the sorted names of the held
roles that bind the tool, which may be empty. `role` names the role that admitted a call, so a
refusal never carries it, and a consumer MUST NOT read `rolesChecked` as roles that admitted
anything. On a call the check lets through, `role` and `bindingId` name the first held role by name
whose binding covers the object, which is no longer always the first held role by name that binds
the tool, and `credentialId` names that role's credential row when it has one. `straza.audit.admin`
records `apps.binding.create` and `apps.binding.delete` gain `covers` when the binding has a covers
line, always as an object: `{"every": true}`, or `{"objects": [{"id", "value"}]}` with the objects
sorted by value and each value as it was when the role was published. A first pin of a server's
object type, an unpin and migration 39's backfill change covers lines in place and write no binding
record, so a consumer cannot rebuild a binding's covers line from binding records alone. Attribute
values never ride either record. Examples `valid-audit-mcp-object-allow.json`,
`valid-audit-mcp-object-deny.json` and `valid-audit-admin-apps-binding-create-covers.json` (captured
from the eval stack's objects-demo server on 2026-10-19, at a build of the working tree that
became this revision's commit: [live]). Additive for consumers: new fields on
existing records, with no new event type and no schema change, so the default sink list is
unchanged. Consumers MUST ignore unknown fields, as before.

Revision 43 (2026-10-18): Straza lists the objects of an MCP server whose manifest pins an
object type (app-manifest revision 7), and every listing that changes something is on the chain.
`straza.audit.admin` gains the action `apps.objects.list`. strazad's own background listing
writes it, so it carries no `actor`, `actorId` or `actorVia`, and they are never fabricated. One
record is written on a server's first listing, whenever the version of the server's object list
changes, and whenever the outcome of a listing flips between `ok` and `failed`. A listing whose
result equals the list Straza holds, or a failure that follows a failure, writes none. The record
carries `target`, the server's id, `app`, the server's name, `tool`, the listing tool, `type`,
the pinned object type, `outcome` ∈ `ok|failed`, `count`, the objects in the list Straza holds
after the listing, and `version`, an integer, the version of that list, which is 0 before the
first good listing. Most `apps.*` actions name the server only by `app`, and this one also names
it by id in `target`, as the records of other objects name theirs. An `ok` record adds `joined`
and `left`, integers, the numbers of objects that joined and left the list at `version`. A `failed` record
adds `reason`, the sentence that says what failed and what to do next, and keeps `count` and
`version` of the list that stays, because a failed listing changes nothing. A record never
carries an object's value, title or attributes. `apps.secret.set`, which the reference producer
has written with `app`, `role` and `scope` ∈ `app|role` before this revision and which is listed
from it, gains the `scope` value `listing`, the server's listing credential, which Straza sends
only on its own calls to the listing tool, with `role` empty. `apps.secret.remove` carries
`scope: "listing"` with `role` empty when it removes the listing credential, and every other
removal carries no `scope`, as before. Setting or removing a listing credential emits the
existing `straza.apps.updated` with `change: secret` and `app`, and a listing emits no
`straza.apps.*` and no `straza.identity.*` event. Examples
`valid-audit-admin-apps-objects-list.json`, `valid-audit-admin-apps-objects-list-failed.json`,
`valid-audit-admin-apps-secret-set-listing.json` and
`valid-audit-admin-apps-secret-remove-listing.json` (captured from the eval stack's
objects-demo server on 2026-10-18: a listing that added one repo, the listing that failed once
its listing credential was removed, and the removal and the new setting of that credential:
[live]). Additive for consumers: one new action, a new
value of the existing field `scope`, and the fields of the new action, with no new event type and
no schema change, so the default sink list is unchanged. Consumers MUST tolerate unknown `action`
values and unknown `scope` values on `straza.audit.admin`.

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
used, `reverts` on an undo, and from revision 51 `agentLoosened[]`, the
sets whose `agent.guardrail` risk the publish confirmed). `draft.discard` (`draft`, `revision`,
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
of `arguments` and `argumentsTruncated`, because the deny record of a
submit refused for a secret would otherwise carry that secret. Consumers MUST tolerate the new
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
set equal to the one record read back from the eval stack on
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
equal to the six allow records read back from the eval stack on
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
field sets equal to the five records read back from the eval stack
on 2026-09-21: [live]).

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
dedupe by `id`. From revision 53 the reference producer cuts an event to
at most 256 KiB of JSON, as revisions 53 and 58 say.

## 2. Subject taxonomy

| Type / subject | Emitted when | data payload (minimum) |
|---|---|---|
| `straza.audit.tool` | a local-tool decision (hook PDP or `/v1/decide`) | `session,user,harness,event,tool,command,effect,ruleId,setName,reason,snapshot` (rev 20: `setName` = the deciding PolicySet, empty when no rule matched; absent on pre-rev-20 records); adds `toolName` (mcp.call upstream tool; rev 50: the harness's own name of an `other` tool), `paths[]` (file/url targets), `workspace` when the event carries them; adds `agentType,agentId` (rev 13) when the harness attributed the call to a delegate, so a deny inside a subagent is then distinguishable from the main agent's; rev 59: a straza client's record adds `commandTruncated,commandHash` and `pathsTruncated,pathsHash` when it cut `command` or `paths` at 64 KiB, and `event: audit.dropped` is the client's loss record, with `session,user,event,reason,droppedRecords,oversizeRecords,evictedFiles,evictedRecords,refusedCalls,dropEvents` and `firstDrop,lastDrop` when known, and none of the other minimum fields |
| `straza.audit.mcp` | a gateway `tools/call` decision or refusal, or a served `tools/list` (rev 24), or a view list or read on a server's endpoint (rev 42), or a git grant issued or refused on `POST /v1/grants` (rev 48) | `session,user,harness,event,tool,app,toolName,effect,ruleId,setName,reason,snapshot` (rev 20: `setName` as on `straza.audit.tool`); adds `arguments` (verbatim `tools/call` arguments as JSON text, producer-capped; reference producer 8 KiB) + `argumentsTruncated: true` when cut; rev 23: `granted` and `default` (booleans, always present), `bindingId` + `role` (the access row that admitted the call, absent on the built-in `straza` app) and `credentialId` (the credential row the upstream call used, absent when none); rev 24: a refused call (unbound name, hidden tool, throttle) is `effect: deny` with the refusal as `reason` and no rule, a served `tools/list` is `event: tools.list` with `effect: allow`, `reason: catalog served` and `count` (the tools in that response), and `obligations[]` lists the firing rule's obligations (absent when none; never written since rev 28, policyset revision 18 having retired the list); rev 31: a call a rule allowed and a later gate refused (a denied, expired or pending hold, or no credential resolvable for the caller) is `effect: deny` with the allowing `ruleId` and `setName` and the refusal sentence as `reason`, and a credential refusal carries no credential fields; rev 35: `credentialSource` gains `client_credentials` (a token of the agent's own client at the server's provider), with `credentialId` of the form `client_credentials:<server id>:<user id>` and no `credentialOwner`; rev 37: `argumentsDigest` and `argumentsSize` in place of `arguments` on `straza__draft_submit`; rev 42: a served view list on a server's endpoint `/mcp/{server}` is `event: resources.list` with `effect: allow`, `reason: views served`, `app` the server and `count` (the views in that response), a view read is `event: resources.read` with `app` and `uri` (the address asked for, producer-capped like `arguments`, with `uriTruncated: true` when cut), `effect: allow` with `reason: view served` or `effect: deny` with the refusal sentence, and a `tools/list` on a server's endpoint names the server in `app`; rev 44: every call the object check decides adds `object` (`type`, `reach`, and `value`, `id`, `valueTruncated` and `valueSha256` when they apply) when the tool's reach is `argument` or `credential` and on every refusal of that check, and a call refused before it carries none, and that refusal is `effect: deny` with no rule, `granted: false`, no `role` or `bindingId`, and `rolesChecked[]`, the held roles that bind the tool; rev 48: a git grant is `event: grant` with the fields of a `tools/call` record of the tool `straza.git.ops` maps its op to, `arguments` holding the repo under the pinned argument plus `refs[]` and `branch` on a push with refs, one record per issued grant and per refusal of the decision, an open ticket included, and none for the 202 of an open hold, and an issued grant adds `grant` (`jti`, `op` ∈ `fetch\|push`, `aud`, `exp` as an RFC 3339 time, and `approval` when an approval ran), whose CloudEvents `id` is the grant's `dec` claim; rev 52: a server or tool name the caller's catalog did not resolve is cut at 256 bytes on a character boundary and followed by `... (<n> bytes)`, the length as sent, in `app`, `toolName` and `reason` alike, and so is each name an `approval_request` describes where Straza's own refusal `reason` quotes it, while a resolved name stays whole and a Rego deny message stays as written; the schema check's detail in the refusal of a gated call is cut at 512 bytes; rev 58: a call whose reply the gateway withheld for holding the injected credential adds a second record under its own `id`, `event: tool.post` and `effect: deny` with the allowing `ruleId` and `setName` and the call's other fields, the event `request` is one deny per gateway request refused after its token verified, `ruleId` `revoked` or `attestation`, with empty `tool`, `toolName` and `setName` and `app` the server of a `/mcp/{server}` request, and on `/mcp` an unknown tool whose name holds `__` names the part before it in `app` and the part after it in `toolName` |
| `straza.audit.admin` | a mutation of an admin object, over the admin API or over SCIM, or from the gateway's drafting tools, or from strazad's own background work, which names no actor, or (rev 40) an approver device enrolled with a person's enroll token (rev 25: `actorVia` names the credential, `action` names the object) | `actor,action,target`; rev 18: `actor` (username or admin-API-token name) + `actorId` (user/token id) + `actorVia` ∈ `login\|session\|api-token\|enroll-token` (rev 40: `enroll-token` on `approver.enroll`, whose actor is the person whose one-time enroll token was consumed, and consumers MUST tolerate unknown `actorVia` values; rev 26: `scim-token` retired with the SCIM token kind; a SCIM-plane record carries `api-token` with the admin API token's name as `actor` and its id as `actorId`); all three OMITTED when no authenticated principal produced the event (never fabricated); rev 23 actions: `apps.install` (`app,runtime`; rev 30 adds `update` on every record and `changed[]`, field paths and never values, when `update` is true; rev 32 adds `adminRole`, the name of the role that administers the server), `apps.remove` (`app,roles[]`; rev 32 adds `adminRole`, the minted role that went with the server, its memberships each chaining a `roles.unassign` with reason `server removed`), rev 32 actions `apps.disable` and `apps.enable` (`app,status,adminRole`; recorded by the reference producer before rev 32 and listed from it), `apps.binding.delete` (`app,role,tools[],bindingId`, rev 44: `covers` when the binding has a covers line), `apps.secret.remove` (`app,role`); rev 24 action: `apps.grant.remove` (`app,user,credentialId,reason`, one per per-user grant a SCIM deactivation removes); rev 25 actions: `api-token.create` (`name,id,scope`), `api-token.revoke` (`id`), and listing leaves no record (rev 26 retired `scim-token.create` and `scim-token.revoke` with the kind); rev 27 actions: `roles.assign` and `roles.unassign` (`target` the assignment id, `user`, `role`, `roleId`, `origin` ∈ `scim\|admin`, `reason` when the caller has one; one record per membership that starts or ends, over the admin API or over SCIM); rev 33 actions: `roles.create` and `roles.delete` (`target` the role id, `role` its name, `server` the owning server's name when the role is server-owned; one record per role that is born or that goes, the server removal cascade included); listed from rev 33 and recorded by the reference producer before it: `roles.update` (`target` the role id, `role`), `apps.binding.create` (`app,role,tools[]`, rev 44: `covers` when the binding has a covers line), `policy.create` and `policy.update` (`name,id,priority`), `policy.delete` (`name,id`), `policy.activate` and `policy.deactivate` (`name,id,snapshot`, the snapshot that resulted); rev 34: the field `purpose` and the signing key actions `signing-keys.create`, `signing-keys.rotate`, `signing-keys.promote` and `signing-keys.retire` (`purpose,kid`, plus `was` on a retirement by hand; one record per change for the whole deployment, promote and the janitor's retire without the actor fields), and `signing-keys.rotate` (`kid`, no `purpose`) for the session key, recorded by the reference producer before rev 34 and listed from it; rev 37 actions: `draft.create`, `draft.update`, `draft.check`, `draft.contact`, `draft.publish`, `draft.discard`, `draft.expire`, `roles.implication.create` and `roles.implication.delete`, and `draft` on every record a publish writes; rev 40 actions, one record per identity setup write that succeeds and none for a refused one, each with `user` the user's id and all but `nhi-key.removed` with `username`: `user.create` (`target` and `user` the new user's id, `username`, `kind` ∈ `human\|nhi`, `userType` when set, `origin` ∈ `admin\|scim`, rev 41: `scim` on a create or a revive over SCIM, and the revive's `user.create` follows its `user.reactivated` or `user.lift.blocked` record), `user.update` (`target`, `user`, `username`, `changed[]`, the field names a write changed and never values, rev 41: also a SCIM PUT, PATCH or DELETE, whose `changed[]` can carry `username` and `external_id`, and whose record of a status change follows the `user.killed`, `user.reactivated` or `user.lift.blocked` record of that change where the admin API's precedes it, so a consumer pairs them by `user`; a write that changes nothing writes no record), `enroll-token.create` (`target` and `user` the id of the user the token is for, `username`, plus `channel` and `self: true` on the self route; never the token), `approver.enroll` (`target` and `device` the device id, `user` the user id, `username`, `platform`, `name`; actor the person, `actorVia` `enroll-token`), `nhi-key.set` (`target` and `user` the agent's id, `username`, `fingerprint` `sha256:<hex>` of the public key; never the key), and `nhi-key.removed` (`target` and `user` the agent's id, `reason` on a user delete), recorded by the reference producer on a user delete before rev 40 and listed from it; rev 41 action: `approver.revoke` (`target` and `device` the device id, `user` the user id, `username`, `reason` `user deleted by admin`; one record per approver device a user delete removes); rev 43 action: `apps.objects.list` (`target` the server's id, `app`, `tool`, `type`, `outcome` ∈ `ok\|failed`, `count`, `version`, plus `joined` and `left` on `ok` and `reason` on `failed`; no actor fields, written by strazad's background listing on a server's first listing, on every change of its object list version and on every flip of the outcome; never an object's value, title or attributes), and rev 43 lists `apps.secret.set` (`app,role,scope` ∈ `app\|role\|listing`, recorded by the reference producer before rev 43 with `app` and `role`) and adds `scope` `listing` to `apps.secret.remove` when it removes the listing credential, `role` empty on both; rev 56 action: `enroll-token.refused` (`reason`, plus `target` and `user` the user's id and `username` when the body names a user that exists; one record per request that reaches the admin enroll-token route, which mints nothing, so it is the one refused identity setup write that has a record, and none for a request the admin plane refuses first); rev 57 action: `device.revoke` (`target` and `device` the device id, `user` the user id, `reason` `device revoked by admin`; one record per admin device revoke that deletes the device, none for a 404 or a refused write, and none for the revoke of a reused key, which keeps its `device.key.reused` on `straza.audit.identity`); rev 58 action: `user.delete` (`target` and `user` the user id, `username`, `reason` `user deleted by admin`; one record per admin delete that removes a user, before the records of what ends with the user, and none for a 404 or a refused write) |
| `straza.audit.authn` | login and session-end outcomes (rev 21, 22, 28, 39, 41), and a key-bound session token without a valid proof (rev 45) | `action,outcome` with `action` ∈ `login\|session.end\|proof`; `login` adds `via` ∈ `id-token\|session-token\|device-token\|password\|client-assertion\|slack\|approver-token\|device-key` and `outcome` ∈ `success\|failure`; `session.end` has `outcome` ∈ `revoked-self\|revoked-admin\|idle-closed\|lifetime-closed\|revoked-key-reuse` (rev 28: the janitor's absolute-lifetime close; rev 58: `revoked-key-reuse`, a session that the revoke of a reused device key ended); `user`/`userId`/`session`/`harness` when known (never fabricated); `reason` on failures and ends; `sourceIp` (transport peer, never XFF) + `userAgent` (capped 256 B) ONLY when a client connection produced the event (janitor events and a `slack` login failure carry neither: from rev 39 their absence means that no connection from the person's own client produced the event); rev 39: a `slack` login failure adds `approvalId`, the request the refused tap tried to decide, and carries no `session`, `harness`, `sourceIp` or `userAgent`; rev 41: an `approver-token` login failure carries `sourceIp`, `userAgent` when sent, and no `session` or `harness`, and on a decide refusal `approvalId`; rev 45: `device-key` names the key lanes, a key-lane check-in, whose nonce challenge writes no record and whose replayed proof has the reason `the DPoP proof was replayed`, and a key enrollment refused because its user is not a person, and `proof` has `outcome` `failure` only, with `userId`, `session` and `harness` from the token, `reason`, `sourceIp`, `userAgent` when sent, and no `via` or `user`; rev 46: a `login` failure with the `reason` `the client's bearer device credential is retired` is a straza client older than the key lane on the `id-token`, `device-token` or `session-token` lane, and one with `key custody is below governance.minCustody` is a person's key enrollment, a key check-in or a refresh whose reported key custody ranks below the server's minimum, and an `id-token` login failure of an AI agent may carry `the DPoP proof did not verify` or `the DPoP proof was replayed` when its check-in carried a proof; rev 47: a `device-key` login failure of a person's key enrollment may carry `the sign-in was made for another key`, `the sign-in carries no auth_time`, `the sign-in is older than 10 minutes`, `the sign-in time is ahead of the server clock`, `the sign-in lane is below governance.minSignIn`, `the sign-in was made for another Straza program` or `the last sign-in on this machine is older than governance.signInMaxAge`, and `the sign-in lane is below governance.minSignIn` and `the last sign-in on this machine is older than governance.signInMaxAge` also on a key check-in and, with `session-token`, on the refresh of a keyed device's session, and on the enterprise profile an `id-token` login failure may carry `a person's ID token alone does not start a session on enterprise` or `an agent check-in without a key proof is refused on enterprise`; rev 49: an `id-token` login failure of an AI agent may carry `the ID token was issued for a key that is no longer registered` or `the DPoP proof key is not the key the ID token was issued for`, and a `revoked-admin` `session.end` may carry `the agent's key was replaced` or `the agent's key was removed`; rev 55: a `login` failure with the `reason` `strazactl no longer signs in` is a refused strazactl enrollment, check-in, device credential or session on the `id-token`, `device-token`, `session-token` or `device-key` lane; rev 58: the revoke of a reused key writes one `revoked-key-reuse` `session.end` per session it ends, with the reason `device key reused`, and an admin device revoke writes one `revoked-admin` record per session it ends with the reason `device revoked by admin`, a `password` login failure may carry `an AI agent or a service account cannot sign in with a password`, and a `session-token` login failure with `session is no longer active` or `device or user revoked` is also written for a `/v1/audit/batch` upload refused for that revocation |
| `straza.audit.identity` | the identity kill switch fires, is lifted, or an IdM lift is blocked by a surviving lock (SCIM `active:false`/DELETE, admin disable/lock, reactivation/unlock), or (rev 45) a device key is used from a copy, or (rev 47) a person enrolls a machine key | `user,action,origin`: `action` ∈ `user.killed\|user.reactivated\|user.lift.blocked\|device.key.reused\|device.enrolled` (rev 15, rev 45, rev 47); `user.killed` adds `reason,sessionsRevoked`; `user.lift.blocked` adds `heldBy[]` (the surviving lock origins); rev 45: `device.key.reused` carries `user,device,counter,storedCounter,reason` and no `origin`, `counter` and `storedCounter` integers, one record per device a key-lane check-in revokes for a counter at or below the last one accepted; rev 47: `device.enrolled` carries `user,device,signIn,custody` and `authTime` when the ID token carried `auth_time`, and no `origin`, `signIn` ∈ `browser\|code` and `custody` ∈ `account\|user`, one record per person's key enrollment that succeeds |
| `straza.audit.prompt` | capture-enabled session: the user submitted a prompt | `session,user,harness,content,mode,truncated,contentHash`: `mode` ∈ `verbatim\|redact`; `contentHash` = SHA-256 of the FULL original content; `content` capped (client knob, default 64 KiB); adds `agentType,agentId` (rev 13) when a delegate produced it |
| `straza.audit.reply` | capture-enabled session: the model (or, rev 13, one of its delegates at `subagent.stop`) finished a response | same shape as `straza.audit.prompt`, and a delegate's reply carries `agentType` (+`agentId`) and rides the PARENT session id |
| `straza.audit.sentinel` | the in-process audit sentinel emits a verdict about a session (§2.1), or (rev 58) the gateway withholds a reply that held the credential it injected | `session,detector,severity,reason,evidence[]`: `detector` ∈ `denyBurst\|denyThenVariant\|writeThenExecute\|captureContent\|toolMixAnomaly\|credentialEcho` (rev 58: `credentialEcho`, raised by the gateway with `source` `strazad`); `severity` ∈ `info\|warn\|critical`; `reason` = one human-readable sentence naming the evidence; `evidence[]` = CE `id`s of the triggering audit events; adds `user` when known and `window` (e.g. `"60s"`) when the detector is windowed |
| `straza.audit.approval` | a `mode: approve` gate is requested, when it resolves, and when its approval is used, a ticket's grant or, since revision 38, a hold's approval (§2.2) | `phase,approvalId,session,user,rule,set,lane,summary,state`: `phase` ∈ `request\|resolution\|consumed`; `lane` ∈ `hook\|gateway`; `state` ∈ `pending\|approved\|denied\|expired`; adds `justification` (gateway lane, `phase=request` only), `decidedBy,channel` plus the optional `decidedReason,decidedDeviceId` (revision 19) on `phase=resolution`, and `consumedBy,consumedAt` on `phase=consumed` (both classes since revision 38); rev 52: `summary` and `justification` hold at most 4 KiB each |
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

Since revision 58 the gateway is a second emitter of one detector,
`credentialEcho`. It raises the verdict on the request path when it
withholds a reply that held the credential it injected for the call
(revision 58), whether the sentinel runs or not, through the durable
outbox every gateway record takes. Its `source` is `strazad`, where the
sentinel's own verdicts carry `strazad-sentinel`. The verdict carries
`session`, `user`, `severity` `critical`, `reason`, today `The MCP server
<server> answered a call of its tool <tool> with the credential Straza
injects for it, so the gateway withheld the reply.`, `evidence` with the
`id` of the call's `tool.post` deny record, and no `window`. It gates
nothing, because the gateway has already withheld the reply, and none is
written when the audit queue refused that deny record. Each withheld reply
raises one verdict.

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
  lane has no schema to inject into and omits it). From revision 52 each
  of `summary` and `justification` holds at most 4 KiB, cut as revision 52
  says.
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
