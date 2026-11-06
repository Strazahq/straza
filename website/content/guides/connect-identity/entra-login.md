---
title: Entra ID login
description: People sign in to Straza with their Microsoft Entra ID account, in a browser on the machine they enroll, and strazad trusts your tenant's ID tokens.
summary: >-
  People sign in to Straza with their Entra ID account, in a browser on the machine they
  enroll, and Straza never holds their password. You register Straza in Entra as a public
  client with a 127.0.0.1 redirect and the auth_time claim, and point strazad at your tenant.
  Your IGA provisions the people over SCIM.
pagetype: how-to
weight: 32
draft: false
tested:
  version: v1.0.0-1577-ged23b225-dirty
  platform: Not walked. The Entra steps follow Microsoft's documentation as read on 2026-10-22, and the Straza side follows the code of the working tree of the browser sign-in before its commit. No step ran against an Entra tenant. The console address step and the console's sign-in were written from the code on 2026-10-22 and not walked
  date: 2026-10-22
  except: every Entra step, which has not run against a real tenant
applies_to: enterprise
who: You, as the Entra admin and the Straza admin
where: The Microsoft Entra admin center, strazad's configuration file, and a terminal on a person's machine
steps: true
keywords: entra microsoft azure oidc login provider sign in browser
---


You make Microsoft Entra ID the login provider of an enterprise Straza, as the admin of both. You register an app for Straza in the Microsoft Entra admin center, point strazad at your tenant, and enroll a machine. At the end people sign in to Straza with their Entra account, in a browser on the machine they enroll, and Straza never holds their password.

No step on this page has been fired against a real Entra tenant yet. The Entra settings follow Microsoft's documentation, and the Straza side follows the code. [Keycloak login]({{< relref "guides/connect-identity/keycloak-login.md" >}}) is the provider that was walked end to end.

## Before you start {.nostep}

- An Entra role that registers applications and manages enterprise applications, such as Application Administrator.
- Your tenant id, which the Overview page of Microsoft Entra ID shows as Tenant ID.
- Access to strazad's configuration, and a person's Linux machine with the `straza` package, as [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) describes.
- An identity manager that provisions people into Straza over SCIM, because the enterprise profile creates no user at sign-in.

## What depends on Entra {.nostep}


Most of the sign-in is Straza's own and works alike at every provider. `straza service` runs the browser sign-in with PKCE, names the enrolling key in the nonce, and takes the answer only on 127.0.0.1. strazad checks that the sign-in names that key and is at most 10 minutes old, applies `governance.minSignIn`, writes the audit records, and refuses a person's bare ID token under a coding harness name. What depends on Entra is the app registration, how Entra matches the port of the loopback redirect, and the `auth_time` claim, which Entra sends only when you add it.

## Register the app

In the Microsoft Entra admin center, open App registrations, which the menu lists under Identity and Applications at the time of writing, and press **New registration**.

1. Name it `Straza`.
2. Under **Supported account types**, pick **Accounts in this organizational directory only**.
3. Leave **Redirect URI** empty, because a later step adds it, and press **Register**.

The Overview page of the new app shows its **Application (client) ID**. strazad needs it as `oidc.clientId`, and every Straza program presents it, so keep it.

Straza's programs run on people's machines and hold no secret, so at Entra they are a public client, on the platform **Mobile and desktop applications**, which redeems its code with PKCE alone. The next step puts the redirect on that platform. Microsoft documents that a redirect registered under the **Web** platform instead makes Entra ask for a client secret that no Straza program holds.

## Add the loopback redirect


Straza's redirect is `http://127.0.0.1:<port>/callback`, on a port that each sign-in picks. Microsoft recommends 127.0.0.1 over localhost for a loopback redirect, but the portal's form leans to localhost, so add the redirect in the app's manifest. Open **Manifest** and add `http://127.0.0.1/callback` as a redirect of the public client, then save.

In the Microsoft Graph format of the manifest, the redirect goes under `publicClient`, which nobody has yet confirmed for a 127.0.0.1 redirect against a real tenant:

```json
"publicClient": {
  "redirectUris": [
    "http://127.0.0.1/callback"
  ]
}
```

In the older AAD Graph format, it goes under `replyUrlsWithType`, with the type `InstalledClient`. Either way, **Authentication** then lists it under **Mobile and desktop applications**. A redirect registered only as `http://localhost/callback` does not match, because Straza never sends localhost.

Microsoft documents that Entra ignores the port of a localhost redirect of a desktop app, and Straza relies on the same for 127.0.0.1 until a tenant confirms it. So leave `oidc.loopbackPort` at 0. If Entra refuses the redirect because of its port, register a fixed one instead, such as `http://127.0.0.1:8765/callback`, and set `oidc.loopbackPort: 8765` on strazad.

## Add the console address


The console and the self-service page sign in within the browser tab, and Entra sends the tab back to the address people open the console at, normally your strazad public URL, followed by `/console/signin`. Because the tab builds that address from its own origin, a server that people open under two addresses needs both. Entra lets the tab redeem the code itself only for a redirect on the platform **Single-page application**.

Open **Authentication**, add a platform of the type **Single-page application**, and enter that address, such as `https://straza.example.com/console/signin`. Add one address for each address people open the console at, on each Straza server. Keep `http://127.0.0.1/callback` under **Mobile and desktop applications**, because `straza enroll` still uses it.

## Add the auth_time claim


Open **Token configuration**, press **Add optional claim**, pick the token type **ID**, tick `auth_time`, and press **Add**. Entra then says in each ID token when the person signed in. Without the claim, Straza refuses every browser sign-in for a machine, because it cannot tell that the person signed in just now.

{{< fails >}}
``Straza refused this sign-in because the identity provider sent no auth_time claim, so Straza cannot tell that you signed in just now. Ask IT to add the auth_time claim to the ID token of the Straza app, then run `straza enroll --server https://straza.example.com` again.``
: The app's ID tokens lack `auth_time`. Add the optional claim as this step shows.
{{< /fails >}}

## Grant the sign-in permissions


Open **API permissions**, press **Add a permission**, pick **Microsoft Graph**, then **Delegated permissions**, and tick `openid` and `profile` under **OpenId permissions**. Then press **Grant admin consent** for your tenant, so nobody is asked to consent at their first sign-in.

Straza asks for both scopes on every sign-in. With `profile`, Entra puts `preferred_username` in the ID token, which strazad matches to the account your identity manager provisioned.

## Require assignment

In **Enterprise applications**, open **Straza**, then **Properties**, set **Assignment required** to **Yes**, and save. Then add the people and groups who use Straza under **Users and groups**. Entra signs in nobody else to Straza, and an unassigned person sees Entra's own refusal in the browser.

## Allow sign-in with a code


Skip this step unless people need it. A machine with no display enrolls with `straza enroll --use-device-code`, which needs the device code flow, which Entra allows when you set **Allow public client flows** to **Yes** under **Authentication**, in **Advanced settings**. That setting also allows the password grant for this app, which hands a person's password to whatever program asks, so leave it off when nobody needs the code.

Microsoft's managed Conditional Access policy Block device code flow turns itself on in some tenants, and then `--use-device-code` fails whatever **Allow public client flows** says. A machine enrolled with a code is still refused while `governance.minSignIn` is `browser`, the enterprise default, so lower it to `code` only when you accept a sign-in that cannot show which machine it was approved for.

## Point strazad at Entra


The `oidc` block of strazad's configuration names your tenant's issuer and the app's client id. Put your tenant id and the app's Application (client) ID in place of the two names in angle brackets.

```yaml
oidc:
  issuer: https://login.microsoftonline.com/<tenant-id>/v2.0
  clientId: <application-client-id>
  jitProvision: false
```

`STRAZA_OIDC_ISSUER` and `STRAZA_OIDC_CLIENT_ID` are the environment forms of the first two keys. Write the issuer exactly as above, with `/v2.0` and no trailing slash, because it must equal the issuer in Entra's ID tokens. strazad reads the tenant's discovery document when it starts and refuses to start when it cannot reach it. Restart strazad after the change.

