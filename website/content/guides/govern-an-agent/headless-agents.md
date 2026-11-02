---
title: Headless agents
description: An AI agent with no person at the keyboard holds a key of its own, enrolls without a browser, and has a destructive command denied by your policy.
pagetype: how-to
weight: 70
draft: false
tested:
  version: v1.0.0-1570-gaf326165-dirty
  platform: An Alpine 3.20 container against a standalone server, both built from a development working tree before its commit, where straza service ran with STRAZA_HOME in the agent's home directory, every curl and strazactl step ran with the page's admin API token, the assign step ran on a developer role made with strazactl roles create, and keygen, the headless enrollment, both wrapper calls and doctor ran through that service. The key revoke was run on 2026-10-06 at v1.1.0-117-g106081a8. The sentences that say a key change ends the agent's sessions and that the server refuses an ID token issued for an old key were written from the code on 2026-10-27 and not walked. The console forms were checked against the console's source and not clicked, and the demo stack's role list was read there and its guardrails reason and wrapper output come from its seed policy and the walk of 2026-09-19, because the demo stack takes no writes. The sentences on a fresh enterprise server and the section that hands an enrolled OS account to an agent were written from the code on 2026-11-01 and not walked
  date: 2026-10-21
applies_to: both
who: You, as the admin
where: The console or a terminal with strazactl, and a terminal on the agent's machine
steps: true
modes: [console, cli]
mode_default: cli
keywords: headless nhi enroll key assertion straza service client secret
---


You give an AI agent that runs with no person at the keyboard, such as a build job or an always-on service, a key of its own. You work as the admin on the server, and in a terminal on the agent's machine. At the end, the agent enrolls without a browser, and your Straza policy has denied it a destructive command.


`straza service`, the Straza background service on the agent's machine, holds the agent's Ed25519 key, and you register the matching public key on the agent's identity. At each session start the service checks in without a device or a browser, with a fresh proof from that key, so the session token is bound to it. The ID token that check-in carries comes from an assertion the key signs, which the service signs again once the ID token it holds runs out. That ID token names the key it was issued for, so the server refuses it once you replace or remove the key, and refuses a check-in proof from any other key. The agent's own files and environment hold no key and no token, so there is nothing long-lived for the agent to copy.

## Before you start {.nostep}


- The `straza` binary on the agent's machine, a Linux, macOS or Windows machine, with `straza service` running. Run the service under an OS account other than the agent's, as the [sandbox]({{< relref "guides/govern-an-agent/hookless-processes.md" >}}) and the demo stack do with a service container of their own, so the agent cannot read its key. The `straza` package for Linux does that too, because it runs the service under the straza-service OS account, as [Install]({{< relref "get-started/install.md#install-the-package-on-linux" >}}) shows. On Windows the service always runs under its own OS account, as the Windows service that [Install]({{< relref "get-started/install.md#install-the-service-on-windows" >}}) sets up, and on a Mac the Straza package for macOS runs it under its own OS account, as [Install]({{< relref "get-started/install.md#install-the-service-on-macos" >}}) shows. On a Linux or macOS machine with no system service and no managed install, a service the agent's own user starts with `STRAZA_HOME` set works too, and then the key sits where the agent can read it.
- An admin's console sign-in, or `strazactl` on an admin API token in `STRAZA_API_TOKEN`, minted in the console as [Mint an admin API token]({{< relref "guides/console/settings.md#mint-an-admin-api-token" >}}) shows.
- A role to assign the agent. The example assigns the role `developer`, which the [demo stack]({{< relref "get-started/enterprise-demo-stack.md" >}}) seeds on `http://127.0.0.1:8420`. A fresh enterprise server holds no such role and no PolicySet, and it denies every local tool that no rule allows, so an admin first creates the roles and the PolicySet of [Give agents their local tools]({{< relref "guides/operate/enterprise-shape.md#give-agents-their-local-tools" >}}). A standalone server has no `developer` role, and the agent needs none there, because the seeded `standalone-starter` PolicySet governs every identity, one with no roles included.
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
{"id":"01a15d8e-8320-768e-81ea-8a38ddf6fa7d","username":"build-bot","status":"active","kind":"nhi"}
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
Put an admin API token that holds `identity:read` and `identity:write` in `STRAZA_API_TOKEN`.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl assign developer --user build-bot
```
{{< /command >}}

{{< see >}}`assigned developer to build-bot`{{< /see >}}
{{< /cli >}}

The identity then shows `effective_roles` `["demo-tools-sandbox","developer","midpoint-self-service","views-demo-tools"]`, because `developer` composes the other three. With the starter of [Give agents their local tools]({{< relref "guides/operate/enterprise-shape.md#give-agents-their-local-tools" >}}) on an enterprise server, they are `developer` and the `local-tools` it composes. On a standalone server, skip this step, or assign a role you created with `strazactl roles create`.

## Generate the agent's key


{{< only form="cli" >}}The key is made in a terminal on the agent's machine.{{< /only >}}

On the agent's machine, run `straza keygen` as the agent's user. The service makes the key and keeps the private half in its state directory, so the private half never reaches the agent. The command prints the public half and the exact registration line an administrator runs. A second keygen for the same user is refused until you add `--force`, because a new key stops the registered one from working.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza keygen --user build-bot
```
{{< /command >}}

