---
title: Install
description: Put the three Straza binaries on your PATH and check that they came from the Straza release job.
summary: >-
  Straza is three binaries: `strazad` runs the server, `strazactl` administers it and `straza`
  checks policy beside an agent. You verify a release by its cosign signature and checksums, or
  build from source. A machine whose agents you govern gets `straza` from a package instead, a
  deb, rpm, MSI or pkg, which also installs `straza service`.
aliases:
  - /get-started/install/
pagetype: how-to
weight: 5
draft: false
who: You, on each machine that runs Straza
where: A terminal, or PowerShell on Windows
steps: true
tested:
  version: v1.1.0-117-g106081a8
  platform: An Alpine 3.20 linux/amd64 container that held only the three binaries of this build, copied into /usr/local/bin, where the three version commands ran and the state folders of the undo step were seen. The release download, the cosign check and the checksum check ran later on Linux against the published release 1.1.2 on 2026-10-14, and they run against 2.0.0 only after its tag. Every Windows step was not run, and the build from source was not run because this walk ran no git or make. The Windows service step now installs from the MSI. MSIs built from the source tree at v1.0.0-1600-g3c02a286 ran on a Windows 10 machine on 2026-10-26, by a test script and not by these steps, where the install, the uninstall, an upgrade while hooks ran, the refused downgrade and the purge passed. The service it registers ran on 2026-10-22 on a Windows 10 machine at v1.0.0-1585-gb30a22d7, installed with the managed install and the sc.exe commands of that version, and again as an upgrade to v1.0.0-1586-g3a9b10d6 while a Claude Code window stayed open. The macOS steps were written on 2026-10-26 from the package sources and the code, and the steps for the release pkg the same day from the release workflow and its scripts, before any release carried a pkg. An unsigned package built from the source tree ran in a macOS 26.6.2 virtual machine on 2026-10-26 at v1.0.0-1610-g366fac50, installed by a test script and not by these steps. On 2026-10-27 a pkg built from the source tree at v1.0.0-1615-g272946e5, signed with the Developer ID and notarized by Apple, passed spctl on a Mac, and the same test script installed, upgraded, uninstalled, installed again and purged it in a macOS 26.6.2 virtual machine
  except: the build from source, and the Windows and macOS steps, which only a test script ran
  date: 2026-10-06
applies_to: both
keywords: install download verify checksum signature binary windows powershell service straza-service named pipe msi msiexec macos pkg launchd shasum notarized notarization developer id gatekeeper pkgutil spctl jamf intune
---


Straza is three executables. You download a signed release, or you build them from source with Go. From 2.0.0, a release also carries the `straza` client in packages that install its background service: a deb and an rpm package for Linux, an MSI for Windows on amd64, and a pkg for macOS, signed with a Developer ID and notarized by Apple. A Mac can build that package from the source tree as well.

| Executable | Purpose |
|---|---|
| `strazad` | Runs the Straza server |
| `strazactl` | Administers the server from a terminal |
| `straza` | Checks policy beside an agent |

Release builds exist for Linux, Windows and macOS, each on amd64 and arm64. Choose the files for your operating system and processor. [Requirements and sizing]({{< relref "reference/requirements-and-sizing.md" >}}) lists the platforms and versions Straza was tested with, and the processors, memory and disk each shape needs. The steps below use a POSIX shell, and each step has a PowerShell form for Windows.


`straza` starts a governed session through `straza service`, the Straza background service that holds each machine's key and sessions. The service runs on Linux, macOS and Windows, and so do `strazad` and `strazactl`.

## Download a release and verify it


Each release carries one archive per platform, named after the project, the version, the operating system and the architecture, for example `straza_2.1.0_linux_amd64.tar.gz`. Windows gets a zip, such as `straza_2.0.0_windows_amd64.zip`. Inside are the three binaries, both license files, `NOTICE`, `TRADEMARKS.md`, the third-party notices in `THIRD_PARTY_NOTICES.txt`, the README and the standalone quickstart. Linux also gets the `straza` client as a deb and an rpm package for each architecture. Their names end in `.deb` or `.rpm` in place of `.tar.gz`. Windows on amd64 also gets the client as an MSI, whose name ends in `_windows_amd64.msi`. A Mac gets the client as one pkg for Apple silicon and Intel Macs, whose name ends in `_darwin_universal.pkg`. The packages, the MSI and the pkg exist from 2.0.0, and releases from 1.1.2 and earlier carry the archives only. A prerelease such as `2.1.0-rc.1` gets neither the pkg nor the MSI.

