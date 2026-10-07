---
title: Contributing
description: Tell you how to propose a change and what a contribution needs before it lands.
pagetype: reference
weight: 30
draft: false
keywords: contributing pull request cla
---


A contribution to Straza is a pull request on GitHub that passes the gates kept in the repository. Open an issue first for anything beyond a small fix, so the design is agreed before the code exists. A typo or a one-line bug fix can go straight to a pull request. Before your first pull request is merged, you accept the Contributor License Agreement once, by posting one sentence on the pull request. You keep the rights in your work and grant SynapTech s. r. o., which publishes Straza, a license to use it. A check records your acceptance and confirms it in a comment that mentions you. The check also refuses material that its author marks as excluded from the agreement, in the words that section 1 of the agreement names for that. The steps are in [CONTRIBUTING.md](https://github.com/strazahq/straza/blob/main/CONTRIBUTING.md).

## What a change needs


Before a change lands, `make check` is green: vet, lint, the generated-reference drift check and the full test suite in two passes, one under the race detector and one with cgo off, the way the release binaries are built. Tests are table-driven. Policy, token and cryptographic code is written test-first. The commit subject says what changed in at most 72 characters, and the why goes in the body. The pull request description says what changed and why, and the release notes carry that line. A wire-format change ships in one change with its schema revision, its fixtures and its version bump, and the previous version's fixtures still replay green.

## Where the gates live


The Makefile defines every target. `make security` runs the network-bound vulnerability and secret scanners before a push, and CI runs lint, the file-length ratchet and the race-detector test suite on every pull request. The full text, with the changelog and wire-format rules, is [CONTRIBUTING.md](https://github.com/strazahq/straza/blob/main/CONTRIBUTING.md) in the repository, and the [Support]({{< relref "project/support.md" >}}) page says where a question goes.
