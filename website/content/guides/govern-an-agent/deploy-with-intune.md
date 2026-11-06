---
title: Deploy with Intune
description: Ship the Straza MSI to Windows machines as an Intune Win32 app that runs in System context, and the signed Straza pkg to Macs as a macOS PKG app with a shell script and a Managed Login Items profile, each with the managed install for your Straza server.
summary: >-
  Intune puts the Straza background service on Windows machines and Macs, with the managed
  install for your Straza server. On Windows one Win32 app runs the MSI and the managed install
  in System context. On a Mac a PKG app installs the package, a script runs the managed
  install, and a Managed Login Items profile keeps the service on.
pagetype: how-to
weight: 6
draft: false
tested:
  version: v1.0.0-1614-g5e1d90de-dirty
  platform: Not walked. The Windows part was written on 2026-10-23 from the working tree of the Windows MSI before its commit, and from Microsoft's pages on adding a Win32 app to Intune and on the Win32 Content Prep Tool, read the same day. The macOS part was written on 2026-10-26 from the working tree of the release workflow that adds the pkg, before any release carried one, and from Microsoft's pages on macOS PKG apps and macOS shell scripts, read the same day. No Intune tenant, no Windows machine and no Mac has run this recipe. Outside Intune, MSIs built from the source tree at v1.0.0-1600-g3c02a286 were installed, upgraded, uninstalled and purged on a Windows 10 machine by a test script on 2026-10-26. On 2026-10-27 a pkg built at v1.0.0-1615-g272946e5, signed with the Developer ID and notarized by Apple, passed spctl on a Mac, and a test script installed, upgraded, uninstalled and purged it in a macOS 26.6.2 virtual machine that no device management tool manages
  date: 2026-10-26
  except: every Intune step, which has not run on a tenant, a Windows machine or a Mac
applies_to: both
who: You, as the Intune administrator of the machines whose agents you govern
where: A Windows machine for the packaging, and the Microsoft Intune admin center
steps: true
keywords: intune win32 app msi intunewin content prep tool windows fleet deploy system context detection rule supersedence straza service managed install macos pkg shell script settings catalog managed login items team identifier
---


Intune can put `straza service` on every Windows machine and every Mac of a fleet. For Windows, this page packages the Straza MSI and the managed install for your Straza server as one Win32 app that Intune runs in System context. Those are the two steps that [Install the service on Windows]({{< relref "guides/operate/install.md#install-the-service-on-windows" >}}) takes by hand. Each person then enrolls once, as [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md#a-windows-machine" >}}) shows. For Macs, [Deploy to macOS](#deploy-to-macos) adds the Straza package for macOS, the managed install and a profile that keeps the service switched on.

## Before you start {.nostep}


- The Straza MSI, `straza_<version>_windows_amd64.msi`, from 2.0.0 or a later release, checked with cosign and its checksum as [Install]({{< relref "guides/operate/install.md#download-a-release-and-verify-it" >}}) shows. It carries no Authenticode signature yet, so the checksum is what shows that it came from the release job.
- Windows machines on amd64, because the MSI installs on amd64 only.
- Microsoft's Win32 Content Prep Tool, `IntuneWinAppUtil.exe`, on the machine where you package.
- The address of your Straza server, and the harnesses the machines run: `claude-code`, `codex` or `gemini`.

## Package the installer


Make an empty folder named `straza-app` and copy the MSI into it as `straza.msi`, so the scripts carry no version. Save the first script below as `install.cmd` in that folder, with your own server's address and every harness the machines run at the end of its managed install line. Save the second as `uninstall.cmd`.

```bat
@echo off
msiexec /i "%~dp0straza.msi" /qn /norestart
set rc=%errorlevel%
if not %rc%==0 if not %rc%==3010 exit /b %rc%
"C:\ProgramData\straza\bin\straza.exe" install --managed --server https://straza.example.com claude-code
if errorlevel 1 exit /b 1
exit /b %rc%
```

```bat
@echo off
msiexec /x "%~dp0straza.msi" /qn /norestart %*
exit /b %errorlevel%
```

`install.cmd` stops on any exit code of the MSI other than `0` and `3010`. It then runs the managed install with the `straza.exe` the MSI installed, and it hands Intune the MSI's exit code. A `3010` means the install succeeded and a restart finishes removing an old file that was in use, and Intune counts it as a soft reboot.


Claude Code uses only its highest managed settings source, so on a Windows machine with a registry policy for Claude Code or server-managed settings that lack Straza's hooks, Claude Code skips them with no warning, as [Claude Code skips Straza's hooks under a higher managed source]({{< relref "security/known-limits.md" >}}) in Known limits says.

Then package the folder.

{{< command terminal="PowerShell" purpose="in the folder that holds straza-app" >}}
```powershell
IntuneWinAppUtil.exe -c .\straza-app -s .\straza-app\install.cmd -o .\out -q
```
{{< /command >}}

{{< see >}}`install.intunewin` in the folder `out`, the package that Intune takes.{{< /see >}}

