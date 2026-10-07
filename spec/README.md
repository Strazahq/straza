# The Straza spec

This directory is the published contract for identity-governed AI agents:
versioned schemas, normative prose, per-harness mapping tables, and a
runnable conformance kit. It exists so that *other* implementations
(gateways, agent kits, IGA provisioning pipelines) can interoperate with
Straza (or replace it) without reading its source. If a byte crosses a
process boundary in Straza, its shape is defined here, not in Go code
(invariant: no wire format ships without a spec source of truth).

Everything below is at **v1beta1** and frozen for the `spec-v1.0` tag:
breaking changes from here on mean a new major version with migration notes.
Two post-tag additions carry their own status: `harness-config/` is
**v1alpha1** (shapes may still change with migration notes until beta) and
`snapshot/` is a descriptive record of a format that has not changed since it was first built.

## Artifacts

| Artifact | Directory | What it defines |
|---|---|---|
| Agent Governance Hook Profile | `hook-profile/` | The canonical hook event every harness dialect normalizes into (Claude Code, Codex CLI, Gemini/Antigravity; mapping tables in `mappings/`), the tool taxonomy, decision semantics (allow/deny/reason/obligations), and the Tier-1/Tier-2 conformance levels. |
| PolicySet | `policyset/` | The declarative YAML policy document and its normative evaluation semantics: deny-overrides combining, profile defaults, `require:` predicates, `serverCheck`, pattern matching (glob/regex/argv, `${workspace}`), monotonic Rego escape hatch. |
| App Manifest | `app-manifest/` | The `App` document: a verbatim MCP-registry `server.json` under `server:` plus `straza:` deployment extensions (runtime, credential injection, exposure, limits), and the deterministic registry-import rules. |
| Canonical Objects | `objects/` | The version-control face of configuration objects: the `Role` document that `GET /v1/admin/roles/{id}/export` serves, and the bundle a draft reads, a YAML stream of App, Role, PolicySet and Removal documents that a person publishes as one change. |
| Events | `events/` | The CloudEvents 1.0 envelope profile: subject taxonomy (`straza.audit.* / policy.* / revocation.* / apps.* / identity.*`), payload schemas, and the tamper-evident hash-chain audit record format. |
| SCIM Profile | `scim-profile/` | The strict SCIM 2.0 subset an IdM speaks to Straza: supported resources, ops and filters; Groups render exported roles and take membership writes; `active:false` ⇒ revocation semantics; the one credential that opens it (an admin API token carrying the scim grants). |
| Attestation | `attestation/` | The check-in payload (artifact hashes, harness identity, managed flag), the `managed|advisory|none` levels, and the expected-hash registry verification rules. |
| Session Token | `session-token/` | The JWT claim set, TTL/refresh rules, JWKS discovery, and revocation semantics for the 300-second session token. |
| Harness Config | `harness-config/` | The server-rendered managed harness config: a signed artifact set (`hooks.<harness>`, `mcp.<harness>`) written verbatim by managed installs, its domain-separated signing input and verification rules, and the additive expected-hash-registry publication whose row retirement is the stale-version policy. **v1alpha1** (added post-tag, 2026-08-09). |
| Policy Snapshot | `snapshot/` | The signed CBOR envelope `/v1/snapshot` serves and clients pin-verify: payload fields, content-addressed id, verification rules, offline-grace semantics. **Descriptive backfill** (as-built; schema + fixtures land with the first format bump). |

Format rules: every wire format has a JSON Schema (draft 2020-12; YAML
documents validate after YAML→JSON), normative prose in that artifact's
`SPEC.md` using RFC-2119 keywords, and ≥3 valid + ≥3 invalid examples in
`examples/`.

## Versioning

Schema ids follow `straza.dev/<artifact>/v1beta1 → v1`
Current: **v1beta1 across all artifacts**, the
stabilization pass before v1; no wire changes were made in the beta bump.
Documents declare `apiVersion: straza.dev/v1beta1` (PolicySet, App).
Ids under any other domain are NOT accepted: a clean break, no legacy
aliases. From v1: additive changes = minor,
breaking = new major with migration notes. Any change lands as schema +
fixtures + version bump in one PR, and the release notes name it.

## Validating documents

`strazactl spec validate <policyset|app|event> -f <file>` checks any
document against the embedded validators, which are tested to agree with
the JSON Schemas on the whole example corpus. Third-party implementations
can use the schemas directly; the example corpora double as accept/reject
test suites.

## Claiming conformance

- **Tier 2** (gateway-only): any MCP client that reaches tools through the
  gateway. Nothing to implement from this spec beyond the session token.
- **Tier 1** (full hook PEP): implement the hook profile and prove it:

  ```
  strazactl spec conformance --suite hook-profile --cmd "<your-hook-command>"
  ```

  The runner replays `conformance/tier1/` (43 cases across the Claude Code,
  Codex and Gemini dialects and the Python kit) against your command: per
  case it sets `STRAZA_HARNESS`, writes the payload to stdin, and judges
  your decision encoding,
  including that deny reasons are relayed and malformed input fails
  closed. Your implementation must enforce `conformance/tier1/policy.yaml`
  (standalone profile defaults); `{policy}` in the command line is
  replaced with that file's path. The protocol is normative in the
  `cases.yaml` header and hook-profile `SPEC.md` §Conformance.

  `conformance/tier1/hello-hook/` is a reference implementation of about
  150 lines of standard-library Go, written against nothing but these
  documents; Straza's own kit passes the same suite via
  `straza hook --conformance-policy {policy}`.

The wider `conformance/` tree carries the rest of the shared fixtures:
`hooks/` (per-harness payload → canonical event), `decisions/` (policy
decision tables), `registry/` (registry-import goldens), `scim/`
(request/response transcripts). Straza's own test suites consume these
files directly: they are the contract, not documentation of it.

## License

spec/ is Apache-2.0 (see LICENSE-APACHE at the repo root), deliberately permissive so independent implementations stay unencumbered; the repository core (the server, `straza`, `strazactl`, and console) is AGPL-3.0-only (LICENSE). Proposals and questions
via issues/PRs; once external implementers exist, breaking changes will go
through a lightweight proposal template first.

The files in `conformance/registry/` that end in `.server.json` reproduce
public MCP registry entries as test input. Their descriptions belong to the
servers' publishers.
