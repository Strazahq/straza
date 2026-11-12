---
title: Each caller's own credential
description: Each person reaches an upstream MCP server with their own token or sign-in, and you decide what agents run on.
summary: >-
  Two credential kinds let an upstream MCP server see each person instead of one shared account:
  each caller's own pasted token, or each caller's own sign-in through a provider you configure.
  The gateway adds that person's token to every call. You decide what AI agents with no token or
  sign-in of their own run on.
pagetype: how-to
weight: 25
draft: false
tested:
  version: v1.0.0-1785-gb9f78577
  platform: A throwaway standalone server in an Alpine container on Linux, started with this page's provider block for an identity provider that does not exist, and two servers that point at the demo stack's MCP reference server, github with each caller's token and midpoint with each caller's keycloak sign-in. alice ran straza at a terminal in her own container, and the AI agents joe, sponsored by alice, and ivy, with no sponsor, called through straza mcp in theirs. That walk ran v1.1.0-117-g106081a8 on 2026-10-06. The expired-token deny needs a date that has passed, and strazactl connect and disconnect with --user were not run for this page. strazactl no longer signs in, so those two run on an admin API token, which needs apps:read and apps:write for connect and apps:write for disconnect. The console steps were read from the console's source and not clicked, except the Credentials tab's sign-in. On 2026-11-08 the eval stack ran the Entra parts against a lab tenant, with a test server that checks Microsoft's signature on every token. A person signed in from the Credentials tab and called as themselves from a Linux machine, strazad renewed that sign-in every two minutes on one replica and on two at once, and straza disconnect ended it. An agent called with its own Entra agent identity, and the agent without one, the missing certificate, the disabled agent identity and the agent identity under another blueprint each gave the sentence this page quotes
  except: the expired-token deny, strazactl connect and disconnect with --user, most console steps, AADSTS700016, a disabled blueprint and a staged key
  date: 2026-11-08
applies_to: both
keywords: caller token oauth sign-in connect sponsor agents
who: Admin, then each person who uses the server
where: Console and each person's self-service page, or a terminal
steps: true
modes: [console, cli]
mode_default: console
---


Two credential kinds let the upstream see each person instead of one shared account. `kind: token` is each caller's own pasted token. `kind: oauth` is each caller's own sign-in: each person signs in once through a provider you registered a client at, and every call then carries that person's token. Either way, two people using one server act as two upstream identities.


Both kinds are valid on the `remote` runtime only. A `command` or `oci` server is one process with one identity, so it keeps a shared secret, as [Credentials]({{< relref "guides/serve-mcp-apps/credentials.md" >}}) shows, or runs on the person's own machine under their own login. Until a shared secret is stored for such a server, the gateway reads its tool list and checks its health without a credential, and an upstream that refuses that anonymous connection reads as degraded. An agent with no credential of its own runs on what the manifest's `credential.agents` names, and every other call without a credential is denied.

## Before you start {.nostep}

- An MCP server whose vendor hands out a token per person, or an identity provider where you can register a client for Straza.
- A login that may install MCP servers, as [Add a server]({{< relref "guides/serve-mcp-apps/add-a-server.md" >}}) says.
- For each person, a role that reaches the server, as [Access per role]({{< relref "guides/serve-mcp-apps/catalogs-per-role.md" >}}) shows.

## Declare a server that takes each caller's token


A server whose vendor hands out a token per person, such as GitHub, GitLab, Linear, Supabase or Stripe on their hosted endpoints, takes `kind: token`. Each person pastes their own token once. The server tests it against the upstream, seals it under that person's id and keeps only the ciphertext, and every call that person makes then carries it.

{{< console >}}
In the Add MCP server wizard, pick **Each caller's own token** on the Credential step, then pick what agents with no token of their own run on: `Get nothing`, `Run on their sponsor's token` or `Use this server's shared secret`.

{{< shot name="server-add-token" caption="**Each caller's own token** picked, with how the token is sent and what agents with no token of their own run on." >}}
{{< /console >}}

{{< cli >}}
The manifest names the kind, what agents run on, and how the token is sent:

```yaml
straza:
  runtime:
    kind: remote
    remote:
      url: https://api.githubcopilot.com/mcp/
  credential:
    kind: token
    agents: sponsor
    inject:
      as: header
      name: Authorization
      template: "Bearer {{secret}}"
```

Install it with `strazactl apps install -f`, as [Add a server]({{< relref "guides/serve-mcp-apps/add-a-server.md" >}}) shows.
{{< /cli >}}

On a `command` runtime the parser refuses the kind with "credential.kind token gives each caller their own credential, which a command runtime cannot take: it is one process and one identity. Use kind static, or run the server on your own machine".

## Paste your own token


This step is each person's own, for themselves or for an agent they sponsor.

{{< console >}}
On the self-service page at `/self-service/`, open **Credentials**. It lists every server the person's roles reach, with what each one uses: not set, set with the fingerprint, the expiry and who set it, expired, a shared account an administrator set, or shared only on a `command` or `oci` server.

{{< clicks "Credentials" "Paste a token" "Save token" >}}

{{< shot name="selfservice-paste-token" caption="The sheet for the github server, with **Token**, **Stops working on, optional** and **Save token**." >}}

Paste the token in **Token**, and type the date the vendor shows in **Stops working on, optional**. Straza tests the token once, then keeps only a sealed copy. **Remove** takes a row away.
{{< /console >}}


{{< cli >}}
From a terminal, a person runs `straza connect` on the session their harness already holds. The paste is a hidden prompt on a terminal and a read from stdin in a script, never an argument, so the value stays out of the shell history and the process list. `--expires` records the date the provider shows, so Straza can deny the token on that day in words instead of relaying the upstream's error.

{{< command terminal="Person's machine" purpose="client" >}}
```sh
straza connect github --expires 2026-12-31
```
{{< /command >}}

```text
Paste the token for github (input hidden):
connected github for alice, token fingerprint 70c1, expires 2026-12-31
```

The listing shows the fingerprint and never the value. The `midpoint` row is the demo stack's sign-in server, which the next steps describe:

{{< command terminal="Person's machine" purpose="client" >}}
```sh
straza connect
```
{{< /command >}}

```table
SERVER    KIND   PROVIDER  CONNECTED  FINGERPRINT  EXPIRES               AGENTS                SET BY
github    token  -         true       70c1         2026-12-31T00:00:00Z  sponsor, not allowed  -
midpoint  oauth  keycloak  false      -            -                     sponsor               -
```

`straza disconnect github` takes the row away and prints `disconnected github`. `strazactl connect` and `strazactl disconnect` change another user's connection, named with `--user`, for an administrator and for scripts, on an admin API token that holds `apps:read` and `apps:write`, because `strazactl connect` reads the user's connections before it changes one. `strazactl connect github --user joe` pastes a token for the agent joe the same way, and `strazactl disconnect github --user joe` removes it. A sponsor sets or removes an agent's token in the sponsored-agent rows of the Credentials tab. An AI agent or a service account reads and changes only its own connection, whatever roles it holds, and cannot let agents use it. Naming another user answers 403, and a refused change writes one `connection.refused` record.
{{< /cli >}}


Straza forgets a removed credential at once, and the next call to that server is denied until the person connects again. The deny sentence an agent relays names the self-service page, so a person who meets it knows where to go.

{{< fails >}}
`MCP server github needs your own token and none is stored for you. Run straza connect github, or paste one on your credentials page at https://straza.example/self-service/credentials`
: Paste a token on the Credentials tab, or run `straza connect github`.

`your token for app github expired on 2026-12-31 (as it was recorded). Paste a new one on your credentials page at https://straza.example/self-service/credentials`
: Paste a new token. An expired row never falls through to another credential.
{{< /fails >}}

## Configure a sign-in provider {#each-callers-own-sign-in}


A sign-in server needs a provider in strazad's config file first, and neither the console nor a command sets one. Register a client for Straza at your identity provider with the redirect address `/v1/connect/callback` under the server's public URL, `server.publicUrl`, such as `https://straza.example/v1/connect/callback`. Then name the provider in the config file:

