// Package agentguard is the client kit's thin client: everything that runs
// beside a harness (Claude Code, Codex, Gemini CLI, the python SDKs) as the
// person or agent who runs it. It holds no key and no token: sessions,
// decisions and the audit spool live in `straza service` (the service
// sub-package), reached over the local API (localapi). The user's straza
// home keeps only the trace setting, the journal, the error log, the trace
// log and the capture watermarks.
//
//   - Thin clients: hook.go, flows.go, normalize.go, adapter.go, exec.go,
//     mcpproxy*.go, enroll.go, serviceclient.go, capture.go, state.go.
//   - The strazad client the service uses: client.go, spool/, trace/.
//   - Install: install*.go, managed*.go, render.go, codextrust.go,
//     geminitrust.go.
//   - Diagnostics: doctor*.go, errorlog.go, tracelog.go, journal.go,
//     conformance.go, coverage.go.
package agentguard
