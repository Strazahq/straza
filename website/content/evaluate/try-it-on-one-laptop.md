---
title: Try it on one laptop
description: Run Straza and Claude Code on your own computer. A starter rule denies a command, and you approve a held command in the browser.
aliases:
  - /get-started/first-governed-session/
pagetype: tutorial
weight: 70
draft: false
summary: >-
  You start the server on your computer, enroll that computer and connect Claude Code. The starter rule denies `rm -rf` at once. A rule you add holds `git push` until you approve it yourself in this browser. The agent's next try within 60 seconds then runs, and the approval covers only that try.
who: Admin, and later alice
where: Four terminals and a browser
steps: true
applies_to: standalone
tested:
  version: v1.0.0-1700-g89d0cf1d
  platform: in a fresh Alpine 3.20 container on Linux amd64, with the browser steps in headless Chromium. The approve dialog's sentence and its toast were reworded from the code on 2026-11-03 and not walked
  date: 2026-11-02
  except: Claude Code itself, whose calls went to straza hook by hand, and the enroll browser sign-in, done by device code
keywords: try laptop standalone enroll install first deny confirm approve self-service this browser requests retry claude code
---


You run Straza on your own machine as a [person]({{< relref "concepts/identities.md" >}}) named alice and govern her Claude Code. The standalone profile includes its own database, event broker and sign-in page, so nothing else has to run. A [policy]({{< relref "concepts/policy-model.md" >}}) rule denies a command at once. Then a rule you write holds a command until you [approve]({{< relref "concepts/approvals-model.md" >}}) it.

## Before you start {.nostep}


- A Linux or macOS machine with no Straza package and no managed install, because either one makes every `straza` command use the system service. On Windows the service runs only as the Windows service, as [Install]({{< relref "guides/operate/install.md#install-the-service-on-windows" >}}) shows.
- The binaries `strazad`, `strazactl` and `straza` on your PATH, as [Put the binaries on your path]({{< relref "guides/operate/install.md#put-the-binaries-on-your-path" >}}) shows.
- Claude Code, a browser on this machine and an empty directory for the server.

## Start the server


In the first terminal, in the empty directory, start the server on the standalone profile. On a shared network, such as an office or a café, put `STRAZA_APPROVER_TLS_AUTO_MINT=false` in front of `strazad`. That turns off the phone approver, which otherwise listens on port 8443 on every interface. This tutorial approves in the browser through port 8420 and does not need it.

{{< command terminal="Terminal 1" purpose="server, keep it running" >}}
```sh
strazad serve --profile standalone
```
{{< /command >}}

{{< see >}}The log says `strazad serving` at `127.0.0.1:8420`. Above it are the passwords of `admin` and `break-glass`, and a line that says the starter policy denies `rm -rf`.{{< /see >}}

{{< now title="Save both passwords now" >}}The first start prints them once. A restart of the same data directory does not print them again.{{< /now >}}

{{< details summary="Recorded output" >}}
It shows only the lines named above, with the passwords masked. The walk's server listened on every address of its container and answered at `http://127.0.0.1:18420`, as the final fold explains.

```text
{"level":"WARN","msg":"bootstrap admin created. Store this password now, it will not be shown again","username":"admin","password":"5861xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}
{"level":"INFO","msg":"starter policy seeded. Recursive force-delete (rm -rf) is denied with a reason; edit or delete PolicySet standalone-starter to change this"}
{"level":"WARN","msg":"break-glass admin created. Store this password in your vault now, it will not be shown again; rotate on-box via `strazactl users set-password break-glass`","username":"break-glass","password":"95c4xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}
{"level":"INFO","msg":"strazad serving","addr":"[::]:8420","profile":"standalone","publicUrl":"http://127.0.0.1:18420","tls":false,"version":"v1.0.0-1700-g89d0cf1d"}
```
{{< /details >}}

