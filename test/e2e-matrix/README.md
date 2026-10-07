# e2e-matrix: spec-authored journeys against the real binaries

The unit suite proves each part of Straza against a model of its neighbours.
This lane proves the journeys an operator actually walks, with nothing
modelled: a real `strazad` built from the tree, the real `straza` client
deciding every hook event in each harness dialect, the real gateway, the
real admin and SCIM APIs, and the audit chain read back at the end. It needs
no model, no key, no docker and no network beyond loopback, so it runs on a
laptop in about a minute and on a free CI runner as the pull-request gate.

The scenarios are written from the published contract, never from the code.
A scenario cites the published page or the spec section it proves, and the
corpus author does not open `internal/`. The published contract is
everything under `spec/`, the OpenAPI document in `pkg/api`, and the pages
of the docs site, the security model's invariants among them. That rule is
what makes this lane a guardrail: the implementation cannot grade its own
homework.

## Run it

```
make e2e-matrix                              # build, boot, run every scenario, report
STRAZA_E2E_MATRIX=1 go test -count=1 -run TestJourneys -v ./test/e2e-matrix
STRAZA_E2E_SCENARIOS=j0-golden-path make e2e-matrix   # comma-separated subset by id
```

Without `STRAZA_E2E_MATRIX=1` the boot-and-run test skips with a printed
reason, so `go test ./...` in the commit gate stays fast. The always-on tests
in this package parse every scenario file and every dialect renderer and
refuse an empty corpus, so a broken scenario fails at commit time instead of
being skipped at run time.

The report lands in `.work/report.md` next to this file, one row per
scenario and harness, plus the timers table. CI uploads it as an artifact.

## What a run does

1. Builds `strazad` and `straza` from the tree into `.work/bin` with
   `CGO_ENABLED=0`, the same way a release is built.
2. Reserves a free loopback port, starts `strazad serve --profile
   standalone` on it with a data directory under the test's temp dir, reads
   the one-time bootstrap admin password from its own log, and waits for
   `/readyz`. The cold-start timer starts at spawn and stops at the first
   ready answer. A scenario that declares `governance` lifetimes gets them
   written into a config file passed with `--config`, because the offline
   grace bound has no environment face by design.
3. Signs in as the bootstrap admin through the built-in issuer's device
   flow, exactly as an operator's `strazactl login` would, and mints an
   admin API token with the SCIM grants so scenarios can act as an IdM.
4. Starts the hermetic MCP upstream the `mcp` steps and the app manifests
   point at through `${upstream}`. It serves three tools: `echo` returns its
   `text` argument, `get-sum` returns the sum of its numeric `a` and `b` as a
   decimal string, and `get-env` takes no argument and returns the fixed
   text `E2E_UPSTREAM=1`. The server is started with a two second gateway
   hold for approve-mode calls, so a held call answers "approval pending"
   within seconds instead of the production default.
5. Runs the positive control on every dialect: a policy that must deny is
   checked to deny through the real `straza hook` before any scenario
   counts. A dialect whose control passes silently would make every green
   row below it worthless, so a failed control fails the run.
6. Runs each scenario file in `corpus/` in name order, once per dialect it
   lists, and boots a fresh `strazad` and upstream for every run, repeating
   steps 2 to 4, so runs never see each other's rows, names or sessions.
7. Stops every `strazad` and upstream by process id and writes the report.

The bootstrap password, the admin token, the IdM token and every session
token stay out of the test log and the report. CI logs on a public repository
are public.

## Scenario files

One YAML file per scenario in `corpus/`, named `<group>-<slug>.yaml`.

