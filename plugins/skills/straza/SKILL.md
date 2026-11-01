---
name: straza
description: Operate Straza, the control plane between an identity manager and AI agents. Use whenever a task mentions Straza, strazad, strazactl, a PolicySet, what an agent or a role may do, adding or onboarding an MCP server with its credential and roles, a tool catalog, an approval hold or ticket, asking a person before a command runs, approving on a phone, SCIM or identity-manager roles for agents, or Straza audit evidence. Covers the five-minute setup, an MCP server end to end, policy with the validator in the loop, a change checked as one draft that a person publishes, replaying recent decisions against a local policy file, explaining a deny, role mapping, and evidence from the audit chain.
license: Apache-2.0
compatibility: Straza 2.x from v2.0.0, where strazactl runs on an admin API token and `straza service` holds each machine's key. The commands were checked against the source of strazad, strazactl and straza at build v1.0.0-1678-gbcd21546. The skill defers to the docs page when they disagree.
---

# Straza

Straza is the control plane between an identity manager and AI agents. Identity flows in over SCIM 2.0 or the admin API. At every tool call, a shell command, a file write or an MCP tool call becomes one event that is decided against signed policy before it runs and lands as one audit record after. Decisions are local and fail closed. Disable a person in the identity manager and the sessions of their agents die with them. Prompts and completions never pass through Straza.

This skill is a map. Read the rules, pick the job, open the one reference file it names, and follow that file. Every command comes from the public docs at https://docs.straza.ai/ and its machine index at https://docs.straza.ai/llms.txt. When this skill and a docs page disagree, the page wins, and say so.

## Rules that hold for every job

1. Policy is never handed over unvalidated. Run `strazactl policy validate -f <file>` on every local policy file yourself, offline. The person then runs the checks against the server in their own terminal: `strazactl drafts check -f <file>` for the server's checks against live state, `strazactl policy simulate ... -f <file>` for the calls that matter, and `scripts/replay-recent.sh` for the role's recent real decisions, all before the person publishes. The local validate checks the grammar and refuses a reserved Straza role in `match.roles`, and `drafts check` refuses what needs the live store, such as a role of the wrong kind, with the fix in its answer. A running session keeps the snapshot it checked in with until its next check-in, so a change shows in new sessions first.
2. Identity work starts from the live inventory. Ask the person to run `strazactl roles list` and `strazactl users list` and share the output before you propose any mapping. Never invent a role name. Present every mapping as a proposal the person confirms, and the person then verifies it with `strazactl catalog preview --user <name>`.
3. Evidence starts from records, never from prose. Every claim names the record type, the sequence range, the `strazactl audit verify` result and the query that produced it.
4. Governance wiring is not yours to write. `straza install <harness>` and `straza uninstall <harness>` are the only writers of hook settings, and `sudo straza install --managed --server <server-url> <harness>` belongs to the administrator, who installs the straza package for the machine's OS first. Never edit a harness settings file by hand.
5. Secrets never enter a manifest, a chat, a `!` command or a file you write, because each ends up in the transcript. The person types them in a terminal of their own: a shared secret at the hidden prompt of `strazactl apps secret set <server>`, their own token at the hidden prompt of `straza connect <server>`, and an OAuth sign-in on the credentials page that `straza connect <server>` prints. `servers.md` section 3 says which kind a server needs.
6. If Straza governs your own session, never uninstall, edit or weaken your own hooks, and never switch a setting that lets you decide your own approvals. Tell the person what is blocked and let them act.
7. You hold no Straza credential. `strazactl` runs on an admin API token, and a person never puts one where an agent runs as them, so never ask for one and never use one you find. You run the offline checks, and the person runs every command that reaches the server. The section Reads, checks and changes says what runs where.

## Pick the job

| The user wants | Open | Then |
|---|---|---|
| one agent under policy in five minutes, ask me before a command runs, the kill switch | `references/quickstart.md` | walk it top to bottom, standalone profile |
| a policy that denies, holds, tickets, records or classifies what a role may do | `references/policy.md`, then a file in `references/examples/` | write from the requirement, validate, check, simulate, replay, then the person creates and publishes the draft |
| to know whether a local policy file breaks anyone before it is published | `scripts/replay-recent.sh <user> <file.yaml> [limit]` | the person runs it, and you read the rows where the file decides differently |
| to know why a call or a git command was denied or held | `references/policy.md`, the section Explaining a deny | audit record to rule to fix, then simulate today's answer |
| an MCP server added, its credential set, a role that reaches it, approvals on some tools, a phone that decides | `references/servers.md` | manifest, roles, access set, approvers, check, the person publishes, credential, preview, assign |
| identity-manager roles mapped to Straza roles, agents provisioned with sponsors | `references/identity.md`, then `references/identity/<manager>.md` | inventory, proposal, confirmation, write, verify |
| audit evidence, SIEM queries, a control mapping for an auditor | `references/evidence.md` | records, queries, verify, then the pack |

