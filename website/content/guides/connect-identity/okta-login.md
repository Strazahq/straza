---
title: Okta login
description: People sign in to Straza with their Okta account, in a browser on the machine they enroll, and strazad trusts your Okta authorization server's ID tokens.
summary: >-
  People sign in to Straza with their Okta account, in a browser on the machine they enroll,
  and Straza never holds their password. You create a native app with PKCE and a fixed
  127.0.0.1 redirect port, and point strazad at your authorization server. Your IGA, or Okta
  itself, provisions the people over SCIM.
pagetype: how-to
weight: 34
draft: false
tested:
  version: v1.0.0-1577-ged23b225-dirty
  platform: Not walked. The Okta steps follow Okta's documentation as read on 2026-10-22, and the Straza side follows the code of the working tree of the browser sign-in before its commit. No step ran against an Okta org. The console address step and the console's sign-in were written from the code on 2026-10-22 and not walked
  date: 2026-10-22
  except: every Okta step, which has not run against a real Okta org
applies_to: enterprise
who: Okta admin and Straza admin
where: Okta Admin Console, strazad config file, and a terminal on a person's machine
steps: true
keywords: okta oidc login provider sign in browser native app pkce
---


You make Okta the login provider of an enterprise Straza, as the admin of both. You create a native app for Straza in the Okta Admin Console, point strazad at your authorization server, and enroll a machine. At the end people sign in to Straza with their Okta account, in a browser on the machine they enroll, and Straza never holds their password.

No step on this page has been fired against a real Okta org yet. The Okta settings follow Okta's documentation, and the Straza side follows the code. [Keycloak login]({{< relref "guides/connect-identity/keycloak-login.md" >}}) is the provider that was walked end to end.

## Before you start {.nostep}

- Okta administration for app integrations and for the authorization server you sign in at.
- Access to strazad's configuration, and a person's Linux machine with the `straza` package, as [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) describes.
- An identity manager that provisions people into Straza over SCIM, because the enterprise profile creates no user at sign-in. Okta can do that itself, as [Okta]({{< relref "guides/connect-identity/okta.md" >}}) shows.
- A free port from 1024 to 65535 on people's machines for the sign-in answer. The examples use 8765.

## What depends on Okta {.nostep}


Most of the sign-in is Straza's own and works alike at every provider. `straza service` runs the browser sign-in with PKCE, names the enrolling key in the nonce, and takes the answer only on 127.0.0.1. strazad checks that the sign-in names that key and is at most 10 minutes old, applies `governance.minSignIn`, writes the audit records, and refuses a person's bare ID token under a coding harness name. What depends on Okta is the app integration, the grant types its authorization server allows, and the port of the loopback redirect. Okta's documentation reads as matching that port exactly, which nobody has yet confirmed against a real Okta org, so this page fixes one port.

## Create the app

In the Okta Admin Console, open Applications, then Applications, and press **Create App Integration**. Pick **OIDC - OpenID Connect** and **Native Application**, then press **Next**.

1. Name it `Straza`.
2. Under **Grant type**, tick **Authorization Code**. Tick **Device Authorization** as well only when people sign in with a code, as a later step explains.
3. Enter `http://127.0.0.1:8765/callback` under **Sign-in redirect URIs**, with the port you picked.
4. Pick **Limit access to selected groups** under **Assignments**, and name the groups whose members use Straza.
5. Press **Save**.

On the app's **General** tab, keep **Client authentication** at **None** and **Require PKCE as additional verification** ticked. Straza's programs run on people's machines and hold no secret, and they send an S256 challenge with every browser sign-in. The **Client ID** on the same tab is what strazad needs as `oidc.clientId`.

## Allow the grants at the authorization server

Open Security, then API, pick the authorization server you sign in at, and open **Access Policies**. Make sure a policy assigned to the Straza app has a rule that allows **Authorization Code** for the people who use Straza. Okta then mints ID tokens for the Straza app from that server, and its **Settings** tab shows the **Issuer** that strazad needs.

## Add the console address


The console and the self-service page sign in within the browser tab, and Okta sends the tab back to the address people open the console at, normally your strazad public URL, followed by `/console/signin`. Because the tab builds that address from its own origin, a server that people open under two addresses needs both. On the Straza app's **General** tab, add that address under **Sign-in redirect URIs**, such as `https://straza.example.com/console/signin`, next to the loopback redirect, and save. Add one address for each address people open the console at, on each Straza server.

The tab then redeems the code at Okta from the page, so Okta must trust the console's origin. Open Security, then API, then **Trusted Origins**, and press **Add Origin**. Enter the origin people open the console at, such as `https://straza.example.com`, tick **CORS**, and save. Add one origin for each such address.

## Point strazad at Okta


The `oidc` block of strazad's configuration names the authorization server's issuer, the app's client id and the port. Put your own issuer and client id in place of the two names in angle brackets. For Okta's default custom authorization server the issuer has the form `https://<your-org>.okta.com/oauth2/default`.