The server keeps its state under `data` in that directory. Its console and API listen on 127.0.0.1 only. [Ports and network]({{< relref "reference/ports-and-network.md" >}}) lists every port the server opens.

## Sign in to the console


Open `http://127.0.0.1:8420/console/` in your browser and press **Sign in**. On the page titled Sign in to Straza, enter `admin` and the saved password.

{{< see >}}The console opens on Overview. The bottom of the menu says `standalone` and the server's build.{{< /see >}}

`strazactl` has no sign-in of its own, so mint it an admin API token here.

{{< clicks "Settings" "API tokens" "New API token" >}}

Name it `admin-terminal`. Under **Lifetime** pick `expires in 30 days`, under **The job** pick **Full root**, and press **Mint token**. Copy the token now, because the sheet shows it once and the server keeps only its hash.

{{< fails >}}
`Invalid username or password.`
: Use the `admin` password from the first start of this data directory.
{{< /fails >}}

## Add alice


In a second terminal, pass the token to `strazactl`. At the `read` line, paste the token and press Enter, which keeps it off the screen and out of the shell history. Then create alice, the person your agent works for. The [role]({{< relref "concepts/roles-and-access.md" >}}) `straza-enroll-browser` lets her turn a browser into an approval device.

{{< command terminal="Terminal 2" purpose="administration" >}}
```sh
export STRAZA_SERVER=http://127.0.0.1:8420
read -rs STRAZA_API_TOKEN && export STRAZA_API_TOKEN
strazactl users create alice --password 'pick-a-passphrase'
strazactl assign straza-enroll-browser --user alice
```
{{< /command >}}

{{< see >}}`created user alice` with an id, then `assigned straza-enroll-browser to alice`.{{< /see >}}

{{< details summary="Recorded output" >}}
```text
created user alice (01a19de0-43b0-7327-82a8-d66405f53f36)
assigned straza-enroll-browser to alice
```
{{< /details >}}

Whoever holds the token acts as an administrator until it expires or you revoke it, so keep it in this terminal only. Never put it in a shell profile or anywhere an agent runs.

{{< fails >}}
`strazactl needs an admin API token in STRAZA_API_TOKEN.`
: Run the `read` line again in this terminal and paste the token.
{{< /fails >}}

## Start the background service


The background service, `straza service`, keeps alice's key and every Straza [session]({{< relref "concepts/machines-and-sessions.md" >}}) that runs on this machine. With `STRAZA_HOME` set, it runs as your own OS user and keeps its socket and its state in that directory instead of the system location.

{{< command terminal="Terminal 3" purpose="background service, keep it running" >}}
```sh
export STRAZA_HOME="$HOME/.straza"
straza service
```
{{< /command >}}

{{< see >}}A log line that says `straza service listening`, with the socket path and `user_mode=true`.{{< /see >}}

## Enroll this machine as alice


Open a fourth terminal for alice. It shares `STRAZA_HOME` with the service but has no token, so the Claude Code you start here cannot read the token.

{{< command terminal="Terminal 4" purpose="alice" >}}
```sh
export STRAZA_HOME="$HOME/.straza"
straza enroll --server http://127.0.0.1:8420
```
{{< /command >}}

Your browser opens on the sign-in page. Sign in as `alice` with her passphrase. The tab says that you are signed in to Straza on this computer.

{{< see >}}`Enrolled as alice`, the id of this device, and that the background service holds this machine's key.{{< /see >}}

The enrollment lasts 7 days from alice's sign-in. After that the server refuses this machine's check-ins until she runs `straza enroll` again.

{{< fails >}}
``Straza's background service for STRAZA_HOME … isn't running``
: Start `straza service` in Terminal 3, and export the same `STRAZA_HOME` in both terminals.

`This machine has no display, so Straza cannot open a browser here.`
: Add `--use-device-code`, open the link it prints, and sign in there as alice.
{{< /fails >}}

## Connect Claude Code


