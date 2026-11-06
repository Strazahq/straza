# Harness Config, v1alpha1

Status: **alpha** (new artifact, added 2026-08-09 after the spec-v1.0 tag;
shapes may still change with migration notes until beta).

## What this is

Tier-1 governance depends on hook wiring living inside each harness's
managed settings file. The harness-config artifact is that wiring as a
**server-rendered, signed document**: exactly the bytes a managed install
writes into an empty layout, published by the platform so that the served
artifact, the file on disk, and the attestation expected-hash registry row
all hash identically. Distribution is pull-only and the write stays with the
privileged installer: the platform never reaches into a machine, it only
says what legitimate content is, and the attestation check at session start
(spec/attestation) is what makes divergence visible.

## Document

Served at `GET /v1/harness-config?harness=<h>&platform=<goos>`
(`pkg/api/openapi.yaml`), `application/json`, ETag = sha256 hex of the served
body. Schema: `harness-config.schema.json`.

| Field | Meaning |
|---|---|
| `format` | Envelope format version, `1`. |
| `kind` | `harness-config`, the artifact discriminator. |
| `harness` | Harness the artifacts wire (`claude-code`, `codex`, `gemini`). |
| `platform` | Target GOOS (`linux`, `darwin`, `windows`). Content varies by OS only, never by architecture. |
| `artifacts[]` | One entry per managed file. |

Each artifact:

| Field | Meaning |
|---|---|
| `artifact` | The attestation measurement key this file lands under: `hooks.<harness>` (the managed hook wiring file), `mcp.<harness>` (a vendor-documented managed MCP registration file, where one exists). |
| `content` | The file bytes, base64 (std). A consumer MUST write them verbatim: any local merge changes the hash and reads as tamper. |
| `content_hash` | `sha256:<hex64>` over the content bytes: the same form attestation measurements and registry rows use, so the three are directly comparable. |
| `key_id` | Signing key id, resolved via `/.well-known/straza/snapshot-keys.json` (the keys an enrolling client pins). |
| `sig` | ed25519 signature, base64 (std). |

## Signing

Signatures use the platform's snapshot signing key (ed25519) over a
domain-separated, NUL-delimited input:

```
"straza.harness-config.v1" 0x00 harness 0x00 platform 0x00 artifact 0x00 content-bytes
```

The domain prefix keeps a harness-config signature and a policy-snapshot
signature (same key, raw CBOR payload) from ever being confusable; the NUL
delimiters make the field split unambiguous, so `harness`, `platform`, and
`artifact` MUST be non-empty and NUL-free (a signer MUST refuse otherwise).

## Verification (consumer, before any use)

1. `format` MUST be `1` and `kind` MUST be `harness-config`; reject others.
2. The document MUST carry at least one artifact.
3. For every artifact: recompute sha256 over `content` and compare to
   `content_hash`; resolve `key_id` against the pinned snapshot keys; verify
   the signature over the input above using the DOCUMENT's `harness` and
   `platform` (a document whose envelope fields were rewritten fails here).
4. Any failure MUST be treated as fail-closed: do not write, do not trust
   (the invariant that clients verify before use,
   https://docs.straza.ai/security/security-model/).

## Relationship to attestation

Publishing a render registers each `hooks.*` artifact's `content_hash` into
the expected-hash registry (spec/attestation) **additively**, one row per
exact `GOOS/GOARCH`: platform-blank rows are forbidden for these artifacts
because they would put every OS's hash into every OS's allowed set, letting
a file that carries another platform's content (hook paths that cannot exist
on this OS: governance off while wired-looking) still attest `managed`.
Multiple registered rows per artifact are the upgrade window; **retiring a
row is the stale-version policy**: a machine still on that content drops to
`att=none` at its next session start and `governance.minAttestation`
decides. `mcp.<harness>` is served but deliberately not registered: it is
not a measured attestation artifact today, and the registry fails closed on
any registered-but-unreported artifact, so registering it would break every
already-deployed managed fleet at once.

## Rendering constraints

Rendered content assumes the vendor-default managed layout (default managed
binary path per OS, default `%ProgramData%` on Windows). A fleet on a custom
binary directory or a relocated `%ProgramData%` legitimately produces
different bytes and MUST fall back to local rendering plus per-machine hash
registration (the pre-existing `install --managed` flow). The platform
self-verifies every document before serving it and never serves what it
cannot re-verify.

Examples in `examples/` (3 valid, 3 invalid) are generated deterministically
and replayed by `internal/harnesscfg` conformance tests; `examples/README.md`
documents the throwaway example key and the regeneration command.

Changes to this artifact follow the spec process: schema + fixtures +
CHANGELOG + version bump in the same PR.

## Changelog

- **v1alpha1** (2026-08-09): initial artifact: envelope, signing
  input, verification algorithm, additive registry publication with exact
  per-GOOS/GOARCH rows, retirement-as-stale-version semantics.
- **v1alpha1 content** (2026-10-24): the linux `hooks.claude-code` artifact
  gains an `env` table with one git config include: `GIT_CONFIG_COUNT` `1`,
  `GIT_CONFIG_KEY_0` `include.path` and `GIT_CONFIG_VALUE_0`
  `/run/straza-service/git/hosts.conf`, the host file `straza service`
  writes for git through a Straza git grant. The envelope, the signing input
  and the verification do not change. The artifact's `content_hash` changes
  once, so publishing adds a registry row beside the old one, and a machine
  on the old wiring keeps attesting until that row is retired. The four
  examples built from the linux claude-code render are regenerated.
- **v1alpha1 content** (2026-10-24): the windows `hooks.claude-code`
  artifact gains the same `env` table, with `GIT_CONFIG_VALUE_0`
  `C:\ProgramData\straza-service\git\hosts.conf`, the host file
  `straza service` writes on Windows. As for linux, the envelope, the
  signing input and the verification do not change, the `content_hash`
  changes once and publishing adds a registry row beside the old one. No
  example is built from the windows render, so none is regenerated.
- **v1alpha1 content** (2026-10-26): the darwin artifacts of claude-code,
  codex and gemini run `/opt/straza/bin/straza`, where the Straza package
  for macOS installs the binary, instead of `/usr/local/bin/straza`. The
  darwin `hooks.claude-code` artifact also gains the same `env` table as
  linux and windows, with `GIT_CONFIG_VALUE_0`
  `/opt/straza/run/git/hosts.conf`, the host file `straza service` writes
  on macOS. As for linux and windows, the envelope, the signing input and
  the verification do not change, each `content_hash` changes once and
  publishing adds a registry row beside the old one.
  `valid-gemini-darwin.json` is regenerated, because it is built from the
  darwin gemini render.
- **v1alpha1 content** (2026-11-04): the `PreToolUse` hook of every
  `hooks.claude-code` artifact, on linux, darwin and windows, gains
  `"onFailure": "block"`, so Claude Code 2.1.295 and later blocks a tool
  call when the straza hook cannot start, is killed, times out or prints
  output Claude Code cannot read, instead of running it unchecked. Earlier
  Claude Code releases ignore the key. No other hook and no codex or gemini
  artifact changes. As before, the envelope, the signing input and the
  verification do not change, each claude-code `content_hash` changes once
  and publishing adds a registry row beside the old one. The four examples
  built from the linux claude-code render are regenerated.
