---
title: Changelog
description: List what changed in each release.
pagetype: reference
weight: 20
draft: false
keywords: changelog releases versions release notes versioning
---


Straza keeps its changelog in [CHANGELOG.md](https://github.com/strazahq/straza/blob/main/CHANGELOG.md) at the root of the repository. Its Unreleased section lists what the main branch carries since the last release, and each release has a section of its own. The release notes of each version tag live on the releases page at https://github.com/strazahq/straza/releases.

## Latest release


The latest release is [Straza 2.0.0](https://github.com/strazahq/straza/releases/tag/v2.0.0), a major release. strazad 2.0.0 refuses the machines that a 1.x `straza` client enrolled and the sign-in of a 1.x `strazactl`, so every governed machine gets a new client and every person enrolls again. [Upgrade from 1.1.2 to 2.0.0]({{< relref "guides/operate/backup-and-upgrade.md#upgrade-from-112-to-200" >}}) lists the steps in their order. Its notes list the changes, name each break of a wire format, and link the steps that verify your download.

Before it came [1.1.2](https://github.com/strazahq/straza/releases/tag/v1.1.2), a bug-fix release, [1.1.1](https://github.com/strazahq/straza/releases/tag/v1.1.1), a security update, and [1.1.0](https://github.com/strazahq/straza/releases/tag/v1.1.0), the first public release.

## How versions are numbered


Straza uses separate versions for its binaries, API, and wire formats:

| Version | Where to find it |
|---|---|
| Binary | Run `strazad version` or request `/version`. Builds between tags include the commit count and short hash. |
| Admin API | Read `info.version` in `pkg/api/openapi.yaml`, the source of the [API reference]({{< relref "reference/api.md" >}}). |
| Wire format | Read the version and revision in the relevant [specification]({{< relref "reference/spec.md" >}}). |

Use the binary version when reporting a problem. Check the format revision when writing a document that an older client must read.


Within one major version, a change to a wire format under `spec/` is additive. A field is never removed or renamed, an optional request field never becomes required, and a change lands as the schema, its example and conformance fixtures and the version bump in one commit, the release notes name it, and the previous revision's fixtures still pass against the new decoder. The cost sits with rollout order, since parsers are strict: a document that uses a block from a newer revision is rejected by an older client, so clients roll before such a document is written.

A change that refuses what an earlier release accepted is a break, and the release that carries it is a new major version. Straza 2.0.0 is such a release. Its five breaks are all in the check-in and enrollment of the attestation specification, whose changelog records each one. Its release notes name each one, and [The spec]({{< relref "reference/spec.md#how-a-specification-changes" >}}) lists them.

## How to read a release note


A release note opens with the platforms the archives and the Linux packages cover and the two commands that verify the checksums file and your download, then names the container image tag for that version, and links the quickstart. The archives, `checksums.txt`, its signature and certificate, and one software bill of materials per archive are attached below the note. From 2.0.0 a release also attaches the deb and rpm packages, the Windows MSI and the macOS pkg, and its note names them. [Supply chain]({{< relref "security/supply-chain.md" >}}) explains what each artifact is and how to reproduce the binaries from source, and [Install]({{< relref "get-started/install.md" >}}) walks the verification.