```yaml
id: j0-golden-path
group: J0
title: A provisioned human and an enrolled agent work through policy, approval and deprovisioning
provenance: doc website/content/security/security-model.md, spec/scim-profile/SPEC.md §Groups, spec/policyset/SPEC.md §approve
harnesses: [claude-code, codex, gemini, python-sdk]
identities:
  alice: {kind: human, password: "Walk-2026!"}
  agent-1: {kind: nhi}
steps:
  - name: the IdM provisions alice
    scim: {method: POST, path: /scim/v2/Users, body: {...}}
    want: {status: 201}
    save: {aliceId: id}
  - name: alice enrolls her laptop
    enroll: {as: alice}
  - name: a destructive command is refused with the reason
    hook:
      as: alice
      event: {kind: tool.pre, tool: shell.exec, command: "rm -rf /work"}
    want: {decision: deny, reasonContains: "recursive"}
  - name: the refusal is on the chain exactly once
    audit: {subject: alice, kind: tool.pre, decision: deny, reasonContains: "recursive", count: 1}
```

`harnesses` lists the dialects the scenario runs under. The whole scenario
runs once per dialect, in order, and every run gets its own `strazad` on a
fresh data directory, its own upstream, fresh identities and a fresh
`STRAZA_HOME` per identity, so nothing a run creates can reach the next one.
The report carries one row per scenario and dialect. An `audit` count
therefore counts one run, and an `mcp` step uses the session that run's
last `session.start` opened.

`identities` declares who takes part. A human identity carries the password
the built-in issuer will accept; the scenario itself creates the user
through the admin or SCIM API in a step, and that step must set the same
password through the admin API, since the SCIM profile does not carry
passwords. An NHI identity gets a local key from `straza keygen` and a
headless enrolment.

`provenance` names what the scenario proves, with the same `doc` and `live`
tags the conformance corpus uses. The runner refuses a scenario without it.
An optional `suspect` list holds one sentence per step whose expected value
the sources left open, so a red on that step is read as a documentation gap
first.

An optional `governance` map shortens the lifetimes the scenario's `strazad`
boots with, as duration strings under the config file's own keys:
`offlineGraceTTL`, `deviceTokenTTL` and `sessionMaxLifetime`, and nothing
else. A scenario about the clock sets them to minutes so a token refresh, a
credential renewal, a lifetime close or a grace window happens inside the
run. The runner refuses any other governance key, so a scenario can never
change the posture the binary decides under without the reader seeing it.

```yaml
governance:
  offlineGraceTTL: 2m40s
  deviceTokenTTL: 6m45s
  sessionMaxLifetime: 3m45s
```

Two bounds the binary fixes shape such a scenario. The session token lives
300 s and the client refreshes it inside its last 120 s, so a daemonless
refresh lands about three minutes after the session start. The admin token
the steps act with is the ten-minute login token; a `server: {action: start}`
step signs the admin in again, so a scenario that runs longer than that
restarts the server somewhere inside the ten minutes or ends before them.

## Steps

Every step has a `name`, exactly one action key, and usually a `want`. Two
optional keys apply to any step:

- `save: {<name>: <json path>}` stores a value from the response body, for
  example `save: {appId: id}`, `save: {ref: approval.id}` or `save: {ref: 0.id}`
  on a bare array. Later steps use it as `${appId}`. The runner also provides `${server}` (the strazad base
  URL), `${upstream}` (the hermetic MCP upstream URL) and `${bin}` (the
  directory holding the built binaries).
- `within: <duration>` repeats the step until `want` holds or the deadline
  passes, and records the elapsed time from the end of the previous step
  under `timer: <name>`. The cadence is 100 ms up to a 20 s wait and one
  two-hundredth of the wait above that, so a four-minute wait polls about
  every 1.2 s: a clock-driven hook step would otherwise spawn thousands of
  hooks whose records flood the chain. This is how propagation is measured:
  an admin step changes something, and the next step carries `within` and a
  `timer`.

Actions:

| Action | Fields | `want` |
|---|---|---|
| `admin` | `method`, `path`, `body` (an object sent as JSON, or `yaml:` a string sent as `application/yaml`, which PolicySets and app manifests use), sent with the operator's login token from the device flow, or with the login token of the person named under `as: <identity>` | `status`, optional `body: {<json path>: <value>}` where a `null` value asserts the path is absent, which is how a scenario says a list has exactly so many items, and optional `bodyContains: <substring>` of the raw answer |
| `scim` | `method`, `path`, `body`, sent with the IdM API token the runner minted with the SCIM grants | `status`, optional `body` and `bodyContains` as above |
| `enroll` | `as: <identity>` | none; an enrolment that does not complete fails the step |
| `approver` | `as: <human identity>` and `action`. `action: enroll` takes `token`, an enroll token that an earlier `admin` step minted as that person with `POST /v1/approvals/self/enroll-token` and saved. The runner makes a P-256 key for the run and enrolls it as a browser, the way the self-service page does. `action: decide` takes `request`, `verdict: approve` or `deny` and an optional `reason`. The runner fetches the single-use challenge, signs the decision with the enrolled key and posts it. A decide takes no `within`, because a repeat after a success answers 409 | `status`, optional `body` and `bodyContains` as above. An enroll answers 201. A decide answers 200 with `state`, and the record's decider, channel and words are read with an `admin` step |
| `daemon` | `as: <identity>`, `action: start` or `stop`; the `straza daemon` that holds the push stream for that identity | none |
| `server` | `action: stop` or `start`; stops or restarts `strazad` on the same port and data directory | none |
| `hook` | `as`, `event` (a canonical event, below) or `payload: {<dialect>: <raw payload>}` for a dialect-specific case | `decision: allow`, `deny` or `block`, `silent: true`, `reasonContains`, `contextContains` for `session.start` |
| `mcp` | `as`, then `list: true` or `tool` with `args`; the call goes through the gateway with the session the identity's last `session.start` hook step opened, so one precedes it | `tools` and `notTools` for a list; `result` (a substring of the tool result) or `refused: true` with `reasonContains` for a call |
| `audit` | `subject`, `kind`, `action`, `decision`, `reasonContains`, `count`; matched against rows since the scenario started, `kind` against the event type or the hook event, `action` against the record's action such as `signing-keys.rotate` | the fields are the assertion; `within` defaults to 10 s because the audit drain is asynchronous |
| `wait` | a plain duration written in place of a map, such as `wait: 8s` | none; a wait records nothing |

A step that decides an approval names the person whose verdict it is under
`as`. The decide routes take a person's own session or ID token and refuse a
token that carries no user, so the operator's token can answer only the
records the operator may decide, and a scenario about four eyes, a
self-approval, a confirm gate or a sponsor says who acts. The runner signs
that person in through the built-in issuer's device flow with the password the
scenario declared, which is the same ten-minute login token the operator's
steps carry, and keeps it for the rest of the run. Only a human identity may be
named: a scenario that names an NHI is refused at load, because a non-human
identity never decides.

`wait` and `within` answer two different questions. A change that propagates is
polled with `within` and timed, because the poll observes the moment it lands.
A window that lapses has nothing to poll for, and polling it would spend the
very allowance the scenario is letting expire, so the scenario sleeps through
the window with `wait` and proves the lapse with the call that follows. A wait
takes no `want`, `save`, `within` or `timer`, and it adds its whole duration to
the run: the two lapse scenarios set five second windows and sleep eight
seconds each, so each of them costs about ten seconds more than a scenario
without one.

The `hook` decision judgement is not restated here. The runner applies the
per-dialect rules written in the header of `spec/conformance/tier1/cases.yaml`:
what counts as a deny on claude-code, codex and gemini, where the reason must
appear, and which events demand a silent ack. A scenario that needs a
different judgement is wrong, not the rules.

## Canonical events

A `hook` step describes the event once, in the vocabulary of
`spec/hook-profile/SPEC.md`, and the runner renders it into each dialect's
native payload. The renderers are written from `spec/hook-profile/mappings`
and the payload corpus under `spec/conformance/hooks`, never from the
client's normaliser.

| `kind` | Extra fields |
|---|---|
| `session.start` | none; `want.contextContains` checks the injected context |
| `prompt.submit` | `prompt` |
| `tool.pre` | `tool` and the tool's fields below |
| `tool.post` | `tool` and the tool's fields, plus `output` |
| `session.end` | none |

