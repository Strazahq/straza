---
title: Reporting a vulnerability
description: Where to report a vulnerability in private, what a useful report holds, what happens after you send it, and what is in scope.
pagetype: reference
weight: 30
draft: false
keywords: vulnerability report disclosure security contact advisory
---


Report a vulnerability in Straza privately, through [GitHub private vulnerability reporting](https://github.com/strazahq/straza/security/advisories/new) or by email to security@straza.ai. A public issue is the wrong place for it, because a bug in Straza can let an agent do what its operator forbade, and reports take priority over feature work for the same reason. The repository file [SECURITY.md](https://github.com/strazahq/straza/blob/main/SECURITY.md) is the policy this page describes, and it wins if the two ever differ.

## What a good report contains


Name the component: `strazad`, `strazactl`, the `straza` client or one of the published wire-format specifications. Give the version or the commit, which `strazad version`, `strazactl version`, `straza version` and the server's `/version` route print. Describe the steps that reproduce it, and say what you think an attacker gains, in terms of the [security model]({{< relref "security/security-model.md" >}}) where you can: a decision bypassed, a credential exposed, a record forged. A proof of concept is welcome. Test only against systems you own.

## What happens after you report


Within 72 hours you get an acknowledgment, and within 7 days a triage verdict: accepted, duplicate or not a vulnerability, with the reasoning. An accepted report gets a fix or a documented mitigation with a target of 90 days, ordered by severity. The highest class is a kill-switch bypass, a policy-decision bypass, credential exposure to an agent, and snapshot or attestation forgery. Disclosure is coordinated: the maintainer agrees a publication date with you, credits you in the advisory unless you decline, and publishes a GitHub Security Advisory that names the fixed versions.

## Scope


The invariants on the security model page define the scope. Decisions fail closed, no upstream tool credential reaches the agent's machine, policy snapshots are signed, and the audit path stays off the hot path, so a violation of any of them is in scope and serious by definition. Bypassing the hook through interpreter indirection, such as a script the agent writes and then runs, is a documented limitation with layered mitigations and is not a vulnerability by itself. A user-mode hook on a machine where the agent can run anything is advisory and not a security boundary, and an MCP server that a user adds to a harness outside the gateway is that user's responsibility, so a bypass through either is not a vulnerability by itself. A bypass of the MCP gateway is in scope, and so is a bypass of a deny that the documentation claims is covered at the hook. Report at once any mismatch between the published checksums or signatures of a release and its artifacts.

## Supported versions


The latest minor release of the current major receives security fixes. Older releases get fixes only for critical severities, on a best-effort basis.

## See also


[Supply chain]({{< relref "security/supply-chain.md" >}}) explains what a release ships with and how to verify it. A bug or a feature request that is not a vulnerability goes to GitHub issues, and anything else to hello@straza.ai, as the [support]({{< relref "project/support.md" >}}) page says.
