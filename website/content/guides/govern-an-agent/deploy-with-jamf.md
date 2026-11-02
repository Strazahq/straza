---
title: Deploy with Jamf Pro
description: Ship the signed Straza pkg to Macs with one Jamf Pro policy that installs it and runs the managed install for your Straza server, and keep the service switched on with a Managed Login Items profile.
pagetype: how-to
weight: 7
draft: false
tested:
  version: v1.0.0-1614-g5e1d90de-dirty
  platform: Not walked. Written on 2026-10-26 from the working tree of the release workflow that adds the pkg, before any release carried one, and from Jamf's documentation on packages, policy scripts and the Managed Login Items payload, read the same day. No Jamf Pro instance and no Mac enrolled in one has run this recipe. Outside Jamf Pro, on 2026-10-27, a pkg built at v1.0.0-1615-g272946e5, signed with the Developer ID and notarized by Apple, passed spctl on a Mac, and a test script installed, upgraded, uninstalled and purged it in a macOS 26.6.2 virtual machine that no device management tool manages
  date: 2026-10-26
applies_to: both
who: You, as the Jamf Pro administrator of the Macs whose agents you govern
where: Jamf Pro, and a Mac or a Linux machine for the download and its check
steps: true
keywords: jamf jamf pro policy package script parameter configuration profile managed login items team identifier macos mac pkg fleet deploy straza service managed install uninstall
---


