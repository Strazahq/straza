---
title: Glossary
description: Each word the docs use, in one sentence, with a link to the page that explains it.
pagetype: reference
weight: 70
draft: false
keywords: glossary terms definitions IGA identity provider role access row view server endpoint enroll token change feed client assertion client credentials draft knowledge pack sentinel key enrollment dpop proof sign-in deadline custody straza service background service
---


Look a word up here when a page uses it and you are not sure what it means. Each word has one name and one meaning across these docs, and its entry links to the page that explains it. The terms are grouped by the part of the product they belong to. The last two tables cover the ordinary words that do several jobs and the words the product retired.

When a definition here and a specification disagree, the specification wins and this page has a bug. [How it works]({{< relref "evaluate/how-it-works.md" >}}) shows the core nouns in one picture.

## Identity



| Term | Definition | Detail |
|---|---|---|
| [user]({{< relref "concepts/identities.md" >}}) | One identity Straza knows: a person, an AI agent or a service account. | |
| [person]({{< relref "concepts/identities.md#a-person" >}}) | A human user, who signs in and may enroll a machine, decide approvals and sponsor agents. | User type `human`. |
| [AI agent]({{< relref "concepts/identities.md#an-agent" >}}) | An AI model that acts through tools, either running as its person, as a coding agent does, or as a user of its own. | User type `agent`, when it is a user of its own. |
| [service account]({{< relref "concepts/identities.md" >}}) | A user of its own for a workload that acts for no one in particular, which Straza handles by the agent's rules. | User type `service`. |
| [sponsor]({{< relref "concepts/identities.md#a-sponsor" >}}) | The person accountable for an AI agent or another user, who decides its held calls when a rule names no approver role. | |
| [identity typology]({{< relref "concepts/identities.md" >}}) | What your IGA, or an admin on standalone, records about the kind of a user: user type, agency mode, swarm ID, sponsor and ephemeral. | |
| [IGA]({{< relref "evaluate/identity-and-your-iga.md" >}}) | The identity governance and administration system, such as midPoint or Okta, that decides who exists, who holds which role and who sponsors whom. | Any SCIM 2.0 system can play the part. On standalone an admin does its job. |
| [identity provider]({{< relref "evaluate/identity-and-your-iga.md#sign-in-at-your-identity-provider" >}}) | The OpenID Connect system where people sign in on the enterprise profile, such as Entra ID, Okta or Keycloak. | On standalone strazad signs people in itself. |
| [bootstrap admin]({{< relref "evaluate/identity-and-your-iga.md#the-first-admin-on-enterprise" >}}) | How the first administrator comes to exist on a fresh server. | Standalone prints the password of `admin` once. Enterprise uses the break-glass admin or `oidc.bootstrapAdmin`. |
| [break-glass admin]({{< relref "evaluate/identity-and-your-iga.md#the-first-admin-on-enterprise" >}}) | The local emergency admin that exists on every server, for when nobody else can sign in. | Signs in at strazad's own emergency page, never at the identity provider. |
| [admin API token]({{< relref "security/keys-certificates-and-tokens.md" >}}) | The long-lived credential, starting `wat_`, that strazactl, scripts and your IGA's SCIM client use in place of a sign-in. | |

## Roles



