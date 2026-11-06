---
title: Enroll a machine
description: The Straza background service on this machine holds a key for your Straza identity, and straza status and straza doctor show it ready for an agent.
summary: >-
  You enroll a machine once, before any agent runs on it. You sign in, in a browser on that
  machine, and `straza service`, the Straza background service, keeps a key for you, so every
  agent session there checks in as you without another sign-in. `straza status` and `straza
  doctor` then show the machine ready.
pagetype: how-to
weight: 5
draft: false
tested:
  version: v1.0.0-1570-gaf326165-dirty
  platform: An Alpine 3.20 container against a standalone server, both built from a development working tree before its commit, with straza service started with STRAZA_HOME and the sign-in page answered by a form post. The person alice ran as root with the home directory /root. The attestation refusal came from a second container whose server was restarted with STRAZA_MIN_ATTESTATION=managed. The enterprise sign-in output was not run in this walk. The browser sign-in, its refusals and the --use-device-code flag were written from the code on 2026-10-22 and not walked. The Windows machine section ran on 2026-10-22 on a Windows 10 machine at v1.0.0-1585-gb30a22d7, where alice enrolled through the browser and status and doctor answered through the pipe, and a second, standard account saw itself as not enrolled. The administrator's step there now installs from the MSI. A Windows 10 machine installed, upgraded and removed that MSI on 2026-10-26 in a run that did not follow these steps. Its other Windows sentences were written from the code and not run. The macOS section was written on 2026-10-26 from the package sources and the code, and its release pkg the same day from the release workflow, before any release carried a pkg. An unsigned package built from the source tree ran in a macOS 26.6.2 virtual machine on 2026-10-26 at v1.0.0-1610-g366fac50, where a person enrolled through the browser sign-in by a test script and not by these steps, and no signed pkg has run on a Mac yet. The section on moving a machine to another server was written from the code on 2026-11-01 and not walked
  date: 2026-10-21
  except: the browser sign-in and its refusals, the macOS section and moving a machine to another server, written from the code and not walked
applies_to: both
who: You, on the machine where your agent runs
where: A terminal and a browser
steps: true
keywords: enroll straza service background service key custody sign in attestation status doctor offline grace kill switch
---


You enroll a machine once, before you connect any agent on it. Enrolling signs you in and gives `straza service`, the Straza background service, a key for you, so each agent session there checks in as you without another sign-in. The pages for [Claude Code]({{< relref "guides/govern-an-agent/claude-code.md" >}}), [Codex CLI]({{< relref "guides/govern-an-agent/codex-cli.md" >}}) and [Gemini CLI]({{< relref "guides/govern-an-agent/gemini-cli.md" >}}) start where this page ends.

## Before you start {.nostep}


- A Linux, macOS or Windows machine, where `straza service` runs.
- On Linux, the `straza` package installed or the `straza` binary on your PATH. On macOS, the Straza package for macOS that an administrator installed, or the `straza` binary on your PATH. On Windows, the Windows service that an administrator installed. [Install]({{< relref "guides/operate/install.md" >}}) shows each one.
- The address of your Straza server. The commands below use `http://127.0.0.1:8420`, so put your own address in its place.
- An account the server knows. A standalone server keeps its own users, so an admin creates yours with `strazactl users create` and gives you its password. An enterprise server signs you in at your identity provider, and your identity manager provisions your account to Straza over SCIM.
- A browser on this machine for the sign-in. A machine with no display signs in with a code instead, as the sign-in step shows.

An AI agent with no person at the keyboard enrolls with a key of its own instead, as [Headless agents]({{< relref "guides/govern-an-agent/headless-agents.md" >}}) shows.

## A machine with the straza package {.nostep}


On a Linux machine with the `straza` package from [Install]({{< relref "guides/operate/install.md#install-the-package-on-linux" >}}), the work splits between an administrator and each person:

1. An administrator installs the package, which puts `straza service` in place under its own OS account.
2. The administrator runs the managed install for each harness. It pins the Straza server for every user of the machine and wires the harness's hooks to `/usr/bin/straza`.
3. Each person runs `straza enroll` with no `--server`, because the service enrolls at the server that the managed configuration names.

The managed install for Claude Code looks like this. Use `codex` or `gemini` for those harnesses, and your own server's address in place of the example.

