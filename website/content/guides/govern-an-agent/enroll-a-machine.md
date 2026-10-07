---
title: Enroll a machine
description: This machine holds a device credential for your Straza identity, and straza status and straza doctor show it ready for an agent.
pagetype: how-to
weight: 5
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: An Alpine Linux container against a standalone server, as the local user alice, with the sign-in page answered by a form post. The enterprise sign-in output is the recorded walk against the demo stack of 2026-09-19
  date: 2026-10-06
applies_to: both
who: You, on the machine where your agent runs
where: A terminal and a browser
steps: true
keywords: enroll device credential sign in attestation status doctor offline grace daemon
---


You enroll a machine once, before you connect any agent on it. Enrolling signs you in and leaves a device credential on the machine, so each agent session there checks in as you without another sign-in. The pages for [Claude Code]({{< relref "guides/govern-an-agent/claude-code.md" >}}), [Codex CLI]({{< relref "guides/govern-an-agent/codex-cli.md" >}}) and [Gemini CLI]({{< relref "guides/govern-an-agent/gemini-cli.md" >}}) start where this page ends.

## Before you start {.nostep}


- The `straza` binary on your PATH, from [Install]({{< relref "get-started/install.md" >}}).
- The address of your Straza server. The commands below use `http://127.0.0.1:8420`, so put your own address in its place.
- An account the server knows. A standalone server keeps its own users, so an admin creates yours with `strazactl users create` and gives you its password. An enterprise server signs you in at your identity provider, and your identity manager provisions your account to Straza over SCIM.

An AI agent with no person at the keyboard enrolls with a key of its own instead, as [Headless agents]({{< relref "guides/govern-an-agent/headless-agents.md" >}}) shows.

## Sign in and enroll


Run `straza enroll` with your server's address.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza enroll --server http://127.0.0.1:8420
```
{{< /command >}}

It prints a link and a code, then waits. Open the link in a browser, check that the page shows the same code, and sign in. A standalone server signs you in itself, on its `/oidc/device` page, where you enter your local username and password. An enterprise server sends you to your identity provider, and enroll names it first, in a line that starts `Signing in at your identity provider:`.

{{< see >}}`Enrolled as alice (device 01a112c7-878a-7962-8ba3-817c4d9007f1).` with your own username and device id.{{< /see >}}

{{< details summary="Recorded output, standalone" >}}
```text
To authorize this device, open:
  http://127.0.0.1:8420/oidc/device?user_code=JSSJ-G7RH
and confirm code JSSJ-G7RH
waiting for approval in the browser…
Enrolled as alice (device 01a112c7-878a-7962-8ba3-817c4d9007f1).
```
{{< /details >}}


{{< details summary="Recorded output, enterprise demo stack" >}}
```text
Signing in at your identity provider: http://localhost:8480/realms/straza
To authorize this device, open:
  http://localhost:8480/realms/straza/device?user_code=MSSR-GFNQ
and confirm code MSSR-GFNQ
waiting for approval in the browser…
Enrolled as petra (device 01a0b93c-6508-73b0-ae5a-057c01c6c5df).
```
{{< /details >}}


You can set `STRAZA_SERVER` in place of `--server`. Enroll then says so on stderr before it starts, in the form `enrolling at http://127.0.0.1:8420 (from $STRAZA_SERVER)`.


The client keeps the server's address, the keys that check every policy the server sends, and your device credential in `.straza` under your home directory. A device credential lasts 30 days from its issue or its last renewal. A check-in renews it once it is past half that life, so a machine that stops checking in loses it 15 to 30 days after its last check-in. When it is gone, run `straza enroll` again.


{{< fails >}}
`the device code expired before it was approved. Run enroll again and approve the NEWLY printed code (a browser tab from an earlier attempt shows a stale one)`
: Run `straza enroll` again and approve the code it prints this time.

`That code is not valid or has expired. Check it against the console or terminal and try again.`
: The standalone sign-in page did not accept the code. Copy it from the terminal again, or start enroll over.

`Invalid username or password.`
: The standalone sign-in page refused the password. Ask the admin who created your account for it.

`--server is required (or set STRAZA_SERVER)`
: Pass `--server` with your server's address, or set `STRAZA_SERVER`.

An error that starts `oidcflow: reach` and ends `connect: connection refused`
: Nothing answers at that address. Check the address, and check that strazad runs there.
{{< /fails >}}

## Check the enrollment


Ask the client what it holds.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza status
```
{{< /command >}}

{{< see >}}Your server, your identity, and `session    none (start a harness session)`.{{< /see >}}

```table
server     http://127.0.0.1:8420
identity   alice (device 01a112c7-878a-7962-8ba3-817c4d9007f1)
session    none (start a harness session)
```

No session exists yet, because an agent opens one when it starts. On a machine that is not enrolled, `straza status` prints `not enrolled`.

## Run the doctor


`straza doctor` checks each part of the setup and prints a hint under every line that needs action. It exits 1 when a line fails, so a script can stop on it.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza doctor
```
{{< /command >}}

{{< see >}}`[ ok ]` on enrollment, identity and server, and `[WARN]` on session, snapshot and wiring.{{< /see >}}

```table
[ ok ] enrollment  server http://127.0.0.1:8420, 1 snapshot key(s) pinned, straza v1.1.0-117-g106081a8
[ ok ] identity    alice (device 01a112c7-878a-7962-8ba3-817c4d9007f1), device credential valid until 2026-11-05
[ ok ] server      http://127.0.0.1:8420 healthy (strazad v1.1.0-117-g106081a8, standalone profile)
[WARN] session     no active session
                   → start a harness session (the SessionStart hook checks in), or pipe a SessionStart payload through `straza hook`
[WARN] snapshot    no cached snapshot
                   → start a harness session. SessionStart downloads and verifies the active snapshot
[WARN] wiring      no harness has Straza hooks wired
                   → run `straza install claude-code` (or codex/gemini). Enterprise layouts use `straza install --managed --server <server-url> claude-code` (or codex/gemini)
[ ok ] audit-spool no pending audit events (no successful drain recorded yet)
[ ok ] trace       journal on, 0 records; debug off
```

