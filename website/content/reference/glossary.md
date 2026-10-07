---
title: Glossary
description: Each word the docs use, with its one meaning and where it shows up in the product.
pagetype: reference
weight: 70
draft: false
keywords: glossary terms definitions view server endpoint enroll token change feed client assertion client credentials draft knowledge pack sentinel
---


Look a word up here when a page uses it and you are not sure what it means. Each word has one name and one meaning, and every other page uses it that way. The terms are grouped by the part of the product they belong to, and the last two tables cover the ordinary words that do several jobs and the words the product retired.

When a definition here and a specification disagree, the specification wins and this page has a bug. [How a tool call is decided]({{< relref "concepts/how-straza-works.md" >}}) follows one governed call through the product.

## Identity



| Term | Definition | Detail |
|---|---|---|
| user | A principal record: a person, an AI agent or a service account. | Provisioned over SCIM, or created through the admin API or the console. The record tracks its origin. |
| person | A user who is a human and signs in interactively. | The wire keeps `kind: human`, and `user_type: human` when the identity manager sets the typology. A person enrolls a machine with the device flow, may decide approvals and may sponsor AI agents. |
| AI agent | A principal that acts for a person and has no interactive login. | The wire keeps `kind: nhi` with `user_type: agent`, and `strazactl users create-nhi` creates one. It enrolls headless, with a registered Ed25519 key or with client credentials at your identity provider. Its sessions carry no device, it never decides an approval, and it is refused on every admin route whatever role it holds. |
| service account | A non-interactive principal that acts for no one in particular. | The wire keeps `kind: nhi` with `user_type: service`. It enrolls the same headless way, carries no agency mode and no sponsor, and never decides an approval. |
| sponsor | The person an identity manager records as accountable for an AI agent, or for a person such as a contractor. | A rule that routes to the person behind the agent sends the request to the sponsor. A sponsor may also let their agents run on their own connections to an MCP server. |
| identity manager | The system that decides who exists, which roles they hold and who sponsors whom, such as midPoint or Okta. | It pushes to `/scim/v2/` with an admin API token that carries the scopes `scim:read` and `scim:write`. People sign in at the identity provider, not at the identity manager. |
| identity provider | The OIDC system people sign in at, such as Keycloak. | Named by `oidc.issuer` in the enterprise profile. The standalone profile is its own issuer. |
| role | The one identity object: what access rows, policy and approvals key on. | Kinds `business`, `application` and `approver` on the access plane, and kind `straza` for rights inside strazad itself. |
| application role | A role of kind `application`: it holds the access row to one MCP server. | The only kind an access row attaches to and `match.roles` names. One made with a server belongs to that server, as a server-owned role. One made without a server is for policy rules only, and a new access row for it is refused. A business role composes application roles. |
| business role | A role of kind `business` that composes application roles for a job, such as developer. | It holds no access row of its own, and `match.roles` refuses it. A holder of it holds every application role it composes. |
| approver role | A role of kind `approver`: the right to decide approvals and nothing else. | It has no access rows and no knowledge packs, it composes nothing and nothing composes it, and it is the only kind `approve.roles` may name besides `straza-admin`. |
| Straza role | A role of kind `straza`: rights inside strazad itself, such as `straza-admin`. | The control plane. It never reaches an agent tool, and `match.roles` refuses it. |
| reserved role names | Names beginning with `straza-` or with `mcp-admin-`, refused at role creation. | The product's own roles, `straza-admin`, `straza-global-mcp-admin`, `straza-enroll-mobile`, `straza-enroll-browser` and `straza-draft-config`, are refused at deletion as well. The `mcp-admin-` prefix belongs to the server admin role Straza names with each MCP server. A delegated admin role is a role you name, mapped through `admin.roleAreas`. |
| server admin role | The Straza role that Straza names together with an MCP server. | Kind `straza`, named with the prefix `mcp-admin-` and then the server's name, such as `mcp-admin-demo-tools`. Straza names it once, at registration, and never renames it. Deleting it on its own is refused, and removing the server takes it and every membership of it. |
| server admin | Someone who administers one MCP server and no other. | Holds that server's admin role, directly or through a business role that composes it. Changes the server, its credential, its settings and its secrets, pauses and resumes it, probes it, reads its logs, and defines the roles their server owns. Never who holds those roles, never its policies, never its runtime kind or its address, and never the removal of the server itself. [Delegate one MCP server]({{< relref "guides/operate/delegate-one-server.md" >}}) sets one up. |
| server-owned role | An application role that belongs to one MCP server and reaches that server alone. | Every application role made with a server, by a global admin or by that server's admin. Named with the server's folded name, a hyphen and a suffix, as in `demo-tools-readers`, and recorded as owned by that server, which every admin action checks. A server admin names its tools one by one, and only a global admin gives it the glob that means every tool including tools added later. Composes no other role, though a business role may compose it. Once anyone holds it, the tool list is frozen for the server admin and the delete is refused with the holder count. It goes away with its server, one record per membership that ends. [Access per role]({{< relref "guides/serve-mcp-apps/catalogs-per-role.md" >}}) creates one. |
| global MCP admin role | The product role `straza-global-mcp-admin`, which administers every MCP server. | Created at boot on every install and undeletable, with the fixed meaning `apps:read` and `apps:write`, so an identity manager imports one specific role with no configuration. `admin.roleAreas` refuses an entry for it, because a fixed meaning must not be narrowed. |
| SCIM group | How an exported role renders on SCIM: the protocol's Group shape, not a second object. | `displayName` is the role name and `members` are its direct holders. Every role renders, Straza roles included. |
| role assignment | The edge that assigns a role to a user. | Optionally time-boxed, with origin `scim` or `admin`. A SCIM members write creates or removes the same edge. |
| role composition | A role that gives another role to everyone who holds it. | The wire words are `implies` and `implications`. Cycle-checked at creation and resolved into the session's effective roles at check-in. |
| subject | A session's resolved identity: user, effective roles, attestation and identity typology. | Computed when the session is minted and refreshed at check-in. Policy match and catalog filtering read it. |
| session | A governed agent runtime bound to a subject and, for a person, an enrolled machine. | Carries the session token of 300 seconds. Revocable one at a time, in bulk, or through the user. |
| enrolled machine | A machine enrolled for one user, bound into that person's sessions. | The wire and `strazactl devices` call it a device. It holds the device credential, wire field `device_token`, which opens only the check-in. A device credential lasts 30 days from its issue or its last renewal. A check-in renews it once it is past half that life, so a machine that stops checking in loses it 15 to 30 days after its last check-in. |
| enrollment | The once-per-machine bootstrap that creates the local identity. | `straza enroll` runs the OIDC device flow, and `--headless` enrolls an AI agent or a service account with its key or its client credentials. [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) walks it. |
| client credentials | The OAuth 2.0 grant a program uses to get a token with no person signing in. | An AI agent uses it at enrollment and at every session start. Under enterprise it can present `STRAZA_CLIENT_ID` and `STRAZA_CLIENT_SECRET` at your identity provider, and under either profile a client assertion signed by its own key at Straza's own sign-in. A server whose manifest sets `credential.agents: client_credentials` gets an agent's own token for its calls the same way. |
| client assertion | A short-lived signed JWT that proves a client holds a private key, sent in place of a client secret, as RFC 7523 defines. | An AI agent with a registered Ed25519 key signs a fresh one at every session start, valid for at most 5 minutes and accepted once. The audit record of that sign-in names `client-assertion` as the way it signed in. strazad signs one with its own client assertion key when it gets an agent's token at your identity provider for a server. |
| check-in | The periodic sync from client to server, bounded by the token lifetime. | Refreshes the session token and resolves the roles again, keeps the attestation level fixed at session start, and discovers a revocation by its refusal. |
| admin API token | The one long-lived credential, prefix `wat_`, for scripts, connectors and the SCIM client of an identity manager. | Minted once in plaintext by `strazactl api-token create` or the console, then stored hashed. Scoped to areas, such as `apps:write`, or to `full`, and `scim:read` with `scim:write` opens `/scim/v2/`. Its own audit actor, with no user and no roles behind it. |
| kill switch | Deactivation in the identity manager ends every session of that user. | SCIM `active: false` or a DELETE revokes and removes the user's per-user grants. The push channel carries it to connected daemons, and the poll within the token lifetime is the backstop. |
| lock | A Straza-side lock that an identity manager's reactivation cannot lift. | Set by `strazactl users lock` or `POST /v1/admin/users/{id}/lock`. It survives SCIM `active: true` and lifts only by an explicit unlock. |
| identity typology | What an identity is: `userType`, `agencyMode`, `sponsor`, `swarmId` and `ephemeral`. | Mastered by the identity manager over SCIM, and policy sets select on it with `match.identity`. An unset typology matches no identity selector. |
| bootstrap admin | How the first administrator exists. | Standalone prints the `admin` password once at first boot. Enterprise assigns `straza-admin` to the first login that matches `oidc.bootstrapAdmin`. |
| break-glass admin | The emergency account for a lockout, created on every profile. | Its password is printed once at first boot, and every login by it is logged as an alarm. In the enterprise profile it signs in at the server's own emergency sign-in page, never at the identity provider, and an identity provider account of the same name is refused and never linked to it. |

