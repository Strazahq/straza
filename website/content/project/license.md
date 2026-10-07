---
title: License
description: State the license terms Straza ships under.
pagetype: reference
weight: 50
draft: false
keywords: license agpl apache terms commercial copyright
---


Straza ships under two licenses, split so that the platform stays open while integrating with it stays free of copyleft. The server, the clients and the console are AGPL-3.0-only, and everything you embed in your own agent or implement against, and the agent skill bundle, is Apache-2.0.

| Component | License |
|---|---|
| The server `strazad`, the `straza` client, `strazactl` and the console, and everything not listed below | [AGPL-3.0-only](https://github.com/strazahq/straza/blob/main/LICENSE) |
| `spec/`, the wire formats, schemas and conformance fixtures | [Apache-2.0](https://github.com/strazahq/straza/blob/main/LICENSE-APACHE) |
| `kits/`, the agent SDKs | Apache-2.0 |
| `pkg/`, the API contract and the client-facing types | Apache-2.0 |
| `adapters/`, the harness mapping tables | Apache-2.0 |
| `plugins/`, the agent skill bundle, including the copy the docs site serves from `/.well-known/skills/` | Apache-2.0 |
| `.claude-plugin/marketplace.json`, the plugin catalog at the repository root | Apache-2.0 |
| The prebuilt connector `deploy/compose/eval-stack/midpoint/connectors/universal-rest-connector.jar` for the evaluation stack, a separate program | Apache-2.0, its own license, with the license and notice files beside it |
| The Straza approver app, in its own repository | Apache-2.0 |

## What the split means


The AGPL lets you run, study, change and share the platform, and asks one thing of a changed version that users interact with over a network: section 13 says such a version must offer those users its corresponding source, at no charge, from a network server. A fork or a hosted derivative therefore stays open source, and wrapping a modified Straza in a closed service is not a way around it. The Apache license on the specifications, the kits, the API types and the adapter tables grants you a copyright license and a patent license to use, change and redistribute them, with the notices kept, and imposes no copyleft, so code that you write against them never inherits the AGPL, and an independent implementation of the spec is welcome by design.


Running an unmodified copy inside your organization creates no obligation under either license, because the AGPL's network clause applies to a modified version and the rest of its terms govern conveying copies. If the AGPL does not work for your organization, a commercial license is available, and the contact for that, or for deployment and integration work, is hello@straza.ai.

## Third-party files


Vendored third-party files keep their own licenses. The console carries two kinds of third-party source: the QR encoder `qrcodegen.js` by Project Nayuki, with its MIT notice in its own header, and 18 interface components generated from shadcn/ui, under the MIT license in the LICENSE file beside them. Everything else the console is built from, the React runtime included, arrives as an npm package pinned to an exact version in the console's manifest and to a content hash in its lock file, and the console serves the notices of those packages at `/console/THIRD_PARTY_NOTICES.txt`. The Go dependencies carry the licenses their modules declare. Each release lists them in its software bill of materials and reproduces their notices in `THIRD_PARTY_NOTICES.txt`, in every archive and in the container image under `/licenses/`. Two typefaces ship with the docs site, Atkinson Hyperlegible Next and Atkinson Hyperlegible Mono, under the SIL Open Font License 1.1, with its text in `website/static/assets/fonts/OFL.txt`.

The licenses grant no right to use the Straza name or logo. [TRADEMARKS.md](https://github.com/strazahq/straza/blob/main/TRADEMARKS.md) says how they may be used.

Copyright 2026 Patrik Rovňak and the Straza contributors. Published by SynapTech s. r. o. under an exclusive license from Patrik Rovňak.
