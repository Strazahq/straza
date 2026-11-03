---
title: Hookless processes
description: A process with no hook surface runs inside the Straza sandbox image, where every command it runs meets your policy and its obvious bypasses fail.
summary: >-
  `straza exec` decides each command against policy before it runs, but on an open machine an
  agent can skip it. Inside the Straza sandbox image the wrapper is the only way to run a
  program, and a service container keeps the key, so every command the process runs meets your
  policy.
pagetype: how-to
weight: 60
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: An Alpine 3.20 container against a standalone server, where the three wrapper commands ran under a headless key enrollment and again under a workstation enrollment, the staging copy ran as root without sudo, and the key revocation of the take-down ran, and its sentence that the revocation ends the running sessions and refuses the held ID token was written from the code on 2026-10-27 and not walked, while building the sandbox image and every docker run step were not run because this walk may not run docker build, so their output is the recorded walk of 2026-09-19 against the demo stack
  date: 2026-10-06
  except: building the sandbox image and every docker run step, whose output comes from an earlier recorded walk
applies_to: both
who: You, on the machine that runs the process
where: A terminal with Docker
steps: true
keywords: sandbox exec hookless tier 3 straza service background service
---


You run a process that has no hook surface, such as a script or an agent framework without hooks, inside the Straza sandbox image. You work in a terminal with Docker on the machine that runs the process. At the end, every command the process runs is a policy decision, and you have seen its obvious bypasses fail.


`straza exec` decides each command against policy before the child runs. On an open machine it is exactly as advisory as a user-mode hook, because an agent that can run anything can skip the wrapper. Inside the sandbox image the wrapper becomes the process's only way to make the kernel run anything. That makes the image one of the lanes that hold at a boundary, as [the security model]({{< relref "evaluate/security-model.md#what-each-enforcement-point-is-worth" >}}) explains.

## Before you start {.nostep}


- For the first step, the `straza` binary on your PATH, with `straza service` running and enrolled against your Straza server. A person's machine enrolls as [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) shows, and an AI agent enrolls with a key as [Headless agents]({{< relref "guides/govern-an-agent/headless-agents.md" >}}) shows.
- Docker on Linux, for the image. The sandbox enrolls through a service container of its own, so it needs no enrollment from the host.
- A role whose policy covers the commands the process runs. The example runs as the AI agent `sandbox-bot`, which holds the seeded `demo-tools-sandbox` role of the [demo stack]({{< relref "evaluate/try-it-on-the-demo-stack.md" >}}), against `http://127.0.0.1:8420`. A fresh enterprise server holds no PolicySet and denies every command that no rule allows, so an admin first sets up the role and the PolicySet of [Give agents their local tools]({{< relref "guides/operate/enterprise-shape.md#give-agents-their-local-tools" >}}) and gives the agent its `developer` role. A standalone server needs no role, because its seeded `standalone-starter` PolicySet applies to every identity.

## Run a command through the wrapper


Prefix any command with `straza exec --`. An allowed command runs with its output and exit code passed straight through. A denied one prints the reason and exits 2.

{{< command terminal="Terminal" purpose="on the process's machine" >}}
```sh
straza exec -- date
straza exec -- rm -rf /tmp/probe
straza exec -- sh -c 'echo hello; exit 7'
```
{{< /command >}}

{{< see >}}The date, the policy's reason for the deny, and `hello`. The `rm -rf` line exits 2, and the `echo` line exits 7, the child's own code.{{< /see >}}

```text
Sat Sep 19 10:56:46 AM UTC 2026
Straza: destructive command denied by the agent guardrails
hello
```

That block ran under a headless enrollment with a key, and a workstation enrollment behaves the same way. A session from the wrapper checks in under the harness name `exec-wrapper`, so a PolicySet can give these processes rules of their own. On an open machine this is still advisory, and the next steps close that.


Against a standalone server, the denied line carries the reason of the seeded `standalone-starter` PolicySet, `Straza starter policy: recursive force-delete is denied. Delete files individually, or an admin can edit the standalone-starter PolicySet.`

## Build the sandbox image


The sandbox image removes every way to run a program except the wrapper. Its PATH holds only `straza`, `wexec`, the governed shell and the agent's own interpreter. A wrapper replaces both `/bin/sh` and `/bin/bash` and routes every shell string through `straza exec`. At build time, a sweep strips execute and read from every other file and fails the build if anything unexpected stays executable.