## Catalog



| Term | Definition | Detail |
|---|---|---|
| MCP server | A tool server registered with Straza and governed by it. | Declared by a manifest, and a stopped server exposes nothing. The wire and path word for it is `app`. |
| manifest | The MCP server's declaration: identity, runtime, credential, tools and exposure. | Fixed by the app manifest specification, with `kind: App`. The runtime is `command`, `remote` or `oci`. |
| apps directory | The directory strazad watches for manifest files. | `apps.dir`, by default `apps` under the data directory. A file there proposes a draft that a person publishes, and never installs, changes or removes a server by itself. |
| exposure | The manifest's upper bound on which of the server's tools an access row may reach at all. | An access row intersects with it and never exceeds it. |
| tool | One callable capability of an MCP server. | On the combined endpoint `/mcp` agents see it as `server__tool`, such as `scout-tools__echo`, and on the server's own endpoint, such as `/mcp/scout-tools`, by its own name. The SCIM rendering writes `server:tool`. Access rows and `toolNames` matchers name it by its bare name, such as `echo`. |
| access row | The row that gives an application role tools on one MCP server, called access in the console and `bindings` on the wire. | Tool names, or the glob for every tool including tools added later. Without access the tool does not exist for the role, unlisted and unknown on call. With it the tool runs unless a policy gates it. |
| catalog | What one session's subject can see. | Access rows intersected with exposure and the live inventory, minus the tools a policy denies. |
| catalog preview | The debugger for why a role sees or misses a tool. | `strazactl catalog preview --role` prints a status and reason per tool, and the console's access check shows the same chain. |
| policy filter | Catalog hiding of tools whose call would be an unconditional deny. | `apps.catalog.policyFilter`, on by default. Hiding shapes context, and every call is checked again regardless. |
| MCP | The Model Context Protocol, how agents talk to tool servers. | The gateway speaks it on both sides, and a conformant client sees no difference. |
| gateway | The MCP-fronting enforcement point inside strazad. | Filters `tools/list`, decides `tools/call`, adds credentials on the server side and emits the audit event. |
| server endpoint | One MCP server's own address on the gateway, `/mcp/` followed by the server's name. | Tools keep the server's own names there, and the server's views are served only there. `straza mcp` with a server's name fronts it for a chat app. |
| view | An HTML screen that an MCP server links from a tool, which a chat app can show beside the tool's result. | An MCP Apps `ui://` resource. The gateway serves a view only on the server endpoint, to a caller who can see a tool that links it, and only while the manifest's `straza.exposure.views` is true, which it is not by default. [Show MCP Apps views]({{< relref "guides/serve-mcp-apps/show-views.md" >}}) turns them on. |
| knowledge pack | Text bound to a role, which every agent session of the role's holders starts with in its context. | Created with `strazactl packs create` and bound to an application or business role. The check-in answer carries it, with a checksum the server computes. It tells the agent things and enforces nothing. [Knowledge packs]({{< relref "guides/govern-an-agent/knowledge-packs.md" >}}) shows the commands. |
| native tools | Straza's own tools, served by the gateway as the built-in `straza` app. | Agents see them as `straza__<tool>`. `approval_request`, `approval_status` and `approval_await` let an agent ask for a ticket and follow it. `draft_submit` and `draft_status` are listed to a session whose roles include `straza-draft-config`. |