One line is trimmed from this output, the `approver` line about the listener that phones use to approve.


Expect the three warnings on a fresh enrollment. The session and snapshot lines turn green when your agent's first session checks in, and the wiring line turns green when you install the hooks for your harness. Against an enterprise server, the server line names the `enterprise profile`. Before you enroll, the doctor reports `[FAIL] enrollment  no straza state found` and still runs the checks that read only this machine.

## Once an agent runs {.nostep}


After you install the hooks, your harness opens a session each time it starts, and the harness pages show how to open one by hand. `straza status` then names the session, its roles, its attestation and the policy it holds:

```table
server     http://127.0.0.1:8420
identity   alice (device 01a112c7-878a-7962-8ba3-817c4d9007f1)
session    01a112c7-a149-7de0-83b2-708a2cbad1e5 (roles [], attestation advisory)
snapshot   e9063b81e486b94f5475d2aec841137a78708a45042a81462628e25aea44c33c
killswitch edge push (SSE on the server origin, /v1/push); poll-refresh is the backstop; straza doctor probes the lane
```

The role list is empty here because alice holds no role on this standalone server. The doctor's session and snapshot lines turn green, and it adds a `killswitch` line:

```table
[WARN] killswitch  edge push (SSE via http://127.0.0.1:8420/v1/push): verified from here (the server accepted a push subscription), but no daemon heartbeat exists in this straza home: no daemon is subscribed to hear a push
                   → start `straza daemon` (and keep it running) for sub-second revocation; without one revocation waits for the next hook call's poll-refresh, else token TTL
```

## Run the daemon {.nostep}


That warning concerns how soon a revocation reaches this machine. Each hook still checks its session on every call and denies when it cannot, with or without a daemon. `straza daemon` keeps a connection open to the server and hears a revocation at once, so a session an admin revokes ends on this machine in under a second. Without it, the revocation waits for a hook's next renewal of the session token. Start the daemon after a session exists, in a terminal of its own or as a service of your login.

{{< command terminal="Terminal" purpose="the daemon, keep it running" >}}
```sh
straza daemon
```
{{< /command >}}

```text
daemon: edge push subscribing; poll-refresh remains the backstop
daemon: kill-switch push active (gateway edge)
```

The doctor's `killswitch` line then ends `daemon alive (pid 934): sub-second revocation`. With no session, the daemon stops at once with `no active session (start a harness session first)`.

## Attestation levels {.nostep}


Every session carries an attestation level, which says how far the server can trust the hook wiring on the machine. The client reports file hashes when a session checks in, and the server sets the level from them. Each session keeps its level until it ends.

| Level | When a session gets it | What it tells you |
|---|---|---|
| `managed` | The hooks came from `sudo straza install --managed --server <server-url> <harness>`, and every hash the client reports matches one the server registered for that harness and platform. | The wiring is root-owned and unchanged. |
| `advisory` | The client runs in user mode, after `straza install` without `--managed` or with no install at all, as in the steps above. A managed install lands here too while the server holds no hash for its harness and platform. | The client reported hashes, and nothing verified them. An agent that can run commands can turn user-mode hooks off. |
| `none` | The check-in carried no hashes, or a managed install reported a file whose hash the server does not hold. | A mismatch is a sign that the managed files changed. |


A server refuses a session below its `governance.minAttestation`. A standalone server defaults to `none`, so any enrolled machine checks in. An enterprise server defaults to `managed`, so a user-mode machine gets no session, and the agent reads this at session start:

```text
Straza: checkin failed: /v1/checkin: attestation level "advisory" is below the required level "managed". Reinstall with `straza install --managed <harness>` or contact your administrator
```

A single rule can also ask for a level with `require.attestation`, as the [PolicySet grammar]({{< relref "reference/policyset-grammar.md" >}}) lists.

## Working offline {.nostep}


A session token lives 300 seconds, and a hook renews it once it has less than two minutes left. When the server cannot be reached, the hook keeps deciding from the last policy it verified, for the offline grace that `governance.offlineGraceTTL` sets. That is 15 minutes on a standalone server and 0 on an enterprise server, so an enterprise hook denies as soon as its token expires. Past the grace, every call is denied with a reason of this shape, where Straza fills in the two durations:

```text
Straza: session token expired <age> ago and the offline grace period (<grace>) is exhausted. Straza cannot verify current policy while the platform is unreachable; reconnect and run `straza doctor`.
```


A server that answers and refuses the renewal, for example after an admin disabled your account, ends the grace at once. The reason then starts `Straza: session renewal was refused:` and carries the server's own sentence.

[Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md" >}}) lists every lifetime in one table, and [Standalone and enterprise compared]({{< relref "reference/standalone-and-enterprise.md" >}}) lists every default that differs between the profiles.

## Next {.nostep}

- [Claude Code]({{< relref "guides/govern-an-agent/claude-code.md" >}}) installs the hooks for Claude Code and checks the first governed call.
- [Codex CLI]({{< relref "guides/govern-an-agent/codex-cli.md" >}}) does the same for Codex, which also asks you to trust the hooks.
- [Gemini CLI]({{< relref "guides/govern-an-agent/gemini-cli.md" >}}) does the same for Gemini.
- [Any MCP client]({{< relref "guides/govern-an-agent/any-mcp-client.md" >}}) connects a client that has no hooks through the gateway.
