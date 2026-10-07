---
title: The security model
description: What Straza defends, where each defense holds, the invariants behind it with what each one costs, and the mechanism behind the limits it leaves with you.
pagetype: explanation
weight: 10
draft: false
keywords: security model invariants threat residual risk fail closed known limits
---


A security reviewer or an auditor reads this page to learn what Straza defends, where each defense holds, what each one costs and which limits stay with you. Straza protects the actions an agent takes under an identity, so that a session can do only what its roles allow, every decision leaves a record that cannot be edited in place, and a revoked identity is refused by the gateway on its next call and by the workstation within seconds while the client daemon is connected. The design rests on eleven invariants, and [Known limits]({{< relref "security/known-limits.md" >}}) lists in one table every risk they leave with you.

It is the security reading of the system that [Trust model and limits]({{< relref "concepts/trust-model-and-non-goals.md" >}}) describes.

## The problem


An agent runs with the shell, the files and the credentials of the person or the workload behind it, and a model can be talked into using them. The attacks that follow from that are ordinary ones aimed at a new target: a prompt that turns into a destructive command, a session token lifted from a laptop, a policy file swapped on its way to the machine that enforces it, a hook edited out of the harness configuration, an upstream API key read out of the agent's environment, and a log altered after the fact. A control that sits in the model's own process cannot answer those, because the process is the thing under attack. Straza answers them by deciding every action against a signed policy, by keeping the credential and the record away from the agent, and by refusing to act when it cannot tell what the current state is.

## The boundaries, in the order a call crosses them


The first boundary is the agent's own machine, and it is the weakest. A hook installed in user mode governs every tool call the harness makes, and an agent that can run anything on that machine can also run around it, so a user-mode hook on an open machine is advisory. A managed install moves the binary, the configuration and the hook wiring into root-owned system paths and measures their hashes, and the server compares them with an expected set when a session starts.

