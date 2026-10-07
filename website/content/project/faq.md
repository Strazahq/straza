---
title: FAQ
description: Short answers to the questions people ask first about Straza, each with a link to the page that covers its topic.
pagetype: reference
weight: 45
draft: false
keywords: faq questions ai model offline outage identity manager prompts outputs data leave network telemetry usage relay
---


These are the questions people ask first about Straza. Each answer is short and links the page that covers the topic in full.

## AI inside Straza


There is no AI inside Straza. Every decision is deterministic policy evaluation over a signed snapshot, as [Policies]({{< relref "concepts/policy-model.md" >}}) describes. The optional [classifier]({{< relref "guides/write-policy/classify.md" >}}) is a fixed heuristic with no model, no network and no state. [The sentinel]({{< relref "guides/audit/sentinel.md" >}}) is rule-based detection. It runs after the fact and blocks nothing. [No AI in the product]({{< relref "concepts/trust-model-and-non-goals.md#no-ai-in-the-product" >}}) gives the reasons, starting with why model traffic is never proxied.

## When strazad is down


Nothing fails open. In the enterprise profile, governed actions are denied once the cached session token expires, at most five minutes after it was minted, because the grace period is zero. The standalone profile adds 15 minutes of grace. A decision that needs the server, such as an approval, is denied within 2 seconds with a reason that says the security layer is unreachable.

[Working offline]({{< relref "guides/govern-an-agent/enroll-a-machine.md#working-offline" >}}) shows the deny an agent reads when the grace runs out. [Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md#sessions-and-the-client" >}}) lists every bound in one table.

## Straza and your identity manager


Your identity manager stays the source of who exists, which roles they hold and who sponsors each AI agent. Straza receives that over SCIM 2.0 and turns it into decisions at every action. A role assignment reaches a running agent at its next check-in, and a deactivation revokes the user's sessions.

[Identities and roles]({{< relref "concepts/identities.md" >}}) explains why the identity manager stays the master. [Connect identity]({{< relref "guides/connect-identity/_index.md" >}}) connects midPoint, Okta or another SCIM client, with a guide for each.

## Prompts and model outputs


Straza does not filter prompts or model outputs. It decides what an agent does, the tool calls and commands, and it never sits on the wire between the agent and its model. When a policy set turns on recording, the conversation is recorded for audit, word for word or with secrets masked, and the model's traffic is still never filtered or rewritten.

[Record a conversation]({{< relref "guides/write-policy/capture.md" >}}) turns recording on. [What Straza does not try to do]({{< relref "concepts/trust-model-and-non-goals.md#what-straza-does-not-try-to-do" >}}) lists the other non-goals.

## Data that leaves your infrastructure


Decisions happen on the agent's machine or on your strazad. strazad and its clients send no usage data, and none of them checks for updates. Beyond that, what leaves is what your configuration turns on. A Slack approval card carries the requester, the action and the redacted call preview to Slack, as [Slack]({{< relref "guides/approve/slack.md" >}}) shows.

A phone push travels through Apple's or Google's push service, a WebPush or ntfy host you allow, or the Straza relay at `push.straza.ai`, as an envelope with no content. The relay is off for strazad on its own. The Helm chart, the compose template and the demo stack turn it on, and [The hosted relay]({{< relref "guides/approve/push-and-connectivity.md#the-hosted-relay" >}}) says what it receives. [Outbound connections]({{< relref "reference/ports-and-network.md#outbound-connections" >}}) lists every destination strazad dials and how to turn the relay off.
