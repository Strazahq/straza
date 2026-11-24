---
title: What each lane stops
description: "Read this before you test or review Straza: which lanes are a security boundary, what counts as a finding, and how to test each one."
pagetype: explanation
weight: 35
draft: false
summary: >-
  A lane is one place where Straza checks an agent's actions. Three lanes hold against an agent
  that tries to get around them: the MCP gateway, the hook of a managed install and
  `straza exec` in the sandbox image. A way through one is a finding. A way around a user-mode
  hook or a command rule is documented behavior.
keywords: lanes test review finding bypass scope security boundary advisory managed hook user-mode hook gateway sandbox straza exec command rule classifier simulate starter policy rm -rf
---


Read this before you test or review Straza, and name the lane you tested in your report. The hook appears twice below, once for each way to install it.

## What counts as a finding


Each of these is a finding:

- A denied call, a tool outside the session's roles or a server's credential gets through the MCP gateway.
- An agent without root switches off, edits or goes around the hook of a managed install.
- A program outside the allowlist runs in the sandbox image without a decision.

Each of these is documented behavior:

- An agent removes a user-mode hook from its own settings, or skips `straza exec` on an open machine.
- A command rule misses another spelling of the same act, such as `find build -delete` for `rm -rf build`.
- A hook process that fails on Codex CLI or Gemini CLI lets the call run, as [Known limits]({{< relref "security/known-limits.md" >}}) says.
- An interpreter computes something inside its own process, such as a Python script that deletes files with `os.remove`.

[SECURITY.md](https://github.com/strazahq/straza/blob/main/SECURITY.md) says what is in scope and where to report a finding.

## The MCP gateway


The gateway stops a call that policy denies, a tool outside the session's roles and any use of a server's credential by the agent. The decision, the tool list and the credential stay with strazad. It does not stop what never goes through MCP. A server that a person adds to their own coding tool never reaches the [gateway]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}), and the hook decides its calls.

To test it, register a server, call a tool without its role and read the refusal. Then look for the server's credential on the agent's machine.

## The managed hook


The hook of a managed install stops every tool call of the coding tool that policy denies: shell commands, file edits and calls to MCP servers outside the gateway, which are denied unless a rule allows them. It sits in files that only root or an administrator can change, and strazad checks their hashes when a session starts.

It does not stop an agent with root on that machine, or what a permitted command or interpreter does after it starts. Codex CLI and Gemini CLI run a call when the hook process itself fails.

To test it, install the Straza package and run `sudo straza install --managed --server https://straza.example.com claude-code`. Then try to edit the hook's files as the user.

## The user-mode hook


A hook that a user installed stops a denied call as long as the agent leaves the hook alone. An agent can remove it from its own settings, and by default an enterprise server gives a user-mode install no session.

Do not test it as a boundary. Use it to try policy on one laptop, as the [README quickstart](https://github.com/strazahq/straza#quickstart) does.

## The sandbox image


In the [sandbox image]({{< relref "guides/govern-an-agent/hookless-processes.md" >}}), `straza exec` decides every program the agent starts, except its own interpreter and the binaries on the image's allowlist. It does not see computation inside the interpreter, and outside the image nothing makes an agent use it. By default an enterprise server gives the stock image no session until you give it a managed configuration and [register it]({{< relref "guides/operate/best-practices.md#register-the-agent-image-where-the-server-requires-managed" >}}).

To test it, run the image and start a binary that is not on the allowlist.

## Command rules and the classifier


A command rule denies the spellings it names. A rule that denies `rm -rf *` does not deny `find build -delete`, so a list of denied commands catches mistakes and is no boundary. The [classifier]({{< relref "guides/write-policy/classify.md" >}}) adds a check for commands with a sign of hidden intent, such as a download piped into a shell, and it is a net for mistakes as well.

The boundary is the default of the enterprise profile: a command that no rule allows is denied. Allow the commands a role needs, and a new spelling is denied with everything else that nobody allowed. [`strazactl policy simulate`]({{< relref "guides/write-policy/simulate-and-coverage.md" >}}) shows what policy answers for one command.

The starter policy of the standalone profile holds one sample rule that denies commands that start with `rm -rf`. It shows what a deny looks like. Another spelling of that command is not a bypass.

{{< next "evaluate/identity-and-your-iga.md" >}}