{{< command terminal="Terminal" purpose="as an administrator" >}}
```sh
sudo straza install --managed --server https://straza.example.com claude-code
```
{{< /command >}}

{{< see >}}A `managed config` line, a `managed binary` line that names `/usr/bin/straza`, and the `managed wiring` and `managed mcp` lines of the harness.{{< /see >}}

On Linux the command runs only where the package installed `/usr/bin/straza`, because it never copies itself there. [Claude Code]({{< relref "guides/govern-an-agent/claude-code.md" >}}) explains what a managed install writes and how it differs from your own hook settings.

{{< fails >}}
`` `straza install --managed` found no /usr/bin/straza, which the Straza package installs. Install the straza package for this distribution first, then run this command again. ``
: The package is not installed on this machine. Install it as [Install]({{< relref "guides/operate/install.md#install-the-package-on-linux" >}}) shows, then run the managed install again.

``The Straza server at https://straza.example.com renders claude-code hooks that run /usr/local/bin/straza, but this machine's Straza is /usr/bin/straza, so those hooks would never run. Upgrade the Straza server first, then run `straza install --managed` again.``
: The server is older than the client and renders the path its own release installed to. The install wrote no wiring for that harness. Upgrade the server, then run the managed install again.
{{< /fails >}}

When the machine reaches the Straza server only through a proxy, give the service that proxy in a systemd drop-in, because systemd starts the service without anyone's shell environment. Run `sudo systemctl edit straza-service`, add the lines below with your own proxy's address, and then run `sudo systemctl restart straza-service`.

```ini
[Service]
Environment=HTTPS_PROXY=http://proxy.example.com:3128
```

## A Windows machine {.nostep}


On a Windows machine the work splits the same way:

1. An administrator installs the Straza MSI, which puts `straza.exe` in `C:\ProgramData\straza\bin`, locks `C:\ProgramData\straza` to administrators and installs `straza service` as the Windows service straza-service. The administrator then runs the managed install for each harness, which pins the Straza server for every user of the machine and wires the harness's hooks to that `straza.exe`. [Install the service on Windows]({{< relref "guides/operate/install.md#install-the-service-on-windows" >}}) shows both, and [Deploy with Intune]({{< relref "guides/govern-an-agent/deploy-with-intune.md" >}}) does both on a fleet.
2. Each person runs `straza enroll` with no `--server`, in a PowerShell of their own, because the service enrolls at the server that the managed configuration names.

The MSI adds `C:\ProgramData\straza\bin` to the machine PATH, so a PowerShell window started after your next sign-in finds `straza` by name. Until then, run it by its full path, as below.

{{< command terminal="PowerShell" purpose="as yourself, not as an administrator" >}}
```powershell
C:\ProgramData\straza\bin\straza.exe enroll
```
{{< /command >}}

The service names each caller by the Windows account the pipe reports, so every person enrolls from their own signed-in session. From here on this page holds on Windows too, with the full path in place of `straza` wherever the folder is not on your Path. [Known limits]({{< relref "security/known-limits.md" >}}) lists what this version does not cover on Windows yet.

## A macOS machine {.nostep}


On a Mac the work splits the same way:

1. An administrator installs the Straza package for macOS, the signed pkg of release 2.0.0 or later or a package built from the source tree, which puts `straza` in `/opt/straza/bin`, links `/usr/local/bin/straza` to it and starts `straza service` as the launchd daemon ai.straza.service. The administrator then runs the managed install for each harness, which pins the Straza server for every user of the Mac and wires the harness's hooks to `/opt/straza/bin/straza`. [Install the service on macOS]({{< relref "guides/operate/install.md#install-the-service-on-macos" >}}) shows both, and [Deploy with Intune]({{< relref "guides/govern-an-agent/deploy-with-intune.md#deploy-to-macos" >}}) and [Deploy with Jamf Pro]({{< relref "guides/govern-an-agent/deploy-with-jamf.md" >}}) do both on a fleet.
2. Each person runs `straza enroll` with no `--server`, in a Terminal window of their own, because the service enrolls at the server that the managed configuration names.

{{< command terminal="Terminal" purpose="as yourself, not with sudo" >}}
```sh
straza enroll
```
{{< /command >}}

