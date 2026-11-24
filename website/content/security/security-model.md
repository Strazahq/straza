---
title: The security model in depth
description: For a security reviewer or an auditor. The boundaries a call crosses, the eleven invariants with what each one costs, and the mechanism behind five limits.
pagetype: explanation
weight: 10
draft: false
summary: >-
  Six boundaries stand between an agent and your systems, from the agent's machine to the audit
  record. Eleven invariants hold the design, and each one has a cost that this page names. Five limits are explained with
  their mechanism and with what you can do today.
keywords: security model invariants threat residual risk fail closed known limits
---


Straza protects the actions an agent takes under an identity. A session does only what its roles allow, every decision leaves a record that cannot be edited in place, and a revoked identity is refused. [The security model]({{< relref "evaluate/security-model.md" >}}) gives the short answer, and this page gives the mechanism behind it.

## The boundaries, in the order a call crosses them



### The agent's machine

A managed install keeps the configuration and the hook wiring in root-owned system paths, and the package installs the binary where only an administrator can change it. An agent without root therefore cannot switch the hook off. On such an install `straza service` runs as a system service. It decides each call under its own OS account and keeps the person's key and sessions out of reach of the person's processes. A hook that a user installed can be removed by the agent, and by default an enterprise server gives such an install no session.

The client measures the hashes of its files. When a session starts, strazad compares them with the hashes registered for that coding tool, which by default cover the hook wiring alone. A mismatch or a missing measurement makes the session attest as `none`, and under the enterprise default that means no token. Each refresh keeps the level the session started with, so a change made during a session shows at the next session start. Under a lower minimum, such as the standalone default, a policy can still require the `managed` level for a sensitive role. The hashes are the client's own report, as [The attestation level comes from the client](#the-attestation-level-comes-from-the-client) explains.

### The hop to strazad

Session tokens and admin API tokens cross it, so it is TLS or a network you trust. strazad serves TLS 1.2 or newer from a certificate pair you give it, the enterprise profile warns at boot when it serves plaintext, and no setting skips certificate verification. The standalone profile binds its main listener to 127.0.0.1:8420 and gives no such warning, so rebinding it to a routable address is the moment to add TLS. Unless `server.approverTLS.autoMint` is false, it also serves the phone approver routes on port 8443 on every interface, with a self-signed certificate it creates in the data directory. [Hardening]({{< relref "security/hardening.md" >}}) walks that step.

### The server

strazad verifies the session token against its own signing keys, an in-memory denylist and the attestation minimum, all without the database. The decision runs over a compiled policy snapshot in memory, and the roles come from a cache filled at check-in. After a restart that cache is empty and the token alone cannot prove roles, so the gateway refuses the call until the client checks in again.

### The gateway's hop to an MCP server

This boundary holds with no code on the agent's side. The credential a tool needs is bound to its server on the Straza side and sealed at rest. strazad injects it into the upstream request, or into the environment of a `command` or `oci` server, by name. It never passes through the argument vector or through any answer to a client. A server that needs a credential and has none fails closed. For a remote server Straza refuses to dial a link-local, cloud metadata or unspecified address, and the enterprise profile also refuses a loopback address unless `apps.allowLoopbackUpstreams` is true. Any other internal address in a manifest is the administrator's to review, until an egress allowlist per MCP server exists.

An MCP server that a person adds to their own coding tool never reaches the gateway. The hook decides each of its calls and denies them unless a rule allows that server, and on Claude Code a managed install loads only the servers in Straza's own file. Such a server's credential sits on the person's machine, so register every server that holds a credential in Straza.

### The sandbox image

The sandbox image is for a process without a hook. Its path holds the shim, the governed shell and the agent's interpreter. Every other file loses its execute bit at build time, and the build fails if one survives. The root filesystem is read-only, the writable mounts are noexec, capabilities are dropped and no process can gain privileges.