```yaml
oidc:
  issuer: <issuer-of-your-authorization-server>
  clientId: <client-id-of-the-straza-app>
  loopbackPort: 8765
  jitProvision: false
```

Write the issuer exactly as the authorization server shows it, because it must equal the issuer in Okta's ID tokens. strazad reads the discovery document there when it starts and refuses to start when it cannot reach it. Restart strazad after the change. Okta documents `auth_time` among the claims of its ID tokens, and Straza needs that claim to accept a browser sign-in for a machine.

Okta's documentation reads as accepting a loopback redirect only with the exact port it has registered, which is not yet confirmed against a real Okta org, so `oidc.loopbackPort`, or `STRAZA_OIDC_LOOPBACK_PORT`, carries the same port as the app's sign-in redirect. strazad publishes it in `/.well-known/straza/idp.json`, and `straza service` then listens there on 127.0.0.1 for the one answer of each sign-in. Pick a port no other program on people's machines uses, because a sign-in that finds it taken stops with the sentence below.

{{< fails >}}
``Straza could not wait for the sign-in answer on port 8765, because another program uses it. Wait a minute and run `straza enroll` again, or ask your Straza admin to change oidc.loopbackPort.``
: Another program on the machine holds the port, often a sign-in that still waits. Wait for it, or change the port at Okta and in `oidc.loopbackPort` together.
{{< /fails >}}

## Allow sign-in with a code


Skip this step unless people need it. A machine with no display enrolls with `straza enroll --use-device-code`, which needs the **Device Authorization** grant type ticked on the app and allowed in the access policy rule of your authorization server. A machine enrolled with a code is still refused while `governance.minSignIn` is `browser`, the enterprise default.

## How a token finds its account {.nostep}


strazad first matches the ID token's `sub` to the account's `externalId`. When Okta also provisions people over SCIM with the Okta user id as `externalId`, as [Okta]({{< relref "guides/connect-identity/okta.md" >}}) maps it, that id is the `sub` of Okta's ID tokens, and every sign-in finds its account. Without a match, the first sign-in links an account that has no `externalId` yet by matching `preferred_username` to its `userName`.

## Sign in


On a person's machine, in a desktop session, enroll the machine.

{{< command terminal="Terminal" purpose="on the person's machine" >}}
```sh
straza enroll --server https://straza.example.com
```
{{< /command >}}

Enroll names your authorization server in a line that starts `Signing in at your identity provider:`, then opens the browser at Okta's sign-in page. Okta asks the person to sign in again, with the factors your sign-on policy requires, because Straza asks for a fresh sign-in. The browser then returns to `straza service` on the machine, at the port you fixed.

{{< see >}}`Enrolled as` and the person's username, then `The Straza background service holds this machine's key.`{{< /see >}}

In the console, **Sign in** takes the tab to Okta's sign-in page and back to the page the person started on. The console does not ask for a fresh sign-in, so Okta may send a browser that is signed in to Okta straight back.

{{< fails >}}
Okta shows an error about the `redirect_uri` in the browser
: The app's sign-in redirect does not match the one Straza sent. Check that the port at Okta equals `oidc.loopbackPort`, and that the host is 127.0.0.1. The command gives up after 5 minutes.

A sentence that starts `Your identity provider refused the sign-in and said:`
: Okta refused the sign-in and sent its reason, which the sentence quotes. A person outside the assigned groups, or an access policy rule without the grant, are the usual causes.

`The sign-in did not come back from your identity provider. If it showed an error about a redirect address, your administrator must add https://straza.example.com/console/signin to Straza's registration there. Press Sign in to try again.`
: The console says this, with your server's address, after Okta refused its redirect. Add the address under **Sign-in redirect URIs**, as the console address step shows.

A console sentence that starts `Your identity provider at` and goes on `did not let this page finish the sign-in.`
: The browser could not redeem the sign-in at Okta, because the server's origin is not a trusted origin with CORS. Add it under **Trusted Origins**.

``Straza refused this sign-in because the identity provider sent no auth_time claim, so Straza cannot tell that you signed in just now. Ask IT to add the auth_time claim to the ID token of the Straza app, then run `straza enroll --server https://straza.example.com` again.``
: The ID token lacks `auth_time`. Check the claims of the authorization server you sign in at.
{{< /fails >}}

## Caveats {.nostep}



Deactivating a person in Okta stops new sign-ins and enrollments. Okta's SCIM deactivation, when Okta provisions Straza, also cuts the person's running Straza sessions and refuses every check-in from their machines until Okta reactivates them. While Okta cannot sign anyone in, the break-glass admin still signs in through **Emergency sign-in** on the console's sign-in card, which opens Straza's own sign-in page, and mints a token there when strazactl is needed.

## Next {.nostep}

- [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) walks the enrollment and every refusal it can meet.
- [Okta]({{< relref "guides/connect-identity/okta.md" >}}) provisions people and their role membership from Okta over SCIM.
- [Entra ID login]({{< relref "guides/connect-identity/entra-login.md" >}}) sets up the same sign-in at Microsoft Entra ID.
