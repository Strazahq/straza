---
name: straza
description: Operate Straza, the control plane between an identity manager and AI agents. Use whenever a task mentions Straza, strazad, strazactl, a PolicySet, what an agent or a role may do, adding or onboarding an MCP server with its credential and roles, a tool catalog, an approval hold or ticket, asking a person before a command runs, approving on a phone, SCIM or identity-manager roles for agents, or Straza audit evidence. Covers the five-minute setup, an MCP server end to end, policy with the validator in the loop, a change checked as one draft that a person publishes, replaying recent decisions against a local policy file, explaining a deny, role mapping, and evidence from the audit chain.
license: Apache-2.0
compatibility: Straza 1.x, strazad v1.1.0 and the strazactl of the same release. The skill defers to the docs page when they disagree.
---

# Straza

Straza is the control plane between an identity manager and AI agents. Identity flows in over SCIM 2.0 or the admin API. At every tool call, a shell command, a file write or an MCP tool call becomes one event that is decided against signed policy before it runs and lands as one audit record after. Decisions are local and fail closed. Disable a person in the identity manager and the sessions of their agents die with them. Prompts and completions never pass through Straza.

This skill is a map. Read the rules, pick the job, open the one reference file it names, and follow that file. Every command comes from the public docs at https://docs.straza.ai/ and its machine index at https://docs.straza.ai/llms.txt. When this skill and a docs page disagree, the page wins, and say so.

## Rules that hold for every job

1. Policy is never handed over unvalidated. Run `strazactl policy validate -f <file>` on every local policy file, `strazactl drafts check -f <file>` for the server's checks against live state, `strazactl policy simulate ... -f <file>` for the calls that matter, and `scripts/replay-recent.sh` for the role's recent real decisions, all before the person publishes. The local validate checks the grammar and refuses a reserved Straza role in `match.roles`, and `drafts check` refuses what needs the live store, such as a role of the wrong kind, with the fix in its answer. A running session keeps the snapshot it checked in with until its next check-in, so a change shows in new sessions first.
2. Identity work starts from the live inventory. Run `strazactl roles list` and `strazactl users list` before proposing any mapping. Never invent a role name. Present every mapping as a proposal the person confirms, then verify with `strazactl catalog preview --user <name>`.
3. Evidence starts from records, never from prose. Every claim names the record type, the sequence range, the `strazactl audit verify` result and the query that produced it.
4. Governance wiring is not yours to write. `straza install <harness>` and `straza uninstall <harness>` are the only writers of hook settings, and `sudo straza install --managed <harness>` belongs to the administrator. Never edit a harness settings file by hand.
5. Secrets never enter a manifest, a chat, a `!` command or a file you write, because each ends up in the transcript. The person types them in a terminal of their own: a shared secret at the hidden prompt of `strazactl apps secret set <server>`, their own token at the hidden prompt of `straza connect <server>`, and an OAuth sign-in on the credentials page that `straza connect <server>` prints. `servers.md` section 3 says which kind a server needs.
6. If Straza governs your own session, never uninstall, edit or weaken your own hooks, and never switch a setting that lets you decide your own approvals. Tell the person what is blocked and let them act.
7. Reads and checks run on the person's login, and changes do not. Inside a coding agent, `strazactl` refuses every command that changes Straza on the person's login and sends nothing. Never unset the variables that give you away to get past it. The section Reads, checks and changes says what runs where.

## Pick the job

| The user wants | Open | Then |
|---|---|---|
| one agent under policy in five minutes, ask me before a command runs, the kill switch | `references/quickstart.md` | walk it top to bottom, standalone profile |
| a policy that denies, holds, tickets, records or classifies what a role may do | `references/policy.md`, then a file in `references/examples/` | write from the requirement, validate, check, simulate, replay, then the person creates and publishes the draft |
| to know whether a local policy file breaks anyone before it is published | `scripts/replay-recent.sh <user> <file.yaml> [limit]` | read the rows where the file decides differently |
| to know why a call was denied or held | `references/policy.md`, the section Explaining a deny | audit record to rule to fix, then simulate today's answer |
| an MCP server added, its credential set, a role that reaches it, approvals on some tools, a phone that decides | `references/servers.md` | manifest, roles, access set, approvers, check, the person publishes, credential, preview, assign |
| identity-manager roles mapped to Straza roles, agents provisioned with sponsors | `references/identity.md`, then `references/identity/<manager>.md` | inventory, proposal, confirmation, write, verify |
| audit evidence, SIEM queries, a control mapping for an auditor | `references/evidence.md` | records, queries, verify, then the pack |