Beside the archives sit `checksums.txt`, its signature `checksums.txt.sig`, the signing certificate `checksums.txt.pem` and a software bill of materials for every archive. A package holds the same `straza` binary as the Linux archive of its architecture, and the MSI holds the same `straza.exe` as the Windows amd64 zip. The bill of materials of that archive lists the binary's modules. In the pkg, the `straza` binaries of the two darwin archives are joined into one file and signed with a Developer ID, so the bills of materials of those archives cover it. The checksums file is signed with keyless cosign by the GitHub Actions job that built the release, so the certificate names that job's identity.

Download the archive for your platform, or the package for a Linux machine, the MSI for a Windows machine or the pkg for a Mac whose agents you govern, and the three checksum files from the releases page at github.com/strazahq/straza/releases into one folder. Then verify the signature and the checksum there. This needs `cosign` and `sha256sum`.

{{< command terminal="Terminal" purpose="in the download folder" >}}
```sh
cosign verify-blob --signature checksums.txt.sig --certificate checksums.txt.pem \
  --certificate-identity-regexp '^https://github\.com/Strazahq/straza/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
sha256sum -c checksums.txt --ignore-missing
```
{{< /command >}}

{{< see >}}Both commands exit with status 0, and `sha256sum` prints `OK` beside each file you downloaded.{{< /see >}}

The identity pattern pins the signing certificate to the Straza repository, its release workflow and a version tag, so a signature made anywhere else does not verify.

### On macOS


Older macOS versions have no `sha256sum`, so run the same `cosign` command and then check the hashes with `shasum`, which every macOS includes. The pkg has its line in `checksums.txt` like every other release file, so this one check covers an archive and the pkg alike.

{{< command terminal="Terminal" purpose="in the download folder" >}}
```sh
shasum -a 256 -c --ignore-missing checksums.txt
```
{{< /command >}}

{{< see >}}The command exits with status 0 and prints `OK` beside each file you downloaded.{{< /see >}}

### On Windows


The same check runs in PowerShell, with `cosign` for Windows on your PATH. PowerShell continues a line with a backtick, and it has no `sha256sum`, so the last three lines compare the hash themselves. Put your archive's name in `$zip`.

{{< command terminal="PowerShell" purpose="in the download folder" >}}
```powershell
cosign verify-blob --signature checksums.txt.sig --certificate checksums.txt.pem `
  --certificate-identity-regexp '^https://github\.com/Strazahq/straza/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' `
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
$zip = "straza_2.1.0_windows_amd64.zip"
$line = Select-String -Path checksums.txt -Pattern ('  ' + [regex]::Escape($zip) + '$')
(Get-FileHash -Algorithm SHA256 $zip).Hash -eq ($line.Line -split '\s+')[0]
```
{{< /command >}}

{{< see >}}`cosign` reports the signature as verified, and the last line prints `True`.{{< /see >}}