## Add the app


In the Microsoft Intune admin center, open Apps, then All Apps, and create an app of the type Windows app (Win32). Select `install.intunewin` as the app package file. Fill in the Program and Requirements pages as the table shows, and keep the default list of return codes, where `3010` is a soft reboot.

| Field | Value |
|---|---|
| Installer type | Command line |
| Install command | `install.cmd` |
| Uninstall command | `uninstall.cmd` |
| Install behavior | System |
| Device restart behavior | Determine behavior based on return codes |
| Operating system architecture | x64 only |

Add the detection rule of the next step, then assign the app to your device group as Required.

## Detect the install


On the Detection rules page, choose a custom detection script and upload the one below, saved as `detect.ps1`. Leave its two options at No: running as a 32-bit process and enforcing a signature check. Write the version from your MSI's file name in `$want`, such as `2.0.0` for `straza_2.0.0_windows_amd64.msi`.

```powershell
$want = '2.1.0'
$exe = 'C:\ProgramData\straza\bin\straza.exe'
$svc = Get-Service straza-service -ErrorAction SilentlyContinue
if ($svc.Status -ne 'Running' -or -not (Test-Path $exe)) { exit 1 }
$line = & $exe version
if ($line -like "straza $want (*") { Write-Output $line; exit 0 }
exit 1
```

Intune counts the app as installed only when the script prints a line and exits with 0. This script does so when the service runs and `straza version` names the version you packaged. A file rule cannot compare that version, because `straza.exe` carries no file version. The service reports `Running` only once its pipe listens, so a machine where it cannot take its pipe fails detection.

## Upgrade and remove {.nostep}


For a later release, package its MSI the same way, with its version in `$want`. Add it as a new app that supersedes this one, with Uninstall previous version set to No. The new MSI then upgrades the installed one in place while harness windows stay open, and every enrollment on the machine stays. After the first upgrade that brings `git-remote-straza.exe`, people sign out and in once, as [Git through Straza]({{< relref "guides/serve-mcp-apps/git-through-straza.md#before-you-start" >}}) says. Set to Yes, Intune would run the old app's uninstall first, which removes the managed wiring and the service until the new install puts them back.

The uninstall removes the managed wiring of Claude Code, Codex CLI and Gemini CLI, stops and removes `straza service` and removes `straza.exe` and its copy `git-remote-straza.exe`. With them goes the MSI's PATH entry. It keeps the keys in `C:\ProgramData\straza-service`, so a later install resumes every enrollment. It also keeps `C:\ProgramData\straza\config.yaml` and the codex shim beside the binary. To delete the keys as well, as on machines you retire, set the uninstall command to `uninstall.cmd PURGE=1`. Then revoke each machine's devices at the Straza server, as [Backup and upgrade]({{< relref "guides/operate/backup-and-upgrade.md#remove-straza-from-a-client-machine" >}}) shows.

Intune runs both scripts as the Windows SYSTEM account, which is no person's account. For codex, the managed install therefore writes no Straza MCP registration into any person's `config.toml`, and the managed codex hooks still enforce every codex tool call. The uninstall leaves each person's `config.toml` as it is.

## Deploy to macOS {.nostep}