| Term | Definition | Detail |
|---|---|---|
| [role]({{< relref "concepts/roles-and-access.md" >}}) | A named set of rights that people and agents hold, made in Straza and assigned by your IGA, or an admin on standalone. | |
| [application role]({{< relref "concepts/roles-and-access.md#what-a-role-grants-on-a-server" >}}) | A role that grants tools on one MCP server through its access row. | The only kind a policy set's `match.roles` names. One with no server only picks which policy applies. |
| [business role]({{< relref "concepts/roles-and-access.md#the-kinds-of-role" >}}) | A role that groups application roles for a job, such as analyst, and grants everything they grant. | Holds no access row of its own. |
| [approver role]({{< relref "concepts/roles-and-access.md#the-kinds-of-role" >}}) | A role whose holders may decide the held calls of a rule that names it. | Grants no tools, composes no role, and no role composes it. |
| [Straza role]({{< relref "concepts/roles-and-access.md#the-kinds-of-role" >}}) | A role that carries rights inside Straza itself, such as `straza-admin`, instead of tools on an MCP server. | |
| [reserved role names]({{< relref "concepts/roles-and-access.md" >}}) | Role names that start with `straza-` or `mcp-admin-`, which belong to the product and which nobody can make. | |
| [server admin role]({{< relref "concepts/roles-and-access.md#where-roles-come-from" >}}) | The Straza role that Straza makes for each MCP server it adds, named `mcp-admin-` and the server's name, such as `mcp-admin-postgres`. | |
| [server admin]({{< relref "guides/operate/delegate-one-server.md" >}}) | A person who administers one MCP server and no other, through its server admin role. | Makes the roles their server owns, and never decides who holds them. |
| [server-owned role]({{< relref "concepts/roles-and-access.md#where-roles-come-from" >}}) | An application role that belongs to one MCP server, carries the server's name and a hyphen, and reaches that server alone. | Every application role made with a server is one. |
| [global MCP admin role]({{< relref "concepts/roles-and-access.md#where-roles-come-from" >}}) | The product role `straza-global-mcp-admin`, which administers every MCP server. | |
| [SCIM group]({{< relref "concepts/roles-and-access.md#who-decides-who-holds-a-role" >}}) | How a role reaches your IGA over SCIM: its holders as members, beside a read-only account of what it grants. | |
| [role assignment]({{< relref "concepts/roles-and-access.md#who-decides-who-holds-a-role" >}}) | The link that gives one user a role, made by your IGA, or an admin on standalone. | Takes effect at once and is never a draft. |
| [role composition]({{< relref "concepts/roles-and-access.md#the-kinds-of-role" >}}) | One role giving every role it composes to everyone who holds it, as a business role does. | The wire words are `implies` and `implications`. |

## Machines and sessions



| Term | Definition | Detail |
|---|---|---|
| [session]({{< relref "concepts/machines-and-sessions.md" >}}) | One run of a coding tool or an agent, whose calls Straza decides under one short-lived token. | |
| [subject]({{< relref "concepts/machines-and-sessions.md#a-session-and-its-token" >}}) | Who a session acts as: its user, the user's roles with every role they compose, the attestation level and the identity typology. | |
| [enrolled machine]({{< relref "concepts/machines-and-sessions.md#enrolling-a-machine" >}}) | A person's machine registered with strazad, whose key ties each of their sessions to that machine. | The wire and `strazactl devices` call it a device. |
| [enrollment]({{< relref "concepts/machines-and-sessions.md#enrolling-a-machine" >}}) | The step, once per machine, that registers a person's machine and its key with strazad. | An agent with its own identity enrolls headless, with its own key and no machine. |
| [browser sign-in]({{< relref "concepts/machines-and-sessions.md#enrolling-a-machine" >}}) | A person's sign-in in a browser on the machine being enrolled, which names that machine's key. | A sign-in with a code names no key, and enterprise refuses it by default. |
| [key enrollment]({{< relref "concepts/machines-and-sessions.md#the-machine-key" >}}) | An enrollment bound to an Ed25519 key that the Straza service makes and that never leaves the machine. | |
| [custody]({{< relref "concepts/machines-and-sessions.md#the-machine-key" >}}) | Who holds a person's machine key: the Straza service under its own OS account, or the service running as the person. | `account` or `user`. Enterprise refuses `user` by default. |
| [DPoP proof]({{< relref "security/credentials-and-sessions.md" >}}) | A short JWT, per RFC 9449, that a key signs over one request and a strazad nonce, so a copied token works nowhere else. | |
| [sign-in deadline]({{< relref "concepts/machines-and-sessions.md#enrolling-a-machine" >}}) | How long a key enrollment lasts, counted from the sign-in that made it: seven days by default, never renewed by use. | |
| [client credentials]({{< relref "concepts/machines-and-sessions.md#an-agent-with-its-own-identity" >}}) | The OAuth 2.0 grant a program uses to get a token with no person signing in. | An agent uses it with its key at strazad, or with a client secret at your identity provider. |
| [client assertion]({{< relref "security/keys-certificates-and-tokens.md" >}}) | A short-lived JWT signed with a private key and sent in place of a client secret, as RFC 7523 defines. | An agent with a registered key signs one at each session start. |
| [check-in]({{< relref "concepts/machines-and-sessions.md#a-session-and-its-token" >}}) | The step in which the Straza service proves a session to strazad and gets a session token, with the roles resolved again. | |
| [attestation]({{< relref "concepts/machines-and-sessions.md#a-session-and-its-token" >}}) | The level strazad gives a client install when a session starts: `managed`, `advisory` or `none`. | |
| [kill switch]({{< relref "concepts/machines-and-sessions.md#the-kill-switch" >}}) | Any of three levers that end sessions at once: revoking one session, disabling or locking a user, or revoking a machine. | A deactivation from your IGA over SCIM disables the user. |
| [lock]({{< relref "concepts/machines-and-sessions.md#the-kill-switch" >}}) | A block an admin puts on a user in Straza, which ends their sessions and which a reactivation from your IGA cannot lift. | |
| [revocation push]({{< relref "concepts/machines-and-sessions.md#the-kill-switch" >}}) | The push stream the Straza service holds for each session, which brings a revocation to that machine at once. | |

