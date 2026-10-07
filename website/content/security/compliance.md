---
title: Compliance mapping
description: Which NIS2 Article 21(2) measures and ISO/IEC 27001:2022 Annex A controls Straza supplies evidence for, and the mechanism behind each one.
pagetype: reference
weight: 40
draft: false
keywords: compliance iso 27001 nis2 controls mapping ai act evidence
---


A compliance officer or an auditor reads this page to see which NIS2 Article 21(2) measures and ISO/IEC 27001:2022 Annex A controls Straza can supply evidence for, and which mechanism backs each row. It describes evidence the product can supply, and it makes no compliance determination for your organization. NIS2 duties, an ISO/IEC 27001 management system and AI Act obligations depend on the organization, the system and its use, so use the mappings below as input to that assessment.

[Known limits]({{< relref "security/known-limits.md" >}}) lists what these mechanisms leave with you, so read it beside this page.

## NIS2, article 21(2)


| Measure | What Straza gives you |
|---|---|
| (a) risk analysis and security policies | Agent permissions as versioned PolicySet documents, compiled into signed, content-addressed snapshots, so the policy in force at any decision is provable. |
| (b) incident handling | One identity manager disable kills every session, one session can be revoked alone, the chain verifies on demand, and login events feed forensics. |
| (c) business continuity | Stateless replicas, self-validating tokens that survive a restart, and specified fail-closed behavior when the server is unreachable. |
| (d) supply chain security | Which tools each role can see and call, sandboxed tool containers, and upstream credentials injected server-side so a third-party model never holds them. |
| (e) security in development and maintenance | Governs the agents that write code: deny-by-default enterprise profile, interpreter tagging, attestation-gated tokens on developer endpoints. |
| (f) effectiveness assessment | Conformance fixtures replay against the engine, simulation answers what a call would do, and the chain supports control testing. |
| (g) cyber hygiene and training | Knowledge packs deliver role-bound instructions into every matching session. Training records remain yours. |
| (h) cryptography | Ed25519 signatures on tokens and snapshots, TLS 1.2 or newer, server secrets sealed at rest, and a session signing key that rotates. |
| (i) human resources and access control | Joiner, mover and leaver arrive over SCIM 2.0, roles carry tools and policy, assignments can be time-boxed, deprovisioning cascades at once. |
| (j) MFA and secure authentication | Enterprise sign-in is delegated to your identity provider over OIDC, so its MFA applies. Attestation adds a check of the install the client reports. |
| Article 23 incident reporting | Timestamped CloudEvents with principals, forwarded by sinks that deliver at least once. Straza provides the evidence, and the report is yours. |


For (a), a PolicySet is a YAML document you keep in version control. Activation compiles every active set into one snapshot whose id is a hash of its content and whose signature the client verifies before enforcing it.

For (b), a SCIM deactivation, an admin disable and a lock all run the same cascade. It writes a revocation record, revokes every active session, denylists the identity on every replica, pushes to the enrolled machines and writes one identity event on the chain. A SCIM deactivation also removes the user's own OAuth grants and pasted tokens, with one audit record per grant. Every login success or refusal is a chained authentication event, and so is every session that its person signs out, an admin revokes, or the idle or lifetime sweeper closes. The sessions a kill revokes are counted on the kill's identity event. The source address and user agent ride on an authentication event when a client connection produced it, so a session the idle sweeper ends carries neither.


Under (c), tokens are verified locally from cached keys. A client keeps deciding from its verified snapshot for at most the configured grace period, which is zero in the enterprise profile, before it denies everything.

Row (d) rests on the gateway. It builds each role's catalog on the server, runs a tool container read-only with all capabilities dropped and no network unless the manifest asks, and passes the injected credential by environment variable name so it never appears in an argument list.

Under (e), the enterprise profile denies a local tool that no rule allows, and an interpreter invocation is tagged for policy to deny or classify. With the enterprise default, a coding harness gets a session token only from a check-in whose reported hashes match the registry. The hook on a machine where the agent can run anything is advisory and not a security boundary, and an MCP server that a user adds to a harness outside the gateway is that user's responsibility. The part of this row that holds against a hostile agent is therefore the gateway, and [Known limits]({{< relref "security/known-limits.md" >}}) lists both exclusions.


