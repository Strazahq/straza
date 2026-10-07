---
title: Client environment
description: Every environment variable the straza client reads, what it changes, and who sets it.
pagetype: reference
weight: 20
draft: false
keywords: environment variables client straza
---


This page lists every environment variable the straza client reads, for the person who installs, scripts or packages it. Each variable stands in for a flag, moves a file for a test or a packaging job, or turns off one background behavior, and none of them sets a hidden default. A test in the client package fails when the binary reads a name this page does not list.


| Variable | Read by | Default when unset | Who sets it |
|---|---|---|---|
| `STRAZA_HOME` | Every command that opens the client state | `.straza` under the user's home directory | An operator or a test that keeps state elsewhere |
| `STRAZA_SERVER` | `straza enroll` | None. `--server` is then required. | The operator's shell or a provisioning script |
| `STRAZA_CLIENT_ID` and `STRAZA_CLIENT_SECRET` | `straza enroll --headless` and each session start, for an agent at an external identity provider | None. The client refuses and names both variables. | The service manager or CI job that runs a headless agent |
| `STRAZA_HARNESS` | `straza hook`, `straza mcp`, `straza connect` and `straza disconnect` | `straza hook` detects the harness from its environment and the payload. The other three use `claude-code`. | The conformance runner, or a launcher for a harness that sets no marker |
| `STRAZA_GEMINI_CONFIG_DIR` | `straza install gemini`, its uninstall and `straza doctor` | `.gemini` under the user's home directory | An operator whose Gemini settings live elsewhere |
| `STRAZA_SYSTEM` | Every read of the managed layout | `/etc/straza`, or `%ProgramData%\straza` on Windows | Tests of the managed layout |
| `STRAZA_MANAGED_BIN_DIR` | `straza install --managed` | `/usr/local/bin`, or `%ProgramData%\straza\bin` on Windows | Tests, or a packaging job with its own bin directory |
| `STRAZA_MANAGED_SETTINGS_DIR` | The managed settings paths of every harness | Each harness's vendor path | Tests of the managed layout |
| `STRAZA_NO_DRAIN_SPAWN` | Every decision that spools an audit record | Unset, so a detached drain starts | CI jobs and harness test rigs |

## State and server


`STRAZA_HOME` names the directory that holds the client's user-mode state, the enrollment and session files among them. When it is unset, the client uses `.straza` under the user's home directory.

A directory the client cannot read looks like a machine that never enrolled. A session start fails with ``Straza: not enrolled (run `straza enroll`)``, followed by the file it could not open and `permission denied`. Each tool call is then denied with `Straza: no active Straza session. Restart the session so straza can check in`, and `straza doctor` reports `no straza state found` and names each file it cannot read.


`STRAZA_SERVER` is the fallback for the `--server` flag of `straza enroll`, and nowhere else. The flag wins when both are set, and there is no localhost guess.

An enrollment keeps the server it resolved, and that stored server outlives the shell that exported the variable. So an enrollment aimed by the environment announces itself once on stderr before the device flow starts, in the form `enrolling at https://straza.example.com (from $STRAZA_SERVER)`. `STRAZA_SERVER` is the only variable the client announces.

{{< fails >}}
`--server is required (or set STRAZA_SERVER)`
: Neither the flag nor the variable names a server. Pass `--server` with your server's address.
{{< /fails >}}

## Headless identity


`STRAZA_CLIENT_ID` and `STRAZA_CLIENT_SECRET` carry the OAuth 2.0 client credentials of an AI agent. The client reads them when `straza enroll --headless` runs against an external identity provider in the enterprise profile.

At enrollment and again at every session start, the client sends them to the identity provider's token endpoint with the scope `openid`. The job that runs the agent therefore keeps both in its environment.

An AI agent can enroll with a key of its own instead, which an admin registers. That needs no secret in the environment at all, and it is the only headless enrollment the standalone profile offers. [Headless agents]({{< relref "guides/govern-an-agent/headless-agents.md" >}}) shows both ways.

{{< fails >}}
``enterprise headless needs STRAZA_CLIENT_ID and STRAZA_CLIENT_SECRET in the environment (the client this AI agent has at your identity provider), or a key of its own instead: run `straza keygen`, have an admin register the public key, and re-enroll``
: At least one of the two variables is missing. Set both in the environment of the job that runs the agent, or enroll the agent with a key of its own.
{{< /fails >}}

## Which harness


`STRAZA_HARNESS` names the harness dialect when no `--harness` flag was given. `straza hook` resolves the dialect in this order:

1. The `--harness` flag.
2. `STRAZA_HARNESS`.
3. The markers the harnesses set themselves: `CLAUDE_CODE` or `CLAUDECODE` for Claude Code, `CODEX_HARNESS` or `CODEX_SANDBOX` for Codex, and `GEMINI_CLI` or `GOOGLE_ANTIGRAVITY` for Gemini.
4. The shape of the payload.

`straza mcp`, `straza connect` and `straza disconnect` read the variable as the harness of a session they start, and fall back to `claude-code`. The conformance runner sets it for each case. That is how a third-party hook under test learns the dialect of the payload it is about to read.

## Gemini's settings directory


`STRAZA_GEMINI_CONFIG_DIR` moves the user-scope directory the Gemini installer writes hooks into, `.gemini` under the home directory by default. The trust records the doctor reads beside it move with it.

Gemini itself reads no directory variable, only its own file path variables and `HOME`. A name that looked like Gemini's own could aim the installer at files Gemini never opens, so the variable carries Straza's name.

## The managed layout


Three variables move the root-owned managed layout, so that its installer and its doctor can be tested on any machine. A production install sets none of them.

- `STRAZA_SYSTEM` replaces the managed configuration root, `/etc/straza` on Linux and macOS and `%ProgramData%\straza` on Windows. While it is set, the client treats the layout as tamper-protected without checking ownership. The variable moves only what straza measures and never what the harness loads, so it cannot pass a tampered file past the server's hash check.
- `STRAZA_MANAGED_BIN_DIR` replaces the directory that holds the system copy of the binary, `/usr/local/bin` or `%ProgramData%\straza\bin`.
- `STRAZA_MANAGED_SETTINGS_DIR` moves each harness's managed settings file, its retired legacy path and its managed MCP registration under one directory, one subdirectory per harness.

## The detached drain


`STRAZA_NO_DRAIN_SPAWN`, set to any value, stops the client from starting a detached `straza drain` after it spools an audit record. The record stays in the spool and leaves with the next upload: after the next decision, at a session start or end, or on the daemon's tick. CI jobs and harness test rigs set it so that a test process never leaves a background uploader behind.