In Terminal 4, install the hooks for your user, then start Claude Code in a throwaway directory.

{{< command terminal="Terminal 4" purpose="alice" >}}
```sh
straza install claude-code
mkdir -p ~/straza-try && cd ~/straza-try
claude
```
{{< /command >}}

{{< see >}}`installed Straza hooks for claude-code` with the settings path, then the banner `Straza governance active: alice` when Claude Code starts.{{< /see >}}

Ask Claude Code to run `rm -rf /tmp/x`. The starter rule denies it, and Claude Code shows the reason, `Straza starter policy: this sample rule denies commands that start with rm -rf.` A command that no rule names, such as `git status`, runs.

{{< details summary="The same calls without Claude Code" >}}
Claude Code runs `straza hook` before each tool call and reads its answer. To watch that without Claude Code, send the same events by hand in Terminal 4. The hook only returns a decision.

```sh
printf '%s' '{"hook_event_name":"SessionStart","session_id":"laptop-1","cwd":"/tmp"}' | straza hook --harness claude-code
printf '%s' '{"hook_event_name":"PreToolUse","session_id":"laptop-1","cwd":"/tmp","tool_name":"Bash","tool_input":{"command":"rm -rf /tmp/x"}}' | straza hook --harness claude-code; echo "exit $?"
```

```text
{"hookSpecificOutput":{"additionalContext":"Straza governs this session as \"alice\". Every tool call is checked against policy and recorded in the audit log. When a call is denied, tell the user what the reason says, then do what it says next: wait for a person's approval, change the call, or try once more later. A denial that says none of these is a policy decision, and the same call will be denied again. Do not try to reach the denied result another way.","hookEventName":"SessionStart"},"systemMessage":"🛡 Straza governance active: alice (straza-enroll-browser) @ http://127.0.0.1:18420 · policy c485b47b783a · attestation advisory"}
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Straza starter policy: this sample rule denies commands that start with rm -rf. Delete files individually, or an admin can edit the standalone-starter PolicySet."}}
Straza starter policy: this sample rule denies commands that start with rm -rf. Delete files individually, or an admin can edit the standalone-starter PolicySet.
exit 2
```

Claude Code reads the JSON on standard output and shows you the reason line from standard error. Exit status 2 tells it not to run the tool.
{{< /details >}}

## Hold git push for yourself


A rule with the mode `confirm` holds a call until the person the session acts for approves it. Nobody else can approve it, an admin included. It guards against an agent's mistake or a prompt injection. It does not guard against you. In Terminal 2, write the rule and activate it.

{{< command terminal="Terminal 2" purpose="administration" >}}
```sh
cat > confirm-git-push.yaml <<'EOF'
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: confirm-git-push
spec:
  rules:
    - id: confirm-git-push
      tools: [shell.exec]
      command:
        allowPatterns: ["git push*"]
      effect: allow
      mode: confirm
EOF
strazactl policy apply -f confirm-git-push.yaml
strazactl policy activate confirm-git-push
```
{{< /command >}}

{{< see >}}`applied confirm-git-push (Off, …)`, then `published confirm-git-push, it is live now` with the new snapshot.{{< /see >}}

{{< details summary="Recorded output" >}}
```text
applied confirm-git-push (Off, 01a19de0-c0bc-7823-969e-caa9fb285ebc)
published confirm-git-push, it is live now; new snapshot efb029c325312d809e3fb89379a34283b6d492d45aed4d8500e79df3456dd5b9
```
{{< /details >}}

The running Claude Code session uses the new rule within seconds. For a person with no sponsor, a rule with the mode `approve` that names no approver role holds the call the same way, because the request then goes to that person.

## Turn on approvals in this browser


Open `http://127.0.0.1:8420/self-service/`. It says `Signed in as admin`, because it shares the console's sign-in. Press **Sign out**, then **Sign in**, and sign in as alice.