The service names each caller by the account that macOS reports for the socket, so every person enrolls from their own account, and two people signed in at once keep their enrollments apart. From here on this page holds on macOS too. [Known limits]({{< relref "security/known-limits.md" >}}) lists what this version does not cover on macOS yet.

## Start the background service


`straza service` holds each enrolled person's key and every Straza session on the machine. Run for the whole machine, it runs under its own OS account. The hooks, `straza mcp` and the other commands pass messages to it over a local socket, where the kernel names the calling user, or on Windows over a named pipe, where Windows names the calling account. `~/.straza` then holds no Straza credential an agent could copy. `straza enroll` asks the service to enroll you, so the service has to run first.

On a machine with the `straza` package, the service is already in place, so skip this step. The package enables `straza-service.socket`, and systemd starts the service under the straza-service OS account at the first connection. On Windows the service is the Windows service an administrator installed, so skip this step there too, and on a Mac with the Straza package for macOS, launchd already runs it. On Linux or macOS without a package, start the service yourself for your own Straza home, in a terminal of its own, and keep it running.

{{< command terminal="Terminal 1" purpose="the background service, keep it running" >}}
```sh
export STRAZA_HOME="$HOME/.straza"
straza service
```
{{< /command >}}

{{< see >}}A log line with `msg="straza service listening"` and the socket path. The service then stays in the foreground.{{< /see >}}

With `STRAZA_HOME` set, the service runs as you, with its socket at `$STRAZA_HOME/service.sock` and its state in `$STRAZA_HOME/service`. This user mode exists on Linux and macOS. It works only on a machine with no system service and no managed install, because on a machine with either one, every `straza` command ignores `STRAZA_HOME` and reaches the system service. Export the same `STRAZA_HOME` in every terminal that runs `straza` and in the shell you start your agent from, because a command with another value, or with none, looks for a different service.


A service that runs as you keeps your key in your own account, where every process of yours can read it, an agent included. It reports the custody `user` when it enrolls your key, and a standalone server accepts that by default. An enterprise server, through `governance.minCustody`, accepts by default only the custody `account`, which a service reports when it runs under its own OS account and you call it from yours. On an enterprise machine, use the service of the `straza` package on Linux, of the Straza package for macOS on a Mac, or the Windows service on Windows, which all run under their own OS account.

{{< fails >}}
`Another Straza background service already uses this state directory, so this one stopped. Stop the other one first, or set a different STRAZA_HOME.`
: A service for this `STRAZA_HOME` already runs. Use that one, or stop it before you start another.

`` `straza service` runs as the Windows service straza-service and cannot run from a console. Start it from an administrator PowerShell with `Start-Service straza-service`. ``
: You ran `straza service` in a terminal on Windows, where the service runs only when the Service Control Manager starts it. An administrator starts it as the sentence says, or installs it first as [Install]({{< relref "guides/operate/install.md#install-the-service-on-windows" >}}) shows.
{{< /fails >}}

## Sign in and enroll


In a second terminal with the same `STRAZA_HOME`, run `straza enroll` with your server's address. On a machine with a Straza package or the Windows service, and a managed install, run `straza enroll` alone, because the service enrolls at the server the managed configuration names. If you pass another address there, enroll says so in a line that starts `This machine's managed configuration names the server` and enrolls at the managed server.

{{< command terminal="Terminal 2" purpose="on the agent's machine" >}}
```sh
straza enroll --server http://127.0.0.1:8420
```
{{< /command >}}

Enroll opens your browser at the sign-in page and waits. A standalone server signs you in itself, on its page Sign in to Straza, where you enter your local username and password. An enterprise server sends you to your identity provider, and enroll names it first, in a line that starts `Signing in at your identity provider:`. The page asks for your password even when you signed in there a moment ago, because Straza asks for a fresh sign-in every time. The browser tab then says `You are signed in to Straza on this computer. You can close this tab.`

{{< see >}}A line that starts `Straza opened your browser to sign in at`, then `Enrolled as alice` with your own username and device id, then `The Straza background service holds this machine's key.`{{< /see >}}

