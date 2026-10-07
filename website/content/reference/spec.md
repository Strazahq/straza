---
title: The spec
description: The ten wire-format specifications Straza publishes, what each one fixes, and how to prove an implementation against them.
pagetype: reference
weight: 60
draft: false
keywords: spec wire format policyset events scim
---


Read this page when you build something that works with Straza without its code, such as a gateway, an agent kit or a provisioning pipeline. The Straza spec is ten documents in the `spec/` folder of the repository, and together they fix every byte that crosses a process boundary in Straza. Each document records its own revisions, so read the current one there.

Each specification is a `SPEC.md` written in RFC 2119 words. Six carry a JSON Schema: the hook profile, PolicySet, app manifest, events, attestation and harness config. The PolicySet, app manifest, events and harness config specifications also carry at least three valid and three invalid examples. The folder is licensed Apache-2.0, while the server and the clients are AGPL-3.0-only, so an independent implementation stays unencumbered.

## The ten specifications


| Specification | What it fixes | Who writes it and who reads it |
|---|---|---|
| [Agent governance hook profile](https://github.com/strazahq/straza/blob/main/spec/hook-profile/SPEC.md) | The canonical event every harness dialect normalizes into, the tool taxonomy, the meaning of allow, deny and reason, and the Tier 1 and Tier 2 conformance levels. A decision's obligations list is always empty. The profile holds one mapping table per harness version, for Claude Code, Codex CLI, Gemini CLI and the Python agent kit. | `straza hook` implements it, and so does any third-party hook that claims Tier 1. |
| [PolicySet](https://github.com/strazahq/straza/blob/main/spec/policyset/SPEC.md) | The YAML policy document and how it is evaluated: deny-overrides combining, profile defaults, `require` predicates, `serverCheck`, glob, regex and argv matching with `${workspace}`, and the Rego escape hatch, which can only tighten a decision. | Policy authors write it. strazad validates, activates and compiles it, and every decision point evaluates it. The decision tables under `spec/conformance/decisions/` test the semantics. [PolicySet grammar]({{< relref "reference/policyset-grammar.md" >}}) lists every key. |
| [App manifest](https://github.com/strazahq/straza/blob/main/spec/app-manifest/SPEC.md) | The `App` document: a verbatim MCP registry `server.json` under `server`, the `straza` block of runtime, credential, exposure and limits, and the rules for importing a registry entry. | App authors write it, and strazad parses it. The import goldens under `spec/conformance/registry/` pin the import. |
| [Events](https://github.com/strazahq/straza/blob/main/spec/events/SPEC.md) | The CloudEvents 1.0 envelope, the subjects under `straza.audit.`, `straza.policy.`, `straza.revocation.`, `straza.apps.` and `straza.identity.`, the smallest payload of each type, the hash-chain record and the client push channel. | strazad and the straza client produce the events. Sinks, SIEM consumers and chain verifiers read them. [Events and the audit record]({{< relref "reference/events.md" >}}) lists every type. |
| [SCIM profile](https://github.com/strazahq/straza/blob/main/spec/scim-profile/SPEC.md) | The strict SCIM 2.0 subset an identity manager speaks to Straza: the resources, operations and filters, roles rendered as groups, and `active: false` as revocation. | Your identity manager speaks the client side. strazad's SCIM server must pass every transcript under `spec/conformance/scim/`, and the transcripts double as request examples. |
| [Attestation](https://github.com/strazahq/straza/blob/main/spec/attestation/SPEC.md) | The check-in payload with the hashes of the binary, the managed config and each harness's hook wiring, the harness identity and the managed flag. It also fixes the levels `managed`, `advisory` and `none`, and how the server checks them at session start. | The straza client measures and sends. strazad computes the level, and a policy may require a minimum. |
| [Session token](https://github.com/strazahq/straza/blob/main/spec/session-token/SPEC.md) | The claims of the Ed25519 JWT, its lifetime of 300 seconds, the refresh rules, the key discovery at `/.well-known/straza/jwks.json` and how revocation works. | strazad issues the token. The straza client, the gateway and any third-party gateway verify it locally against the published keys, with no call per request. |
| [Harness config](https://github.com/strazahq/straza/blob/main/spec/harness-config/SPEC.md) | The managed harness configuration the server renders and signs: the hook and MCP wiring for each harness and platform, exactly the bytes a managed install writes, its signing input and its verification rules. It also fixes how the expected hashes are published, where retiring a row is the policy for stale versions. | strazad renders and signs it at start, and `straza install --managed` writes it. The attestation check at session start makes any divergence visible. |
| [Policy snapshot](https://github.com/strazahq/straza/blob/main/spec/snapshot/SPEC.md) | The signed CBOR envelope that `/v1/snapshot` serves: the payload fields, the content-addressed id, the verification rules and the offline grace. | strazad compiles and signs it. Every decision point verifies it against the keys it pinned before it adopts it. |
| [Canonical object documents](https://github.com/strazahq/straza/blob/main/spec/objects/SPEC.md) | The version-control form of configuration objects, today `kind: Role` and `kind: Removal`. Each is keyed by name with no ids, sorts every list and states desired state only, so the same document applies to another deployment. It also fixes the bundle a draft reads, a YAML stream of App, Role, PolicySet and Removal documents that a person publishes as one change. | strazad serves the Role document at `GET /v1/admin/roles/{id}/export`, and `strazactl roles export`, the console and GitOps pipelines read that one serializer. Drafts read the bundle. |


Who may fetch the snapshot differs by profile, as [Standalone and enterprise compared]({{< relref "reference/standalone-and-enterprise.md#snapshot" >}}) shows.

## How a specification changes


Every specification is at `v1beta1` except two. The harness config is at `v1alpha1`, because it was added after the `spec-v1.0` tag, and its shapes may still change with migration notes. The snapshot specification describes format 1 as it was first built, and a JSON Schema with fixtures is owed at the first change of the format.

Each document changes by revisions that it numbers or dates. These rules hold for every revision:

- A revision adds and never removes. A field is never removed or renamed, and an optional request field never becomes required.
- Every change lands as one commit that carries the schema, the example and conformance fixtures and the version bump. The release notes name it.
- The previous version's fixtures still replay green against the new decoder.
- Strict clients refuse fields they do not know, so a server rolls out its clients before it writes a new revision's fields.

Changes made before the first public version are the stated exceptions, and each specification records its own. Examples are the PolicySet revisions that narrow which built-ins a Rego module may call, and the snapshot route that requires a session token under the enterprise profile. From `v1` on, a breaking change means a new major version with migration notes.

## Validate a document


`strazactl spec validate` checks a document against the embedded validator of one of three kinds, `policyset`, `app` or `event`. It needs no server. The validators are tested to agree with the JSON Schemas on the whole example corpus, so a third party may use the schemas directly instead.

{{< command terminal="Terminal" purpose="any machine" >}}
```sh
strazactl spec validate policyset -f rules.yaml
```
{{< /command >}}

{{< see >}}`rules.yaml: policyset OK`. A document the validator refuses prints each reason, and the command exits with status 1.{{< /see >}}

## Prove a hook implementation


A hook implementation proves Tier 1 conformance with `strazactl spec conformance`. Pass your hook's command with `--cmd`, and the runner puts the path of the conformance policy set where `{policy}` stands. Your hook must enforce that policy set, which holds the standalone profile's defaults. Straza's own client passes with this command.

{{< command terminal="Terminal" purpose="any machine" >}}
```sh
strazactl spec conformance --cmd "straza hook --conformance-policy {policy}"
```
{{< /command >}}

{{< see >}}One `PASS` line per case, and a last line that ends with `(Tier-1 conformance PASS)`.{{< /see >}}

The runner replays the published corpus, which covers the Claude Code, Codex and Gemini dialects and the Python kit. For each case it sets `STRAZA_HARNESS`, writes the payload to stdin and reads your hook's answer. It judges the decision encoding, that deny reasons reach the agent, and that malformed input fails closed.

The suite cannot see what a hook adds to the agent's context at session start or how it spools audit records, so your own tests cover those two. A reference hook of about 150 lines of standard-library Go passes the same suite.

Tier 2 is any MCP client that reaches tools through the gateway. It needs nothing from the spec beyond the session token.
