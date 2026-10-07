---
title: What just happened
description: Name each piece of your first session, from the person and the role to the signed policy, the decision and its record.
pagetype: explanation
weight: 40
draft: false
keywords: vocabulary story identity policy decision evidence session
---


{{< diagram name="first-session" caption="Your first session. strazad signed the starter policy, the hook on your machine checked alice's two calls against its own copy, and both decisions went to the audit chain after the calls." >}}

In [Your first governed session]({{< relref "get-started/first-governed-session.md" >}}) a policy denied one command and allowed another. Straza kept three things apart to get there. Who alice is comes from her user record. What her role means comes from the policy that names it. What happens to one call is decided when the call is made. This page follows the picture from left to right.

## The server and the people


An identity is a user record, for a person or for an agent. In the standalone profile you created `alice` by hand. The server's first start had already created `admin`, whose password it printed once, and `break-glass`, the standing emergency account.

In the enterprise profile, your identity manager provisions users over SCIM instead, and each record carries the origin `scim`. Agents arrive the same way and can name a sponsor, the person accountable for them. Straza records the origin on every user. A deactivation that came over SCIM is lifted when the identity manager sets the user active again, or when a Straza administrator runs `strazactl users enable`.


A role is a label. Other configuration gives it meaning: an access row gives it tools on one MCP server, a PolicySet decides which matching calls run or stop, and a Straza role gives rights inside Straza. Your identity manager assigns roles, and Straza applies what is attached to them. The kind of a role says how Straza uses it.

| Role kind | Purpose |
|---|---|
| Application | Carries tool access and is the kind a PolicySet matches |
| Business | Groups people and composes application roles |
| Approver | Decides approval requests and carries no tools |
| Straza | Gives rights inside Straza, such as `straza-admin` |

You created `dev` as an application role and assigned it to alice, so her sessions carry it. The role does nothing until a policy names it. You made it without an MCP server, so it serves policy rules only and never reaches a server's tools. A role that does reach one is made on its server and belongs to that server, as [Access per role]({{< relref "guides/serve-mcp-apps/catalogs-per-role.md" >}}) shows.

## The signed policy


A PolicySet is one YAML document. Its `match` block selects the sessions it governs. Each rule names the tools it covers, optional matchers such as command patterns, an effect and a reason.

The `standalone-starter` set that denied `rm -rf` has no match block, so it governs every session. Its one rule has six deny patterns and a reason written for the model to read. A fresh standalone store seeds this set once, and once you delete it Straza never creates it again. The enterprise profile seeds no starter set.

You change policy in two moves. `strazactl policy apply` stores a new set switched off, and `strazactl policy activate` makes it live. Activation compiles every live set into one signed snapshot, and each client downloads that snapshot and checks its signature before it uses it.

## The hook and the session


A session is one governed agent run, bound to an identity and, for a person, to an enrolled device. Enrolling gives the machine a long-lived device credential that can only check in. Each check-in returns a session token that lasts 300 seconds and carries the user, a hash of the roles and the id of the policy snapshot. The client renews it before it expires, and `straza status` shows the current session.

Agents without a person start headless sessions with a registered key and need no enrolled device. Every session also records an attestation level, which describes how the hooks were installed. It is `advisory` for a user-mode install, `managed` for a registered root-owned install whose hashes match, and `none` when Straza has no trusted measurement. [Credentials and sessions]({{< relref "security/credentials-and-sessions.md" >}}) explains each credential, and [Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md" >}}) lists how long each one lasts.

## The two decisions


Every governed tool call becomes an event that policy evaluates. The starter rule matched `rm -rf /tmp/x` and denied it, and an explicit deny always wins over an allow.

When no rule applies, the default depends on where the call runs. `git status` ran because local tools are allowed in the standalone profile when no rule names them. The enterprise profile denies them instead. At the gateway, a role's access row allows an MCP tool unless policy denies the call or sends it to a person. A missing or untrusted snapshot, or a required server check that cannot be reached, stops the call it affects.

## The record


Evidence is the audit record. Every decision, login and identity change becomes one record with the actor, the subject, the outcome and the reason. Each record is linked to the one before it by a hash, so `strazactl audit verify` can walk the chain and prove that nothing was removed or altered.

The client keeps its records in a local spool and uploads them after the decision, never before it. A tool call therefore never waits on the audit path, and a record you look for right after a decision may arrive a moment later. Over TLS or on localhost, the Overview card re-hashes the 25 newest records in your browser, and the Audit screen re-hashes every record it has loaded, starting with the newest 200.

## The kill switch


The kill switch is revocation. Revoking a session ends that session. The server pushes the revocation to the client daemon, which drops the session state so that the next tool call is denied. A client without a daemon learns of it at its next token refresh, within the 300-second token lifetime.

Disabling a user revokes every session and refuses new ones until an administrator enables the identity again. In the enterprise profile your identity manager holds that switch. Deactivating the person over SCIM disables the user in Straza, and with the identity provider wired into the same leaver flow it disables the login too, so one action cuts both the authorization and the authentication.

[How a tool call is decided]({{< relref "concepts/how-straza-works.md" >}}) follows one call through these pieces in more depth, and the other [Concepts]({{< relref "concepts/_index.md" >}}) pages explain each piece and why it is built that way.
