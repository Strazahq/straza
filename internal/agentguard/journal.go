package agentguard

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/strazahq/straza/internal/agentguard/trace"
	"github.com/strazahq/straza/internal/policy"
)

// The hook lane's decision journal record (trace package, schema v1): one
// content-free line per decision. Tool names, rule ids, snapshot ids,
// effects, the server status and correlation id, and timings are recorded;
// the command, argv, paths, prompt and reason text never are (the audit
// spool and the server hold those). Every emit is best-effort by
// construction of the trace writer, so nothing here can alter a decision,
// an exit code or a protocol byte.

// decisionFacts carries what the decision path learned beside the Decision
// itself: timing, the snapshot that decided, the escalation branch and the
// server answer it depended on, the spool outcome, and whether the deny was
// a fail-closed answer rather than a rule's.
type decisionFacts struct {
	start       time.Time
	snapshot    string
	escalation  string // none | classify | approve | server | gateway ("" = none)
	status      int
	correlation string
	spooled     bool
	spoolErr    error
	failClosed  bool
}

// journalDecision writes the hook-lane "decision" record. A nil logger or an
// Off level writes nothing; the attributes are built only past that check so
// the default level costs one comparison.
func journalDecision(tl *trace.Logger, n Normalized, d policy.Decision, f decisionFacts) {
	if tl.Level() == trace.Off {
		return
	}
	attrs := make([]slog.Attr, 0, 16)
	attrs = append(attrs,
		slog.String("lane", "hook"),
		slog.String("harness", trace.Short(n.HarnessName, 64)),
		slog.String("event", n.Event.Kind),
		slog.String("tool", trace.Short(n.Event.Tool, 128)),
	)
	if n.Event.ToolName != "" {
		attrs = append(attrs, slog.String("tool_name", trace.Short(n.Event.ToolName, 128)))
	}
	if n.Event.App != "" {
		attrs = append(attrs, slog.String("app", trace.Short(n.Event.App, 128)))
	}
	attrs = append(attrs, slog.String("effect", d.Effect))
	if d.RuleID != "" {
		attrs = append(attrs, slog.String("rule", trace.Short(d.RuleID, 128)))
	}
	if d.SetName != "" {
		attrs = append(attrs, slog.String("set", trace.Short(d.SetName, 128)))
	}
	if f.snapshot != "" {
		attrs = append(attrs, slog.String("snapshot", trace.Short(f.snapshot, 96)))
	}
	esc := f.escalation
	if esc == "" {
		esc = "none"
	}
	attrs = append(attrs, slog.String("escalation", esc))
	if f.status != 0 {
		attrs = append(attrs, slog.Int("status", f.status))
	}
	if f.correlation != "" {
		attrs = append(attrs, slog.String("correlation", f.correlation))
	}
	attrs = append(attrs, slog.Int64("duration_ms", time.Since(f.start).Milliseconds()))
	if f.spooled {
		attrs = append(attrs, slog.String("spool", spoolOutcome(f.spoolErr)))
	}
	if d.Default {
		attrs = append(attrs, slog.Bool("default", true))
	}
	if f.failClosed {
		attrs = append(attrs, slog.Bool("fail_closed", true))
	}
	tl.Journal("decision", attrs...)
}

// spoolOutcome renders the spool append result for the journal: "ok" or
// "err:<class>" (never the error text, which can name a path).
func spoolOutcome(err error) string {
	if err == nil {
		return "ok"
	}
	return "err:" + errClass(err)
}

// errClass names an error's class for the journal without quoting it:
// "timeout" for a blown deadline, the syscall op for a path error ("open",
// "write", ...), "http" for a server refusal, "other" for the rest.
func errClass(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Op
	}
	var se *StatusError
	if errors.As(err, &se) {
		return "http"
	}
	return "other"
}
