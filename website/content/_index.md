---
title: Straza docs
description: Open source runtime governance for AI agents.
seo_description: Open source runtime governance for AI agents. These docs cover deployment, identity, policy, approvals, the MCP gateway, audit, security and the CLI.
pagetype: landing
draft: false
keywords: straza docs runtime governance agents IGA control plane
paths:
  - who: IAM and security architects
    title: Deciding whether it fits
    journey: evaluate/decide-whether-straza-fits.md
    pages:
      - page: evaluate/what-straza-is.md
      - page: evaluate/how-it-works.md
      - page: evaluate/security-model.md
      - page: evaluate/identity-and-your-iga.md
      - page: security/known-limits.md
      - page: evaluate/try-it-on-the-demo-stack.md
  - who: Operators and platform engineers
    title: Setting it up for a team
    journey: guides/set-up-straza-for-a-team.md
    pages:
      - page: reference/standalone-and-enterprise.md
        label: Plan your deployment
      - page: guides/operate/enterprise-shape.md
        label: Install the server
      - page: guides/connect-identity/_index.md
        label: Sign-in and your IGA
      - page: guides/serve-mcp-apps/add-a-server.md
        label: Servers, roles and policy
      - page: guides/govern-an-agent/enroll-a-machine.md
        label: The first machine
      - page: guides/operate/doctor-and-logs.md
        label: Check it end to end
  - who: Developers new to agents and identity
    title: New to this
    journey: evaluate/first-approval-on-one-laptop.md
    pages:
      - page: evaluate/how-it-works.md
      - page: evaluate/try-it-on-one-laptop.md
      - page: guides/write-policy/hold-for-a-human.md
      - page: guides/approve/browser.md
      - page: concepts/policy-model.md
quick:
  - page: guides/operate/doctor-and-logs.md
    label: Diagnose a problem
  - page: guides/operate/kill-switch-runbook.md
    label: Kill-switch runbook
  - page: guides/govern-an-agent/what-the-agent-reads-back.md
    label: Find a refusal
  - page: reference/cli/_index.md
    label: Find a command
  - page: project/changelog.md
    label: What changed in each release
---


Straza is the control plane between your IGA and your agents. Your identity governance and administration system (IGA) says who people and agents are and which roles they hold. Straza turns those roles into a decision on each tool call that passes through it, on the agent's machine and at the gateway in front of your MCP servers, and a rule can ask a person first. Every decision becomes an audit record.
