---
title: Roadmap
description: Say what the latest Straza release added, what is confirmed for the next two months and what is planned after that.
pagetype: reference
weight: 10
draft: false
keywords: roadmap planned next estimate month entra agent id cursor teams passkeys mcp prompts resources linux repositories authenticode cells kubernetes operator strazalytics analytics statistics approver app readable requests f-droid android windows wsl2 openapi rest api redesign iphone
# The board's stage bar is this page's outline, so the page has no rail and
# no "On this page" fold.
outline: false
# The roadmap board above the tables. A stage names the ## heading of its
# table. Each lane lists the features of one area by their names in the
# tables. A feature no lane lists shows in a lane named Other.
board:
  stages:
    - heading: New in 2.2.0
      label: "2.2.0"
      note: this release
    - heading: December 2026
      label: December 2026
      short: December
      note: confirmed
    - heading: January 2027
      label: January 2027
      short: January
      note: confirmed
    - heading: Planned
      label: Planned
      note: no month yet
  lanes:
    - id: identity
      label: Identity
      items:
        - Microsoft Entra Agent ID
        - The agent's Entra identity on its record
        - Sign in through Microsoft Entra
        - Signing keys in the console
        - Passkeys
    - id: approvals
      label: Approvals and devices
      items:
        - Confirm a new device
        - Approver certificate rotation
        - Readable requests on the phone
        - Pinned connections
        - Microsoft Teams approvals
        - AI agents as approvers
        - Approver app on F-Droid
        - Approver app redesign
    - id: gateway
      label: Gateway and MCP
      items:
        - MCP server from an OpenAPI file
        - MCP revision 2026-07-28
        - MCP prompts and resources
        - Signed app manifests
        - Egress allowlist per MCP server
        - A2A
    - id: analytics
      label: Analytics
      items:
        - Call outcomes and token usage
        - Strazalytics
    - id: clients
      label: Clients and packages
      items:
        - Linux package repositories
        - Signed Windows installer
        - Git for Codex CLI and Gemini CLI
        - Agents inside WSL2
        - Cursor
    - id: policy
      label: Policy
      items:
        - AI classifier
    - id: fleet
      label: Fleet
      items:
        - Cells
        - Kubernetes operator
        - Full lifecycle of autonomous agents
  needs:
    - item: Strazalytics
      waits_on: [Call outcomes and token usage]
    - item: MCP prompts and resources
      waits_on: [MCP revision 2026-07-28]
  bundles:
    - label: one app release
      items:
        - Confirm a new device
        - Approver certificate rotation
        - Readable requests on the phone
---


Only confirmed work carries a month, and only for the next two months. A month is an estimate, and each release dates the next two months again. Everything else is planned and has no month yet.

{{< roadmap-board >}}

## New in 2.2.0


| Feature | What you get |
|---|---|
| Microsoft Entra Agent ID | An AI agent acts as its own Entra agent identity at an MCP server. Straza gets the agent that identity's token through its agent blueprint, so the server sees the agent and not a shared account. |
| The agent's Entra identity on its record | An agent's record names the Entra agent identity it acts as. SCIM, the admin API and `strazactl` write it, and the console shows it. |
| Sign in through Microsoft Entra | A person enrolls a machine with their Entra account. |
| Signing keys in the console | An administrator promotes the key Straza signs client assertions with, and each key carries a certificate for Entra. |

## December 2026


| Feature | What you get | Status |
|---|---|---|
| Confirm a new device | A new machine, phone or browser starts working only after you confirm it on a device you already enrolled. This closes a [known limit]({{< relref "security/security-model.md#known-limits" >}}). | <span class="chip next">designing</span> |
| Approver certificate rotation | A rotated server certificate announces its successor, so enrolled phones keep working without a new enrollment. | <span class="chip next">designed</span> |
| Readable requests on the phone | The approver app lays out what a held call does in plain fields, as the console's record sheet does, with the raw arguments one tap away. | <span class="chip idea">planned</span> |
| Call outcomes and token usage | Each MCP call's record gains its duration and outcome, and each recorded turn gains the model and its token usage, so analytics has history from the first day. | <span class="chip next">designed</span> |
| MCP server from an OpenAPI file | You import an OpenAPI file and Straza serves its operations as MCP tools, listed by role, decided by policy and recorded like any other tool. | <span class="chip idea">planned</span> |

