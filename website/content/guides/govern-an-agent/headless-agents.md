---
title: Headless agents
description: An AI agent with no person at the keyboard holds a key of its own, enrolls without a browser, and has a destructive command denied by your policy.
pagetype: how-to
weight: 70
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: An Alpine 3.20 container against a standalone server, where every curl and strazactl step ran with the page's admin API token and the assign step ran on a developer role made with strazactl roles create, while the console forms were checked against the console's source and not clicked, and the demo stack's role list was read there and its guardrails reason and wrapper output come from its seed policy and the walk of 2026-09-19, because the demo stack takes no writes
  date: 2026-10-06
applies_to: both
who: You, as the admin
where: The console or a terminal with strazactl, and a terminal on the agent's machine
steps: true
modes: [console, cli]
mode_default: cli
keywords: headless nhi enroll key assertion
---


You give an AI agent that runs with no person at the keyboard, such as a build job or an always-on service, a key of its own. You work as the admin on the server, and in a terminal on the agent's machine. At the end, the agent enrolls without a browser, and your Straza policy has denied it a destructive command.


The agent holds a local Ed25519 key, and you register the matching public key on the agent's identity. At each session start the agent signs a fresh short-lived token with that key and checks in without a device or a browser. The key is the durable credential, so there is no long-lived bearer token to steal.

## Before you start {.nostep}


- The `straza` binary on the agent's machine.
- An admin login to your Straza server, in the console or in `strazactl`.
- A role to assign the agent. The example assigns the seeded `developer` role of the [demo stack]({{< relref "get-started/enterprise-demo-stack.md" >}}) on `http://127.0.0.1:8420`. A standalone server has no `developer` role, and the agent needs none there, because the seeded `standalone-starter` PolicySet governs every identity, one with no roles included.
- For the `curl` calls, an admin API token in `ADMIN_API_TOKEN`, minted with `strazactl api-token create --name docs --scope identity:read,identity:write`. The token belongs to a person and never to the agent, because an AI agent is refused on every admin route, whatever role it holds.

## Create the agent identity


