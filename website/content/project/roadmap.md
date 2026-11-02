---
title: Roadmap
description: Say what Straza does today and what is planned next.
pagetype: reference
weight: 10
draft: false
keywords: roadmap status planned next scale-out straza service background service linux windows macos windows installer intune launchd pkg jamf notarization
---


A reader who wants to know what Straza does today and what comes next finds both here, together with what the scale-out tier holds. Straza ships one change at a time, and a change is done when its acceptance criteria pass, its tests are green and the documentation reflects the new behavior. This page carries no dates.

## What ships today


Version 2.0.1 is the latest release, a patch to the major release 2.0.0, and version 1.1.0 was the first public release. Policy is decided at three enforcement points: the harness hooks of Claude Code, Codex CLI and Gemini CLI, the MCP gateway, and the exec wrapper with its sandbox profile for processes without a hook. The [trust model]({{< relref "concepts/trust-model-and-non-goals.md" >}}) page says which of them holds at a boundary. A held call is decided in the console, in Slack or on an approver's phone, and the Straza approver app for the phone is in the [App Store](https://apps.apple.com/app/straza-approver/id6798735074) and on [Google Play](https://play.google.com/store/apps/details?id=ai.straza.approver). Identity arrives over SCIM 2.0 or the admin API, headless agents enroll with a key, and a change feed lets an IGA pull back what Straza knows. The audit chain, conversation recording, the audit sentinel, the Python kit, the Helm chart and the heuristic classify mode are all in the tree.

## The background service on each system


`straza service` is the Straza background service. It holds each enrolled person's key and every Straza session on a machine under its own OS account. The hooks and `straza mcp` reach it over a local socket, or a named pipe on Windows, so an AI agent that runs as the person finds no Straza credential to copy. It runs on Linux, macOS and Windows now. On Linux the deb and rpm packages of the `straza` client install it under the straza-service OS account. On Windows it runs as the Windows service straza-service under the virtual account NT SERVICE\straza-service, which the Straza MSI for amd64 installs, by hand or as an Intune app. An Authenticode signature for the MSI and the Windows binaries is planned. So is a way for the commands that Codex CLI and Gemini CLI run in their sandboxes on Windows to reach the service. On macOS it runs as the launchd daemon ai.straza.service under the hidden account _straza-service, which the Straza package for macOS installs. Releases from 2.0.0 carry that package as a pkg signed with a Developer ID and notarized by Apple, and recipes deploy it with Jamf Pro and Intune together with a profile that keeps the service from being switched off. A signing key for the packages and apt and dnf repositories that serve them are planned, and until they exist a package is installed from its file on the release page. Git through a Straza grant runs with Claude Code on Linux, macOS and Windows now. Codex CLI and Gemini CLI get no git wiring yet.

## What is next


Planned next are the remaining conveniences and the hardening items below. The first is a confirmation on the person's phone before a newly enrolled machine starts a session. Approval will gain further channels, Microsoft Teams first, on the channel interface Slack already uses. A Cursor adapter will join the harness hooks. The standalone issuer will get passkeys, so a deployment that uses Straza as its own identity provider stops depending on passwords. MCP prompts remain planned, and so do resources other than the MCP App views the gateway already serves. On the hardening side, a confirmation on a separate device before a new approval device can decide is planned as the next change after this release, which closes the path the security model names among its [known limits]({{< relref "security/security-model.md#known-limits" >}}). A device-bound client certificate will complement attestation, app manifests will verify against a publisher signature before install, and each governed MCP server will get an egress allowlist. The phone lane will let one Straza approver app approve for several deployments, and a rotated approver certificate will announce its successor over the authenticated channel so rotation stops stranding enrolled phones.

## The scale-out tier


The tier after that is about fleets. A Kubernetes operator with custom resources will make a cell declarative, so MCP servers, access rows and policy are reproducible from configuration. The lifecycle of autonomous AI agents will be complete, from a workload's birth in the identity manager to its retirement, with attestation of the image a session ran in. Cells will be the unit of growth: independent deployments, each with its own database and event spine, under one identity manager, joined by a thin enrollment directory and an anchor chain, for residency, for a regulated business unit, or for size.

## How work lands


Work lands under three rules. Anything non-trivial enters as a written design before code, and the invariants on the [security model]({{< relref "security/security-model.md" >}}) page are checked item by item first. Each change lands whole, with its tests. When behavior changes, the pages that describe it change with it, so what you read here is what runs. The [changelog]({{< relref "project/changelog.md" >}}) page says where the release notes are and how to read one.