## January 2027


| Feature | What you get | Status |
|---|---|---|
| Strazalytics | Statistics per agent, machine and MCP server: calls, denials, approval times and failures. It runs as a separate, optional service that reads Straza's event stream. | <span class="chip next">designed</span> |
| Linux package repositories | apt and dnf repositories with a signing key, so a machine updates the Straza client like any other package. | <span class="chip idea">planned</span> |
| Signed Windows installer | The MSI and the Windows binaries carry an Authenticode signature. | <span class="chip idea">planned</span> |
| Pinned connections | A client pins the server's TLS key at enrollment and refuses any other, and a device-bound certificate adds to attestation. | <span class="chip next">designed</span> |
| MCP revision 2026-07-28 | The gateway speaks the current MCP revision to clients and servers. | <span class="chip idea">planned</span> |
| MCP prompts and resources | The gateway governs prompts and data resources as it governs tools: listed by role, decided by policy and recorded. | <span class="chip next">designed</span> |

## Planned


These items are accepted and have no month yet. Each gets one when it is confirmed for the next two months.

| Feature | What you get | Status |
|---|---|---|
| Passkeys | A deployment that uses Straza as its own identity provider signs people in without passwords. | <span class="chip idea">planned</span> |
| Approver app on F-Droid | The Android approver app is also published on F-Droid. | <span class="chip idea">planned</span> |
| Approver app redesign | The approver app on iPhone and Android gets a new design, so a request is quicker to read and to decide. | <span class="chip idea">planned</span> |
| Signed app manifests | A server manifest installs only when its publisher's signature verifies. | <span class="chip idea">planned</span> |
| Egress allowlist per MCP server | Each governed MCP server reaches only the hosts you list. | <span class="chip idea">planned</span> |
| Git for Codex CLI and Gemini CLI | Git through a Straza grant works in Codex CLI and Gemini CLI as it does in Claude Code, inside their sandboxes on Windows too. | <span class="chip idea">planned</span> |
| Cells | Independent deployments, each with its own database and event spine, under one identity manager, for residency, a regulated business unit or size. | <span class="chip next">designed</span> |
| Kubernetes operator | Custom resources make a cell declarative, so access and policy come from configuration, as MCP servers already do. | <span class="chip idea">planned</span> |
| Full lifecycle of autonomous agents | An AI agent's identity is bound to its workload, with attestation of the image each session ran in. Creating and retiring an agent from the identity manager already works. | <span class="chip idea">planned</span> |
| Microsoft Teams approvals | A person decides a held call in Teams, as they can in Slack today. | <span class="chip idea">planned</span> |
| Agents inside WSL2 | An agent a person starts inside WSL2 is governed by the Straza service on Windows, as an agent on Windows itself is. | <span class="chip idea">planned</span> |
| Cursor | Cursor joins Claude Code, Codex CLI and Gemini CLI under the harness hooks. | <span class="chip idea">planned</span> |
| A2A | Straza decides and records calls between agents over the A2A protocol, as it does tool calls. | <span class="chip idea">planned</span> |
| AI agents as approvers | A rule can name an AI agent as a decider of a held call. It is off unless a policy turns it on, and the record names the agent that decided. | <span class="chip idea">planned</span> |
| AI classifier | A rule can send a call to a model you run, which answers allow or deny with a reason. It is off unless a policy turns it on, and the built-in check with no model stays the default. | <span class="chip idea">planned</span> |

## How to read this page


Designed means a written design exists, and planned means the work is accepted and waits for its design. Every item gets a written design before code, and each lands whole with its tests and the pages that describe it. When an item ships, it moves to the [changelog]({{< relref "project/changelog.md" >}}) and leaves this page.