Row (g) rests on [knowledge packs]({{< relref "guides/govern-an-agent/knowledge-packs.md" >}}). A pack is bound to a role that carries tools, never to a Straza role or an approver role, and the check-in response hands every matching pack to the session. A change to a pack writes no audit record today, as [Known limits]({{< relref "security/known-limits.md" >}}) records.

For (h), a session signing key rotation runs in two phases. The new key is staged and every replica holds it before any replica signs with it, and the previous key keeps verifying as retiring until every credential it signed has expired.

Under (i), an assignment carries `valid_from` and `valid_to`, and an expired one stops counting at the next role resolution.

Row (j) has two gaps to know about. The standalone profile authenticates with passwords hashed with bcrypt, and passkeys for that built-in issuer are on the [roadmap]({{< relref "project/roadmap.md" >}}) rather than shipped, a gap that [Known limits]({{< relref "security/known-limits.md" >}}) also lists. In both profiles the break-glass account signs in with a password at the server's own emergency page, outside your identity provider's MFA, and every use of it is alarmed on the audit chain. The gateway enforces the attestation minimum from the token on every call.

## ISO/IEC 27001:2022 Annex A


Annex A of ISO/IEC 27001:2022 lists the controls an organization selects for its management system. These are the ones Straza produces evidence for.

| Control | What Straza gives you |
|---|---|
| A.5.3 segregation of duties | Only a person publishes a draft from the drafts routes, and with `admin.secondPerson` on, a draft whose check lists a risk needs someone other than its author to publish it. An admin API token's direct writes still publish in one request unless their check lists a risk while that setting is on. |
| A.5.15 to A.5.18 access control and rights | Roles fed by your IGA, assignments that only an admin or the identity manager can write, a time box on any assignment, and a deprovisioning cascade. |
| A.5.16 identity management | People, AI agents, service accounts, sessions and devices are records you can list and revoke over the admin API and strazactl. |
| A.5.17 authentication information | Passwords hashed with bcrypt, admin API tokens stored as SHA-256 hashes and shown once, server secrets sealed at rest, a one-time bootstrap password. Values in a server's manifest, its env entries included, are stored in plain text, so a credential belongs in the server's secret (`strazactl apps secret set`) and never in the manifest. |
| A.5.19 to A.5.23 supplier relationships | MCP servers are third-party suppliers: an admin installs each manifest, and the credential broker keeps the supplier credential on the server. |
| A.8.2 to A.8.5 privileged access and secure authentication | Deny by default, role-scoped tool visibility, attestation-gated tokens, per-call checks at the gateway, per-area admin scopes, and a per-server admin role that every MCP server names. |
| A.8.9 configuration management | Managed installs are root-owned. At session start the server checks the hook wiring against the hashes it registers from its own rendering, and checks the binary and the configuration only against hashes an admin registers with `strazactl attestation add`. |
| A.8.15 and A.8.16 logging and monitoring | An append-only hash chain, every admin mutation attributed to its actor and credential, authentication events, Prometheus metrics, and an enterprise audit queue that waits rather than drops when it is full. |
| A.8.24 cryptography | Ed25519 for tokens and snapshots, TLS 1.2 or newer, secretbox sealing, and a two-phase rotation of the session and client assertion keys: staged, active, retiring, retired. |
| A.8.28 secure coding | gosec in the lint job and govulncheck in the CI workflow on every pull request, and invariants pinned by tests that fail the build when broken. |
| A.8.32 change management | A change to a server's manifest, roles, access rows or policy sets over the admin API, strazactl or the apps directory is a draft that the server checks against live state, and its publish is one transaction whose audit records all name the draft. |


For A.5.17, an admin API token is hashed before it is stored, the identity manager's SCIM credential included. The first boot prints the bootstrap administrator's password once, as a warning line in the server log, so every copy of that log holds it until it changes. [Known limits]({{< relref "security/known-limits.md" >}}) lists that log and the plain-text manifest values, each with what reduces it.

