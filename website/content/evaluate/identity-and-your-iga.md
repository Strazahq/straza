---
title: Identity and your IGA
description: What your identity provider, your IGA and Straza each decide, and how the first admin signs in.
pagetype: explanation
weight: 40
draft: false
summary: >-
  Your identity provider authenticates people. Your IGA is the source of people,
  agents, sponsors and role membership, and sends them to Straza over SCIM. Straza
  defines the roles and decides what each role may do on each call. On standalone,
  strazad signs people in and an admin creates people and agents in Straza.
noun: [idp, iga]
keywords: identity provider iga scim sign-in provisioning first admin break-glass bootstrap entra okta keycloak midpoint
---


The table lists which system answers each question and where it runs.

| The question | Who answers it | Where it runs |
|---|---|---|
| Whether this is really the [person]({{< relref "concepts/identities.md" >}}) | Your identity provider, such as Entra ID, Okta or Keycloak | Your cloud tenant or your own server, where the person signs in |
| Who exists, which [roles]({{< relref "concepts/roles-and-access.md" >}}) each holds, and who sponsors each [agent]({{< relref "concepts/identities.md#an-agent" >}}) | Your IGA, such as midPoint or Okta, or any SCIM 2.0 system | Your own server or tenant, which sends each change to strazad over SCIM |
| What each role may do, on each call | Straza | The Straza server, strazad, for calls to [MCP servers]({{< relref "concepts/mcp-apps-and-the-gateway.md" >}}), and the Straza service on each enrolled machine for local tools |


On the standalone profile, Straza answers every question itself. strazad signs people in with a password, and an admin creates people and agents and sets their roles and sponsors.


Straza leaves sign-in and provisioning to your systems because your IGA already runs the joiner, mover and leaver processes and the certification campaigns. A second directory would create offboarding gaps. An enterprise server that gets no users over SCIM or the admin API therefore has nobody to govern.

## Sign-in at your identity provider


strazad trusts one identity provider over OpenID Connect. It reads the provider's discovery document at startup and does not start without it. People sign in there in a browser to enroll a machine or open the console, so Straza holds no password for them and your provider's second factor applies.


Straza verifies the token and then looks the person up among its own users, the same way for every provider. By default, a sign-in on enterprise creates no user, so Straza refuses a person your IGA has not sent yet.


Disabling a person at the identity provider only blocks their next sign-in. Their enrolled machines keep checking in with the machine key until the enrollment expires, by default seven days after the sign-in that created it. Only the deactivation from your IGA ends their sessions, so a leaver flow needs both.

## Provisioning from your IGA


Your IGA sends people, agents, sponsors and role membership to strazad over SCIM 2.0, using an admin API token that an admin mints for it. [midPoint]({{< relref "guides/connect-identity/midpoint.md" >}}) sends people and AI agents, and [Okta]({{< relref "guides/connect-identity/okta.md" >}}) sends people and their role membership. [Any other SCIM 2.0 system]({{< relref "guides/connect-identity/generic-scim.md" >}}) can do the same. Provisioning does not depend on which identity provider checks sign-in, Entra ID included.


A deactivation over SCIM triggers the [kill switch]({{< relref "concepts/machines-and-sessions.md#the-kill-switch" >}}). Straza revokes every session of that user on every machine and deletes the user's own credentials on MCP servers. A SCIM delete only deactivates the user.

## What your IGA can and cannot do


Straza defines the roles, and your IGA assigns them. SCIM refuses a request to create or delete a role, and the error says roles are made in Straza. Each role appears in your IGA as a SCIM group, and group membership is the role assignment.

The group also carries a read-only block that says what the role covers: its kind, the servers and tools it reaches, the [objects]({{< relref "concepts/objects.md" >}}) it covers on each server, and the [policies]({{< relref "concepts/policy-model.md" >}}) that name it. A certifier reads it beside the members, so a review shows what the role grants and who holds it.


Your IGA masters membership of every role, straza-admin included, so its token can make anyone an admin. Control who may assign straza-admin in your IGA, where role assignment is decided. The break-glass admin, a local emergency account on every server, is not exposed over SCIM, so no reconciliation can disable it or remove its role.

## The first admin on enterprise


On a new enterprise server, nobody signs in until your IGA has sent them, and your IGA needs a token that only an admin can mint. You set up the first admin once, from the break-glass account or `oidc.bootstrapAdmin`.


The first start creates the break-glass admin and prints its password once in strazad's log. Sign in with it under Emergency sign-in on the console's sign-in card, which opens Straza's own sign-in page for this account only.


Alternatively, set `oidc.bootstrapAdmin` to your own username at the identity provider before strazad starts. Your first sign-in there then makes you an admin and creates your user if your IGA has not sent it yet. The setting acts only on a sign-in the identity provider checked, and only while the break-glass admin alone holds straza-admin. Once another admin exists, it does nothing.

Next, mint the token for your IGA and connect it, and from then on your IGA decides who holds each role. [Keys, certificates and tokens]({{< relref "security/keys-certificates-and-tokens.md" >}}) covers the break-glass account and its recovery.

{{< details summary="How a sign-in finds its user" >}}

Straza first looks for a user with the token's subject, but no user carries it yet unless your IGA sent it as `externalId`. When none does, Straza matches the token's `preferred_username` to the `userName` your IGA sent and records the subject on that user. A user bound to another subject is refused. That stops a second account at the provider with the same name from taking it over. An account named break-glass at the identity provider is refused on every sign-in, because the name belongs to the local emergency admin.


Entra ID's own SCIM provisioning sends an `externalId` that never equals the token's subject, so the [Entra ID login]({{< relref "guides/connect-identity/entra-login.md" >}}) guide has you provision people without it.
{{< /details >}}

{{< details summary="What protects the break-glass admin" >}}

The break-glass admin exists on every server in either profile. Straza refuses to deactivate, lock or delete it or to take its admin role. Each interactive sign-in as this account writes a login record to the audit chain and an alarm to strazad's log. If strazad starts and finds it without its admin role, it gives the role back.
{{< /details >}}

{{< next "evaluate/try-it-on-the-demo-stack.md" >}}
