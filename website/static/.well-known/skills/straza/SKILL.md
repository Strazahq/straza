---
name: straza
description: Operate Straza, open source runtime governance for AI agents. Use whenever a task mentions Straza, strazad, strazactl, a PolicySet, what an agent or a role may do, adding or onboarding an MCP server with its credential and roles, a tool catalog, an approval hold or ticket, asking a person before a command runs, approving on a phone, SCIM, Entra ID, IGA or identity-manager roles for agents, or Straza audit evidence. Covers the five-minute setup, an MCP server end to end, policy with the validator in the loop, a change checked as one draft that a person publishes, replaying recent decisions against a local policy file, explaining a deny, role mapping, and evidence from the audit chain.
license: Apache-2.0
compatibility: Straza 2.x from v2.0.0, where strazactl runs on an admin API token and `straza service` holds each enrolled user's key. The skill defers to the docs page when they disagree.
---

# Straza

Straza is open source runtime governance for AI agents. It decides each shell command, file write and MCP tool call of an agent against signed policy before the call runs, and it writes one audit record of the decision. Who holds which role comes from an IGA over SCIM 2.0, from role groups in Microsoft Entra ID over Graph, or from an admin. Decisions are made on the agent's machine and in strazad, which holds the MCP gateway, and an unknown state is a deny.

Disabling a person ends every session that runs as that person. An agent user that the person sponsors keeps its own sessions. Straza does not sit between an agent and its model. It records prompts and replies only for the sessions of a PolicySet that sets `capture.conversations: true`.

This skill is a map. Read the rules, pick the job, open the one reference file it names and follow that file. Every command comes from the public docs at https://docs.straza.ai/. Each docs page has a Markdown copy at its address plus `index.md`, and this skill links those copies because a copy costs fewer tokens to read than the page. https://docs.straza.ai/llms.txt lists every copy. When you give a person a docs link, drop the trailing `index.md` so that it opens the page in their browser. When this skill and a docs page disagree, the page wins, and say so.

## Rules that hold for every job

1. Never hand over a policy you have not validated. Run `strazactl policy validate -f <file>` on every local policy file yourself. It needs no server, checks the grammar and refuses a reserved Straza role in `match.roles`. The person then runs the checks that need the server, in their own terminal and before they publish: `strazactl drafts check -f <file>` for the server's checks against live state, `strazactl policy simulate ... -f <file>` for the calls that matter, and `scripts/replay-recent.sh` for one user's recent real decisions. `drafts check` refuses what only the live store can tell, such as a role of the wrong kind, and its answer names the fix. A published change reaches a running session within seconds when the server's push reaches it, and at the session's next renewal otherwise.
2. Identity work starts from the live inventory. Ask the person to run `strazactl roles list --json` and `strazactl users list` and to share the output before you propose any mapping. Never invent a role name. Present every mapping as a proposal that the person confirms, and the person then verifies it with `strazactl catalog preview --user <name>`.
3. Evidence starts from records, never from prose. Every claim names the record type, the sequence range, the `strazactl audit verify` result and the query that produced it.
4. You do not write governance wiring. `straza install <harness>` and `straza uninstall <harness>` are the only writers of hook settings. `sudo straza install --managed --server <server-url> <harness>` belongs to the administrator, who installs the straza package for the machine's OS first. Never edit a harness settings file by hand.
5. Secrets never enter a manifest, a chat, a `!` command or a file you write, because each of those ends up in the transcript. The person types a secret in a terminal of their own: a shared secret at the hidden prompt of `strazactl apps secret set <server>`, their own token at the hidden prompt of `straza connect <server>`, and an OAuth sign-in on the credentials page that `straza connect <server>` prints. `servers.md` section 3 says which kind a server needs.
6. If Straza governs your own session, never uninstall, edit or weaken your own hooks, and never switch a setting that lets you decide your own approvals. Tell the person what is blocked and let them act.
7. You hold no Straza credential, and the offline commands need none. Every other `strazactl` command runs on an admin API token, and a person never puts one where an agent runs as them. Never ask for one, and never use one you find. The section Reads, checks and changes says what you run and what the person runs.

## Pick the job

| The user wants | Open | Then |
|---|---|---|
| one agent under policy in five minutes, ask me before a command runs, the kill switch | `references/quickstart.md` | walk it top to bottom, standalone profile |
| a policy that denies, holds, tickets, records or classifies what a role may do | `references/policy.md`, then a file in `references/examples/` | write from the requirement, validate, check, simulate, replay, then the person creates and publishes the draft |
| to know whether a local policy file breaks anyone before it is published | `scripts/replay-recent.sh <user> <file.yaml> [limit]` | the person runs it, and you read the rows where the file decides differently |
| to know why a call or a git command was denied or held | `references/policy.md`, the section Explaining a deny | audit record to rule to fix, then simulate today's answer |
| an MCP server added, its credential set, a role that reaches it, approvals on some tools, a phone that decides | `references/servers.md` | manifest, roles, access set, approvers, check, the person publishes, credential, preview, assign |
| roles of an IGA or of Entra ID mapped to Straza roles, agents provisioned with sponsors | `references/identity.md`, then `references/identity/<manager>.md` | inventory, proposal, confirmation, write, verify |
| audit evidence, SIEM queries, a control mapping for an auditor | `references/evidence.md` | records, queries, verify, then the pack |

## Reads, checks and changes