{{< see >}}The public key, and the `strazactl users nhi-key set` line that registers it.{{< /see >}}

```text
NHI key generated (the Straza background service holds the private half).
Public key: huw3t4+Vrf69fExt1wBKWkQB9bZMD+eSAVsCs03+cTc=

An administrator registers it with:
  strazactl users nhi-key set build-bot huw3t4+Vrf69fExt1wBKWkQB9bZMD+eSAVsCs03+cTc=
(or: PUT /v1/admin/users/{id}/nhi-key {"public_key":"huw3t4+Vrf69fExt1wBKWkQB9bZMD+eSAVsCs03+cTc="})
Then enroll headless:
  straza enroll --server <strazad-url> --headless --user build-bot
```


{{< fails >}}
``A key for you already exists on this machine. Run `straza keygen --force` to replace it, and then have an admin register the new public key, because the old one stops working.``
: The agent's user already has a key with the service. Keep it, or replace it with `--force` and register the new public half.

``This OS account holds a person's enrollment on this machine, so an agent's key or enrollment cannot replace it. Only `straza enroll` replaces a person's machine key. Run the agent under its own OS account.``
: A person enrolled this OS account. If an agent's key could replace that enrollment, an agent that runs as the person could unenroll the person's machine. So neither `straza keygen`, with or without `--force`, nor `straza enroll --headless` runs here, and no command removes a person's enrollment. Create an OS account for the agent and run its commands there, or have an administrator hand this account to the agent, as [Hand an enrolled OS account to an agent](#hand-an-enrolled-os-account-to-an-agent) shows.
{{< /fails >}}

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
curl -s -X PUT http://127.0.0.1:8420/v1/admin/users/01a15d8e-8320-768e-81ea-8a38ddf6fa7d/nhi-key \
  -H "Authorization: Bearer $ADMIN_API_TOKEN" -H 'Content-Type: application/json' \
  -d '{"public_key":"huw3t4+Vrf69fExt1wBKWkQB9bZMD+eSAVsCs03+cTc="}'
```
{{< /command >}}

{{< see >}}`"status":"set"` and the identity's id.{{< /see >}}

```text
{"status":"set","user_id":"01a15d8e-8320-768e-81ea-8a38ddf6fa7d"}
```

A `GET` on the same path reports the posture, `registered: true` with the created time and a `sha256:` fingerprint, and never the key itself.
{{< /cli >}}

## Enroll the agent


{{< only form="cli" >}}Enrolling runs on the agent's machine.{{< /only >}}

Enroll headless on the agent's machine, as the agent's user. There is no browser and no device. The service proves the key works by minting a token, pins the keys that verify policy snapshots, and keeps the enrollment beside the key.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza enroll --server http://127.0.0.1:8420 --headless --user build-bot
```
{{< /command >}}

