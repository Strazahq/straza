---
title: Any MCP client
description: Your MCP client reaches its tools through the Straza gateway, and you have seen what the gateway answers when it allows a call and when it holds one for a person.
pagetype: how-to
weight: 50
draft: false
tested:
  version: v1.1.0
  platform: A Linux container against the demo stack, as a person who holds the seeded developer and demo-tools-readers roles, with a script that signed the approval the way an enabled browser does. The standalone notes come from an earlier run against a standalone server
  date: 2026-09-28
applies_to: both
who: You, on the machine where your MCP client runs
where: A terminal, and a browser for the approval
steps: true
keywords: mcp client gateway tier 2
---


You connect an MCP client that has no hooks to the Straza gateway, on the machine where the client runs. At the end, the client reaches its tools through the gateway, and you have seen what the gateway answers when it allows a call and when it holds one for a person.


The client talks to the gateway in place of the upstream MCP servers. The gateway checks the session, serves each role its own tool catalog and decides every `tools/call` against policy. It also resolves the upstream credential in memory and audits the result. Because the decision, the catalog and the credential all stay on the server, the gateway holds at a boundary with no code on the agent's side. It cannot see what the agent does outside MCP, so a gateway-only setup governs MCP servers and leaves local commands ungoverned.

## Before you start {.nostep}


- This machine enrolled, as [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) shows.
- An MCP client that can start a stdio MCP server.
- A role that gives you a catalog. The example runs as a person who holds the seeded `developer` and `demo-tools-readers` roles of the [demo stack]({{< relref "get-started/enterprise-demo-stack.md" >}}), against `http://127.0.0.1:8420`. Put your own server's address in its place.
- A session at or above the server's `governance.minAttestation`, because the gateway refuses one below it. The enterprise profile sets that minimum to `managed`, as [Attestation levels]({{< relref "guides/govern-an-agent/enroll-a-machine.md#attestation-levels" >}}) explains.


On a standalone server you sign in as a local user. Its catalog starts empty, because a fresh standalone server has one starter PolicySet and no MCP servers.

## Register the proxy in your client


Register `straza mcp` as a stdio MCP server in your client. The client's config then carries no credential at all. The proxy keeps the rotating session token on the wire for you and joins the same governed session as any hooks on the machine.

Its `--harness` flag names the harness the session checks in under. Without the flag it takes the `STRAZA_HARNESS` environment variable, and without that it uses `claude-code`. With a server name, such as `straza mcp views-demo`, the proxy serves that one server with its own tool names and views, as [Show MCP Apps views]({{< relref "guides/serve-mcp-apps/show-views.md" >}}) shows.

## Check the catalog over stdio


A short exchange over stdin shows the handshake and the catalog without a client. The `sleep` holds the pipe open while the replies come back, the way a real client holds it open.

{{< command terminal="Terminal" purpose="on the client's machine" >}}
```sh
{ printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"stdio","version":"1"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'; sleep 5; } | straza mcp --harness claude-code
```
{{< /command >}}

{{< see >}}An `initialize` answer from the server named `straza`, then a `tools/list` answer with the tools your roles reach.{{< /see >}}

```text
{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"logging":{},"tools":{"listChanged":true}},"protocolVersion":"2025-06-18","serverInfo":{"name":"straza","version":"v1.1.0"}}}
{"jsonrpc":"2.0","id":2,"result":{"tools":[{"description":"Echoes back the input string",...,"name":"demo-tools__echo"},...,{"description":"Returns the sum of two numbers",...,"name":"demo-tools__get-sum"},...]}}
```

The `tools/list` reply is trimmed here. In full, it lists the tools of every MCP server these roles reach, and a tool a policy denies is absent from it.


With no MCP servers installed on a standalone server, the second line is `{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`. The two calls below then need an MCP server you install and give a role access to first, because neither `demo-tools` nor its approval rule exists outside the demo stack.

## Call a tool


Behind the proxy, the gateway speaks streamable HTTP at `POST /mcp` on your server, with the session token as a bearer. The token lives 300 seconds, and `straza mcp` renews it for you. Straza has no command today that hands a session token to a client it did not start, so connect clients through the proxy. The requests below show the gateway's answers on the wire, with `$STRAZA_SESSION_TOKEN` standing for the token of a live session.


