---
title: Settings
description: You read every configuration value with the key that sets it, compose a config change to paste into your deployment, keep the attestation registry, mint and revoke admin API tokens, and rotate the client assertion key.
summary: >-
  Settings shows every configuration value with the key that sets it and changes none of them:
  its sheet composes a change for you to deploy. It keeps the attestation registry, mints and
  revokes admin API tokens, and rotates the key agents sign in with, in the order that keeps
  Entra working.
pagetype: how-to
weight: 40
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: A throwaway standalone server on Linux, read as the admin in headless Chromium. The New API token sheet and the config change sheet were opened and closed, and no token was minted and no hash retired
  date: 2026-10-06
  except: minting a token and retiring a hash, which were not done
applies_to: both
who: Admin
where: Console
steps: true
keywords: console settings configuration config change fragment attestation registry hook hash api token mint revoke scopes
---


Settings has four tabs: **API tokens**, **Agent signing keys**, **Attestation registry** and **Configuration**. It opens on the first one your session may see. **API tokens** needs the `tokens` area, **Agent signing keys** needs a full administrator, and the last two need the `config` area. A tab your session cannot open is named in a line at the top instead. Your configuration itself lives in the config file, the environment or the chart, never in a browser, and this page shows what that means for each tab.

## Before you start {.nostep}

- A console sign-in that holds the `config` area, the `tokens` area, or both.
- Access to the config file or the chart values of your deployment, for any change you decide to make.

## Read the configuration


{{< clicks "Settings" "Configuration" >}}

{{< shot name="settings-config" caption="The Configuration tab, with the relaxed settings first and each setting's name, its value and the badge `relaxed`." >}}

The tab opens with a banner: `Set in the config file, the chart or the environment.` A card such as `2 relaxed` comes next and lists each value that loosens the server with what it costs. Rows sit in six groups: **Deployment**, **Sign-in**, **Governance**, **Approvals and publishing**, **MCP servers** and **Recording**. A row shows its name and the value the server runs with, and a value that loosens the server carries the badge `relaxed`. Select a row to open it. The opened row says what the setting means. It shows `Set in the config file` with the key, such as `governance.minAttestation: none`, and `Environment` with the variable, such as `STRAZA_MIN_ATTESTATION=none`. It also says when a change takes effect. Use the copy button beside a line to copy it.

At the foot, **Console access** lists each Straza role with its holders and the admin areas it opens, such as `straza-admin` with `every area`. The map comes from `admin.roleAreas`, which only the config file sets.

## Compose a config change


{{< clicks "Settings" "Configuration" "Prepare a change" >}}

The sheet writes nothing. Pick a row in **Pick a setting to change** and give the new value, and the sheet shows what the change costs, in the row's own words. Three tabs show the same change three ways: **Config file**, **Chart values** and **Environment**, where a variable exists. Press **Copy** to copy the tab that shows, and merge it into your deployment.

Deploy it the way your deployment changes config: a restart, a rollout or a chart upgrade. Overview shows the new posture at its next read. [Hardening]({{< relref "security/hardening.md" >}}) lists the settings worth tightening before production.

## Keep the attestation registry


{{< clicks "Settings" "Attestation registry" >}}

{{< shot name="settings-registry" caption="The registry under the standalone default, with the strip `Managed installation is not required.` and the hashes of `hooks.claude-code`." >}}

The registry holds the hashes of the hook configurations a managed install writes, grouped by harness: `hooks.claude-code`, `hooks.codex` and `hooks.gemini`, each with the Linux path of its file, such as `/etc/claude-code/managed-settings.json`. Each hash lists its platforms and its status. `current` is the configuration this server generates for that harness and platform, and `allowed` is another accepted one, such as an earlier version. The server registers the configurations it generates when it starts.

A strip at the top says how strict check-in is. Under the standalone default it reads `Managed installation is not required.`, because `governance.minAttestation` is `none` and check-in does not ask for these hashes. Once that setting is `managed`, a client must present a hash this registry accepts.

**Retire this hash** removes one. The question says `A managed client with this hash is refused at its next check-in.`, so retire a hash only after every machine that presents it has a newer configuration.

{{< only form="cli" >}}`strazactl attestation add` registers a custom configuration, and `strazactl attestation list` prints the entries.{{< /only >}}

[Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) explains the attestation levels a machine reports.

## Mint an admin API token


{{< clicks "Settings" "Mint a token" >}}

