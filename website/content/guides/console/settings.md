---
title: Settings
description: You read every configuration value with the key that sets it, compose a config change to paste into your deployment, keep the attestation registry, and mint and revoke admin API tokens.
pagetype: how-to
weight: 40
draft: false
tested:
  version: v1.1.0-117-g106081a8
  platform: A throwaway standalone server on Linux, read as the admin in headless Chromium. The New API token sheet and the config change sheet were opened and closed, and no token was minted and no hash retired
  date: 2026-10-06
applies_to: both
who: You, as an admin
where: The console in a browser
steps: true
keywords: console settings configuration config change fragment attestation registry hook hash api token mint revoke scopes
---


Settings has three tabs: **Configuration**, **Attestation registry** and **API tokens**. The first two need the `config` area and the third needs the `tokens` area, and a tab your session cannot open is named in a line at the top instead. Your configuration itself lives in the config file, the environment or the chart, never in a browser, and this page shows what that means for each tab.

## Before you start {.nostep}

- A console sign-in that holds the `config` area, the `tokens` area, or both.
- Access to the config file or the chart values of your deployment, for any change you decide to make.

## Read the configuration


{{< clicks "Settings" "Configuration" >}}

{{< shot name="settings-config" caption="The Configuration tab, with each setting's key, its environment variable, its value and the badge `relaxed`." >}}

The tab opens with `Read from the config file, the environment and your chart, never from the database. Nothing here changes in a browser; each row says where to set it.` Rows sit in six groups: **Deployment**, **Sign-in**, **Governance**, **Approvals**, **MCP servers** and **Recording**. Each row shows its name, the key and the environment variable that set it, such as `governance.minAttestation · STRAZA_MIN_ATTESTATION`, and the value the server runs with. A value that loosens the server carries the badge `relaxed`. Select a name to read what the setting means and its allowed values, and use the copy button to copy the line for the config file.

At the foot, **Console access** lists each Straza role with its holders and the admin areas it opens, such as `straza-admin` with `every area`. The map comes from `admin.roleAreas`, which only the config file sets.

## Compose a config change


{{< clicks "Settings" "Write a config change" >}}

The sheet writes nothing. Pick a row in **Pick a setting to change** and give the new value, and the sheet shows what the change costs, in the row's own words. **The fragment** shows the same change three ways: for `straza.yaml`, for the chart's `values.yaml`, and as environment variables where they exist. Press **Copy the fragment** and merge it into your deployment.

Deploy it the way your deployment changes config: a restart, a rollout or a chart upgrade. Overview shows the new posture at its next read. [Hardening]({{< relref "security/hardening.md" >}}) lists the settings worth tightening before production.

## Keep the attestation registry


{{< clicks "Settings" "Attestation registry" >}}

{{< shot name="settings-registry" caption="The registry under the standalone default, with the strip `Managed installation is not required.` and the hashes of `hooks.claude-code`." >}}

The registry holds the hashes of the hook configurations a managed install writes, grouped by harness: `hooks.claude-code`, `hooks.codex` and `hooks.gemini`, each with the Linux path of its file, such as `/etc/claude-code/managed-settings.json`. Each hash lists its platforms and its status. `current` is the configuration this server generates for that harness and platform, and `allowed` is another accepted one, such as an earlier version. The server registers the configurations it generates when it starts.

A strip at the top says how strict check-in is. Under the standalone default it reads `Managed installation is not required.`, because `governance.minAttestation` is `none` and check-in does not ask for these hashes. Once that setting is `managed`, a client must present a hash this registry accepts.

**Retire this hash** removes one. The question says `A managed box presenting this hash is refused at its next check-in.`, so retire a hash only after every machine that presents it has a newer configuration.

{{< only form="cli" >}}`strazactl attestation add` registers a custom configuration, and `strazactl attestation list` prints the entries.{{< /only >}}

[Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md" >}}) explains the attestation levels a machine reports.

## Mint an admin API token


{{< clicks "Settings" "API tokens" "New API token" >}}

{{< shot name="settings-new-token" caption="The New API token sheet, with **Name**, **Lifetime** and the six cards of **The job**." >}}

An admin API token is a credential for automation, a connector or your identity manager, limited to the scopes you give it. Name it after the system that holds it, because the name shows in the list and in audit records. Pick a **Lifetime**: `expires in 30 days`, `expires in 90 days`, which is the default, `expires in 1 year` or `never expires`.

Then pick **The job**. Each card carries the scopes that job needs, each with its reason, and you can remove any of them.

| Job | What it is for |
|---|---|
| IGA connector | midPoint or SailPoint: writes users and role membership over SCIM, reads servers, roles and the change feed. |
| Audit automation | Scripts and SIEM-side checks that read the ledger and session state. |
| Policy CI pipeline | A pipeline that applies and activates policy sets from a repository. |
| Read-only reviewer | An auditor's evidence pass. Includes transcript content, marked as such. |
| Full root | Every admin route, including minting more tokens. Rare on purpose. |
| Custom | Build the scopes by hand from the area table. |

The scopes then show as the one string that `strazactl api-token create --scope` takes. Press **Mint token**.

{{< now title="Copy the token now" >}}The token is shown once, on the sheet that follows. Only its SHA-256 is stored, so a lost token cannot be recovered. Revoke it and mint another.{{< /now >}}

{{< only form="cli" >}}`strazactl api-token create` takes any duration in `--ttl`, and a token minted without it never expires. The console offers the four fixed lifetimes above.{{< /only >}}

[midPoint]({{< relref "guides/connect-identity/midpoint.md" >}}) and [Generic SCIM]({{< relref "guides/connect-identity/generic-scim.md" >}}) say which scopes each connector needs.

## Revoke a token


{{< clicks "Settings" "API tokens" "Revoke" "Revoke token" >}}

The question names what the caller loses, such as `Whatever signs in as idm-scim loses the SCIM plane within seconds.` Revocation reaches every strazad replica as an event. A call in flight finishes, and the next one is refused with the reason, so mint the replacement first when that caller matters.

## Next {.nostep}

- [Delegated administration]({{< relref "guides/operate/delegated-admin.md" >}}) sets the `admin.roleAreas` map that **Console access** reads.
- [Overview and Audit]({{< relref "guides/console/overview-and-audit.md" >}}) shows the same relaxed settings under **Security configuration**.
