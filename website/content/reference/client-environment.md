---
title: Client environment
description: Every environment variable the straza client reads, what it changes, and who sets it.
pagetype: reference
weight: 20
draft: false
keywords: environment variables client straza
---


This page lists every environment variable the straza client reads, for the person who installs, scripts or packages it. Each variable stands in for a flag, moves a file for a test or a packaging job, picks the background service a command reaches, or says whether a browser can open for a sign-in, and none of them sets a hidden default. A test in the client package fails when the binary reads a name this page does not list.


| Variable | Read by | Default when unset | Who sets it |
|---|---|---|---|
| `STRAZA_HOME` | Every command, `straza service` included | The client's files go in `.straza` under the user's home directory, and every command reaches the system service at `/run/straza-service/service.sock` on Linux and `/opt/straza/run/service.sock` on macOS, or on Windows at the named pipe `\\.\pipe\straza-service` whatever the variable says | An operator or a test that keeps the files elsewhere, or that runs a `straza service` of its own as the user on a Linux or macOS machine with no system service and no managed install |
| `STRAZA_SERVER` | `straza enroll` | None. Without `--server`, the background service enrolls at the server of a managed configuration, and refuses when there is none. | The operator's shell or a provisioning script |
| `STRAZA_HARNESS` | `straza hook`, `straza mcp`, `straza connect` and `straza disconnect` | `straza hook` detects the harness from its environment and the payload. The other three use `claude-code`. | The conformance runner, or a launcher for a harness that sets no marker |
| `STRAZA_GEMINI_CONFIG_DIR` | `straza install gemini`, its uninstall and `straza doctor` | `.gemini` under the user's home directory | An operator whose Gemini settings live elsewhere |
| `STRAZA_SYSTEM` | Every read of the managed layout | `/etc/straza`, or `C:\ProgramData\straza` on Windows | Tests of the managed layout |
| `STRAZA_MANAGED_BIN_DIR` | `straza install --managed` | `/usr/bin` on Linux, where the `straza` package installs the binary, `/opt/straza/bin` on macOS, where the Straza package for macOS installs it, and `C:\ProgramData\straza\bin` on Windows, where the Straza MSI installs it | Tests, or a packaging job with its own bin directory |
| `STRAZA_MANAGED_SETTINGS_DIR` | The managed settings paths of every harness | Each harness's vendor path | Tests of the managed layout |
| `BROWSER` | `straza enroll` | `xdg-open` on Linux, `open` on macOS and `rundll32 url.dll,FileProtocolHandler` on Windows opens the sign-in link | The person's desktop session, or a test that answers the sign-in itself |
| `DISPLAY` and `WAYLAND_DISPLAY` | `straza enroll` on Linux | Without either one and without `BROWSER`, enroll stops and names `--use-device-code` | The person's desktop session |
| `CLAUDE_CODE_SESSION_ID`, then `CODEX_THREAD_ID` | `git-remote-straza`, the git helper | `straza service` uses the caller's newest live session of the harness | Claude Code sets the first in the processes it starts |

## State and server


`STRAZA_HOME` names the directory that holds the client's own files: the trace setting, the decision journal, the error log, the trace log and the capture watermarks. None of them is a credential. When it is unset, the client uses `.straza` under the user's home directory.

The variable also picks the Straza background service a command talks to. Unset, every command reaches the system service at `/run/straza-service/service.sock`. Set, it reaches a service that runs as the user, started with `straza service` under the same value, whose socket is `$STRAZA_HOME/service.sock` and whose state, the key and the enrollment among it, is `$STRAZA_HOME/service`. One OS user can therefore run one service for each identity, as the journey tests do. That choice holds only on a machine with neither a system service nor a managed install. On a machine with either, every command ignores `STRAZA_HOME` when it picks a service and reaches the system service, because whoever starts a harness controls its environment and must not point its hooks at a socket the caller owns. `straza doctor` then says so on its service line, and the client's own files still follow the variable. On Windows the variable never picks a service, because the service there runs only as the Windows service straza-service, so every command reaches `\\.\pipe\straza-service`.

A shell, a harness or a hook that does not carry the same value reaches another service, or none. When no service answers, `straza status` and every governed call give the sentence that names the value, here for a `STRAZA_HOME` of `/home/alice/.straza`: ``Straza's background service for STRAZA_HOME /home/alice/.straza isn't running, so this command can't reach your Straza session. Start it with `straza service` in a shell that has the same STRAZA_HOME.``, and `straza doctor` gives the same advice. With the variable unset, the sentence names the system service instead and ends with ``On Linux, `systemctl status straza-service` shows why.`` On Windows it ends with ``On Windows, `Get-Service straza-service` shows whether it runs, and the Application event log under straza-service shows why it stopped.``


`STRAZA_SERVER` is the fallback for the `--server` flag of `straza enroll`, and nowhere else. The flag wins when both are set, and there is no localhost guess. On a machine with a managed configuration, the service enrolls at the server that configuration names, whatever the flag or the variable says, and enroll prints a note when they name another one. With neither and no managed configuration, the service refuses the enrollment.

The background service keeps the server an enrollment resolved, and that stored server outlives the shell that exported the variable. So an enrollment aimed by the environment announces itself once on stderr before the sign-in starts, in the form `enrolling at https://straza.example.com (from $STRAZA_SERVER)`. `STRAZA_SERVER` is the only variable the client announces.

{{< fails >}}
``Straza needs the address of your Straza server to enroll. Run `straza enroll --server <url>` with the address strazad serves on, such as https://straza.example.com.``
: Neither the flag, the variable nor a managed configuration names a server. Pass `--server` with your server's address.
{{< /fails >}}