Each refresh keeps the level the session started with, so a change made during a session shows at the next session start. Tampering shows as a mismatch or a missing measurement, which makes the session attest as `none`, and under the enterprise default that means no token at all. Policy can then require the `managed` level for a sensitive role, so the decision about what an open machine may do is yours to write down. The hashes are the ones the client reports, as [the attestation section below](#the-attestation-level-comes-from-the-client) explains. [Known limits]({{< relref "security/known-limits.md" >}}) lists that limit and the advisory hook, each with what reduces it today.


The second boundary is the hop from the client to strazad. Session tokens and admin API tokens cross it, so it is TLS or a network you trust. strazad serves TLS 1.2 or newer from a certificate pair you give it, the enterprise profile warns at boot when it serves plaintext, and there is no knob anywhere in the product to skip certificate verification.

The standalone profile binds its main listener to 127.0.0.1:8420 and stays quiet, so rebinding it to a routable address is the moment to add TLS. Unless `server.approverTLS.autoMint` is false, it also serves the phone approver routes on port 8443 on every interface, with a self-signed certificate it creates in the data directory. [Hardening]({{< relref "security/hardening.md" >}}) walks that step.


The third boundary is strazad itself. A request arrives with a session token that the server verifies against its own signing keys, checks against an in-memory denylist, and checks against the attestation minimum, all without touching the database. The decision runs over a compiled policy snapshot held in memory, and the subject's roles come from a cache filled at check-in. After a restart that cache is empty, the token alone cannot prove roles, and the gateway refuses the call until the client checks in again rather than guessing, which is the fail-closed shape every unknown state takes.


The fourth boundary is the gateway's hop to an upstream MCP server, and it is the one that holds at boundary grade with no agent-side code. The credential a tool needs is bound to its MCP server on the Straza side, sealed at rest, and injected into the upstream request, or into the environment of a `command` or `oci` server, by name, never through the argument vector and never through any answer to a client. No credential for an MCP server behind the gateway sits on the agent's machine. An MCP server that needs a credential and has none fails closed until one exists. An MCP server registered directly on a harness is outside this boundary, because only the hook sees its calls and its credentials are yours to protect, and it has its own row in [Known limits]({{< relref "security/known-limits.md" >}}).


The fifth boundary is the sandbox profile for processes without a hook. Inside that image, the wrapper is the process's only route to running anything. The path holds the shim, the governed shell and the agent's interpreter, every other file loses its execute bit at build time, and the build fails if one survives. The root filesystem is read-only, the writable mounts are noexec, capabilities are dropped and no process can gain privileges.

Two residuals remain, and the image's own README states them. Computation inside the interpreter is ungoverned, because the interpreter is the agent. Every binary on the allowlist is reachable by the same user id without a decision, so per-argument rules are boundary-grade only for tools that are absent from the image. Egress is yours to lock down as well, because the shipped compose file leaves the container's network routable, and the boundary-grade shape is an internal network that holds only the agent and strazad. [Known limits]({{< relref "security/known-limits.md" >}}) lists these residuals, and the fact that a check-in cannot prove it came from the image, with what reduces each.


The last boundary is the record. Every decision becomes an event on a hash chain, written after the decision and never in its path, and a sink you configure keeps a copy of every event off the box, where a database owner cannot rewrite it. [Evidence and audit]({{< relref "concepts/evidence.md" >}}) explains the chain and what it can and cannot prove.

## The invariants and what each one costs

Eleven invariants hold the design together. Each one below names the mechanism that holds it and what it costs you.

### No decision reads the database


Decisions use the compiled snapshot, and authentication uses locally verified tokens. A native tool of the built-in straza MCP server, such as the approval and drafting tools, reads and writes only its own records by id, after the decision to run it. A counting store in the test suite fails the build when a new repository is added without being classified, so the rule cannot erode one getter at a time.

The cost is lag. A policy change is a new snapshot that clients adopt at their next check-in or on a push nudge. A server restart empties the in-memory subject cache, so every client re-checks in once before its next gateway call succeeds.

### Session tokens validate themselves, and revocation is pushed



Each session token lives 300 seconds, is signed with Ed25519, and is verified against a cached key set that carries the staged, active and retiring keys. A rotation started with `strazactl signing-keys rotate session` therefore strands nobody. Every replica holds the new key before any replica signs with it, and the old key verifies until every credential it signed has expired.

Revoking a session, a user or a device writes a record and adds the target to an in-memory denylist on every replica. A session entry is never lifted again.

The cost is a window. With the client daemon connected, the revocation lands in under two seconds on the [demo stack]({{< relref "get-started/enterprise-demo-stack.md" >}}), except that a session the daemon started again after a refusal learns of its own revoke at the next 30-second poll. Without the daemon, the hook keeps allowing from its cache until the token expires, at most 300 seconds after the mint in both profiles, while the gateway refuses the revoked token at once. Only a standalone hook whose renewal cannot learn the server's judgment, because strazad cannot be reached or something in front of it answers instead, adds its offline grace, up to 1,200 seconds after the mint. [Known limits]({{< relref "security/known-limits.md" >}}) lists this window with what shortens it, and [Credentials and sessions]({{< relref "security/credentials-and-sessions.md" >}}) follows a machine's credential from enrollment to lockout.

### Audit is asynchronous


Server decisions enter an in-memory queue with room for 4,096 records before they are committed to the transactional outbox. A write that fails is tried again three times. After that, `governance.auditBackpressure` decides what happens to the record.

Under `block`, the enterprise default, strazad keeps trying the record for as long as the database cannot take it. When the queue is full, a new decision waits up to 25 seconds for room and is then refused with a reason, so nothing runs without its record. Under `drop-with-counter`, the standalone default, the record is counted in `straza_audit_lost_total` and logged with its type and id. A record the full queue cannot take is dropped and counted in `straza_audit_dropped_total`.

When strazad stops, it ends the event streams of connected clients and waits up to 10 seconds for the requests still running. It then writes what the queue holds for up to 5 seconds before it closes the database, so give it 30 seconds to stop. The cost is that a record can be lost in the cases that [Audit records can be lost](#audit-records-can-be-lost) lists.

### The data plane is stateless


Any gateway instance can serve any session, and sticky routing is only an optimization. The cost is the one named above. State that a replica needs for a session is rebuilt from the token and a check-in, never assumed.

### Upstream credentials never reach the agent


Injection happens on the gateway side, no API returns an upstream secret to a client, and the tests scan the bytes an agent can see for the injected value. The agent's own Straza credentials, the device credential and the session token, do sit on its machine, as [Credentials and sessions]({{< relref "security/credentials-and-sessions.md#what-sits-on-disk" >}}) shows. The cost is that a credentialed tool works only through a reachable gateway, since there is no way to hand the agent the secret for an offline call.

### Every decision fails closed


An unknown state is a deny that carries a reason the agent can read and act on. The clearest cost is the offline grace period. While the server is unreachable, a client keeps deciding from its verified snapshot for at most `governance.offlineGraceTTL` past its token's expiry, which is 15 minutes in the standalone profile and zero in the enterprise profile, as [the profile comparison]({{< relref "reference/standalone-and-enterprise.md#offline" >}}) shows. Then every governed action is denied with the words `session token expired`, the age, and the instruction to reconnect and run `straza doctor`. A bad deploy of strazad in an enterprise deployment therefore halts every agent within five minutes, which is the intended behavior and the thing to plan for.

Audit is the deliberate exception in the standalone profile, where a decision proceeds when its record cannot be written. In the enterprise profile a decision waits for room in the audit queue instead, so a long database outage stops decisions, as [Audit records can be lost](#audit-records-can-be-lost) explains.

### Enforcement artifacts are tamper-evident


Managed installs are root-owned, and the hashes they report are checked against an expected set when a session starts. A mismatch produces the `none` level, which the enterprise default refuses a token for. Admin clients are exempt from that check-in minimum so that an administrator can sign in before any hash exists, and the gateway refuses their tokens on the data plane under the same minimum, so the exemption cannot become a bypass.

The cost is operational. A managed install needs administrator rights, and a client upgrade needs its new hashes in the registry before its sessions attest as managed again.

### Snapshots and releases are signed


Each snapshot is content-addressed and signed with Ed25519, and the client verifies it against keys pinned at enrollment. A running session that fetches a snapshot it cannot verify refuses to enforce it. It keeps governing with the previous one until the session time it held runs out, and then its hooks deny every governed call until a fetch succeeds. When a session start meets such a snapshot, it fails, so its hooks deny. Releases are signed with cosign, and [Supply chain]({{< relref "security/supply-chain.md" >}}) says how to check one.

The cost is that a new snapshot key means pinning the keys again on every machine, because the pin is the trust anchor. A person runs `straza enroll` again, as [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) shows, and on a managed install an administrator runs `straza install --managed --server <strazad URL> <harness>` again. No rotation withdraws an old key today. [Known limits]({{< relref "security/known-limits.md" >}}) lists that limit, and [Keys, certificates and tokens]({{< relref "security/keys-certificates-and-tokens.md#back-up-and-rotate-by-procedure" >}}) explains why.

### The spec is the contract


A byte that crosses a process boundary has its shape in the published specification. A change ships as a schema, fixtures and a version bump together, named in the release notes, with the previous fixtures still passing. The cost falls on rollout order. Parsers are strict, so a policy document that uses a block from a newer revision is rejected by an older client, and you roll clients before you write the block.

### The binary stays single and static


CGO is off in the Makefile and in the release pipeline, SQLite is pure Go, the message bus runs inside strazad by default in both profiles, and the push senders are written against the standard library rather than a cloud SDK. The cost is a narrower choice of libraries and the work of writing some of them by hand.

### A process runtime never carries a caller's credential


A `command` or `oci` MCP server is one process with one identity. The manifest parser therefore refuses a per-person credential kind on those runtimes, and the resolver refuses it again whatever a stored manifest says. The cost is that a server whose upstream needs each person's own token runs on the `remote` runtime or on that person's machine.

## The enterprise profile refuses command servers


This is a rule of the enterprise profile, beside the invariants above, and the standalone profile does not hold it. A `command` server would be a child process of strazad with strazad's user, files and network, so it could read the key that seals every stored credential. The enterprise profile refuses that runtime at every door and never starts a `command` server that is already stored. The cost is that a server published only as an npm or PyPI package runs as its own service or pod and joins as a `remote` server.

Under the standalone profile `command` servers still run as strazad's own user, which trusts their code as much as Straza itself. [The profile comparison]({{< relref "reference/standalone-and-enterprise.md#command" >}}) shows both profiles side by side.

## What is left with you


Some risks stay with you whatever you configure. [Known limits]({{< relref "security/known-limits.md" >}}) gives each one a row with what reduces it today, and this section explains the ones that start at the boundaries above.

Root on an endpoint owns every process on it, including the hook, and no enforcement in user space changes that.

Interpreter indirection, where the agent writes a script and then runs it, is a documented limitation. The interpreter call is tagged and can be denied or sent to the heuristic classifier per rule. The audit sentinel, once you turn it on, detects the write-then-execute sequence afterwards without blocking it. A bypass through interpreter indirection is therefore not by itself a vulnerability, while a bypass of the gateway, or of a deny the documentation says is covered, is one.

Straza refuses to dial a link-local, cloud metadata or unspecified address for a remote MCP server, and the enterprise profile also refuses a loopback address unless `apps.allowLoopbackUpstreams` is true. Any other internal address in a remote manifest is the administrator's review duty until an egress allowlist per MCP server exists.

The standalone issuer authenticates with passwords, hashed with bcrypt, because passkeys for it are not built. The server cannot verify from the wire that a session came from the sandbox image, so treat that claim as your deployment's and keep high-blast-radius permissions on managed sessions.


Two things are out of scope by design. Model traffic is never proxied, so prompts and completions stay on whatever stack you run and Straza never sees them unless a policy turns on conversation recording at the hook. Computation inside a process is ungoverned, in the sandbox and everywhere else, because the interpreter is the agent.

## The limits in detail {#known-limits}

Each limit below describes the code as it stands in v1.1.0, together with what you can do about it today. [Known limits]({{< relref "security/known-limits.md" >}}) lists every limit in one table, and the rows for these link here for the mechanism.

### An agent that holds its person's login can enroll an approval device


A decision signed by an enrolled phone or browser proves which device decided, and it does not prove that a person acted. A person who holds `straza-admin` can mint an enroll token for their own user, and so can a person who holds `straza-enroll-mobile` or `straza-enroll-browser`, for that kind of device. The server stores the platform and the attestation that the enrolling device reports without checking them, so any program that makes a P-256 key can enroll as a phone.

strazactl keeps its login in `~/.straza/credentials.json`, which every process that runs as the person can read. An AI agent that runs as the person can therefore enroll a device of its own. That device then decides every request the person may decide, including one the agent raised itself when the rule leaves the decision to that person.

What you can do is keep a person's login away from their agents. Inside Claude Code, the Gemini CLI and Codex installed from npm, strazactl sees the variables those agents set and refuses every command that changes Straza on the stored login, minting an enroll token included. That guard stops a mistake or a naive prompt injection. It does not stop an agent that unsets the variables or reads the credentials file itself.

So do admin work on a machine or an account where no agent runs as you, or run `strazactl logout` when you finish, which revokes the stored session and deletes the file. `strazactl approvers list` shows every enrolled approval device with its owner, and `strazactl approvers revoke` removes one that nobody recognizes. [Known limits]({{< relref "security/known-limits.md" >}}) lists this limit with the confirmation step that the roadmap plans for it.

### The hook lane is not a security boundary


A hook inside an agent's process on a machine the agent controls can be skipped by an agent that can run anything, so it is advisory there, as [Trust model and limits]({{< relref "concepts/trust-model-and-non-goals.md" >}}) says. An MCP server that a person adds to their own harness, outside the gateway, is outside what Straza governs at a boundary. Only that advisory hook sees its calls, the gateway never does, and its credentials sit on the person's machine. Governing and protecting such a server is that person's responsibility. Register every MCP server that holds a credential in Straza instead, so the gateway decides its calls and the credential stays on the server.

### Manifest values are stored in plain text


The server stores each MCP server's manifest as plain JSON, so a value written into it, such as a fixed environment variable, an argument or an address, sits in clear in any copy of the database. The server's lists and exports show every environment value, every flagged argument and the user part, query and fragment of every address masked. That keeps them off screens and out of exported files, while storage stays plain text.

A value that must stay secret belongs in the server's secret, which `strazactl apps secret set` seals, and never in the manifest. The [Credentials]({{< relref "guides/serve-mcp-apps/credentials.md" >}}) guide shows how to declare one. A person or a drafts token that reads a server without apps:write can still learn from a drafts check whether a guessed value at a masked place equals the stored one, for any value the secret scan does not flag. Keep a secret in the server's secret, and never in a plain-named environment value.

### A Rego module can run past its time limit


The Rego modules that apply to one decision share a time limit of 100 ms, and a decision whose modules run past it is a deny. The server checks the limit between evaluation steps, so it cannot stop one built-in call that is already running.

A module may not call a built-in that reaches the network, the file system or the process environment, or one whose single call can run far past the limit. The server refuses a policy set whose module calls `http.send`, `net.lookup_ip_addr`, `json.match_schema`, `json.verify_schema`, `opa.runtime`, `strings.render_template`, `rego.parse_module`, `graph.reachable_paths`, `bits.lsh`, `net.cidr_contains_matches`, `glob.match` or one of the six `graphql` built-ins.

An allowed built-in given a very large value, such as `regex.match` over many megabytes, can still finish seconds after the limit and use memory in proportion, and the decision is then a deny. On a workstation the hook reads what the harness sends with no size cap. The event it builds carries the whole shell command and the whole arguments of an MCP tool call, so a module that runs `regex.match` over them can hold a very large call for seconds before it denies. Review a Rego module as you would review code, and give the right to write policy only to people you would trust with that.

### The attestation level comes from the client


A managed install keeps the binary, the configuration and the hook wiring in root-owned system paths, so a person without administrator rights cannot change them. The server compares the hashes the client reports with its registry when a session starts. It registers the hook wiring hashes itself from its own rendering of the harness configuration, and the binary and the configuration count only once an admin registers their hashes with `strazactl attestation add`.

The device credential and the session state stay in the person's home directory even on a managed install, where any process of that person can read them. A process that runs as the person can therefore send a check-in of its own with the registered hashes and receive a `managed` session. So `managed` shows that the reported files match the registry. It does not prove which program sent the report. Treat it as a guard against changed enforcement files, and never as the only thing between an agent and an action that must not happen.

### Audit records can be lost


A record can be lost in each of these cases:

| Case | What happens |
|---|---|
| strazad crashes, or is killed before its stop finishes, while records wait in its in-memory queue. A stop can take 20 seconds: up to 10 for the requests still running, up to 5 to write the queue, and up to 5 more on SQLite while another process holds the database. An orchestrator that kills it sooner, such as Docker with its default of 10 seconds, is such a kill | Those records are lost, and nothing records the loss |
| A request is still running 10 seconds after the stop began, such as a call held for an approval | Its record may never be written, and nothing records the loss |
| In the standalone profile, the first attempt to write a record and three retries 0.2, 0.4 and 0.8 seconds apart fail, for example while the database is down | The record is lost, `straza_audit_lost_total` counts it, and strazad logs one error line with its type and id |
| The database refuses the record itself on the first attempt and three retries, in either profile, which is a defect in strazad | The record is lost, `straza_audit_lost_total` counts it, and strazad logs one error line with its type and id that asks you to report it |
| An attempt to write a record ends without an answer from the database, for example after its 5 seconds, or when a pooler such as PgBouncer loses its connection to the database, and no retry settles it | The record may be stored or lost. `straza_audit_lost_total` counts it, and strazad logs one error line with its type and id that says so: search the audit chain for that id |
| strazad stops before it can write the records left in its queue, 5 seconds after the requests have finished, for example while the database is down in the enterprise profile | Those records are lost, and strazad logs one error line with their count. `straza_audit_lost_total` counts them too, but nobody can read it then: the metrics endpoint has already closed, and the counter starts at zero in the next process. The log line is the only trace |
| The in-memory queue is full in the standalone profile | The record is dropped and `straza_audit_dropped_total` counts it |
| In the enterprise profile, a server decision finds the queue full for 25 seconds, or its client leaves first | The decision does not run, and its client, if it still waits, gets a deny that says the database cannot be reached. Its record never enters the queue. strazad logs one fail-closed line with the request's correlation id and counts it in `straza_failclosed_total` |
| strazad cannot write an admin, identity, authentication or approval record to its database | The record is lost, and strazad logs one warning line, with no retry and no counter |
| The client spool cannot be written | The decision proceeds without a record |
| The client spool holds more than 32 MiB of parked records, or one record is 3 MiB or larger | The oldest parked files, or that record, are dropped, and a loss marker records it for `straza doctor` |
| A client uploads a spooled record that names a session of another user or another device, an unknown session or a value that is not a session id, or a batch names more than 1000 sessions other than the uploader's | The server leaves the record out of the chain, logs one warning line for each session the upload names this way, with the count of records it covers, the first record's id and the uploader's session (one more line with the count for the records past the 1000), and counts each record in `straza_audit_refused_total`. The client spool has already deleted it with the rest of the batch |

Under `block`, the enterprise default of `governance.auditBackpressure`, strazad keeps each record in its queue until the database takes it. While the database is down, the queue fills after 4,096 server decisions, about 7 minutes at 10 decisions per second or 41 seconds at 100. After that each new decision waits up to 25 seconds for room and is then refused with a reason that says the database cannot be reached. Nothing runs without its audit record, and strazad holds at most 25 seconds of waiting decisions.

Each waiting decision holds a connection and memory, so strazad needs memory for 25 seconds of its peak decision rate. The Helm chart's limit of 512 MiB is reached above about 250 server decisions per second. strazad logs `audit queue waiting for the database` when it starts to hold the queue, one fail-closed line for each refused decision, and `audit queue moving again` when the database takes the record.

The decisions that ran before the queue filled have their records in memory only, so a stop or a crash during the outage loses them, as the table says. A decision that still waits for room when strazad stops never runs. It is refused, or its connection closes when the process exits. If you prefer that agents keep working, set `governance.auditBackpressure` to `drop-with-counter`, the standalone default. Decisions then go on, and the records that cannot be written are lost and counted.

Keep the database reachable and watch it. On the server and its clients, watch these:

- `straza_audit_dropped_total`, `straza_audit_lost_total`, `straza_audit_refused_total` and `straza_failclosed_total` on the server.
- The error lines that name each lost record, and the warning lines of admin, identity, authentication and approval records.
- Each `audit batch:` warning line, which counts the refused spooled records.
- The loss marker that `straza doctor` reports on each client.
- `straza_sink_deadletter_total`, for sink deliveries that failed.

Verify the chain as well. It cannot show a record that never reached it, so on the server these counters and log lines are the only trace of a loss.

Report anything that contradicts this page through [Reporting a vulnerability]({{< relref "security/reporting.md" >}}).