{{< see >}}`Enrolled headless as build-bot.`, followed by a sentence that the Straza background service holds this agent's key and starts each of its sessions.{{< /see >}}

```text
Enrolled headless as build-bot. The Straza background service holds this agent's key and starts each of its sessions.
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


Against a standalone server, the denied line carries the reason of the seeded `standalone-starter` PolicySet, `Straza starter policy: recursive force-delete is denied. Delete files individually, or an admin can edit the standalone-starter PolicySet.` On the demo stack, the always-on `agent-sam` container runs as the AI agent `sam-sre-agent` and enrolls the same way at boot, through a service container of its own. Its logs show the signed assertion accepted, a session bound to the service's key, and a destructive command denied with the same reason.


Check the agent's setup with the doctor.

{{< command terminal="Terminal" purpose="on the agent's machine" >}}
```sh
straza doctor
```
{{< /command >}}

{{< see >}}An `enrollment` line that names `build-bot (AI agent, no device)` with the server and the start of the key's thumbprint.{{< /see >}}

An agent has no device, because the key mints each token. `straza status` shows each live session once a call has run.

## Hand an enrolled OS account to an agent {.nostep}


A person's enrollment stays with their OS account, and no command removes it. To run an agent in an OS account that a person enrolled, such as a build account someone once enrolled by hand, an administrator of the machine clears that account's folder in the state directory of `straza service`:

1. In that OS account, run `straza status` and wait until its `audit` line reads `nothing waiting to upload`, because the folder holds the audit records the service has not uploaded yet.
2. Revoke the person's device on the server with `strazactl devices revoke <username> <device-id>`. The `identity` line of `straza status` names the device, and `strazactl devices list <username>` lists them all.
3. Stop the service, delete the account's folder, and start the service again, with the commands for your system below.

On Linux, `id -u build` prints the uid of the OS account, here `build` with the uid 1001. Stop the socket as well, because a connection to it starts the service again.

{{< command terminal="Terminal" purpose="as an administrator" >}}
```sh
sudo systemctl stop straza-service.socket straza-service.service
sudo rm -r /var/lib/straza-service/u/1001
sudo systemctl start straza-service.socket
```
{{< /command >}}

`id -u build` prints the uid on a Mac too, here 502.

{{< command terminal="Terminal" purpose="as an administrator" >}}
```sh
sudo launchctl bootout system/ai.straza.service
sudo rm -r /var/db/straza-service/u/502
sudo launchctl bootstrap system /Library/LaunchDaemons/ai.straza.service.plist
```
{{< /command >}}

On Windows the folder is named for the SID of the Windows account. In an elevated PowerShell, print it first, with `DOMAIN\build` in place of `build` for a domain account:

{{< command terminal="PowerShell" purpose="as an administrator" >}}
```powershell
(New-Object System.Security.Principal.NTAccount('build')).Translate([System.Security.Principal.SecurityIdentifier]).Value
```
{{< /command >}}

Then remove the folder of that SID, here `S-1-5-21-1004336348-1177238915-682003330-1001`:

{{< command terminal="PowerShell" purpose="as an administrator" >}}
```powershell
Stop-Service straza-service
Remove-Item -Recurse C:\ProgramData\straza-service\u\S-1-5-21-1004336348-1177238915-682003330-1001
Start-Service straza-service
```
{{< /command >}}

Check the uid or the SID before you delete, because each folder there holds one account's enrollment. When the person started the service themselves with `STRAZA_HOME`, stop that service and delete `$STRAZA_HOME/service/u/<uid>` instead. The account then holds no enrollment, and `straza status` in it reports it as not enrolled. Generate the agent's key there and enroll it, as the steps above show.

## Retire the agent {.nostep}


To retire the agent, soft-delete the user with `DELETE /v1/admin/users/{id}`. Neither the console nor `strazactl` has this. The delete revokes its sessions, ends its assignments, and removes its assertion key and per-user OAuth grants, with audit records for the changes.

To end the agent's sessions and stop new ones, and keep the identity, revoke only the key.

{{< console >}}
{{< clicks "Users" "build-bot" "Revoke key" "Revoke key" >}}

{{< shot name="user-agent-key" caption="A registered key with its fingerprint, **Rotate key** and **Revoke key**." >}}

The dialog says `The running sessions of build-bot end now, and it can no longer mint sessions with the client_credentials grant.`
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl users nhi-key unset build-bot
```
{{< /command >}}