## Credentials



| Term | Definition | Detail |
|---|---|---|
| credential kind | What an MCP server's upstream needs from each call, set by the manifest's `credential.kind`. | `none`, the default, `static`, `token` or `oauth`. The gateway adds the credential, and no agent ever holds it. |
| shared secret | The one secret a `static` server receives on every call. | Stored with `strazactl apps secret set`, sealed under the key-encryption key, and never read back. |
| role override | A shared secret stored for one role, used for that role's calls in place of the server's own. | `strazactl apps secret set <server> --role <role>`. Only an application role may hold one. |
| each caller's own token | `credential.kind: token`: each person pastes their own vendor token once. | Stored per person with `straza connect` or on the Credentials tab of the self-service page. Valid on the `remote` runtime only. |
| each caller's own sign-in | `credential.kind: oauth`: each person signs in once through a provider in strazad's config. | The provider is named under `oauth.providers` and in the manifest's `credential.oauth.provider`. Nobody can finish a sign-in for an AI agent. |
| agents fallback | The manifest's `credential.agents`: what an AI agent with no credential of its own runs on. | `own`, the default, means nothing, `sponsor` its sponsor's connection once the sponsor allows it, `shared` the server's shared secret, and `client_credentials` a token of the agent's own client at the provider. |
| fingerprint | The first four hex characters of a stored value's SHA-256. | Tells which value is stored without reading it. Every list shows a secret by scope and fingerprint, never by value. |

