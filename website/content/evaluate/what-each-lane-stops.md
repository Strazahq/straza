---
title: What each lane stops
description: "Read this before you test or review Straza: which lanes are a security boundary, what counts as a finding, and how to test each one."
pagetype: explanation
weight: 35
draft: false
summary: >-
  Only some of the lanes where Straza checks an agent's actions are a security boundary.
  Getting a denied call, an unlisted tool or a server's credential through the MCP gateway is a
  finding, and so is editing a managed hook without root. Going around a user-mode hook,
  `straza exec` outside the sandbox image or a command rule is documented behavior.
keywords: lanes test review finding bypass scope security boundary advisory managed hook user-mode hook gateway sandbox straza exec command rule classifier simulate starter policy rm -rf
---


A lane is one place where Straza checks an agent's actions, and only some lanes are a security
boundary. If you report on Straza, say which lane you tested.

Getting a denied call, an unlisted tool or a server's credential through the MCP gateway is a
real finding. So is editing or removing the hook of a managed install without root.

A way around a user-mode hook, `straza exec` outside the sandbox image or a command rule is
documented behavior and not a finding. So are an MCP server a person adds to their own harness
and what an interpreter computes inside its own process. The [README quickstart](https://github.com/strazahq/straza#quickstart) runs in user mode.

| Lane | What it stops | What it does not stop | How to test it |
|---|---|---|---|
| [MCP gateway]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}) | A call that policy denies, a tool outside the session's roles, and any use of a server's credential by the agent. The decision, the tool list and the credential stay on the server. | Anything that does not go through MCP, and a server a person adds to their own harness. | Register a server, call a tool without its role and read the refusal. The credential never appears on the agent's side. |
| Managed hook | A harness tool call that policy denies, on a machine where the hook sits in root-owned files that strazad checks when a session starts. | An agent with root on that machine, and what an interpreter computes inside its own process. | Install with `sudo straza install --managed`, then try to edit the hook's files as the user. |
| User-mode hook | Mistakes. It tells a cooperating agent what policy says before it acts. | An agent that decides to skip it. This lane is advisory by design. By default the enterprise profile, the one for an organization, starts sessions only from a managed install. | Do not test it as a boundary. Use it to try policy on one laptop. |
| `straza exec` in the [sandbox image]({{< relref "guides/govern-an-agent/hookless-processes.md" >}}) | Every program the agent starts, except its own interpreter and the binaries on the image's allowlist. | Computation inside the interpreter. On an open machine it is as advisory as a user-mode hook. | Run the image and start a binary that is not on the allowlist. |
| Command rules and the [classifier]({{< relref "guides/write-policy/classify.md" >}}) | The spellings a rule names, and commands with a clear sign of hidden intent, such as a download piped into a shell. | Every other spelling of the same act. A command rule is a convenience on top of the lanes above and never the boundary. | [`strazactl policy simulate`]({{< relref "guides/write-policy/simulate-and-coverage.md" >}}) with the command you want to check. |

The starter policy holds one sample rule that denies commands that start with `rm -rf`. It
shows what a deny looks like. It is not a delete guard, and another spelling of the same act
is not a bypass.

[SECURITY.md](https://github.com/strazahq/straza/blob/main/SECURITY.md) says what is in scope and where to report a finding.

{{< next "evaluate/identity-and-your-iga.md" >}}
