---
title: Servers and the gateway
description: How an MCP server sits behind Straza's gateway, which tools a session sees, and what the gateway does with each tool call.
pagetype: explanation
weight: 80
draft: false
summary: >-
  An agent connects to Straza's gateway and reaches every MCP server behind it through that
  connection. A session sees only the tools its roles reach. The gateway checks each call against
  policy, adds the server's credential and records the call. The credential stays at the gateway,
  which makes the gateway a security boundary.
noun: [server, tool]
keywords: mcp server tool gateway catalog manifest runtime rate limit timeout views
---


An MCP server offers tools to an [agent]({{< relref "concepts/identities.md" >}}) over the Model Context Protocol. In Straza every server sits behind the gateway, and an agent reaches all of them through its connection to the gateway. A tool is an action an agent can take, such as `read_rows` on a database server.

## A server and its manifest


Each server has a manifest, a YAML document that keeps the server's entry in the MCP Registry format and adds a block for Straza. The Straza block says how to reach the server: a process Straza starts, a remote server it calls over HTTP, or a container image. It can also cap the tools the server exposes and set a call rate and a timeout. It declares the server's credential and, where the server has them, its [objects]({{< relref "concepts/objects.md" >}}). A field Straza does not know fails the install, so a typo cannot pass as a setting that has no effect. A person publishes a manifest in a [draft]({{< relref "concepts/drafts.md" >}}), and an MCP Registry entry can be turned into a manifest.


The server's name is its identity in policy and the prefix of its tools. On the demo stack the server postgres runs adaptive-pg-mcp, and the agent sees its tool as `postgres__read_rows`.


The enterprise profile refuses a server that would run as a process inside Straza, where it could reach Straza's keys and database. Run it as its own service and add it as a remote server instead, as [Standalone and enterprise compared]({{< relref "reference/standalone-and-enterprise.md#command" >}}) shows. A container starts in a sandbox by default. Its root is read-only, every capability is dropped, it gains no new privileges and it has no network.

## The tools a session sees

{{< diagram name="gateway-catalog" caption="A tool reaches the list only when the server lists it, its manifest exposes it and one of the session's roles has access to it. The credential joins the call only at the gateway." >}}


To build a session's tool list, the gateway first keeps each tool that the server lists now, that its manifest exposes, and that an access row of one of the session's [roles]({{< relref "concepts/roles-and-access.md" >}}) names. Then it runs the [policy]({{< relref "concepts/policy-model.md" >}}) for the session. By default a tool that policy denies is hidden, and a tool that needs a person's [approval]({{< relref "concepts/approvals-model.md" >}}) stays visible and asks the agent for a reason. A tool without access and a hidden tool get the same unknown-tool answer, so the answer never reveals that a hidden tool exists.


When [your IGA]({{< relref "evaluate/identity-and-your-iga.md" >}}), or an admin on standalone, changes the roles of a person or an agent, a running session takes the change within seconds through its push stream. A session without a connected push stream waits for its next check-in, at most 5 minutes. The gateway then tells the client that the tool list changed, and the client lists again without a restart.

## What happens to a tool call


For each tool call, the gateway checks these in order:

1. Whether the session may see the tool.
2. The server's rate limit for this session, and for all sessions of the same person or agent together.
3. On a server that declares objects, whether a role the session holds covers the object the call names.
4. Policy, against the policy snapshot in force.
5. A person's approval, when the rule asks for one.
6. The server's [credential]({{< relref "concepts/credentials.md" >}}).
7. An [audit record]({{< relref "concepts/evidence.md" >}}) with the arguments, which the gateway queues before the call. It does not wait for the record to be written.
8. The call itself, which times out after 30 seconds unless the manifest or the deployment sets another limit.


A manifest can set a rate limit, which caps the calls a session makes to that server each second. All sessions of one person or agent together may make five times as many, so opening more sessions gives no more calls. Both limits run before policy, so a runaway loop costs no policy work and spends no approval. Each refused call still leaves its record. Every strazad instance counts both limits on its own.


A deny comes back as a tool error whose text starts with `Straza:` and carries the reason. The model reads the reason and can tell the deny apart from a connection fault.


Some servers keep state for each MCP session, such as a working folder or the rows a query left open. On a remote server the gateway opens one session with the server for each MCP connection of a Straza session, so no caller reaches the state the server keeps for another, and two agent windows of one person do not share it. One Straza session holds at most 64 of them on one server. It closes that session after ten minutes without a call, and also when a call on it fails, and the next call opens a new one. A server that Straza starts as a process or a container is one process with one session, and every caller shares it. Run a server that keeps state for each session as a remote server.

## How an agent connects


An agent sees the gateway as a single MCP server named Straza MCP Gateway, and every request carries the session's token. A coding tool that runs Straza's hooks reaches it through a local proxy, which the Straza install registers in that tool as an MCP server named straza. The coding tool then puts its own prefix in front of the server and tool names. Any other MCP client connects to the gateway directly, as [Any MCP client]({{< relref "guides/govern-an-agent/any-mcp-client.md" >}}) shows.


The gateway also offers a built-in server named `straza`. Its tools let an agent ask for an approval and wait for the answer, and holders of the drafting role also get tools to propose a draft. Policy decides whether an agent sees these tools, as it does for any other tool. A server whose manifest allows it can show MCP Apps views, which the gateway serves on that server's own address, as [Show MCP Apps views]({{< relref "guides/serve-mcp-apps/show-views.md" >}}) explains.

## Why a gateway, and what it costs


How much a hook on the agent's machine holds depends on its install. An agent without root cannot switch off the hook of a managed install, and an agent can remove a hook the user installed. The gateway is a security boundary in both cases, because the server's credential stays there and a call that goes around the gateway has none. This is true only while agents have no other route to the server, so remove such routes when you deploy. [Deployment best practices]({{< relref "guides/operate/best-practices.md" >}}) shows how on Kubernetes. [The security model]({{< relref "evaluate/security-model.md" >}}) says which paths are a boundary.


Every governed MCP call crosses the gateway, which adds a network hop. When the gateway cannot be reached, the agent has none of the MCP tools behind it. You also run one more service, sized to your fleet. The gateway is built to keep no state of its own, so you scale it by adding instances and any instance can serve any session. The rate limits are counted inside each instance, so with several instances a session, or all sessions of one person or agent, can get the rate once for each instance.

{{< details summary="How it works inside" >}}

strazad serves the gateway at `/mcp`, and each server's own endpoint under `/mcp/` and the server's name. Before anything else, the gateway refuses a request whose `Origin` header names a web page at any address but Straza's own. It checks each request's session token against cached keys. Then it checks the revocation list and the deployment's minimum attestation. If this instance has not seen the session since its last restart, it asks the client to check in again.


A large tool list comes in pages, and each page cursor is checked against the list it was issued for. When an access row or a policy changes while a client is paging, its cursor fails and the client lists again from the start.

{{< /details >}}

{{< next "concepts/credentials.md" >}}
