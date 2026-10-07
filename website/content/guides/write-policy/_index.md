---
title: Policies
description: Write the rules that decide what an agent may do.
pagetype: index
weight: 50
draft: false
keywords: policy rules write activate
---

- [Your first deny]({{< relref "guides/write-policy/first-deny.md" >}}) writes and activates a policy that denies one dangerous command and tells the agent why.
- [Hold a call for a person]({{< relref "guides/write-policy/hold-for-a-human.md" >}}) holds one call until a named person approves it, with a bounded wait.
- [Tickets]({{< relref "guides/write-policy/tickets.md" >}}) gates a class of calls behind a ticket that an approver grants once and the agent consumes later.
- [Classify]({{< relref "guides/write-policy/classify.md" >}}) sends a call to the inline classifier when a static rule cannot tell whether it is safe.
- [Record a conversation]({{< relref "guides/write-policy/capture.md" >}}) turns recording on for one role, word for word or with secrets masked.
- [Simulate a call]({{< relref "guides/write-policy/simulate-and-coverage.md" >}}) tests a policy against a call before you activate it and shows which policies govern each role.
