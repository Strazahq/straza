---
title: Straza docs
description: Open source runtime governance for AI agents.
pagetype: landing
draft: false
keywords: straza docs runtime governance agents identity manager control plane
---


Straza is the control plane between your IdM/IGA and your agents. Your identity manager (IdM) or identity governance and administration system (IGA) says who people and agents are and which roles they hold. Straza turns those roles into a decision on each tool call that passes through it, on the agent's machine and at the gateway in front of your MCP servers. It records every decision.


{{< diagram name="where-straza-sits" caption="Your identity manager stays the source of people and roles. Straza decides, and every decision comes back to it as an audit record. Solid lines carry policy and calls, and dashed lines carry records." >}}

## Start from what you came to do


<ul class="jobs">
<li class="start"><a href="{{< relref "get-started/install.md" >}}"><span class="j">Try it on one machine</span><span class="first">Install the binaries, then run your first governed session. The server needs nothing else.</span><span class="go">Install <span class="arrow" aria-hidden="true">→</span></span></a></li>
<li><a href="{{< relref "guides/operate/_index.md" >}}"><span class="j">Run it for a team</span><span class="first">Pick a profile, deploy with Docker or Helm, add TLS and back it up.</span><span class="go">Run Straza <span class="arrow" aria-hidden="true">→</span></span></a></li>
<li><a href="{{< relref "guides/connect-identity/_index.md" >}}"><span class="j">Connect your identity manager</span><span class="first">midPoint, Okta or any SCIM client provisions people, agents and roles.</span><span class="go">Connect identity <span class="arrow" aria-hidden="true">→</span></span></a></li>
<li><a href="{{< relref "guides/govern-an-agent/_index.md" >}}"><span class="j">Govern an agent or a coding assistant</span><span class="first">Connect Claude Code, Codex CLI, Gemini CLI or your own Python agent.</span><span class="go">Govern agents <span class="arrow" aria-hidden="true">→</span></span></a></li>
<li><a href="{{< relref "guides/serve-mcp-apps/add-a-server.md" >}}"><span class="j">Put MCP servers behind the gateway</span><span class="first">Add a server, store its credential and give a role its tools.</span><span class="go">Add a server <span class="arrow" aria-hidden="true">→</span></span></a></li>
<li><a href="{{< relref "security/_index.md" >}}"><span class="j">Review security and audit</span><span class="first">The security model, the known limits, the audit chain and the compliance mapping.</span><span class="go">Security <span class="arrow" aria-hidden="true">→</span></span></a></li>
<li><a href="{{< relref "get-started/enterprise-demo-stack.md" >}}"><span class="j">Explore the demo stack</span><span class="first">midPoint, Keycloak and working AI agents, wired together on your machine.</span><span class="go">The demo stack <span class="arrow" aria-hidden="true">→</span></span></a></li>
</ul>

## Already running Straza

<div class="quick">
<a href="{{< relref "guides/operate/doctor-and-logs.md" >}}">Diagnose a problem</a>
<a href="{{< relref "guides/operate/kill-switch-runbook.md" >}}">Kill-switch runbook</a>
<a href="{{< relref "reference/standalone-and-enterprise.md" >}}">Standalone and enterprise compared</a>
<a href="{{< relref "reference/cli/_index.md" >}}">Find a command</a>
<a href="{{< relref "project/changelog.md" >}}">What changed in each release</a>
</div>
