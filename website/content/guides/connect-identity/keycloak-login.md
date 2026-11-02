---
title: Keycloak login
description: strazad trusts your Keycloak realm, people sign in to Straza with their own Keycloak identity, and Straza never holds a password.
pagetype: how-to
weight: 30
draft: false
tested:
  version: v1.1.0
  platform: A Linux container on the demo stack's host, with Keycloak 26.0 and the shipped realm import. The device flow ran from the container, and the browser step was driven headless as alice. The client settings for the browser sign-in and its steps were written on 2026-10-22 from the code and from the shipped realm and were not walked. The console's browser sign-in, with its redirect address and web origin, was written from the code on 2026-10-22 and not walked
  date: 2026-09-28
applies_to: enterprise
who: You, as the Keycloak admin and the Straza admin
where: The Keycloak admin console, strazad's configuration file, and a terminal with strazactl
steps: true
modes: [console, cli]
mode_default: cli
keywords: keycloak oidc login provider
---


You make Keycloak the login provider of an enterprise Straza, as the admin of both. You work in the Keycloak admin console, in strazad's configuration file and in a terminal. At the end strazad trusts your realm, you have signed in to the console with your own Keycloak identity, and strazactl reads your account on an admin API token you minted there.


In the enterprise profile a person signs in at Keycloak, and strazad verifies the ID token Keycloak minted against the realm's published keys. Straza holds no password, so passwords, second factors and session policy belong to Keycloak. Who exists still belongs to your identity manager, because a verified token opens a door only for an account that provisioning created. Keycloak is the login factor and never the source of record.

## Before you start {.nostep}

- Keycloak administration for one realm.
- A shell with `strazactl`, and access to strazad's configuration.

The examples ran against the [demo stack]({{< relref "get-started/enterprise-demo-stack.md" >}}). Its realm `straza` at `http://localhost:8480` is imported from a file in the repository, so every setting below is the one that file carries.

## Create the client


In your realm, create a client with the client id `straza` and these settings. Every row but the last is what the demo stack's realm ships.

| Setting | Value | Why |
|---|---|---|
| Client authentication | off | `straza enroll` runs on people's machines and the console in their browsers, and neither has a secret to keep |
| Standard flow | on | the browser sign-in of `straza enroll` and of the console |
| Valid redirect URIs | `http://127.0.0.1/callback` | the browser hands the answer to a port on the person's own machine. Registered without a port, the redirect matches any port in Keycloak 25 and 26 |
| Valid redirect URIs | the address people open the console at, normally your strazad public URL, followed by `/console/signin`, such as `https://straza.example.com/console/signin` | Keycloak sends the console's and the self-service page's sign-in back to this address, which the browser builds from the origin it opened, so add one for each address people open the console at |
| Proof Key for Code Exchange Code Challenge Method | S256 | Straza sends an S256 challenge with every browser sign-in |
| OAuth 2.0 Device Authorization Grant | on | the sign-in with a code that `straza enroll --use-device-code` picks |
| Web origins | the origin people open the console at, normally your strazad public URL, such as `https://straza.example.com` | the console redeems its sign-in at Keycloak from the browser |
| Direct access grants | off | no Straza login uses a password grant |

On the demo stack the console addresses are `http://localhost:8420/console/signin` and `http://127.0.0.1:8420/console/signin`, with both origins under Web origins. The demo stack's realm turns direct access grants on for its non-interactive seeder alone, and Straza itself never uses that grant. Keycloak signs with RS256 by default, and strazad accepts RS256, ES256 and EdDSA. Keycloak puts `auth_time` in its ID tokens by default, and Straza needs that claim to accept a browser sign-in for a machine. No fixed port is needed here, so leave `oidc.loopbackPort` at 0.


{{< fails >}}
`identity provider ... does not advertise a device_authorization_endpoint. Enable the OAuth 2.0 device authorization grant for client "straza"`
: `straza enroll --use-device-code` stops with this sentence when the device grant is off. Turn on OAuth 2.0 Device Authorization Grant on the client.

A sentence that starts `Your identity provider refused the sign-in and said:`
: Keycloak refused the browser sign-in and sent its reason, which the sentence quotes. When Standard flow is off, turn it on.

Keycloak shows an error about `redirect_uri` in the browser
: The client has no loopback redirect, so Keycloak never sends the answer back and the command gives up after 5 minutes. Add `http://127.0.0.1/callback` under Valid redirect URIs.
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


Clients never hardcode the identity provider. They ask strazad where to sign in, read the realm's discovery document, and open the browser at the realm's sign-in page.

{{< console >}}
Open your strazad address followed by `/console/` and press **Sign in**. The line above the button names the realm's host, such as `You sign in at your identity provider, localhost:8480, and come back to this page.` The tab goes to Keycloak. Sign in as yourself, and Keycloak sends the tab back to the console, which says `Signing you in`. When the browser is still signed in at Keycloak, it can send you straight back without asking for your password. The console admits a person who holds an admin role, and anyone else lands on the self-service page.

{{< shot name="signin" caption="The console's sign-in card, taken on a standalone server, where the line names the server's own sign-in page." >}}