Two things in the image run without a decision. What the interpreter computes is not decided, because the interpreter is the agent. A binary on the allowlist runs without a decision too, so a rule on a tool's arguments holds only for a tool that is absent from the image. The shipped compose file leaves the container's network routable. The shape that holds is an internal network with only the agent, its Straza service and strazad on it.

### The record

Every decision is written to a hash chain after it is made, and [Audit records can be lost](#audit-records-can-be-lost) lists the cases where a record does not arrive. A sink you configure keeps a copy of every event off the box, where a database owner cannot rewrite it. [Evidence and audit]({{< relref "concepts/evidence.md" >}}) explains the chain and what it can and cannot prove.

## The object boundary


A server whose one credential reaches many objects, such as every repository of a GitHub organization, can pin an object type. Each role's access row on that server then says which objects the role covers. The gateway holds that line on every `tools/call` to the server, on `/mcp` and on the server's own endpoint alike.

The check runs after the tool check and the rate limit and before the policy engine. It reads the pinned declaration, the caller's held roles and this replica's copy of the object list from memory, so it adds no database read. A call that no held role covers is refused with a reason. It never reaches policy or a person's approval, and it leaves one `straza.audit.mcp` record that names the object and the held roles that bind the tool.

### How the gateway reads the object

The gateway reads the object from the one argument the declaration names. It refuses arguments that are not valid UTF-8, that repeat a key at any depth, that nest deeper than 128 levels, that carry the argument a second time under a spelling that differs only in case, or that send it as anything but a string. Each refusal removes a way for the server to read another value than the gate did. The value is then looked up byte for byte, with no trimming, case folding or decoding beyond JSON's own, so a spelling the list does not hold is an unknown object.

### What happens when the gateway cannot vouch for a state

Every such state narrows access:

- An access row on a pinned server without a covers line covers nothing, and a draft refuses to publish one. A row whose covers line Straza cannot read leaves the gateway altogether, with an error in the server log.
- A listing that fails, runs out of time or goes over a limit changes nothing, so the last good list stays, and the server's tab shows the reason.
- With `apps.objects.enabled` false, no listing runs and only a role on every object may call a tool that names an object, so turning the check off never widens access.
- A call that names an object the list does not hold gets the same refusal as an object no held role covers, so an agent cannot learn which objects exist. It also asks for one new listing of the server. Straza lists a server at most once a minute this way, so a new repository reaches the list in about two minutes when a refused call comes again after that minute, and otherwise at the next 10-minute listing.
- Tools the pinned declaration does not name count as reaching every object. So does a tool whose live mark the pinned reach no longer meets, names another type or no longer parses, until a person publishes the change.
- When a published change gives the server another type, listing tool or set of kept attributes, its list starts over and Straza lists it again at once. Until that listing lands, a role that names its objects is refused.

### What the server receives

The server receives what the gate checked. Straza forwards the arguments as the agent sent them and never rewrites them into a canonical copy, because that form would change how numbers are written, and the refusals above make the raw bytes mean one thing to every reader. On a call that waits for a person's approval, Straza first takes out the justification field the approval asked for.

When a role that names its objects grants a call, Straza adds the object's id from its list as `params._meta["ai.straza/object-id"]`. A server that keeps ids can then refuse a call on an object that was deleted and made again since the last listing. A call through a role on every object carries no id. Straza drops the `_meta` an agent sends, so that key always comes from Straza. The server's listing credential goes to its listing tool alone and never rides a tool call.

### What the grant binds

The role that grants a call is the first held role by name whose covers line covers the object, and the audit record and the credential broker both name it. An approval binds the object's type and value, so a ticket approved for one repository never runs a call on another, and the gateway checks coverage again when the ticket is used. The audit record of a call on a tool that names its object carries the object's type, reach, value and id. The attributes a listing keeps never leave the box in it.

Only the gateway holds this boundary. The hook's decision on the agent's machine never runs the object check, and a server a person adds to their own coding tool has none, so register every server whose objects matter behind the gateway. The boundary also rests on three promises the server makes about what its tools touch, which [Objects]({{< relref "concepts/objects.md#where-objects-are-checked" >}}) states.

## The invariants and what each one costs


| Invariant | Mechanism and cost |
|---|---|
| No decision reads the database | Decisions run on a compiled snapshot in memory, and tokens are verified locally. The cost is lag: a policy change reaches a client at its next check-in or on a push. |
| Session tokens validate themselves, and revocation is pushed | A token lives 300 seconds and is signed with Ed25519, and a revocation goes to an in-memory denylist on every replica. The cost is a window: a machine whose push stream is down learns of a revocation at its next renewal. |
| Audit is asynchronous | A decision's record goes through an in-memory queue to the database. A crash can lose the newest records, and on enterprise a full queue makes decisions wait and then refuses them. |
| The data plane is stateless | Any strazad replica can serve any session. A replica rebuilds a session's state from the token and a check-in, and every replica needs the same `secret.key`. |
| Upstream credentials never reach the agent | The gateway adds the credential to the call, and no API returns it. A tool that needs a credential therefore works only through a reachable gateway. |
| Every decision fails closed | An unknown state is a deny with a reason. An outage of strazad therefore stops every enterprise agent within five minutes. |
| Enforcement artifacts are tamper-evident | A managed install is root-owned, and its hashes are checked when a session starts. It needs administrator rights to install. |
| Snapshots and releases are signed | Clients verify each snapshot against keys pinned at enrollment, and releases are signed with cosign. A new snapshot key means that every machine pins the keys again. |
| The spec is the contract | Every wire format is in the published specification and changes with a version bump. Parsers are strict, so you upgrade clients before a policy uses a newer block. |
| The binary stays single and static | CGO is off, SQLite is pure Go and the message bus runs inside strazad by default. The cost is a narrower choice of libraries. |
| A process runtime never carries a caller's credential | The manifest parser and the resolver both refuse a per-person credential on a `command` or `oci` server. A server that needs each person's own token runs as a `remote` server or on that person's machine. |

Each fold below gives the mechanism and the cost of one invariant in full.

{{< details summary="No decision reads the database" >}}

Decisions use the compiled snapshot, and authentication uses locally verified tokens. A native tool of the built-in straza MCP server, such as the approval and drafting tools, reads and writes only its own records by id, after the decision to run it. A counting store in the test suite fails the build when a new repository is added without being classified, so the rule cannot erode one getter at a time.

The cost is lag. A policy change is a new snapshot that clients adopt at their next check-in or on a push. A server restart empties the in-memory cache of subjects, so every client checks in once more before its next gateway call succeeds.
{{< /details >}}

{{< details summary="Session tokens validate themselves, and revocation is pushed" >}}


Each session token lives 300 seconds and is signed with Ed25519. It is verified against a cached key set that carries the staged, active and retiring keys, so a rotation started with `strazactl signing-keys rotate session` strands nobody. Every replica holds the new key before any replica signs with it, and the old key verifies until every credential it signed has expired. The signing keys sit in the database sealed under `secret.key`, so a copy of the database taken since the first start of 2.3.0 signs no token. An older backup holds the session key unsealed until that key is rotated.

Revoking a session, a user or a device writes a record and adds the target to an in-memory denylist on every replica. A session entry is never lifted again.

The cost is a window. `straza service` holds a push stream for each live session, and with that stream connected a revocation lands in under two seconds on the [demo stack]({{< relref "evaluate/try-it-on-the-demo-stack.md" >}}). While the stream is down, the revocation reaches the hooks at the first call that renews the session, once 120 seconds or less of the token remain. That is at most about 180 seconds after the token was signed, in both profiles, for a session that keeps working. The gateway refuses the revoked token at once. Only a standalone service whose renewal cannot learn the server's judgment adds its offline grace, up to 1,200 seconds after the token was signed. That happens when strazad cannot be reached, or when something in front of it answers instead. [Credentials and sessions]({{< relref "security/credentials-and-sessions.md" >}}) follows a machine's credential from enrollment to lockout.
{{< /details >}}

{{< details summary="Audit is asynchronous" >}}

Server decisions enter an in-memory queue with room for 4,096 records before they are committed to the transactional outbox. A write that fails is tried again three times. After that, `governance.auditBackpressure` decides what happens to the record.

Under `block`, the enterprise default, strazad keeps trying the record for as long as the database cannot take it. When the queue is full, a new decision waits up to 25 seconds for room and is then refused with a reason, so no decision runs before its record is in the queue. Under `drop-with-counter`, the standalone default, the record is counted in `straza_audit_lost_total` and logged with its type and id. A record that the full queue cannot take is dropped and counted in `straza_audit_dropped_total`.

When strazad stops, it ends the event streams of connected clients and waits up to 10 seconds for the requests still running. It then writes what the queue holds for up to 5 seconds before it closes the database. Give it 30 seconds to stop, which leaves room above the 20 seconds a full stop can take. The cost is that a record can be lost in the cases that [Audit records can be lost](#audit-records-can-be-lost) lists.
{{< /details >}}

{{< details summary="The data plane is stateless" >}}


Any gateway instance can serve any session, and sticky routing is only an optimization. A replica never assumes the state it needs for a session. It rebuilds that state from the token and a check-in, and the cost is that fresh check-in when a session reaches a replica that has not seen it. Every replica mounts the same `secret.key`, and one that holds another key refuses to start.
{{< /details >}}

{{< details summary="Upstream credentials never reach the agent" >}}

Injection happens on the gateway side, no API returns an upstream secret to a client, and the tests scan the bytes an agent can see for the injected value. When an MCP server answers with the credential Straza injected for the call, whole or encoded, the gateway withholds the reply. The agent reads a refusal, and the audit chain gets a deny record and a critical `credentialEcho` verdict. A tool listing or a view that holds the credential is not served either.

The agent's own Straza credentials do sit on its machine. The key and the session tokens sit with `straza service`, under its own OS account where it runs as the system service. strazactl keeps no login, so an admin credential sits on a machine only when someone puts an admin API token there. A person mints such a token in the console with a scope and an expiry. It is a bearer credential, and whoever holds it acts with its scope until it expires or is revoked, so never put one where an agent runs as you. [Credentials and sessions]({{< relref "security/credentials-and-sessions.md#what-sits-on-disk" >}}) shows what sits on disk.

The cost is that a tool with a credential works only through a reachable gateway, since there is no way to hand the agent the secret for an offline call.
{{< /details >}}

{{< details summary="Every decision fails closed" >}}

An unknown state is a deny that carries a reason the agent can read and act on. The clearest cost is the offline grace period. While the server is unreachable, `straza service` keeps deciding from its verified snapshot for at most `governance.offlineGraceTTL` past its token's expiry. That is 15 minutes in the standalone profile and zero in the enterprise profile, as [the profile comparison]({{< relref "reference/standalone-and-enterprise.md#offline" >}}) shows. Then every governed action is denied with the words `session token expired`, the age, and the instruction to reconnect and run `straza doctor`. A bad deploy of strazad in an enterprise deployment therefore halts every agent within five minutes, which is the intended behavior and the thing to plan for. strazad also refuses to start without the `secret.key` its database was sealed with, and that stops agents the same way.

The coding tool runs the hook, so the coding tool decides what happens when `straza hook` itself does not run. The Claude Code wiring sets `onFailure` to `block` on its PreToolUse entry. On Claude Code 2.1.295 and later a tool call is therefore blocked when the hook cannot start, is killed, times out or prints output Claude Code cannot read. A machine wired before the setting existed keeps its old entry until `straza install` runs again. Earlier Claude Code releases ignore the setting, and Codex and Gemini CLI offer none, so on them such a call runs unchecked. Once `straza hook` runs, it denies on its own errors. A call blocked because the hook could not start leaves no Straza audit record, while one blocked after the hook was killed or timed out may already carry the decision Straza recorded.

Audit is the deliberate exception. On the agent's machine a decision goes on when the client's spool cannot take its record, in both profiles. On the server, a standalone decision proceeds when its record cannot be written, and an enterprise decision waits for room in the audit queue instead. A long database outage therefore stops enterprise decisions, as [Audit records can be lost](#audit-records-can-be-lost) explains.
{{< /details >}}

{{< details summary="Enforcement artifacts are tamper-evident" >}}

Managed installs are root-owned, and the hashes they report are checked against the registered set when a session starts. A mismatch produces the `none` level, for which the enterprise default refuses a token. Admin clients are exempt from that check-in minimum, so that an administrator can sign in before any hash exists. The gateway refuses their tokens on the data plane under the same minimum, so the exemption cannot become a bypass.

The cost is operational. A managed install needs administrator rights. strazad registers the hash of the hook wiring itself and keeps the rows of older versions. Once an admin also registers the hash of the binary or the configuration, a client upgrade needs its new hashes in the registry before its sessions attest as managed again.
{{< /details >}}

{{< details summary="Snapshots and releases are signed" >}}

Each snapshot is content-addressed and signed with Ed25519, and the client verifies it against keys pinned at enrollment. A running session that fetches a snapshot it cannot verify refuses to enforce it. It keeps governing with the previous one until the session time it held runs out, and then its hooks deny every governed call until a fetch succeeds. A session start that meets such a snapshot fails, so its hooks deny. Releases are signed with cosign, and [Supply chain]({{< relref "security/supply-chain.md" >}}) says how to check one.

The cost is that a new snapshot key means pinning the keys again on every machine, because the pin is the trust anchor. A person runs `straza enroll` again, as [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) shows, and on a managed install an administrator runs `straza install --managed --server <strazad URL> <harness>` again. No rotation withdraws an old key today. [Keys, certificates and tokens]({{< relref "security/keys-certificates-and-tokens.md#back-up-and-rotate-by-procedure" >}}) explains why.
{{< /details >}}

{{< details summary="The spec is the contract" >}}

A byte that crosses a process boundary has its shape in the published specification. A change ships as a schema, fixtures and a version bump together, named in the release notes, with the previous fixtures still passing. The cost falls on rollout order. Parsers are strict, so an older client rejects a policy document that uses a block from a newer revision, and you upgrade clients before you write the block.
{{< /details >}}

{{< details summary="The binary stays single and static" >}}

CGO is off in the Makefile and in the release pipeline, SQLite is pure Go, and the message bus runs inside strazad by default in both profiles. The push senders are written against the standard library and no cloud SDK. The cost is a narrower choice of libraries and the work of writing some of them by hand.
{{< /details >}}

{{< details summary="A process runtime never carries a caller's credential" >}}

A `command` or `oci` MCP server is one process with one identity. The manifest parser therefore refuses a per-person credential kind on those runtimes, and the resolver refuses it again whatever a stored manifest says. The cost is that a server whose upstream needs each person's own token runs on the `remote` runtime or on that person's machine.
{{< /details >}}

## The enterprise profile refuses command servers


This is a rule of the enterprise profile, beside the invariants above, and the standalone profile does not hold it. A `command` server would be a child process of strazad with strazad's user, files and network. It could read `secret.key`, which seals every stored credential and the signing keys. The enterprise profile refuses that runtime at every door and never starts a `command` server that is already stored. The cost is that a server published only as an npm or PyPI package runs as its own service or pod and joins as a `remote` server.

Under the standalone profile `command` servers still run as strazad's own user, which trusts their code as much as Straza itself. [The profile comparison]({{< relref "reference/standalone-and-enterprise.md#command" >}}) shows both profiles side by side.

## The limits in detail {#known-limits}

[Known limits]({{< relref "security/known-limits.md" >}}) lists the limits that stay with you. The sections below explain five mechanisms in full, with what you can do today.

### An agent that holds its person's login can enroll an approval device


A decision signed by an enrolled phone or browser proves which device decided. It does not prove that a person acted.

Enrolling a device takes an enroll token, and strazad mints one only for whoever is signed in as that person. A person does that on the self-service page or in the console. Minting needs `straza-admin`, or `straza-enroll-mobile` or `straza-enroll-browser` for that kind of device. No admin route or console screen enrolls a device in another person's name, and an admin API token mints no enroll token at all. strazad stores the platform and the attestation that the enrolling device reports without checking them, so any program that makes a P-256 key can enroll as a phone.

So whoever can sign in as a person can enroll a device as that person. An AI agent that runs as the person can read every file the person can read. When it finds a sign-in of the person there, such as an unexpired ID token, it can enroll a device of its own as the person. That device then decides every request the person may decide, including one the agent raised itself when the rule leaves the decision to that person.

The standalone profile has a second way in. strazad is the identity provider there, so a credential that may set a person's password, such as an admin API token with the identity scope, can sign in as that person. The `straza.audit.admin` record `user.update` with `changed` `password` names that credential, and the person's own password stops working. The enterprise profile closes this way, because your identity provider signs people in there and the built-in issuer signs in only the break-glass admin.

What you can do today is keep every credential of a person away from their agents. Never put an admin API token in a shell profile, in a file an agent can read or in a terminal an agent shares. Give a token for a terminal the scopes the work needs and a short lifetime, and revoke it when the work is done. Keep tokens with the identity area few. `strazactl approvers list` shows every enrolled approval device with its owner, and `strazactl approvers revoke` removes one that nobody recognizes. The [roadmap]({{< relref "project/roadmap.md" >}}) plans a confirmation on a device you already enrolled.

### Manifest values are stored in plain text


strazad stores each MCP server's manifest as plain JSON. A value written into it, such as a fixed environment variable, an argument or an address, therefore sits in clear in any copy of the database. The server's lists and exports mask every environment value, every flagged argument and the user part, query and fragment of every address. That keeps them off screens and out of exported files, while storage stays plain text.

A value that must stay secret belongs in the server's secret, which `strazactl apps secret set` seals, and never in the manifest. The [Credentials]({{< relref "guides/serve-mcp-apps/credentials.md" >}}) guide shows how to declare one. A person or a drafts token that reads a server without apps:write can still learn from a drafts check whether a guessed value at a masked place equals the stored one, for any value the secret scan does not flag. Keep a secret in the server's secret, and never in a plain-named environment value.

### A Rego module can run past its time limit


The Rego modules that apply to one decision share a time limit of 100 ms, and a decision whose modules run past it is a deny. The engine checks the limit between evaluation steps, so it cannot stop one built-in call that is already running.

A module may not call a built-in that reaches the network, the file system or the process environment, or one whose single call can run far past the limit. strazad refuses a policy set whose module calls `http.send`, `net.lookup_ip_addr`, `json.match_schema`, `json.verify_schema`, `opa.runtime`, `strings.render_template`, `rego.parse_module`, `graph.reachable_paths`, `bits.lsh`, `net.cidr_contains_matches`, `glob.match` or one of the six `graphql` built-ins.

An allowed built-in given a very large value, such as `regex.match` over many megabytes, can still finish seconds after the limit and use memory in proportion, and the decision is then a deny. On a workstation the hook reads what the coding tool sends with no size cap, and a call over 16 MiB is denied before Rego runs. The event carries the whole shell command and the whole arguments of an MCP tool call, so a module that runs `regex.match` over them can hold a very large call for seconds before it denies. Review a Rego module as you would review code, and give the right to write policy only to people you would trust with that.

### The attestation level comes from the client


A managed install keeps the configuration and the hook wiring in root-owned system paths, so a person without administrator rights cannot change them. strazad compares the hashes the client reports with its registry when a session starts. It registers the hook wiring hashes itself, from its own rendering of the coding tool's configuration. The binary and the configuration count only once an admin registers their hashes with `strazactl attestation add`.

On Linux, macOS and Windows, `straza service` measures its own binary and the root-owned managed files itself, under its own OS account, and keeps the person's key and sessions out of reach of the person's processes. The hashes stay the client's report all the same. A program of the person's own that holds a fresh sign-in of the person can enroll a key it keeps itself, report the `account` custody, send a check-in with the registered hashes and receive a `managed` session.

So `managed` shows that the reported files match the registry, and it does not prove which program sent the report. Treat it as a guard against changed enforcement files, and never as the only thing between an agent and an action that must not happen.

### Audit records can be lost


A record can be lost in each of these cases:

| Case | What happens |
|---|---|
| strazad crashes, or is killed before its stop finishes, while records wait in its in-memory queue. A stop can take 20 seconds, so an orchestrator that kills it sooner, such as Docker with its default of 10 seconds, is such a kill. | Those records are lost, and nothing records the loss. |
| A request is still running 10 seconds after the stop began, such as a call held for an approval. | Its record may never be written, and nothing records the loss. |
| strazad is told to stop while a background worker, such as the key janitor or the session janitor, has changed the database and not yet written the record of that change. | strazad waits up to 12 seconds for the record. If the worker is still writing then, for example because the database does not answer, strazad logs one error line that says a record may be missing. A kill in that moment loses the record, and nothing records the loss. |
| An attempt to write a record ends without an answer from the database, for example after its 5 seconds, or when a pooler such as PgBouncer loses its connection to the database, and no retry settles it | The record may be stored or lost. `straza_audit_lost_total` counts it, and strazad logs one error line with its type and id that says so: search the audit chain for that id |
| strazad stops before it can write the records left in its queue, 5 seconds after the requests have finished, for example while the database is down in the enterprise profile | Those records are lost, and strazad logs one error line with their count. `straza_audit_lost_total` counts them too, but nobody can read it then: the metrics endpoint has already closed, and the counter starts at zero in the next process. The log line is the only trace |
| In the enterprise profile, a server decision finds the queue full for 25 seconds, or its client leaves first | The decision does not run, and its client, if it still waits, gets a deny that says the database cannot be reached. Its record never enters the queue. strazad logs one fail-closed line with the request's correlation id and counts it in `straza_failclosed_total` |
| strazad cannot write an admin, identity, authentication or approval record to its database | strazad tries once more under the same id, so a write whose answer was lost leaves no second copy. A record still not written is lost, and strazad logs one error line that names it and counts it in `straza_outbox_records_lost_total`, except an approver phone's refusal record, which the phone's next refused request writes again |
| The client spool cannot be written | The decision proceeds without a record |
| An upload failed and the client spool holds more than 32 MiB of unsent records, one record is 3 MiB or larger, or the client's background service refuses a decide or capture call before it can record it | The oldest unsent files, that record or that call's record are lost. The client counts the loss, `straza doctor` reports it on the machine, and the next upload reports it to strazad as one `straza.audit.tool` record with the event `audit.dropped`, which the client keeps sending until strazad takes it |
| A record is over 256 KiB of JSON when strazad writes it to its database, for example a hook command of hundreds of kilobytes. | strazad cuts the longest strings of the record until it fits. It marks each cut with the string's whole length and its SHA-256, so you can check a cut command against the client's own copy. A record that still does not fit keeps only its main fields, as the fold below sets out. |

{{< details summary="Cases on the standalone profile, faults outside strazad and the exact cut of a large record" >}}
| Case | What happens |
|---|---|
| In the standalone profile, the first attempt to write a record and three retries 0.2, 0.4 and 0.8 seconds apart fail, for example while the database is down | The record is lost, `straza_audit_lost_total` counts it, and strazad logs one error line with its type and id |
| The database refuses the record itself on the first attempt and three retries, in either profile, which is a defect in strazad | The record is lost, `straza_audit_lost_total` counts it, and strazad logs one error line with its type and id that asks you to report it |
| The in-memory queue is full in the standalone profile | The record is dropped and `straza_audit_dropped_total` counts it |
| A record is over 256 KiB of JSON when strazad writes it to its database, for example a hook command of hundreds of kilobytes, whose record escapes each `<`, `>` and `&` into six bytes | strazad cuts the longest strings of the record to one common length at which it fits, each followed by `... (<n> bytes, sha256 <hex>)`, its whole length and the hash of the whole string, so you can check a cut command against the client's own copy. The rest of each cut string is lost. A record that cutting cannot fit, such as one with thousands of paths, keeps only who acted, on what, the outcome and reason, and the command, workspace and arguments, each cut at 256 bytes the same way. Its other fields, the paths among them, are lost, and its `dataOmitted` gives the length and the hash of the whole record. A record over 256 KiB that holds more than 65,536 JSON values takes that form at once, without the cut, and so does a client record past the 65,536 values one upload decodes in full, whatever its length |
| A row in the database outbox is larger than the event bus's maximum message size, which happens only when an external NATS server's `max_payload` is below 256 KiB or a row was written to the database past strazad | The relay sets the row aside, so its record never reaches the chain or the sinks, and every later record is published. `straza_outbox_set_aside_total` counts it, and strazad logs one error line with its id, subject and size and the bus's `max_payload` |
| A client uploads a spooled record that names a session of another user or another device, an unknown session or a value that is not a session id | The server leaves the record out of the chain, logs one warning line for each session the upload names this way, with the count of records it covers, the first record's id and the uploader's session, and counts each record in `straza_audit_refused_total`. The client spool has already deleted it with the rest of the batch |
| A client uploads more than 1000 records at once, which a straza client never does | The server refuses the whole upload with 413, stores none of its records and logs one warning line. The client keeps the records in its spool |

A full stop takes up to 20 seconds: up to 10 for the requests still running, up to 5 to write the queue, and up to 5 more on SQLite while another process holds the database.
{{< /details >}}

Under `block`, the enterprise default of `governance.auditBackpressure`, strazad keeps each record in its queue until the database takes it. While the database is down, the queue fills after 4,096 server decisions, which is about 7 minutes at 10 decisions per second or 41 seconds at 100. After that each new decision waits up to 25 seconds for room and is then refused with a reason that says the database cannot be reached. No decision runs before its audit record is in the queue, and strazad holds at most 25 seconds of waiting decisions.

Each waiting decision holds a connection and memory, so strazad needs memory for 25 seconds of its peak decision rate. The Helm chart's limit of 512 MiB is reached above about 250 server decisions per second. strazad logs `audit queue waiting for the database` when it starts to hold the queue, one fail-closed line for each refused decision, and `audit queue moving again` when the database takes the record.

The decisions that ran before the queue filled have their records in memory only, so a stop or a crash during the outage loses them, as the table says. A decision that still waits for room when strazad stops never runs. It is refused, or its connection closes when the process exits. If you prefer that agents keep working, set `governance.auditBackpressure` to `drop-with-counter`, the standalone default. Decisions then go on, and the records that cannot be written are lost and counted.

Keep the database reachable and watch it. On the server and its clients, watch these:

- `straza_audit_dropped_total`, `straza_audit_lost_total`, `straza_audit_refused_total`, `straza_outbox_set_aside_total` and `straza_failclosed_total` on the server.
- The error lines that name each lost record, and the warning lines of admin, identity, authentication and approval records.
- Each `audit batch:` warning line, which counts the refused spooled records.
- The `straza.audit.tool` records with the event `audit.dropped`, which name what each client lost, and the audit-dropped line of `straza doctor` on a client until its next upload reports the loss.
- `straza_sink_deadletter_total`, for sink deliveries that failed.

Verify the chain as well. It cannot show a record that never reached it, so on the server these counters and log lines are the only trace of a loss.

Report anything that contradicts this page through [Reporting a vulnerability]({{< relref "security/reporting.md" >}}).