Open **This browser**, press **Enable this browser**, keep or change the name, and press **Enable**. The browser makes a key that never leaves it. The server keeps only the public half. Use a normal window, since a private window deletes the key when it closes.

{{< see >}}`Enabled. This browser decides as alice.`, and one device row for alice marked `this browser` and `software key`.{{< /see >}}

## Approve your agent's call


Ask Claude Code to run `git push`. The hook denies it at once. Claude Code shows a reason that starts `Straza: this call needs the requester's confirmation` and ends with a reference.

On the self-service page, open **Requests**. Within a few seconds a row shows `git push` for alice, with `only the requester confirms`. Press **Approve**, then **Approve request**. The dialog says `The call runs once, when the agent retries it soon after your yes.`

Within 60 seconds of your approval, ask Claude Code to run `git push` again in the same session.

{{< see >}}`Approved. git push runs for alice if the agent retries it soon.` in the browser, and Claude Code runs the command on its next try. A try after that needs a new approval.{{< /see >}}

{{< details summary="The same calls without Claude Code" >}}
Send this in Terminal 4 for the deny and its reference. After you approve in the browser, send it again for the allow.

```sh
printf '%s' '{"hook_event_name":"PreToolUse","session_id":"laptop-1","cwd":"/tmp","tool_name":"Bash","tool_input":{"command":"git push"}}' | straza hook --harness claude-code; echo "exit $?"
```

```text
Straza: this call needs the requester's confirmation, on their enrolled phone, or the self-service page under This browser (http://127.0.0.1:18420/self-service/); retry after it is confirmed (ref 01a19de1-3f04-73a3-8a89-46c524213da7)
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Straza: this call needs the requester's confirmation, on their enrolled phone, or the self-service page under This browser (http://127.0.0.1:18420/self-service/); retry after it is confirmed (ref 01a19de1-3f04-73a3-8a89-46c524213da7)"}}
exit 2
```

One second after the approval, the same command was allowed.

```text
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}
exit 0
```

A third send got a new reference, `01a19de1-76cc-7543-991c-80ddacfc5eff`, because the approval had been used.
{{< /details >}}

{{< fails >}}
No row under Requests, or the row is gone
: A request waits 90 seconds for your approval and then expires. Ask Claude Code to run the command again, which opens a new request.
{{< /fails >}}

## Read the record


Every step above left a record on the [audit chain]({{< relref "concepts/evidence.md" >}}), where each record is linked to the one before it by a hash. In Terminal 2, read the newest five and check the chain.

{{< command terminal="Terminal 2" purpose="administration" >}}
```sh
strazactl audit tail --limit 5
strazactl audit verify
```
{{< /command >}}

{{< see >}}The request, the deny with its reference, alice's approval from her browser, its use, and the allowed `git push` with `confirmed by alice`. Then `audit chain intact:` and a count.{{< /see >}}

{{< details summary="Recorded output" >}}
Each record is trimmed. The real ones also carry the ids of the session, the user, the snapshot and the record, plus the source and the time.

```text
#14 [alice] {"data":{"approvalId":"01a19de1-3f04-73a3-8a89-46c524213da7","lane":"hook","phase":"request","rule":"confirm-git-push","state":"pending","summary":"shell.exec: git push"},"type":"straza.audit.approval"}
#15 [alice] {"data":{"command":"git push","effect":"deny","event":"tool.pre","harness":"claude-code","ruleId":"confirm-git-push","tool":"shell.exec"},"type":"straza.audit.tool"}
#16 [alice] {"data":{"approvalId":"01a19de1-3f04-73a3-8a89-46c524213da7","channel":"browser","decidedDeviceId":"apd_01a19de1-0cd4-7200-9af5-ca482bc3d1a2","lane":"hook","phase":"resolution","state":"approved"},"type":"straza.audit.approval"}
#17 [alice] {"data":{"approvalId":"01a19de1-3f04-73a3-8a89-46c524213da7","consumedAt":"2026-11-02T20:09:09Z","lane":"hook","phase":"consumed","state":"approved"},"type":"straza.audit.approval"}
#18 [alice] {"data":{"command":"git push","effect":"allow","event":"tool.pre","harness":"claude-code","reason":"Straza: confirmed by alice (ref 01a19de1-3f04-73a3-8a89-46c524213da7)","ruleId":"confirm-git-push","tool":"shell.exec"},"type":"straza.audit.tool"}
audit chain intact: 18 records verified
```
{{< /details >}}