Enroll also prints the link, after `If no browser opened, open this link on this computer:`. Open it only in a browser on this machine. The answer of the sign-in comes back to `straza service` on a port of 127.0.0.1, which a browser on another machine never reaches, and the service waits 5 minutes for it.


Enroll takes the browser sign-in when `BROWSER` names a browser program, or on Linux when `DISPLAY` or `WAYLAND_DISPLAY` says the session has a display, as [Client environment]({{< relref "reference/client-environment.md#the-browser-sign-in" >}}) lists. On a machine with no display, such as one you reach over SSH, it stops and names the other way to sign in, with a code that you confirm in a browser on another device:

{{< command terminal="Terminal 2" purpose="on a machine with no display" >}}
```sh
straza enroll --use-device-code --server http://127.0.0.1:8420
```
{{< /command >}}

It prints a link and a code, then waits. Open the link in any browser, check that the page shows the same code, and sign in. A sign-in code cannot show which machine it was approved for, so a server whose `governance.minSignIn` is `browser`, the enterprise default, refuses it, and a standalone server accepts it.


{{< details summary="Recorded output, sign-in code, standalone" >}}
```text
To authorize this device, open:
  http://127.0.0.1:8420/oidc/device?user_code=4GDL-HMXF
and confirm code 4GDL-HMXF
Enrolled as alice (device 01a15d8a-bc3b-7821-8651-c3cd0d22f431). The Straza background service holds this machine's key.
```
{{< /details >}}


A browser sign-in enrolls a key only when its nonce names that key and its auth_time is at most 10 minutes old. The service makes the key before the browser sign-in and names the key in the sign-in request, and the server checks both. A browser sign-in that another Straza program asked for, or one made for another key, enrolls nothing, and neither does an older one. A code sign-in names no key and is accepted unless `governance.minSignIn` is `browser`. Every enrollment writes a `device.enrolled` record to the audit chain, which says whether you signed in through the browser or with a code.


You can set `STRAZA_SERVER` in place of `--server`. Enroll then says so on stderr before it starts, in the form `enrolling at http://127.0.0.1:8420 (from $STRAZA_SERVER)`.


The service keeps the server's address, the keys that check every policy the server sends, and your key in its state directory. That directory belongs to the service's own OS account when it runs for the whole machine, and to you when you started it with `STRAZA_HOME`. Each enrollment makes a new key and lasts until its sign-in deadline, `governance.signInMaxAge`, 7 days by default. The deadline counts from the moment you signed in at the identity provider, or from the enrollment when the ID token carries no sign-in time, and use never renews it. Past it the server refuses the next session start and the next renewal with a sentence that says to run `straza enroll --server` with the server's address and sign in again.


A machine that an older Straza client enrolled holds a bearer device credential, which the server no longer accepts. Its next session start is refused with the sentence below. Update `straza`, start the service, and enroll the machine again.

A machine enrolled with a sign-in code, by `--use-device-code` or by a Straza client older than the browser sign-in, is refused at its next session start once the server's `governance.minSignIn` is `browser`, which is the enterprise default. Update `straza` if it does not open a browser, and run `straza enroll` again in a desktop session on that machine.


The sentences below name the example server `https://straza.example.com`, and yours name your own. On a machine whose managed configuration names the server, the sentences that `straza` writes itself leave out `--server`, because the service enrolls at that server whatever the flag says.

{{< fails >}}
``This machine has no display, so Straza cannot open a browser here. Run `straza enroll --use-device-code` to sign in with a code on another device, if your Straza admin allows it.``
: The terminal has neither `BROWSER`, `DISPLAY` nor `WAYLAND_DISPLAY`. Run enroll in a desktop session on this machine, or use the code as the step above shows.

``The browser sign-in did not finish within 5 minutes. Run `straza enroll --server https://straza.example.com` again.``
: Nobody finished the sign-in in the browser in time. Run enroll again and sign in when the browser opens.

``Straza refused this sign-in because your identity provider says you last signed in at 2026-10-22 09:14:03 UTC, more than 10 minutes ago. Run `straza enroll --server https://straza.example.com` again and sign in when the browser asks.``
: The identity provider answered with an earlier sign-in instead of asking for your password, and the sentence names the time it gave. Run enroll again. If it keeps happening, ask IT whether the provider honors a request for a fresh sign-in.

