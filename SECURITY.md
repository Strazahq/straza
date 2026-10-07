# Security policy

Straza is a security product. A vulnerability in it can let an agent do what its
operator forbade, so reports take priority over feature work.

## Reporting a vulnerability

Do not open a public issue for a security bug.

Report it privately through [GitHub private vulnerability reporting](https://github.com/strazahq/straza/security/advisories/new)
on this repository, or by email to security@straza.ai. Include what you can: the
affected component (`strazad`, `strazactl`, the `straza` client or a wire format under
`spec/`), the version or commit, the steps that reproduce it and your view of the
impact. A proof of concept is welcome. Test only against systems you own.

You get an acknowledgment within 72 hours and a triage verdict (accepted, duplicate or
not a vulnerability, with the reasoning) within 7 days. An accepted report gets a fix
or a documented mitigation with a target of 90 days, ordered by severity. The highest
class is a kill-switch bypass, a policy-decision bypass, credential exposure to an
agent, and snapshot or attestation forgery. Disclosure is coordinated: we agree a
publication date with you, credit you in the advisory unless you decline, and publish
a GitHub Security Advisory that names the fixed versions.

## Supported versions

The latest minor release of the current major receives security fixes. Older releases
get fixes only for critical severities, on a best-effort basis.

## Scope

The security model is at https://docs.straza.ai/security/security-model/. The
invariants it names define the scope: decisions fail closed, no upstream tool
credential reaches the agent's machine, policy snapshots are signed, and the audit
path stays off the hot path. A violation of any of them is in scope and serious by
definition.

A hook-layer bypass through interpreter indirection, such as `python3 script.py`
running what the hook never saw, is a documented limitation with layered mitigations
and not a vulnerability by itself. A user-mode hook on a machine where the agent can
run anything is advisory and not a security boundary, and an MCP server that a user
adds to a harness outside the gateway is that user's responsibility, so a bypass
through either is not a vulnerability by itself. A bypass of the MCP gateway, or of a
deny that the documentation claims is covered at the hook layer, is in scope.

Releases ship with a software bill of materials and a cosign signature. Report any
mismatch between the published checksums or signatures and the artifacts at once.

## Verifying a release

Each release archive comes with `checksums.txt`, its cosign signature and certificate,
and an SBOM. The verification recipe is at https://docs.straza.ai/get-started/install/.
At runtime, policy snapshots and session tokens are Ed25519-signed. A client refuses a
policy snapshot it cannot verify against the keys it pinned at enrollment, and strazad
refuses a session token it cannot verify.