When you are done, find the token's id with `strazactl api-token list` and revoke it with `strazactl api-token revoke` and that id.

## Remove Straza from this machine


Remove the hooks first, while the service still runs. If the service stops while the hooks are installed, Claude Code can run no tool, because the hook denies every call with `Straza's background service for STRAZA_HOME … isn't running`.

{{< command terminal="Terminal 4" purpose="alice" >}}
```sh
straza uninstall claude-code
```
{{< /command >}}

{{< see >}}`removed Straza hooks for claude-code` and `removed Straza MCP server for claude-code`, each with the file it changed.{{< /see >}}

Then press Ctrl+C in Terminal 3 to stop the service and in Terminal 1 to stop the server. Delete alice's key and the service's state, and the directory Claude Code worked in.

{{< command terminal="Terminal 4" purpose="alice" >}}
```sh
rm -rf "$HOME/.straza" ~/straza-try
```
{{< /command >}}

Last, delete the directory you started the server in. It holds the server's data, the users and the admin token included.

## What this protects on one laptop {.nostep}


The confirm rule catches a mistake by the agent, and a prompt injection that asks for a command you did not mean. The agent cannot approve its own call, because a session that a coding tool checked in can never decide a request.

It does not stop you, because you are the one who approves. A hook installed for your user only advises, so an agent that can run any command can bypass it. Started by hand, the service runs as you, so any program you run can read alice's key, including the agent. An agent that can read your sign-in could also enroll an approval device of its own. [Known limits]({{< relref "security/known-limits.md" >}}) has a row for each of the last three, and [The security model]({{< relref "evaluate/security-model.md" >}}) shows where Straza enforces a boundary.

{{< details summary="How this page was walked" >}}

Every terminal step ran in a fresh Alpine 3.20 container on Linux amd64, as an ordinary user, with the binaries built at v1.0.0-1700-g89d0cf1d. The browser steps ran in headless Chromium on the host.

For the host's browser to reach the server, `strazad` ran with `--listen :8420`, so it listened on every address of the container. The container's port 8420 was published on the host as 127.0.0.1:18420, and `STRAZA_PUBLIC_URL` was set to `http://127.0.0.1:18420`, so sign-in pages and links named the address the browser could reach. A socat forward made that address answer inside the container too. Every command therefore used `http://127.0.0.1:18420` where this page says `http://127.0.0.1:8420`.

The container has no display, so `straza enroll` ran with `--use-device-code`, and alice signed in on the code page in the browser. `straza install claude-code` and Claude Code itself did not run. Each call of the agent went to `straza hook --harness claude-code` by hand, as the folds above show. The admin API token was minted in the console and reached `strazactl` through the `read` line as written.

The server start with the phone approver off and the removal steps ran on 2026-11-03 in a fresh container of the same kind, with the binaries built at v1.0.0-1709-gacba8249. With `STRAZA_APPROVER_TLS_AUTO_MINT=false`, nothing listened on port 8443, and port 8420 still served the approver routes and the self-service page. `straza install claude-code` and `straza uninstall claude-code` ran for the container's own user. With the service stopped, `git status` sent to `straza hook` came back denied with exit status 2.

In an earlier walk on the same build, a retry 62 seconds after the approval asked again, and an approve rule with no approver role held alice's call as her own confirm request.
{{< /details >}}

{{< next "guides/write-policy/hold-for-a-human.md" >}}