``Straza refused this sign-in because the identity provider sent no auth_time claim, so Straza cannot tell that you signed in just now. Ask IT to add the auth_time claim to the ID token of the Straza app, then run `straza enroll --server https://straza.example.com` again.``
: The identity provider does not say when you signed in. On Entra ID the admin adds the optional claim, as [Entra ID login]({{< relref "guides/connect-identity/entra-login.md" >}}) shows.

``Straza refused this sign-in because it was made for a different enrollment. Run `straza enroll --server https://straza.example.com` again and finish the sign-in in the browser it opens.``
: The ID token names another key, or a sign-in of another Straza program reached the enrollment. Run enroll again and finish the sign-in it opens.

``Straza refused this sign-in because it was made for another Straza program. Run `straza enroll --server https://straza.example.com` on this machine and finish the sign-in it starts.``
: A standalone server's sign-in that another Straza program, such as the console, asked for reached the enrollment. Run enroll and finish the sign-in it opens.

``Straza refused this sign-in because your identity provider says you last signed in at 2026-10-22 09:14:03 UTC, longer ago than your Straza admin allows. Sign out at your identity provider, then run `straza enroll --server https://straza.example.com` again and sign in.``
: A sign-in with a code reused a sign-in at the identity provider that is already past the sign-in deadline, `governance.signInMaxAge`. Sign out at the provider so it asks for your password, then enroll again.

``Straza refused this sign-in because your Straza admin requires a browser sign-in, and a sign-in code cannot show which machine it was approved for. Run `straza enroll --server https://straza.example.com` in a desktop session on this machine.``
: You ran `--use-device-code`, and the server's `governance.minSignIn` is `browser`. Run enroll without the flag in a desktop session.

``Straza refused this machine because it was enrolled with a sign-in code, and your Straza admin now requires a browser sign-in. Run `straza enroll --server https://straza.example.com` in a desktop session on this machine, and update Straza first if it does not open a browser.``
: The session start of a machine enrolled with a code, once the server requires the browser. Enroll the machine again through the browser.

``Straza could not wait for the sign-in answer on port 8765, because another program uses it. Wait a minute and run `straza enroll --server https://straza.example.com` again, or ask your Straza admin to change oidc.loopbackPort.``
: The server fixes the port of the sign-in answer with `oidc.loopbackPort`, and another program holds it, often another sign-in that is still waiting.

``The Straza server at http://127.0.0.1:8420 does not offer a browser sign-in yet. Run `straza enroll --server http://127.0.0.1:8420 --use-device-code`, or ask your Straza admin to upgrade the server.``
: The server is older than the client. Sign in with the code, or upgrade the server.

``the sign-in code expired before it was approved. Run `straza enroll --server https://straza.example.com` again and approve the new code it shows``
: Run `straza enroll --use-device-code` again and approve the code it prints this time.

``Straza's background service for STRAZA_HOME /home/alice/.straza isn't running, so this command can't reach your Straza session. Start it with `straza service` in a shell that has the same STRAZA_HOME.``
: The service is not running for this `STRAZA_HOME`, here alice's `/home/alice/.straza`, or this terminal exports another value. Start the service as the step before shows.

``Straza's background service isn't running on this machine, so this command can't reach your Straza session. Run `straza doctor` to see why, or ask IT to reinstall Straza. On Windows, `Get-Service straza-service` shows whether it runs, and the Application event log under straza-service shows why it stopped.``
: On Windows the service straza-service is stopped or not installed. An administrator checks it as [Install]({{< relref "guides/operate/install.md#install-the-service-on-windows" >}}) shows.

``Straza refused this machine because its key is kept in your own account instead of by the Straza background service, and your Straza admin requires the service. Install Straza from your software portal, then run `straza enroll`.``
: The server accepts only the custody `account`, and this service runs as you. Ask your IT team for the service they run under its own account.

``This Straza client is older than the server's sign-in rules, which now need the Straza background service on this machine. Update Straza from your software portal, then run `straza enroll`.``
: An older `straza` on this machine checked in with its bearer device credential. Update it, start the service, and run `straza enroll` again.

`That code is not valid or has expired. Check it against the console or terminal and try again.`
: The standalone sign-in page did not accept the code of `--use-device-code`. Copy it from the terminal again, or start enroll over.

