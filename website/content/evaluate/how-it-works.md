---
title: How it works
description: One tool call followed from the role to the audit record, then the gateway, approvals and what happens when Straza cannot decide.
aliases:
  - /concepts/how-straza-works/
  - /get-started/how-that-worked/
pagetype: explanation
weight: 20
draft: false
summary: >-
  Roles reach Straza from your IGA, Entra ID or an admin. Each session holds a token that lives
  300 seconds, and each machine holds a signed copy of your policy. Straza decides
  each tool call before it runs, on the agent's machine or at the gateway, and it denies when it
  cannot decide.
keywords: how it works tool call decided hook gateway straza exec policy rule audit record snapshot session token approval hold fail closed kill switch revoke
---

{{< diagram name="model" >}}

## How a tool call is decided


sam, the AI agent from [What Straza is]({{< relref "evaluate/what-straza-is.md" >}}), ran `rm -rf build/` on the demo stack, and Straza refused it. These steps led to that answer.

1. midPoint, the IGA of the demo stack, assigned sam the [role]({{< relref "concepts/roles-and-access.md" >}}) `operator` and sent that to Straza over SCIM. Entra ID or an admin can assign a role as well.
2. sam's session started. The Straza service on sam's machine checked in with strazad. strazad saw that sam is active, looked up sam's roles and signed a token for the session. The token lives 300 seconds, and the service renews it.
3. The service fetched the [policy]({{< relref "concepts/policy-model.md" >}}) as a snapshot that strazad signed, and it checked the signature.
4. The policy set `agent-guardrails` applies to sam's session, because its `match.roles` lists an application role that `operator` includes. A policy set names application roles, and a business role such as `operator` is a bundle of them.
5. sam handed the command to `straza exec`, which asked the service. A coding tool such as Claude Code asks through the Straza hook before each tool runs.
6. The service decided on the machine, from its snapshot, with no call to strazad. The rule below matched, so the command did not run and sam read the rule's reason.
7. The service sent the decision to strazad as an [audit record]({{< relref "concepts/evidence.md" >}}), and strazad added it to the hash chain.


This is an excerpt of the policy set, with the roles it applies to and the rule that matched:

```yaml
kind: PolicySet
metadata:
  name: agent-guardrails
spec:
  match:
    roles: [demo-tools-sandbox, midpoint-self-service, midpoint-operations, demo-tools-readers]
  rules:
    - id: no-rm-rf
      events: [tool.pre]
      tools: [shell.exec]
      command:
        denyPatterns: ["rm -rf *", "git push --force*"]
      effect: deny
      reason: "Straza: destructive command denied by the agent guardrails"
```


This is the record of that decision from the demo stack's audit log, with its fields in reading order:

```json
{
  "specversion": "1.0",
  "type": "straza.audit.tool",
  "id": "290616b2-1792-4efb-9442-f5773b915228",
  "source": "straza",
  "time": "2026-11-23T15:14:19.744543256Z",
  "data": {
    "user": "01a1173a-7706-7617-927c-597e8e04b772",
    "session": "01a208f8-bc0d-7ad8-acee-e52906d1f01d",
    "harness": "exec-wrapper",
    "event": "tool.pre",
    "tool": "shell.exec",
    "command": "rm -rf build/",
    "workspace": "/work",
    "effect": "deny",
    "ruleId": "no-rm-rf",
    "setName": "agent-guardrails",
    "reason": "Straza: destructive command denied by the agent guardrails",
    "snapshot": "eb0eae7ecb438ffd7f93c89d7aaad55b5544ae3c3d9ced0c0f4d23494e5a704e",
    "app": ""
  }
}
```


A deny beats every allow. When no rule matches a local tool, the enterprise profile denies it and standalone allows it.

## A call to an MCP server


A tool of an [MCP server]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}) behind Straza is decided at the gateway in strazad. The session, the roles and the policy engine are the same.

1. The agent's MCP client connects to Straza's gateway instead of the server.
2. strazad lists only the tools that the session's roles grant. A call to any other tool gets the answer `unknown tool`.
3. On a server that declares [objects]({{< relref "concepts/objects.md" >}}), strazad checks the object the call names, such as one repository, against the role.
4. The policy allows the call, denies it or holds it for a person.
5. After an allow, strazad adds the server's [credential]({{< relref "concepts/credentials.md" >}}) and passes the call on, so the agent never holds it.

## When a rule asks a person


A rule with the mode `approve` holds a call until a person decides. The rule names who that is: the holders of an approver role, the [sponsor]({{< relref "concepts/identities.md" >}}) of whoever asked, or both. When the rule names nobody, the request goes to the sponsor. A person who has no sponsor decides their own request, on a phone or browser they enrolled. The approver answers in the console, on the self-service page or on a phone, and in Slack where you turn that on.

At the gateway the call stays open while the person decides. It waits 90 seconds unless the rule sets another time, and never longer than a cap of 120 seconds that an operator can change. On the agent's machine the call ends at once with a reference to the request, and the agent repeats the call after the yes. A rule can ask for a ticket instead. Then a person has 24 hours by default, and the approved call runs once. A request that nobody answers in time ends in a deny. [Approvals]({{< relref "concepts/approvals-model.md" >}}) covers who decides, and [Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md#approvals" >}}) lists every wait.