Jamf Pro can put `straza service` on every Mac of a fleet. This page installs the Straza package for macOS and runs the managed install for your Straza server in one policy, and a configuration profile keeps the service switched on. Those are the steps that [Install the service on macOS]({{< relref "get-started/install.md#install-the-service-on-macos" >}}) takes by hand. Each person then enrolls once, as [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md#a-macos-machine" >}}) shows.

## Before you start {.nostep}


- The Straza pkg, `straza_<version>_darwin_universal.pkg`, from 2.0.0 or a later release, checked with cosign and its checksum as [Install]({{< relref "get-started/install.md#download-a-release-and-verify-it" >}}) shows. The release job signs it with the Developer ID of SynapTech s. r. o., team WMPWM57KYK, and Apple notarizes it, so Jamf Pro installs it as it is.
- Macs on macOS 13 or later, enrolled in Jamf Pro through Automated Device Enrollment or with an enrollment the person approved, which the profile of the last step needs.
- The address of your Straza server, and the harnesses the Macs run: `claude-code`, `codex` or `gemini`.

## Upload the pkg


In Jamf Pro, open Settings, then Computer management, then Packages, and add a new package. Upload the pkg under its own file name, which carries the version, so every release you upload stays apart from the others. Leave the options of the package at their defaults, because the pkg needs no restart.

## Add the managed install script


Open Settings, then Computer management, then Scripts, and add a script named `Straza managed install` with the text below. On its Options tab, set the priority to After, and label parameter 4 `Straza server` and parameter 5 `Harnesses`.

```sh
#!/bin/sh
# Jamf Pro runs this as root after the policy installs the pkg.
# Parameter 4 is the Straza server's address, parameter 5 the harnesses.
server=$4
harnesses=$5
bin=/opt/straza/bin/straza
if [ -z "$server" ] || [ -z "$harnesses" ]; then
  echo "The Straza managed install needs the server's address in parameter 4 and the harnesses in parameter 5, such as claude-code codex, and one of them is empty, so nothing was changed. Fill in both in the policy's Scripts payload, then run the policy again." >&2
  exit 1
fi
if [ ! -x "$bin" ]; then
  echo "The Straza managed install found no $bin, which the Straza pkg installs, so nothing was changed. Add the pkg to this policy's Packages payload, keep this script at the priority After, then run the policy again." >&2
  exit 1
fi
# Each harness becomes one argument, so $harnesses stays unquoted.
set -f
exec "$bin" install --managed --server "$server" $harnesses
```

The script changes nothing when a parameter is empty or the pkg's binary is missing, and it says which one in the policy's log. It runs as root outside sudo, so for codex the managed install writes no Straza MCP registration into any person's `config.toml` and says so. The managed codex hooks still govern every codex tool call.

## Create the policy


Open Computers, then Policies, and add a policy named `Straza`. Give it the Packages payload with the pkg and the Scripts payload with the script, fill in the fields as the table shows, and scope it to the Macs whose agents you govern.

| Payload | Field | Value |
|---|---|---|
| General | Trigger | Recurring Check-in, and Enrollment Complete for Macs that enroll later |
| General | Execution Frequency | Once per computer |
| Packages | Action | Install |
| Scripts | Priority | After |
| Scripts | Straza server | Your server's address, such as `https://straza.example.com` |
| Scripts | Harnesses | `claude-code`, or several separated by spaces, such as `claude-code codex` |

{{< see >}}In the policy's logs, each Mac in scope shows the install of the pkg, then the script's output with a `managed config` line that names `/etc/straza/config.yaml`, a `managed binary` line that names `/opt/straza/bin/straza`, and the `managed wiring` and `managed mcp` lines of each harness.{{< /see >}}

The priority After runs the script once the pkg is installed, so the managed install always finds the binary. A Mac where the pkg refused to install, such as one where accounts other than root can change `/opt`, gets the script's sentence about the missing binary in the log, and `/var/log/install.log` on that Mac says why the pkg stopped, as [Install]({{< relref "get-started/install.md#install-the-service-on-macos" >}}) shows.

## Keep the service switched on


Without a Managed Login Items profile, the service can be switched off under Login Items & Extensions in System Settings, and while it is off every governed tool call on the Mac is refused. Open Computers, then Configuration Profiles, and add a profile at the computer level that installs automatically. Add its Managed Login Items payload with one rule. Scope the profile to the same Macs.

| Field | Value |
|---|---|
| Rule Type | Team Identifier |
| Rule Value | `WMPWM57KYK` |
| Comment | `Straza service`, or leave it empty |

The rule matches every login item whose code is signed by team WMPWM57KYK, which is the Straza service of the release pkg. macOS turns such an item on and keeps it on, and on macOS 26 it also skips its prompt about background items for it. A package built from source without that team's Developer ID does not match the rule.

## Upgrade and remove {.nostep}


For a later release, upload its pkg, and add a second policy with only that pkg, at the frequency Once per computer and with the same scope. It upgrades each Mac in place while harness windows stay open, as [Backup and upgrade]({{< relref "guides/operate/backup-and-upgrade.md" >}}) describes. The managed install needs no new run, because the upgrade keeps its configuration and wiring. Then put the new pkg in the first policy in place of the old one, so a Mac that enrolls later starts on the new release.

To take Straza off a Mac, remove it from the scope of the first policy and the profile. Then run a policy on it, at the frequency Once per computer, with a script that runs the uninstall of the binary itself.

```sh
#!/bin/sh
exec /opt/straza/bin/straza installer uninstall
```

The uninstall stops the service and removes the managed wiring, the package's files and its receipt, and keeps the keys in `/var/db/straza-service` for a reinstall. On a Mac you retire, add `--purge` at the end of that line, which also deletes the keys and the _straza-service account, and then revoke the Mac's devices at the Straza server, as [Backup and upgrade]({{< relref "guides/operate/backup-and-upgrade.md#remove-straza-from-a-client-machine" >}}) shows.

## What was tested {.nostep}


This recipe has not run on a Jamf Pro instance, and no Mac enrolled in Jamf Pro has taken the pkg, the script or the profile. The account that runs a policy script and the parameter slots are as Jamf's documentation describes them, and they have not been seen on a Mac. The page is written from the release workflow and the pkg's sources, before any release carried a pkg. An unsigned package built from the source tree, and on 2026-10-27 a package signed with the Developer ID and notarized by Apple, ran in a macOS 26.6.2 virtual machine that no device management tool manages.