| `tool` | Fields |
|---|---|
| `shell.exec` | `command` |
| `file.read` | `path` |
| `file.write` | `path`, `content` |
| `mcp.call` | `app`, `toolName`, `args` |

A scenario that must send something no canonical event can express, for
example a malformed payload for a fail-closed case, uses `payload` with one
raw document per dialect and runs only under the dialects it names.

## Timers

The timers table in the report has one row per timer name and dialect, with
the observed time and the project's performance budget where one exists.
Budgets are printed, not enforced, in this lane: shared CI runners jitter,
and the perf lane owns enforcement on reference hardware. A timer with no
published budget is reported so the number is known.

| Timer | Measures | Published budget |
|---|---|---|
| `cold-start` | `strazad` spawn to the first ready answer, the worst of every boot in the run | under 1 s |
| `hook-overhead` | spawn to exit of every single-shot `hook` step that decides, which is `tool.pre`, `tool.post` and `prompt.submit` without `within`, reported as p50 and p95 per dialect; a polled hook step is a wait and its attempts, some of which refresh or re-acquire over the network, are not samples | p95 under 25 ms |
| `session-start` | spawn to exit of every `session.start` hook step, which includes the check-in round trip, worst per dialect | none published |
| `kill-switch-gateway` | SCIM deactivation or session revoke to the first refused gateway call | none published; the target is under 5 s |
| `kill-switch-hook` | the same, to the first refused hook decision with the identity's daemon connected | p99 under 2 s |
| `policy-gateway` | PolicySet activation to the first gateway call decided by it | none published |
| `policy-hook` | PolicySet activation to the first hook decision made under the new snapshot, daemon connected | none published |
| `catalog-unbind` | binding removal or role removal to the tool disappearing from the identity's gateway list | none published |
| `approval-retry` | admin approval to the retried call being allowed | none published |
| `policy-pulse` | PolicySet activation to the first daemonless hook decision made under it, on a session busy with decisions | none published; the design bound is at most one call plus `snapshotLagSeconds`, 30 s by default |
| `refresh-adopt` | a role assignment to the first daemonless hook decision made with the role, which the session learns only at its refresh inside the token's last 120 s | none published; the design puts it between 180 s and 300 s after the session start |
| `reacquire-adopt` | a second role assignment to the first daemonless hook decision made with it after the janitor closed the session, which the session learns when its refused refresh falls back once to the enrolment lane | none published |
| `grace-exhausted` | the server stop to the first hook deny past the token's expiry plus the offline grace bound | none published; the bound is the configured `offlineGraceTTL` past expiry |
| `ticket-expiry` | a ticket raised on a five second window to its record reading expired | none published; the sweep runs every 15 s, so the design puts it under 20 s |

Without a daemon the hook lane learns about a change on two clocks, both
published. A policy change arrives through the snapshot pulse the detached
drain fires after every decision, at most once per `snapshotLagSeconds`, so
a busy session is at most one call plus 30 s behind the active policy and
an idle one learns it at its next decision. An identity change, such as a
role, arrives only with a check-in: the refresh the client schedules inside
the last 120 s of the session token's life, a re-acquire, or a session
start, because roles are resolved on the server at check-in and the pulse
carries only the snapshot. The propagation scenarios measure the
daemon-connected path and prove the no-daemon path by the session-start
step that follows; the clock scenario measures both no-daemon clocks under
`policy-pulse` and `refresh-adopt`.

## The clock scenario

`g9-clock-lifetimes` boots its stack with minute lifetimes and lets four
clocks run at once, on the claude-code dialect alone, because the clocks
live in the client's session logic and in the server, not in the payload
shape the other groups prove on every dialect. It runs about eight
minutes and proves, in one run: a policy change reaches a busy
daemonless session through the snapshot pulse; a role given after the
session started reaches it only at the refresh inside the token's last
120 s; the janitor closes every session at its lifetime with a chained
record; a refused refresh falls back once to the enrolment lane on the hook
lane and on the daemon, the device credential presented there is renewed
once past half its life, and a second role arrives with that check-in; an
idle credential still expires; an expired token is refused at the gateway;
a stopped server leaves the hook deciding locally through the grace window
and denying past it with the published wording; and a signing key staged
under live sessions breaks none of them, every later refresh presenting a
token the retiring key signed. What it cannot prove from outside is that
the promoted key is the one signing, because nothing readable through the
API shows a key's status, and the old key's retirement, which is a month
away because the approver token lifetime bounds it.

