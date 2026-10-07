---
title: MCP servers
description: Register MCP servers with Straza, hold their credentials and decide which role reaches which tool.
pagetype: index
weight: 40
draft: false
keywords: mcp apps gateway catalog servers
---

- [Add a server]({{< relref "guides/serve-mcp-apps/add-a-server.md" >}}) registers an MCP server, checks its connection and gives a role access to its tools.
- [Credentials]({{< relref "guides/serve-mcp-apps/credentials.md" >}}) chooses who supplies an upstream server's credential and stores one shared secret for it.
- [Each caller's own credential]({{< relref "guides/serve-mcp-apps/caller-credentials.md" >}}) lets each person reach an upstream server with their own token or sign-in.
- [Access per role]({{< relref "guides/serve-mcp-apps/catalogs-per-role.md" >}}) gives a role access to selected tools and previews what its sessions see.
- [Show MCP Apps views]({{< relref "guides/serve-mcp-apps/show-views.md" >}}) lets a chat app render a server's interactive views through the server's own endpoint, with every click decided like any other tool call.
- [The apps directory]({{< relref "guides/serve-mcp-apps/gitops-apps-directory.md" >}}) keeps server manifests in a directory under version control, where a file proposes a draft that a person publishes.

Registering a server makes none of its tools visible to a person or an agent. A session lists and calls them only after a role it holds has access.