When an identity manager feeds your server, it creates the identity and gives it its roles, as [AI agents as identities]({{< relref "guides/connect-identity/nhi-provisioning.md" >}}) shows. In that case, start at [Generate the agent's key](#generate-the-agents-key). Assigning a role here whose membership the identity manager writes is drift, and the identity manager's next full write of that role's membership removes it.


{{< only form="cli" >}}The console has no way to add a user, because identities arrive over SCIM or through `strazactl`.{{< /only >}}


Otherwise, create an agent user yourself. The `kind` is `nhi`, the same signal the identity manager carries over SCIM. Set `user_type` to `agent` for an AI agent that acts for a person, or to `service` for a technical account with no agency. When the field is left out, the server stores `agent`.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
curl -s -X POST http://127.0.0.1:8420/v1/admin/users \
  -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/json' \
  -d '{"username":"build-bot","kind":"nhi","user_type":"agent","display":"Build bot"}'
```
{{< /command >}}

{{< see >}}The new identity's `id`, which the key step needs.{{< /see >}}

```text
{"id":"01a1130a-fadc-7627-8b54-0a9646ce1d5a","username":"build-bot","status":"active","kind":"nhi"}
```

The response is trimmed here to those four fields.


`strazactl users create-nhi build-bot --type agent` creates the same identity from a terminal. A standalone server has no identity manager feeding it, so one of these two is how its agent identities are made.

## Assign the agent a role


{{< console >}}
On the **Users** screen, open build-bot's sheet, pick the role under **Assign**, press **Assign role**, and confirm.

{{< clicks "Users" "build-bot" "Assign" "developer" "Assign role" "Assign role" >}}

{{< see >}}`build-bot holds developer now.`{{< /see >}}
{{< /console >}}

{{< cli >}}
Log in to `strazactl` as an admin, or put the token in `STRAZA_API_TOKEN`.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl assign developer --user build-bot
```
{{< /command >}}

{{< see >}}`assigned developer to build-bot`{{< /see >}}
{{< /cli >}}

The identity then shows `effective_roles` `["demo-tools-sandbox","developer","midpoint-self-service","views-demo-tools"]`, because `developer` composes the other three. On a standalone server, skip this step, or assign a role you created with `strazactl roles create`.

## Generate the agent's key


{{< only form="cli" >}}The key is made in a terminal on the agent's machine.{{< /only >}}

On the agent's machine, run `straza keygen`. The private half never leaves the machine. The command prints the public half and the exact registration line an administrator runs.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza keygen --user build-bot
```
{{< /command >}}

{{< see >}}The public key, and the `strazactl users nhi-key set` line that registers it.{{< /see >}}

```text
NHI key generated (private half stays in /root/.straza/state/nhi-key.json).
Public key: hQBJfQNxXQUSn4d9TR+Oso8k8NTcgHoGgZv2ufDnKzk=

An administrator registers it with:
  strazactl users nhi-key set build-bot hQBJfQNxXQUSn4d9TR+Oso8k8NTcgHoGgZv2ufDnKzk=
(or: PUT /v1/admin/users/{id}/nhi-key {"public_key":"hQBJfQNxXQUSn4d9TR+Oso8k8NTcgHoGgZv2ufDnKzk="})
Then enroll headless:
  straza enroll --server <strazad-url> --headless --user build-bot
```

## Register the public key


Register the public half on the agent's identity. Each agent has one key, so registering again rotates it. A human identity is refused outright.

{{< console >}}
On build-bot's sheet, the **Assertion key** section says `Cannot start a session until a key is registered.` Press **Register key**, paste the public key under **Public key, base64, 32 bytes**, and press **Register key** again.

{{< clicks "Users" "build-bot" "Register key" "Register key" >}}

{{< shot name="agent-key-register" caption="The **Assertion key** section of build-bot, with the public key pasted and **Register key**." >}}

{{< see >}}`The assertion key of build-bot is registered.` The section then shows `registered` and the key's fingerprint.{{< /see >}}
{{< /console >}}

{{< cli >}}
Run the `strazactl users nhi-key set` line that keygen printed, or call the API with the identity's `id`:

{{< command terminal="Terminal" purpose="administration" >}}
```sh
curl -s -X PUT http://127.0.0.1:8420/v1/admin/users/01a1130a-fadc-7627-8b54-0a9646ce1d5a/nhi-key \
  -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/json' \
  -d '{"public_key":"hQBJfQNxXQUSn4d9TR+Oso8k8NTcgHoGgZv2ufDnKzk="}'
```
{{< /command >}}

{{< see >}}`"status":"set"` and the identity's id.{{< /see >}}

```text
{"status":"set","user_id":"01a1130a-fadc-7627-8b54-0a9646ce1d5a"}
```

A `GET` on the same path reports the posture, `registered: true` with the created time and a `sha256:` fingerprint, and never the key itself.
{{< /cli >}}

## Enroll the agent


{{< only form="cli" >}}Enrolling runs on the agent's machine.{{< /only >}}

Enroll headless on the agent's machine. There is no browser and no device. The command proves the key works by minting a token, pins the keys that verify policy snapshots, and reports that sessions are deviceless.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza enroll --server http://127.0.0.1:8420 --headless --user build-bot
```
{{< /command >}}

{{< see >}}`Enrolled headless as build-bot`, followed by a note that sessions are deviceless.{{< /see >}}

```text
Enrolled headless as build-bot (nhi-key; sessions are deviceless, and the local credential mints each session's token).
```

## Prove a governed call


{{< only form="cli" >}}The proof runs on the agent's machine.{{< /only >}}

Run one safe and one destructive command through the wrapper. The session starts from the key with no interaction.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza exec -- date
straza exec -- rm -rf /tmp/probe
```
{{< /command >}}

{{< see >}}The date, then the policy's reason for the deny. The `rm -rf` line exits 2.{{< /see >}}

```text
Sat Sep 19 10:33:01 UTC 2026
Straza: destructive command denied by the agent guardrails
```


Against a standalone server, the denied line carries the reason of the seeded `standalone-starter` PolicySet, `Straza starter policy: recursive force-delete is denied. Delete files individually, or an admin can edit the standalone-starter PolicySet.` On the demo stack, the always-on `agent-sam` container runs as the AI agent `sam-sre-agent` and enrolls the same way at boot. Its logs show the signed assertion accepted, a deviceless check-in, and a destructive command denied with the same reason.


Check the agent's setup with the doctor.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza doctor
```
{{< /command >}}

{{< see >}}An identity line that reads `build-bot: headless AI agent enrollment (nhi-key lane), sessions are deviceless`.{{< /see >}}

That line confirms there is no device credential to check, because the key mints each token. `straza status` shows the live session, its roles and the snapshot once a call has run.

## Retire the agent {.nostep}


To retire the agent, soft-delete the user with `DELETE /v1/admin/users/{id}`. Neither the console nor `strazactl` has this. The delete revokes its sessions, ends its assignments, and removes its assertion key and per-user OAuth grants, with audit records for the changes.

To stop new sessions and keep the identity, revoke only the key.

{{< console >}}
{{< clicks "Users" "build-bot" "Revoke key" "Revoke key" >}}

{{< shot name="user-agent-key" caption="A registered key with its fingerprint, **Rotate key** and **Revoke key**." >}}

The dialog says `build-bot can no longer mint sessions with the client_credentials grant.`
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl users nhi-key unset build-bot
```
{{< /command >}}

The API form is `DELETE /v1/admin/users/{id}/nhi-key`.
{{< /cli >}}

Revoking the key does not end the sessions that already run. To stop current access and keep the identity, revoke the sessions or the user, as the [Kill-switch runbook]({{< relref "guides/operate/kill-switch-runbook.md" >}}) shows.

## Protect the key {.nostep}


The key file is the agent's durable secret. Protect it as you would a password, and rotate it by registering a new public key. Humans are excluded from this grant on purpose, so a stolen key can never become a password-equivalent human login.

An enterprise server offers two ways to enroll an agent. One is a per-agent key at the built-in issuer, as on this page, which needs no identity-provider step. The other is the identity provider's own client-credentials grant, with a client id and secret, and then that secret is the durable credential. A standalone server has only the key, because the built-in issuer is the only issuer there.

## Next {.nostep}


- [AI agents as identities]({{< relref "guides/connect-identity/nhi-provisioning.md" >}}) provisions agents from your identity manager.
- [Hookless processes]({{< relref "guides/govern-an-agent/hookless-processes.md" >}}) runs the agent inside the sandbox image, where the wrapper is the only way to run a process.
- [Who may draft and publish]({{< relref "guides/changes/who-may-draft.md" >}}) shows how an agent proposes a config change. It uses the drafting tools of the built-in `straza` app, which the gateway lists to holders of the `straza-draft-config` role, and a person publishes the draft.
- [Identities and roles]({{< relref "concepts/identities.md" >}}) explains the identity model behind people and agents.