A console sign-in opens a session in the browser tab and enrolls no device.
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="your workstation" >}}
```sh
curl -s http://localhost:8420/.well-known/straza/idp.json
```
{{< /command >}}

{{< see >}}Your realm as `issuer`, `straza` as `client_id`, and `browser` as `min_sign_in`, the lowest sign-in the server accepts for a person's machine.{{< /see >}}

strazactl has no sign-in of its own. It runs on an admin API token that a person mints in the console, so sign in at `http://localhost:8420/console/` as the console form of this step shows, then open the mint sheet:

{{< clicks "Settings" "API tokens" "New API token" >}}

Pick `expires in 30 days`, then **Custom** under **The job**, and set the `identity` area to read, which the next step needs. [Mint an admin API token]({{< relref "guides/console/settings.md#mint-an-admin-api-token" >}}) walks the sheet. Copy the token, which is shown once, and hand it to strazactl in your terminal:

{{< command terminal="Terminal" purpose="your workstation" >}}
```sh
export STRAZA_SERVER=http://localhost:8420
read -rs STRAZA_API_TOKEN && export STRAZA_API_TOKEN
```
{{< /command >}}

`read -rs` reads the token you paste without showing it and keeps it out of the shell history. Never put the token where an agent runs as you, such as a shell profile or a file an agent can read. A token is a bearer credential, so whoever holds it acts with its scope until it expires or is revoked.
{{< /cli >}}


{{< fails >}}
`Cookie not found`
: Keycloak shows this when the browser lost its login cookie mid-flow. On the demo stack Keycloak binds the cookie to the name `localhost`, so a browser that opens the address as `127.0.0.1` loses it. Open the address exactly as printed.

`The sign-in did not come back from your identity provider. If it showed an error about a redirect address, your administrator must add https://straza.example.com/console/signin to Straza's registration there. Press Sign in to try again.`
: The console says this, with your server's address, after Keycloak refused its redirect address. Add that address to the client's Valid redirect URIs, as the client step shows.

A sentence that starts `Your identity provider at` and goes on `did not let this page finish the sign-in.`
: The browser could not redeem the sign-in at Keycloak, because the client's Web origins lack the console's origin. Add the origin people open the console at under Web origins.
{{< /fails >}}

## Verify


Your Keycloak sign-in found your Straza account.

{{< console >}}
{{< clicks "Users" "alice" >}}

{{< see >}}Your own account's sheet. The console admitted you because your verified sign-in found that account and it holds an admin role.{{< /see >}}
{{< /console >}}

{{< cli >}}
{{< command terminal="Terminal" purpose="your workstation" >}}
```sh
strazactl users list
```
{{< /command >}}

{{< see >}}A row for `alice` with the status `active`, read on the token you minted after your Keycloak sign-in.{{< /see >}}
{{< /cli >}}

## Undo


Signing out ends the console's session, and revoking the token ends what strazactl can do with it.

{{< console >}}
**Sign out** in the header menu ends the console's tab session. The card then says `Signed out. The session was revoked on the server. Your identity provider may still have you signed in, so on a shared computer sign out there too.`
{{< /console >}}

{{< cli >}}
`unset STRAZA_API_TOKEN` takes the token out of your terminal. Then revoke it in the console, because the token holds no right to revoke itself:
{{< /cli >}}

{{< clicks "Settings" "API tokens" "Revoke" "Revoke token" >}}

Press **Revoke** on the token's row, then **Revoke token**. The next call on it is refused.

## Caveats {.nostep}


Disabling a person in Keycloak stops new logins and enrollments and nothing more. strazad verifies the Keycloak token at login only, and it verifies its own session tokens against its own keys. The identity manager's SCIM deactivation cuts running Straza sessions and every enrollment of the person, such as a machine key that `straza service` holds. A leaver flow therefore triggers both: Keycloak for the authentication cut, and SCIM for the authorization cut.


While Keycloak is down, nobody signs in through it. The break-glass admin still signs in through **Emergency sign-in** on the console's sign-in card, which opens Straza's own sign-in page instead of your identity provider, and mints a token there when strazactl is needed. [Standalone and enterprise compared]({{< relref "reference/standalone-and-enterprise.md#break-glass" >}}) shows the break-glass admin in both profiles.


The demo stack's issuer is `http://localhost:8480/realms/straza`, the address your browser and the command-line tools use. Containers cannot reach that address, so strazad fetches the discovery document at `keycloak:8080` through `oidc.discoveryUrl`. Keycloak serves the token and key endpoints of that document on the internal address.

## Next {.nostep}

- [midPoint]({{< relref "guides/connect-identity/midpoint.md" >}}) provisions the accounts that Keycloak logins then sign in to.
- [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) signs a workstation in for an agent with `straza enroll`.
- [Entra ID login]({{< relref "guides/connect-identity/entra-login.md" >}}) and [Okta login]({{< relref "guides/connect-identity/okta-login.md" >}}) set up the same sign-in at those providers.
- [Identities and roles]({{< relref "concepts/identities.md" >}}) explains who exists and where each fact comes from.
