package agentguard

import (
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/agentguard/trace"
)

// The mcp lane's journal records (trace schema v1): the proxy makes no local
// policy decision (the gateway PEP does), so its journal line per tool call
// names the tool, the outcome as the gateway answered it, the HTTP status and
// correlation id of that answer, and the timing; its debug lines follow the
// proxy lifecycle (session, redial, resync, reconcile). Content-free: never
// arguments, never results, never reason text.

// mcpJournalRecheck is how often a long-lived proxy re-reads the trace toggle,
// so `straza trace on` takes effect without restarting the harness.
const mcpJournalRecheck = 30 * time.Second

// journalCall writes one "call" record for a proxied tool call: outcome ok
// (the gateway answered a result), denied (the gateway answered an isError
// result, which is how a policy deny arrives), or error (the call failed in
// transport or the gateway refused the session). revived marks a call that
// rode a freshly revived gateway session.
func (p *mcpProxy) journalCall(tool string, out *mcp.CallToolResult, err error, call *trace.Call, start time.Time, revived bool) {
	if p.tr.Level() == trace.Off {
		return
	}
	outcome := "ok"
	switch {
	case err != nil:
		outcome = "error"
	case out != nil && out.IsError:
		outcome = "denied"
	}
	status, corr := call.Get()
	attrs := make([]slog.Attr, 0, 8)
	attrs = append(attrs,
		slog.String("lane", "mcp"),
		slog.String("harness", trace.Short(p.harness, 64)),
		slog.String("tool", trace.Short(tool, 128)),
		slog.String("outcome", outcome),
	)
	if status != 0 {
		attrs = append(attrs, slog.Int("status", status))
	}
	if corr != "" {
		attrs = append(attrs, slog.String("correlation", corr))
	}
	attrs = append(attrs, slog.Int64("duration_ms", time.Since(start).Milliseconds()))
	if revived {
		attrs = append(attrs, slog.Bool("revived", true))
	}
	p.tr.Journal("call", attrs...)
}

// debugSession writes the "mcp.session" debug record for the governed session
// the proxy runs under (id, snapshot, seconds left); nothing at journal level.
func (p *mcpProxy) debugSession(ses Session) {
	if !p.tr.DebugOn() {
		return
	}
	p.tr.Debug("mcp.session",
		slog.String("session", trace.Short(ses.SessionID, 64)),
		slog.String("snapshot", trace.Short(ses.SnapshotID, 96)),
		slog.Int64("expires_in_s", int64(time.Until(ses.ExpiresAt).Seconds())))
}
