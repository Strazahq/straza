---
title: Roadmap
description: Say what Straza does today and what is planned next.
pagetype: reference
weight: 10
draft: false
keywords: roadmap status planned next scale-out
---


A reader who wants to know what Straza does today and what comes next finds both here, together with what the scale-out tier holds. Straza ships one change at a time, and a change is done when its acceptance criteria pass, its tests are green and the documentation reflects the new behavior. This page carries no dates.

## What ships today


Version 1.1.0 is the first public release. Policy is decided at three enforcement points: the harness hooks of Claude Code, Codex CLI and Gemini CLI, the MCP gateway, and the exec wrapper with its sandbox profile for processes without a hook. The [trust model]({{< relref "concepts/trust-model-and-non-goals.md" >}}) page says which of them holds at a boundary. A held call is decided in the console, in Slack or on an approver's phone, and the Straza approver app for the phone is in the [App Store](https://apps.apple.com/app/straza-approver/id6798735074) and on [Google Play](https://play.google.com/store/apps/details?id=ai.straza.approver). Identity arrives over SCIM 2.0 or the admin API, headless agents enroll with a key, and a change feed lets an IGA pull back what Straza knows. The audit chain, conversation recording, the audit sentinel, the Python kit, the Helm chart and the heuristic classify mode are all in the tree.

## What is next


Planned next are the remaining conveniences and the hardening items below. Approval will gain further channels, Microsoft Teams first, on the channel interface Slack already uses. A Cursor adapter will join the harness hooks. The standalone issuer will get passkeys, so a deployment that uses Straza as its own identity provider stops depending on passwords. MCP prompts remain planned, and so do resources other than the MCP App views the gateway already serves. On the hardening side, a confirmation on a separate device before a new approval device can decide is planned as the next change after this release, which closes the path the security model names among its [known limits]({{< relref "security/security-model.md#known-limits" >}}). A device-bound client certificate will complement attestation, app manifests will verify against a publisher signature before install, and each governed MCP server will get an egress allowlist. The phone lane will let one Straza approver app approve for several deployments, and a rotated approver certificate will announce its successor over the authenticated channel so rotation stops stranding enrolled phones.

## The scale-out tier


The tier after that is about fleets. A Kubernetes operator with custom resources will make a cell declarative, so MCP servers, access rows and policy are reproducible from configuration. The lifecycle of autonomous AI agents will be complete, from a workload's birth in the identity manager to its retirement, with attestation of the image a session ran in. Cells will be the unit of growth: independent deployments, each with its own database and event spine, under one identity manager, joined by a thin enrollment directory and an anchor chain, for residency, for a regulated business unit, or for size.

## How work lands


Work lands under three rules. Anything non-trivial enters as a written design before code, and the invariants on the [security model]({{< relref "security/security-model.md" >}}) page are checked item by item first. Each change lands whole, with its tests. When behavior changes, the pages that describe it change with it, so what you read here is what runs. The [changelog]({{< relref "project/changelog.md" >}}) page says where the release notes are and how to read one.
