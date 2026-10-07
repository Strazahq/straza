---
title: Hardening
description: Every security setting checked against its default in both profiles, and each control the profiles leave to you turned on, with the command that proves it.
pagetype: how-to
weight: 13
draft: false
who: You, as the admin who runs the server
where: A terminal that reaches strazad, with strazactl signed in as an admin, and the server's config file
steps: true
tested:
  version: v1.1.0
  platform: on the enterprise demo stack and on a standalone server in a Linux container
  date: 2026-09-28
applies_to: both
keywords: hardening security settings defaults tls listen metrics token version response headers body cap rate limit approver pin attestation floor localToolDefault unsignedOwnDecisions secondPerson disableSSL admin api token scim rotation signing key sinks sandbox
---





An operator who owns a running Straza server tightens it here one control at a time, with the command that proves each change. The enterprise profile already starts from a strict floor: local tools are denied unless a policy allows them, an unreachable server denies everything at token expiry, the audit queue waits rather than drops, and a coding harness gets a session token only from a managed install whose reported hashes match the registry. A standalone server relaxes all four for a single machine. The table below names the setting behind each of them, beside every other security setting and its default in both profiles. The numbered steps after it are the controls that neither profile can turn on for you, because each needs a fact only you hold: a certificate, a token, a role name, a receiver.

## Security settings and their defaults {.nostep}


