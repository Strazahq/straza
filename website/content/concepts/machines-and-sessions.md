---
title: Machines and sessions
description: How a person's machine enrolls with a key that never leaves it, how each session gets a short-lived token, and how the kill switch revokes sessions and machines.
pagetype: explanation
weight: 70
draft: false
summary: >-
  A person enrolls a machine once by signing in through the browser, and the Straza service on it
  makes a key that never leaves it. Each session checks in with a proof from that key and gets a
  token that lives 300 seconds and renews while it runs. The kill switch revokes sessions, and
  their next call is denied.
noun: [agent, machine]
keywords: machine device enrollment enroll key session token check-in attestation kill switch revocation push stream lifetime
---


A session is one run of a coding tool or an [agent]({{< relref "concepts/identities.md#an-agent" >}}), and Straza decides every call in it under a short-lived token. At the tool boundary, a coding assistant on a laptop, a worker deploying from CI and a personal agent acting for a manager look the same. Straza tells them apart because each session's token names its user, its machine, its coding tool and the session itself. Decisions, [approvals]({{< relref "concepts/approvals-model.md" >}}) and [audit records]({{< relref "concepts/evidence.md" >}}) can therefore be traced to the names in that token, and you can end a single session or machine without touching the person's others.

## Enrolling a machine


A [person]({{< relref "concepts/identities.md" >}}) enrolls each machine once. The Straza service on the machine makes a new Ed25519 key and opens the person's browser at the sign-in page: your identity provider on enterprise, strazad itself on standalone. strazad registers the machine with that key only when the sign-in names the key and is at most 10 minutes old. Enrolling also pins the keys that verify each signed [policy]({{< relref "concepts/policy-model.md" >}}) snapshot, so the machine uses only policy that strazad signed.


A machine with no browser can sign in with a code instead. A code names no key and cannot show which machine it was approved for, so the enterprise profile refuses it by default.


An enrollment lasts seven days from the sign-in that made it, in both profiles. Using the machine does not extend it. After that, strazad refuses the machine's check-ins and says why, and the person enrolls again with a new sign-in. An admin can change the seven days in strazad's configuration. [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) has the steps.

## The machine key


The key never leaves the machine. The service keeps the private half in its own state directory and gives strazad only the public half and proofs signed with it. It also binds the enrollment to the machine's own identity, so it stops using a key whose files were copied to another machine.


The check-in counter catches any other use of a copied key. Each check-in carries a number above the last one, and when strazad sees one at or below the last it accepted, it revokes the machine and records the reuse on the audit chain.


Installed from a package, the service runs under its own account and keeps the key in a folder only that account can open, so a program the person runs, their agent included, cannot read it. By default enterprise refuses a key kept in the person's own account, and standalone accepts both.


The key proves which machine and which enrollment a call comes from. It is not proof that a person acted, because an agent on that machine runs as the person. A held call that needs the person's own approval goes to a phone or browser they enrolled, as [the security model]({{< relref "evaluate/security-model.md" >}}) explains.

## A session and its token


When a coding tool starts a session, the straza hook asks the service to check in. The service signs the check-in with the machine key, over a fresh nonce from strazad and the next counter, and sends hashes of its own install. strazad checks the proof against the key it registered, its in-memory denylist, the person's status and the enrollment's deadline. It then rates the install, resolves the person's [roles]({{< relref "concepts/roles-and-access.md" >}}) and mints a session token.


The rating is the attestation level, `none`, `advisory` or `managed`. Managed means the install's files match the hashes you registered for it. By default the enterprise profile starts no session below managed, and the [gateway]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}) checks that minimum again on every call. The demo stack lowers it to none for a lab. A session keeps the level it started with, so a renewal does not rate the install again.


The token names the user, the session, the machine, the coding tool, the attestation level, a hash of the roles, the policy snapshot and the thumbprint of the machine key. It is valid only with a fresh proof from that key, so a copied token alone is useless. It expires after 300 seconds. strazad checks it on every call without reading its database.