On a Mac fleet, Intune installs the Straza package for macOS as a macOS app, runs the managed install as a shell script of its own, and installs a profile that keeps the service switched on. Those are the steps that [Install the service on macOS]({{< relref "guides/operate/install.md#install-the-service-on-macos" >}}) takes by hand, and each person then enrolls once, as [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md#a-macos-machine" >}}) shows. You need the following:

- The Straza pkg, `straza_<version>_darwin_universal.pkg`, from 2.0.0 or a later release, checked with cosign and its checksum as [Install]({{< relref "guides/operate/install.md#download-a-release-and-verify-it" >}}) shows. The release job signs it with the Developer ID of SynapTech s. r. o., team WMPWM57KYK, and Apple notarizes it.
- Macs on macOS 13 or later, enrolled in Intune through Automated Device Enrollment or with an enrollment the person approved, which the profile of the last step needs.
- The address of your Straza server, and the harnesses the Macs run: `claude-code`, `codex` or `gemini`.

### Add the pkg as an app

In the Microsoft Intune admin center, open Apps, then All Apps, and create an app of the type macOS app (PKG). Select the pkg as the app package file. On the Program page, leave both the pre-install script and the post-install script empty. Intune reports the app installed even when its post-install script fails, so a managed install there could fail with no sign in the admin center, and the next step runs it as a script whose failure shows. On the Requirements page, choose macOS 13 as the minimum operating system. Fill in the Detection rules page as the table shows, then assign the app to your device group as Required.

| Field | Value |
|---|---|
| Ignore app version | No |
| Included apps | One entry, with the bundle ID `ai.straza.straza` and the version of the pkg, such as `2.1.0` for `straza_2.1.0_darwin_universal.pkg`. Delete any other entry. |

The pkg installs no app bundle, so `ai.straza.straza` is the identifier of its package receipt. A post on Microsoft's Intune blog says that the agent reads package receipts too, and no tenant has shown yet that Intune reports this app as installed. With Ignore app version set to No, Intune installs a pkg whose version differs from the one on the Mac, which is how a later release reaches the fleet.

### Run the managed install as a script


Save the script below as `straza-managed.sh`, with your own server's address in `server` and every harness the Macs run at the end of its last line, such as `claude-code codex`, because the managed install takes several.

```sh
#!/bin/sh
# Intune runs this as root, beside the pkg app's install, so it waits for the pkg.
server=https://straza.example.com
bin=/opt/straza/bin/straza
waited=0
while [ ! -x "$bin" ]; do
  if [ "$waited" -ge 1800 ]; then
    echo "The Straza managed install waited 30 minutes for $bin, which the Straza pkg app installs, and it is still missing, so nothing was changed. Check the pkg app's install status for this Mac in Intune, then let this script run again." >&2
    exit 1
  fi
  sleep 30
  waited=$((waited + 30))
done
exec "$bin" install --managed --server "$server" claude-code
```

Then open Devices, then By platform, macOS, Manage devices, Scripts, and add the script with these settings, assigned to the same device group.

| Field | Value |
|---|---|
| Upload script | `straza-managed.sh` |
| Run script as signed-in user | No, so it runs as root |
| Script frequency | Not configured, so it runs once |
| Max number of times to retry if script fails | 3 |

Intune runs scripts and app installs side by side, so the script waits up to 30 minutes for the binary that the pkg installs. A nonzero exit, from that wait or from the managed install, shows as Failed in the script's Device status, so a Mac whose managed install failed stands out there. The managed install runs as root, outside sudo, so for codex it writes no Straza MCP registration into any person's `config.toml` and says so. The managed codex hooks still govern every codex tool call.


Claude Code uses only its highest managed settings source, so on a Mac with a Claude Code profile or server-managed settings that lack Straza's hooks, Claude Code skips them with no warning, as [Claude Code skips Straza's hooks under a higher managed source]({{< relref "security/known-limits.md" >}}) in Known limits says.

### Keep the service switched on


Without a Managed Login Items profile, the service can be switched off under Login Items & Extensions in System Settings, and while it is off every governed tool call on the Mac is refused. Create a profile for macOS of the type Settings catalog. In the settings picker, search for Managed Login Items, add Service Management - Managed Login Items, and add one rule.

| Field | Value |
|---|---|
| Rule Type | Team Identifier |
| Rule Value | `WMPWM57KYK` |
| Comment | `Straza service`, or leave it empty |

Assign the profile to the same device group. The rule matches every login item whose code is signed by team WMPWM57KYK, which is the Straza service of the release pkg. macOS turns such an item on and keeps it on, and on macOS 26 it also skips its prompt about background items for it. A package built from source without that team's Developer ID does not match the rule.

### Upgrade and remove on macOS


For a later release, open the app, select the new pkg as its app package file and give the included app the new version. Intune then installs it on every Mac that has the old one, and the pkg upgrades in place while harness windows stay open, as [Backup and upgrade]({{< relref "guides/operate/backup-and-upgrade.md" >}}) describes. The managed install needs no new run. The upgrade keeps its configuration and wiring.

Intune has no uninstall for a macOS PKG app. To take Straza off a Mac, remove the Mac from the app's and the script's assignments, then assign it a second script that runs the uninstall of the binary itself, with Run script as signed-in user set to No.

```sh
#!/bin/sh
exec /opt/straza/bin/straza installer uninstall
```

The uninstall stops the service and removes the managed wiring, the package's files and its receipt, and keeps the keys in `/var/db/straza-service` for a reinstall. On a Mac you retire, add `--purge`, which also deletes the keys and the _straza-service account, and then revoke the Mac's devices at the Straza server, as [Backup and upgrade]({{< relref "guides/operate/backup-and-upgrade.md#remove-straza-from-a-client-machine" >}}) shows.

## What was tested {.nostep}


This recipe has not run on an Intune tenant. It is written from the MSI's source in `deploy/windows` and from Microsoft's pages on Win32 apps. Outside Intune, a test script installed MSIs built from the source tree on a Windows 10 machine on 2026-10-26, upgraded one while hooks ran, saw an older one refused, and uninstalled and purged them, as [Requirements and sizing]({{< relref "reference/requirements-and-sizing.md#tested-with" >}}) records.

The macOS part has not run on an Intune tenant either, so whether Intune detects the pkg through `ai.straza.straza` has not been seen, and no Mac enrolled in Intune has taken the pkg, the script or the profile. It is written from the release workflow and the pkg's sources, before any release carried a pkg, and from Microsoft's pages on macOS PKG apps and shell scripts. An unsigned package built from the source tree, and on 2026-10-27 a package signed with the Developer ID and notarized by Apple, ran in a macOS 26.6.2 virtual machine that no device management tool manages.