## Policy



| Term | Definition | Detail |
|---|---|---|
| policy set | The policy document: `match` says which sessions, `rules` say what happens. | YAML with `apiVersion: straza.dev/v1beta1` and `kind: PolicySet`. Applied, it is stored off until activated, and the live sets compile into the snapshot. |
| match | Which sessions a policy set governs. | Application roles or users by name, plus identity typology. OR within a list, AND across lists, and an omitted match means every session. |
| rule | One decision step inside a set. | Events, tool and `apps` selectors, matcher blocks and an effect, with an optional mode, `require` and reason. |
| event | A canonical happening from the hook profile that a rule evaluates. | `tool.pre` is the default decision point, and the hook profile defines the kinds. |
| matcher blocks | The pattern vocabulary of a rule: `command`, `paths`, `toolNames` and `interpreters`. | Each has an allow and a deny side, deny consulted first. A rule whose matchers miss produces no verdict at all. |
| interpreter | An event attribute marking interpreter invocations such as `python3` or `bash -c`. | The `interpreters` side is consulted only when the attribute is present. |
| effect | What a rule declares, allow or deny, and the verdict it produces for one event. | Deny wins across firing rules, and a profile default applies when nothing fires. |
| combining | How verdicts across firing rules merge. | Deny overrides. An `mcp.call` with no rule runs when a role has access to the tool and is denied otherwise. |
| priority | Orders which rule a decision is reported under among winners of the same effect. | It never changes which effect wins, and a rule that holds a call for approval is always the one named. |
| mode serverCheck | A winning allow that also needs the server online. | The local decision point calls `POST /v1/decide`, and an unreachable server means deny, whatever the grace period says. |
| mode classify | A winning allow that also needs a classifier verdict. | The built-in heuristic decides. Unavailable, error or timeout means deny, and the mode is exempt from the hook's latency target. |
| mode approve | A winning allow that also needs a person's decision. | See the approvals section. A local deny never escalates. |
| mode confirm | A winning allow that the requester alone must confirm. | The rule names no deciders, and an autonomous agent's call is denied instead. |
| require | Session preconditions checked before the matchers. | `attestation` at a minimum level, `harness` by name or minimum version, and `deviceCert`. Any failure denies with the rule's reason. |
| reason | The operator's sentence that a deny or hold carries. | Shown to the agent and written to the audit event, so a model can read it and change course. |
| snapshot | The signed, compiled bundle of the live policy sets that every decision point verifies. | Content-addressed. No valid snapshot within the grace period means every governed action is denied. |
| grace period | How long a verified snapshot keeps deciding after the server became unreachable. | `governance.offlineGraceTTL`: zero in the enterprise profile, 15 minutes in standalone, as [Standalone and enterprise compared]({{< relref "reference/standalone-and-enterprise.md#offline" >}}) shows. |
| simulate | Dry-running an event and subject against the live snapshot, with an optional local file overlaid. | `POST /v1/admin/policies/simulate` answers which rule wins and why, with no state change. |
| starter policy | `standalone-starter`, seeded once on a fresh standalone store. | Denies recursive force-delete with a reason. It is deletable and never seeded again, and enterprise seeds nothing. |
| recording | Rule-opted-in recording of prompts and replies onto the audit chain, word for word or with secrets masked. | Never silent, because the session is told. The chain stores hash and size, the text lives in the transcripts store, and the YAML key is `capture:`. |
| Rego escape hatch | Advanced rules written in Rego where the YAML vocabulary ends. | Monotonic, so Rego can only tighten a decision. A module may not call a built-in that reaches the network, a file or the environment, or one whose single call can run far past the deadline, and [PolicySet grammar]({{< relref "reference/policyset-grammar.md#escape" >}}) lists them. The modules of one decision share a 100 ms deadline, checked between evaluation steps, past which the decision is a deny. |
| draft | A proposed change to MCP servers, roles, access rows or policy sets, checked against live state and published by a person in one step. | The console's Drafts area and `strazactl drafts create -f` store one, and a file in the apps directory and an agent's drafting tools propose one too. Only a person publishes it, and its documents go live together or not at all. [Propose and publish a change]({{< relref "guides/changes/propose-and-publish.md" >}}) follows one. |

