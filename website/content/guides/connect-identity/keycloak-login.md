---
title: Keycloak login
description: strazad trusts your Keycloak realm, people sign in to Straza with their own Keycloak identity, and Straza never holds a password.
pagetype: how-to
weight: 30
draft: false
tested:
  version: v1.1.0
  platform: A Linux container on the demo stack's host, with Keycloak 26.0 and the shipped realm import. The device flow ran from the container, and the browser step was driven headless as alice
  date: 2026-09-28
applies_to: enterprise
who: You, as the Keycloak admin and the Straza admin
where: The Keycloak admin console, strazad's configuration file, and a terminal with strazactl
steps: true
modes: [console, cli]
mode_default: cli
keywords: keycloak oidc login provider
---


You make Keycloak the login provider of an enterprise Straza, as the admin of both. You work in the Keycloak admin console, in strazad's configuration file and in a terminal. At the end strazad trusts your realm, and you have signed in with your own Keycloak identity and seen the device that login enrolled.


In the enterprise profile a person signs in at Keycloak, and strazad verifies the ID token Keycloak minted against the realm's published keys. Straza holds no password, so passwords, second factors and session policy belong to Keycloak. Who exists still belongs to your identity manager, because a verified token opens a door only for an account that provisioning created. Keycloak is the login factor and never the source of record.

## Before you start {.nostep}

- Keycloak administration for one realm.
- A shell with `strazactl`, and access to strazad's configuration.

The examples ran against the [demo stack]({{< relref "get-started/enterprise-demo-stack.md" >}}). Its realm `straza` at `http://localhost:8480` is imported from a file in the repository, so every setting below is the one that file carries.

## Create the client


In your realm, create a client with the client id `straza` and these settings. The first four rows are what the demo stack's realm ships.

| Setting | Value | Why |
|---|---|---|
| Client authentication | off | the CLI device flow has no secret to keep |
| Standard flow | off | no browser redirect flow is used |
| OAuth 2.0 Device Authorization Grant | on | the flow `strazactl login` and `straza enroll` speak |
| Web origins | your strazad public URL | the console runs the same flow from the browser |
| Direct access grants | off | no Straza login uses a password grant |

The demo stack's realm turns direct access grants on for its non-interactive seeder alone, and Straza itself never uses that grant. Keycloak signs with RS256 by default, and strazad accepts RS256, ES256 and EdDSA.


{{< fails >}}
`identity provider ... does not advertise a device_authorization_endpoint. Enable the OAuth 2.0 device authorization grant for client "straza"`
: `strazactl login` stops with this sentence when the device grant is off. Turn on OAuth 2.0 Device Authorization Grant on the client.
{{< /fails >}}

## Point strazad at the realm


The `oidc` block of strazad's configuration names the issuer and the client id. The demo stack sets these values on the strazad container:

```yaml
oidc:
  issuer: http://localhost:8480/realms/straza
  discoveryUrl: http://keycloak:8080/realms/straza/.well-known/openid-configuration
  clientId: straza
  jitProvision: false
```

`STRAZA_OIDC_ISSUER` and `STRAZA_OIDC_CLIENT_ID` are the environment forms of the first and third keys.

{{< only form="cli" >}}The console's Settings, tab Configuration, shows **Identity provider** and **Sign-in creates users** and changes neither. You set them in the file.{{< /only >}}


`jitProvision` stays `false` in enterprise, because provisioning decides who exists. A person Keycloak knows and the identity manager has not provisioned is refused at login with the reason "identity not provisioned". [Standalone and enterprise compared]({{< relref "reference/standalone-and-enterprise.md#new-users" >}}) shows the default of each profile.


strazad runs OIDC discovery against the issuer at boot and refuses to start when the issuer is unreachable. Refusing to start is safer than serving logins it cannot verify.


Set `oidc.discoveryUrl`, or `STRAZA_OIDC_DISCOVERY_URL`, when strazad cannot reach the issuer's own address. The demo stack needs it, because strazad runs in a container and reaches Keycloak by the internal name `keycloak`. The document fetched there must name `oidc.issuer` exactly, byte for byte, or strazad refuses to boot, and every token must still carry that issuer. Left empty, the default, strazad fetches the document at the issuer.


A fresh enterprise deployment has no administrator. `oidc.bootstrapAdmin` names one Keycloak username whose first verified login is provisioned despite `jitProvision: false` and assigned `straza-admin`. It works only while no `straza-admin` assignment exists, so the setting goes inert as soon as your identity manager manages that role. Remove the line after the bootstrap. The demo stack points it at a transient `seed-bootstrap` user, which its seeder locks once the admin the identity manager assigned has landed.

## How a token finds its account {.nostep}


strazad maps a verified ID token to a local account in this order:

1. The token's `sub` is matched against the account's `externalId`.
2. If nothing matches, `preferred_username` is matched against the account's `userName`. A match fills in `externalId` with this `sub`, which links the account once. A username already bound to a different subject is refused with "username ... is bound to another identity" and is never taken over.
3. If nothing matches and JIT is off, the login is refused.

Keep Keycloak usernames equal to the `userName` your identity manager provisions, and midPoint keeps them equal when it masters both sides. If your identity manager sends `externalId`, it must be the Keycloak subject. Otherwise leave it out, and the first login links the account.

## Sign in


Clients never hardcode the identity provider. They ask strazad where to sign in, read the realm's discovery document, and run the device flow.

