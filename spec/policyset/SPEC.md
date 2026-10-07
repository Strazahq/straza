# PolicySet, v1beta1 (revision 25)

Status: **beta**. The specification began as v1alpha1, and the move to
beta changed no wire format. Schema: `policyset.schema.json`. Examples:
`examples/valid-*.yaml`, `examples/invalid-*.yaml`. The platform parser
MUST agree with the schema on every example. Decision semantics are
exercised by the decision-table fixtures in `spec/conformance/decisions/`.

Revision 25 (2026-10-06): **a Rego escape module may not call
`glob.match`** (compile semantics; no document-shape change, so documents
keep parsing under every revision-24 client). A module that calls
`glob.match` no longer compiles, so validation, activation and a snapshot
open refuse it, with the reason of the built-ins whose single call can run
far past the deadline. A probe against the reference platform's OPA version
showed both faults. A pattern of 256 nested alternatives that each begin
with a star, 1.5 KB long, ran 198 ms in one call. Braces nested four
million deep, which a module builds by doubling a string, ended the process
with a stack overflow. The refused built-ins are now seventeen, twelve of
them for a single call that runs long or ends the process. The refusal
narrows what compiles, which is not additive, and the spec README freezes
v1beta1 against breaking changes. This one narrowing is a stated exception
to that rule, made before the first public release, because it closes a
way for a module to end the process. A policy
set still matches a glob with the rule patterns of section 4, and a module
matches a string with `regex.match`, `startswith`, `endswith` and
`contains`. No example or fixture calls `glob.match`, and the previous
fixtures replay green. Section 5 holds the rule and the list.

Revision 24 (2026-09-28): **an event kind outside the canonical set is
denied, and a tool outside the taxonomy takes the profile default**
(decision semantics only; no document-shape change, so documents keep
parsing under every revision-23 client). Section 3 gave defaults only for
the canonical kinds and tools, and the reference platform allowed every
other event when no rule fired. An event whose kind is not one of the nine
canonical kinds of the hook profile is now denied in both profiles, before
any rule is read, with a reason that lists the known kinds. A `tool.pre`
or `permission.request` event whose tool is not `mcp.call` and that no
rule matches now takes the profile default whatever its tool. A tool
outside the canonical taxonomy and an event with no tool are therefore
denied where the profile default is deny, the enterprise default, with a
reason that lists the known tools. Where the profile default is allow, the
standalone default, they are still allowed whatever their command, exactly
like `other`. The first-party client emits only canonical kinds and maps
an unknown harness tool to `other`, so its decisions change only for a
blocking hook payload that names no tool, which the enterprise default now
denies. New fixture cases in `enterprise-defaults.yaml` and
`shell-standalone.yaml` pin the decisions, and the previous fixtures
replay green. Section 3 items 3 and 4 hold the rule.

Revision 23 (2026-09-27): **one approval of a hold runs at most one call,
on any number of replicas** (PEP semantics only; no document-shape change and no
decision change, so documents keep parsing and deciding under every
revision-22 client). Section 2 item 8 said the hook lane honors a
single-use exemption keyed `(session, ruleId, argvHash)`. The reference
platform kept that exemption in the memory of each replica and minted it
even when the held gateway call ran on the same approval, so one approval
could run the identical call once as the held call, once more as a retry,
and once more on each further replica. The use of a hold's approval is now
one atomic server-side write on the approval record, the same write a
ticket's grant uses, so it holds across replicas and restarts. One approval
runs at most one call: the held call it was raised for, whenever its wait
ends, or one identical retry of the same person and session within
`retryTTLSeconds` of the decision, whichever uses it first. A second held
call attached to the same request is refused, and every later identical
call raises a new request. A PEP that
cannot reach the approval store to look up or use an approval denies
(fail-closed). No example or fixture changes, and the previous fixtures
replay green. Section 2 item 8 holds the rule.

Revision 22 (2026-09-27): **`approve.bind: predicate` is reserved** (no
document-shape change and no decision change, so documents keep parsing
and deciding under every revision-21 client). Revision 6 said that
`predicate` binds a ticket grant to a policy-matcher predicate, a widening
for calls whose arguments carry a nonce or a timestamp. The reference
platform never implemented it. Every lane builds the grant key from
`binding` alone and consumes a grant only for a later call with the same
fingerprint, so `predicate` has always behaved exactly as `fingerprint`.
This revision retracts the widening instead of implementing it. The value
stays legal in the schema and the parser, and the decision still carries
it verbatim, so a stored set keeps parsing on every client and keeps the
grant its `binding` gives it today. A PEP MUST treat `predicate` as
`fingerprint`. Validate answers a per-rule advisory with code
`bind-reserved`, and activation logs the same sentence. A future revision
that adds a wider re-match will spell it with a new value, so a document
that carries `predicate` never widens without an edit. The previous
fixtures replay green: case `ticket-deploy-explicit-values` in
`approve-standalone.yaml` still expects `approveBind: predicate` on the
decision. Section 2 item 8 holds the rule.

Revision 21 (2026-09-27): **a Rego escape module reaches nothing beyond its
input, and one deadline bounds it** (compile and decision semantics; no
document-shape change, so documents keep parsing under every revision-20
client). A module that calls one of sixteen built-ins no longer compiles,
so validation, activation and a snapshot open refuse it. Five reach the
network, the file system or the process environment. Eleven can run far
past the deadline in a single call, or end the process. The escape modules
of one decision share a 100 ms deadline, checked between evaluation steps,
and a decision whose modules run past it is a deny. The refusal narrows
what compiles, which is not additive, and the spec README freezes v1beta1
against breaking changes. This one narrowing is a stated exception to that
rule, made before the first public release, because it closes a way for a
module to reach beyond its input. No example or fixture calls a
refused built-in, and the previous fixtures replay green. Section 5 holds
the rule and the list.

