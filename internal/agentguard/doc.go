// Package agentguard is the client kit: everything that runs on the operator's
// or the agent's machine beside a harness (Claude Code, Codex, Gemini CLI, the
// python SDKs) and keeps it inside policy. It is one package by design, because
// the hook, the MCP proxy, the daemon and the doctor share state and config, so
// the concerns live in file prefixes rather than sub-packages.
//
//   - Decisions: hook.go, flows.go, normalize.go, localpdp.go, adapter.go,
//     exec.go, mcpproxy.go, mcpproxy_redial.go.
//   - Session: client.go, enroll.go, state.go, headless.go, daemon.go,
//     heartbeat.go, pushedge.go.
//   - Install: install*.go, managed*.go, render.go, codextrust.go,
//     geminitrust.go, drain_spawn*.go, managed_unix.go, managed_windows.go.
//   - Diagnostics: doctor*.go, errorlog.go, tracelog.go, conformance.go,
//     capture.go, spoolwrite.go. The trace writer and readers live in the
//     trace sub-package, and the audit buffer lives in the spool sub-package.
package agentguard
