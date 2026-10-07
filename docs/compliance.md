# Compliance mapping: NIS2, ISO/IEC 27001, EU AI Act (and neighbors)

No product is NIS2 compliant, ISO certified or AI Act compliant by itself.
NIS2 obligations bind the *operator* (essential/important entities), ISO/IEC
27001 certifies an organization's ISMS, and the AI Act binds providers/deployers of *AI systems* and GPAI
*models* (there is no AI Act product certification at all). What a product can
do is (a) give operators the controls and evidence they need, and (b) be built
the way those frameworks expect. This document maps both directions and lists
the gaps.

Context: **NIS1** (Directive 2016/1148) is repealed; **NIS2** (Directive
2022/2555) applies since 2024-10-18 (member-state transpositions vary, so read
your own country's act). AI coding agents acting on production code and
internal systems squarely fall under "policies on access control", "supply chain security" and "use of secure authentication" for
covered entities, and that is Straza's job.

## 1. What Straza gives a NIS2-covered operator (Art. 21(2) measures)

| NIS2 Art. 21(2) | Straza contribution |
|---|---|
| (a) risk analysis & security policies | PolicySet as versioned, auditable policy-as-code for agent actions; policy snapshots signed and content-addressed (what was enforced, when, provably). |
| (b) incident handling | Kill switch: an identity manager deactivation revokes every session of the person, and connected clients drop them in under two seconds on the demo stack; per-session revocation; a hash-chained audit trail (`strazactl audit verify`). Every web and CLI login success or refusal is a chained `straza.audit.authn` event with source IP and user agent, and so is every session a person signs out or an admin revokes. A session the idle or lifetime sweeper closes is such an event too, without the source IP and user agent. |
| (c) business continuity | Stateless data plane (any pod serves any session), self-validating tokens survive restarts, fail-closed degradation semantics are specified (the fail-closed behavior is described on the [security model](https://docs.straza.ai/security/security-model/) page), Postgres/NATS HA per operator infra. The break-glass admin signs in at the server's own emergency page in both profiles, so an identity provider outage or a provisioning mistake never locks the control plane. |
| (d) **supply chain security** | Which tools each role can see and call, sandboxed tool containers, and upstream credentials injected on the server so a third-party model never holds them. |
| (e) security in acquisition/development/maintenance | Governs the agents doing the development: default-deny profiles, interpreter tagging, attestation-gated tokens on developer endpoints. The hook on a machine where the agent can run anything is advisory and not a security boundary, and an MCP server a user adds to a harness outside the gateway is that user's responsibility. |
| (f) effectiveness assessment | Conformance corpora + assertion-backed demos are replayable evidence; audit chain supports control testing. |
| (g) cyber hygiene & training | Knowledge packs deliver role-bound instructions/guardrails into every session. |
| (h) cryptography policies | Ed25519 signatures (tokens, snapshots), TLS 1.2 or newer, secretbox at rest; the transport defaults are on [TLS and exposure](https://docs.straza.ai/guides/operate/tls-and-exposure/) and the keys on [Keys, certificates and tokens](https://docs.straza.ai/security/keys-certificates-and-tokens/). |
| (i) HR security & **access control** | IGA-native: SCIM lifecycle from the identity manager (joiner/mover/leaver), roles carry policy, tools and knowledge, sessions as principals, time-boxed assignments (`valid_from/valid_to`). |
| (j) **MFA / secure authentication** | Enterprise: sign-in is delegated to your identity provider over OIDC, so its MFA and passkeys apply, and attestation adds a machine factor. The standalone issuer uses passwords hashed with bcrypt, and passkeys for it are on the roadmap. The break-glass account signs in with a password in both profiles, outside the identity provider's MFA. |
| Art. 23 incident reporting (24 h / 72 h) | Audit events are CloudEvents with timestamps and principals; SIEM sinks (an HMAC-signed webhook and a JSONL file, delivered at least once; see [Sinks and SIEM](https://docs.straza.ai/guides/operate/sinks-and-siem/)) feed the operator's reporting pipeline. Straza provides the evidence, not the report. |

## 2. ISO/IEC 27001:2022 Annex A: main touchpoints

| Control | Straza |
|---|---|
| A.5.3 segregation of duties | Only a person publishes a draft from the drafts routes. The drafts publish route refuses an admin API token and an AI agent whatever scopes or roles they hold, and refuses a session a coding harness checked in. The publisher needs standing over every object in the draft, the standing the direct route for that object asks (events revision 37). An admin API token keeps every direct write it has today: each is a one-item draft of the api door that the token publishes in the same request, so automation never waits. `admin.secondPerson` adds the second person. It is file-only, off by default in both profiles, and `GET /v1/admin/config` shows it as `admin.second_person`. While it is on, a publish whose check lists a risk, which a change that widens access and a server removal both raise, is refused to anyone who wrote a revision of the draft, to the person who minted an admin API token that wrote one, followed through tokens that minted tokens, and to the sponsor of an agent that wrote one. A token whose minter cannot be traced refuses every publisher. A direct admin write whose one-item check lists a risk is refused to every caller, root and tokens included, so such a change, a direct `DELETE` of a server included, goes through a draft that a second person publishes. A change whose check lists no risk publishes by its author, and pausing a server stays an immediate operation, so an incident response never waits (NIS2 21(2)(b)). The setting leaves two paths open. Assignments stay immediate operations, so a person can create a role nobody holds with an ungated access row, which widens nothing, and then assign it. The identity manager's approval of assignments closes that path. The second path is any credential of a person that an agent can read on that person's machine, which publishes as that person: the stored strazactl login, whose refusal inside a coding agent stops an honest mistake and not an agent that unsets the variables that mark it, and an unexpired ID token, which the publish route accepts as every admin route does, the straza client's included. The `draft.publish` record names the publisher as `actor` and the author of the first revision as `proposer`. |
| A.5.15 to A.5.18 access control, rights management | Role model fed by IGA, admin-gated grants, time-boxed assignments, immediate deprovisioning cascade that also removes the user's per-user OAuth grants with one audit record per grant, and every role grant that starts or ends, over the admin API, over SCIM or by a user delete, leaves one audit record naming the actor. |
| A.5.15, A.5.18 delegated entitlement definition, kept apart from assignment (also NIS2 21(2)(i)) | The team that owns an MCP server defines the entitlements that reach it and never hands them out. Every application role that reaches a server belongs to that server, whether the server's admin or a global admin made it, so each entitlement names the one server it reaches. A server admin creates roles their own server owns, names the tools each one reaches one by one and deletes the ones nobody holds, only a global admin gives such a role every tool and the tools the server adds later, and every verb is authorized on the stored column `owner_app_id`, never on the role's name. Granting a holder stays with the identity manager over SCIM, a role someone already holds has a tool list the server admin can neither widen nor delete, and a role a server owns implies no other role, so its reach never leaves that server. Each create and delete writes one `roles.create` or `roles.delete` record on `straza.audit.admin` naming the actor, the role and the server (events rev 33), and the SCIM group carries the owning server as an attribute (scim-profile rev 19, on every application role that reaches a server since rev 20) so a campaign can certify the family on its own. |
| A.5.16 identity management | People, AI agents and service accounts are identities, and sessions and devices are records you can list and revoke. AI agents arrive over SCIM with the agent extension that spec/scim-profile defines. Every user created or changed through the admin API or over SCIM, every approver enroll token and enrollment, every approver device a user delete removes (`approver.revoke`), and every AI agent key set or removed writes one chained `straza.audit.admin` record that names who acted (events revisions 40 and 41). |
| A.5.17 authentication information | Passwords hashed with bcrypt, admin API and SCIM tokens stored as SHA-256 hashes, server secrets sealed with secretbox, a one-time bootstrap password. Values in a server's manifest, its env entries included, are stored in plain text, so a credential goes into the server's secret and never into the manifest. The workstation device credential is purpose-scoped and lasts 30 days from its issue or its last renewal. A gated check-in renews it once it is past half that life, so a machine that stops checking in loses it 15 to 30 days after its last check-in, and every renewal is a `straza.identity.updated` renew event. The client assertion key, which signs agents' clients in at the customer's identity provider, is sealed under the sealing key at rest, never leaves the server through any route, record or log line, and is managed by a full administrator only. In the manifest, an env value, an argument, or the user part, query, fragment and path of an address sit in clear in the database, its backups and an apps directory file. Every read masks them, the admin API, strazactl apps export, the console, health reasons, the server log and the error an AI agent reads included. Two gaps remain. A value the secret scan does not flag prints when the server itself writes it to its log. A drafts check still tells a caller who reads a server without apps:write whether a guessed unflagged value equals the stored one. One secret per server moves into a stored credential with credential.inject. |
| A.5.19 to A.5.23 supplier/cloud relationships | MCP servers are third-party suppliers: an admin installs each manifest, and the credential broker keeps the supplier credential on the server. |
| A.5.32 intellectual property rights | For the project: an outside contribution is merged only after each of its authors accepts the Contributor License Agreement on the pull request, and a pull request stays unmerged until the check in `.github/workflows/cla.yml` reports `straza/cla` green for every author. The check judges each author by the GitHub account, never by an email or a name, and refuses material that its author marks as excluded from the agreement. The Apache-2.0 directories `spec/`, `pkg/`, `adapters/`, `plugins/` and `kits/python/` each carry their LICENSE file, and every release archive, the container image and the console carry `THIRD_PARTY_NOTICES.txt` with the notices and license texts of the third-party software Straza ships. |
| A.5.33 protection of records | For the project: each acceptance of the Contributor License Agreement is recorded in a private repository, bound to the GitHub account's numeric id, the agreement's version and the SHA-256 of its text, and confirmed in writing on the pull request. Every run of the check reads the whole history of the pull request and records what an earlier run missed. |
| A.8.2 to A.8.5 privileged access, info access restriction, secure auth | Deny-by-default, role-scoped tool visibility, attestation-gated tokens, gateway re-checks per call. Separation of duties on the admin plane has two axes. By area: `admin.roleAreas` maps roles to per-area grants (`area:read\|write`, 12 areas) and `wat_` admin API tokens carry the same per-area scopes, so an auditor seat reads audit without touching config or identity. By object: every MCP server names one role, minted at registration as `mcp-admin-` plus the server's namespace and name, and its holder administers that server alone, refused elsewhere with the role they would need. The product role `straza-global-mcp-admin` is boot-created and undeletable with the fixed meaning `apps:read, apps:write`, and `admin.roleAreas` refuses to narrow it. A token carries areas only, never a server's role, so the per-server office is always a grant to a person. The break-glass emergency account is one username on its own sign-in page, refused for every other account, throttled per IP and alarmed on every use. Only a person uses the admin API: a user typed as an AI agent or a service account is refused on every admin route, the approve and deny routes included, and on `POST /v1/approvals/self/enroll-token`, whatever roles it holds, with one `straza.audit.authn` failure record. Every admin request on a session re-reads the user's status and the lock denylist, so a disable or a lock binds on the next request. The connect routes and `GET /v1/self/servers` judge a session the same way, and a Slack tap by a person whose Straza user is disabled or locked is refused before any decision, with one `straza.audit.authn` failure record, and the signed phone lane writes such a record when it refuses a disabled or locked person, once per approver token and reason, or a person retyped as an AI agent or a service account, once per decision. |
| A.8.9 configuration management | Managed installs are root-owned. At session start the server checks the hook wiring against the hashes it registers from its own rendering, and checks the binary and the configuration only against hashes an admin registers with `strazactl attestation add`. The hashes come from the client, so the `managed` level shows that the reported files match and does not prove which program reported them. |
| A.8.15 and A.8.16 logging & monitoring | Append-only hash-chained audit, Prometheus metrics, and an async pipeline whose losses are counted and logged, except four that are not counted: a crash or a kill loses the in-memory queue with no trace, a request still running 10 seconds after the stop began can lose its record with no trace, an admin, identity, authentication or approval record that fails to write leaves one warning line and no counter, and a client whose spool cannot be written logs the loss on that machine only. The cases in which a record can still be lost are among the known limits in section 5. Every admin mutation names who acted and through which credential (`actor`, `actorId`, `actorVia`: login, session or API token name, or the enroll credential on an approver enrollment). `straza.audit.authn` records every login success or failure, attestation refusals included, and every session end a person, an admin or a sweeper causes, with `sourceIp` and `userAgent` when a client connection produced it, so a session end the idle or lifetime sweeper causes carries neither. |
| A.8.24 cryptography | See [Keys, certificates and tokens](https://docs.straza.ai/security/keys-certificates-and-tokens/); keys rotate (signing keys staged, active, retiring, retired: `strazactl signing-keys rotate session` stages a key every replica holds before any replica signs with it, and the previous key retires once every credential it signed has expired; the database holds one active session key and one active snapshot key, so replicas that start together sign with the same key; the client assertion key, purpose `client_assertion`, follows the same life cycle with its private half sealed under the sealing key, one staged and one active key enforced by the database, and create, rotate and retire open to a full administrator only, each leaving one audit record). |
| A.8.28 secure coding | For the product: test-first policy/crypto code, gosec + govulncheck in CI, invariants enforced by tests. For the operator: Straza governs AI-written code paths. |
| A.8.29 security testing in development and acceptance | The end-to-end suite in test/e2e-matrix walks scenarios written from the spec, never from the code, against the binaries built from the tree on every pull request: provisioning, enrollment, policy decisions on every hook dialect, approvals, tickets and grants, deprovisioning and the audit chain, with the propagation timers reported, and one scenario on a stack with minute lifetimes proves the token refresh, the credential renewal and expiry, the session lifetime close, the offline grace window and a signing-key rotation under live sessions. The conformance corpora replay in the unit suite and the harness matrix runs the vendor CLIs against the built binaries every week. |
| A.8.32 change management (also NIS2 21(2)(e)) | Every change to a server's manifest, roles, access rows, implications and policy sets made over the admin API, with strazactl or through the apps directory is a draft first. Pausing a server, its secrets and assignments stay immediate operations by design. A file in the apps directory proposes a draft that a person publishes, and a file that goes proposes the server's removal the same way, so the directory never changes a live server by itself. strazad checks a draft against live state and answers a verdict: the refusals that block it, the risks a publisher acknowledges with a tick or by typing the text the check names, the warnings, and who gains which tool on which server. The publish is the change record. One transaction writes the objects, the before and after of each in `draft_changes`, and the audit records, and a draft whose objects moved after its check is refused as stale. `draft.publish` carries the revision, the items, the resulting snapshot, the risk digest the publisher reviewed and the keys of the risks acknowledged, never a typed text or a document, and every record the publish writes carries the same `draft` (events rev 37). `strazactl drafts revert` makes a new draft that undoes a published one, checked like any other. The direct admin routes keep their requests and answers and publish a one-item draft in the same request, so their records carry `draft` too, and a saved edit of a live policy set is a draft that activate publishes. |

## 3. EU AI Act: Reg. (EU) 2024/1689, as amended by Reg. (EU) 2026/1744

The Digital Omnibus on AI, Regulation (EU) 2026/1744, entered into force on
2026-07-27 and moved the dates of the high-risk chapter. Read the dates below
against the consolidated text on EUR-Lex before you cite them in a contract or
a filing.

### 3.1 Does the Act bind Straza itself? No

Straza is a deterministic policy/audit/approval layer: match rules, token
crypto, hash chains, with **no ML inference anywhere in any shipped binary**.
Under Art. 3(1) as construed by the Commission's guidelines on the AI-system
definition (2025-02-06; Recital 12 carve-out for systems operating on rules
"defined solely by natural persons"; discriminator = the capacity to infer,
unchanged by the omnibus), Straza is **not an AI system**, and it is not a
GPAI model. The same holds for the kits under kits/. So: no
provider/deployer obligations, no conformity assessment, no CE marking, no
registration attach to shipping Straza. (The **CRA** is the regulation that
*does* bind the product; see §4.)

Straza does not claim that it makes you AI Act compliant, and adding an
inferential component would change this analysis for that feature.

### 3.2 What Straza gives an AI Act deployer/provider of agent systems

| AI Act | Duty (who, from when) | Straza contribution |
|---|---|---|
| Art. 5 | Prohibited practices (everyone; in force) | Deny lanes enforce an organization's internal bans on prohibited-use patterns; the audit chain proves what agents did and did not do. Marginal by design, since this is an organizational duty. |
| Art. 4 | AI literacy (effort duty; in force) | Knowledge packs push role-bound usage rules into every session; training records remain the operator's. |
| **Art. 50(1)** | Disclose AI nature **and on whose behalf the agent acts** (providers of human-interacting systems, incl. agents per the 2026-07-20 guidelines; **from 2026-08-02**) | Binding each session to a person and on-behalf attribution (per-user impersonation toward governed tools, AI agent identity in the identity manager) is exactly the substrate that disclosure needs. The disclosure UX itself belongs to the agent product. An approval step does not make an agent's output a human interaction. Machine-to-machine traffic with no human-facing output is outside Art. 50(1). |
| Art. 12 | Logging by design (high-risk; 2027-12-02) | Hash-chained CloudEvents audit of every governed call (exact command, arguments, paths, decision, rule, policy snapshot), plus prompt/reply capture. |
| Art. 14 | Human oversight able to intervene/interrupt (high-risk; 2027-12-02) | `mode: approve` gates (console/Slack/mobile), SoD via `selfApproval: false`, a person's decision on their own request accepted only when an enrolled phone or browser signs it (`approval.unsignedOwnDecisions` off, the default), coding-harness sessions refused on every decide route, fail-closed enforcement, kill switch (an identity manager deactivation revokes the sessions in seconds). An admin login can enroll a new approval device for its own user, so an AI agent that can use its person's admin login can enroll a device of its own and decide the requests that person may decide. |
| Art. 19 / 26(6) | Keep logs for at least 6 months (high-risk; 2027-12-02) | Audit chain is **append-only, and no pruning janitor exists in the codebase**; SIEM sinks give long-horizon archival. Transcript and approval stores DO prune: for high-risk deployments raise `governance.captureRetention` and `approval.retention` (defaults 30 d) to at least 4380 h, or rely on the SIEM copy. |
| Art. 26(2) | Assign competent human oversight (high-risk) | Approver roles (their own role kind, so the oversight population certifies as a distinct class), role-gated catalog, and bindings: named humans with actual authority over specific tools. |
| Art. 26(5) | Monitor operation; suspend and inform when risk manifests (high-risk) | Near-live audit, SIEM feed, and the kill switch as the suspend mechanism. |
| Art. 72/73 | Post-market monitoring; serious-incident reporting (high-risk) | Evidence: transcripts search, audit export, `strazactl audit verify` integrity proof. The report itself is the operator's. |

Most AI *coding/ops* agent deployments are not Annex III
high-risk uses. For them, the Act today means Art. 5 + Art. 4, and Art. 50
from 2026-08-02 where agents face humans; the Chapter III rows above become
live only if the use case is high-risk, and only from 2027-12-02.

### 3.3 What Straza does NOT do (AI Act)

Art. 9 risk-management system, Art. 10 data governance, Art. 11 technical
documentation, Art. 15 accuracy/robustness, conformity assessment / CE /
EU-database registration, Art. 27 FRIA. These are provider-side,
organizational, or model-level. Straza supplies *evidence into* those
processes; it never constitutes them.

Sources: Reg. (EU) 2026/1744 (ELI `data.europa.eu/eli/reg/2026/1744/oj`);
Commission Art. 50 guidelines (2026-07-20, IP/26/1653) and Art. 50 FAQ (updated
2026-07-24); Commission AI-system-definition guidelines (2025-02-06);
artificialintelligenceact.eu article texts.

## 4. Product posture (what Straza itself practices)

- Secure SDLC: invariant-driven tests and conformance corpora. CI runs on
  every pull request: golangci-lint with gosec, govulncheck, the test suite
  under the race detector, a coverage floor and the end-to-end suite.
  `make security` runs govulncheck, osv-scanner and gitleaks over the tree
  and its history, and the binaries build with CGO off.
- Supply chain: pinned modules (go.sum), static binaries, `-trimpath`;
  releases ship a syft SBOM per archive, a checksums file signed with
  keyless cosign that covers every archive, and a container image signed by
  digest, relevant for the
  **EU Cyber Resilience Act** (CRA, Reg. 2024/2847: products with digital
  elements; obligations phase in through 2027) which *does* bind
  the product, unlike NIS2.
- Disclosure: SECURITY.md: private reporting through GitHub private
  vulnerability reporting or security@straza.ai, a 72-hour acknowledgment, a
  7-day triage and a 90-day fix target, and coordinated disclosure. Relevant
  to CRA Annex I part II (vulnerability handling).

## 5. Gaps and known limits

Not built yet, and listed on the roadmap: passkeys for the standalone issuer, a
device-bound client certificate, publisher signature checks on app manifests,
and an egress allowlist per MCP server. Formal certifications of the project
are not held.

The known limits of v1.1.0 follow. The
[security model](https://docs.straza.ai/security/security-model/#known-limits)
explains each one and what you can do about it.

- An agent that holds its person's login can enroll an approval device.
- The hook lane is not a security boundary, and an MCP server a user adds to a
  harness outside the gateway is that user's responsibility.
- Manifest values are stored in plain text.
- A Rego module can run past its time limit, because the server checks the limit
  only between evaluation steps.
- The attestation level comes from the client.
- Audit records can be lost, in the cases the security model lists.
- An old snapshot signing key cannot be withdrawn.