Revision 20 (2026-09-24, https://docs.straza.ai/guides/operate/drafts-and-publishing/):
**the built-in `straza` app carries the granted fact on its two drafting
tools** (decision semantics only, with no document-shape change, so
documents keep parsing under every revision-19 client). The gateway lists
`straza__draft_submit` and `straza__draft_status` only to a session whose
roles include the Straza role `straza-draft-config`, and a call of either
carries `granted: true`, set after that tier-one check as it is for a
proxied tool. A holder's call that no rule matches is therefore allowed
with the revision 17 reason, and a rule that fires wins as always, deny,
hold and ticket included. The three approval tools never carry the fact, so
policy stays their only gate. `match.roles` still refuses a Straza role
(revision 16), so the role and not a rule is the grant. The previous
fixtures replay green. Fixture: `granted-native-drafting.yaml` in
`spec/conformance/decisions/`.

Revision 19 (2026-09-20): **the sponsor decider is the person behind the
agent** (request-time semantics; no document-shape change, documents keep
parsing under every revision-18 client). `deciders: [sponsor]`, and the
revision-14 default for a bare approve, route to the person behind the
requester's agent. For a requester who has a sponsor, agent or person, that
is the sponsor, as before. For a PERSON with no sponsor it is the person:
the platform creates the record as a confirm record, its `mode` reads
`confirm` on every surface, and the requester alone decides it, exactly as
under `mode: confirm`. Before this revision that case was the revision-14
request-time deny, so a rule that routed to the sponsor served an agent and
denied a person who ran their own agent. A rule that also names `roles` is
unchanged: the roles carry the record and it never becomes a
self-confirmation, because the author asked for a second pair of eyes. An
AGENT with no usable sponsor is still denied at request time with the cause
and the fix, and so is a person whose recorded sponsor is unusable, fail
closed. `mode: confirm` stays valid and is the same control spelled out for
every requester. A person's decision on their own request is accepted only
from an enrolled device that signs it, unless the deployment sets
`approval.unsignedOwnDecisions`. The decision-table fixtures cannot see
request-time routing, so the cases live in the platform's approval tests
and in the journey corpus.

Revision 18 (2026-09-07): **the approve rule is the reported rule, and the
obligations list is retired** (decision semantics and a validation rule;
no document-shape change, documents keep parsing under every revision-17
client). Two changes. First, when a `mode: approve` rule fires on the
winning allow, the reported `ruleId`, `reason` and set are that rule's,
whatever its priority or set order (§3 item 2). Before this revision a
plain allow from another set at the same priority whose name sorted
first, or a lower-indexed plain allow in the same set, was the reported
rule, and the approval record then named a rule that had not mandated the
hold. The hold is what the caller experiences, so the record names the
rule that caused it. Deny attribution is unchanged. One revision-17
fixture case (`approve-and-classify-both-fire`) pinned the old attribution
and is re-pinned; the new case is
`approve-named-over-earlier-set-plain-allow`. Second, the `redact` and
`notify` obligations are retired, not implemented: they parsed on any
rule, rode the decision and the gateway audit record, and no lane ever
ran them. `obligations[]` on a decision is always empty. The platform
refuses a document that carries an `obligations` list **at validate and
at activation** (400 naming every offending rule and the fix:
`capture.mode: redact` on the set keeps captured content out of the
transcript, `approve.notify` on the rule tells people about a held call).
Drafts may still be applied (git-first flow); sets that were active
before this revision keep parsing and keep governing, with one boot WARN
per set, until their next activation. The field stays in the schema and
on the wire so stored documents validate and older clients read an empty
list.

Revision 17 (2026-09-04): **the granted fact on `mcp.call`** (decision
semantics only; no document-shape change, documents keep parsing under
every revision-16 client). The canonical event gains an optional boolean
`granted`. It says that the subject holds a role whose access row (a
`tool_bindings` row) admits this tool on this app. Only a path that has
checked the binding may set it: the gateway after its tier-one catalog
lookup, the catalog overlay and the admin catalog preview after the
matcher test, and policy simulate after an explicit access check. The
hook lane never sets it, `/v1/decide` zeroes it on ingress whatever the
client sent, and the built-in `straza` app never carries it (policy stays
its only visibility gate). When no rule fires, a granted `mcp.call`
answers **allow** with `default: true` and the reason `allowed by role
access. No policy rule gates this tool.`, and an ungranted one keeps the
**deny** with `default: true`. A rule that fires wins in both directions,
deny over allow, approve gating included, Rego included. Access grants,
policies gate: an access row is the grant, a policy refuses or gates. The
previous fixtures replay green because an absent field keeps the deny.
Fixtures: `granted-standalone.yaml` and the enterprise case in
`enterprise-defaults.yaml`.

Revision 16 (2026-08-25): **application-only match.roles** (semantics
only; no wire-shape change, documents keep parsing under every
revision-15 client). `match.roles` MUST name application-kind roles:
they are the roles that carry tool bindings, and a session holds the
implication closure, so a set naming the application role governs every
path to its tools, while one naming a business role governs a single
grant path (the same application role held directly, or through another
business role, walks past it). The platform refuses **at validate and at
activation** (400 listing every violation and its fix): a business role
(name the application roles it composes instead, or use
`match.identity`), an approver role (decide authority only; its place is
`approve.roles`), or a straza role (the control plane never matches
sessions). A name that is NOT a role in Straza stays legal, deliberately
unlike the revision-15 pool rule: `match` is a selector, not a reference
(`match.users` has no existence check either); it matches nobody until
the role exists, and a role later born with a refused kind is caught at
the set's next activation plus the boot audit. Drafts may still be
applied (git-first flow); sets that were active before this revision
keep governing, with one boot WARN per set, until their next
activation. Decision semantics are unchanged.

Revision 15 (2026-08-21): **the approver role kind** (semantics only; no
wire-shape change, documents keep parsing under every revision-14
client; see https://docs.straza.ai/concepts/approvals-model/).
Roles gain a fourth kind, `approver`: decide authority only, on the
access plane (it renders on the SCIM wire and the IdM masters and
certifies its membership), never a tool binding or a knowledge pack,
terminal in the implication graph, fixed at create. `approve.roles`
MUST name approver-kind roles, or `straza-admin` by name; the platform
refuses anything else **at validate and at activation** (400 listing
every violation and its fix): an access role (business or application,
which must never double as decide authority by name coincidence), a
straza role other than straza-admin, or a name that is not a role in
Straza (a pool nobody holds is a doomed record). Drafts may still be
applied (a policy can be written before its roles exist; it cannot
start governing before they do). Sets that were active before this
revision keep governing, with one boot WARN per set, until their next
activation. Decide time is unchanged: a decider must freshly hold one of
the record's roles (or sit in its user-scoped pool), NHIs never decide,
`selfApproval` and the revision-14 sponsor default are untouched.

Revision 14 (2026-08-21): the sponsor default and the unroutable deny
(semantics only; no wire-shape change, no new fields, documents keep
parsing under every revision-13 client). A **bare approve** (an approve
rule with no `roles`, no `deciders`, no `selfApproval`; `mode: confirm`
is its own lane) now behaves exactly as if it declared
`deciders: [sponsor]`: approval falls on the requester's accountable
human by default, an engine default like `timeoutSeconds: 90`. A request
whose decider pool resolves ENTIRELY empty (no roles and no usable
sponsor: absent, unknown, inactive, an NHI, or the requester themselves)
is **denied at request time** with a reason naming the cause and the fix;
no pending record is created, because a record nobody is notified of and
nobody but a console-watching admin could find would only expire to deny
after wasting the whole window (holds: seconds; tickets: days). This
supersedes the revision-13 quiet-unrouted behavior for NEW records;
pending unrouted rows created before the upgrade keep it (announced
nowhere, straza-admin decide rights, expiry deny). The straza-admin
fallback for decide RIGHTS remains for such legacy rows and for
`selfApproval` records with no pool; rules that WANT admin deciders say
so with `roles: [straza-admin]`. Sponsors stay human-only: an agent
sponsoring an agent is not accountability, and agent-hierarchy fleets
model the accountable human on every NHI's sponsor edge in the IdM
(see https://docs.straza.ai/concepts/identities/).

Revision 13 (2026-08-20): `approve.deciders`, decider kinds resolved per
record. The `approve` block gains `deciders` (list; the only member today
is `sponsor`), composable with `roles`: allowed deciders for a record are
the requester's resolved sponsor (when listed) plus holders of any listed
role. The sponsor is the requester's accountable human (`User.Sponsor`,
the IdM-owned edge, synced over SCIM); it is resolved AT REQUEST TIME and
persisted on the approval record (`approver_users`), so a later sponsor
change never silently retargets an open record. A resolved sponsor must
be an active, non-NHI user; anything else counts as absent. Vocabulary is
spec-fixed with the revision-8 typo posture: unknown kinds and duplicates
are rejected at parse, never silently widened. Under `mode: confirm`,
`deciders` is REJECTED like the revision-11 pool fields (the requester is
the decider). Reference-server announcement semantics change with it: a
record with NO decider signal at all (no resolved sponsor, no roles) is
UNROUTED: no channel announces it (create/reminder lanes), it remains on
the console/CLI pull surfaces and in the `straza.audit.approval` stream,
straza-admin retains decide RIGHTS over it, and it still expires to deny,
so an unconfigured pool can no longer page every admin's phone at fleet
scale. Additive; the revision-2 strict-parser caveat applies: roll
clients before authoring documents that SET `deciders`.

Revision 12 (2026-08-13, unified role model, spec/scim-profile §4):
`match.groups` is RETIRED. Groups no longer exist as a platform object (the SCIM surface renders
roles in the wire-group costume; membership IS role assignment), so the
selector families are `roles`, `users`, and `identity`. A document
carrying `spec.match.groups` is rejected at parse with a pointed error,
and presence alone fails, even an empty list: after the store migration
the selector could only ever match nothing, and a deny that silently
matches nothing is the worst failure mode an enforcement plane has.
Subjects no longer carry group names anywhere (checkin payload, gateway
subject cache, client kit state, catalog preview, builder simulation).
BREAKING for documents that used `match.groups`: re-spell onto `roles`
(under the unified model the former mapped group IS the role). Example
`invalid-match-groups.yaml` pins the refusal; `valid-rego-escape.yaml`
re-anchored onto roles.

Revision 11 (2026-08-09): `mode: confirm`, the requester-decides gate. A
fourth rule mode: the human behind the requesting session must confirm
before the effect is final. It shares the `approve` knob block
(class/timeouts/bind/binding/notify, same normalization and defaults), but
the decider pool is NOT configurable: `approve.roles` and
`approve.selfApproval` under `mode: confirm` are REJECTED at parse (the
requester is a fact on the record, not configuration; rejecting beats
silently ignoring). Decide-time semantics: only the requester may decide
(root included may not), non-human identities never decide (unchanged), an
unattended confirm expires into deny, and decide-notification routing
targets the requester alone. Engine invariant (the revision-9 clamp's
sibling): an autonomous-agency subject turns a confirm rule into a plain
deny at evaluation, because there is no human to confirm and the record
would only expire. PEP behavior is otherwise identical to `approve` (hook
lane deny-with-reference + single-use retry exemption; gateway lane
blocking hold), so clients need no new machinery. Decision wire: allows
carry `confirm: true` beside the `approve` spec. Additive; the revision-2
strict-parser caveat applies: roll clients before authoring documents
that USE `mode: confirm`.

Revision 10 (2026-08-01): `require.deviceCert` honesty. NO wire change:
schema, parse, and evaluation semantics are untouched (an explicit
subject `deviceCert: true` still satisfies the predicate, as the
`combining-standalone.yaml` decision fixtures pin). What changed is the
PRODUCED value: the device-certificate factor was never implemented,
yet the reference server set subject `deviceCert` from mere device
enrollment while the client kit hardcoded `false`. The same rule allowed
on the gateway PEP and denied on the hook PEP, and the gateway side
silently passed a check that verified nothing. Both reference producers
now emit `false` until the factor ships, which fails closed, the
§"require" bullet documents the not-implemented status, and
validate/activate surface a per-rule advisory (`warnings` in the validate
response) so authors hear it before a set governs.

Revision 9 (2026-07-28): identity-typology match (spec/scim-profile
revision 5 carries the provisioning half). `spec.match` gains
`identity`, `{userType, agencyMode, swarmId}` lists selecting on the
typology the checkin resolved onto the subject (§1.4): same OR-within/
AND-across combining as the other selectors; an UNSET subject field matches
no listed value. Vocabularies are spec-fixed for `userType`
(`human|agent|service`) and `agencyMode`
(`interactive|supervised|autonomous`); `swarmId` values are
deployment-defined. NEW ENGINE INVARIANT, not a matcher: a winning
`mode: approve` decision for a subject whose `agencyMode` is `autonomous`
NEVER carries `selfApproval`: the engine clamps it per decision, whatever
the rule says (there is no human behind the session to be the self;
`examples/valid-identity-match.yaml` documents the shape, fixture
`identity-standalone.yaml` pins it). Documents that SET `match.identity`
require revision-9 clients. The strict parser on older clients rejects the
unknown key and fails closed: roll clients before writing identity blocks.

Revision 8 (2026-07-27): approval notification routing
(https://docs.straza.ai/concepts/approvals-model/). The `approve`
block gains `notify` (list; members `console` | `slack` | `push`): which
notification channels ANNOUNCE this rule's approvals. Empty/omitted = every
configured channel (pre-revision-8 behavior, unchanged). `console` names the
always-on pull surface and matches no push notifier, so `notify: [console]`
alone silences third-party notification for the rule. Routing narrows the
announcement lanes only (create nudge, near-expiry ticket reminder, terminal
status): WHO may decide stays `roles`/`selfApproval`, and every decision
surface (console, CLI, admin API, an already-posted card's callback) keeps
working regardless. A channel listed but not configured simply has nothing to
deliver. The console floor always stands. Unknown members, empty strings,
and duplicates are rejected at validation (fail-closed typo safety: a
misspelled channel must never silently widen back to all-configured).
Distinct from the `notify` obligation, a rule-level list that never ran
and is retired by revision 18. Additive; the revision-2 strict-parser
caveat applies: roll clients before writing `notify`.

Revision 7 (2026-07-25): approval fingerprint v2 (§2 item 8). The
`approve` block gains `binding` (`call` | `tool`, **default `call`**): what the approval
fingerprint COVERS for `mcp.call` events. `call` folds the canonicalized
`tools/call` arguments (hook-profile `args` attribute) into the key, so an
approval binds the exact call, not just the tool; `tool` is the explicit,
diffable opt-out for high-frequency benign tools (identity-only key, the
pre-revision-7 mcp behavior). Non-mcp lanes always bind the concrete call;
`binding` is ignored there. Orthogonal to the ticket `bind` knob (`bind`
selects what RE-MATCHES a grant; `binding` selects what the fingerprint
covers). Keys are versioned (`v2:<scope>:sha256:<hex>`); v1 keys
(`sha256:<hex>`) remain accepted on the consume paths while pre-revision-7
grants drain (bounded by max ticketTTL + grantTTL). Additive; the revision-2
strict-parser caveat applies: roll clients before writing `binding`.

Revision 6 (2026-07-23): long-running approvals (tickets;
https://docs.straza.ai/guides/write-policy/tickets/). The `approve` block gains
`class` (`hold` | `ticket`, **default `hold`**), plus the ticket-class knobs
`ticketTTLSeconds` (decision window, default 24 h, cap 30 d), `grantTTLSeconds`
(post-approval consume window, default 1 h, cap 24 h), and `bind`
(`fingerprint` | `predicate`, default `fingerprint`). `class: hold` is
byte-identical to revision 4: every existing approve rule keeps its exact
semantics (default `hold`, `timeoutSeconds`/`retryTTLSeconds` unchanged). The
blocking-wait knobs are rejected under `class: ticket` (a ticket never blocks).
Additive; the revision-2 strict-parser caveat applies: roll clients before
writing ticket rules.

Revision 5 (2026-07-21): the `straza.dev` apiVersion/ids and the
`straza.ext` Rego escape package replace the pre-release names; rule shapes
and evaluation semantics unchanged. The pre-release ids are a clean break
(§7).

Revision 4 (2026-07-18): `mode: approve` for human-approval-escalated
allows (§2 item 8), and the `approve`
rule block (`roles`, `timeoutSeconds`, `selfApproval`, `retryTTLSeconds`)
configuring the gate. A winning allow is held for a human decision;
timeout, an unreachable approval service, or an explicit denial ⇒ `deny`
(fail-closed, the serverCheck/classify posture). The block is legal only
alongside `mode: approve`. Additive; the revision-2 strict-parser caveat
applies: roll clients before writing approve rules.

Revision 3 (2026-07-17): `mode: classify` for classifier-escalated allows
(§2.7; https://docs.straza.ai/guides/write-policy/classify/), and the
`interpreters` matcher block (§2.2, §4), matched against the hook-profile
`interpreter` attribute on `shell.exec` events. Additive; the revision-2
strict-parser caveat applies: roll clients before writing either.

Revision 2 (2026-07-15): optional `spec.capture` block, the
conversation-capture opt-in (§6; https://docs.straza.ai/guides/write-policy/capture/).
Additive, but parsers are strict: clients older than this revision reject
documents that USE the block. Roll clients before writing capture blocks.

Words MUST/SHOULD/MAY are RFC-2119.

## 1. Document

A PolicySet is a YAML document (`apiVersion: straza.dev/v1beta1`,
`kind: PolicySet`). `metadata.name` identifies the set; `spec.priority`
(default 0) orders **reason selection only**: it never changes
the winning effect. `spec.match` selects the sessions the set applies to:
OR within each list (`roles`, `users`), AND across lists that are
present; an omitted or empty `match` applies to **all** sessions. Role
and user references are by **name** (username for users). `match.groups`
existed through revision 11 and is a pointed parse error since revision
12 (groups retired with the unified role model).

### 1.4 Identity-typology selectors (revision 9)

`spec.match.identity` selects on the identity classification the platform
resolved at checkin (mastered over SCIM, spec/scim-profile §3.2):

```yaml
match:
  roles: [dev]
  identity:
    userType: [agent]                    # human | agent | service
    agencyMode: [autonomous, supervised] # interactive | supervised | autonomous
    swarmId: [scan-fleet-1]              # deployment-defined fleet ids
```

- Combining is identical to the other selectors: OR within each list, AND
  across every present list (including `roles`/`users`).
- An **unset** subject field matches no listed value: unclassified
  identities fall outside every identity-scoped set. A restrictive posture
  that must also catch unclassified subjects is therefore written
  **deny-unless-`<type>`**: a blanket deny in an unscoped set plus the
  privileged lane in a `userType: [human]`-scoped set (fixture case
  `deny-unless-human-catches-unclassified`).
- `userType`/`agencyMode` members outside the spec vocabulary are parse
  errors (a typo must fail loudly, not silently match nothing).
- **Autonomous selfApproval clamp (engine invariant):** when the subject's
  `agencyMode` is `autonomous`, a winning `mode: approve` decision carries
  `selfApproval: false` regardless of the rule. Approval records persist the
  clamped bit, so every decide surface refuses the requester uniformly.
  This is deliberately not expressible policy, because a convention could
  not bind other sets' approve rules.

## 2. Rule matching

A rule *applies* to a canonical event (see `spec/hook-profile`) when ALL of
the following hold:

1. `events` (default `[tool.pre]`) contains the event kind.
2. `tools` is omitted OR contains the event's canonical tool.
3. `apps` is omitted OR (the tool is `mcp.call` AND the event's app name is
   in `apps`). A rule with `apps` set never applies to non-`mcp.call` events.

An applying rule produces a **verdict**:

1. If `require` is present and any predicate fails → verdict `deny`
   (with the rule's `reason`). Predicates:
   - `attestation`: the session's level must be at least the named level
     (order: `managed` > `advisory` > `none`).
   - `deviceCert: true`: the session must hold a platform-issued device
     certificate. **Not implemented (revision 10)**: no certificate is
     issued or verified yet, reference producers set every subject's
     `deviceCert` to `false`, so this predicate cannot be satisfied and
     the rule denies wherever it gates an allow (fail-closed by design;
     validate/activate surface an advisory). The field stays legal,
     because a device certificate factor is planned.
   - `harness`: the session harness must match one entry; entries are
     `name` or `name>=version` (numeric dotted compare on the version prefix).
2. Otherwise, matcher blocks are consulted in **deny-first** order:
   - `command.denyPatterns` / `paths.deny` / `toolNames.deny` /
     `interpreters.deny` matched → `deny`.
   - else `command.allowPatterns` / `paths.allow` / `toolNames.allow` /
     `interpreters.allow` matched → `allow`.
   - else, if the rule HAS matcher blocks but none matched → **no verdict**
     (the rule does not fire).
   - An `interpreters` side is consulted ONLY when the event carries a
     non-empty `interpreter` attribute (§4), so a `"*"` pattern never matches
     a non-interpreter event.
3. If the rule has NO matcher blocks: the verdict is `effect`. A rule with
   neither matcher blocks, nor `require`, nor `effect` is invalid
   (rejected at validation).
4. `effect`, when present alongside matcher blocks, MUST be consistent with
   the matched side and MAY be used by authors for readability; validators
   MUST reject `effect: allow` combined with only deny-side patterns and
   vice versa.
5. Multi-path events: the deny side fires when ANY path matches; the allow
   side fires only when EVERY path matches (and there is at least one path).
6. A firing rule with `mode: serverCheck` marks the decision as requiring an
   online check **when the winning effect is allow**: local PDPs MUST NOT
   act on that allow alone. They call `POST /v1/decide`; if the platform is
   unreachable the decision is `deny`, regardless of grace TTL. A
   locally-computed deny is final and needs no online check.
7. A firing rule with `mode: classify` marks the decision as requiring a
   classifier verdict **when the winning effect is allow**: PEPs MUST obtain
   the verdict from their configured Classifier before acting on that allow;
   an unavailable classifier, an error, or a blown deadline is a `deny`
   (fail-closed, the same posture as §2.6). A locally-computed deny is
   final and needs no verdict. The shipped backend is the built-in heuristic
   (no model, no configuration). A classify escalation is exempt from the
   hook p95 < 25 ms budget BY DESIGN and is meant to be rule-scoped:
   operators aim it at interpreter invocations and sensitive roles, not at
   `git status` (see https://docs.straza.ai/concepts/how-straza-works/).
8. A firing rule with `mode: approve` marks the decision as requiring a
   **human decision** before acting, again **only when the winning effect is
   allow**. The decision carries the rule's `approve` block (its normalized
   form, see below); PEPs resolve the gate through the approval service, and
   a timeout, an unreachable service, an unmappable approver, or an explicit
   denial is a `deny` (fail-closed, the §2.6/§2.7 posture). A locally-computed
   deny is final and never carries the marker. There are **two enforcement
   lanes** with two blocking shapes: the gateway lane (`mcp.call`) holds the
   upstream call until the request resolves or `timeoutSeconds` elapses, or
   until the platform's hold cap answers first with a pending reason and the
   same retry path (`approval.gatewayHoldSeconds` in the reference server,
   120 s by default); the hook lane (local tools) answers `deny` immediately with a `retry after
   approval` reference. On both lanes one approval runs **at most one** call
   (revision 23): the held call it was raised for, or one identical retry
   keyed `(user, session, ruleId, argvHash)` within `retryTTLSeconds` of the
   decision, whichever uses it first, without re-prompting the human, and
   nothing beyond that one run. The held call was already waiting when the
   approval landed, so `retryTTLSeconds` does not bound it, and a held call
   never uses a ticket's grant. The use is one atomic server-side write on
   the approval record, so it holds across replicas and restarts. The `approve`
   block configures the gate: `roles` names the approver roles (resolved on
   the identity plane at decision time, not baked into the snapshot; since
   revision 15 every name MUST be an approver-kind role or `straza-admin`,
   refused otherwise at validate and activation);
   `deciders` (revision 13) adds decider KINDS resolved per record at
   request time, today `sponsor` (the requester's accountable human,
   persisted onto the record as `approver_users`), composable with `roles`;
   an **empty or omitted pool (`roles` and `deciders` alike) defaults to
   `deciders: [sponsor]`** (revision 14): the record routes to the person
   behind the agent (revision 19), which is the requester's sponsor, or
   the requester when that requester is a person with no sponsor, whose
   record is then a confirm record. When an agent has no usable sponsor,
   or a person's recorded sponsor is unusable, the request is DENIED AT
   REQUEST TIME with the cause and fix in the reason (no pending record; a
   fleet never waits on an approval nobody will see); `timeoutSeconds`
   (default 90, range
   1..3600) bounds the wait; `selfApproval` (default false) governs whether
   the requester may decide their own request; `retryTTLSeconds` (default 60,
   range 1..600) is how long after the decision a retry may use the
   approval. The `approve` block is legal
   ONLY alongside `mode: approve` or `mode: confirm` (item 8a; under
   `confirm` the pool fields are rejected). An approve escalation is human-latency by
   definition and, like classify, is exempt from the hook p95 budget and
   meant to be rule-scoped (see https://docs.straza.ai/concepts/how-straza-works/).

   **Classes (`class`, revision 6).** `class` selects the approval shape,
   **default `hold`**, so every pre-revision-6 rule keeps its exact
   semantics:
   - `hold` is the shape described above: a bounded blocking wait, expiry =
     deny, and at most one run per approval. `timeoutSeconds` and
     `retryTTLSeconds` apply; the ticket knobs are ignored.
   - `ticket` is a **day-scale request** whose grant a **later (possibly
     fresh) session** consumes. It does not block: the PEP answers immediately
     (deny-with-ticket) and a later matching call consumes the grant. Two
     clocks replace the blocking wait: `ticketTTLSeconds` (default 86400 = 24 h,
     range 1..2592000 = 30 d) is the decision window (the request expiry:
     `ExpiresAt = createdAt + ticketTTLSeconds`, swept to `expired` = deny like
     any pending request); `grantTTLSeconds` (default 3600 = 1 h, range
     1..86400 = 24 h) is the post-approval consume window: a ticket is
     consumable only while `state == approved AND consumed_at IS NULL AND now <
     decidedAt + grantTTLSeconds`. The grant is **DB-durable, single-use, and
     session-agnostic**: it follows the requester (`user`) + the approved
     action, so a fresh execution session can cash a grant a planning session
     raised. `bind` selects how a later call re-matches
     the grant. `fingerprint`, the default, binds the grant to exactly this
     call's EventKey, the mutate-after-approve defense. `predicate` is
     reserved (revision 22): it is accepted and carried on the decision
     verbatim, but a PEP MUST treat it as `fingerprint`, so the grant is
     consumed only by a later call with the same EventKey. A future wider
     re-match gets a new value. For an `mcp.call` whose arguments change on
     every call, `binding: tool` is the supported loosening.
     Under `class: ticket` the blocking-wait knobs are inapplicable and setting
     `timeoutSeconds`/`retryTTLSeconds` is **rejected at validation** (the same
     author-mistake posture as "approve block requires mode: approve").
     Consumption is a server-side atomic write, so an offline client can never
     cash a ticket. It denies (fail-closed). Since revision 23 the use
     of a hold's approval is the same write.

   **Fingerprint coverage (`binding`, revision 7).** `binding` selects what
   the approval fingerprint covers for `mcp.call` events, **default `call`**
   (the secure default):
   - `call` folds the call's canonicalized arguments (hook-profile `args`)
     into the key: sorted keys, no inter-token whitespace, normalized
     numbers, the injected `_straza_justification` dropped at the top level,
     so retry/key-order/formatting noise never re-prompts a human, while ANY
     real argument difference does. Arguments too ambiguous to canonicalize
     (duplicate keys, malformed JSON) have NO fingerprint: the PEP MUST
     deny (fail-closed). A call whose arguments the server never observed
     (an older client that does not forward `args`) keys at tool scope:
     the fingerprint MUST NOT pretend to bind what was not seen, and the
     `binding_scope` wire field tells the per-record truth.
   - `tool` covers App+ToolName only: an explicit, policy-diffable
     loosening for high-frequency benign tools.
   `binding` is consulted for `mcp.call` only; `shell.exec` and described
   commands always bind the concrete call. It applies to BOTH classes (hold
   approvals and ticket grants alike).

   **Notification routing (`notify`, revision 8).** `notify` lists the
   channels that announce this rule's approvals: members `console`, `slack`,
   `push`; **empty/omitted = every configured channel** (the pre-revision-8
   default, and the recommended one: a missed nudge costs more than a double
   one). It narrows the ANNOUNCEMENT lanes only (the create nudge, the
   near-expiry ticket reminder, the terminal status): eligibility to decide
   stays `roles`/`selfApproval`, and the console/CLI/admin-API decision
   surfaces plus any already-posted card's callback keep working regardless.
   `console` is the always-on pull surface (it names no push notifier), so
   `notify: [console]` alone silences third-party notification; a listed but
   unconfigured channel has nothing to deliver and the console floor stands.
   Unknown members and duplicates are **rejected at validation**: a typo
   must never silently widen back to all-configured.

8a. A firing rule with `mode: confirm` (revision 11) marks the decision as
   requiring the **requester's confirmation** before acting, again only on
   a winning allow. It is the sudo posture: the human driving the session
   proves presence and intent; it defends against agent error and prompt
   injection, deliberately not against the human. The decision carries the
   same normalized `approve` block (classes, binding, notify, timeouts all
   apply identically) plus `confirm: true`; PEP behavior on both lanes is
   byte-for-byte the approve flow, so clients need no new machinery. The
   differences are decide-time and routing: only the requester may decide
   (any other identity, root included, is refused: the record proves the
   requester's presence, which nobody else can supply), non-human
   identities never decide (unchanged), decide-notification targets the
   requester alone (only the requester is notified), and expiry is deny. The
   decider pool is not configurable: `approve.roles` and
   `approve.selfApproval` under `mode: confirm` are **rejected at
   validation** (the requester is a fact on the record, not
   configuration). Engine invariant, the revision-9 clamp's sibling: an
   autonomous-agency subject turns a confirm rule into a plain **deny** at
   evaluation, because no human exists to confirm and a minted record
   could only expire. The three postures in one view: `approve` +
   `roles` = four-eyes; `approve` + `roles` + `selfApproval: true` = peer
   (requester included in the pool); `confirm` = the requester alone.

## 3. Combining

All firing verdicts from all applicable sets are combined:

1. **Explicit deny overrides allow.**
2. The reported `ruleId`, `reason` and set come from the highest-priority
   firing rule of the winning effect (ties: lexicographic set name, then
   rule order). When an approve rule fires on the winning allow, the
   reported `ruleId`, `reason` and set are that rule's whatever its
   priority, because the hold is what the caller experiences and the
   approval record must name the rule that mandated it (revision 18).
3. If no rule fires, defaults apply:
   - `mcp.call` carrying `granted: true` → **allow** with the reason
     `allowed by role access. No policy rule gates this tool.` (revision
     17). The fact means the subject holds a role whose `tool_bindings`
     row admits the tool; only the gateway, the catalog overlay, the
     admin catalog preview and policy simulate set it, each after its
     own binding check. The hook lane never sets it, `/v1/decide` zeroes
     it on ingress, and the built-in `straza` app carries it only on
     `straza__draft_submit` and `straza__draft_status`, for a subject
     holding `straza-draft-config` (revision 20).
   - `mcp.call` without the fact → **deny** (an ungranted tool is invisible
     on the gateway; the reason names the missing access).
   - local tools (`shell.exec`, `file.*`, `net.fetch`, `task.spawn`,
     `other`) → the profile default (`allow` standalone, `deny` enterprise).
     A tool outside the taxonomy and an event with no tool take the same
     profile default. No rule can name them, so where the default denies,
     the reason lists the known tools (revision 24).
   - non-tool events (`session.start`, `prompt.submit`, `tool.post`,
     `subagent.*`, `session.end`, `compact.pre`) → `allow` (audit-only).
4. An event whose kind is not one of the canonical kinds is denied in
   both profiles, before any rule is read, because no rule can name such
   a kind. The reason lists the known kinds (revision 24).

Decisions are `{effect, ruleId, reason, obligations[], serverCheck,
classify, approve}`. `approve` (the winning rule's normalized `approve`
block) is present only on an allow gated by `mode: approve`.
`obligations[]` is always empty since revision 18: the `redact` and
`notify` obligations never ran on any lane and are retired, not
implemented. A document that carries an `obligations` list is refused at
validate and at activation (400 naming every offending rule and the fix).
Stored sets that carry the list keep parsing and keep governing, with one
boot WARN per active set until their next activation. The field stays on
the wire for older clients, which read an empty list. Human approval ships
as the `mode: approve` rule escalation (§2 item 8), never as an obligation.

## 4. Patterns

- Patterns are globs unless prefixed `re:`, which denotes a Go regular
  expression; both are **anchored** (must match the whole subject).
- Glob syntax: `*` matches any run of characters except `/` in path
  patterns (and any run of characters in command/toolName patterns);
  `**` in path patterns matches across `/`; `?` matches one character.
- **Command matching** runs the pattern against (a) the raw command string,
  (b) the argv re-joined with single spaces, and (c) every individual argv
  token. Any hit counts (defeats trivial quoting/whitespace tricks). Argv
  is produced by a POSIX-ish word splitter honoring `'`, `"` and `\`.
- **Path matching** runs against lexically cleaned paths: separators
  normalized to `/`, `.`/`..` segments resolved, `${workspace}` in the
  pattern substituted with the session's workspace root before compiling.
  Relative event paths are resolved against the workspace first.
- **Interpreter matching** (`interpreters`, revision 3) runs the same
  pattern language against the canonical event's `interpreter` attribute
  (see `spec/hook-profile`): the detected interpreter basename a
  `shell.exec` event invokes (`python3`, `bash`, …). The block is consulted
  ONLY when the event carries a non-empty `interpreter`; deny side wins as
  everywhere.
- Matching is case-sensitive except drive letters on Windows paths, which
  are lowercased during cleaning.

## 5. Rego escape hatch

`spec.escape.rego` embeds an OPA module evaluated AFTER declarative rules.
It may only tighten: the platform queries `deny` rules only, and compilation
MUST reject modules that declare `allow` (or `default allow`) rules. Input
document: `{event, subject, decision}` where `decision` is the declarative
outcome. Any non-empty `deny` result flips the decision to `deny` with the
returned message as reason.

A module MUST NOT reach beyond its input document, and the escape modules
that apply to one decision share one deadline of 100 ms (revision 21).
Compilation MUST reject a module that calls any of these built-ins, and the
refusal names the set, the built-in, the line of the call and why:

- `http.send`, `net.lookup_ip_addr`, `json.match_schema`,
  `json.verify_schema` and `opa.runtime`, because each can reach the
  network, the file system or the process environment.
- `strings.render_template`, `rego.parse_module`, `graph.reachable_paths`,
  `bits.lsh`, `net.cidr_contains_matches`, `glob.match` (revision 25),
  `graphql.is_valid`, `graphql.parse`, `graphql.parse_and_verify`,
  `graphql.parse_query`, `graphql.parse_schema` and
  `graphql.schema_is_valid`, because a single call of each can run far past
  the deadline, and the graphql parsers and `glob.match` can end the
  process on deeply nested input.

Every other built-in of the platform's OPA version stays available. The
platform checks the deadline between evaluation steps and cannot stop a
built-in call that is running, so a call of an allowed built-in on a very
large value, such as `regex.match` over a string of many megabytes, can
still end past the deadline. A decision whose modules run past the
deadline is `deny` with a reason that names the set. It is never an allow.

## 6. Conversation capture (revision 2)

`spec.capture` opts the set's matched sessions into conversation capture:

```yaml
capture:
  conversations: true
  mode: verbatim   # or redact; verbatim is the default
```

Semantics:

- Capture is **off** unless at least one set whose `match` applies to the
  session declares `conversations: true`. The IGA plane decides *whose*
  conversations are recorded; the session's snapshot carries the directive,
  so a governed client can see (and a governed agent can be told) that
  capture is active, with no silent surveillance of un-notified principals.
- `mode: verbatim` (default) records content as-is; `redact` obliges the
  client to mask recognizable credential patterns before spooling. When
  matched sets disagree, **redact wins**: a mandated minimization MUST NOT
  be overridable by a looser set.
- `mode` without `conversations: true` is rejected (inert configuration).
- Captured turns ride the audit pipeline as `straza.audit.prompt` /
  `straza.audit.reply` (spec/events revision 5): content capped per event,
  truncation marked with the full-content SHA-256 so the hash chain
  witnesses what it did not store.

## 7. Change control and versioning

Per the additive-change rule in the spec README every change here lands
with schema + fixtures + CHANGELOG + version bump in one PR.

New documents MUST declare `apiVersion: straza.dev/v1beta1`. The
pre-release ids are a clean break (2026-07-21, the namespace rename) and
are NOT accepted, with no deprecated grace.