## When Straza cannot decide


Straza denies. A call is denied when the service on the machine does not answer, and when a rule needs strazad and strazad cannot be reached. The deny names the cause and the next step.

While strazad is down, a session keeps deciding from its verified snapshot until its token runs out, which takes at most 300 seconds. Standalone then adds 15 minutes of grace, and enterprise adds none. With these defaults an outage of strazad stops every enterprise agent within five minutes.

The coding tool starts the hook, so a hook process that fails to start is the coding tool's to handle. Claude Code 2.1.295 and later blocks the call. Codex CLI and Gemini CLI run it, as [Known limits]({{< relref "security/known-limits.md" >}}) says.

## When access is taken away


Disabling a user in your IGA, in Entra ID or in Straza revokes every session of that user and refuses new ones. The gateway refuses a revoked session on its next call. The service on the agent's machine learns of it at once over the session's push stream, or at the next renewal of the token while that stream is down. Taking a role away reaches running sessions the same way.

{{< details summary="Every outcome a rule can pick" >}}

The rule that wins decides the call. A winning allow can carry a mode, which adds one more step before the tool runs.

| Outcome | What happens |
|---|---|
| <span class="chip allow">allow</span> | The tool runs. |
| <span class="chip deny">deny</span> | The tool does not run, and the agent reads the rule's reason. At the gateway, a tool that a rule denies for the whole session is left out of the session's list. |
| <span class="chip hold">approve</span> | A person decides first, as described above. |
| <span class="chip hold">confirm</span> | Only the person who asked can say yes. An agent with its own identity cannot, so its call ends in a deny. |
| <span class="chip plain">serverCheck</span> | strazad decides the call again with the live policy. An unreachable strazad means a deny. |
| <span class="chip plain">classify</span> | A built-in check reads the command first, for signs such as a download piped into a shell. It uses no AI model. |

[Policies]({{< relref "concepts/policy-model.md" >}}) explains how rules match and combine.
{{< /details >}}

{{< details summary="How the policy reaches each machine" >}}

A person publishes a change to a policy set. strazad compiles every live set into one snapshot and signs it with Ed25519. The snapshot's id is the SHA-256 of the signed bytes, and the `snapshot` field of the record above is that id, so a record names the exact policy that decided.

The service on each machine checks the signature against the keys it pinned when the machine enrolled, or against the keys in the managed configuration on a managed install. It also checks the id against the bytes. It uses a snapshot only when both checks pass. Otherwise it keeps deciding with the last one that passed until the session's time runs out, and then it denies.

The service looks for a new snapshot when a session starts, when its token renews, when a policy event arrives on the session's push stream, and after a decision once its last look is 30 seconds old.
{{< /details >}}

{{< details summary="Enrollment, check-in and the session token" >}}

A person enrolls a machine with `straza enroll`. The service makes an Ed25519 key that never leaves it and opens the browser at your identity provider, or at strazad's own sign-in on standalone. strazad registers the machine only when the sign-in names that key and is at most 10 minutes old. By default the enrollment lasts 7 days from the sign-in. A machine with no display can sign in with a code instead, which the enterprise profile refuses by default. An agent with its own identity signs in with its own key and needs no enrolled device.

At each session start the service checks in with a proof from the machine's key and the hashes it measured of its own install. strazad compares them with the hashes it registered and sets the attestation level.

| Level | The install |
|---|---|
| `managed` | Root-owned, and its hashes match the registered ones. |
| `advisory` | In the user's own files, or managed for a coding tool that strazad holds no hashes for. |
| `none` | The client sent no hashes, or a managed file does not match. |

By default the enterprise profile signs no token below `managed`. The console and the self-service page are the exception, and they cannot call MCP tools.

The token names the user, the session, the device, the coding tool, the attestation level and the snapshot. A machine's token works only with a fresh proof from that machine's key, and strazad verifies it without a database read. The service holds the token, so the agent never sees it. [Machines and sessions]({{< relref "concepts/machines-and-sessions.md" >}}) and [Credentials and sessions]({{< relref "security/credentials-and-sessions.md" >}}) cover the details.
{{< /details >}}

{{< details summary="Why the decision is made on the machine" >}}

A coding agent calls tools in loops that run thousands of times an hour. A round trip to a server on each call would make the check the slowest step of the loop. So the policy travels to the machine as a signed snapshot, and a revocation is pushed to it.

The budget for the decision on the machine is under 100 microseconds at the 99th percentile on a snapshot of 10,000 rules. For the whole hook, process start included, it is under 25 milliseconds at the 95th percentile. The load rig under `test/load` reproduces both numbers on your own hardware. Rules with the mode classify or approve are outside the budget, because they are meant for a few chosen calls.

On the server, an allow or a deny at the gateway reads no database. Only an approval and the drafting tools do. Any strazad instance can serve any session once the client has checked in there, and the client checks in on its own.

Audit records are written after the decision, so a record can appear a moment after its call. [Evidence and audit]({{< relref "concepts/evidence.md" >}}) explains the delivery path, and [The security model in depth]({{< relref "security/security-model.md#audit-records-can-be-lost" >}}) lists when a record can be lost.
{{< /details >}}

{{< next "evaluate/security-model.md" >}}