```yaml
oauth:
  providers:
    keycloak:
      clientId: straza-connect
      clientSecretFile: /etc/straza/keycloak-connect-secret
      authUrl: https://sso.example.com/realms/example/protocol/openid-connect/auth
      tokenUrl: https://sso.example.com/realms/example/protocol/openid-connect/token
      scopes: [openid]
```

The key under `providers`, here `keycloak`, is the name a manifest's `credential.oauth.provider` names. `clientId` is required, and so is one of `clientSecret` and `clientSecretFile`, where the file wins when both are set. `authUrl` and `tokenUrl` are required too, except for a provider named `github`, which defaults both to github.com. `scopes` are asked for when a manifest names none. The `github` provider alone can also take its client from `STRAZA_OAUTH_GITHUB_CLIENT_ID` with `STRAZA_OAUTH_GITHUB_CLIENT_SECRET` or `STRAZA_OAUTH_GITHUB_CLIENT_SECRET_FILE`. strazad reads the config at start, so restart it after the change. A provider with no `clientId`, secret or endpoints fails the start with a sentence that names the missing key.

For Microsoft Entra, Straza's client is an app registration of its own, apart from the one people sign in to Straza with in [Entra ID login]({{< relref "guides/connect-identity/entra-login.md" >}}), because this one keeps a client secret on the server. Register it with the redirect address under the platform **Web** and add a client secret under **Certificates & secrets**. On the app registration of the API that the MCP server checks tokens for, open **Expose an API** and add a delegated scope, such as `access_as_user`. Then add that scope to Straza's client under **API permissions** and grant admin consent. The scopes Straza asks for name that scope and `offline_access`, because Entra hands out a refresh token only for `offline_access`. Without one, a person's sign-in ends when its first token expires, after about an hour.

```yaml
oauth:
  providers:
    entra-crm:
      clientId: <client id of Straza's connect app registration>
      clientSecretFile: /etc/straza/entra-crm-secret
      authUrl: https://login.microsoftonline.com/<tenant id>/oauth2/v2.0/authorize
      tokenUrl: https://login.microsoftonline.com/<tenant id>/oauth2/v2.0/token
      scopes: ["api://<client id of the API>/access_as_user", "offline_access"]
```

