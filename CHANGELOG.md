# Changelog

All notable changes to Straza are listed here, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

The Unreleased section holds what the main branch carries since the last release. Each
release also has notes on the [releases page](https://github.com/strazahq/straza/releases),
and the [changelog page of the docs](https://docs.straza.ai/project/changelog/) says how
Straza numbers its binaries, its API and its wire formats.

## [Unreleased]

## [2.0.1] - 2026-11-02

Straza 2.0.1 is a security and bug-fix release for 2.0.0. It changes no wire format and no
database schema, so the upgrade is the binary or the package alone.
The Linux, macOS and Windows packages restart `straza service`. If you run `straza service` from the
archive, restart it after the upgrade, because the classify and exec fixes run in it.

### Security

- `straza exec` applies interpreter rules. It sent commands without the interpreter tag that the
  harness hook adds, so a rule such as `interpreters: {deny: ["python*"]}` held on a hook and not on
  exec, which is also the path of a hookless sandbox's wrappers. The background service now tags an
  untagged shell command the way strazad does, so after the upgrade an exec call that an interpreter
  rule denies or sends to the classifier is denied or classified.
- The standalone SQLite database is created readable by its owner only. It was created 0644, and the
  standalone guide made its data directory 0755, so any local user could read the audit records,
  password hashes and server settings in it. strazad also narrows an existing database and its WAL
  and SHM files to 0600 when it starts, and logs each file it changes.

### Fixed

- A call that a classify rule allows runs again. In 2.0.0 the background service kept the classify
  mark on a classifier allow, so the hook denied every such call as unresolved while its audit record
  said allow.
- An API token request with `expires_in` sent as text, such as `"1h"`, answers that it must be a
  whole number of seconds, where it answered "name is required". A body that is not valid JSON on the
  API token, pack and role create routes says so, and "name is required" now means only that.

## [2.0.0] - 2026-11-02

Straza 2.0.0 moves every credential on a person's machine into a local service that AI agents
running as that person cannot read. It adds roles that cover some objects of a server, git through
Straza and rules by tool name, and it closes a set of security gaps. It is a major release. strazad
2.0.0 refuses the 1.1.x `straza` and `strazactl` clients, so every machine updates and every person
enrolls again.
[Upgrade from 1.1.2 to 2.0.0](https://docs.straza.ai/guides/operate/backup-and-upgrade/#upgrade-from-112-to-200)
walks the upgrade step by step.

### Upgrade notes

Read these before you upgrade, and do them in this order.

1. Register the browser sign-in at your identity provider. The console and the self-service page
   return to the address people open the console at, followed by `/console/signin`. On Entra ID that
   is a single-page application address, on Keycloak a valid redirect and web origin, and on Okta a
   sign-in redirect and trusted origin. `straza enroll` receives its answer at
   `http://127.0.0.1/callback` on a free port, or on the port that `oidc.loopbackPort` fixes for a
   provider that matches ports exactly. One public client with PKCE takes both addresses. The built-in
   issuer needs nothing. See [Keycloak login](https://docs.straza.ai/guides/connect-identity/keycloak-login/),
   [Entra ID login](https://docs.straza.ai/guides/connect-identity/entra-login/) and
   [Okta login](https://docs.straza.ai/guides/connect-identity/okta-login/).
2. Back up the store. Migrations 38 to 41 run when strazad starts. To go back to 1.1.2, first run
   `strazad migrate --to 37` with the 2.0.0 binary. That drops object pins and covers, machine key
   bindings and the recorded client builds.
3. Upgrade strazad, or the Helm chart, before the machines. A Linux managed install refuses hook
   wiring from an older server.
4. Install the client from its package on every governed machine: the deb or rpm on Linux, the MSI on
   Windows, the pkg on macOS. `straza install --managed` no longer copies itself and refuses to run
   until the package is installed. On Linux, run it by the package's full path,
   `sudo /usr/bin/straza install --managed --server <server-url> <harness>`, then delete the
   `/usr/local/bin/straza` that 1.1.x left, because it comes first on the PATH. strazad refuses the
   1.1.x client when it enrolls, starts a session or refreshes one, so its sessions end within five
   minutes. See [Install](https://docs.straza.ai/get-started/install/).
5. Every person runs `straza enroll` again in a desktop session, which signs in through the browser.
   `--use-device-code` is for a machine with no display. On the enterprise profile a code sign-in no
   longer enrolls a machine unless `governance.minSignIn` is set to `code`. See
   [Enroll a machine](https://docs.straza.ai/guides/govern-an-agent/enroll-a-machine/).
6. Enroll each AI agent again through `straza service`, which now holds the agent's credential. An
   agent that signs in at Straza's own issuer, with no identity provider for agents, gets a new key:
   run `straza keygen --user <agent>` as the agent's OS user on its machine, and register the key it prints with
   `strazactl users nhi-key set`, which replaces the old one. An agent that signs in with a client
   secret at Entra ID, Okta or Keycloak gets that secret from a file only the service can read, and
   you rotate every secret that sat in an agent's environment. See
   [Headless agents](https://docs.straza.ai/guides/govern-an-agent/headless-agents/).
7. strazactl runs on an admin API token only. A person mints one in the console under Settings, API
   tokens, with a scope and an expiry, and sets it in `STRAZA_API_TOKEN`. These strazactl commands
   are gone: `login`, `logout`, `approvals approve`, `approvals deny`, `approvals enroll-token`,
   `drafts publish` and `drafts contact`. A person decides, publishes and enrolls in the console, on
   the self-service page or on the phone. Delete strazactl's old credentials file.
8. Approver phones that are already enrolled keep working. A person enrolls a new phone or browser on
   the self-service page, signed in as themselves. The admin enroll route refuses every caller, and an
   enroll code that route minted before the upgrade no longer works. See
   [Approve on a phone](https://docs.straza.ai/guides/approve/phone/).
9. Remove any unit or script that runs `straza daemon` or `straza drain`. `straza service` does their
   work. `straza connect --allow-agents` is gone too, and its switch is on the Credentials tab of the
   self-service page.
10. A console served over plain HTTP from a network address cannot sign in. Serve it over HTTPS or
    open it at 127.0.0.1.

### Added

- [`straza service`](https://docs.straza.ai/reference/cli/straza/straza_service/), a local
  credential holder for Linux, Windows and macOS. It holds each person's machine key, each agent's
  key or client secret and every session on the machine, all under its own OS account, so an AI
  agent that runs as the person finds no credential to copy. Each harness window gets its own
  session, and the service keeps its sessions across a restart, so open windows survive a package
  upgrade. See [Credentials and sessions](https://docs.straza.ai/security/credentials-and-sessions/).
- Packages: deb and rpm for amd64 and arm64, an MSI for Windows amd64, and one macOS pkg for Intel
  and Apple silicon, signed with Developer ID and notarized by Apple. The signed checksums file lists
  every package. Guides deploy the MSI with
  [Intune](https://docs.straza.ai/guides/govern-an-agent/deploy-with-intune/) and the pkg with
  [Jamf Pro](https://docs.straza.ai/guides/govern-an-agent/deploy-with-jamf/) or Intune. No tenant
  has run those two guides yet.
- A machine's enrollment is bound to a key that never leaves the machine. Every session token needs a
  fresh DPoP proof (RFC 9449), so a copied token is refused. Straza detects a copied key and revokes
  its device.
- Browser sign-in with PKCE for `straza enroll`, the console and the self-service page, with new
  sign-in guides for [Entra ID](https://docs.straza.ai/guides/connect-identity/entra-login/) and
  [Okta](https://docs.straza.ai/guides/connect-identity/okta-login/). Their steps follow each
  provider's documentation, and no Entra ID or Okta tenant has run them yet.
- Roles that cover some objects of a server. A role can cover some objects of an MCP server, such as
  some repositories, instead of all of them. Before any policy runs, the gateway refuses a call on an
  object that none of the caller's roles covers, and says which objects those roles do cover. The
  console lists a server's objects on their own tab, `strazactl apps objects` lists them, and SCIM
  shows each role's `covers` to an IGA tool such as midPoint. See
  [Limit a role to some objects](https://docs.straza.ai/guides/serve-mcp-apps/limit-a-role-to-some-objects/)
  and, for server authors, [Object marks for server authors](https://docs.straza.ai/reference/object-marks/).
- Git through Straza. An agent in Claude Code runs plain `git clone`, `fetch` and `push` with no git
  credential on its machine. Straza decides each fetch and push with the same roles, covers, policy
  and approvals as an MCP call, and a push's approval names the exact ref updates it carries. A
  server turns this on with a `straza.git` block in its manifest, and nothing is configured on the
  machines. See [Git through Straza](https://docs.straza.ai/guides/serve-mcp-apps/git-through-straza/).
- Rules by tool name. An admin allows, holds or denies a harness tool that Straza does not classify,
  by the tool's own name and with no Straza release. The deny names the tool and the rule that would
  allow it. straza-agentkit 0.3.0 sends each framework tool's own name. See the
  [PolicySet grammar](https://docs.straza.ai/reference/policyset-grammar/).
- Drafts name the objects each role covers. A role that would reach more objects is a line the
  publisher confirms, and a publish groups its confirmations by kind. See
  [Propose and publish](https://docs.straza.ai/guides/changes/propose-and-publish/).
- In the console, a denied or held call on the Overview opens that exact call in Audit, and the
  sessions panel marks a machine whose client build differs from the server's.
- `strazactl connect --user` works on an admin API token with `apps:read` and `apps:write`, so a
  script can set the connections of its users.
- Settings: `governance.minCustody`, `governance.minSignIn`, `governance.signInMaxAge`,
  `oidc.loopbackPort`, `server.mcpMaxBodyBytes`, `apps.objects.enabled` and
  `admin.serverAdminsCoverEvery`, listed in the
  [configuration reference](https://docs.straza.ai/reference/configuration/). Metrics:
  `straza_outbox_set_aside_total` and `straza_approvals_unroutable_total`, described in
  [Metrics and alerts](https://docs.straza.ai/guides/operate/metrics-and-alerts/).
- The enterprise demo stack can run adaptive-pg-mcp, a Postgres MCP server, behind Straza under the
  compose profile `pg`, from its published 0.1.1 image.

### Changed

- A Claude Code window left unused keeps working on its next call for up to seven days. A revoked
  window stays refused for those seven days.
- On the enterprise profile, strazad refuses a person's ID token at a check-in under a coding harness,
  and an agent's check-in that carries no key proof.
- Replacing or removing an agent's key ends its sessions and retires the ID tokens issued for the old
  key.
- A session started on a machine can no longer let agents use a person's connection or change another
  user's connection. A browser sign-in still can.
- A call over the MCP body limit gets a JSON-RPC error with its own id and a sentence that names the
  limit and the setting, where it got a parse error before. See
  [Hardening](https://docs.straza.ai/security/hardening/).
- Audit records are bounded. Names a caller sent are cut at 256 bytes, details at 512 bytes and
  approval text at 4 KiB, each followed by its full length. A record over 256 KiB keeps its identity
  and outcome, plus a hash of the whole record.
- A check-in whose harness name, harness version or client version is longer than 256 bytes is
  refused with a sentence.
- Revoking a device or a session is one transaction. A revoke the database refuses changes nothing and
  answers 500, and running it again ends the target.
- Only Straza's own client ids may start a device sign-in. At most 4096 can wait at once, and a
  per-address limit applies.
- The agent skill plugin moves to 0.6.35. It tells an agent never to ask for or use an admin API
  token, and it no longer has an agent read the first boot's admin passwords.
- The Helm chart's home is https://straza.ai.

### Removed

- `straza daemon` and `straza drain`, whose work `straza service` does.
- `straza connect --allow-agents`.
- strazactl's `login`, `logout`, `approvals approve`, `approvals deny`, `approvals enroll-token`,
  `drafts publish` and `drafts contact`, and the device credential that strazactl and the straza
  client kept.

### Fixed

- midPoint's live sync no longer stops on "Uid value must not be blank". The change feed skips
  identity events that name no object.
- When a window's server stays down past the grace period, the deny gives the outage wording, where it
  told the person to restart the session.
- An approval request that nobody could decide is recorded as the deny it is, where it was recorded as
  an allow.
- docs.straza.ai serves a robots.txt, a dated sitemap, page metadata and a 404 page.

### Security

- An AI agent running as an administrator could copy strazactl's credentials file and use the whole
  admin API from any machine. It could also approve requests in a person's name, or enroll its own key
  as another person's approver phone. strazactl now runs on admin API tokens, and no admin route or
  console screen enrolls an approver device for someone else. The straza client keeps no credential
  an agent can read. On the standalone profile, a credential that may set a person's password can
  still sign in as that person, as [Known limits](https://docs.straza.ai/security/known-limits/) says.
- An anonymous client could exhaust strazad's memory through the device sign-in start, on both
  profiles.
- With two replicas, a removed role kept working for up to 12 hours on the replica that had not
  refreshed the session.
- An agent kept calling a server on its sponsor's connection after the sponsor was locked.
- approval_request let an agent open tickets for tools it cannot see, read the deny reason of a hidden
  tool, skip a server's rate limit and reopen a call a person had just denied.
- An agent's draft could widen a role the agent would be assigned later, and the agent gained the
  widening when the assignment started.
- When an agent's key was replaced or removed, its sessions kept running and its ID tokens stayed
  valid for 10 minutes.
- One oversized audit record could stop every later record from reaching the chain and the sinks.
- A failed revoke could answer "revoked" while other replicas still admitted the session for up to
  300 seconds.
- During an event bus outage, a replica could keep a removed role. A role named `a,b` shared the tool
  catalog of the roles `a` and `b`.
- A session started on a machine could let agents use a person's connection or change other users'
  connections.
- Dependencies: Go 1.26.10 for GO-2026-6809, and go-jose 4.1.6 for GHSA-mgcc-cww5-fp2q,
  GHSA-4w3g-jfj2-cw2q and GHSA-vvm8-gf78-28g4. In the console, zod moves to 4.6.6 and the build's
  vite to 7.3.7.

## [1.1.2] - 2026-10-14

### Fixed

- `straza mcp` checks in again when strazad ends its session. Before, it kept sending the
  refused token, and every tool call failed as Unauthorized for up to about four and a half
  minutes.
- A new session carries its user type, agency mode and swarm from the first hook call. A
  PolicySet scoped by `match.identity` now decides from the start of a session, where it
  applied only after the first refresh.
- With two or more replicas, a burst of check-ins on one replica no longer makes check-ins
  on another answer 503.
- Six built-in tools of current Claude Code releases are mapped into the tool taxonomy:
  `ListAgents`, `ReadNotifications`, `FetchInboxMessage`, `SendFeedback`, `ArtifactComments`
  and `ArtifactData`. They fell to `other`, which the enterprise profile denies. A governed
  machine needs the new `straza` client to get the mapping.
- A draft's contact to a server that never answers is always reported as a timeout. It
  could read "the connection failed before it answered".

### Changed

- `straza daemon` prints strazad's reason and the next step when its session refresh is
  refused.
- The README is rewritten around a Kubernetes example and the demo stack.

## [1.1.1] - 2026-10-10

### Security

- Built with Go 1.26.9 and golang.org/x/net 0.60.0, which fix twelve Go advisories that
  Straza's code reaches, among them an HTTP/2 crash and HTTP/2 memory exhaustion in strazad.
  Upgrade every strazad, and the clients when you can.

### Changed

- The container image carries the OCI source, description and licenses labels.

## [1.1.0] - 2026-10-08

The first public release.

### Added

- `strazad`, the server: policy decisions, the MCP gateway, the admin API and web console,
  SCIM 2.0 intake and the hash-chained audit.
- `straza`, the client that runs beside the agent: hooks for Claude Code, Codex CLI and
  Gemini CLI, the MCP proxy and `straza exec`.
- `strazactl`, the admin command line.
- Signed release archives for Linux, macOS and Windows, the container image
  `ghcr.io/strazahq/straza` and the Helm chart under `deploy/helm/straza`.

[Unreleased]: https://github.com/strazahq/straza/compare/v2.0.1...HEAD
[2.0.1]: https://github.com/strazahq/straza/compare/v2.0.0...v2.0.1
[2.0.0]: https://github.com/strazahq/straza/compare/v1.1.2...v2.0.0
[1.1.2]: https://github.com/strazahq/straza/compare/v1.1.1...v1.1.2
[1.1.1]: https://github.com/strazahq/straza/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/strazahq/straza/releases/tag/v1.1.0