An allowed call runs upstream and returns its result.

```sh
curl -s --max-time 15 -X POST http://127.0.0.1:8420/mcp \
  -H "Authorization: Bearer $STRAZA_SESSION_TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"demo-tools__echo","arguments":{"message":"hello from any MCP client"}}}'
```

```text
{"id":3,"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"Echo: hello from any MCP client"}]}}
```


{{< details summary="The handshake and the catalog over HTTP" >}}
The same `initialize` over HTTP names the gateway itself:

```sh
curl -s --max-time 15 -X POST http://127.0.0.1:8420/mcp \
  -H "Authorization: Bearer $STRAZA_SESSION_TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}'
```

```text
{"id":1,"jsonrpc":"2.0","result":{"capabilities":{"tools":{"listChanged":true}},"protocolVersion":"2025-06-18","serverInfo":{"name":"straza-gateway","title":"Straza MCP Gateway","version":"v1.1.0"}}}
```

A `tools/list` returns the same catalog as the proxy, including `demo-tools__echo`, `demo-tools__get-sum` and the `midpoint__*` and `straza__approval_*` tools:

```sh
curl -s --max-time 15 -X POST http://127.0.0.1:8420/mcp \
  -H "Authorization: Bearer $STRAZA_SESSION_TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
```
{{< /details >}}

## Approve a held call


A call that a rule holds for approval does not answer at once. The gateway keeps the connection open until a person decides, the rule's window ends, or `approval.gatewayHoldSeconds` passes, whichever comes first. That setting is 120 seconds by default.

Here `get-sum` needs a yes from the person behind the agent. This example runs as a person with no sponsor, so the call waits for that person's own confirmation. Give the client more time than the window, which is two minutes on this rule.

```sh
curl -s --max-time 130 -X POST http://127.0.0.1:8420/mcp \
  -H "Authorization: Bearer $STRAZA_SESSION_TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"demo-tools__get-sum","arguments":{"a":2,"b":3,"_straza_justification":"checking the gateway"}}}'
```


While the command waits, open `/self-service/` on the same server in a browser you enabled under **This browser**, and approve the request.

{{< clicks "Requests" "Waiting" "Approve" "Approve request" >}}

[Approve in the browser]({{< relref "guides/approve/browser.md" >}}) shows how to enable the browser, and an enrolled phone shows the same request.

{{< see >}}The call returns the moment you decide, with the tool's result.{{< /see >}}

```text
{"id":4,"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"The sum of 2 and 3 is 5."}]}}
```

In the example, a script enrolled a key the way **Enable this browser** does and signed the approval. The request's record then named the person as the decider and `browser` as the channel, with the reason given.

The console and `strazactl` refuse a decision on your own request, because an agent on your machine could make the same call. Only a device that signs confirms it, unless the operator sets `approval.unsignedOwnDecisions`. [Approve in the console]({{< relref "guides/approve/console.md" >}}) covers each place a person decides.


When nobody answers within the window, the call comes back as a tool-level error that the model can read. The same call made again opens a new request.

```text
{"id":4,"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"Straza: approval request expired after 120s with no decision (ref 01a0e9a3-acb2-7b53-af02-710d6f946c30)"}],"isError":true}}
```

An AI agent's call waits the same way for the person behind the agent, its sponsor. A rule that names an approver role sends the request to whoever holds that role. When the hold ends before the rule's window does, the call answers that the approval is pending, and [What the agent reads back]({{< relref "guides/govern-an-agent/what-the-agent-reads-back.md" >}}) quotes that answer and the approval tools an agent uses to follow it.

## When the gateway refuses {.nostep}


The gateway fails closed. A call with no token is rejected before any tool is considered:

```sh
curl -s --max-time 15 -X POST http://127.0.0.1:8420/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":5,"method":"tools/list"}'
```

```text
{"error":"missing session token"}
```

A revoked token is refused too. When a token's attestation is below the configured minimum, the gateway refuses it with the level it needs. Because the upstream credential stays on the server, the gateway governs the call itself, so it holds even for a client you do not control.

## Next {.nostep}

- [MCP servers and the gateway]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}) explains the catalog, the credentials and the decision behind each answer.
- [What the agent reads back]({{< relref "guides/govern-an-agent/what-the-agent-reads-back.md" >}}) lists every answer the model can get from Straza.
