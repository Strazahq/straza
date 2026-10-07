# Govern one agent in five minutes

The standalone profile runs on one machine: one server binary with an embedded database, an embedded event broker and a built-in sign-in page. No Docker, no Postgres, no external identity provider. At the end an agent session starts with a governance line naming the person and the signed policy in force, and a destructive command is denied inside the agent before it runs. The docs page this follows is https://docs.straza.ai/get-started/first-governed-session/.

You need the three binaries `strazad`, `strazactl` and `straza` on the PATH, two terminals, and the agent harness on the same machine. Take them from a release archive on https://github.com/strazahq/straza/releases, or build them from a clone of the repository with Go 1.26 or newer, Git and Make; the binaries land under `bin`:

```sh
make build
export PATH="$PWD/bin:$PATH"
strazad version
```

The person at the keyboard signs in twice in a browser; hand them the printed link both times. Inside a coding agent, `strazactl` refuses the changes of steps 3, 7 and 9 on the person's login, so hand those commands to the person for their admin terminal. The reads run anywhere, `strazactl policy simulate` included.

## 1. Start the server

```sh
strazad serve --profile standalone
```

The first boot prints the `admin` password once and the `break-glass` password once, seeds the starter policy, and serves on `http://127.0.0.1:8420`, loopback only. Tell the user to copy the admin password from the log now. State lives under `data` in the directory the server started from. A lost password means stop the server, delete `data` and start again.

Keep the server running in its own terminal. Once a harness is governed, a stopped server does not switch governance off: the agent keeps deciding from its last signed policy for up to 15 minutes, and after that every tool call is denied until the server is back. If you are the agent inside the harness being governed, say so to the person before step 6, because your own tool calls are governed from the next session on. The way out when the server is gone for good is `straza uninstall claude-code`, which the person runs in a plain shell; never run it yourself.

## 2. Sign in as the admin

```sh
strazactl login --server http://127.0.0.1:8420
```

The command prints a link with a device code and waits. The user opens it, enters `admin` and the password, and the terminal prints `Logged in as admin.`. `login` is the one command that needs the server address, and it remembers it. A `login timed out` means the code expired after ten minutes; run it again and approve the new code.

## 3. Create a person and a role

```sh
strazactl users create alice --password 'pick-a-passphrase' --email alice@example.com
strazactl roles create dev --kind application
strazactl assign dev --user alice
```

A PolicySet matches application roles, which is why the kind is stated. The password is visible on the command line, so the person picks it and runs the first line in their own terminal.

## 4. See the policy that already governs

```sh
strazactl policy list
strazactl policy show standalone-starter
strazactl policy simulate --roles dev --tool shell.exec --command 'rm -rf /tmp/x'
```

A fresh standalone store already carries `standalone-starter`, one active set that denies recursive force-deletes with a reason. The simulation answers `This call would be denied.` and names the rule and the set. The same command with `git status` is allowed, because the standalone profile allows a local tool no rule mentions. Writing a set of the user's own is `policy.md`.

## 5. Enroll the machine as the person

```sh
straza enroll --server http://127.0.0.1:8420
straza status
```

Enroll prints a link and a code and waits for the user to sign in as `alice`. It binds this machine to her once. `straza status` then names the server and the identity and says no session exists yet, because a session starts when a harness checks in. `straza doctor` at this point reports enrollment, identity and server as ok and three expected warnings with the command that clears each.

## 6. Wire the harness

```sh
straza install claude-code
```

Use `codex` or `gemini` for those harnesses. The command merges the hook entries into the harness settings and registers the `straza mcp` gateway server, then prints the files it wrote. Restart the harness. Codex also needs the user to open `/hooks` inside Codex and trust the Straza hooks once, and the install says so.

Start a new session and ask the agent to run `rm -rf /tmp/x`. The session opens with a governance line naming the user, the role and the policy snapshot, and the command is denied before it runs, with the starter policy's reason shown to the user and to the model. `git status` passes. The same check runs without a harness by piping the harness payload into the hook:

```sh
printf '%s' '{"hook_event_name":"PreToolUse","session_id":"first-session","tool_name":"Bash","tool_input":{"command":"rm -rf /tmp/x"}}' | straza hook --harness claude-code
```

A deny prints the reason and exits 2. An allow exits 0.

## 7. Pull the kill switch

```sh
straza daemon
```

Leave the daemon running in its own terminal as the person. Without it a session learns of a revocation at its next token refresh, at most 300 seconds away; with it the push arrives at once. From the admin terminal:

```sh
strazactl sessions list
strazactl sessions revoke <session id>
strazactl users disable alice
```

Revoking the session makes the daemon print that hooks now deny, and the next tool call in the agent is denied. Disabling the user is the durable cut: every session is revoked and the next session start is refused. `strazactl users enable alice` brings her back.

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
strazactl approvals enroll-token alice
```

Do not create an approver role for alice and name it in the rule: she would be its only holder, nobody may decide their own request, and the hold would expire to a deny. `examples/hold-for-approver.yaml` is the team shape. The enroll token is single use and expires in ten minutes, so mint it with alice and her phone present; she scans the QR in the Straza approver app. Before she scans, check that the `Servers:` line names an address her phone can open, and that push is configured, or the request never reaches her phone while the app is closed. `servers.md` sections 6 and 7 cover both, the browser alternative, and what the agent sees while the call waits.

## Add an MCP server

`servers.md` takes one MCP server end to end: the manifest, install and health, the credential for a shared secret or each person's own token, the application role, the role's approvals, the phone, the preview and the assignment, in the order that keeps a half-built setup harmless.

## Where next

- Enterprise: Postgres, SCIM from the identity manager, external sign-in and the managed install are https://docs.straza.ai/guides/operate/enterprise-shape/, and `identity.md` maps the roles.
- Kubernetes: https://docs.straza.ai/get-started/run-it-on-kubernetes/
- The full demo stack with midPoint and Keycloak: https://docs.straza.ai/get-started/enterprise-demo-stack/