## Servers and the gateway



| Term | Definition | Detail |
|---|---|---|
| [MCP]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}) | The Model Context Protocol, the way agents talk to tool servers. | |
| [MCP server]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}) | A tool server that sits behind Straza's gateway, known to Straza by its manifest. | The wire and path word for it is `app`. |
| [manifest]({{< relref "concepts/mcp-apps-and-the-gateway.md#a-server-and-its-manifest" >}}) | The YAML document that tells Straza how to reach one MCP server, which tools it exposes, its credential and its objects. | |
| [exposure]({{< relref "concepts/mcp-apps-and-the-gateway.md#the-tools-a-session-sees" >}}) | The manifest's cap on which of a server's tools any access row may reach. | |
| [tool]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}) | One action an agent can take: a tool of an MCP server, or a local one such as a shell command. | Through the gateway an agent sees `postgres__read_rows`. Rules and access rows name `read_rows`. |
| [access row]({{< relref "concepts/roles-and-access.md#what-a-role-grants-on-a-server" >}}) | The row that gives an application role tools on one MCP server, named one by one or as every tool. | Called access in the console and `bindings` on the wire. |
| [catalog]({{< relref "concepts/mcp-apps-and-the-gateway.md#the-tools-a-session-sees" >}}) | The tools one session sees: those the server lists, its manifest exposes and an access row of its roles names, minus what policy hides. | |
| [catalog preview]({{< relref "guides/serve-mcp-apps/catalogs-per-role.md" >}}) | A check that shows which tools a role or a person would see, with a reason for each. | |
| [policy filter]({{< relref "concepts/mcp-apps-and-the-gateway.md#the-tools-a-session-sees" >}}) | Leaving out of a session's tool list the tools that policy denies, which is on by default. | A call to a hidden tool gets the unknown-tool answer. |
| [gateway]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}) | The part of strazad in front of your MCP servers, which decides each call, adds the server's credential and records the decision. | |
| [server endpoint]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}) | One MCP server's own address on the gateway, `/mcp/` and the server's name, where its tools keep their own names. | The combined endpoint `/mcp` puts each server's name in front of its tools. |
| [view]({{< relref "guides/serve-mcp-apps/show-views.md" >}}) | An HTML screen that an MCP server links from a tool, which a chat app can show beside the tool's result. | |
| [knowledge pack]({{< relref "guides/govern-an-agent/knowledge-packs.md" >}}) | Text bound to a role that every agent session of its holders starts with in its context. | It tells the agent things and enforces nothing. |
| [native tools]({{< relref "concepts/mcp-apps-and-the-gateway.md#how-an-agent-connects" >}}) | The tools of `straza`, the gateway's built-in server, which let an agent ask for an approval, wait for it and propose a draft. | |