The service renews the token when a call arrives with 120 seconds or less left. An idle session's token expires. strazad closes a session after 600 seconds with no check-in or renewal, and closes every session at 12 hours, renewed or not. In both cases the next call starts a new session with the machine key and no person acting, so a stolen session token stops working by a known time.


When the token runs out and strazad cannot be reached, the enterprise profile treats that unknown state as a deny and refuses every governed call at once. A strazad outage therefore stops its agents for as long as it lasts. Standalone keeps deciding from its last signed policy for 15 more minutes.

## An agent with its own identity


An agent with its own identity enrolls no machine. A worker in CI has no laptop, and no person signs in for it. The service on the machine it runs on holds the agent's key, or a client secret in a file only the service can open. The service gets the agent an ID token, with the key at strazad or with the secret at your identity provider, and reuses it until 30 seconds before it expires. Then it checks the agent in with a fresh proof from a key it holds for the agent. Every session of the agent is bound to that key. [Headless agents]({{< relref "guides/govern-an-agent/headless-agents.md" >}}) sets one up.


Replacing or removing the agent's registered key ends every running session of the agent at once. strazad also refuses an ID token issued for the old key, so the agent's next session needs the current one.

## The kill switch

{{< diagram name="kill-switch" caption="Disabling an identity ends its sessions on every machine. A machine whose Straza service holds the session's push stream learns of it at once. Any other machine learns of it when it next renews the session's token." >}}


The kill switch is any of these actions. Each one reaches every strazad replica over the event bus.

- Revoking a session interrupts it, and the coding tool's next session start opens a new one that every check-in rule judges again.
- Disabling or locking a user ends every session of that user on every machine, and refuses new check-ins until someone lets the user back in. A deactivation from your IGA over SCIM does this too.
- Revoking a machine ends its enrollment, and the person's other machines keep theirs.


At the gateway, strazad refuses a revoked session's next call at once, from its in-memory denylist. On the machine, the service holds a push stream for each session. While that stream is open, the revocation arrives as a push, and every call in the session denies from then on. The project's budget for that path is under two seconds at the 99th percentile, measured by the load rig in `test/load/probe_kill.go` from the revoke to the event on the session's stream.


When the stream is down, the session learns of it at the first call that renews its token, once 120 of its 300 seconds or fewer remain. For a session that keeps working, that is at most about 180 seconds after its token was minted. On standalone, a machine that cannot reach strazad at all adds its 15 minutes of offline grace.

{{< details summary="What strazad does on a deactivation" >}}

strazad marks the user disabled, writes a revocation, adds the user to its in-memory denylist, then marks each of the user's sessions revoked and publishes the revocation. Every other strazad replica replays it into its own denylist. The Straza service receives it on the push stream of each session it holds. A renewal that strazad refuses for a revoked user marks the session revoked in the same way, and the service never retries it. The deny the agent reads says the session was revoked, says that a disabled user's check-in is refused until an administrator re-enables it, and tells the agent to inform the user and stop.


A reactivation from your IGA lifts only the revocations your IGA made. A lock placed in the console therefore remains, and the refused lift is recorded on the audit chain. Revoked sessions stay revoked, and the machine's next check-in opens a new session without a new enrollment. A SCIM deactivation also deletes the user's own credentials on MCP servers, their OAuth grants and pasted tokens, so a returning user connects those servers again. A disable by a Straza admin leaves those credentials in place.


The kill and the lift each leave an identity record on the audit chain that names where it came from, so an auditor can see which authority acted. [Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md#sessions-and-the-client" >}}) lists every bound on this page. The [kill-switch runbook]({{< relref "guides/operate/kill-switch-runbook.md" >}}) has the steps for each action, and [Credentials and sessions]({{< relref "security/credentials-and-sessions.md" >}}) follows a session from start to end.
{{< /details >}}

{{< next "concepts/mcp-apps-and-the-gateway.md" >}}