## The browser sign-in


`straza enroll` signs a person in through a browser on the machine it runs on, and three variables decide whether it can open one. It takes the browser sign-in when `BROWSER` is set, always on macOS and Windows, and on Linux when `DISPLAY` or `WAYLAND_DISPLAY` is set. Otherwise it stops with ``This machine has no display, so Straza cannot open a browser here. Run `straza enroll --use-device-code` to sign in with a code on another device, if your Straza admin allows it.`` It never switches to the code by itself, so a script that wants the code passes `--use-device-code`.

`BROWSER` names one program, which enroll runs with the sign-in link as its only argument and does not wait for. It is not a list of programs separated by colons, and it takes no `%s`. Without it, enroll uses `xdg-open` on Linux, `open` on macOS and `rundll32 url.dll,FileProtocolHandler` on Windows. Enroll prints the link either way, after `If no browser opened, open this link on this computer:`.

## Headless identity


The client reads no secret of an AI agent from the environment, and `STRAZA_CLIENT_ID` and `STRAZA_CLIENT_SECRET` change nothing. An agent at an external identity provider gets its client id and secret from a file that only `straza service` reads, named for the agent's numeric user id, or on Windows for the SID of the agent's account. The service reads `$CREDENTIALS_DIRECTORY/agent-<uid>.json`, in the directory that systemd's `LoadCredential` names, and `<state dir>/credentials/agent-<uid>.json` when `CREDENTIALS_DIRECTORY` is unset. For the system service that state directory is `/var/lib/straza-service`, and for the Windows service it is `C:\ProgramData\straza-service`. The file holds one JSON object, `{"client_id": "...", "client_secret": "..."}`, and a missing file or an empty field refuses the enrollment with a sentence that names the path the service read. An agent with a key of its own needs no secret at all, and that is the only headless enrollment the standalone profile offers. [Headless agents]({{< relref "guides/govern-an-agent/headless-agents.md" >}}) shows both ways.

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

- `STRAZA_SYSTEM` replaces the managed configuration root, `/etc/straza` on Linux and macOS and `C:\ProgramData\straza` on Windows. While it is set, the client treats the layout as tamper-protected without checking ownership. The variable moves only what straza measures and never what the harness loads, so it cannot pass a tampered file past the server's hash check.
- `STRAZA_MANAGED_BIN_DIR` replaces the directory of the system binary that the managed hooks run. That is `/usr/bin` on Linux, where the `straza` package installs it, `/opt/straza/bin` on macOS, where the Straza package for macOS installs it, and `C:\ProgramData\straza\bin` on Windows, where the Straza MSI installs it. On all three the managed install refuses to run without it and never copies itself there.
- `STRAZA_MANAGED_SETTINGS_DIR` moves each harness's managed settings file, its retired legacy path and its managed MCP registration under one directory, one subdirectory per harness.

On Windows the client asks Windows where the ProgramData folder is, so the `ProgramData` variable of a shell moves neither the managed root nor its `bin` folder. Codex's own managed file still follows that variable, because Codex defines its path that way.

## Git


`git-remote-straza` is the git helper, the straza binary started under that name. It reads `CLAUDE_CODE_SESSION_ID`, then `CODEX_THREAD_ID`, to name the harness window a git command runs in, and sends that window to `straza service` in the same headers the hooks send. Claude Code sets `CLAUDE_CODE_SESSION_ID` in the processes it starts, to the session id its hooks report. Without either variable, the service uses the caller's newest live session of the harness, which the helper names from the markers in [Which harness](#which-harness), or `claude-code` when none is set. The value is the agent's own claim, so it can name another window of the same OS user and never another user's.

On Windows the helper is `git-remote-straza.exe`, a second copy of `straza.exe` that the Straza MSI installs in `C:\ProgramData\straza\bin`. The MSI appends that folder to the machine `PATH`, so that git finds the helper there, and the uninstall takes the entry out again. A process started before the install keeps the `PATH` it started with. People therefore sign out and in once after the install.

On macOS the helper is `/usr/local/bin/git-remote-straza`, a link to `/opt/straza/bin/straza` that the Straza package for macOS installs, and git finds it through the `PATH`.

The client also sets three of git's own variables, and reads none of them. On Linux and macOS both Claude Code installs, and on Windows the managed install, write `GIT_CONFIG_COUNT` `1`, `GIT_CONFIG_KEY_0` `include.path` and `GIT_CONFIG_VALUE_0` with the path of the host file into the `env` table of the settings they manage, so git in agent processes includes the host file that `straza service` writes. Inside those processes they replace a variable of the same name from the person's environment. The host file is `/run/straza-service/git/hosts.conf` on Linux, `/opt/straza/run/git/hosts.conf` on macOS and `C:\ProgramData\straza-service\git\hosts.conf` on Windows, where a machine whose Windows runs from another drive uses the same path on that drive. Every local user may read it, and only the service's own OS account may change it. SYSTEM and Administrators may change the Windows file too. [Git through Straza]({{< relref "guides/serve-mcp-apps/git-through-straza.md#how-git-reaches-straza" >}}) explains the file.

## Variables systemd sets for the service


`straza service` reads three variables that systemd sets for a service it starts, and no person sets them. `LISTEN_PID` and `LISTEN_FDS` hand the service a socket that systemd already listens on, and the service then serves that socket instead of opening its own. It serves exactly one and refuses to start when systemd passes more. `CREDENTIALS_DIRECTORY` names the directory of the files systemd passes to the service, where it looks for an AI agent's client secret.