The MSI for a Windows machine whose agents you govern gets the same hash check in [its own step](#install-the-service-on-windows).

The Windows steps on this page follow the release configuration and were not walked on a Windows machine.

{{< now title="A mismatch means a different file" >}}Any failed check means the file is not the one the release job built. Delete it and report it through the channel on the [Security]({{< relref "security/_index.md" >}}) pages.{{< /now >}}

## Install the package on Linux


On a Linux machine whose agents you govern, install the `straza` package in place of the `straza` binary from the archive. It holds only the client and its background service, so `strazad` and `strazactl` still come from the archive. Other machines skip this step.

Install the package you verified as root, from the download folder, with `apt` on Debian and Ubuntu or `dnf` on RHEL and Rocky Linux. On an arm64 machine, write `arm64` in place of `amd64`.

{{< command terminal="Terminal" purpose="Debian or Ubuntu, in the download folder" >}}
```sh
sudo apt install ./straza_*_linux_amd64.deb
```
{{< /command >}}

{{< command terminal="Terminal" purpose="RHEL or Rocky Linux, in the download folder" >}}
```sh
sudo dnf install ./straza_*_linux_amd64.rpm
```
{{< /command >}}

{{< see >}}The package manager reports `straza` installed, and `systemctl is-active straza-service.socket` then prints `active`.{{< /see >}}

The package installs `/usr/bin/straza`, the units `straza-service.socket` and `straza-service.service`, and the straza-service OS account, and it enables the socket. systemd starts `straza service` under that account at the first connection, with the hardening its unit sets, its socket at `/run/straza-service/service.sock` and its state in `/var/lib/straza-service`. No package script wires a harness or enrolls anyone. [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md#a-machine-with-the-straza-package" >}}) goes on from here, with the managed install an administrator runs and the `straza enroll` each person runs.

The packages carry no OpenPGP signature, and no apt or dnf repository serves them yet, so the checksum check of the step before is what shows that the file came from the release job. The [roadmap]({{< relref "project/roadmap.md#the-background-service-on-each-system" >}}) lists the repositories as planned.

## Put the binaries on your path


Extract the archive and copy the three binaries into a directory on your PATH, such as `/usr/local/bin` on Linux and macOS. On a Linux machine with the `straza` package, copy only `strazad` and `strazactl`. The package's `/usr/bin/straza` then stays the only `straza` there. On a Mac with the Straza package for macOS, copy only those two as well, because `/usr/local/bin/straza` is the package's link to its `/opt/straza/bin/straza`, and a copy onto that link would overwrite the package's binary.

### On Windows


In the same PowerShell window, which still holds `$zip`, extract the zip into a folder of your own. The commands add that folder to your user Path and to the Path of the current window. New PowerShell windows read the user Path when they start.

{{< command terminal="PowerShell" purpose="in the download folder" >}}
```powershell
$dir = "$env:LOCALAPPDATA\Programs\straza"
Expand-Archive -Path $zip -DestinationPath $dir
[Environment]::SetEnvironmentVariable("Path", [Environment]::GetEnvironmentVariable("Path", "User") + ";$dir", "User")
$env:Path += ";$dir"
```
{{< /command >}}

{{< see >}}`strazad.exe`, `strazactl.exe` and `straza.exe` in the folder. You type them without `.exe`.{{< /see >}}

## Install the service on Windows


On a Windows machine whose agents you govern, an administrator installs `straza service` from the Straza MSI, which installs on amd64 only. Run the commands of this step in a PowerShell started with Run as administrator, in the download folder. Other machines skip this step. [Deploy with Intune]({{< relref "guides/govern-an-agent/deploy-with-intune.md" >}}) installs the same MSI on a fleet.

The service runs under its own OS account, the virtual account NT SERVICE\straza-service, which Windows creates with the service and manages without a password. Clients reach it on the named pipe `\\.\pipe\straza-service`. It keeps the keys, the sessions and the audit spool in `C:\ProgramData\straza-service`, which only the service, SYSTEM and Administrators can open.

The MSI carries no Authenticode signature yet, so first check its hash against the checksums file whose signature you verified.

{{< command terminal="PowerShell" purpose="as an administrator, in the download folder" >}}
```powershell
$msi = (Get-Item .\straza_*_windows_amd64.msi).FullName
$line = Select-String -Path checksums.txt -Pattern ('  ' + [regex]::Escape((Split-Path $msi -Leaf)) + '$')
(Get-FileHash -Algorithm SHA256 $msi).Hash -eq ($line.Line -split '\s+')[0]
```
{{< /command >}}

{{< see >}}`True`.{{< /see >}}

Then install it. `Start-Process` waits for the installer and returns its exit code, which a bare `msiexec` call in PowerShell does not, and the installer writes its log to `straza-msi.log` in your temporary folder.

{{< command terminal="PowerShell" purpose="as an administrator, in the download folder" >}}
```powershell
(Start-Process msiexec.exe -Wait -PassThru -ArgumentList "/i `"$msi`" /qn /l*v `"$env:TEMP\straza-msi.log`"").ExitCode
```
{{< /command >}}

{{< see >}}`0`, and `Get-Service straza-service` then shows the status `Running`.{{< /see >}}

The MSI puts `straza.exe` in `C:\ProgramData\straza\bin` and locks `C:\ProgramData\straza` so that only administrators can change it. Beside `straza.exe` it puts `git-remote-straza.exe`, a second copy of it that git runs for [Git through Straza]({{< relref "guides/serve-mcp-apps/git-through-straza.md" >}}), and it adds `C:\ProgramData\straza\bin` to the machine PATH, so that git finds the helper in processes started after the next sign-in. It registers the service under its own OS account, with a security identifier of its own and a restart after each failure, registers its source in the Application event log and starts it. The service reports `Running` only once its pipe listens, so a start that cannot take the pipe shows as a failed start. At its first start the service creates `C:\ProgramData\straza-service` for itself. An exit code of `3010` also means success, and a restart then finishes removing an old file that was in use.

{{< fails >}}
An exit code of `1603`, and an Error event in the Application event log under the source straza-service whose text holds `The Straza MSI could not lock C:\ProgramData\straza to administrators, so Windows undid the install.`
: The rest of that text says why. Most often an account other than an administrator created `C:\ProgramData\straza` first, which any user may do under `C:\ProgramData`, and the MSI does not take over a folder another account owns. Move that folder away, then install the MSI again.

`A newer version of Straza is already installed. Uninstall it first if you need this older version.` in `straza-msi.log`
: This MSI is older than the one on the machine, and the MSI refuses a downgrade. Keep the newer one, or uninstall it first as [Undo](#undo) shows.
{{< /fails >}}

Then run the managed install with the MSI's `straza.exe`. It pins your Straza server for every user of the machine and wires the harness's hooks to that binary. Use `codex` or `gemini` for those harnesses, and your own server's address in place of the example.

{{< command terminal="PowerShell" purpose="as an administrator" >}}
```powershell
C:\ProgramData\straza\bin\straza.exe install --managed --server https://straza.example.com claude-code
```
{{< /command >}}

{{< see >}}A `managed config` line that names `C:\ProgramData\straza\config.yaml`, a `managed binary` line that names `C:\ProgramData\straza\bin\straza.exe`, and the `managed wiring` and `managed mcp` lines of the harness.{{< /see >}}

The managed install never copies a binary on Windows, so it runs only where the MSI installed `straza.exe`. [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md#a-windows-machine" >}}) goes on from here with the `straza enroll` each person runs.

{{< fails >}}
`` `straza install --managed` found no C:\ProgramData\straza\bin\straza.exe, which the Straza MSI installs. Install the Straza MSI for Windows first, then run this command again. ``
: The MSI is not installed on this machine. Install it as the step above shows, then run the managed install again.

A message that starts `` `straza install --managed` could not lock C:\ProgramData\straza to administrators ``
: The PowerShell window does not run as administrator. Start one with Run as administrator, then run the managed install again.
{{< /fails >}}


When a `straza` command says that the background service isn't running, check the service in an elevated PowerShell. `Get-Service straza-service` shows whether it runs, and the Application event log under the source straza-service shows why it stopped.

{{< command terminal="PowerShell" purpose="as an administrator" >}}
```powershell
Get-Service straza-service
Get-EventLog -LogName Application -Source straza-service -Newest 10 | Format-List TimeGenerated, EntryType, Message
```
{{< /command >}}

{{< see >}}`Running` in the status column, and a `msg="straza service listening"` line among the newest events with no Error entry after it.{{< /see >}}

A start the service refuses ends in an Error event with `msg="straza service stopped"` and the reason. `Start-Service` then reports that the start failed, and Windows tries again after 5 seconds, 5 seconds and a minute. When accounts other than administrators can change `C:\ProgramData\straza`, the service still starts, and it writes a Warning event with `msg="the managed config is not used"` and the reason. `straza doctor`, run by the person, checks the rest of the machine, and its `managed layout` line fails in that case too.

{{< fails >}}
A refusal that names `C:\ProgramData\straza-service` with the account it belongs to, and ends `An administrator must move that folder away, then start the service again.`
: Another account created the folder before the service's first start, or its access rules let other accounts in. Move the folder away, then run `Start-Service straza-service`.

A message that starts `` `straza service` could not create its pipe \\.\pipe\straza-service, because another program already holds that name ``
: Another program created a pipe of that name before the service started. Restart the computer, or find and stop that program, then start the service again.

A message that starts `` `straza service` could not read this computer's MachineGuid ``
: The service binds each key to the MachineGuid value under `HKLM\SOFTWARE\Microsoft\Cryptography` and does not start without it. Check that the value exists, then start the service again.

`` `straza service` runs as the Windows service straza-service and cannot run from a console. Start it from an administrator PowerShell with `Start-Service straza-service`. ``
: Someone ran `straza service` in a terminal. On Windows it runs only when the Service Control Manager starts it.

``Straza does not trust C:\ProgramData\straza, because accounts other than administrators can change it. An administrator must run `straza install --managed` again, which locks the folder.``
: The `managed layout` line of `straza doctor`, and the reason of the service's `the managed config is not used` event. An earlier Straza, or another program, left the folder open to other accounts. Run the managed install again as an administrator. Until then, Straza treats the machine as one without a managed install, so the service uses the server and keys of each enrollment.
{{< /fails >}}

## Install the service on macOS


On a Mac whose agents you govern, an administrator installs `straza service` from the Straza package for macOS, the pkg that releases from 2.0.0 carry, downloaded and verified in [the first step](#download-a-release-and-verify-it). On a test Mac, or when no release you can use carries the pkg, [build the package from source](#build-the-package-from-source) first and install that one. Other machines skip this step. [Deploy with Intune]({{< relref "guides/govern-an-agent/deploy-with-intune.md#deploy-to-macos" >}}) and [Deploy with Jamf Pro]({{< relref "guides/govern-an-agent/deploy-with-jamf.md" >}}) install the same pkg on a fleet.

The service runs as the launchd daemon ai.straza.service under its own OS account, the hidden account _straza-service, which the package creates with a free user ID from 450 to 499. Clients reach it on the socket `/opt/straza/run/service.sock`, where macOS tells the service the account of each caller. It keeps the keys and sessions in `/var/db/straza-service`, which only that account can open, and Time Machine leaves that folder out of its backups.

The release job signs the pkg and its binary with the Developer ID of SynapTech s. r. o., the company that publishes Straza, and Apple notarizes the pkg, so macOS installs it without a warning. Your checksum check of the first step already proves where the file came from. To see what macOS checks as well, ask it before you install, in the download folder.

{{< command terminal="Terminal" purpose="on the Mac, in the download folder, optional" >}}
```sh
pkgutil --check-signature straza_*_darwin_universal.pkg
spctl -a -vvv -t install straza_*_darwin_universal.pkg
```
{{< /command >}}

{{< see >}}From `pkgutil`, the lines `Status: signed by a developer certificate issued by Apple for distribution` and `Notarization: trusted by the Apple notary service`, a line that starts `Signed with a trusted timestamp on`, and `Developer ID Installer: SynapTech s. r. o. (WMPWM57KYK)` as the first certificate of the chain. From `spctl`, the file name followed by `accepted`, then `source=Notarized Developer ID` and `origin=Developer ID Installer: SynapTech s. r. o. (WMPWM57KYK)`.{{< /see >}}

{{< fails >}}
`spctl` prints `rejected`, or `pkgutil` names another certificate or no notarization
: The file is not the pkg that the release job signed, or Apple no longer trusts its signature. Do not install it. Delete it and report it through the channel on the [Security]({{< relref "security/_index.md" >}}) pages. A package you built from source without your own Developer ID fails both checks, as every unsigned package does.
{{< /fails >}}

Then install the pkg.

{{< command terminal="Terminal" purpose="as an administrator, in the download folder" >}}
```sh
sudo installer -pkg straza_*_darwin_universal.pkg -target /
```
{{< /command >}}

{{< see >}}`installer: The install was successful.`, and `sudo launchctl print system/ai.straza.service` then shows `state = running`.{{< /see >}}

Before any file lands, the package refuses to install while accounts other than root can change `/opt`, `/opt/straza` or `/opt/straza/bin`, because the hooks run the binary there. It then puts `straza` in `/opt/straza/bin`, links `/usr/local/bin/straza` and `/usr/local/bin/git-remote-straza` to it, and installs the daemon as `/Library/LaunchDaemons/ai.straza.service.plist`. git runs the second link for [Git through Straza]({{< relref "guides/serve-mcp-apps/git-through-straza.md" >}}). The package creates `/var/db/straza-service` and the socket folder `/opt/straza/run` for the service's account and starts the service. From then on launchd starts it at every boot, before anyone signs in, and restarts it when it stops. The service writes its log to `/var/log/straza-service/service.log`. A service that an administrator disabled before the install stays disabled. System Settings lists the service under Login Items & Extensions by the organization in its signing certificate, which is SynapTech s. r. o. for the release pkg.

{{< fails >}}
`installer` reports that the install failed, and `/var/log/install.log` holds a line that starts `Straza was not installed, because`
: The package stopped before any file landed. The rest of the line names the cause and what an administrator changes: a folder on the binary's path that other accounts can change, an existing _straza-service account that is not hidden or has a login shell, or no free user ID from 450 to 499. A Straza service that already ran keeps running.

A line in `/var/log/install.log` that starts `Straza installed its files`
: The files landed, but the service did not start, and the rest of the line says why, such as a state or socket folder that belongs to another account. Do what it says, then install the package again.
{{< /fails >}}

Then run the managed install with the package's binary. It pins your Straza server for every user of the Mac and wires the harness's hooks to `/opt/straza/bin/straza`. Run it by that full path, because on some Macs a person owns `/usr/local/bin`, where the package's links sit. Use `codex` or `gemini` for those harnesses, and your own server's address in place of the example.

{{< command terminal="Terminal" purpose="as an administrator" >}}
```sh
sudo /opt/straza/bin/straza install --managed --server https://straza.example.com claude-code
```
{{< /command >}}

{{< see >}}A `managed config` line that names `/etc/straza/config.yaml`, a `managed binary` line that names `/opt/straza/bin/straza`, and the `managed wiring` and `managed mcp` lines of the harness.{{< /see >}}

The managed install never copies a binary on macOS, so it runs only where the package installed `/opt/straza/bin/straza`. A device management tool that runs it as root, outside sudo, skips each person's codex MCP registration and says so, and the managed codex hooks still govern every codex tool call. [Enroll a machine]({{< relref "guides/govern-an-agent/enroll-a-machine.md#a-macos-machine" >}}) goes on from here with the `straza enroll` each person runs.

{{< fails >}}
`` `straza install --managed` found no /opt/straza/bin/straza, which the Straza package for macOS installs. Install the Straza package first, then run this command again. ``
: The package is not installed on this Mac. Install it as the step above shows, then run the managed install again.
{{< /fails >}}

When a `straza` command says that the background service isn't running, check the service in Terminal. `sudo launchctl print system/ai.straza.service` shows whether it runs, and `/var/log/straza-service/service.log` shows why it stopped.

{{< command terminal="Terminal" purpose="as an administrator" >}}
```sh
sudo launchctl print system/ai.straza.service
sudo tail -n 20 /var/log/straza-service/service.log
```
{{< /command >}}

{{< see >}}`state = running` in the first output, and a `msg="straza service listening"` line in the log with no error after it.{{< /see >}}

{{< fails >}}
A message that starts `` `straza service` cannot use /opt/straza/run ``
: The socket folder that the package makes is missing, belongs to another account, or other accounts can change it. Install the package again, which makes it, then start the service with `sudo launchctl kickstart system/ai.straza.service`.

A message that starts `` `straza service` could not read this Mac's hardware UUID from /usr/sbin/ioreg ``
: The service binds each key to the Mac's hardware UUID, which `ioreg` reports as `IOPlatformUUID`, and does not start without it. Check that `/usr/sbin/ioreg -rd1 -c IOPlatformExpertDevice` prints that line, then start the service with `sudo launchctl kickstart -k system/ai.straza.service`.
{{< /fails >}}


The Intune and Jamf Pro recipes named above also install a Managed Login Items profile for the team identifier WMPWM57KYK, which keeps the service switched on. Only a device management tool can install that profile, on macOS 13 or later. Without it, the service can be switched off under Login Items & Extensions, and while it is off every governed tool call on the Mac is refused. Claude Code also reads its managed settings from one source only, and a Claude Code profile from a device management tool comes before the managed settings file. On a Mac with such a profile, Claude Code skips the file that carries Straza's hooks, with no warning, so keep such a profile off the Macs whose agents you govern.

### Build the package from source


On a test Mac, or when no release you can use carries the pkg, build the package on a Mac from a clone of the repository, with Go 1.26 or newer, Git and Apple's Command Line Tools. The script needs the notices of every module the binary links, so generate them first. Its version comes from `git describe`.

{{< command terminal="Terminal" purpose="on a Mac, in a folder of your own" >}}
```sh
git clone https://github.com/strazahq/straza.git
cd straza
v=$(git describe --tags | sed 's/^v//')
go run ./tools/notices -release -version "$v" -o build/notices/THIRD_PARTY_NOTICES.txt
sh deploy/macos/build-pkg.sh "$v" dist
```
{{< /command >}}

{{< see >}}A last line that starts `build-pkg.sh: wrote` and names the package in `dist`, whose name ends in `_darwin_universal.pkg`.{{< /see >}}

The package holds one universal `straza` for Apple silicon and Intel Macs. Without the signing variables, the script says in one line that the package and its binary are unsigned and not notarized, which suits a test Mac and not a fleet. When `STRAZA_SIGN_APP` and `STRAZA_SIGN_INSTALLER` name Developer ID Application and Developer ID Installer identities of your own in the Mac's keychain, it signs the binary and the package as the release job does. It first checks that each certificate comes from Apple's current Developer ID authority, G2. After signing, it checks that the signed binary differs from the unsigned ones only by the signature. With `STRAZA_NOTARY_PROFILE` set to a notarytool keychain profile as well, Apple notarizes the package and the script staples the ticket to it.

Install your package as the pkg above, from the clone.

{{< command terminal="Terminal" purpose="as an administrator, in the clone" >}}
```sh
sudo installer -pkg dist/straza_*_darwin_universal.pkg -target /
```
{{< /command >}}

{{< see >}}`installer: The install was successful.`, and `sudo launchctl print system/ai.straza.service` then shows `state = running`.{{< /see >}}

The managed install and the service checks of this step hold for it unchanged. An unsigned package shows in Login Items & Extensions as `straza`, the name of its binary, in place of an organization.

## Check each binary


Ask each binary what it is.

{{< command terminal="Terminal" purpose="any folder" >}}
```sh
strazad version
strazactl version
straza version
```
{{< /command >}}

{{< see >}}One line from each, with its name, its version, the commit it was built from, the Go version and the platform.{{< /see >}}

The same three commands work in PowerShell.

## Build from source instead {.nostep}


You need Go 1.26 or newer, Git and Make. Clone the repository, change into it, and run the build target.

{{< command terminal="Terminal" purpose="build" >}}
```sh
git clone https://github.com/strazahq/straza.git
cd straza
make build
```
{{< /command >}}

The build prints one `go build` line per binary and writes the same three static binaries under `bin`, stamped with the version that `git describe` reports for your checkout. The first build downloads and compiles every dependency, so it takes longer than the builds after it. Add the repository's `bin` directory to the PATH of the current POSIX shell and ask each binary what it is.

{{< command terminal="Terminal" purpose="build" >}}
```sh
export PATH="$PWD/bin:$PATH"
strazad version
strazactl version
straza version
```
{{< /command >}}

{{< see >}}One line each, such as `strazad v1.1.0-117-g106081a8 (commit 106081a8, go1.26.6, linux/amd64)`.{{< /see >}}

Keep this terminal open for the tutorial. A new terminal needs the same PATH setting, unless you copy the binaries into a directory already on the PATH.

## Choose how to run the server {.nostep}


The server also ships as the container image `ghcr.io/strazahq/straza`, signed with keyless cosign like the checksums file, and as a Helm chart under `deploy/helm/straza` for Kubernetes. Each way of running it has its own page. The scale column comes from [Requirements and sizing]({{< relref "reference/requirements-and-sizing.md" >}}).

| Path | Profile | Scale | Choose it when |
|---|---|---|---|
| [The release archive on one host]({{< relref "guides/operate/standalone-host.md" >}}) | Standalone | One host. A run held 1,000 connected agents in 2 cores and 1 GB. | You want one binary and one data directory, with nothing else to install. |
| [A build from source](#build-from-source-instead) | Either | The same as the release archive | You need a commit that has no release yet, or you build your own binaries. |
| [The container image with Docker]({{< relref "guides/operate/docker.md" >}}) | Standalone, or enterprise with the repository's compose file | One host | You run your services as containers. |
| [The enterprise shape]({{< relref "guides/operate/enterprise-shape.md" >}}) | Enterprise | Replicas behind one load balancer, sharing one Postgres and one NATS | You sign people in through your own identity provider, or you need more than one replica. |
| [Kubernetes with Helm]({{< relref "guides/operate/kubernetes-with-helm.md" >}}) | Enterprise by default, or standalone with one replica | Two replicas by default, with Postgres and NATS bundled or your own | You run Kubernetes. |
| [The demo stack]({{< relref "evaluate/try-it-on-the-demo-stack.md" >}}) | Enterprise, with demo defaults | One machine with 4 cores and 8 GB | You want to try the whole product on one machine, with midPoint, Keycloak and a SIEM. |

`straza` goes onto the machines of the people and agents you govern, and `strazactl` onto the machines of the people who administer Straza. On a Linux machine you govern, the `straza` package installs the client and runs `straza service` beside the agents under its own OS account. On a Windows machine the [Straza MSI](#install-the-service-on-windows) does the same, and on a Mac the [Straza package for macOS](#install-the-service-on-macos). [Your first governed session]({{< relref "evaluate/try-it-on-one-laptop.md" >}}) starts the service by hand instead, as your own user. [Run Straza]({{< relref "guides/operate/_index.md" >}}) lists every operations guide.

## Undo {.nostep}


Installing from the archive writes nothing beyond the binaries, so removing Straza from a machine where it was only installed means deleting the three files. On Windows, also take the folder out of your user Path. The Straza MSI comes off in an elevated PowerShell with the install command of its step, with `/x` in place of `/i`. The uninstall removes the wiring of every harness the managed install wired, stops and removes the service and removes `straza.exe` and its copy `git-remote-straza.exe`. With them goes the MSI's PATH entry. It keeps `C:\ProgramData\straza-service` with the machine's keys, so the MSI installed again resumes every enrollment, unless you add `PURGE=1` after `/qn`, which deletes them. A macOS package has no uninstall of its own, so the Straza package for macOS comes off with `sudo /opt/straza/bin/straza installer uninstall`. It stops the service, removes the wiring of Claude Code, Codex CLI and Gemini CLI and then the package's files, and keeps `/var/db/straza-service` with the Mac's keys for a reinstall, unless you add `--purge`. The `straza` package comes off with its package manager, and [Backup and upgrade]({{< relref "guides/operate/backup-and-upgrade.md#remove-straza-from-a-client-machine" >}}) says what each removal keeps on the machine and what stays at the server. Once `strazad serve` has run, it keeps its state in a `data` directory where it was started. `strazactl` keeps nothing on the machine. An older `strazactl` that signed in left a `credentials.json` in `.straza` under your home directory. That file works nowhere and can be deleted. `straza service` keeps each enrollment in its state directory, which is `service` inside `STRAZA_HOME` when you start it with `STRAZA_HOME` set, as the first governed session does. If you connected Claude Code, run `straza uninstall claude-code` first to remove the hook wiring. Stop `straza service` if it runs, then remove `data` and `.straza` for a clean machine.

[Your first governed session]({{< relref "evaluate/try-it-on-one-laptop.md" >}}) starts the server and takes you to the first policy decision.