`Invalid username or password.`
: The standalone sign-in page refused the password. Ask the admin who created your account for it.

``Straza needs the address of your Straza server to enroll. Run `straza enroll --server <url>` with the address strazad serves on, such as https://straza.example.com.``
: Neither `--server`, `STRAZA_SERVER` nor a managed configuration names a server. Pass `--server` with your server's address, or set `STRAZA_SERVER`.

An error that starts `oidcflow: reach` and ends `connect: connection refused`
: Nothing answers at that address. Check the address, and check that strazad runs there.
{{< /fails >}}

## Check the enrollment


Ask the service what it holds for you.

{{< command terminal="Terminal 2" purpose="on the agent's machine" >}}
```sh
straza status
```
{{< /command >}}

{{< see >}}The service with its custody, your server, your identity with its device and key, and `session    none (start a harness session)`.{{< /see >}}

```table
service    straza service v1.0.0-1570-gaf326165-dirty (custody user)
server     http://127.0.0.1:8420
identity   alice (device 01a15d8a-bc3b-7821-8651-c3cd0d22f431, key jotq8ezInO5o)
session    none (start a harness session)
audit      nothing waiting to upload
```

No session exists yet, because an agent opens one when it starts. Before you enroll, the identity line reads ``not enrolled. A person runs `straza enroll --server <strazad url>` and signs in with their work account. An AI agent runs `straza enroll --server <strazad url> --headless` with its registered key or the client secret IT installed for it.`` On a machine whose managed configuration names the server, both commands leave out `--server <strazad url>`.

## Run the doctor


`straza doctor` checks each part of the setup and prints a hint under every line that needs action. It exits 1 when a line fails, so a script can stop on it.

{{< command terminal="Terminal 2" purpose="on the agent's machine" >}}
```sh
straza doctor
```
{{< /command >}}

{{< see >}}`[ ok ]` on service, enrollment and server, and `[WARN]` on session and wiring.{{< /see >}}

```table
[ ok ] service     straza service v1.0.0-1570-gaf326165-dirty answers at /root/.straza/service.sock and keeps keys in user custody
[ ok ] enrollment  alice (device 01a15d8a-bc3b-7821-8651-c3cd0d22f431) at http://127.0.0.1:8420, key jotq8ezInO5o, straza v1.0.0-1570-gaf326165-dirty
[ ok ] server      http://127.0.0.1:8420 healthy (strazad v1.0.0-1570-gaf326165-dirty, standalone profile)
[ ok ] approver    https://172.17.0.3:8443 (pin sha256/8iZoXmgcDdEcpQHrji7Qozi8RmtOaUZuV0YaWVL20g8=, auto-minted cert data/approver-tls/cert.pem, expires 2029-01-18, verified from here: the surface presents the pinned key)
[WARN] session     no live session
                   → start a harness session (its SessionStart hook starts one through the Straza background service), or pipe a SessionStart payload through `straza hook`
[WARN] wiring      no harness has Straza hooks wired
                   → run `straza install claude-code` (or codex/gemini). Enterprise layouts use `straza install --managed --server <server-url> claude-code` (or codex/gemini)
[ ok ] trace       journal on, 0 records; debug off
[ ok ] audit-spool no pending audit events (no successful upload recorded yet)
```