## How a token finds its account {.nostep}


The first sign-in links an account that your identity manager provisioned by matching the ID token's `preferred_username` to the account's `userName`, and only while the account has no `externalId`. Entra's `preferred_username` is usually the person's user principal name, so provision `userName` with the same value.

Entra's own SCIM provisioning sends `mailNickname` as `externalId` by default. That never equals the ID token's `sub`, which Entra makes different for each app, so such a person is refused at the first sign-in. strazad's log names the cause in a line `enroll: login not accepted` whose error says `username ... is bound to another identity`. Straza does not link on Entra's `oid` claim yet. Until it does, provision people without `externalId`, and the first sign-in links each account by username.

## Sign in


On a person's machine, in a desktop session, enroll the machine.

{{< command terminal="Terminal" purpose="on the person's machine" >}}
```sh
straza enroll --server https://straza.example.com
```
{{< /command >}}

Enroll names your tenant in a line that starts `Signing in at your identity provider: https://login.microsoftonline.com/`, then opens the browser at Microsoft's sign-in page. Entra asks the person to sign in again even when the browser is signed in to Microsoft already, because Straza asks for a fresh sign-in, and your Conditional Access policies decide which factors that sign-in needs. The browser then returns to `straza service` on the machine.

{{< see >}}`Enrolled as` and the person's username, then `The Straza background service holds this machine's key.`{{< /see >}}

In the console, **Sign in** takes the tab to Microsoft's sign-in page and back to the page the person started on. The console does not ask for a fresh sign-in, so Entra may send a browser that is signed in to Microsoft straight back.

{{< fails >}}
Entra shows the error AADSTS50011 in the browser, about a redirect URI that does not match
: The app lacks `http://127.0.0.1/callback`, or Entra compared the port. Add the redirect in the manifest, or fix the port with `oidc.loopbackPort` as the redirect step shows. The command gives up after 5 minutes.

A sentence that starts `Your identity provider refused the sign-in and said:` and names AADSTS7000218
: Entra asked for a client secret, which Microsoft documents for a redirect under the **Web** platform, though this answer has not been seen from a real tenant yet. Move it to **Mobile and desktop applications** through the manifest.

Entra shows the error AADSTS50105 in the browser
: The person is not assigned to the app. Add them, or a group they belong to, under **Users and groups**.

`The sign-in did not come back from your identity provider. If it showed an error about a redirect address, your administrator must add https://straza.example.com/console/signin to Straza's registration there. Press Sign in to try again.`
: The console says this, with your server's address, after Entra refused its redirect, usually with AADSTS50011. Add the address under **Single-page application**, as the console address step shows.

A console sentence that starts `Your identity provider` and says it refused the sign-in or did not let this page finish it
: The console address sits on a platform other than **Single-page application**, where Microsoft documents that Entra refuses a code that a browser redeems. This answer has not been seen from a real tenant yet. Move the address to **Single-page application**.

``login not accepted: run `straza enroll` again``
: strazad did not accept the ID token. Look in strazad's log for the line `enroll: login not accepted`, or `session exchange: login not accepted` for a console sign-in. When its error says `username ... is bound to another identity`, the provisioned account carries an `externalId` that is not the ID token's `sub`, as the section on how a token finds its account explains.
{{< /fails >}}

## Caveats {.nostep}


Disabling a person in Entra stops new sign-ins and enrollments and nothing more. Your IGA's SCIM deactivation cuts the person's running Straza sessions and refuses every check-in from their machines until your IGA reactivates them, so a leaver flow triggers both. While Entra cannot sign anyone in, the break-glass admin still signs in through **Emergency sign-in** on the console's sign-in card, which opens Straza's own sign-in page, and mints a token there when strazactl is needed.

## Next {.nostep}

- [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) walks the enrollment and every refusal it can meet.
- [Okta login]({{< relref "guides/connect-identity/okta-login.md" >}}) sets up the same sign-in at Okta.
- [Configuration]({{< relref "reference/configuration.md" >}}) lists `oidc.loopbackPort` and `governance.minSignIn` with their defaults.