You run the commands that work on a file offline and need no server: `strazactl policy validate -f <file>`, `strazactl spec validate <policyset|app|event> -f <file>` and `strazactl apps import <server.json>`, which turns a registry entry into a manifest and uploads nothing. Of the `straza` client, `straza status`, `straza doctor` and `straza version` only report, so run them yourself.

Every `strazactl` command that reads or changes Straza reaches the server on an admin API token in `STRAZA_API_TOKEN`, with the server in `STRAZA_SERVER` or `--server`. A person mints the token in the console under Settings, API tokens, with a scope and an expiry. Whoever holds the token acts with its scope until it expires or is revoked, so the person keeps it in a terminal of their own, never in a shell profile, a file you can read or a terminal you share.

Hand the person every read, check and change as commands for that terminal, and work from the output they share: every list and show, `apps tools`, `apps export`, `roles export`, `catalog preview`, `audit tail`, `audit verify`, `policy simulate`, `drafts check` and `drafts create`. Check whether `STRAZA_API_TOKEN` is set where you run without printing it, for example with `test -n "$STRAZA_API_TOKEN"`. If it is set, do not use it. Tell the person that an admin token sits where an agent runs as them, so that they revoke it in the console and keep its replacement out of your reach.

When your user holds the Straza role `straza-draft-config`, the gateway lists two tools of the built-in straza MCP server to you. `straza__draft_submit` stores your documents as a draft and answers its id, and `straza__draft_status` reads the server's check of it. When you have these tools, submit the draft yourself and give the person its id. Otherwise hand them `strazactl drafts create`. You cannot publish.

A person publishes every draft in the console under Drafts and confirms there each line that widens access. A token creates and checks drafts and cannot publish a stored one. The direct commands, such as `strazactl policy apply` and `strazactl apps install`, change the live state in the same request. Hand the person `drafts` commands instead, so that a change to MCP servers, roles, access rows and policy sets goes through one draft, as `servers.md` and `policy.md` walk it. A draft can install a `command` server, which runs a program on the Straza host under the standalone profile, and it can change the rules your own sessions run under. So the person reads such a manifest or set in the draft before they publish it.

These stay with the person as well: a secret at the hidden prompt of `strazactl apps secret set`, every `strazactl assign` and `strazactl unassign`, `strazactl sessions revoke` and `strazactl sinks replay`. A person decides a held request in the console under Approvals, on the self-service page, on their phone or in Slack where that is turned on, because a decision must name a person.

## The pieces on one screen

| Piece | What it is |
|---|---|
| `strazad` | The server: decisions, the MCP gateway, the console and the audit chain. |
| The standalone profile | One binary with an embedded database and its own sign-in page. It allows a local tool that no rule denies. When the server is unreachable, a session keeps deciding for 15 minutes after its token expires. |
| The enterprise profile | Postgres, an external identity provider, and identity from SCIM or Microsoft Entra ID. It denies a local tool that no rule allows, expects the managed install and has no grace after the token expires. |
| `strazactl` | The administrator's CLI for people and automation. It runs on an admin API token in `STRAZA_API_TOKEN`. |
| `straza` | The client on the agent's machine. |
| `straza service` | The background service of the client. It holds the person's key and every Straza session on a Linux, macOS or Windows machine, and the `straza` commands that need the key or a session reach it over a local socket, or a named pipe on Windows. |
| `straza doctor` | The first thing to run when something fails. It prints its own next action on every line that is not ok. |
| The console | The same work as the CLI, at the server address, with a wizard for servers and a policy builder. A person signs in there, mints admin API tokens under Settings, publishes drafts under Drafts and decides holds under Approvals. The approver phone decides holds too, and so does Slack where it is turned on. |

The objects are a user, who is a person, an agent or a service, and a role of kind business, application, approver or straza. An agent can have a sponsor. A PolicySet names application roles. An MCP server has an app manifest, and an access row gives a role that server's tools. A draft holds changes to servers, roles, access rows and policy sets until a person publishes them as one. A session is bound to a user and a harness, and to a device unless it is a headless agent's. Every decision leaves an audit record.

The commands of `strazactl` are `users`, `roles`, `assign`, `unassign`, `policy` (validate, simulate, diff, apply, activate, deactivate, delete, list, show), `apps` (import, export, install, show, tools, objects, objects refresh, objects draft, logs, recheck, enable, disable, secret set, secret remove, bind, unbind, list, remove), `bindings` (list), `drafts` (check, create, update, rebase, list, show, discard, revert), `catalog preview`, `connect` and `disconnect` for another user's connection with `--user`, `spec validate`, `spec conformance`, `approvals` (list), `approvers`, `sessions`, `devices`, `attestation`, `signing-keys`, `transcripts`, `audit` (tail, verify), `sinks`, `api-token`, `status` and `version`. `--json` on `apps list`, `apps tools`, `roles list`, `bindings list`, `policy list`, `drafts check`, `drafts list`, `drafts show`, `drafts create`, `drafts update`, `drafts rebase`, `drafts discard`, `drafts revert` and `apps objects` prints the server's JSON answer and nothing else, so ask the person for those and not for the tables.

The commands of `straza` are `enroll`, `status`, `install`, `uninstall`, `connect`, `disconnect`, `exec`, `hook`, `doctor`, `service`, `mcp`, `keygen`, `trace`, `logs` and `version`.

## Where the docs are

- Start: https://docs.straza.ai/evaluate/try-it-on-one-laptop/index.md
- Concepts: https://docs.straza.ai/evaluate/how-it-works/index.md
- Reference: https://docs.straza.ai/reference/index.md for the CLI, the configuration keys and the API