## Reads, checks and changes

Reads and checks run on the person's login wherever you run them: every list and show, `apps tools`, `apps export`, `roles export`, `catalog preview`, `audit tail`, `audit verify`, and the checks `strazactl policy simulate` and `strazactl drafts check`, which store nothing. A command that changes Straza does not. Inside a coding agent `strazactl` refuses it before anything is sent, because on the person's login you would act as that person. The answer names the variable your harness sets, `CLAUDECODE` under Claude Code:

```text
strazactl: CLAUDECODE is set, so strazactl runs inside a coding agent, and this command changes Straza. On the strazactl login stored on this machine the agent would act as the person who logged in, so nothing was sent. Reads and checks such as policy simulate still work. For automation, use an admin API token in STRAZA_API_TOKEN that a person mints outside the agent with strazactl api-token create. An agent proposes config changes through the built-in straza MCP server's drafting tools
```

You cannot create or publish a draft on the person's login: both change Straza, so `strazactl` refuses them inside a coding agent, where you would act as that person. You write and check the files, and the person runs `strazactl drafts create` and `strazactl drafts publish` in their own terminal.

Every change to MCP servers, roles, access rows and policy sets goes through one draft, as `servers.md` and `policy.md` walk it. A draft can install a `command` server, which runs a program on the Straza host under the standalone profile, and change the rules your own sessions run under, so the person reads such a manifest or set in the draft before publishing it. The other changes stay with the person too, in their own terminal: `strazactl login`, a secret at the hidden prompt of `strazactl apps secret set`, `strazactl approvals enroll-token`, a decision with `strazactl approvals approve` or `strazactl approvals deny`, every `strazactl assign` and `strazactl unassign`, `strazactl sessions revoke` and `strazactl sinks replay`.

## The pieces on one screen

- `strazad` is the server. The standalone profile runs one binary with an embedded database and a built-in sign-in page, allows a local tool that no rule denies, and keeps a 15 minute grace when the server is unreachable. The enterprise profile runs on Postgres with SCIM in and an external identity provider, denies a local tool that no rule allows, expects the managed install, and has no grace.
- `strazactl` is the administrator's CLI: `login`, `users`, `roles`, `assign`, `unassign`, `policy` (validate, simulate, apply, activate, deactivate, list, show), `apps` (import, export, install, show, tools, logs, recheck, secret set, bind, unbind, list, remove), `bindings` (list), `drafts` (check, create, update, list, show, publish, discard, revert), `catalog preview`, `connect`, `spec validate`, `approvals` (list, approve, deny, enroll-token), `approvers`, `sessions`, `audit` (tail, verify), `sinks`, `api-token`. `--json` on `apps list`, `apps tools`, `roles list`, `bindings list`, `policy list`, `drafts check`, `drafts list` and `drafts show` prints the server's JSON answer and nothing else, so read those instead of the tables.
- `straza` is the client on the agent's machine: `enroll`, `status`, `install`, `uninstall`, `connect`, `hook`, `doctor`, `daemon`, `mcp`, `trace`, `logs`. `straza doctor` prints its own next action on every line that is not ok, so run it before guessing.
- The objects: a user, human or an agent with a sponsor; a role of kind business, application, approver or straza; a PolicySet that names application roles; an MCP server with an app manifest; an access row that gives a role that server's tools; a draft that holds changes to servers, roles, access rows and policy sets until a person publishes them as one; a session bound to a user, a device and a harness; an audit record per decision.
- The console at the server address does the same as the CLI with a wizard for servers and a policy builder, and the approvals page and the approver phone decide holds.

## Where the docs are

- Start: https://docs.straza.ai/get-started/first-governed-session/
- Concepts: https://docs.straza.ai/concepts/how-straza-works/
- Reference: https://docs.straza.ai/reference/ for the CLI, the configuration keys and the API
