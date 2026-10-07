---
title: Concepts
description: How Straza works and why it is built the way it is.
pagetype: index
weight: 20
draft: false
keywords: concepts architecture model explanation
---

- [How a tool call is decided]({{< relref "concepts/how-straza-works.md" >}}) follows an agent action from identity and policy to enforcement and audit.
- [Identities and roles]({{< relref "concepts/identities.md" >}}) covers users, roles, devices and sessions, and how access follows them.
- [Policies]({{< relref "concepts/policy-model.md" >}}) explains how rules match actions, combine decisions and require approval.
- [Approvals]({{< relref "concepts/approvals-model.md" >}}) says who can approve an action and what happens while it waits.
- [Evidence and audit]({{< relref "concepts/evidence.md" >}}) follows audit events from the decision to storage, verification and external delivery.
- [MCP servers and the gateway]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}) shows how the gateway controls tool access and supplies upstream credentials.
- [Trust model and limits]({{< relref "concepts/trust-model-and-non-goals.md" >}}) states what Straza defends against, what it trusts and what it does not try to do.