## Approvals



| Term | Definition | Detail |
|---|---|---|
| approval request | The record a `mode: approve` escalation creates. | Requester, rule, set, lane, summary and justification, with its notify routing frozen at creation. |
| approver roles | `approve.roles`: who may decide, per rule. | Only approver roles or `straza-admin`. Membership is resolved on the identity side at decision time. |
| deciders | Who may decide one request: the requester's sponsor and the holders of the named approver roles. | Set by `approve.roles` and `approve.deciders`. An AI agent is never one. |
| eligible decider | A person who holds a listed approver role, or the person behind the agent. | The requester is eligible only with `selfApproval: true` or on a record that fell on them, and an AI agent never decides. |
| person behind the agent | Who `deciders: [sponsor]` and a rule that names nobody route to. | An agent's sponsor. A person who runs their own agent and has no sponsor is that person and confirms alone. A person who has a sponsor is decided by that sponsor. |
| selfApproval | Whether the requester may decide their own request. | Off by default. When on, the requester joins the deciders and decides on an enrolled phone or in This browser, and an autonomous agent never gets it. |
| hold | The class that keeps the call waiting for a decision. | The decision window is `timeoutSeconds`, and expiry denies. The gateway holds the upstream call, and the hook answers deny with a retry allowance. |
| single-use exemption | The one run an approval leaves for the held call or its identical retry. | Keyed by person, session, rule and argument hash, and used once on any replica within `retryTTLSeconds` of the decision. |
| ticket | The day-scale class: request now, a later session consumes the grant. | The decision window is `ticketTTLSeconds`. |
| grant | What an approved ticket leaves: one later run of the exact call. | Single-use, found by the call's fingerprint, and valid for `grantTTLSeconds`. |
| justification | The model's stated reason for the call. | Added to the gated tool's schema and shown to approvers, unverified. |
| notify | `approve.notify`: which channels announce a rule's requests. | `console`, `slack` and `push`. Omitted means every configured channel, and it narrows announcements only. |
| channel | A registered notification implementation. | Console, Slack and the phone. Status and a test send live in the console's channels page. |
| push route | An approver device's registered delivery endpoint. | WebPush, UnifiedPush, FCM or APNs, host-allowlisted. The payload is content-free, and the details are fetched over the authenticated API. |
| enroll token | The one-time token that pairs a phone or a browser with a person as an approver device. | An admin mints one with **Add a phone** in the console or with `strazactl approvals enroll-token`. A person who holds `straza-enroll-mobile` or `straza-enroll-browser` mints their own on the self-service page. It lasts 10 minutes and works once, and the pairing's audit record carries `actorVia: enroll-token`. |
| phone approval | Deciding on an enrolled phone with a hardware-bound key. | Straza sends the request to the approver's phone, and the decision comes back signed and single-use. |
| reminder | The one near-expiry nudge for an undecided ticket. | Replaces the original notification and is sent once for the whole deployment. |
| first decision wins | Any eligible decider, on any surface, exactly once. | Other surfaces reconcile to it, and there is no quorum. |

[Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md#approvals" >}}) gives the default and the limit of each approval window, and what happens when it runs out.

## Enforcement and runtime



| Term | Definition | Detail |
|---|---|---|
| hook lane | Harness hooks deciding local tool use on the agent's machine. | The target is a 95th percentile under 25 ms, process start included. Approve and classify escalations are exempt. |
| gateway lane | MCP calls decided, and held, at the gateway. | Where approvals hold the upstream call and credentials are added. |
| PDP | The policy decision point, where a verdict is computed. | Locally against the verified snapshot with no database and no network call, or at the server for escalations. |
| harness | The agent runtime being governed: Claude Code, Codex CLI, Gemini CLI or a Python agent. | Integration depth differs per harness, and `require.harness` pins a name and a minimum version. |
| attestation | The integrity level of the client install, carried on the session. | `managed`, `advisory` or `none`, computed at session start and fixed for the session. A hash mismatch makes it `none`, and policy decides what that means. |
| fail closed | Unreachable or unverifiable means deny for governed actions. | Snapshots past grace, `serverCheck`, `classify`, approvals and enrollment. The audit sentinel is the one exception, and it blocks nothing. |
| deny with reason | Every deny carries the operator's reason to the agent and to the record. | The model can read it and change course. |
| revocation push | The channel that carries a kill to connected daemons. | Target-scoped subjects over the `/v1/push` stream on the main listener. The poll within the token lifetime is the backstop. |
| audit chain | The append-only record of decisions and identity events. | Written after the decision and never in its path. `strazactl audit verify` recomputes it, and sinks forward it. |
| sink | A forwarder of the event stream to a system you already run. | A webhook with an HMAC signature, or a file. Delivery is at least once from a durable consumer, and parked deliveries can be replayed. |
| audit sentinel | An off-path consumer that judges sessions after the fact. | Rule-based detectors whose verdicts join the chain. It fails open with an alarm and is off by default. [The sentinel]({{< relref "guides/audit/sentinel.md" >}}) turns it on. |

## Platform pieces



| Term | Definition | Detail |
|---|---|---|
| strazad | The server: decision service, gateway, SCIM endpoint, console and self-service page in one static binary. | Stateless on the data plane. Replicas share the store, the event bus and the key-encryption key. |
| strazactl | The operator's command line. | Everything the console does, scriptable, logging in through the same device flow. |
| straza | The client binary on the agent's machine. | Commands such as `enroll`, `install`, `hook`, `mcp`, `exec`, `doctor` and `daemon`, listed in full under Command line. It opens no port. |
| profile | The deployment personality of one binary. | `standalone` suits one machine or a small team and needs nothing else installed. `enterprise` expects your identity provider and a Postgres database. [Standalone and enterprise compared]({{< relref "reference/standalone-and-enterprise.md" >}}) lists every difference. |
| store | The system of record, Postgres or SQLite. | Never read on a request path. Decisions come from the snapshot and the session state. |
| event bus | NATS JetStream carrying the audit stream and the control events. | Runs in-process with no socket, in either profile, until `events.embedded` is `false` and `events.url` names an external NATS server, which several replicas share. `STRAZA_EVENTS_URL` sets both at once. |
| key-encryption key (KEK) | The key that seals secrets at rest, `secrets.kekFile`. | The third thing replicas share. Rotate it under your key management practice. |
| console | The embedded web administration at `/console/`. | Over the same admin API as strazactl. |
| change feed | The ordered list of changes to users, roles, MCP servers, their tools and access rows that an identity manager's connector reads to stay in step. | `GET /v1/admin/changes` pages it with a cursor that only moves forward, and a reader may see one change twice. A connector reads it with an admin API token and still runs a full reconciliation from time to time. |
| self-service page | The end-user and approver web surface at `/self-service/`, with its Requests, Credentials and This browser tabs. | Where a person decides in a browser, sets the credentials their calls use and enables this browser as an approver device. The server's root path lands there, and `/approvals/` redirects to it. |
| enterprise demo stack | The one-command enterprise demonstration. | Keycloak, midPoint, strazad, demo agents and an optional SIEM, seeded. Its names, such as the `demo-tools` server, appear only on pages that introduce them as the demo stack's. |

## The overloaded words


Most confusion in this product is a few ordinary words doing several jobs. When a sentence is ambiguous, name the part of the product it is about.

| Word | Its jobs | How to tell them apart |
|---|---|---|
| role | `match.roles` selects sessions, `approve.roles` names deciders, and an access row names the role that gets the tools. | Three positions and one object. The section a sentence is about names the job. |
| group | The SCIM rendering of an exported role, and your identity manager's own groups. | On `/scim/v2/Groups` it is a role in the Group shape. The identity manager's own groups never cross the wire. |
| app | The wire and path word for a registered MCP server, and the mirror object an identity manager may keep for certification. | Sentences say MCP server, while `app` stays in `kind: App`, `apps.catalog.*`, `strazactl apps ...` and `app.yaml`. The mirror is read-only, and touching it changes nothing in Straza. |
| policy | The policy set documents, and colloquially the whole policy side. | Never includes access rows: access opens a tool, and policy gates it. |
| approve | `mode: approve`, the `approve` block that configures it, and the act of a person. | The block is legal only with the mode, and the act belongs to whoever the roles resolve to. |

## The words we do not say


The table above disambiguates words that stay. This one lists the words the product retired, so that an older page, a saved search or a support thread still leads you to the current name.

| Word | What it used to mean | Say instead |
|---|---|---|
| seat, package, access package, entitlement, team | a business role, or the people who hold one | business role, or the role by name |
| grant, bind, binding | the row that gives a role tools on an MCP server | access row, and the verb is give a role access to a server. The command names `apps bind` and `apps unbind` and the wire word `bindings` stay, and grant stays for the single-use grant a ticket leaves and for an OAuth grant |
| implies, implication | one role carrying another | composes, composition. The wire words `implies` and `implications` stay |
| in place, standing approval, held, blocked, waits for a person | the state of a call waiting for a decision | Waiting, with the class hold or ticket under it. The postures are allowed, needs approval, denied and checked |
| pool | the people a rule routes an approval to | deciders |
| minted, of a role | the admin role a server names at registration | server admin role, or global MCP admin role. A token, a code, a key and a certificate are still minted |
| IdM, IdP | the systems either side of Straza | identity manager for roles, membership and sponsors, and identity provider for the login. Both short forms stay in this glossary and in product output a page quotes |
| NHI, non-human identity | a principal with no interactive login | AI agent, or service account for `user_type: service`. The wire keeps `kind: nhi`, `user_type` and `strazactl users create-nhi` |
| app, in a sentence | a registered MCP server | MCP server. `app` stays in paths, flags, YAML keys, command names and file names |
| enrol, enrolment | the once-per-machine bootstrap | enroll, enrollment, the spelling the wire uses |
| control-plane role, the straza kind | the kind that carries rights inside strazad | Straza role. The control plane may explain it where a reader first meets it |
| refused, of a policy outcome | a rule turning a call down | denied. Refused stays for an admin action the server turns down, such as a validation refusal or a delete it does not perform |
| capture, as a display word | prompts and replies on the chain | recording, word for word, secrets masked. The YAML key `capture:` and wire words such as `capture.mode` stay |
| PolicySet, in a sentence | the policy document | policy set. `kind: PolicySet` and the title of its specification stay |
| eval stack | the one-command enterprise demonstration | enterprise demo stack, or the demo stack once a page has named it. File paths such as `deploy/compose/eval-stack` stay |
