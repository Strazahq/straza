---
title: Python agents
seo_title: Govern Python agents with the agentkit
description: Govern a Python agent with the Straza agent kit so every tool call passes through policy.
summary: >-
  The Straza agent kit lets a Python agent with its own tool loop ask Straza about each tool
  call before it runs. The kit holds no policy logic: every check spawns the local `straza`
  binary, so the agent gets the same signed snapshot and audit trail as a harness. A broken
  setup fails closed.
pagetype: how-to
weight: 40
draft: false
tested:
  version: v1.1.0
  platform: A Linux container against the demo stack, with the kit from the release source on PYTHONPATH and an identity that holds the seeded developer role. The standalone notes come from an earlier development build run against a standalone server. The paragraph on a fresh enterprise server was written from the code on 2026-11-01 and not run
  date: 2026-09-28
  except: the paragraph on a fresh enterprise server, written from the code and not run
applies_to: both
keywords: python agentkit sdk tool call
---


You build a Python agent with its own tool loop and no harness hooks, and want each tool call decided by your Straza policy before it runs. By the end of this page one script asks Straza about two shell commands, and you have seen the destructive one denied with a reason.


The Straza agent kit is a small standard-library package with no policy logic inside it. Every decision spawns the local `straza` binary with a `python-sdk` payload, so your agent gets the same signed snapshot, the same fail-closed behavior, and the same audit trail as the harness hooks. You enroll the machine, install the kit, and put one `check` call in front of each tool, and a deny stops the call before it runs. The kit sits in the same tier as the harness hooks with the same caveat: it governs the tool calls your framework routes through it, never computation inside the process, so it is advisory on an open machine. The lanes that hold at a boundary are the MCP gateway and the sandbox image, as [the trust model]({{< relref "evaluate/security-model.md" >}}) explains.


You need the `straza` binary on your PATH (or in `STRAZA_BIN`), with `straza service` running and the machine enrolled against your Straza server as [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) shows, and Python 3.9 or newer. When you started the service yourself with `STRAZA_HOME`, run the agent with the same value, because the `straza` the kit spawns finds that service through it. On a machine with a system service or a managed install, it ignores the variable and reaches the system service. The example ran on a machine enrolled against the [demo stack]({{< relref "evaluate/try-it-on-the-demo-stack.md" >}}) as an identity that holds the seeded `developer` role.


A fresh enterprise server holds no PolicySet and denies every local tool that no rule allows, so the kit's `shell.exec` checks come back denied there. An admin first sets up the role and the PolicySet of [Give agents their local tools]({{< relref "guides/operate/enterprise-shape.md#give-agents-their-local-tools" >}}) and gives the identity its `developer` role.


On a standalone host, you enroll as a local user, such as the `admin` account the first boot creates, and you need no role, because the seeded `standalone-starter` PolicySet applies to every identity.

## Install the kit


The kit is the `straza_agentkit` package under `kits/python` in the repository, and it is not published on a package index yet, so you point Python at the checkout with `PYTHONPATH`, as the run command below does. It pulls no dependencies, because the policy engine lives in the `straza` binary rather than in Python. The `straza` binary must be on the machine and enrolled, and `STRAZA_BIN` names it when it is not on the PATH.

## Write the smallest agent


Save this as `kit-demo.py`. It starts a governed session, then asks Straza to decide a destructive command and a safe one before either runs.

```python
import straza_agentkit as straza

straza.start_session()
for cmd in ("rm -rf /tmp/x", "git status"):
    d = straza.check("shell.exec", command=cmd)
    print(cmd, "->", "allowed" if d.allowed else "denied", d.reason)
```


The `check` call takes the canonical tool name and the fields policy matches on. For a real tool loop you wrap the function with `@straza.guard(...)`, which raises `StrazaDenied` on a deny so your framework surfaces the reason to the model.

A tool of your own that fits no canonical kind, such as `refund_order`, goes in as `other` with its name: `straza.check("other", tool_name="refund_order")` or `@straza.guard("other", name="refund_order")`. The LangChain and OpenAI Agents glue sends every tool's name this way. The enterprise profile denies such a tool until an admin allows, holds or denies it by name, as [Harness tools by name]({{< relref "reference/policyset-grammar.md#harness-tools-by-name" >}}) shows, and a new tool needs no Straza release.

## Run it against your server


Run the script from the root of your Straza checkout, where you saved `kit-demo.py`. When `straza` is not on your PATH, set `STRAZA_BIN` to its full path as well. The kit spawns `straza`, which decides each call against the machine's snapshot: the destructive command comes back denied with the policy reason, and the safe one is allowed.

```sh
PYTHONPATH=kits/python python3 kit-demo.py
```

```text
rm -rf /tmp/x -> denied Straza: destructive command denied by the agent guardrails
git status -> allowed 
```


Against a standalone server, the first line carries the reason of the seeded `standalone-starter` PolicySet, `Straza starter policy: recursive force-delete is denied. Delete files individually, or an admin can edit the standalone-starter PolicySet.` The second line is unchanged, because the standalone default allows a local tool no rule matches.

The decisions ride the same audit trail as any harness session, under the `python-sdk` harness name. To take the kit back out, remove the `straza` calls from the loop; the enrollment on the machine stays until you revoke its device or delete its identity.

## Verify


Run `straza trace show` on the machine to see the decision journal the kit produced, one content-free line per decision (tool, effect, rule, snapshot, duration). The kit keeps no logger of its own, so `straza doctor` and `straza logs` are its diagnostics too.

## Caveats


The kit denies when `straza` is missing, unenrolled, cannot reach `straza service`, times out, or errors, so a broken setup fails closed rather than running ungoverned. It cannot see code an agent runs outside its tool loop, which is why it is advisory on an open machine. A rule's `require.attestation` can demand `managed` for sensitive roles, and a `python-sdk` session carries its own harness name, so policy can give it tighter rules.


When your agent delegates, the subagent is the enforcement boundary: a bare delegation leaves the child's tools ungoverned, so use the framework glue (`govern_subagents` for LangChain, `guard_agent` for the OpenAI Agents SDK) that pushes enforcement into every child graph. For calls to an MCP server, do not map them as tools; point the framework's MCP client at the Straza gateway instead.

[MCP servers and the gateway]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}})
