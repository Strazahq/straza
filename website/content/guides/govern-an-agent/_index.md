---
title: Govern agents
description: Put one agent under Straza policy, harness by harness.
pagetype: index
weight: 30
draft: false
keywords: govern agent harness hooks gateway
---

- [Claude Code]({{< relref "guides/govern-an-agent/claude-code.md" >}}) puts Claude Code under Straza policy on one machine and confirms the first governed call.
- [Codex CLI]({{< relref "guides/govern-an-agent/codex-cli.md" >}}) puts Codex CLI under Straza policy on one machine.
- [Gemini CLI]({{< relref "guides/govern-an-agent/gemini-cli.md" >}}) puts Gemini CLI under Straza policy on one machine.
- [Python agents]({{< relref "guides/govern-an-agent/python-agents.md" >}}) governs a Python agent with the Straza agent kit, so every tool call passes through policy.
- [Any MCP client]({{< relref "guides/govern-an-agent/any-mcp-client.md" >}}) points an MCP client at the Straza gateway, so its tool calls are governed without a harness hook.
- [Hookless processes]({{< relref "guides/govern-an-agent/hookless-processes.md" >}}) runs a process that has no hook surface inside the Straza sandbox, so its actions still meet policy.
- [Headless agents]({{< relref "guides/govern-an-agent/headless-agents.md" >}}) enrolls an AI agent with no person at the keyboard, with a key it holds, so it checks in without a browser.

- [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) enrolls a machine once, before any harness page.
- [What the agent reads back]({{< relref "guides/govern-an-agent/what-the-agent-reads-back.md" >}}) shows the deny reasons, the pending answers and the approval tools an agent sees.
- [Knowledge packs]({{< relref "guides/govern-an-agent/knowledge-packs.md" >}}) says what a pack holds and how an agent gets it.