Its rows cover the settings that decide who can reach strazad, what it shows without a token, how a call falls when no rule decides it, and what the audit trail keeps. A row marked Fixed has no setting that changes it. [Standalone and enterprise compared]({{< relref "reference/standalone-and-enterprise.md" >}}) lists every other difference between the profiles, [Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md" >}}) says what happens when each lifetime runs out, and [Configuration]({{< relref "reference/configuration.md" >}}) lists every key with its environment variable.

| Setting | What it controls | Standalone | Enterprise |
|---|---|---|---|
| `server.listen` | The address of the main listener, which serves the API, the console and the gateway | `127.0.0.1:8420`, this machine only | `:8420`, every interface |
| `server.tls.certFile` and `keyFile` | TLS 1.2 or newer on the main listener | Unset, so plain HTTP with no warning | Unset, so plain HTTP with a warning at every boot |
| `server.approverTLS.autoMint` | A second listener, HTTPS only, that serves only the phone approver routes | `true`: port 8443 on every interface, with a self-signed certificate it creates in the data directory | `false`: no approver listener until you configure one |
| `server.approverTLS.perIPRPS` | Requests per second that each connecting address may make to the approver listener | 10 | 10 |
| `server.loginPerIPRPS` | Requests per second that each client address may make to the password submit, and under enterprise to the agent token endpoint | 2 | 2 |
| `server.maxBodyBytes` | The largest request body, except on `/mcp` and `/v1/audit/batch`, which cap their own at 4 MiB | 1 MiB | 1 MiB |
| `server.metricsToken` | A bearer token that `/metrics` requires | Unset, so `/metrics` answers anyone who reaches the listener | Unset, the same |
| Fixed | Browser hardening headers on every answer of both listeners | Always sent | Always sent |
| Fixed | `/version`, `/healthz` and `/readyz` on the main listener | Answer without a token | Answer without a token |
| Fixed | The signed policy snapshot at `/v1/snapshot` | Answers without a token | Needs a checked-in session |
| `capture.bodyStore.disableSSL` | Turns off TLS to the S3-compatible store that holds recorded conversation bodies | Not used, because standalone keeps bodies in its database | `false`, so TLS stays on |
| `oidc.jitProvision` | Creates a user at their first verified sign-in | `true` | `false`, because your identity manager creates users over SCIM |
| `governance.minAttestation` | The lowest attestation that a check-in gets a session for | `none`, any install | `managed`, hashes that match the registry |
| `governance.offlineGraceTTL` | How long a client keeps deciding from its last signed policy after its token expires, while strazad cannot be reached | 15 minutes | 0, so it denies once the token expires |
| `governance.sessionMaxLifetime` | The longest a session lives from its start | 12 hours | 12 hours |
| `governance.deviceTokenTTL` | The lifetime of a machine's device credential, renewed at a check-in past half of it | 30 days | 30 days |
| `governance.localToolDefault` | The decision for a call that no rule matches and that is not an MCP tool call: a local tool, a tool Straza does not know, or a call that names no tool | `allow` | `deny` |
| Fixed | An MCP server that runs as a command | Runs as a child process of strazad, with its user, files and network | Refused, with no setting to allow it |
| `apps.allowLoopbackUpstreams` | Whether strazad dials a remote MCP server at a loopback address. Link-local, cloud metadata and unspecified addresses are refused in both profiles | `true` | `false` |
| `approval.unsignedOwnDecisions` | Whether a person may decide their own request from the console, strazactl or Slack, which carry no device signature | `false`, so only an enrolled phone or browser decides it | `false`, the same |
| `admin.secondPerson` | Whether a change that widens access needs a second person to publish it | `false` | `false` |
| `governance.auditBackpressure` | What strazad does with audit records that the database cannot take | `drop-with-counter`: decisions go on, and lost records are counted | `block`: records stay queued until the database takes them, and once 4,096 wait, a new decision waits up to 25 seconds and is then refused |
| `governance.sentinel.enabled` | The audit sentinel, which flags patterns after the fact and blocks nothing | `false` | `false` |

Every answer of both listeners carries `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer` and `Content-Security-Policy: frame-ancestors 'none'`, so no other site can frame the console's Approve button. strazad sets no fuller content security policy, because the console uses inline styles and an enterprise sign-in loads your identity provider from another origin. If your own policy needs one, set it at your ingress and check that the console still signs in behind it.

`/version` answers anyone who reaches the main listener. It names the exact version and commit, the Go version, the operating system and architecture, the profile and the runtimes this host can start MCP servers with. When strazad serves the approver listener itself, it adds the approver's address, pin and certificate expiry, and the certificate's path when strazad minted it. `strazactl status`, `straza doctor` and the console's footer read it with no token. `/readyz` names a failing component with its error. The approver listener answers both `/version` and `/healthz` with 404, because it serves only the approver routes and `/readyz`.

Two settings in the table open a known limit when you change them. Setting `approval.unsignedOwnDecisions` to true lets an agent that runs as a person approve its own calls with that person's strazactl login. Setting `apps.allowLoopbackUpstreams` to true under enterprise lets a remote manifest reach strazad's own host. [Known limits]({{< relref "security/known-limits.md" >}}) lists every limit with what reduces it.

## Read the plaintext warning as your signal


Until you give the enterprise profile a certificate pair, its listener serves plaintext and says so once at boot, whether or not an ingress in front of it terminates TLS. That line is the check for this control: while it appears, session tokens and admin API tokens cross the wire unencrypted. The [enterprise demo stack]({{< relref "get-started/enterprise-demo-stack.md" >}}) runs the plaintext shape on purpose, and the log of its server container, `straza-eval-strazad-1`, carries the warning:

```sh
docker logs straza-eval-strazad-1 2>&1 | grep -m1 PLAINTEXT
```

```text
{"time":"2026-09-28T19:21:59.0857078Z","level":"WARN","msg":"serving PLAINTEXT HTTP in the enterprise profile. Set server.tls.{certFile,keyFile} or terminate TLS at your ingress/LB; session tokens and admin API tokens transit this listener. The TLS and exposure guide walks both shapes","docs":"https://docs.straza.ai/guides/operate/tls-and-exposure/"}
```


Setting `server.tls.certFile` and `server.tls.keyFile` makes the line disappear. An ingress that terminates TLS does not, because the warning keys on the profile and the listener's own TLS state, so behind an ingress the line stays at every boot and your check is the ingress configuration instead. The standalone profile never prints it, because its listener defaults to the loopback address, so a standalone server that you rebind to a routable address is plaintext without a word. [TLS and exposure]({{< relref "guides/operate/tls-and-exposure.md" >}}) walks both shapes and the public URL that must change with them.

## Gate the metrics endpoint


`/metrics` answers anyone who can reach the main listener, and it counts decisions by effect and names MCP servers, sinks and routes. That is too much for an open port. Set `server.metricsToken`, or the variable `STRAZA_METRICS_TOKEN`, and the endpoint requires that value as a bearer token, compared in constant time. Set the variable on the server, keep the same value in `METRICS_TOKEN` on the client side, and ask twice:

```sh
curl -s -w '\n%{http_code}\n' http://127.0.0.1:8420/metrics
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $METRICS_TOKEN" http://127.0.0.1:8420/metrics
```

```text
{"error":"metrics token required (server.metricsToken)"}

401
200
```

Give Prometheus the same value through `authorization.credentials_file` in its scrape configuration, as [Metrics and alerts]({{< relref "guides/operate/metrics-and-alerts.md" >}}) shows. A token does not make the endpoint fit for a public ingress, so keep scraping it over the private network.

## Pin the approver listener and read the pin back


The phone approval app trusts the approver listener by the key pinned at enrollment rather than by a certificate chain, so the pin is the fact to record and to protect. `/version` on the main listener reports it together with the certificate's expiry, without a token, as the table above says. This is the enterprise demo stack's answer:

```sh
curl -s http://localhost:8420/version | jq .approver
```

```text
{
  "public_url": "https://127.0.0.1:8443",
  "tls_spki_pin": "sha256/d9WLGvXRfWEn+KWy49rYsYzBpLq2NErbdeZcc7rE5po=",
  "cert_not_after": "2028-12-26T19:21:57Z",
  "auto_minted": true,
  "cert_file": "/var/lib/straza/approver-tls/cert.pem"
}
```


Compare the pin with the one your approvers enrolled against after every server move or restore. A different pin means a new certificate pair, and every enrolled phone must enroll again, which is why the pair belongs in your backups and why the enterprise profile expects you to bring the four `server.approverTLS` settings rather than minting a pair per pod.

## Require a managed install for sensitive roles


Attestation has two floors. `governance.minAttestation` is the check-in floor, `managed` by default in the enterprise profile and `none` in standalone, and the gateway enforces the same level from the token on every call. A policy rule adds the second floor with a `require` block, so one role can demand a managed install for its local tools while another keeps working from a user-mode install. The managed install itself is `straza install --managed --server <server-url> <harness>`, run with sudo, or as an administrator on Windows, which puts the enforcement files in root-owned system paths and lets the server check the hashes they report at check-in. This page shows the server side of it. [Known limits]({{< relref "security/known-limits.md" >}}) says what the `managed` level does and does not prove.


The expected hashes of the hook wiring come from the server's own render of the harness configuration and are registered at boot, so the registry is populated before the first managed client checks in. Hashes of the binary and the configuration count only once you register them with `strazactl attestation add`.

```sh
strazactl attestation list
```

```table
ID                                    ARTIFACT           HARNESS      PLATFORM       HASH                                                                     NOTE
01a0e977-8378-7af2-9422-f2045e601d73  hooks.claude-code  claude-code  linux/amd64    sha256:c9ee42bfb613e2e9165bad88ee8d77e889ddb02aa4effe7aa9f664d51b928ab8  harness-config render (strazad v1.1.0)
01a0e977-837f-7af9-a247-79867a3eafeb  hooks.claude-code  claude-code  linux/arm64    sha256:c9ee42bfb613e2e9165bad88ee8d77e889ddb02aa4effe7aa9f664d51b928ab8  harness-config render (strazad v1.1.0)
```

The listing is trimmed to its first two rows. Each row the server registered itself names a hook wiring artifact, and its note names the render and the running strazad version.


For the policy floor, create an application role `managed-tools`, export the demo stack's `agent-guardrails` set with `strazactl policy show agent-guardrails > managed-tools-guardrails.yaml`, rename it, point `match.roles` at the new role, drop its capture block, and give the rule that opens the local tools a `require` block with its own reason:

```yaml
    - id: local-tools
      require:
        attestation: managed
      reason: "Straza: local tools for this role need a managed, hash-verified install"
      events: [tool.pre]
      tools: [shell.exec, file.read, file.write, file.edit, net.fetch, task.spawn]
      effect: allow
```

```sh
strazactl roles create managed-tools --kind application --description "local tools only from a managed install"
strazactl policy apply -f managed-tools-guardrails.yaml
strazactl policy activate managed-tools-guardrails
```

```text
created role managed-tools (01a0b955-4bab-737b-b7ea-d1ff952521a1)
applied managed-tools-guardrails (Off, 01a0b955-4bcc-74c4-9d2e-0612473faade)
published managed-tools-guardrails, it is live now; new snapshot d14326a6ef234c280f76e9262f87935ebeeb7cfa23a0dac49a42f0e6f595f5a7
```


A simulation proves the floor without a client. A session from a user-mode install attests as `advisory` at best, and the same command from a managed install attests as `managed`:

```sh
strazactl policy simulate --roles managed-tools --tool shell.exec --command "git status" --attestation advisory
strazactl policy simulate --roles managed-tools --tool shell.exec --command "git status" --attestation managed
```

```text
This call would be denied.
Decided by rule local-tools in policy managed-tools-guardrails: Straza: local tools for this role need a managed, hash-verified install.
```

```text
This call would be allowed.
Decided by rule local-tools in policy managed-tools-guardrails: Straza: local tools for this role need a managed, hash-verified install.
```

Both answers are cut after the second line, and the rest names the subject, the snapshot and the wire fields. The reason travels with the deny, so an agent on an unmanaged machine reads why its shell stopped working instead of a bare denial.

## Scope every admin API token


A long-lived admin API token is its own principal, hashed at rest and shown once, and it should carry only the areas the system behind it reads. The scope grammar is `area:read` or `area:write` over the areas that [Delegated admin]({{< relref "guides/operate/delegated-admin.md" >}}) lists, `drafts` among them, and `full` is the root credential. Areas are all a token can carry. The role that a single MCP server names, the other way that guide delegates, is assigned to a person in your identity manager and never to a token, so no long-lived credential can quietly become a server's admin. This example mints a token for a SIEM collector that only reads the audit chain, with a lifetime:

```sh
strazactl api-token create --name siem-collector --scope audit:read --ttl 720h
```

```text
id:      1ff50659-4aeb-45f7-b8ed-452840751869
name:    siem-collector
scope:   audit:read
expires: 2026-10-28 20:14 UTC
token:   wat_QnKSWl...

Store this token now. It is not retrievable again.
```

The token value here is cut after its first characters. Put the printed token in `ADMIN_API_TOKEN`. Inside its area the token works, and one step outside it the server names the missing scope:

```sh
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $ADMIN_API_TOKEN" 'http://localhost:8420/v1/admin/audit?limit=1'
curl -s -w '\n%{http_code}\n' -H "Authorization: Bearer $ADMIN_API_TOKEN" http://localhost:8420/v1/admin/policies
```

```text
200
{"error":"token lacks scope policy:read"}

403
```


Revoking is one command, and a revoked token gets `{"error":"admin API token rejected: unknown, expired or revoked (strazactl api-token list)"}` with status 401 on its next request. `strazactl api-token list` shows every token's scope, expiry and last use, which is where an unused token with a wide scope stands out.

```sh
strazactl api-token revoke 1ff50659-4aeb-45f7-b8ed-452840751869
```

```text
revoked 1ff50659-4aeb-45f7-b8ed-452840751869
```

## Rotate the identity manager's token


The identity manager's credential is an admin API token whose scope carries `scim:read,scim:write`, the scopes it writes people and role membership with. It is stored as a SHA-256 hash and shown once, so rotation means minting a new one, moving the identity manager to it, and revoking the old one, in that order, with no gap in provisioning. On the enterprise demo stack the live token is named `midpoint`, so this example mints a second one and revokes it:

```sh
strazactl api-token create --name identity-manager-rotation --scope scim:read,scim:write
```

```text
id:      7db165d8-00cb-4cb9-87e3-d19931d1a07f
name:    identity-manager-rotation
scope:   scim:read,scim:write
expires: never
token:   wat_eB9_Pe...

Store this token now. It is not retrievable again.
```

Paste the value into the identity manager's connector, wait until `strazactl api-token list` shows a LAST USED time on the new token, then revoke the previous token:

```sh
strazactl api-token revoke 7db165d8-00cb-4cb9-87e3-d19931d1a07f
```

```text
revoked 7db165d8-00cb-4cb9-87e3-d19931d1a07f
```

The example revoked its own token and left the live one in place, and `strazactl api-token list` no longer carries it. The [midPoint]({{< relref "guides/connect-identity/midpoint.md" >}}) and [Generic SCIM]({{< relref "guides/connect-identity/generic-scim.md" >}}) guides show where the value goes on the identity manager's side.

## Rotate the session signing key


Every session token, device credential and approver device token is signed with one Ed25519 key that the server keeps in its store. Rotating it is one command, and the rotation runs in two phases so that no replica ever signs with a key another replica has not seen yet:

```sh
strazactl signing-keys rotate session
```

The answer is two sentences. The first names the new key, `Staged a new session signing key <kid>.`, and the second gives the timings, `It becomes the signing key in about 1 minute, and the previous key stops verifying about 30 days after that.` Behind them, the new key is stored as staged and published in the JWKS at once. Every replica reloads its keys every 30 seconds, and after two of those intervals one replica promotes the staged key to active and marks the previous key retiring. A retiring key still verifies. Nothing signed before the switch is refused, and the old key is retired 30 days and one interval after it stopped signing, because the device credential and the approver device token live 30 days and were signed with it. A raised `governance.deviceTokenTTL` lengthens that wait.

Running the command again while a key is staged returns the same key, so a repeated command never piles up keys. Each run writes one `straza.audit.admin` record with the action `signing-keys.rotate` and the kid. A deployment with several replicas needs nothing more: the reload and the promotion run on every replica, and a replica that restarts loads the current key set from the store.


The command names the purpose because a second key rotates the same way. `strazactl signing-keys rotate client_assertion` creates or rotates the key that signs agents' clients in at your identity provider, and its first answer gives the key document's address to register there as the JWKS URL. That key can sign in as any agent, so only a full administrator may run it, the role `straza-admin` or an admin API token with the scope `full`, and never from a session that a coding harness checked in. `strazactl signing-keys list` shows every key with its purpose and status, which is how you confirm a promotion or a retirement. When a client assertion key may have been copied, `strazactl signing-keys retire <kid>` takes it out of the key document on every replica within 30 seconds. Retiring the key that signs leaves nothing to sign with, so run the rotate command right after and expect about a minute until the new key signs. Your identity provider may still hold the retired key in its own cache, so clear that cache or remove the JWKS URL from the agents' clients there as well. Every create, rotate, promotion and retirement leaves one `straza.audit.admin` record with the purpose and the kid.

## Back up the keys that are also identities


Three files cannot be regenerated for free: the sealing key that opens every stored credential, and two key pairs that clients pinned, the approver TLS pair behind the pin above and the WebPush key that browser subscriptions are bound to. Losing the sealing key fails every credentialed MCP server closed, losing the approver pair makes every phone enroll again, and losing the WebPush key ends every browser push subscription until the browser registers again. [Backup and upgrade]({{< relref "guides/operate/backup-and-upgrade.md" >}}) lists where each lives per profile and how to restore it, and [Keys, certificates and tokens]({{< relref "security/keys-certificates-and-tokens.md" >}}) puts these three beside every other key and token the server holds.

## Send the audit stream off the box


The hash chain proves that no record was edited in place, and it cannot by itself defeat an attacker who owns the database and rewrites every later record too. A sink puts a copy where that attacker cannot reach, as a signed webhook or a file, from a durable consumer that retries through an outage. The stream keeps undelivered events up to `events.auditStreamMaxAge` and `events.auditStreamMaxBytes` and then discards the oldest, so size both for the longest receiver outage you plan for, and keep the store's hash chain as the record. The enterprise demo stack forwards into Elasticsearch, and the list shows its health:

```sh
strazactl sinks list
```

```table
NAME     TYPE     TARGET                                          BACKLOG  PARKED  DELIVERED  LAST ERROR
elastic  webhook  http://elasticsearch:9200/straza-events/_doc?…  0        0       479        -
```

A growing backlog or a parked count above zero is the thing to alert on. LAST ERROR keeps the most recent failed delivery, with its time in UTC, even after later deliveries succeed, so read it together with the backlog and the parked count. Here no delivery has failed since the stack booted, so the cell reads a dash. [Sinks and SIEM]({{< relref "guides/audit/sinks-and-siem.md" >}}) adds a sink and replays a parked event.

## Put hookless agents in the sandbox profile


A process that runs `straza exec` on an open machine is governed exactly as far as a user-mode hook is, because the same process can call the kernel without the wrapper. The sandbox image closes that: the wrapper becomes the only executable route, the root filesystem is read-only, the writable mounts are noexec and capabilities are dropped. Two things stay open inside the image: computation in the agent's own interpreter is not governed, and the shipped compose file leaves the container's network routable, so set `internal: true` on its network and attach only strazad, as the comments in `deploy/sandbox/compose.yaml` describe. Build it and run your agent inside it as [Hookless processes]({{< relref "guides/govern-an-agent/hookless-processes.md" >}}) shows, and keep the image's allowlist at what shipped, since every binary added to it is a direct execution target that no rule reaches.

## Keep MCP servers behind the gateway


An MCP server that a person adds to a harness's own configuration does not pass through the gateway. Only the advisory hook sees its calls, its credential sits on that machine, and governing it is that person's responsibility. Register every MCP server that holds a credential in Straza, so the gateway decides its calls and the credential stays on the server. On Claude Code, a managed install also writes an exclusive managed MCP file, so the harness loads only the servers named there.

## Keep people's logins away from their agents


An AI agent that runs as a person can read that person's strazactl login in `~/.straza/credentials.json`, and with it enroll an approval device of its own. Inside Claude Code, the Gemini CLI and Codex installed from npm, strazactl refuses every command that changes Straza on the stored login, and an agent that unsets their variables gets past that guard. Do admin work on a machine or an account where no agent runs as you, or end it with `strazactl logout`, which revokes the stored session and deletes the file. Review the enrolled approval devices regularly:

```sh
strazactl approvers list
```

Revoke a device nobody recognizes with `strazactl approvers revoke` and its ID.


Keep `approval.unsignedOwnDecisions` at its default, false. Then a person's decision on their own request counts only when an enrolled phone or browser signs it, and a decision from the console, strazactl or Slack is refused. Set to true, it lets an agent that holds the person's strazactl login approve its own calls. This path is one of the [known limits]({{< relref "security/known-limits.md" >}}), with what reduces it.

## Undo {.nostep}


Removing `server.metricsToken` and restarting opens the metrics endpoint again. The policy floor is undone with `strazactl policy deactivate managed-tools-guardrails`, after which `strazactl policy delete managed-tools-guardrails --yes` removes the set and `strazactl roles delete managed-tools --yes` removes the role. A revoked token cannot be restored: mint a new one. A staged signing key cannot be withdrawn either. It is promoted on schedule, and the previous key retires when the credentials it signed have expired.