## Objects



| Term | Definition | Detail |
|---|---|---|
| [object type]({{< relref "concepts/objects.md#what-a-server-declares" >}}) | The one word a server declares for what its tools act on, such as `database` or `space`. | |
| [object]({{< relref "concepts/objects.md" >}}) | One thing a server's tools act on, such as one database, on a server that declares them. | |
| [object list]({{< relref "concepts/objects.md" >}}) | What a server's listing tool last returned, which Straza fetches itself every 10 minutes. | A catalog is a different thing: the tools one session sees. |
| [object marks]({{< relref "reference/object-marks.md" >}}) | The keys a server puts on its tools to declare its object type and how each tool reaches objects. | They only pre-fill a draft. Straza decides with the copy a person pins. |
| [reach]({{< relref "concepts/objects.md" >}}) | How one pinned tool reaches objects: through an argument, through its credential, every object at once, or none. | |
| [covers]({{< relref "concepts/objects.md#what-a-role-covers" >}}) | The line of an access row that says which of a server's objects the role covers: every object, or the ones it names. | |

## Credentials



| Term | Definition | Detail |
|---|---|---|
| [credential kind]({{< relref "concepts/credentials.md#whose-credential-a-call-carries" >}}) | What a server's manifest says each call needs: nothing, one shared secret, each caller's own token, or each caller's own sign-in. | `none`, `static`, `token` or `oauth`. |
| [shared secret]({{< relref "concepts/credentials.md#whose-credential-a-call-carries" >}}) | The one secret a server receives on every call, which an admin stores sealed and nobody can read back. | |
| [role override]({{< relref "concepts/credentials.md" >}}) | A shared secret stored for one role, which that role's calls carry in place of the server's own. | |
| [each caller's own token]({{< relref "concepts/credentials.md#whose-credential-a-call-carries" >}}) | A vendor token each person stores once, which their own calls to that server carry. | Remote servers only. |
| [each caller's own sign-in]({{< relref "concepts/credentials.md#whose-credential-a-call-carries" >}}) | A sign-in each person makes once at a provider an admin registered, which their own calls carry. | Remote servers only. Nobody can finish one for an AI agent. |
| [agents fallback]({{< relref "concepts/credentials.md#whose-credential-a-call-carries" >}}) | What an AI agent with no credential of its own runs on, as the manifest names it. | `own`, the default, which is nothing, or `sponsor`, `shared` or `client_credentials`. |
| [listing credential]({{< relref "concepts/credentials.md#the-listing-credential" >}}) | The credential Straza calls a server's listing tool with, which no tool call ever carries. | |
| [git grant]({{< relref "guides/serve-mcp-apps/git-through-straza.md" >}}) | A short signed token that lets a git server serve one fetch or one push of one repository. | Not the grant an approved ticket leaves. |
| [fingerprint]({{< relref "concepts/credentials.md#where-it-is-kept-and-who-can-see-it" >}}) | The first four hex characters of a stored value's SHA-256, which tell which value is stored without showing it. | |

## Policy



| Term | Definition | Detail |
|---|---|---|
| [policy set]({{< relref "concepts/policy-model.md#rules-and-sets" >}}) | One policy document: a named set of rules with a match that says whose sessions it governs. | `kind: PolicySet` in YAML. |
| [match]({{< relref "concepts/policy-model.md#rules-and-sets" >}}) | The part of a policy set that says whose sessions it governs, by application role, by user or by identity typology. | |
| [rule]({{< relref "concepts/policy-model.md#rules-and-sets" >}}) | One step of a policy set that names the kind of call it looks at, may carry patterns, and allows or denies. | |
| [event]({{< relref "concepts/policy-model.md#how-a-rule-matches-a-call" >}}) | One happening in Straza's own terms that a rule evaluates, such as a tool about to run. | |
| [matcher blocks]({{< relref "concepts/policy-model.md#how-a-rule-matches-a-call" >}}) | A rule's patterns, `command`, `paths`, `toolNames` and `interpreters`, each with a deny side read before its allow side. | |
| [by-name rule]({{< relref "reference/policyset-grammar.md#harness-tools-by-name" >}}) | A rule that names harness tools by their own name, such as a tool a new harness release added. | |
| [interpreter]({{< relref "reference/policyset-grammar.md" >}}) | An event attribute that marks a call to an interpreter, such as `python3` or `bash -c`. | |
| [effect]({{< relref "concepts/policy-model.md#the-outcomes" >}}) | What a rule declares, allow or deny, and so the verdict it gives one event. | |
| [combining]({{< relref "concepts/policy-model.md#how-the-deciding-rule-is-picked" >}}) | How the verdicts of every firing rule merge into one decision, where a deny from any rule wins. | |
| [priority]({{< relref "concepts/policy-model.md#how-the-deciding-rule-is-picked" >}}) | The number that picks which rule a decision is reported under, which never lets an allow beat a deny. | |
| [mode approve]({{< relref "concepts/approvals-model.md" >}}) | A mode that holds a winning allow until a person decides. | |
| [mode confirm]({{< relref "concepts/approvals-model.md#confirm-the-requester-decides" >}}) | A mode that lets only the person behind the request say yes. | An agent with no person behind it is denied at once. |
| [mode serverCheck]({{< relref "concepts/policy-model.md#the-outcomes" >}}) | A mode in which strazad decides the call again with the live policy before it runs. | |
| [mode classify]({{< relref "concepts/policy-model.md#the-outcomes" >}}) | A mode in which a built-in check reads a shell command for a hidden command, such as a download piped into a shell. | |
| [require]({{< relref "concepts/policy-model.md#how-a-rule-matches-a-call" >}}) | Conditions a rule puts on the session, such as a lowest attestation level, checked before its patterns. | |
| [reason]({{< relref "concepts/policy-model.md#the-outcomes" >}}) | The operator's sentence that a rule's deny or hold carries to the agent and into the audit record. | A tool that policy hides answers as unknown, and only the audit record keeps the reason. |
| [snapshot]({{< relref "concepts/policy-model.md#how-the-policy-reaches-each-machine" >}}) | The signed, compiled bundle of every live policy set that each machine and the gateway decide from. | |
| [grace period]({{< relref "evaluate/how-it-works.md#when-straza-cannot-decide" >}}) | How long a machine keeps deciding from its verified snapshot after its session token runs out while strazad is unreachable. | None on enterprise, 15 minutes on standalone. |
| [simulate]({{< relref "guides/write-policy/simulate-and-coverage.md" >}}) | Running one call against the live snapshot with no effect, optionally with a policy file laid over it, to see which rule wins. | |
| [starter policy]({{< relref "guides/write-policy/first-deny.md" >}}) | `standalone-starter`, a policy set seeded once on a fresh standalone server, which denies a few spellings of recursive force-delete. | Deletable. Enterprise seeds none. |
| [Rego escape hatch]({{< relref "reference/policyset-grammar.md#escape" >}}) | Rules in the Rego language for what the YAML rules cannot say, which can only add denies. | |

## Changes



| Term | Definition | Detail |
|---|---|---|
| [draft]({{< relref "concepts/drafts.md" >}}) | A proposed change to MCP servers, roles or policy, which Straza checks against live state and which goes live whole or not at all. | |
| [apps directory]({{< relref "concepts/drafts.md#the-apps-directory" >}}) | A folder that strazad watches for manifest files, where each file that appears, changes or goes becomes a draft. | |

## Approvals



| Term | Definition | Detail |
|---|---|---|
| [approval request]({{< relref "concepts/approvals-model.md" >}}) | The record a held call opens, naming the requester, the rule, the call and who may decide it. | |
| [approver roles]({{< relref "concepts/approvals-model.md#who-decides" >}}) | `approve.roles`: the roles whose holders may decide a rule's requests. | Only approver roles or `straza-admin`. |
| [deciders]({{< relref "concepts/approvals-model.md#who-decides" >}}) | The people who may decide one request: holders of the approver roles the rule names, or else the requester's sponsor. | |
| [eligible decider]({{< relref "concepts/approvals-model.md#who-decides" >}}) | One person among a request's deciders, checked at the moment they decide. | The requester counts only when the rule allows self-approval or the request fell to them. |
| [person behind the agent]({{< relref "concepts/approvals-model.md#who-decides" >}}) | Who a rule that routes to the sponsor reaches: the requester's sponsor, or the requester when they are a person with no sponsor. | |
| [selfApproval]({{< relref "concepts/approvals-model.md" >}}) | A rule setting that lets the requester decide their own request. | |
| [hold]({{< relref "concepts/approvals-model.md#how-long-a-request-waits" >}}) | The approval class for a decision a person makes within minutes. | 90 seconds by default, up to an hour. |
| [ticket]({{< relref "concepts/approvals-model.md#how-long-a-request-waits" >}}) | The approval class for a decision that takes hours or days, which denies the call with a reference and leaves a grant. | 24 hours by default, up to 30 days. |
| [single-use exemption]({{< relref "concepts/approvals-model.md#what-one-approval-covers" >}}) | The one run an approved hold leaves for the held call or its identical retry. | Tied to the session, the rule and a fingerprint of the call. |
| [grant]({{< relref "concepts/approvals-model.md#how-long-a-request-waits" >}}) | What an approved ticket leaves: one later run of the exact call by the same requester, from any session. | Not a git grant. |
| [justification]({{< relref "concepts/approvals-model.md#the-hook-path-and-the-gateway-path" >}}) | The agent's own words on why it needs a held gateway call, which approvers read unchecked. | |
| [notify]({{< relref "concepts/approvals-model.md" >}}) | `approve.notify`: which places announce a rule's requests. | It narrows announcements only, and every place can still decide. |
| [channel]({{< relref "guides/approve/_index.md" >}}) | A way Straza announces requests to deciders: the console, Slack or a push to a phone. | |
| [push route]({{< relref "guides/approve/push-and-connectivity.md" >}}) | The delivery address an approver's phone registers, so Straza can send it a push notification. | |
| [enroll token]({{< relref "guides/approve/phone.md" >}}) | A one-time token that pairs a phone or a browser with the signed-in person as an approver device. | |
| [phone approval]({{< relref "concepts/approvals-model.md#where-they-decide" >}}) | Deciding a request in the Straza approver app, which signs the decision with the phone's key. | |
| [reminder]({{< relref "guides/write-policy/tickets.md" >}}) | The one nudge Straza sends shortly before an undecided ticket expires. | |
| [first decision wins]({{< relref "concepts/approvals-model.md#where-they-decide" >}}) | The rule that one decider, on any surface, decides a request once, and every other surface follows. | There is no quorum. |

## Enforcement and runtime



| Term | Definition | Detail |
|---|---|---|
| [hook lane]({{< relref "evaluate/how-it-works.md#how-a-tool-call-is-decided" >}}) | The path on the agent's machine, where a coding tool's hook asks the Straza service before a local tool runs. | Advisory, because an agent that can run anything can skip it. |
| [gateway lane]({{< relref "evaluate/how-it-works.md#how-a-tool-call-is-decided" >}}) | The path where strazad decides each call to an MCP server at the gateway, and can hold it for a person. | Where Straza holds a boundary. |
| [PDP]({{< relref "evaluate/how-it-works.md" >}}) | The policy decision point, the code that computes a verdict from the verified snapshot. | |
| [harness]({{< relref "evaluate/what-straza-is.md#where-straza-decides" >}}) | The coding tool or agent runtime Straza governs, such as Claude Code, Codex CLI, Gemini CLI or a Python agent. | |
| [fail closed]({{< relref "evaluate/how-it-works.md#when-straza-cannot-decide" >}}) | Ending a governed call in a deny when Straza cannot tell the current state, such as when strazad cannot be reached. | The audit sentinel is the one part that fails open, and it blocks nothing. |
| [deny with reason]({{< relref "concepts/mcp-apps-and-the-gateway.md#what-happens-to-a-tool-call" >}}) | A deny that tells the agent why, in a sentence it reads, so the model can change course instead of retrying. | A tool that policy hides answers as an unknown tool instead, and only the audit record keeps the reason. |

## Audit



| Term | Definition | Detail |
|---|---|---|
| [audit chain]({{< relref "concepts/evidence.md#the-hash-chain" >}}) | The append-only record of every decision and identity event, where each record carries the hash of the one before. | |
| [sink]({{< relref "concepts/evidence.md#delivery-to-your-siem" >}}) | A forwarder that copies audit records to a system you run, such as your SIEM, by webhook or to a file. | |
| [audit sentinel]({{< relref "concepts/evidence.md#conversations-and-the-sentinel" >}}) | An off-path reader of the audit records that judges whole sessions after the fact. | |
| [recording]({{< relref "concepts/evidence.md#conversations-and-the-sentinel" >}}) | Keeping the text of an agent's conversation, which a policy set turns on and the session is told about. | The YAML key is `capture:`. |

## Platform pieces



| Term | Definition | Detail |
|---|---|---|
| [strazad]({{< relref "evaluate/what-straza-is.md#who-straza-is-for" >}}) | The Straza server: decision service, gateway, SCIM endpoint, console and self-service page in one static binary. | |
| [strazactl]({{< relref "reference/cli/strazactl/_index.md" >}}) | The operator's command line, which runs on an admin API token and does what the console does. | It cannot decide a request or publish a draft, because each takes a person. |
| [straza]({{< relref "reference/cli/straza/_index.md" >}}) | The client binary on the agent's machine, with commands such as `enroll`, `hook`, `mcp`, `exec` and `service`. | |
| [straza service]({{< relref "concepts/machines-and-sessions.md#the-machine-key" >}}) | The Straza background service on an agent's machine, which holds the machine's key and every Straza session there. | |
| [profile]({{< relref "evaluate/what-straza-is.md#who-straza-is-for" >}}) | Which of two shapes strazad runs in: `standalone` for one host or a small team, or `enterprise` with your identity provider, IGA and Postgres. | |
| [store]({{< relref "reference/standalone-and-enterprise.md" >}}) | The database of record, SQLite on standalone or Postgres on enterprise. | |
| [event bus]({{< relref "guides/operate/enterprise-shape.md" >}}) | NATS JetStream, which carries the audit stream and control events such as revocations between strazad replicas. | |
| [key-encryption key (KEK)]({{< relref "concepts/credentials.md#where-it-is-kept-and-who-can-see-it" >}}) | The key that seals every stored credential at rest. | |
| [console]({{< relref "guides/console/_index.md" >}}) | The web administration that strazad serves at `/console/`, over the same admin API as strazactl. | |
| [self-service page]({{< relref "guides/approve/browser.md" >}}) | The page at `/self-service/` where a person decides requests in a browser, stores their own credentials and enables the browser as an approver device. | |
| [change feed]({{< relref "guides/connect-identity/midpoint.md" >}}) | The ordered list of changes to users, roles, MCP servers, tools and access rows that your IGA's connector reads to stay in step. | |
| [enterprise demo stack]({{< relref "evaluate/try-it-on-the-demo-stack.md" >}}) | The one-command enterprise demonstration, with Keycloak, midPoint, strazad and governed agents, seeded. | |

## The overloaded words


Most confusion in this product is a few ordinary words doing several jobs. When a sentence is ambiguous, name the part of the product it is about.

| Word | Its jobs | How to tell them apart |
|---|---|---|
| role | `match.roles` selects sessions, `approve.roles` names deciders, and an access row names the role that gets the tools. | Three positions and one object. The section a sentence is about names the job. |
| group | The SCIM rendering of a role, and your IGA's own groups. | On `/scim/v2/Groups` it is a role in the Group shape. Your IGA's own groups never cross the wire. |
| app | The wire and path word for a registered MCP server, and the mirror object your IGA may keep for certification. | Sentences say MCP server. The mirror is read-only, and touching it changes nothing in Straza. |
| policy | The policy set documents, and loosely the whole policy side. | Never includes access rows: access opens a tool, and policy gates it. |
| approve | `mode: approve`, the `approve` block that configures it, and the act of a person. | The block is legal only beside `mode: approve` or `mode: confirm`, and the act belongs to whoever may decide. |

## The words we do not say


The table above disambiguates words that stay. This one lists the words the product retired, so that an older page, a saved search or a support thread still leads you to the current name.

| Word | What it used to mean | Say instead |
|---|---|---|
| seat, package, access package, entitlement, team | a business role, or the people who hold one | business role, or the role by name |
| grant, bind, binding | the row that gives a role tools on an MCP server | access row, and the verb is give a role access to a server. The command names `apps bind` and `apps unbind` and the wire word `bindings` stay, and grant stays for the grant a ticket leaves, a git grant and an OAuth grant |
| implies, implication | one role carrying another | composes, composition. The wire words `implies` and `implications` stay |
| in place, standing approval, held, blocked, waits for a person | the state of a call waiting for a decision | Waiting, with the class hold or ticket under it. The postures are allowed, needs approval, denied and checked |
| pool | the people a rule routes an approval to | deciders |
| minted, of a role | the admin role a server names at registration | server admin role, or global MCP admin role. A token, a code, a key and a certificate are still minted |
| IdM, identity manager, IdP | the systems either side of Straza | IGA for people, role membership and sponsors, and identity provider for sign-in. Where any SCIM 2.0 client is meant, say so. The old forms stay only in product output a page quotes |
| NHI, non-human identity | a principal with no interactive login | AI agent, or service account for `user_type: service`. The wire keeps `kind: nhi`, `user_type` and `strazactl users create-nhi` |
| app, in a sentence | a registered MCP server | MCP server. `app` stays in paths, flags, YAML keys, command names and file names, such as `kind: App`, `apps.catalog.*`, `strazactl apps` and `app.yaml` |
| enrol, enrolment | the once-per-machine bootstrap | enroll, enrollment, the spelling the wire uses |
| control-plane role, the straza kind | the kind that carries rights inside strazad | Straza role. The control plane may explain it where a reader first meets it |
| refused, of a policy outcome | a rule turning a call down | denied. Refused stays for an admin action the server turns down, such as a validation refusal or a delete it does not perform |
| capture, as a display word | prompts and replies on the chain | recording, word for word, secrets masked. The YAML key `capture:` and wire words such as `capture.mode` stay |
| PolicySet, in a sentence | the policy document | policy set. `kind: PolicySet` and the title of its specification stay |
| eval stack | the one-command enterprise demonstration | enterprise demo stack, or the demo stack once a page has named it. File paths such as `deploy/compose/eval-stack` stay |
