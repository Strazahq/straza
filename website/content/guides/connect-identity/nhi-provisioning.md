---
title: AI agents as identities
description: An AI agent provisioned by your identity manager signs in to Straza with its own key and no browser, and leaves again when the identity manager retires it.
pagetype: how-to
weight: 50
draft: false
tested:
  version: v1.0.0-1570-gaf326165-dirty
  platform: An Alpine 3.20 container against a standalone server, both built from an uncommitted working tree, where the SCIM request, the role made with strazactl roles create and its members PATCH, every strazactl and curl block, and keygen, the headless enrollment, status and the refused enrollment through a straza service started with STRAZA_HOME in the agent's home directory all ran. That run came before strazactl stopped signing in, so the admin API token the steps now hand to strazactl was written in on 2026-11-01 and not walked. The administrator's commands and the agent's commands ran as the same OS user with separate home directories. The console steps and the midPoint notes describe the demo stack and were not run. The Undo sentences that say a key removal ends the running sessions and refuses the held ID token, and the dialog text there, were written from the code on 2026-10-27 and not walked
  date: 2026-10-21
applies_to: both
who: You, as the admin, and the AI agent's machine
where: A terminal with strazactl and curl, and a terminal on the agent's machine
steps: true
modes: [console, cli]
mode_default: cli
keywords: nhi agent provisioning sponsor credential
---


Your identity manager creates and retires AI agents the way it does people, and each agent reaches Straza with its own identity and a key instead of a password. You work as the admin in one terminal and on the agent's machine in another. At the end one agent is provisioned over SCIM, enrolled with a key and no browser, and retired again.

## How an agent differs from a person {.nostep}


An AI agent is an identity like a person, born in your identity manager and provisioned over SCIM. Two things differ. Its record says it is an agent and names the person behind it, so policy can treat it as an agent and approvals can reach that person. Its credential is an Ed25519 key registered on the identity, so it enrolls with no browser and no password.

The identity manager cuts an agent the way it cuts a person. Once the record is deactivated, the next token grant fails and every running session is revoked.

## Before you start {.nostep}


- `strazactl` with `STRAZA_SERVER` and an admin API token in `STRAZA_API_TOKEN`, or a console session that holds the identity area. The steps mint another token, which takes `tokens:write`, a root-equivalent grant, so mint a **Full root** token for 30 days in the console as [Mint an admin API token]({{< relref "guides/console/settings.md#mint-an-admin-api-token" >}}) shows, and revoke it when you are done.
- The `straza` binary on the agent's machine.
- An admin API token in `ADMIN_API_TOKEN`, for the SCIM request and the two admin API reads below. Mint it with `strazactl api-token create --name idm-agents --scope scim:read,scim:write,identity:read`. In the console, mint it under **Settings**, **API tokens**, **New API token** with the job **Custom**, **scim** set to **read and write** and **identity** set to **read**.

The command examples ran against a standalone server at `http://localhost:8420`, with the agent's side on the same host. The console steps and the midPoint notes describe the [demo stack]({{< relref "get-started/enterprise-demo-stack.md" >}}).

## Provision the identity


Send the user with the agent schema in `schemas`, `userType` `agent`, and the Straza extension carrying `agencyMode` and `sponsor`. In the demo stack midPoint sends this request. Here it is sent with curl:

{{< command terminal="Terminal" purpose="SCIM, as the identity manager" >}}
```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/scim+json' \
  -X POST http://localhost:8420/scim/v2/Users -d '{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User","urn:ietf:params:scim:schemas:extension:agent:2.0:Agent","urn:straza:params:scim:schemas:extension:2.0:User"],"externalId":"idm-agent-0001","userName":"scout-bot","displayName":"Scout bot","title":"Scout agent","userType":"agent","active":true,"urn:straza:params:scim:schemas:extension:2.0:User":{"agencyMode":"autonomous","sponsor":"alice"}}' \
  | jq -c '{id, userName, userType, active, ext: .["urn:straza:params:scim:schemas:extension:2.0:User"]}'
```
{{< /command >}}

{{< see >}}`"kind":"nhi"` and the sponsor `alice` under `ext`.{{< /see >}}

```text
{"id":"01a15d8e-856f-7d10-9e63-a550763da368","userName":"scout-bot","userType":"agent","active":true,"ext":{"agencyMode":"autonomous","kind":"nhi","locked":false,"origin":"scim","sponsor":"alice"}}
```


Each field tells Straza something different.

| Field | What Straza does with it |
|---|---|
| The agent schema, or `userType` `agent` | Records the identity as an AI agent. An AI agent never decides an approval, and it may sign in with a key. Either signal is enough, for a connector that cannot send the schema. |
| `agencyMode` | Policy can match it with `match.identity.agencyMode`. With `autonomous`, a rule that asks for the requester's own confirmation denies, because no person stands behind the session. `supervised` and `interactive` change nothing beyond what a policy matches on them. |
| `sponsor` | The username of the person behind the agent. An approval rule that names no deciders and no approver roles goes to this person, and a rule can name them beside approver roles with `deciders: [sponsor]`. |