{{< console >}}
Open your strazad address followed by `/console/` and press **Sign in with a code**. Press **Open the sign-in page**, sign in at Keycloak as yourself, and confirm the code. The console admits a person who holds an admin role, and anyone else lands on the self-service page.

{{< shot name="signin-code" caption="Press **Open the sign-in page**. Your code is different." >}}

A console sign-in opens a session in the browser tab and enrolls no device. The device the next step reads comes from `strazactl login` on a workstation.
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="your workstation" >}}
```sh
curl -s http://localhost:8420/.well-known/straza/idp.json
```
{{< /command >}}

{{< see >}}Your realm as `issuer` and `straza` as `client_id`.{{< /see >}}

```text
{"client_id":"straza","issuer":"http://localhost:8480/realms/straza","nhi_issuer":"http://localhost:8420"}
```

{{< command terminal="Terminal" purpose="your workstation" >}}
```sh
strazactl login --server http://localhost:8420
```
{{< /command >}}

The command prints an address and a code, then waits. Open the address, sign in at Keycloak as yourself and confirm the code.

{{< see >}}`Logged in as alice.` with your own username.{{< /see >}}

```text
Signing in at your identity provider: http://localhost:8480/realms/straza
Open http://localhost:8480/realms/straza/device?user_code=XEFW
and confirm code XEFW
Logged in as alice.
note: this login can change Straza's configuration. Keep it away from coding agents. Automation uses an admin API token in STRAZA_API_TOKEN, and an agent proposes config changes through the built-in straza MCP server.
```

The one-time code is cut after its first four characters. The example signed in as `alice`. On success strazad checks the token in, enrolls this workstation as a device, and stores a device credential, so later sessions start without a browser. [Lifetimes and timeouts]({{< relref "reference/lifetimes-and-timeouts.md" >}}) says how long that credential lasts.
{{< /cli >}}


{{< fails >}}
`login timed out (device code expired)`
: The code was not confirmed in time. Run `strazactl login` again and confirm the new code.

`Cookie not found`
: Keycloak shows this when the browser lost its login cookie mid-flow. On the demo stack Keycloak binds the cookie to the name `localhost`, so a browser that opens the address as `127.0.0.1` loses it. Open the address exactly as printed.
{{< /fails >}}

## Verify


The login left a device on your account.

{{< console >}}
{{< clicks "Users" "alice" >}}

{{< shot name="user-devices" caption="The **Devices** section of a user's sheet, here with a device named alice-laptop and the badge `active`." >}}

{{< see >}}Under **Devices**, a row named `strazactl@` and the host name of your workstation, with the badge `active`.{{< /see >}}

The section lists the machines that enrolled with `strazactl login` or `straza enroll`. It reads `No device is enrolled.` until one of them has.
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="your workstation" >}}
```sh
strazactl devices list alice
```
{{< /command >}}

{{< see >}}A device named `strazactl@` and the host name of your workstation.{{< /see >}}

```table
ID                                    NAME                      PLATFORM  STATUS  ENROLLED
01a0e994-0de0-7513-84eb-3eac841ea38f  strazactl@workstation     linux     active  2026-09-28 19:53:09
```

The listing is trimmed to the new device, whose host name was `workstation` here. Keep its ID for the next step.
{{< /cli >}}

## Undo


Logging out ends the session, and the device credential stays valid until you revoke the device.

{{< console >}}
**Sign out** in the header menu ends the console's tab session.

{{< clicks "Users" "alice" "Revoke" "Revoke device" >}}

Press **Revoke** on the device's row under **Devices**, then **Revoke device**. Getting the device back means enrolling again with a fresh sign-in.
{{< /console >}}

{{< cli >}}
`strazactl logout` revokes the current session and deletes the local credentials file. Then revoke the device:

{{< command terminal="Terminal" purpose="your workstation" >}}
```sh
strazactl devices revoke alice 01a0e994-0de0-7513-84eb-3eac841ea38f
```
{{< /command >}}

{{< see >}}`its device credential is dead`.{{< /see >}}

```text
revoked device 01a0e994-0de0-7513-84eb-3eac841ea38f of alice: its device credential is dead; re-enrolling needs a fresh login
```
{{< /cli >}}

## Caveats {.nostep}


Disabling a person in Keycloak stops new logins and enrollments and nothing more. strazad verifies the Keycloak token at login only, and it verifies its own session tokens against its own keys. The identity manager's SCIM deactivation cuts running Straza sessions and device credentials. A leaver flow therefore triggers both: Keycloak for the authentication cut, and SCIM for the authorization cut.


While Keycloak is down, nobody signs in through it. The break-glass admin still signs in on Straza's own page, Emergency sign-in to Straza, and `strazactl login --break-glass` opens that page instead of your identity provider. [Standalone and enterprise compared]({{< relref "reference/standalone-and-enterprise.md#break-glass" >}}) shows the break-glass admin in both profiles.


The demo stack's issuer is `http://localhost:8480/realms/straza`, the address your browser and the command-line tools use. Containers cannot reach that address, so strazad fetches the discovery document at `keycloak:8080` through `oidc.discoveryUrl`. Keycloak serves the token and key endpoints of that document on the internal address.

## Next {.nostep}

- [midPoint]({{< relref "guides/connect-identity/midpoint.md" >}}) provisions the accounts that Keycloak logins then sign in to.
- [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) signs a workstation in for an agent with `straza enroll`.
- [Identities and roles]({{< relref "concepts/identities.md" >}}) explains who exists and where each fact comes from.
