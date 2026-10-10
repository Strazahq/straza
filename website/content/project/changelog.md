---
title: Changelog
description: List what changed in each release.
pagetype: reference
weight: 20
draft: false
keywords: changelog releases versions release notes versioning
---


Straza's release notes live on the releases page at https://github.com/strazahq/straza/releases, one entry per version tag. The repository carries no changelog file, so the releases page is the record to read.

## Latest release


The latest release is [Straza 1.1.1](https://github.com/strazahq/straza/releases/tag/v1.1.1), a security update of [1.1.0](https://github.com/strazahq/straza/releases/tag/v1.1.0), the first public release. Its notes sum up what Straza does and where to start, and they link the steps that verify your download.

## How versions are numbered


Straza uses separate versions for its binaries, API, and wire formats:

| Version | Where to find it |
|---|---|
| Binary | Run `strazad version` or request `/version`. Builds between tags include the commit count and short hash. |
| Admin API | Read `info.version` in `pkg/api/openapi.yaml`, the source of the [API reference]({{< relref "reference/api.md" >}}). |
| Wire format | Read the version and revision in the relevant [specification]({{< relref "reference/spec.md" >}}). |

Use the binary version when reporting a problem. Check the format revision when writing a document that an older client must read.


Every change to a wire format under `spec/` is additive. A field is never removed or renamed, an optional request field never becomes required, and a change lands as the schema, its example and conformance fixtures and the version bump in one commit, the release notes name it, and the previous revision's fixtures still pass against the new decoder. The cost sits with rollout order, since parsers are strict: a document that uses a block from a newer revision is rejected by an older client, so clients roll before such a document is written.

## How to read a release note


A release note opens with the platforms the archives cover and the two commands that verify the checksums file and your download, then names the container image tag for that version, and links the quickstart. The archives, `checksums.txt`, its signature and certificate, and one software bill of materials per archive are attached below the note. [Supply chain]({{< relref "security/supply-chain.md" >}}) explains what each artifact is and how to reproduce the binaries from source, and [Install]({{< relref "get-started/install.md" >}}) walks the verification.
