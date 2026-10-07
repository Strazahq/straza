# Straza standalone quickstart

This quickstart puts one Claude Code session under central policy on your own
machine. A policy denies `rm -rf` with a reason, and one command ends a live
session. Everything runs locally: one server binary with its embedded database,
event broker and sign-in page. You need no Docker, no Postgres and no external
identity provider.

You need Claude Code installed, and cosign and sha256sum if you verify the
download. The documentation at [docs.straza.ai](https://docs.straza.ai/) walks
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
down. Store that one in your vault too. Any admin session rotates it with
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

## 2. Sign in and create a person

In a second terminal, sign in as `admin`. `login` is the one command that names
the server, and every later command targets the server you signed in to:

```sh
strazactl login --server http://127.0.0.1:8420
```

Open the address it prints, enter `admin` and the password, and press Sign in.
The terminal prints `Logged in as admin.` Then create alice, an application
role `dev`, and the assignment:

```sh
strazactl users create alice --password 'pick-a-passphrase' --email alice@example.com
strazactl roles create dev --kind application
strazactl assign dev --user alice
```

`strazactl logout` ends the admin session when you are done.

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

## 4. Enroll the machine and wire Claude Code

As alice, the person who runs the agent:

```sh
straza enroll --server http://127.0.0.1:8420
straza install claude-code
```

`enroll` prints an address. Open it and sign in as alice. `install` merges the
hook wiring into `~/.claude/settings.json`, registers the Straza MCP server, and
prints the paths it changed. Restart Claude Code so it loads those settings,
then check the installation:

```sh
straza doctor
```

`doctor` checks the enrollment, the server, the snapshot and the wiring, and
each finding it prints carries the command that fixes it.

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

Start the client daemon as alice in a third terminal, so revocation reaches the
running session at once:

```sh
straza daemon
```

Then list the sessions and revoke alice's:

```sh
strazactl sessions list
strazactl sessions revoke <session-id>
```

The daemon prints `daemon: revocation received. Session state dropped, hooks now deny`,
and the next tool call in that session is denied. A gateway call of the revoked
session is refused with
`Straza: this session has been revoked. Start a new session: its check-in uses this device's existing enrollment, so re-enrolling is not needed`.
A new session checks in again without a new enrollment. Disabling the user,
`strazactl users disable alice`, is the durable cut, and a check-in is then
refused with
`Straza: this device or user has been revoked. Contact your administrator`.

## Where to go next

- MCP servers by role:
  [Serve MCP servers](https://docs.straza.ai/guides/serve-mcp-apps/). A file in
  `./data/apps/` proposes a draft, which you publish with
  `strazactl drafts publish <id>` or on the console.
- Each person's own upstream account:
  [Each caller's own credential](https://docs.straza.ai/guides/serve-mcp-apps/caller-credentials/)
  covers `credential.kind: oauth` and pasted tokens. A clone carries
  `test/oauthstub`, a local stand-in for an OAuth provider and an upstream MCP
  server, to try the sign-in with no OAuth app.
- Managed installs and attestation: the
  [Claude Code guide](https://docs.straza.ai/guides/govern-an-agent/claude-code/)
  covers `sudo straza install --managed claude-code` and what its attestation
  proves.
- Enterprise, with Postgres, SCIM from your identity manager and an external
  identity provider:
  [The demo stack](https://docs.straza.ai/get-started/enterprise-demo-stack/),
  [Connect identity](https://docs.straza.ai/guides/connect-identity/) and
  [Kubernetes with Helm](https://docs.straza.ai/guides/operate/kubernetes-with-helm/).