## Scope

In this lane today: the golden path (J0), session lifecycle (G1), the
fail-closed matrix (G2), policy and catalog behaviour (G3), approvals
beyond one hold (G4), tickets and grants (G5), delegated server
administration (G6), server-owned roles and the one-server rule (G7), each through the four hook
dialects where a hook step applies, and the clock-driven lifetimes (G9) on
one dialect.

The approvals group walks a hook-lane hold through approval, the
single-use retry, a denial, a repeated and a conflicting decision, expiry
and the state filter on every dialect, and on one dialect proves decider
routing, an unsponsored agent denied at request time, and the gateway's
answers after a denied and an expired hold. The tickets group raises a
ticket on the hook lane on every dialect, approves it, consumes it once
from a fresh session of the same person, keeps it blocked after a denial
without a new request, expires it on the sweep with the next call asking
again, and refuses the knobs a ticket cannot carry at apply; on the
gateway, on one dialect, a grant binds the exact canonicalized call by
default, so a changed justification consumes it while a changed argument
opens a fresh ticket, and `binding: tool` lets a call with other arguments
consume it.

Delegated server governance runs on one dialect, because both of its rungs
live in the server rather than in the payload shape the other groups prove on
every dialect. A server admin changes the one server whose admin role she
holds and is refused everywhere else (`g6-delegated-server-admin`). She also
defines a role that server owns over two of its tools, a team reaches those
tools through a business role composed onto it, and she is refused when she
binds a role her server does not own, points her own role at the other
server, hands the role to a person, or deletes it while somebody holds it
(`g7-server-owned-roles`). Removing the server ends that membership with a
record. An application role belongs to the one server it reaches: the
operator's second server for a server's own role is refused, a role that
belongs to no server is refused every server with the way out, and a team
reaches two servers through a business role composing one role per server
(`g7-one-server-per-role`).

Six scenarios about who decides, and about a window closing, run on one
dialect, because both live in the server rather than in the payload shape the
other groups prove on every dialect. A rule that names an approver role is
decided by that person and refused to the requester, whose retry then runs
once (`g4-four-eyes`). A rule that sets `selfApproval` lets the requester
decide her own request, while a second rule of the same set refuses the same
person, which is the self-exclusion and not a missing role
(`g4-self-approval`). A confirm rule is decided by the requester alone and
refuses both a second person and the bootstrap admin, and a decider pool
written under that mode is refused at apply (`g4-confirm-mode`). A sponsor
decides the hold her agent raised, and the agent's retry runs once
(`g4-sponsor-decides`). Two more let a window close instead of using it: an
approved hold whose retry exemption lapses makes the identical call ask again
(`g4-retry-exemption-lapse`), and an approved ticket whose grant nobody
consumes lapses the same way, leaving a record that never gains its consume
fields (`g5-grant-window-lapse`).

Not in this lane: the sentence a gateway
caller reads when a denial lands inside the socket wait, because no step
decides during another step's wait; arguments too ambiguous to
canonicalize, because the `mcp` step sends a JSON object; argument
redaction, the phone approver contract, an external issuer or an IdM
product, the helm install lifecycle, cross-harness concurrency, and any
live model. The ollama lane in `test/agent-e2e` covers model behaviour and
`test/harness-matrix` covers the real vendor CLIs.

## Reading a red row

A red row is one of two things. Either the scenario misread the contract,
and the fix is in the scenario with its `provenance` line corrected, or the binary
disagrees with the contract, and that is a product bug to report as an
issue. The scenario is never bent to make the binary pass, and the spec is
never edited from this lane.