The same block can also carry `clientCredentials`, so that agents get tokens of their own Entra agent identities, as [An agent's own Entra agent identity](#entra-agent-identity) shows.

## Declare a server that takes each caller's sign-in


{{< console >}}
In the Add MCP server wizard, pick **Each caller's own sign-in** on the Credential step. The card stays off until a provider exists, and it starts on `Run on their sponsor's sign-in` for agents.
{{< /console >}}

{{< cli >}}
The manifest names the provider:

```yaml
straza:
  runtime:
    kind: remote
    remote:
      url: https://mcp.example.com/mcp
  credential:
    kind: oauth
    agents: sponsor
    oauth:
      provider: keycloak
      scopes: [openid]
    inject:
      as: header
      name: Authorization
      template: "Bearer {{secret}}"
```
{{< /cli >}}

An install that names a provider strazad does not know is refused with `credential.oauth.provider "github" is not configured on this server. Add oauth.providers.github to the strazad config, or pick one of: keycloak.` The list at the end names the providers strazad has. The demo stack's `midpoint` server works this way, through its Keycloak.

## Sign in once


This step is each person's own.

{{< console >}}
On the self-service page, open **Credentials** and press **Sign in with keycloak** on the server's row, with your provider's name in place of keycloak. The provider's page opens, and after the sign-in the browser comes back to the Credentials tab, which stores the grant.
{{< /console >}}

{{< cli >}}
`straza connect midpoint` prints the address of that tab and waits there. The sign-in itself always finishes in a browser.
{{< /cli >}}


Straza keeps the sign-in's tokens sealed on the server and renews them before they expire, so the person signs in once. Every `oauth.refreshInterval`, one minute by default, strazad renews each sign-in whose token expires within `oauth.refreshWindow`, ten minutes by default, and stores the new refresh token when the provider sends one. Only a sign-in that came with a refresh token is renewed. A renewal the provider refuses keeps the old token, and once that expires the person's calls are denied until a later renewal works or the person signs in again. `straza disconnect midpoint` removes it. At Entra a refresh token lasts 90 days by default and each renewal brings a new one, so a sign-in in use stays until an administrator resets the person's password in the Entra or Microsoft 365 admin centre, or the person or an administrator revokes the person's sessions. A password the person changes themselves leaves it alone.

Every strazad replica runs that renewal on its own, without a lock, and keeps the token it renewed itself. Entra accepts a refresh token again after it was used once, so two replicas that renew one sign-in at the same moment both get a working token.

{{< fails >}}
`MCP server midpoint needs your own keycloak sign-in and you have not connected. Run straza connect midpoint, or sign in on your credentials page at https://straza.example/self-service/credentials`
: Sign in on the Credentials tab of the self-service page.
{{< /fails >}}

## Let your agents use your connection


A person never falls back to another credential. Only an agent does, and only when the manifest allows it and, for `sponsor`, the row's owner allows it too. This step is the sponsor's.

{{< console >}}
On the self-service page, open **Credentials** and turn on **My agents may use this** on your own row of the server.
{{< /console >}}

Your own switch has no command. A session that `straza` started on your machine can be used by an agent running as you, so from a session strazad turns the opt-in on, and changes another user's connection, only on your own sign-in in a browser. From any other session it answers that it needs your own sign-in in a browser and names the Credentials tab, and the audit chain records the refusal as `connection.refused`.

An organization that wants agents to run on people's connections by default switches the opt-in for each user from a script, on an admin API token that holds `apps:write`. `--allow-agents=false` takes it back.

{{< command terminal="Terminal" purpose="administration" >}}
```sh
strazactl connect github --user alice --allow-agents
```
{{< /command >}}

The `straza.identity.updated` record of a change made on a token carries `setBy` with the token's id and `actorVia` set to `api-token`, and the `SET BY` column of the connection's listing names the token. A change a person makes carries no `actorVia`.

Before the switch, an agent joe sponsored by alice is denied with "agent joe has no token for app github, and its sponsor alice has not allowed agents on their github connection. alice allows it on their credentials page at https://straza.example/self-service/credentials, or an administrator sets the agent's own with strazactl connect github --user joe". After it, joe's calls carry alice's token, and the record of each call reads `credentialSource: sponsor` with `credentialOwner` holding alice's user id, so a review can tell an agent acting on its sponsor's account from alice acting on her own. The same switch exists on a sign-in connection, so an `oauth` server with `agents: sponsor` behaves the same way.

## What agents run on {.nostep}


An agent cannot open a browser or receive a token from a vendor, so the manifest's `agents` value says what an agent with no row of its own runs on:

- `own`, the default, means nothing. The call is denied until the agent's sponsor pastes a token for it in the sponsored-agent rows of the Credentials tab, or an administrator does with `strazactl connect github --user joe`.
- `sponsor` means the sponsor's own row, once the sponsor allowed it. The audit record then names the agent as the caller and the sponsor as the owner of the credential.
- `shared` means the server's shared secret, set as for a static server.
- `client_credentials`, on a sign-in server only, means a token the agent's own client gets at the provider, and only the manifest sets it. The provider's block in the config then needs `clientCredentials.assertionAudience`, the audience the provider wants in a client assertion: Keycloak takes its realm issuer URL, and Okta its token endpoint URL. For Microsoft Entra it names an agent blueprint instead, as [An agent's own Entra agent identity](#entra-agent-identity) shows. Straza's client assertion key signs that assertion. A full administrator creates the key with `strazactl signing-keys rotate client_assertion` and, a minute later, makes it the signing key with `strazactl signing-keys promote client_assertion <kid>`, or does both under Settings, **Agent signing keys** in the console. Every later rotation ends with the same promote, as [Keys, certificates and tokens]({{< relref "security/keys-certificates-and-tokens.md" >}}) explains.

## An agent's own Entra agent identity {#entra-agent-identity .nostep}


When the server trusts Microsoft Entra, `client_credentials` gives each agent a token of its own Entra agent identity, so the upstream sees that agent and not a shared account. The agent never holds an Entra credential. Three things must be in place:

1. An agent blueprint in your tenant that holds the certificate of Straza's client assertion key. A full administrator downloads it under Settings, **Agent signing keys** in the console, or prints it with `strazactl signing-keys certificate client_assertion <kid>`, and an Agent ID Administrator uploads it under the blueprint's Credentials, Certificates. The console shows the certificate's SHA-1 thumbprint, the value Entra lists it under, and the blueprint's client id from the provider block in step 3. Give Straza a blueprint of its own, because whoever holds a blueprint's credential can get a token as every agent identity under it, and Microsoft draws one blueprint around one trust boundary.
2. For each agent, an agent identity under that blueprint, and its client id on the agent's Straza record as `entraAgentId`. Your IGA writes it over SCIM, or an administrator runs `strazactl users entra-agent-id set <agent> <client id>`. The agent's page under Users in the console shows the client id it is linked to and who owns the link, and does not change it.
3. A provider block that names the blueprint instead of an assertion audience:

```yaml
oauth:
  providers:
    entra-crm:
      clientId: <client id of Straza's connect app registration>
      clientSecretFile: /etc/straza/entra-crm-secret
      authUrl: https://login.microsoftonline.com/<tenant id>/oauth2/v2.0/authorize
      tokenUrl: https://login.microsoftonline.com/<tenant id>/oauth2/v2.0/token
      clientCredentials:
        entraBlueprint: <client id of the agent blueprint>
        scopes: ["api://<client id of the resource>/.default"]
```

The tenant is the one `tokenUrl` names, by id or by domain, so it is never `common`, `organizations` or `consumers`. `scopes` holds exactly one scope that ends in `/.default`. It names the resource the agents' tokens open, and only strazad's config sets it. `assertionAudience` stays out, because Entra takes the token endpoint as the audience. The fields for people's sign-in stay required, as for every provider. strazad checks all of this at start and names the key to fix. Run every replica on this version before the config gains `entraBlueprint`, because an older replica refuses to start with it.

On an agent's call the gateway sends two requests to the token endpoint within the one limit of 3 seconds. The blueprint signs in with Straza's certificate and asks for an exchange token for the agent's identity. The agent identity then presents that exchange token and gets its own token. Straza keeps that token in memory until shortly before it expires, about an hour, and never keeps or logs the exchange token. The Entra agent identity comes only from the agent's record as its check-in read it, which Straza signs into the agent's session token, never from a header, the request or a tool argument. A change of `entraAgentId` takes effect with no restart, on every strazad replica, once the agent holds a session token from a check-in that began after the change. The replica that took the change drops the agent's session state, so the agent's next call there checks in again and runs as the new identity. A session token from before the change can still run as the old identity on another replica until it expires, at most 330 seconds: its 300-second lifetime and the 30 seconds of clock skew Straza allows. A person never gets such a token.

What this does not do yet:

- A server that answers its tool list only with a token offers no tool, because Straza reads a server's tool list with its own credential and never with an agent's. An identity of Straza's own at the provider, used only for that read, is planned.
- One provider entry names one blueprint and one resource. A second resource is a second entry with the same tenant and blueprint. Agents of a second blueprint need an entry of their own and a server that names it.
- A disable of the agent identity in Entra takes effect at its next token request, up to an hour later. Disable or lock the agent in Straza to cut it at once.
- The audit record of a call names the source `client_credentials` and the agent, not the Entra agent identity. The agent's record holds the mapping.
- Straza's certificate is self-signed and valid for 730 days, so a tenant policy that limits certificate lifetime or requires a trusted certificate authority refuses its upload.

{{< fails >}}
`agent sam-sre-agent could not get a crm token. The agent has no Entra agent identity on its Straza record. Ask the identity team to set entraAgentId for sam-sre-agent, or set it with strazactl users entra-agent-id set sam-sre-agent <client id>, then call again.`
: Set the agent's `entraAgentId`. Entra is not asked until it is set.

`agent sam-sre-agent could not get a crm token. Entra refused the blueprint 5f8c2a1e-3b4d-4e6f-9a7b-1c2d3e4f5a6b (AADSTS700027), because it holds no certificate of the client assertion key 6f1c2f0e-3b7a-4d19-9a55-2f4c8e1b7d10. An Agent ID Administrator uploads the certificate that strazactl signing-keys certificate client_assertion 6f1c2f0e-3b7a-4d19-9a55-2f4c8e1b7d10 prints to that blueprint, then the agent calls again.`
: Upload the certificate of that key to the blueprint.

`agent sam-sre-agent could not get a crm token. Entra has no agent identity 9d8c7b6a-5f4e-4d3c-8b2a-1f0e9d8c7b6a in tenant 0e1d2c3b-4a59-4687-9a1b-2c3d4e5f6a7b (AADSTS700016). Check entraAgentId on sam-sre-agent's Straza record against the agent identity in the Entra admin centre, correct it with strazactl users entra-agent-id set, then call again.`
: Compare the client id on the agent's record with the agent identity in the Entra admin centre.

`agent sam-sre-agent could not get a crm token. Entra says the agent identity 9d8c7b6a-5f4e-4d3c-8b2a-1f0e9d8c7b6a is disabled (AADSTS7000112). Ask the identity team why it was disabled, and call again once it is enabled.`
: The identity team enables the agent identity in the Entra admin centre.

`agent sam-sre-agent could not get a crm token. Entra says the blueprint 5f8c2a1e-3b4d-4e6f-9a7b-1c2d3e4f5a6b is disabled (AADSTS7000112), so no agent of it gets a token. Ask the identity team why it was disabled, and call again once it is enabled.`
: A disabled blueprint stops every agent of it. Ask the identity team to enable it in the Entra admin centre. This answer is derived from the code and has not been seen from a real tenant.

`agent sam-sre-agent could not get a crm token. Entra has no blueprint 5f8c2a1e-3b4d-4e6f-9a7b-1c2d3e4f5a6b in tenant 0e1d2c3b-4a59-4687-9a1b-2c3d4e5f6a7b (AADSTS700016). Check oauth.providers.entra-crm.clientCredentials.entraBlueprint in strazad's config against the agent blueprint in the Entra admin centre, then call again.`
: `entraBlueprint` in the provider block names no blueprint of that tenant. Copy the blueprint's client id from the Entra admin centre into the config and restart strazad. This answer is derived from the code and has not been seen from a real tenant.

`agent sam-sre-agent could not get a crm token. Entra refused the agent identity 9d8c7b6a-5f4e-4d3c-8b2a-1f0e9d8c7b6a at the second step (AADSTS7002134), which Entra answers when the agent identity sits under another blueprint than 5f8c2a1e-3b4d-4e6f-9a7b-1c2d3e4f5a6b, the one Straza signs in as. Check entraAgentId on sam-sre-agent's Straza record against that blueprint's agent identities in the Entra admin centre, correct it with strazactl users entra-agent-id set, then call again.`
: The agent's record names an agent identity of another blueprint. Set the client id of its agent identity under Straza's blueprint, or create one there.

`agent sam-sre-agent could not get a crm token. The client assertion key 6f1c2f0e-3b7a-4d19-9a55-2f4c8e1b7d10 is staged and signs nothing yet. An Agent ID Administrator uploads its certificate, which strazactl signing-keys certificate client_assertion 6f1c2f0e-3b7a-4d19-9a55-2f4c8e1b7d10 prints, to the Entra blueprint 5f8c2a1e-3b4d-4e6f-9a7b-1c2d3e4f5a6b, then an administrator promotes it with strazactl signing-keys promote client_assertion 6f1c2f0e-3b7a-4d19-9a55-2f4c8e1b7d10, and the agent calls again.`
: Upload the staged key's certificate, then promote the key.
{{< /fails >}}

Every other refusal from Entra names the step, the blueprint or the agent identity, Entra's error code and where to look. Its line in the strazad log carries Entra's `error`, `error_codes`, `trace_id` and `correlation_id`, which find the request in Entra's sign-in log, and never Entra's own description.

## Who signs in for whom {.nostep}


Nobody can finish a sign-in for an agent, and the agent cannot finish one either. A sign-in is finished in a browser, and strazad stores it only for the person who is signed in to Straza in that browser. The provider sends the browser back to `/v1/connect/callback`, which redeems nothing and hands the provider's code to the Credentials tab. The tab posts it with the person's session, and strazad compares the user who started the sign-in with the user of that session before it asks the provider for anything. A link somebody else started is refused with both names, and the refusal is recorded. No browser is signed in to Straza as an agent, so an agent that runs `straza connect midpoint` is refused in words, and neither an administrator with `--user` nor the sponsor can stand in. A pasted token has no such form, which is why `strazactl connect github --user joe` works on a `token` server and `strazactl connect midpoint --user joe` does not.

| Who is calling | What they do | What the deny says until they do it |
| --- | --- | --- |
| A person | Signs in once at the provider from the Credentials tab of their self-service page. `straza connect midpoint` prints that tab's address and waits there. | MCP server midpoint needs your own keycloak sign-in and you have not connected. Run straza connect midpoint, or sign in on your credentials page at https://straza.example/self-service/credentials |
| An agent with a sponsor | Nothing itself. An administrator sets `credential.agents: sponsor`, the sponsor turns the switch on, and the agent runs on the sponsor's sign-in. `shared`, which the wizard offers as Use a shared account, and `client_credentials`, which only the manifest sets, are the other two ways. | agent joe has no midpoint sign-in of its own. An agent cannot sign in through a browser. An administrator sets credential.agents on midpoint to sponsor so it runs on alice's sign-in once alice allows it, or to shared or client_credentials |
| An agent with no sponsor | Nothing itself. An administrator picks Use a shared account in the wizard, which writes `credential.agents: shared`, or sets `client_credentials` in the manifest. | agent joe has no midpoint sign-in of its own. An agent cannot sign in through a browser. An administrator sets credential.agents on midpoint to shared or client_credentials |

The wizard's sign-in card starts on `agents: sponsor` for that reason, and `agents: own` on a sign-in server means an agent gets nothing. On the sponsor's Credentials tab the agent's row reads "Not signed in" with the reason, and "Uses your sign-in" once the sponsor's switch covers it.

## When Straza lists a server's objects {.nostep}


A server with `kind: token` or `kind: oauth` can pin its objects as well, as [Add a server]({{< relref "guides/serve-mcp-apps/add-a-server.md#objects" >}}) shows. Straza then calls the server's listing tool itself, with no person's session behind the call. That call carries the server's listing credential, a static secret stored for the listing alone, and never a person's token or sign-in, a sponsor's, the shared secret, or a token that `client_credentials` gets. Straza renders the listing credential through the manifest's `inject` block like any other credential, so with an `inject` block such as the two above, the listing sends `Authorization: Bearer` and the listing credential. Give it a token the upstream accepts on its listing tool, and store it once the pin is published, with `strazactl apps secret set` and `--listing`. Without one the listing fails, and Straza lists nothing for that server. A remote server that takes no credential is listed without one.

## Caveats {.nostep}


A pasted token is sealed under the same server key as every other stored secret, and there is no per-person encryption, because the gateway must open the row on every call, including an agent's calls while the person is away.


Your identity manager names an agent's sponsor. It can send the agent's manager, which Straza makes the sponsor, as [Generic SCIM]({{< relref "guides/connect-identity/generic-scim.md" >}}) shows. Whoever can change an agent's manager there therefore chooses whose connections the agent can reach. The sponsor can paste a token for the agent. On a server with `agents: sponsor`, the agent's calls run on the sponsor's own row once the sponsor has allowed agents on it. Straza does not check that the manager is a person, so weigh who in your identity manager may change one.

## Next {.nostep}

[Access per role]({{< relref "guides/serve-mcp-apps/catalogs-per-role.md" >}}) gives a role the server's tools.