{{< shot name="settings-new-token" caption="The Mint a token sheet, with **Name**, **Lifetime** and the six cards of **The job**." >}}

The token is limited to the scopes you give it. Name it for who holds it, because the name shows in the list and in audit records. Pick a **Lifetime**: `1 day`, `7 days`, `30 days`, `90 days`, which is the default, `1 year` or `Never`. The hint under the list says `For one call from your terminal, pick 1 day.`

Then pick **The job**. Each card carries the scopes that job needs, each with its reason, and you can remove any of them.

| Job | What it is for |
|---|---|
| IGA connector | midPoint or SailPoint: writes users and role membership over SCIM, reads servers, roles and the change feed. |
| Audit automation | Scripts and SIEM-side checks that read audit records and session state. |
| Policy CI pipeline | A pipeline that applies and activates policies from a repository. |
| Read-only reviewer | An auditor's evidence pass. Includes transcript content. |
| Full root | Every admin route, including minting more tokens. |
| Custom | Build the scopes by hand from the area table. |

Under **Custom**, the area table has the columns **Area and what it covers** and **Access**. Each row names an area with what it covers under its name, and offers `none`, `read` or `read and write`.

The scopes then show as the one string that `strazactl api-token create --scope` takes. Press **Mint token**.

{{< now title="Copy the token now" >}}The token is shown once, on the sheet that follows. Only its SHA-256 is stored, so a lost token cannot be recovered. Revoke it and mint another.{{< /now >}}

{{< only form="cli" >}}`strazactl api-token create` takes any duration in `--ttl`, and a token minted without it never expires. The console offers the fixed lifetimes above.{{< /only >}}

[midPoint]({{< relref "guides/connect-identity/midpoint.md" >}}) and [Generic SCIM]({{< relref "guides/connect-identity/generic-scim.md" >}}) say which scopes each connector needs.

## Revoke a token


{{< clicks "Settings" "Revoke" "Revoke token" >}}

The question names what the caller loses, such as `idm-scim loses the SCIM plane within seconds.` Revocation reaches every strazad replica as an event. A call in flight finishes, and the next one is refused with the reason, so mint the replacement first when that caller matters.

## Rotate the client assertion key


{{< clicks "Settings" "Agent signing keys" "Rotate key" "Rotate key" >}}

The client assertion key signs the sign-ins of agents that use client credentials at Keycloak, Okta or Microsoft Entra. Keycloak and Okta read a new key from Straza's key address by themselves. Entra takes only a certificate uploaded to the agent blueprint, so the certificate and every Entra step show only when a provider in the config names a blueprint under `clientCredentials.entraBlueprint`. The tab lists the live keys with their status, `staged`, `active` or `retiring`, the time each was created and, with Entra, the date its certificate ends. Retired keys fold under one line.

**Rotate key** stages a new key, and a panel then walks the rotation in the order that keeps Entra working:

1. The new key is published and signs nothing yet. The active key keeps signing.
2. With Entra, **Download certificate** saves the key's certificate as a `.pem` file, and an Agent ID Administrator uploads it under Credentials, Certificates of the blueprint the panel names by its client id. The panel shows the certificate's SHA-1 thumbprint the way Entra lists it, so the upload can be checked against Entra's row.
3. **Promote** turns on a minute after staging, once every replica publishes the key. With Entra, it asks you to confirm that the blueprint lists the thumbprint, because Entra refuses every agent until the blueprint holds the certificate.

After the promote, the previous key retires by itself within 2 minutes. With Entra, the panel then reminds you to remove the old certificate from the blueprint, because Entra accepts it until you do, and to link each agent to its Entra agent identity. With no key yet, the tab's button reads **Create key** and the same panel follows.

**Retire** on a row is for a key that may have been copied. Retiring the active key stops every agent sign-in at Keycloak, Okta and Entra until you rotate and promote a new key. [Keys, certificates and tokens]({{< relref "security/keys-certificates-and-tokens.md" >}}) explains the key and its certificate.

{{< only form="cli" >}}`strazactl signing-keys rotate client_assertion`, `strazactl signing-keys certificate client_assertion <kid>` and `strazactl signing-keys promote client_assertion <kid>` run the same three steps.{{< /only >}}

## Next {.nostep}

- [Delegated administration]({{< relref "guides/operate/delegated-admin.md" >}}) sets the `admin.roleAreas` map that **Console access** reads.
- [Overview and Audit]({{< relref "guides/console/overview-and-audit.md" >}}) shows the same relaxed settings under **Security configuration**.
