# Govern one agent in five minutes

The standalone profile runs on one machine: one server binary with an embedded database, an embedded event broker and a built-in sign-in page. No Docker, no Postgres, no external identity provider. At the end an agent session starts with a governance line naming the person and the signed policy in force, and a destructive command is denied inside the agent before it runs. The docs page this follows is https://docs.straza.ai/get-started/first-governed-session/.

You need a Linux machine with the three binaries `strazad`, `strazactl` and `straza` on the PATH, four terminals, and the agent harness on the same machine. The client starts sessions on Linux, macOS and Windows, and this quickstart uses Linux. Take them from a release archive on https://github.com/strazahq/straza/releases, or build them from a clone of the repository with Go 1.26 or newer, Git and Make; the binaries land under `bin`:

```sh
make build
export PATH="$PWD/bin:$PATH"
strazad version
```

The person at the keyboard signs in twice in a browser: in the console, where they mint an admin API token for `strazactl`, and at the enrollment of step 5, where `straza enroll` opens their browser on the sign-in page. Every `strazactl` command that reaches the server runs on that token in the person's admin terminal, so hand those commands to the person, reads included, and work from the output they share. You run only `strazactl policy validate`, which checks a file offline.

## 1. The person starts the server

This step is the person's own. They run the server in the first terminal, and you wait until they tell you it runs:

```sh
strazad serve --profile standalone
```

Never run it yourself, never read its log and never ask for what it printed. The first boot prints the `admin` password and the `break-glass` password once, in that log. A log you read puts both passwords in your transcript, and either one signs in as an administrator. Tell the person to copy both passwords from the log into their password vault now, because a restart does not print them again. The first boot also seeds the starter policy and serves on `http://127.0.0.1:8420`, loopback only. State lives under `data` in the directory the server started from. A lost password means stop the server, delete `data` and start again.

The person keeps the server running in that terminal. Once a harness is governed, a stopped server does not switch governance off: the agent keeps deciding from its last signed policy for up to 15 minutes, and after that every tool call is denied until the server is back. If you are the agent inside the harness being governed, say so to the person before step 6, because your own tool calls are governed from the next session on. The way out when the server is gone for good is `straza uninstall claude-code`, which the person runs in a plain shell; never run it yourself.

## 2. Sign in to the console and mint an admin token

The person opens `http://127.0.0.1:8420/console/` in a browser, presses Sign in, and signs in as `admin` with the password from the log. Under Settings, API tokens, they press New API token, name it `admin-terminal`, pick `expires in 30 days` and the Full root job, press Mint token, and copy the token, which is shown only once. Then, in the second terminal, the one they use as the admin:

```sh
export STRAZA_SERVER=http://127.0.0.1:8420
read -rs STRAZA_API_TOKEN && export STRAZA_API_TOKEN
strazactl users list
```

`read -rs` reads the pasted token without showing it and keeps it out of the shell history. The list shows the two users the first boot created, `admin` and `break-glass`. The token is a bearer credential: whoever holds it acts as an administrator until it expires or is revoked. So it stays in that terminal only, never in a shell profile, and the harness never starts from that terminal. Never ask the person for the token. They revoke it under Settings, API tokens when they finish.

## 3. Create a person and a role

```sh
strazactl users create alice --password 'pick-a-passphrase' --email alice@example.com
strazactl roles create dev --kind application
strazactl assign dev --user alice
```

A PolicySet matches application roles, which is why the kind is stated. The password is visible on the command line, so the person picks it.

## 4. See the policy that already governs

```sh
strazactl policy list
strazactl policy show standalone-starter
strazactl policy simulate --roles dev --tool shell.exec --command 'rm -rf /tmp/x'
```

A fresh standalone store already carries `standalone-starter`, one active set that denies recursive force-deletes with a reason. The simulation answers `This call would be denied.` and names the rule and the set. The same command with `git status` is allowed, because the standalone profile allows a local tool no rule mentions. Writing a set of the user's own is `policy.md`.

## 5. Start the background service and enroll the machine as the person

In the third terminal, as the person:

```sh
export STRAZA_HOME="$HOME/.straza"
straza service
```

`straza service` holds alice's key and every Straza session of this machine, and the hooks reach it over a local socket. Keep it running in its own terminal. The straza package for Linux runs it under its own OS account, and here it runs as the person instead, with its socket and state under `STRAZA_HOME`. This works only on a machine with no system service and no managed install, because on a machine with either one, every `straza` command ignores `STRAZA_HOME` and reaches the system service. Export the same `STRAZA_HOME` in every terminal that runs `straza` or starts the harness, or the hooks deny because they cannot reach the service. In the fourth terminal, which holds no admin token:

```sh
export STRAZA_HOME="$HOME/.straza"
straza enroll --server http://127.0.0.1:8420
straza status
```

Enroll opens the user's browser on the sign-in page and waits for them to sign in as `alice`, with `--use-device-code` as the way on a machine with no display. It binds this machine to her once, with a key the service keeps. `straza status` then names the service, the server and the identity and says no session exists yet, because a session starts when a harness checks in. `straza doctor` at this point reports the service, the enrollment and the server as ok, and two expected warnings, no session and no harness wiring, each with the command that clears it.

## 6. Wire the harness

```sh
straza install claude-code
```

Use `codex` or `gemini` for those harnesses. The command merges the hook entries into the harness settings and registers the `straza mcp` gateway server, then prints the files it wrote. Restart the harness from a terminal that exports the same `STRAZA_HOME`, so its hooks reach the service. Codex also needs the user to open `/hooks` inside Codex and trust the Straza hooks once, and the install says so.

Start a new session and ask the agent to run `rm -rf /tmp/x`. The session opens with a governance line naming the user, the role and the policy snapshot, and the command is denied before it runs, with the starter policy's reason shown to the user and to the model. `git status` passes. The same check runs without a harness by piping the harness payload into the hook:

```sh
printf '%s' '{"hook_event_name":"PreToolUse","session_id":"first-session","tool_name":"Bash","tool_input":{"command":"rm -rf /tmp/x"}}' | straza hook --harness claude-code
```

A deny prints the reason and exits 2. An allow exits 0.

## 7. Pull the kill switch

`straza service` holds a push stream for each live session, so a revocation reaches a running session at once while the service runs. A session whose stream is down learns of it at the first call that renews its token, once 120 seconds or less of the 300 remain, so at most about 180 seconds after the token was minted for a session that keeps working. From the admin terminal:

```sh
strazactl sessions list
strazactl sessions revoke <session id>
strazactl users disable alice
```

Revoking the session marks it revoked in the service, and the next tool call in the agent is denied. A new session start checks in again and gets a new session, because only the session was revoked. Disabling the user is the durable cut: every session is revoked and the next session start is refused. `strazactl users enable alice` brings her back.

## 8. Read the evidence

```sh
strazactl audit tail --limit 6
strazactl audit verify
```

Every decision and identity change above is one record on the audit chain, and `verify` reports the chain intact with the count. `evidence.md` is the reference for queries and controls.

## 9. Ask me on my phone before a command runs

One person governing their own agent confirms their own held calls. A rule whose approve block names no `roles` and no `deciders` sends the request to the person behind the call, and alice has no sponsor, so she confirms it herself on her own enrolled phone or browser:

```yaml
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: dev-ask-before-push
  description: git push waits for the person's own confirmation
spec:
  priority: 100
  match:
    roles: [dev]
  rules:
    - id: git-push-confirm
      events: [tool.pre]
      tools: [shell.exec]
      command:
        allowPatterns: ["git push", "git push *"]
      effect: allow
      mode: approve
      approve:
        timeoutSeconds: 300
      reason: "Straza: git push waits for your confirmation on your phone"
```

```sh
strazactl policy validate -f dev-ask-before-push.yaml
strazactl policy apply -f dev-ask-before-push.yaml
strazactl policy activate dev-ask-before-push
strazactl assign straza-enroll-mobile --user alice
```

Do not create an approver role for alice and name it in the rule: she would be its only holder, nobody may decide their own request, and the hold would expire to a deny. `examples/hold-for-approver.yaml` is the team shape. She enrolls her own phone, never you, because a phone enrolled in her name decides as her. No admin route or console screen enrolls a phone for someone else, so she does it on the self-service page while she is signed in as herself. With the role she opens `/self-service/` on the server, signs in and chooses Add a phone on the This browser tab, then scans the QR in the Straza approver app. The enroll token is single use and expires in ten minutes, so she mints it with her phone in hand. Before she scans, have her check that the servers the sheet lists name an address her phone can open, and that push is configured, or the request never reaches her phone while the app is closed. `servers.md` sections 6 and 7 cover both, the browser alternative, and what the agent sees while the call waits.

## Add an MCP server

`servers.md` takes one MCP server end to end: the manifest, install and health, the credential for a shared secret or each person's own token, the application role, the role's approvals, the phone, the preview and the assignment, in the order that keeps a half-built setup harmless.

## Where next

- Enterprise: Postgres, SCIM from the identity manager, external sign-in and the managed install are https://docs.straza.ai/guides/operate/enterprise-shape/, and `identity.md` maps the roles.
- Kubernetes: https://docs.straza.ai/get-started/run-it-on-kubernetes/
- The full demo stack with midPoint and Keycloak: https://docs.straza.ai/get-started/enterprise-demo-stack/