Expect the two warnings on a fresh enrollment. The session line turns green when your agent's first session checks in, and the wiring line turns green when you install the hooks for your harness. Against an enterprise server, the server line names the `enterprise profile`. Before you enroll, the doctor reports `[FAIL] enrollment  this machine isn't enrolled for you yet`, and when the service does not answer, its first line is a failing `service` line that says how to start it. In both cases it still runs the checks that read only this machine.

## Once an agent runs {.nostep}


After you install the hooks, your harness opens a session each time it starts, and the harness pages show how to open one by hand. The service keeps one session for each harness window. `straza status` then lists each one with its harness, its id, the user, how long its token lasts and the state of its kill switch:

```table
service    straza service v1.0.0-1570-gaf326165-dirty (custody user)
server     http://127.0.0.1:8420
identity   alice (device 01a15d8a-bc3b-7821-8651-c3cd0d22f431, key jotq8ezInO5o)
session    claude-code 01a15d8a-bd38-7f04-a43b-1b1090003ec7 as alice (expires in 4m57s, kill switch connected)
audit      nothing waiting to upload
```

The doctor's session line turns green, and it adds a `killswitch` line for the push stream of each session.

## The kill switch {.nostep}


The kill switch is how soon a revocation reaches this machine. `straza service` holds a push stream for each live session, so a revocation reaches a running session within about a second while the service runs and the Straza server can push to it. The service then marks that session revoked, and the next tool call in its window is denied with a reason that starts `Straza: session revoked (kill-switch push from the server).` When the stream is down, the session learns of it at the first call that renews it, once 120 seconds or less of its 300-second token remain, so at most about 180 seconds after the token was minted for a session that keeps working. Nothing has to run besides the service. The doctor's `killswitch` line says whether every live session's stream is connected, and its hint names what to fix when one is not.

## Attestation levels {.nostep}


Every session carries an attestation level, which says how far the server can trust the hook wiring on the machine. The service measures its own binary and, on a managed install, the managed config and the managed hook wiring of the harness, and it reports those hashes when a session checks in. Only root can change the managed config, or on Windows only administrators. Wiring under your home directory is never measured. The server sets the level from the report, and each session keeps its level until it ends.

| Level | When a session gets it | What it tells you |
|---|---|---|
| `managed` | The hooks came from `sudo straza install --managed --server <server-url> <harness>`, or the same command in an elevated PowerShell on Windows, and every hash the service reports matches one the server registered for that harness and platform. | The wiring is unchanged, and on Linux it is root-owned. |
| `advisory` | No managed install exists, after `straza install` without `--managed` or with no install at all, as in the steps above. A managed install lands here too while the server holds no hash for its harness and platform. | Nothing verified the wiring. An agent that can run commands can turn user-mode hooks off. |
| `none` | The check-in carried no hashes, or a managed install reported a file whose hash the server does not hold. | A mismatch is a sign that the managed files changed. |


The server registers those hashes itself. At each start strazad renders the managed hook wiring of Claude Code, Codex CLI and Gemini CLI for Linux, macOS and Windows, which a managed install fetches from it, and stores each hash. [Settings]({{< relref "guides/console/settings.md#keep-the-attestation-registry" >}}) lists them under **Attestation registry**.


A server refuses a session below its `governance.minAttestation`. A standalone server defaults to `none`, so any enrolled machine checks in. An enterprise server defaults to `managed`, so a user-mode machine gets no session, and the agent reads the server's refusal at session start:

```text
Straza: attestation level "advisory" is below the required level "managed". An administrator installs the Straza package for this system, then runs `straza install --managed --server <server-url> <harness>` with sudo, or as an administrator on Windows.
```

A single rule can also ask for a level with `require.attestation`, as the [PolicySet grammar]({{< relref "reference/policyset-grammar.md" >}}) lists.

## Working offline {.nostep}


A session token lives 300 seconds, and the service renews it when a call arrives with two minutes or less left. When the server cannot be reached, the service keeps deciding from the last policy it verified, for the offline grace that `governance.offlineGraceTTL` sets. That is 15 minutes on a standalone server and 0 on an enterprise server, so an enterprise session denies as soon as its token expires. Past the grace, every call is denied with a reason of this shape, where Straza fills in the two durations:

```text
Straza: session token expired <age> ago and the offline grace period (<grace>) is exhausted. Straza cannot verify current policy while the platform is unreachable. This is a connection problem, not a policy decision. Try the call once more in a minute. If it is denied again, tell the user to check the network and run `straza doctor`.
```


A server that answers and refuses the renewal, for example after an admin disabled your account, ends the session at once. The service marks it revoked, and the reason starts `Straza: session revoked (` with the server's own sentence in the parentheses.

[Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md" >}}) lists every lifetime in one table, and [Standalone and enterprise compared]({{< relref "reference/standalone-and-enterprise.md" >}}) lists every default that differs between the profiles.

## Move a machine to another server {.nostep}


A managed install pins the machine's Straza server in the managed configuration, `/etc/straza/config.yaml` on Linux and macOS and `C:\ProgramData\straza\config.yaml` on Windows. To move the machine to another server, an administrator runs the managed install again with the new address. The install fetches the new server's snapshot keys and hook wiring, and it rewrites the managed configuration with them. `straza service` reads that configuration each time it needs it, so the service needs no restart.

Do not start with `straza uninstall --managed`. It removes the managed hook wiring, so the harnesses stop calling Straza, and it leaves the managed configuration in place, so the service still enrolls everyone at the old server.

Run the install with every harness the machine has wired, such as `claude-code codex`, and with your new server's address in place of the example. On Linux:

{{< command terminal="Terminal" purpose="as an administrator" >}}
```sh
sudo straza install --managed --server https://new.example.com claude-code
```
{{< /command >}}

A Mac runs the package's binary by its full path:

{{< command terminal="Terminal" purpose="as an administrator" >}}
```sh
sudo /opt/straza/bin/straza install --managed --server https://new.example.com claude-code
```
{{< /command >}}

On Windows, in an elevated PowerShell:

{{< command terminal="PowerShell" purpose="as an administrator" >}}
```powershell
C:\ProgramData\straza\bin\straza.exe install --managed --server https://new.example.com claude-code
```
{{< /command >}}

{{< see >}}The same lines as at the first install, starting with the `managed config` line.{{< /see >}}

Each enrollment keeps the server it was made at. Until a person enrolls again, the service still checks their sessions in at the old server, and it refuses every policy it fetches from there, because that policy does not verify against the new server's keys. A call that needs such a policy is denied. So right after the install, each person runs `straza enroll` once more as themselves, and the service enrolls them at the new server. Their account has to exist there first: an admin creates it on a standalone server, and your identity manager provisions it to an enterprise server. The new enrollment ends the sessions the machine held for that person, so restart each harness window afterwards. `straza status` then names the new server on its `server` line.

An AI agent on the machine moves with its key. Create its identity on the new server and register its public key there with `strazactl users nhi-key set`, using the public key that `straza keygen` printed when you made the key. If you no longer have it, run `straza keygen --force` as the agent's user and register the new one. Then run `straza enroll --headless --user <agent>` as the agent's user, as [Headless agents]({{< relref "guides/govern-an-agent/headless-agents.md" >}}) shows.

A machine without a managed install keeps the server in each enrollment alone. There each person runs `straza enroll --server https://new.example.com`, and an agent's user runs `straza enroll --headless --server https://new.example.com --user <agent>` once its key is registered at the new server.

On a fleet, run the same managed install on every machine with the tool that deployed Straza, as root on a Mac and as SYSTEM or an administrator on Windows. Put the new address in the scripts of [Deploy with Intune]({{< relref "guides/govern-an-agent/deploy-with-intune.md" >}}) and in the Straza server parameter of [Deploy with Jamf Pro]({{< relref "guides/govern-an-agent/deploy-with-jamf.md" >}}) as well, so a machine that installs later pins the new server. Each person still enrolls once on each machine.

The old server still lists the machine's enrollments. While it runs, revoke them there with an admin API token of the old server that holds `identity:read` and `identity:write` in `STRAZA_API_TOKEN`. `strazactl devices list` prints a person's devices with their ids, and `strazactl devices revoke` revokes one:

{{< command terminal="Terminal" purpose="administration, against the old server" >}}
```sh
export STRAZA_SERVER=https://old.example.com
strazactl devices list alice
strazactl devices revoke alice <device-id>
```
{{< /command >}}

{{< see >}}`revoked device <device-id> of alice. Its key starts no more sessions. To use the machine again, enroll it again with straza enroll.`, with the device id and the username you passed.{{< /see >}}

For an AI agent, `strazactl users nhi-key unset <agent>` against the old server removes the key it registered there.

## Next {.nostep}

- [Claude Code]({{< relref "guides/govern-an-agent/claude-code.md" >}}) installs the hooks for Claude Code and checks the first governed call.
- [Codex CLI]({{< relref "guides/govern-an-agent/codex-cli.md" >}}) does the same for Codex, which also asks you to trust the hooks.
- [Gemini CLI]({{< relref "guides/govern-an-agent/gemini-cli.md" >}}) does the same for Gemini.
- [Any MCP client]({{< relref "guides/govern-an-agent/any-mcp-client.md" >}}) connects a client that has no hooks through the gateway.
