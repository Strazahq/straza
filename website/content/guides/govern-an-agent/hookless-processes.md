---
title: Hookless processes
description: A process with no hook surface runs inside the Straza sandbox image, where every command it runs meets your policy and its obvious bypasses fail.
pagetype: how-to
weight: 60
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: An Alpine 3.20 container against a standalone server, where the three wrapper commands ran under a headless key enrollment and again under a workstation enrollment, the staging copy ran as root without sudo, and the key revocation of the take-down ran, while building the sandbox image and every docker run step were not run because this walk may not run docker build, so their output is the recorded walk of 2026-09-19 against the demo stack
  date: 2026-10-06
applies_to: both
who: You, on the machine that runs the process
where: A terminal with Docker
steps: true
keywords: sandbox exec hookless tier 3
---


You run a process that has no hook surface, such as a script or an agent framework without hooks, inside the Straza sandbox image. You work in a terminal with Docker on the machine that runs the process. At the end, every command the process runs is a policy decision, and you have seen its obvious bypasses fail.


`straza exec` decides each command against policy before the child runs. On an open machine it is exactly as advisory as a user-mode hook, because an agent that can run anything can skip the wrapper. Inside the sandbox image the wrapper becomes the process's only way to make the kernel run anything. That makes the image one of the lanes that hold at a boundary, as [the trust model]({{< relref "concepts/trust-model-and-non-goals.md" >}}) explains.

## Before you start {.nostep}


- The `straza` binary on your PATH, enrolled against your Straza server. A person's machine enrolls as [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) shows, and an AI agent enrolls with a key as [Headless agents]({{< relref "guides/govern-an-agent/headless-agents.md" >}}) shows.
- Docker, for the image.
- A role whose policy covers the commands the process runs. The example runs as the AI agent `sandbox-bot`, which holds the seeded `demo-tools-sandbox` role of the [demo stack]({{< relref "get-started/enterprise-demo-stack.md" >}}), against `http://127.0.0.1:8420`. A standalone server needs no role, because its seeded `standalone-starter` PolicySet applies to every identity.

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

## Stage the enrollment


The container needs an enrollment, and no credential is ever baked into the image. Copy one into a directory the container mounts read-only. The example stages a headless enrollment made with a key, so the directory holds `config.yaml`, `state/identity.json` and `state/nhi-key.json`. A workstation enrollment has only the first two, so leave out the `nhi-key.json` line. The container runs as uid 65532, which must be able to read the files.

{{< command terminal="Terminal" purpose="on the process's machine" >}}
```sh
mkdir -p enroll/state
cp ~/.straza/config.yaml enroll/config.yaml
cp ~/.straza/state/identity.json enroll/state/identity.json
cp ~/.straza/state/nhi-key.json enroll/state/nhi-key.json
sudo chown -R 65532 enroll
```
{{< /command >}}

{{< now title="Treat the directory like a key file" >}}`identity.json` holds the enrollment credential, and `nhi-key.json` holds the agent's private key.{{< /now >}}

## Run the process inside the boundary


Run the container with the flags that finish the boundary: a read-only root filesystem, `noexec` tmpfs mounts and dropped capabilities. The flags below are the runtime hardening of `deploy/sandbox/compose.yaml`, adapted to a single host. The host network lets the container reach a server on loopback, and the staged state lives on a tmpfs that the container's unprivileged user can write.

{{< command terminal="Terminal" purpose="on the process's machine" >}}
```sh
FLAGS="--rm --network host --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  --pids-limit 256 --tmpfs /run/straza:mode=0777,noexec,nosuid,nodev \
  --tmpfs /home/agent:mode=0777,noexec,nosuid,nodev --tmpfs /tmp:mode=1777,noexec,nosuid,nodev \
  -v $PWD/enroll:/straza/enroll:ro -e STRAZA_SERVER_URL=http://127.0.0.1:8420"
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


Run the doctor inside the boundary. It reports the enrollment, the server and the snapshot, the same as on a workstation.

{{< command terminal="Terminal" purpose="on the process's machine" >}}
```sh
docker run $FLAGS straza/sandbox-agent straza doctor
```
{{< /command >}}

{{< see >}}`[ ok ]` on the `enrollment`, `identity` and `server` lines.{{< /see >}}

For a headless enrollment, the identity line reads `sandbox-bot: headless AI agent enrollment (nhi-key lane), sessions are deviceless`, with your agent's name in place of `sandbox-bot`. The server line names the server's profile, `enterprise profile` on the demo stack.

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


Stop the container and delete the staged enrollment directory. Removing the agent's key with `strazactl users nhi-key unset sandbox-bot`, or with **Revoke key** on its sheet in the console, stops new sessions. A session that is already running keeps going. To end the running ones as well, revoke them with `strazactl sessions revoke` or lock the identity with `strazactl users lock`, as the [Kill-switch runbook]({{< relref "guides/operate/kill-switch-runbook.md" >}}) shows. For a workstation enrollment, `strazactl devices revoke` cuts the sessions bound to that device as well as new ones.

## Next {.nostep}

- [MCP servers and the gateway]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}) explains the lane for MCP calls, where the credential never reaches the agent.
- [Headless agents]({{< relref "guides/govern-an-agent/headless-agents.md" >}}) enrolls an AI agent with a key, as the example here did.