Build it from the root of the Straza repository.

{{< command terminal="Terminal" purpose="in the repository root" >}}
```sh
docker build -f deploy/sandbox/Dockerfile -t straza/sandbox-agent .
```
{{< /command >}}

## Enroll through the service


The process never holds a credential. A second container runs `straza service`, the Straza background service, from the same image under its own account, uid 64000. It keeps the enrollment and the key in a volume only it mounts, and every session in its memory. The process reaches it through a socket in a volume the two containers share, where the kernel names the process's uid on every call.

Create the two volumes and start the service container. It uses the host network to reach a server on loopback, like the process container later.

{{< command terminal="Terminal" purpose="on the process's machine" >}}
```sh
docker volume create straza-sock
docker volume create straza-state
docker run -d --name straza-service --network host --read-only --cap-drop ALL \
  --security-opt no-new-privileges:true --user 64000:64000 \
  --tmpfs /tmp:mode=1777,noexec,nosuid,nodev \
  -v straza-sock:/run/straza-service -v straza-state:/var/lib/straza-service \
  --entrypoint /opt/straza/bin/straza straza/sandbox-agent service
```
{{< /command >}}

The one-off commands below run `straza` as the process's user, uid 65532, in a fresh container that mounts the socket volume read-only, so the service enrolls exactly the account the process runs as. Make a key for the AI agent, and have an admin register the public half it prints.

{{< command terminal="Terminal" purpose="on the process's machine" >}}
```sh
S="--rm --network host --read-only --tmpfs /home/agent:mode=0777,noexec,nosuid,nodev \
  -v straza-sock:/run/straza-service:ro --entrypoint /opt/straza/bin/straza"
docker run $S straza/sandbox-agent keygen --user sandbox-bot
```
{{< /command >}}

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl users nhi-key set sandbox-bot <publicKey>
```
{{< /command >}}

Then enroll the agent headless through the service.

{{< command terminal="Terminal" purpose="on the process's machine" >}}
```sh
docker run $S straza/sandbox-agent enroll --headless --user sandbox-bot --server http://127.0.0.1:8420
```
{{< /command >}}

{{< see >}}`Enrolled headless as sandbox-bot.` followed by a sentence that the Straza background service holds this agent's key.{{< /see >}}

A person enrolls the same way with `enroll --use-device-code --server http://127.0.0.1:8420` and no `--headless`, by the code it prints, because the container has no browser to open. A server whose `governance.minSignIn` is `browser`, the enterprise default, refuses that sign-in for a person's machine.

{{< now title="Back up the state volume like a key file" >}}Nothing in the process's container holds a credential. The key never leaves the `straza-state` volume, which only the service mounts. Removing that volume ends the enrollment.{{< /now >}}

## Run the process inside the boundary


Run the container with the flags that finish the boundary: a read-only root filesystem, `noexec` tmpfs mounts and dropped capabilities. The flags below are the runtime hardening of `deploy/sandbox/compose.yaml`, adapted to a single host. The socket volume is mounted read-only, so the process can connect to the service and place nothing beside its socket. `STRAZA_SERVER_URL` is only a cross-check: the entrypoint refuses to start when it differs from the server the service enrolled the process with.

{{< command terminal="Terminal" purpose="on the process's machine" >}}
```sh
FLAGS="--rm --network host --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  --pids-limit 256 -v straza-sock:/run/straza-service:ro \
  --tmpfs /home/agent:mode=0777,noexec,nosuid,nodev --tmpfs /tmp:mode=1777,noexec,nosuid,nodev \
  -e STRAZA_SERVER_URL=http://127.0.0.1:8420"
```
{{< /command >}}

Now run three processes inside it. A shell string from inside Python is governed too, because `/bin/sh` is the wrapper.

{{< command terminal="Terminal" purpose="on the process's machine" >}}
```sh
docker run $FLAGS straza/sandbox-agent sh -c 'echo hello; exit 7'
docker run $FLAGS straza/sandbox-agent wexec rm -rf /tmp/x
docker run $FLAGS straza/sandbox-agent python3 -c 'import subprocess; print("returncode", subprocess.run("rm -rf /", shell=True).returncode)'
```
{{< /command >}}

{{< see >}}`hello`, then the policy's reason twice, then `returncode 2`.{{< /see >}}

