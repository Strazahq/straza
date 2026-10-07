---
title: Security
description: The security model, the credential and session lifecycle, every key and token in one place, the hardening steps, how to report a vulnerability and the compliance mapping.
pagetype: index
weight: 50
draft: false
keywords: security model credentials sessions keys certificates tokens hardening reporting compliance known limits
---

- [The security model]({{< relref "security/security-model.md" >}}) explains what Straza protects, the invariants it holds and the residual risks it leaves with you.
- [Credentials and sessions]({{< relref "security/credentials-and-sessions.md" >}}) follows an enrolled machine through token refresh, session limits and revocation.
- [Keys, certificates and tokens]({{< relref "security/keys-certificates-and-tokens.md" >}}) names every key and credential you protect, back up and rotate.
- [Hardening]({{< relref "security/hardening.md" >}}) tightens a deployment beyond the defaults, one control at a time, and pairs with [TLS and exposure]({{< relref "guides/operate/tls-and-exposure.md" >}}).
- [Reporting a vulnerability]({{< relref "security/reporting.md" >}}) says how to report a vulnerability privately and how the response runs.
- [Compliance mapping]({{< relref "security/compliance.md" >}}) maps Straza controls to ISO 27001 Annex A and NIS2 Article 21(2).
- [Supply chain]({{< relref "security/supply-chain.md" >}}) explains how a release is built, signed and verified.

To respond to a login or access incident, lock the person or device out with the [kill-switch runbook]({{< relref "guides/operate/kill-switch-runbook.md" >}}), then read [Credentials and sessions]({{< relref "security/credentials-and-sessions.md" >}}) for what each lockout does.

- [Known limits]({{< relref "security/known-limits.md" >}}) gives one table of each limit, its impact, today's mitigation and the planned fix.