Under A.8.2 to A.8.5, `admin.roleAreas` maps a role to scopes of the form `area:read` or `area:write` over the areas that [Delegated admin]({{< relref "guides/operate/delegated-admin.md" >}}) lists. Among them is `drafts`, whose `drafts:read` and `drafts:write` open config drafts. Admin API tokens carry the same scopes, so an auditor reads the chain without touching identity or policy. Delegation by object sits beside that map. Every MCP server names one role when it is registered, and a holder of that role administers that server and no other, so the people who run an MCP server own it without an admin role over the whole install. Both paths write the same records.

For A.8.15, every admin mutation on the chain names who acted and through which credential, whether a login, a session or a named admin API token.


For A.8.9, a managed install reports hashes of its binary, its configuration and its hook wiring, and a reported hash that no registered row allows reads as tampering. The server registers the hook wiring hashes itself, and the binary and configuration count once an admin registers theirs. The hashes come from the client, so the `managed` level shows that the reported files match and does not prove which program reported them, as [Known limits]({{< relref "security/known-limits.md" >}}) records.

Under A.8.16, the enterprise profile blocks a request rather than drop its audit event when the in-memory queue is full, so load alone does not drop a record. [Known limits]({{< relref "security/known-limits.md" >}}) lists the loss of audit records as a limit, and [the security model]({{< relref "security/security-model.md#audit-records-can-be-lost" >}}) names each case in which a record can still be lost. A.8.28 rests on the counting store in the test suite, which fails the build when a request path gains a database read.


For A.5.3, the drafts publish route refuses an admin API token and an AI agent whatever they hold, so a draft a pipeline writes waits for a person. An admin API token keeps its direct writes, each a one-item draft the token publishes in the same request. A token that holds `apps`, `identity` or `policy` at write therefore still changes objects without waiting. [Who may draft and publish]({{< relref "guides/changes/who-may-draft.md" >}}) shows who may write a draft and who may publish one.

With `admin.secondPerson: true` in `straza.yaml`, a draft whose check lists a risk cannot be published by anyone who wrote it, minted the token that wrote it, or sponsors the agent that wrote it. A change that widens access and a server removal both raise such a risk. While the setting is on, a direct admin write whose check lists a risk is refused to every caller until it goes through a draft.

The setting leaves assignments immediate, so a role nobody holds can be given ungated access and then assigned, and your identity manager's approval of assignments closes that path. Nor does the setting cover a credential of the person that an agent can read on that person's machine, the strazactl login and an unexpired ID token alike, which publishes as that person. [Known limits]({{< relref "security/known-limits.md" >}}) lists that reach of an agent with what reduces it.

A server's pause, its secrets and every assignment stay immediate operations, so an incident response never waits. Under A.8.32, the draft is the change request and its publish the change record. `draft.publish` names the publisher, the proposer, the resulting snapshot and the risks acknowledged, every record the publish writes carries the draft's number, and `strazactl drafts revert` undoes a publish through a new draft. A file in the apps directory proposes a draft that a person publishes, and deleting the file proposes the server's removal the same way, so the directory never changes a live server by itself. [Propose and publish a change]({{< relref "guides/changes/propose-and-publish.md" >}}) walks a draft from start to publish.

## The EU AI Act


There is no AI Act product certification. Conformity for a high-risk system is mostly the provider's own assessment, and a vendor who says a product makes you compliant is overclaiming. Straza is a deterministic system of rule matching, token cryptography and hash chains. No shipped binary performs model inference, the classify mode is a heuristic, and the audit sentinel is rule-based detection, so the Act's duties fall on your agents and their models and not on this software. This page does not claim compliance on your behalf.


What Straza gives an operator whose agents do fall under the Act is the substrate its duties ask for. Every session is bound to a person or a governed AI agent, so the record of on whose behalf an agent acted exists. The hash chain logs every governed call with its command, arguments, decision and policy snapshot. An approval gate puts a named person's approval in front of a specific action, the kill switch stops an agent from the identity system, and a sink keeps the record for as long as you decide.

An approval gate is an oversight control, and neither this page nor the product describes it as making an agent's output human. The approval is as human as the approver's credentials. An admin login can enroll a new approval device for its own user, so an AI agent that can use its person's admin login can enroll a device of its own and decide the requests that person may decide, as [Known limits]({{< relref "security/known-limits.md" >}}) records. Everything provider-side, risk management, technical documentation, conformity assessment and registration, is yours, and Straza supplies evidence into those processes without constituting them.