The read-only `kind` in the SCIM render is `nhi` only when the create carried the agent schema, and it never changes after creation. A row created with `userType` `agent` alone reads `kind` `human` there for good, while the admin surface and the key sign-in treat it as an agent. The mismatch stays visible to a certifier.

In midPoint the persona archetype and a sponsor reference on the user produce these values, so the seeded agent `sam-sre-agent` arrives this way with no mapping of its own. The demo stack's PolicySet routes the seeded agent's gated calls with `deciders: [sponsor]`. Every identity selector a policy can use is in the [PolicySet grammar]({{< relref "reference/policyset-grammar.md#match" >}}).

{{< console >}}
{{< clicks "Users" >}}

{{< shot name="demo-users" caption="Users on the demo stack, where the agents carry the badge **AI agent** and the origin `SCIM`." >}}

{{< see >}}A `scout-bot` row with the kind badge **AI agent** and the origin badge **SCIM**.{{< /see >}}
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="Straza administration" >}}
```sh
strazactl users list
```
{{< /command >}}

{{< see >}}`scout-bot` with `ORIGIN` `scim`.{{< /see >}}

```table
USERNAME     STATUS    ORIGIN  EMAIL              ID
scout-bot    active    scim                       01a15d8e-856f-7d10-9e63-a550763da368
```

The listing is trimmed to the new row.
{{< /cli >}}

## Generate the key


{{< only form="cli" >}}The key is made on the agent's machine, so this step runs in its terminal.{{< /only >}}

Generate the key on the agent's machine, as the agent's user. `straza service`, the Straza background service on that machine, makes the key and keeps the private half, so it never reaches the agent. It runs on Linux, macOS and Windows, as [Headless agents]({{< relref "guides/govern-an-agent/headless-agents.md" >}}) says. The command prints the exact registration line:

{{< command terminal="Terminal 2" purpose="on the agent's machine" >}}
```sh
straza keygen --user scout-bot
```
{{< /command >}}

{{< see >}}The `Public key:` line, which the next step registers.{{< /see >}}

```text
NHI key generated (the Straza background service holds the private half).
Public key: pzXwnoH6Vcc/cs+T/gxsrZ/SGMzU7J7nQSgfKWy5AUg=

An administrator registers it with:
  strazactl users nhi-key set scout-bot pzXwnoH6Vcc/cs+T/gxsrZ/SGMzU7J7nQSgfKWy5AUg=
(or: PUT /v1/admin/users/{id}/nhi-key {"public_key":"pzXwnoH6Vcc/cs+T/gxsrZ/SGMzU7J7nQSgfKWy5AUg="})
Then enroll headless:
  straza enroll --server <strazad-url> --headless --user scout-bot
```

## Register the public key


Register the public half on the identity, as the admin. One key belongs to each identity, so registering again rotates it, and Straza refuses a key for a human identity.

{{< console >}}
{{< clicks "Users" "scout-bot" "Register key" >}}

{{< shot name="agent-key-register" caption="The **Assertion key** section with the public key pasted and **Register key**." >}}

The **Assertion key** section shows only on an AI agent. Paste the key under **Public key, base64, 32 bytes** and press **Register key**.

{{< see >}}The badge **registered** with the key's fingerprint.{{< /see >}}

{{< fails >}}
`Paste the public key. The server refuses anything that is not exactly a 32-byte Ed25519 key.`
: The field was empty. Paste the whole value from the `Public key:` line, with its closing `=`.
{{< /fails >}}
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="Straza administration" >}}
```sh
strazactl users nhi-key set scout-bot pzXwnoH6Vcc/cs+T/gxsrZ/SGMzU7J7nQSgfKWy5AUg=
```
{{< /command >}}

{{< see >}}`key registered for scout-bot`.{{< /see >}}

```text
key registered for scout-bot. The agent can now `straza enroll --headless --user scout-bot`
```

`strazactl` has no read of the key's posture. The admin API answers it and never returns the key itself:

{{< command terminal="Terminal" purpose="admin API" >}}
```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" http://localhost:8420/v1/admin/users/01a15d8e-856f-7d10-9e63-a550763da368/nhi-key
```
{{< /command >}}

```text
{"created":"2026-10-21T08:23:04.034349172Z","fingerprint":"sha256:86f2e918a060dc958b8483ba0248f82acf8e4d21fe658c2cd589f897b4f848a8","registered":true,"user_id":"01a15d8e-856f-7d10-9e63-a550763da368"}
```
{{< /cli >}}

## Assign a role and enroll


Roles arrive the way they do for a person: the identity manager adds the identity to the role's group. The example added `scout-bot` to the application role `scout-tools-readers` with a members PATCH, the request [Generic SCIM]({{< relref "guides/connect-identity/generic-scim.md#read-a-role-and-write-its-membership" >}}) shows. The identity's `groups` then listed the role.

Enrollment needs no person, because the service signs a short-lived token with the key. strazad's discovery document names the issuer that accepts the key as `nhi_issuer`. On the agent's machine, enroll and check what the service holds:

{{< command terminal="Terminal 2" purpose="on the agent's machine" >}}
```sh
straza enroll --server http://localhost:8420 --headless --user scout-bot
```
{{< /command >}}

{{< see >}}`Enrolled headless as scout-bot.`, followed by a sentence that the Straza background service holds this agent's key.{{< /see >}}

```text
Enrolled headless as scout-bot. The Straza background service holds this agent's key and starts each of its sessions.
```

{{< command terminal="Terminal 2" purpose="on the agent's machine" >}}
```sh
straza status
```
{{< /command >}}

```text
service    straza service v1.0.0-1570-gaf326165-dirty (custody user)
server     http://localhost:8420
identity   scout-bot (AI agent, no device, key l4kywJ7bjSeu)
session    none (start a harness session)
audit      nothing waiting to upload
```

No harness has started a session yet, so the session line says so. Once one starts, a session line names the harness window, the session id, the user, how long its token lasts and the state of its kill switch. The identity line says `AI agent, no device`, because the key mints every session token and no device exists. [Headless agents]({{< relref "guides/govern-an-agent/headless-agents.md" >}}) proves a governed call from such an agent.

## Verify


An administrator's view of the identity shows every fact in one record.

{{< console >}}
{{< clicks "Users" "scout-bot" >}}

{{< see >}}**Origin** that starts `your identity manager, over SCIM`, **Agency** `autonomous`, **Sponsor** `alice`, the role `scout-tools-readers`, and the **Assertion key** badge **registered**.{{< /see >}}
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="admin API" >}}
```sh
curl -s -H "Authorization: Bearer $ADMIN_API_TOKEN" http://localhost:8420/v1/admin/users/01a15d8e-856f-7d10-9e63-a550763da368 \
  | jq -c '{username, kind, user_type, origin, sponsor, agency_mode, effective_roles, nhi_key_registered}'
```
{{< /command >}}

```text
{"username":"scout-bot","kind":"nhi","user_type":"agent","origin":"scim","sponsor":"alice","agency_mode":"autonomous","effective_roles":["scout-tools-readers"],"nhi_key_registered":true}
```
{{< /cli >}}

## Undo


Revoking the key is the cut for the credential, and the next enrollment attempt fails at once. It also ends every session of the agent that is running. The service still holds the agent's last ID token, but the server refuses it, because that token names the removed key, so no new session starts with it. Registering a different key ends the running sessions the same way.

{{< console >}}
{{< clicks "Users" "scout-bot" "Revoke key" "Revoke key" >}}

{{< shot name="user-agent-key" caption="A registered key with its fingerprint, **Rotate key** and **Revoke key**." >}}

The question reads `Revoke the assertion key of scout-bot?` and says `The running sessions of scout-bot end now, and it can no longer mint sessions with the client_credentials grant.`
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="Straza administration" >}}
```sh
strazactl users nhi-key unset scout-bot
```
{{< /command >}}

```text
key revoked for scout-bot
```
{{< /cli >}}

A new enrollment on the agent's machine is then refused:

{{< command terminal="Terminal 2" purpose="on the agent's machine" >}}
```sh
straza enroll --server http://localhost:8420 --headless --user scout-bot
```
{{< /command >}}

```text
straza: the identity provider refused the agent's sign-in (invalid_client): client authentication failed. Check the registered key or client, and that the user is an active agent
```


Retiring the identity is the identity manager's act. A SCIM deactivation or delete disables the account, revokes its sessions and removes its per-user grants. An administrator can also soft-delete it with `DELETE /v1/admin/users/{id}`, which answers `{"status":"deleted"}` and removes the row from `strazactl users list`. Neither the console nor `strazactl` has a delete.

## Caveats {.nostep}


Humans are excluded from the key grant on purpose, so a stolen key can never become a password-equivalent login for a person.

In the enterprise profile an agent has a second way to sign in, the identity provider's own client-credentials grant. There the agent has a client id and secret at the identity provider, which the service reads from a file only it can read and never from the agent's environment, as [Headless agents]({{< relref "guides/govern-an-agent/headless-agents.md" >}}) describes. strazad resolves the token's subject to the provisioned identity's `externalId`. An agent with a registered key uses the key, and an agent without one uses the identity provider. A deployment that registers no keys has nothing to configure. An agent that an older Straza client enrolled with a key file or a client secret in its environment still checks in without a proof, which the server accepts. Register a new key from `straza keygen` through the service with `strazactl users nhi-key set`, and rotate every client secret that sat in an agent's environment. [Standalone and enterprise compared]({{< relref "reference/standalone-and-enterprise.md#agent-sign-in" >}}) shows both profiles.

## Next {.nostep}

- [Headless agents]({{< relref "guides/govern-an-agent/headless-agents.md" >}}) enrolls an agent that no identity manager feeds and proves its first governed call.
- [midPoint]({{< relref "guides/connect-identity/midpoint.md" >}}) hands an agent an access package from midPoint.
