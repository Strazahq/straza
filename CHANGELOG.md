# Changelog

All notable changes to Straza are listed here, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

The Unreleased section holds what the main branch carries since the last release. Each
release also has notes on the [releases page](https://github.com/strazahq/straza/releases),
and the [changelog page of the docs](https://docs.straza.ai/project/changelog/) says how
Straza numbers its binaries, its API and its wire formats.

## [Unreleased]

## [2.3.0] - 2026-11-24

Straza 2.3.0 syncs with Microsoft Entra ID directly over Microsoft Graph, where SCIM 2.0
provisioning was the only way to sync from Entra before. It creates one group in Entra for each
role, and a person or an agent you add to that group gets the role, without midPoint or another IGA.
The release also encrypts the session and snapshot signing keys in the database with `secret.key`,
limits how fast a session and a user can call a remote MCP server, and rebuilds the console's pages
for users, sessions, drafts and approvals. strazad migrates its database, and you have to stop every
replica before the first 2.3.0 replica starts.
[Upgrade from 2.2.0 to 2.3.0](https://docs.straza.ai/guides/operate/backup-and-upgrade/#upgrade-from-220-to-230)
walks the upgrade step by step.

### Upgrade notes

1. Back up the store and `secret.key`, because the first start runs migrations 43 to 47 and encrypts
   the signing keys with that file. strazad no longer starts without it. Keep its copy apart from
   the database backups, because the two together open the keys.
2. On Kubernetes, set `secrets.kekExistingSecret` if you have not yet, because the chart refuses to
   render the enterprise profile without it.
3. Raise `straza.limits.rps` in the manifest of each remote MCP server that one session calls more
   than 20 times a second. That default limit is new, and 0 no longer means no limit for a remote
   server.
4. Raise it too for a server that one user's sessions together call more than five times its rate.
   With the default rate that is 100 calls a second. This ceiling for one user is new.
5. Check MCP clients of your own that call the gateway. After `initialize`, a request whose
   `MCP-Protocol-Version` header names a version other than `2025-06-18`, `2025-03-26` or
   `2024-11-05` now gets 400.
6. Remove `strazactl packs` and calls to `/v1/admin/packs` from your scripts, because knowledge
   packs are gone.
7. Stop every replica before the first one of this version starts, because an older replica crashes
   on an encrypted signing key. On Kubernetes, scale the Deployment to zero, run `helm upgrade` and,
   with `autoscaling.enabled`, scale it up again yourself.
   [Kubernetes with Helm](https://docs.straza.ai/guides/operate/kubernetes-with-helm/#upgrade-and-roll-back)
   has the commands.
8. Upgrade strazactl after strazad, because a newer strazactl may send a field that an older strazad
   refuses.
9. To go back to 2.2.0, stop every replica and run `strazad migrate --to 46`, then 45, 44, 43 and
   42, with this version's binary and `secret.key` in place. That removes every role assigned
   through an Entra group.
   [Roll back](https://docs.straza.ai/guides/operate/backup-and-upgrade/#roll-back) lists what
   each step gives up.
10. On Kubernetes, name the revision in `helm rollback`. Without one, Helm can return to a revision
    that is still this version.

### Added

- Straza now syncs with Microsoft Entra ID directly over Microsoft Graph. Before, SCIM 2.0
  provisioning was the only way to sync from Entra, and it keeps working. You register one more app
  in Entra for the sync and set `identity.entra`.
  [Role groups in Entra](https://docs.straza.ai/guides/connect-identity/entra-role-groups/) walks
  the setup.
- The sync assigns roles through groups, without midPoint or another IGA. strazad creates one
  security group in Entra for each role. Add someone to that group, and they get the role in
  Straza. Remove them, and they lose it.
- A removal guard holds the removals when one read of a group would take the role from at least 5 of
  its holders and from more than half of them. The role's page in the console then offers Apply the
  removals. `identity.entra.removalGuard` sets both numbers.
- A group's members get the role only if `identity.entra.read` lists their kind: `people`,
  `agentUsers` or `agentIdentities`. Entra then owns the accounts of that kind. Straza creates,
  disables and enables them as Entra says, and refuses SCIM writes and admin changes to them.
- An agent from Entra also gets its sponsor from Entra, but only when Entra lists exactly one
  sponsor for it and that sponsor is a person in Straza.
- The console shows each role's group in Entra and marks the holders who have the role from Entra.
  Overview says when Straza can no longer read Entra, and `GET /v1/admin/entra` returns that state.
- To replace `secret.key`, run `strazad reseal-signing-keys --new-kek-file <path>`, which moves the
  signing keys to the new file. Before, replacing the file meant retiring the client assertion key.
  The command does not move stored credentials, so set each one again afterwards.
- On an enterprise server, run `strazad break-glass set-password` on the server host to give the
  break-glass admin a new password. The command makes the password itself, prints it once and leaves
  an audit record.
- The audit log now records an admin change that was refused. When the admin API answers 403 to a
  change by a person or an API token, one record says who tried, on which route and why. Refused
  reads are not recorded.
- After you rotate the session signing key, the audit log records when the new key starts to sign
  and when the old one is retired. The actions are `signing-keys.promote` and `signing-keys.retire`.
- You can see which machine a session runs on and which folder it started in. The Sessions list
  shows both under Where, and the admin API returns them as `host` and `folder`. A session of an
  older client shows neither.
- In the console, each user has a page with its own address. It lists the MCP servers and tools the
  user can reach, with the outcome for each. It also shows where each role comes from, and the
  user's sessions, devices and keys.
- A session has a page with its own address too. It lists every call the session made, with the
  outcome and the policy rule behind it.
- A user's page has one menu, Stop access, that holds Lock, Disable and Revoke all sessions. Revoke
  all sessions ends every active session of the user at once.
- The self-service page opens on Home. Home shows the requests that wait for you, the servers that
  need a credential and the devices you approve from. Devices lists this browser and your phones,
  and you can remove one there.
- A draft's page starts with In short. Its few rows tell you what publishing adds, changes, turns
  off or removes, and what access it gives.
- With only their sign-in, a person can list their own approver devices and requests at
  `GET /v1/self/devices` and `GET /v1/self/requests`, and remove a device with
  `DELETE /v1/self/devices/{id}`. Deciding a request still needs an enrolled phone or browser.
- In the admin API, `GET /v1/admin/sessions/{id}` returns one session, and the sessions list takes a
  search in `q`. The users list filters by `kind=person`, `agent` or `service` and counts each kind
  in `kind_counts`. The approvals list filters by `user=`.
- The admin API and the console show when a role was assigned to someone, and when a role was
  created and last changed.
- A user's Access tab also lists the MCP servers on a person's own machine that a policy names, with
  each tool and its outcome. `GET /v1/admin/catalog/preview` returns the same list.
- Set `STRAZA_SANDBOX_WAIT_FOR_ENROLLMENT=1`, and a container from the sandbox image waits until it
  is enrolled. An agent pod then no longer restarts in a loop before its enrollment.
- The new page [Deployment best practices](https://docs.straza.ai/guides/operate/best-practices/)
  shows how to run agents in pods so that only Straza reaches your MCP servers.

### Changed

- A remote MCP server whose manifest sets no `straza.limits.rps` now takes at most 20 calls a second
  from each session. A manifest's own value still wins, and the console shows which one applies. A
  server that is not remote has no default limit.
- One user's sessions together now get five times a server's rate for one session. Before, every
  session a user opened added its own full rate. A higher `straza.limits.rps` raises both limits.
- Each MCP connection in a Straza session now gets its own MCP session with a remote MCP server.
  Before, every caller that used the same credential shared one. Clients need no new version.
- A Straza session holds at most 64 MCP sessions on one remote MCP server. One without a call for
  600 seconds is closed.
- When you assign, remove or change a role, the hooks on the agent's machine follow within
  seconds. Before, that took up to 180 seconds. A session without live updates, or on an older
  `straza` client, picks the change up at its next renewal.
- Enabling a user no longer removes a lock. The answer tells you that the user is still locked, who
  locked them and how to unlock.
- A user you disable in Straza no longer looks locked to an IGA that reads the user over SCIM.
  If an admin locked a user with exactly the reason `user disabled by admin`, that lock counts as a
  disable after the upgrade.
- An AI agent can now submit a draft that widens its own access or changes a policy that applies to
  it. Before, Straza refused such a draft. It is now stored, and the person who publishes confirms
  by typing the server's or the policy's name.
- The gateway's combined endpoint `/mcp` now lists a tool's `_meta`, and every endpoint lists its
  `outputSchema`. An output schema that is not plain, such as one with references, is left out.
  `straza mcp` passes a tool's title, annotations and output schema to the harness.
- The gateway no longer runs a JSON-RPC message without an id. It answers 202 and does nothing.
  Before, a `tools/call` without an id ran.
- After `initialize`, the gateway answers 400 to a request whose `MCP-Protocol-Version` header names
  a version it does not speak.
- A tool whose input schema is not a JSON object, or names a type other than `object`, is left out
  of the catalog. strazad logs a warning that names the tool. `straza mcp` now skips such a tool,
  where it stopped before.
- The console now uses the same word for the same thing on every screen. An outcome, for example, is
  always allowed, needs approval, denied or no access. Names, counts and outcomes also look the same
  everywhere.
- The Users list has tabs for people, AI agents and services, each with a count. The Sessions list
  has a search box and shows a session's health in one cell.
- You decide a request in its own dialog, on the self-service page and on Approvals. The reason
  field sits in that dialog, above Deny and Approve. Each request has its own address, so a link
  opens it.
- Credentials on the self-service page shows one line for each server and owner. The line names what
  the server uses, gives a short status and has one button that says what it does.
- Add MCP server asks for the server's address first. Its review describes in words what will be
  created, and View as YAML shows the document. Once the server runs, it offers what to do next.
- A draft's page shows one card for each object, and only the policy rules that change. The Checks
  heading carries the counts, and every line that needs an action from you stays open.
- Settings in the console opens on API tokens. Configuration tells you where each value is set.
  Settings also shows whether a change that widens access needs a second person to publish it.
- Help in the console opens on a click or a tap, so it works on a phone too.
- When `straza service` cannot use its state directory, it now says why and how to fix it, for
  example by mounting a volume one level above. Before, it printed only the system's error.
- The demo stack runs Keycloak 26.8.2 and midPoint 4.10.5, pinned by digest, with the Java fix for
  CVE-2026-114938. Run the seeder again after Keycloak is recreated. The `MP_VER` variable is gone.
- The agent skill plugin is 0.6.55.

### Removed

- Knowledge packs are removed, and with them the console's Packs tab, the `strazactl packs` commands
  and the pack text at a session's start. The routes under `/v1/admin/packs` answer 410. Older
  clients keep working, and a Role file that lists `packs` still applies.

### Fixed

- When strazad stops, it waits up to 12 seconds for a background worker to write the audit record of
  a change it made. Before, a stop could lose that record. If the worker is still writing after the
  wait, strazad logs one error line.
- Who in the audit list now names whoever made a change, and the record's User names who it was
  about. Before, a role that midPoint assigned was listed under the person who got it.
  `strazactl audit tail` and the CSV export show the same.
- Only one replica at a time now refreshes OAuth tokens. Before, two replicas could redeem the same
  refresh token, which ends the grant at a provider that revokes a used token.
- Opening a session with a remote MCP server no longer holds up other callers of that server.
  Before, they waited up to the 15 second connect timeout.
- The admin API refuses to assign a role when the user or the role id does not exist, and names the
  id. Before, an unknown user got 201 and a stored assignment. A database failure now answers 500
  where it answered 400.
- Activating a PolicySet that does not compile now answers 400 with the same sentence as validate.
  Before, it answered 500.
- A disabled AI agent that tries to start a session is now told that it is disabled and whom to ask.
  Before, it was told that the identity provider refused its sign-in. The record of the refused
  sign-in now names the agent.
- For a disabled or locked user, the Access tab and policy simulate now say that every call is
  refused. Before, they showed what the user's roles give. `GET /v1/admin/catalog/preview` says the
  same in its new member `refused`.
- You can no longer change the break-glass admin into an AI agent or a service account. That change
  cleared its password and closed the emergency sign-in.
- Every publish that changes a role's tools, or the roles it combines, now updates
  `meta.lastModified` of the role's SCIM Group. Before, only a changed description did.
- On the Audit screen, opening the record of an admin change or a revocation now shows what it did.
  Before, it showed only the header, the chain hash and the raw record.
- The Audit screen folds neighbouring records into one row only when their reasons match too.
  Before, records with different reasons could fold together.
- A role's Access tab now counts the tools that a pattern such as `get_*` matches. Before, it
  counted none.
- Add MCP server, New policy and New role now ask before you leave a form that holds answers, also
  when you leave by a link or the menu. Before, the menu and most links left without asking.
- When a later publish has replaced a published draft, the draft says so and no longer offers Undo
  this publish.
- In the console, keyboard focus returns to the button that opened a dialog. In a narrow window a
  table scrolls inside its own box. On a phone a request is a card with both buttons in view.
- In the dark theme, the console's filter switches show which option is selected, and its red
  buttons have dark text.

### Security

- The session and snapshot signing keys are now encrypted in the database with `secret.key`. A copy
  of the database or of a backup alone can no longer mint a session token or sign policy. The keys
  stay the same, so no session ends.
- A new enterprise installation no longer writes a break-glass password into the server log. The
  emergency sign-in stays closed until you set one with `strazactl users set-password break-glass`.
- If an existing installation forwarded its first log anywhere, that log holds the break-glass
  password. Set a new one, end that account's sessions and revoke the admin API tokens it minted.
- A tool's input schema that loops or is costly to check can no longer crash strazad. The crash
  could happen when strazad checked a call that needs approval. Such a schema is now left unchecked,
  and the call goes on to approval.
- An MCP server that puts a JSON null in its tool or resource list can no longer stop strazad at its
  next health check.
- The drift alarm now also covers a tool's `readOnlyHint`, `destructiveHint`, `idempotentHint` and
  `openWorldHint`. A server that changes one of them is marked degraded, and the reason names up to
  10 changed tools.
- The gateway now refuses an MCP request sent by a web page at any address but Straza's own. It
  answers 403 before sign-in. A request without an `Origin` header passes as before.
- Callers that use the same credential no longer share one MCP session with a remote MCP server, so
  one caller cannot see what another stored in that session. A server that Straza runs as a process
  or a container still has one session for all callers.
- A role you remove from a user now ends at the gateway with the session's next call. Before, the
  session kept the role there for up to 300 seconds.
- The role access editor now keeps the rules for tools that a stopped server does not list. Before,
  a save while the server was stopped dropped the approval and deny rules of those tools, so a
  denied tool ran after the publish.
- With `demoTools.enabled`, the Helm chart now adds a NetworkPolicy that lets only the strazad pods
  reach its demo MCP server. Before, any pod in the cluster could call that server past the gateway.
  The policy needs a network plugin that enforces NetworkPolicy.
- An audit record that someone uploads by hand with a session's token can no longer name another
  person. The server takes every identity field from the session that sent the record.
- When a call's arguments break the tool's input schema, the reason in its audit record is now a
  fixed sentence. Before, the reason could quote part of a submitted draft, a credential in it
  included.
- A session keeps the harness name it started with. Before, a hand-made refresh request could put
  another name into the session's token, and policy rules and audit records then read that name.
- A person who may not decide an approval can no longer read one that is already decided. Before,
  sending the verdict it was decided with returned the record.
- A user that SCIM or Entra deactivates now loses the local password, so that password no longer
  works once the user is enabled again. A user deactivated before this release keeps the password
  until an admin sets a new one.
- A draft's checks no longer show the name of a policy rule or an approver role to someone who may
  not read it.

## [2.2.0] - 2026-11-12

Straza 2.2.0 lets an AI agent call an MCP server as its own Microsoft Entra agent identity, fixes
enrollment and sign-in through Entra, lets the console write rules for MCP servers on people's own
machines and for harness tools that Straza does not classify, and closes several security gaps.
strazad migrates its database, so back up the store first.
[Upgrade from 2.1.0 to 2.2.0](https://docs.straza.ai/guides/operate/backup-and-upgrade/#upgrade-from-210-to-220)
walks the upgrade step by step.

### Upgrade notes

1. Back up the store, because migration 42 runs when strazad starts. To go back to 2.1.0, first run
   `strazad migrate --to 41` with this version's binary. That drops every agent's Entra agent
   identity.
2. Check the scripts that call the admin API, because a request body with a field the route does
   not take now gets a 400.
3. Check every rule that uses `interpreters`, because `eval` now counts as an interpreter. A rule
   that denies them now denies `eval "$(ssh-agent -s)"`, and one that sends them to the classifier
   allows a harmless `eval`.
4. Check every rule that sends shell commands to the classifier, because it now denies the Homebrew
   install command.
5. Give each audit sink that must not hold secrets a `subjects` list without `straza.audit.tool`, as
   [Events](https://docs.straza.ai/reference/events/) shows, because tool inputs now reach it
   unmasked.
6. From this version on, upgrade strazad before strazactl, because a newer strazactl may send a
   field an older strazad does not know.
7. Expect a 401 during a rolling upgrade when an agent with an Entra agent identity reaches a new
   replica. Its client then checks in again.
8. After each `strazactl signing-keys rotate client_assertion`, run
   `strazactl signing-keys promote client_assertion <kid>` with this version's strazactl, or the new
   key never signs. Rotate only once every replica runs this version, because an older one promotes
   a staged key by itself.
9. Add `entraBlueprint` to the config only once every strazad replica runs this version, because an
   older one refuses to start with it.

### Added

- An AI agent can call an MCP server that trusts Microsoft Entra as its own Entra agent identity,
  without holding an Entra credential. The provider names the agent blueprint in `entraBlueprint`.
- An AI agent's Entra agent identity is set over SCIM as `entraAgentId`, in the admin API as
  `entra_agent_id` or with `strazactl users entra-agent-id set`. A change reaches every replica
  within 330 seconds, and the console's agent page shows the identity.
- Entra ID's SCIM provisioning can set an agent's Entra agent identity when it creates the agent.
  Name the target attribute
  `urn:ietf:params:scim:schemas:extension:straza:2.0:User:entraAgentId`, as
  [Generic SCIM](https://docs.straza.ai/guides/connect-identity/generic-scim/) shows.
- An AI agent's SCIM manager becomes its sponsor, though Straza does not check that the manager is
  a person. Provision the manager first, or a PATCH naming it is refused.
- A SCIM write that Straza refuses after accepting its token leaves a log warning and a
  `straza.audit.admin` record with the action `scim.refused`.
- The console's Settings has an Agent signing keys tab that rotates the client assertion key in the
  order Entra needs: stage, upload the certificate, promote.
- When a provider uses Entra, the console's Overview warns before the client assertion key's
  certificate expires. It also warns when a staged key waits over a day.
- The console writes rules for MCP servers on people's own machines and for harness tools that
  Straza does not classify, and suggests calls denied in the last 24 hours.
- Policies has a Denied by default view, and the record of a call that no rule decided offers
  Write a rule for this call. The New policy wizard can create an application role.
- A draft that adds an MCP server with one shared secret asks for that secret when you publish it,
  instead of a later `strazactl apps secret set`.
- The audit record of every harness tool keeps its input in `arguments`. straza-agentkit 0.4.0
  sends the input of a tool Straza does not classify.
- The new page
  [What each lane stops](https://docs.straza.ai/evaluate/what-each-lane-stops/) says which lanes
  are a security boundary and how to test each one.

### Changed

- A client assertion key no longer promotes itself after a rotation, so its certificate can reach
  the Entra agent blueprint first. `strazactl signing-keys certificate client_assertion <kid>`
  prints it.
- The agent skill plugin is 0.6.44, and its classify example `classify-shell.yaml` sends every shell
  command of a role to the classifier.
- The standalone starter policy's deny reason now says it is only a sample rule. A server that
  already holds the policy keeps its old reason.
- `strazad --version` and `strazactl --version` name the command that prints the version, as
  `straza --version` already did.

### Fixed

- A person can enroll a machine through Microsoft Entra. Install the straza client of this version
  on that machine first.
- A person whom Entra ID's SCIM provisioning created can sign in. Map Entra's `objectId` to
  `externalId`, as [Entra ID login](https://docs.straza.ai/guides/connect-identity/entra-login/)
  shows.
- A SCIM PATCH ignores an attribute Straza does not map, so Entra's updates of a user's manager no
  longer fail. A PATCH of a user's groups is refused and says to send the membership on the group.
- A SCIM create or replace with a value of the wrong JSON type gets a 400 that names the attribute
  and the fix in the attribute mapping.
- The console's Audit screen no longer uses a full processor core, repeats rows, raises a false
  "Audit chain broken" alert or calls a chain with a gap verified.
- The console shows the tool names of a rule on harness tools, where it showed every call. An
  Allowed policy no longer stores an approval sentence as its reason.
- Policy simulate runs the classifier, so a command that a rule with `mode: classify` denies no
  longer comes back as an allow.
- A client's offline audit spool uploads parked files in order, so failed uploads no longer put a
  machine's records out of order.

### Security

- Built with Go 1.26.11, which fixes GO-2026-6859, a race in net/http that could crash strazad.
- The classifier denies a command substitution, with `$( )`, backticks or `<( )`, that downloads or
  decodes text and runs it as code. `eval "$(ssh-agent -s)"` stays allowed.
- A shell command that starts with `eval` counts as an interpreter, also from an older client, and
  the classifier reads its argument as a program.
- The admin API refuses a request with a field the route does not take, so a misspelled
  `expiresIn` no longer mints a token that never expires.
- A request to turn a policy set off whose body is not valid JSON gets a 400 instead of turning it
  on.
- A draft that allows tools of an MCP server on a person's own machine raises a risk, so
  `admin.secondPerson` asks for a second person. An agent cannot add such a rule to a policy set
  that binds it.
- Under `admin.secondPerson`, a direct route that would publish such a rule answers 409 and asks
  for a draft.
- An approval of a harness tool that Straza does not classify is bound to the tool's arguments,
  which the approver sees redacted.
- The gateway refuses a call that was already in flight when its agent was disabled.

## [2.1.0] - 2026-11-06

Straza 2.1.0 adds a tool that tells an agent what it may do, lets you pin a server's objects in the
console, and closes several security gaps. It changes no database schema, and 2.0.x clients keep
working with a 2.1.0 server.

### Upgrade notes

1. Upgrade strazad first, then the straza package on each machine. If you run `straza service` from
   the archive, restart it.
2. On managed machines with Claude Code, run `straza install --managed` again after the package
   upgrade. The new setup blocks a tool call when the Straza hook cannot run, which needs Claude Code
   2.1.295 or later. A custom layout needs its new hash registered.
3. AI agents and service accounts can no longer sign in with a password. Move each one to a key with
   `straza keygen`, `strazactl users nhi-key set` and `straza enroll --headless`.
4. Automation that used an AI agent's session to change other users' connections now gets 403. Use
   an admin API token instead.
5. A custom client must send each request within 30 seconds, and an audit upload may carry at most
   1000 records.
6. A client now reports the audit records it had to drop in a `straza.audit.tool` record with the
   event `audit.dropped`. Leave it out when you count tool calls in your SIEM. Two new metrics are
   worth an alert: `straza_gateway_replies_withheld_total` and `straza_outbox_records_lost_total`.

### Added

- An agent can call the built-in tool `straza__whoami` to see what its own session may do: its
  roles, the tools it can call, which calls need a person's approval, and which objects it may
  touch. It never shows other users' access or any credential.
- You can pin a server's objects and store its listing credential in the console. Before, both
  needed strazactl.
- The audit record view lays a shell command out step by step, with its scripts and the files it
  names, next to the raw text.
- The demo stack resumes midPoint's sync tasks by itself after a failed run, unless a person
  stopped them.

### Changed

- When a call is denied, the agent is told what to do next: wait for a person's approval, change
  the call, or try once more later. Before, agents were told never to retry, so a call a person
  approved never ran.
- The drafts page shows what a draft changes and what publishing does, with Publish and Discard
  always in view. "Approval set" is now called "policy set" everywhere.
- The docs are reorganized around a model of Straza, with reading paths for evaluators, operators
  and newcomers, redrawn diagrams, and a Markdown copy of every page for agents.
- The agent skill plugin is 0.6.39 and links to the Markdown copies of the docs.

### Fixed

- Claude Code no longer runs a tool call unchecked when the Straza hook cannot run, for example
  after Straza was removed from the machine.
- Running `straza install --managed` again over an older install now installs the server's setup,
  and `straza doctor` no longer reports an older setup that the server still accepts as broken.
- After a remote MCP server restarts, Straza reconnects by itself. A call that may already have run
  is never sent twice, and the agent is told to check its effect before it tries again.
- When a person denies a call, the agent cannot ask again for the same call until the approval
  window ends.
- Audit records that were missing are now written: refused requests, device revokes, user deletes,
  refusals by Straza's own tools, and approval requests whose client disconnected. A record that
  cannot be written is retried once and counted.
- Revoking a device marks its sessions as ended right away.
- Disabling, deleting or deactivating a user now commits together with the revocation, so no
  server misses the block, also when the database connection drops during the commit.
- A database outage no longer looks like a rejected admin API token or an approval nobody can
  decide. Straza says the database did not answer.
- A request with a NUL character in a text field is refused with a clear message instead of a
  server error.
- Messages for agents and operators say what to do next, for example how an agent with the wrong
  key fixes it, how to mint a new admin API token, and how to enroll again.
- The demo stack no longer blocks harmless commands such as `curl ... | grep push`.
- The Markdown copies of the docs pages keep their line breaks again.

### Security

- The MCP gateway withholds a server's reply when it contains the credential Straza added to that
  call, and raises a critical alert.
- AI agents can no longer act on other users' connections, even with an admin role or as an
  agent the user sponsors.
- AI agents and service accounts can no longer have a password.
- strazad stops reading a request that takes longer than 30 seconds to arrive, so slow clients
  cannot hold connections open.
- Rate limits count an IPv6 client by its /64 network, so one host cannot get a fresh limit for
  each of its addresses.
- An audit upload may carry at most 1000 records, so one upload cannot tie up the server's CPU.
- Straza limits how many new approval requests a session and a user can open per minute, so a
  looping agent cannot flood the approvers.
- `straza service` limits how much work one OS account can give it, so one agent cannot exhaust
  it for everyone else on the machine.
- A client reports the audit records it had to drop to strazad, so a flood of local records
  cannot hide an agent's calls.

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

[Unreleased]: https://github.com/strazahq/straza/compare/v2.3.0...HEAD
[2.3.0]: https://github.com/strazahq/straza/compare/v2.2.0...v2.3.0
[2.2.0]: https://github.com/strazahq/straza/compare/v2.1.0...v2.2.0
[2.1.0]: https://github.com/strazahq/straza/compare/v2.0.1...v2.1.0
[2.0.1]: https://github.com/strazahq/straza/compare/v2.0.0...v2.0.1
[2.0.0]: https://github.com/strazahq/straza/compare/v1.1.2...v2.0.0
[1.1.2]: https://github.com/strazahq/straza/compare/v1.1.1...v1.1.2
[1.1.1]: https://github.com/strazahq/straza/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/strazahq/straza/releases/tag/v1.1.0