## Reads, checks and changes

You run two commands yourself, because both check files offline and need no server: `strazactl policy validate -f <file>` and `strazactl spec validate <policyset|app|event> -f <file>`.

Every other `strazactl` command reaches the server on an admin API token in `STRAZA_API_TOKEN`, with the server in `STRAZA_SERVER` or `--server`. A person mints the token in the console under Settings, API tokens, with a scope and an expiry. The token is a bearer credential: whoever holds it acts with its scope until it expires or is revoked. So the person keeps it in a terminal of their own, never in a shell profile, a file you can read or a terminal you share. Hand the person every read, check and change as commands for that terminal, and work from the output they share: every list and show, `apps tools`, `apps export`, `roles export`, `catalog preview`, `audit tail`, `audit verify`, `policy simulate`, `drafts check` and `drafts create`. If `STRAZA_API_TOKEN` is set where you run, do not use it. Tell the person that an admin token sits where an agent runs as them, so they revoke it in the console and keep its replacement out of your reach.

When your user holds the Straza role `straza-draft-config`, the gateway lists two tools of the built-in straza MCP server to you. `straza__draft_submit` stores your documents as a draft and answers its id, and `straza__draft_status` reads the server's check of it. You cannot publish.

A person publishes every draft in the console under Drafts and confirms there each line that widens access. A token creates and checks drafts but never publishes one. Every change to MCP servers, roles, access rows and policy sets goes through one draft, as `servers.md` and `policy.md` walk it. A draft can install a `command` server, which runs a program on the Straza host under the standalone profile, and change the rules your own sessions run under, so the person reads such a manifest or set in the draft before publishing it. The other changes stay with the person too: a secret at the hidden prompt of `strazactl apps secret set`, every `strazactl assign` and `strazactl unassign`, `strazactl sessions revoke` and `strazactl sinks replay`. A person decides a held request in the console under Approvals, on the self-service page or on their phone, because a decision must name a person.

## The pieces on one screen

- `strazad` is the server. The standalone profile runs one binary with an embedded database and a built-in sign-in page, allows a local tool that no rule denies, and keeps a 15 minute grace when the server is unreachable. The enterprise profile runs on Postgres with SCIM in and an external identity provider, denies a local tool that no rule allows, expects the managed install, and has no grace.
- `strazactl` is the administrator's CLI for people and automation, and it runs on an admin API token in `STRAZA_API_TOKEN`: `users`, `roles`, `assign`, `unassign`, `policy` (validate, simulate, apply, activate, deactivate, list, show), `apps` (import, export, install, show, tools, logs, recheck, secret set, bind, unbind, list, remove), `bindings` (list), `drafts` (check, create, update, list, show, discard, revert), `catalog preview`, `connect` and `disconnect` for another user's connection with `--user`, `spec validate`, `approvals` (list), `approvers`, `sessions`, `audit` (tail, verify), `sinks`, `api-token`. `--json` on `apps list`, `apps tools`, `roles list`, `bindings list`, `policy list`, `drafts check`, `drafts list` and `drafts show` prints the server's JSON answer and nothing else, so ask the person for those instead of the tables.
- `straza` is the client on the agent's machine: `enroll`, `status`, `install`, `uninstall`, `connect`, `hook`, `doctor`, `service`, `mcp`, `trace`, `logs`. `straza service` is the Straza background service, which holds the person's key and every Straza session on a Linux, macOS or Windows machine, and every other `straza` command reaches it over a local socket, or a named pipe on Windows. `straza doctor` prints its own next action on every line that is not ok, so run it before guessing.
- The objects: a user, human or an agent with a sponsor; a role of kind business, application, approver or straza; a PolicySet that names application roles; an MCP server with an app manifest; an access row that gives a role that server's tools; a draft that holds changes to servers, roles, access rows and policy sets until a person publishes them as one; a session bound to a user, a device and a harness; an audit record per decision.
- The console at the server address does the same as the CLI with a wizard for servers and a policy builder. A person signs in there, mints admin API tokens under Settings, publishes drafts under Drafts and decides holds under Approvals, and the approver phone decides holds too.

## Where the docs are

- Start: https://docs.straza.ai/get-started/first-governed-session/
- Concepts: https://docs.straza.ai/concepts/how-straza-works/
- Reference: https://docs.straza.ai/reference/ for the CLI, the configuration keys and the API
