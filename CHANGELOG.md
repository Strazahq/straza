# Changelog

All notable changes to Straza are listed here, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

The Unreleased section holds what the main branch carries since the last release. Each
release also has notes on the [releases page](https://github.com/strazahq/straza/releases),
and the [changelog page of the docs](https://docs.straza.ai/project/changelog/) says how
Straza numbers its binaries, its API and its wire formats.

## [Unreleased]

## [1.1.2] - 2026-10-14

### Fixed

- `straza mcp` checks in again when strazad ends its session. Before, it kept sending the
  refused token, and every tool call failed as Unauthorized for up to about four and a half
  minutes.
- A new session carries its user type, agency mode and swarm from the first hook call. A
  PolicySet scoped by `match.identity` now decides from the start of a session, where it
  applied only after the first refresh.
- With two or more replicas, a burst of check-ins on one replica no longer makes check-ins
  on another answer 503.
- Six built-in tools of current Claude Code releases are mapped into the tool taxonomy:
  `ListAgents`, `ReadNotifications`, `FetchInboxMessage`, `SendFeedback`, `ArtifactComments`
  and `ArtifactData`. They fell to `other`, which the enterprise profile denies. A governed
  machine needs the new `straza` client to get the mapping.
- A draft's contact to a server that never answers is always reported as a timeout. It
  could read "the connection failed before it answered".

### Changed

- `straza daemon` prints strazad's reason and the next step when its session refresh is
  refused.
- The README is rewritten around a Kubernetes example and the demo stack.

## [1.1.1] - 2026-10-10

### Security

- Built with Go 1.26.9 and golang.org/x/net 0.60.0, which fix twelve Go advisories that
  Straza's code reaches, among them an HTTP/2 crash and HTTP/2 memory exhaustion in strazad.
  Upgrade every strazad, and the clients when you can.

### Changed

- The container image carries the OCI source, description and licenses labels.

## [1.1.0] - 2026-10-08

The first public release.

### Added

- `strazad`, the server: policy decisions, the MCP gateway, the admin API and web console,
  SCIM 2.0 intake and the hash-chained audit.
- `straza`, the client that runs beside the agent: hooks for Claude Code, Codex CLI and
  Gemini CLI, the MCP proxy and `straza exec`.
- `strazactl`, the admin command line.
- Signed release archives for Linux, macOS and Windows, the container image
  `ghcr.io/strazahq/straza` and the Helm chart under `deploy/helm/straza`.

[Unreleased]: https://github.com/strazahq/straza/compare/v1.1.2...HEAD
[1.1.2]: https://github.com/strazahq/straza/compare/v1.1.1...v1.1.2
[1.1.1]: https://github.com/strazahq/straza/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/strazahq/straza/releases/tag/v1.1.0
