---
title: Command line
description: Every command of strazad, strazactl and straza with its flags and operands.
pagetype: index
weight: 10
draft: false
keywords: cli strazad strazactl straza commands
---


Straza ships three binaries, and each has its own command tree here. `strazad` is the server: it serves the API, the console, the MCP gateway and the SCIM endpoint, and an operator runs one per deployment. The operator's command line is `strazactl`: everything the console does, from logging in to applying a PolicySet to verifying the audit chain, in a form a script can call. On the agent's machine runs `straza`, the client: it enrolls the device, installs the hooks into a harness, answers each hook call with a decision, fronts MCP for the harness and uploads the audit spool.


The pages under each tree are generated from the binary's own command tree by `make docs-gen`, one page per command with its synopsis, flags and operands, and the commit gate refuses a tree that differs from a fresh run, so a page here is the help text of the release it documents.

- [strazad]({{< relref "reference/cli/strazad/_index.md" >}}), the server
- [strazactl]({{< relref "reference/cli/strazactl/_index.md" >}}), the operator's command line
- [straza]({{< relref "reference/cli/straza/_index.md" >}}), the agent-side client
