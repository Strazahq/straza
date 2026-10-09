---
title: Install
description: Put the three Straza binaries on your PATH and check that they came from the Straza release job.
pagetype: how-to
weight: 10
draft: false
who: You, on each machine that runs Straza
where: A terminal, or PowerShell on Windows
steps: true
tested:
  version: v1.1.0-117-g106081a8
  platform: An Alpine 3.20 linux/amd64 container that held only the three binaries of this build, copied into /usr/local/bin, where the three version commands ran and the state folders of the undo step were seen, while the release download, the cosign and checksum checks and every Windows step were not run because no public release exists before the first one is published, and the build from source was not run because this walk ran no git or make
  date: 2026-10-06
applies_to: both
keywords: install download verify checksum signature binary windows powershell
---


Straza is three executables. You download a signed release, or you build them from source with Go.

| Executable | Purpose |
|---|---|
| `strazad` | Runs the Straza server |
| `strazactl` | Administers the server from a terminal |
| `straza` | Checks policy beside an agent |

Release builds exist for Linux, Windows and macOS, each on amd64 and arm64. Choose the files for your operating system and processor. [Requirements and sizing]({{< relref "reference/requirements-and-sizing.md" >}}) lists the platforms and versions Straza was tested with, and the processors, memory and disk each shape needs. The steps below use a POSIX shell, and each step has a PowerShell form for Windows.

## Download a release and verify it


Each release carries one archive per platform, named after the project, the version, the operating system and the architecture, for example `straza_1.1.1_linux_amd64.tar.gz`. Windows gets a zip, such as `straza_1.1.1_windows_amd64.zip`. Inside are the three binaries, both license files, `NOTICE`, `TRADEMARKS.md`, the third-party notices in `THIRD_PARTY_NOTICES.txt`, the README and the standalone quickstart.

Beside the archives sit `checksums.txt`, its signature `checksums.txt.sig`, the signing certificate `checksums.txt.pem` and a software bill of materials for every archive. The checksums file is signed with keyless cosign by the GitHub Actions job that built the release, so the certificate names that job's identity.

Download the archive for your platform and the three checksum files from the releases page at github.com/strazahq/straza/releases into one folder. Then verify the signature and the checksum there. This needs `cosign` and `sha256sum`.

{{< command terminal="Terminal" purpose="in the download folder" >}}
```sh
cosign verify-blob --signature checksums.txt.sig --certificate checksums.txt.pem \
  --certificate-identity-regexp '^https://github\.com/Strazahq/straza/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
sha256sum -c checksums.txt --ignore-missing
```
{{< /command >}}

{{< see >}}Both commands exit with status 0, and `sha256sum` prints `OK` beside your archive.{{< /see >}}

The identity pattern pins the signing certificate to the Straza repository, its release workflow and a version tag, so a signature made anywhere else does not verify.

### On Windows


The same check runs in PowerShell, with `cosign` for Windows on your PATH. PowerShell continues a line with a backtick, and it has no `sha256sum`, so the last three lines compare the hash themselves. Put your archive's name in `$zip`.

{{< command terminal="PowerShell" purpose="in the download folder" >}}
```powershell
cosign verify-blob --signature checksums.txt.sig --certificate checksums.txt.pem `
  --certificate-identity-regexp '^https://github\.com/Strazahq/straza/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' `
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
$zip = "straza_1.1.1_windows_amd64.zip"
$line = Select-String -Path checksums.txt -Pattern ('  ' + [regex]::Escape($zip) + '$')
(Get-FileHash -Algorithm SHA256 $zip).Hash -eq ($line.Line -split '\s+')[0]
```
{{< /command >}}

{{< see >}}`cosign` reports the signature as verified, and the last line prints `True`.{{< /see >}}

The Windows steps on this page follow the release configuration and were not walked on a Windows machine.

{{< now title="A mismatch means a different file" >}}Any failed check means the file is not the one the release job built. Delete it and report it through the channel on the [Security]({{< relref "security/_index.md" >}}) pages.{{< /now >}}

## Put the binaries on your path


Extract the archive and copy the three binaries into a directory on your PATH, such as `/usr/local/bin` on Linux and macOS.

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
| [The demo stack]({{< relref "get-started/enterprise-demo-stack.md" >}}) | Enterprise, with demo defaults | One machine with 4 cores and 8 GB | You want to try the whole product on one machine, with midPoint, Keycloak and a SIEM. |

`straza` goes onto the machines of the people and agents you govern, and `strazactl` onto the machines of the people who administer Straza, so the archive stays the way to install both. [Run Straza]({{< relref "guides/operate/_index.md" >}}) lists every operations guide.

## Undo {.nostep}


Installing writes nothing beyond the binaries, so removing Straza from a machine where it was only installed means deleting the three files. On Windows, also take the folder out of your user Path. Once `strazad serve` has run, it keeps its state in a `data` directory where it was started. `straza enroll` and `strazactl login` keep theirs in `.straza` under your home directory. If you connected Claude Code, run `straza uninstall claude-code` first to remove the hook wiring. Then remove `data` and `.straza` for a clean machine.

[Your first governed session]({{< relref "get-started/first-governed-session.md" >}}) starts the server and takes you to the first policy decision.