```text
hello
Straza: destructive command denied by the agent guardrails
Straza: destructive command denied by the agent guardrails
returncode 2
```

The first exits 7 and the second exits 2. The third prints the reason once more, because the same rule decides its shell string before Python reports the child's code. Each run also prints one `Straza sandbox: governed profile ready` line to stderr, left out here.

## Check the setup from inside


Run the doctor inside the boundary. It reports the service, the enrollment and the server, the same as on a workstation.

{{< command terminal="Terminal" purpose="on the process's machine" >}}
```sh
docker run $FLAGS straza/sandbox-agent straza doctor
```
{{< /command >}}

{{< see >}}`[ ok ]` on the `service`, `enrollment` and `server` lines.{{< /see >}}

For an AI agent, the enrollment line names it as `sandbox-bot (AI agent, no device)`, with your agent's name in place of `sandbox-bot`. The server line names the server's profile, `enterprise profile` on the demo stack.

## Confirm the bypasses are dead


Two obvious escapes fail. A shell string from Python meets the wrapper, as the step before showed. A direct run of a program by its absolute path fails too, because the build sweep left the file unreadable and not executable, so Python cannot start it.

{{< command terminal="Terminal" purpose="on the process's machine" >}}
```sh
docker run $FLAGS straza/sandbox-agent python3 -c 'import subprocess; subprocess.run(["/usr/bin/ls","/"])'
```
{{< /command >}}

{{< see >}}A `PermissionError` for `/usr/bin/ls`.{{< /see >}}

```text
PermissionError: [Errno 13] Permission denied: '/usr/bin/ls'
```

## What the image leaves open {.nostep}


The real shell is stashed where the wrapper starts it, and the same user can run it directly, so an ungoverned shell is reachable. That shell is bounded by the same small set of executables. It can print, and it cannot reach `/usr/bin/ls` any more than the governed routes could.

```sh
docker run $FLAGS straza/sandbox-agent /straza/real/sh -c 'echo ungoverned shell'
docker run $FLAGS straza/sandbox-agent /straza/real/sh -c '/usr/bin/ls /'
```

```text
ungoverned shell
/straza/real/sh: 1: /usr/bin/ls: Permission denied
```


In-process computation is ungoverned by design, because the agent's own interpreter must be executable. Straza governs execution and MCP traffic, and what the interpreter computes in memory stays ungoverned. Every binary on the image's allowlist is reachable by the same user without a decision, so a per-argument rule holds at the boundary only for a tool that is absent from the image. The hard guarantee of the image is its set of executables, plus fail-closed governance of everything outside it.

Network access stays open as well. The single-host flags above use the host network, and the shipped `deploy/sandbox/compose.yaml` leaves its container network routable. To let the agent reach strazad and nothing else, set `internal: true` on that network and attach only strazad, as the comments in the file describe.


The server cannot tell from the wire that a session ran inside the image, so that claim is your deployment's, as [Known limits]({{< relref "security/known-limits.md" >}}) lists. Keep high-blast-radius permissions on sessions that attest `managed`, and require that level for sensitive roles. Put calls to an MCP server behind the gateway instead of baking the tool into the image, so the credential stays on the server too.

## Take the boundary down {.nostep}


Stop the service container and delete the two volumes, which ends the enrollment on this host.

{{< command terminal="Terminal" purpose="on the process's machine" >}}
```sh
docker rm -f straza-service
docker volume rm straza-sock straza-state
```
{{< /command >}}

Removing the agent's key with `strazactl users nhi-key unset sandbox-bot`, or with **Revoke key** on its sheet in the console, ends the agent's running sessions at once. The server also refuses the ID token the service still holds for the agent, because that token names the removed key, so no new session starts. A different key ends them the same way. To stop the identity on every lane, lock it with `strazactl users lock`, as the [Kill-switch runbook]({{< relref "guides/operate/kill-switch-runbook.md" >}}) shows. For a workstation enrollment, `strazactl devices revoke` cuts the sessions bound to that device as well as new ones.

## Next {.nostep}

- [MCP servers and the gateway]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}) explains the lane for MCP calls, where the credential never reaches the agent.
- [Headless agents]({{< relref "guides/govern-an-agent/headless-agents.md" >}}) enrolls an AI agent with a key, as the example here did.