The API form is `DELETE /v1/admin/users/{id}/nhi-key`.
{{< /cli >}}

Revoking the key ends the sessions that already run, with one `session.end` record each. The service still holds the agent's last ID token, but the server refuses it, because the token names the removed key, and the service then cannot get a new one. Registering a different key ends the running sessions the same way. After `straza keygen --force` on the agent's machine and the registration of its new public key, run `straza enroll --headless` there again, and the agent starts new sessions with the new key. To stop the agent's access on every lane at once, lock the user, as the [Kill-switch runbook]({{< relref "guides/operate/kill-switch-runbook.md" >}}) shows.

## Protect the key {.nostep}


The key in the service's state directory is the agent's durable secret. Protect that directory and its backups as you would a password, and rotate the key with `straza keygen --force` and a new registration. Humans are excluded from this grant on purpose, so a stolen key can never become a password-equivalent human login.

An enterprise server offers two ways to enroll an agent. One is a per-agent key at the built-in issuer, as on this page, which needs no identity-provider step. The other is the identity provider's own client-credentials grant, with a client id and secret, and then that secret is the durable credential. The service reads it from a file that only the service can read, `agent-<uid>.json` with `client_id` and `client_secret`, named by the agent's uid. That file sits in the directory systemd names in `CREDENTIALS_DIRECTORY`, or else in `credentials/` under the service's state directory. The service never reads it from the agent's environment, and it makes a key of its own for the proofs that bind each session. A standalone server has only the key, because the built-in issuer is the only issuer there.


On a machine with the `straza` package, an administrator passes that file to the service with a systemd drop-in. Put the JSON in a file only root can read, here `/etc/credstore/agent-1001.json` for an agent whose OS account has the uid 1001, which `id -u` prints in the agent's shell. Then run `sudo systemctl edit straza-service`, add the lines below, and run `sudo systemctl restart straza-service`. systemd copies the file into the service's credentials directory each time it starts the service, where the agent's account cannot read it.

```ini
[Service]
LoadCredential=agent-1001.json:/etc/credstore/agent-1001.json
```


On Windows an administrator puts the file in `C:\ProgramData\straza-service\credentials`, named for the SID of the agent's Windows account, such as `agent-S-1-5-21-1004336348-1177238915-682003330-1001.json`. `whoami /user` in the agent's own PowerShell prints that SID. Files there take the folder's access rules, so only the service, SYSTEM and Administrators can read them.


An agent that an older Straza client enrolled kept its key in a file of its own home or its client secret in its environment. A standalone server still accepts that agent's check-in without a proof. An enterprise server refuses it with a sentence that starts `Straza refused this session because the user` and says to enroll the agent with a current `straza service`. After you update Straza on the agent's machine, run `straza keygen` through the service and have an admin register the new public key with `strazactl users nhi-key set`, which replaces the old one. Rotate every identity provider client secret that sat in an agent's environment.

## Next {.nostep}


- [AI agents as identities]({{< relref "guides/connect-identity/nhi-provisioning.md" >}}) provisions agents from your identity manager.
- [Hookless processes]({{< relref "guides/govern-an-agent/hookless-processes.md" >}}) runs the agent inside the sandbox image, where the wrapper is the only way to run a process.
- [Who may draft and publish]({{< relref "guides/changes/who-may-draft.md" >}}) shows how an agent proposes a config change. It uses the drafting tools of the built-in `straza` app, which the gateway lists to holders of the `straza-draft-config` role, and a person publishes the draft.
- [Identities and roles]({{< relref "concepts/identities.md" >}}) explains the identity model behind people and agents.
