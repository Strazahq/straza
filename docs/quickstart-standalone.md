# Straza standalone quickstart

This quickstart puts one Claude Code session under central policy on your own
machine. A policy denies `rm -rf` with a reason, and one command ends a live
session. Everything runs locally: one server binary with its embedded database,
event broker and sign-in page. You need no Docker, no Postgres and no external
identity provider.

You need a Linux machine with Claude Code installed, and cosign and sha256sum
if you verify the download. The client starts sessions on Linux, macOS and
Windows, where `straza service` runs, and this quickstart is written for Linux. The documentation at [docs.straza.ai](https://docs.straza.ai/) walks
the same path with recorded output in
[Your first governed session](https://docs.straza.ai/get-started/first-governed-session/).

## 0. Install

Download the archive for your platform from the
[releases page](https://github.com/strazahq/straza/releases), together with
`checksums.txt`, `checksums.txt.sig` and `checksums.txt.pem`. Verify them in
the same directory:

```sh
cosign verify-blob --signature checksums.txt.sig --certificate checksums.txt.pem \
  --certificate-identity-regexp '^https://github\.com/Strazahq/straza/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
sha256sum -c checksums.txt --ignore-missing
```

Then put `strazad`, `strazactl` and `straza` on your PATH.

Or build from source: clone the repository and run `make build` with Go 1.26 or
newer. The three binaries land in `./bin`.

## 1. Start the server

In an empty directory:

```sh
strazad serve --profile standalone
```

The first boot prints the password of the `admin` account once, as a JSON log
line:

```text
{"time":"…","level":"WARN","msg":"bootstrap admin created. Store this password now, it will not be shown again","username":"admin","password":"…"}
```

A second line prints the password of the `break-glass` account, the emergency
account that signs in at the server's own page when your identity provider is
down. Store that one in your vault too. An administrator rotates it with
`strazactl users set-password break-glass --password '<new passphrase>'`. The
identity manager cannot see or change the account.

State lives in `./data`. The main listener binds to `127.0.0.1:8420`, so
administration stays on this machine, and plain HTTP is fine on loopback. The
standalone profile also starts an approver listener for phone approval on port
8443 on every network interface. Read
[Ports and network](https://docs.straza.ai/reference/ports-and-network/) before
you run this setup on a shared network, and
[TLS and exposure](https://docs.straza.ai/guides/operate/tls-and-exposure/)
for anything remote.

## 2. Sign in, mint a token and create a person

strazactl has no sign-in of its own. It runs on an admin API token that a
person mints in the console. Open `http://127.0.0.1:8420/console/`, sign in as
`admin` with the password, open Settings, then API tokens, and press New API
token. Name it `admin-terminal`, pick `expires in 30 days` and the job Full
root, press Mint token and copy the token, which is shown once.

In a second terminal, give the token and the server to strazactl. `read -rs`
reads the pasted token without showing it and keeps it out of the shell
history:

```sh
export STRAZA_SERVER=http://127.0.0.1:8420
read -rs STRAZA_API_TOKEN && export STRAZA_API_TOKEN
```

Then create alice, an application role `dev`, and the assignment:

```sh
strazactl users create alice --password 'pick-a-passphrase' --email alice@example.com
strazactl roles create dev --kind application
strazactl assign dev --user alice
```

The token acts as an administrator until it expires or is revoked. Keep it in
this terminal, never in a shell profile or where an agent runs as you, and
revoke it under Settings, API tokens when you are done.

## 3. Apply a policy

A fresh standalone store already carries the starter policy
`standalone-starter`, which denies recursive force-delete for everyone. The set
below adds a deny for the role dev with its own reason. Both sets sit at
priority 0, so the reason you see is block-rm's, because ties go to the set name
that sorts first.

Save this as `block-rm.yaml`:

```yaml
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: { name: block-rm }
spec:
  match: { roles: [dev] }
  rules:
    - id: no-rm-rf
      tools: [shell.exec]
      command: { denyPatterns: ["rm -rf *"] }
      effect: deny
      reason: "Straza: destructive delete blocked for role dev"
```

Upload it, ask what it would decide, and publish it:

```sh
strazactl policy apply -f block-rm.yaml
strazactl policy simulate --roles dev --tool shell.exec --command 'rm -rf /tmp/x' -f block-rm.yaml
strazactl policy activate block-rm
```

`apply` stores the new set switched off. `simulate` with `-f` answers as if the
file were live, and `activate` compiles, signs and distributes the snapshot.

## 4. Start the background service, enroll the machine and wire Claude Code

`straza service`, the Straza background service, holds alice's key and every
Straza session of this machine, and the hooks reach it over a local socket. The
straza package for Linux runs it under its own OS account. Here it runs as you
instead, in a third terminal, and keeps running:

```sh
export STRAZA_HOME="$HOME/.straza"
straza service
```

With `STRAZA_HOME` set, the service runs as your OS user, with its socket and
state under that directory. This works only on a machine with no system service
and no managed install, because on a machine with either one, every `straza`
command ignores `STRAZA_HOME` and reaches the system service. Export the same `STRAZA_HOME` in every terminal
that runs `straza` or starts Claude Code, or the hooks deny because they cannot
reach the service. Then, back in the second terminal, enroll as alice, the
person who runs the agent:

```sh
export STRAZA_HOME="$HOME/.straza"
straza enroll --server http://127.0.0.1:8420
straza install claude-code
```

`enroll` prints an address. Open it and sign in as alice. `install` merges the
hook wiring into `~/.claude/settings.json`, registers the Straza MCP server, and
prints the paths it changed. Restart Claude Code from that terminal so it loads
those settings, then check the installation:

```sh
straza doctor
```

`doctor` checks the service, the enrollment, the server, the session and the
wiring, and each finding it prints carries the command that fixes it.

User-mode wiring is advisory. The person who runs the agent can edit or remove
it, so the hook lane is not a security boundary. An MCP server the person adds
to the harness themselves is their own responsibility, and Straza does not see
it. The [trust model](https://docs.straza.ai/concepts/trust-model-and-non-goals/)
says what each lane is worth.

## 5. Watch it govern

Start a new Claude Code session and look for the Straza governance banner. Ask the agent to run `git status`, which runs. Then ask it to run
`rm -rf /tmp/x`. Claude Code reports the call as denied with the reason
`Straza: destructive delete blocked for role dev`.

Read the audit record and verify its hash chain:

```sh
strazactl audit tail
strazactl audit verify
```

## 6. Kill switch

`straza service` holds a push stream for each live session, so a revocation
reaches the running session at once. List the sessions and revoke alice's:

```sh
strazactl sessions list
strazactl sessions revoke <session-id>
```

The service marks that session revoked, and the next tool call in that session
is denied. A gateway call of the revoked
session is refused with
`Straza: this session has been revoked. Start a new session: its check-in uses this device's existing enrollment, so re-enrolling is not needed`.
A new session checks in again without a new enrollment. Disabling the user,
`strazactl users disable alice`, is the durable cut, and a check-in is then
refused with
`Straza: this device or user has been revoked. Contact your administrator`.

## Where to go next

- MCP servers by role:
  [Serve MCP servers](https://docs.straza.ai/guides/serve-mcp-apps/). A file in
  `./data/apps/` proposes a draft, which a person publishes in the console
  under Drafts.
- Each person's own upstream account:
  [Each caller's own credential](https://docs.straza.ai/guides/serve-mcp-apps/caller-credentials/)
  covers `credential.kind: oauth` and pasted tokens. A clone carries
  `test/oauthstub`, a local stand-in for an OAuth provider and an upstream MCP
  server, to try the sign-in with no OAuth app.
- Managed installs and attestation: the
  [Claude Code guide](https://docs.straza.ai/guides/govern-an-agent/claude-code/)
  covers the managed install and what its attestation proves. It needs the
  Straza package for the machine's system, the deb, rpm, MSI or pkg, and then
  `sudo straza install --managed --server <server-url> claude-code`.
- Enterprise, with Postgres, SCIM from your identity manager and an external
  identity provider:
  [The demo stack](https://docs.straza.ai/get-started/enterprise-demo-stack/),
  [Connect identity](https://docs.straza.ai/guides/connect-identity/) and
  [Kubernetes with Helm](https://docs.straza.ai/guides/operate/kubernetes-with-helm/).
