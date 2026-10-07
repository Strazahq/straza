package agentguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/agentguard/trace"
)

// The decision journal and debug trace (state/trace.jsonl) are the client's
// normal-operation diagnostic trail, the sibling of the error log
// (errorlog.go): where errors.jsonl records what FAILED, the journal records
// what was DECIDED (tool name, effect, rule, snapshot, escalation branch,
// server status and correlation id, timing), one content-free line per
// decision, always on. `straza trace on` opens a time-boxed debug window that
// adds detail beside it; the install config's `trace:` field can switch the
// whole surface off or force debug. The writer, the toggle and the readers
// live in the trace sub-package (no harness knowledge); this file is the glue:
// the store-scoped logger, the verbs behind `straza trace`, and the doctor
// check. The error log is never affected by any of this: errors are logged
// regardless.

// stateDir returns the per-user state directory (~/.straza/state).
func (s *Store) stateDir() string { return filepath.Join(s.root, "state") }

// TracePath returns the live decision journal / debug trace file
// (state/trace.jsonl; the single rotated generation sits beside it as ".1").
func (s *Store) TracePath() string { return s.statePath(trace.FileName) }

// Trace returns this store's trace logger, built lazily on first use (one
// config read + one toggle stat per process, cached for the process lifetime;
// long-lived callers may SetRecheck on it). A nil store yields a nil logger,
// and a nil logger writes nothing, so call sites never guard.
func (s *Store) Trace() *trace.Logger {
	if s == nil {
		return nil
	}
	s.traceOnce.Do(func() {
		cfg, _ := s.LoadConfig() // not enrolled = no config = the default level
		s.tracer = trace.New(s.stateDir(), cfg.Trace, time.Now())
	})
	return s.tracer
}

// ConfigSource names the config file whose `trace:` field decides: the
// managed file when present (LoadConfig lets it win whole-file), otherwise the
// user's config.yaml. Messages that tell a human which file to edit use it.
func (s *Store) ConfigSource() string {
	if _, err := os.Stat(ManagedConfigPath()); err == nil {
		return ManagedConfigPath()
	}
	return s.configPath()
}

// TraceState is the resolved trace configuration of this install, as
// `straza trace status` and the doctor report it.
type TraceState struct {
	// Level is the effective level as of the query.
	Level trace.Level
	// ConfigLevel is the config file's trace field ("" when absent).
	ConfigLevel string
	// ConfigPath is the config file that decides (ConfigSource).
	ConfigPath string
	// Toggle and TogglePresent describe the user's debug window file.
	Toggle        trace.Toggle
	TogglePresent bool
	// Path is the live trace file; Summary describes both generations.
	Path    string
	Summary trace.Summary
}

// TraceStatus resolves the trace state as of now. Read-only.
func TraceStatus(store *Store, now time.Time) TraceState {
	cfg, _ := store.LoadConfig()
	tg, present, _ := trace.ReadToggle(store.stateDir())
	return TraceState{
		Level:         trace.Resolve(cfg.Trace, tg, now),
		ConfigLevel:   cfg.Trace,
		ConfigPath:    store.ConfigSource(),
		Toggle:        tg,
		TogglePresent: present,
		Path:          store.TracePath(),
		Summary:       trace.Summarize(store.stateDir()),
	}
}

// EnableTrace opens a debug window of d (1s..24h) starting now by writing
// the toggle. Under config `trace: off` it refuses with an error naming the
// deciding file; under `trace: debug` it writes nothing and returns the state
// (debug is already forced on; the caller says so). The returned state
// reflects the window just opened.
func EnableTrace(store *Store, d time.Duration, now time.Time) (TraceState, error) {
	if d < time.Second || d > trace.MaxWindow {
		return TraceState{}, fmt.Errorf("debug window must be between 1s and 24h, got %s", trace.DurationString(d))
	}
	st := TraceStatus(store, now)
	switch st.ConfigLevel {
	case "off":
		return st, fmt.Errorf("tracing is disabled by %s (trace: off); the debug window cannot be turned on here", st.ConfigPath)
	case "debug":
		return st, nil
	}
	tg := trace.Toggle{Level: "debug", Until: now.Add(d)}
	if err := trace.WriteToggle(store.stateDir(), tg); err != nil {
		return st, fmt.Errorf("write trace toggle: %w", err)
	}
	st.Toggle, st.TogglePresent, st.Level = tg, true, trace.Debug
	return st, nil
}

// DisableTrace closes the debug window (removes the toggle); idempotent. The
// journal stays on.
func DisableTrace(store *Store) error { return trace.RemoveToggle(store.stateDir()) }

// TraceRecords returns the last n records (n <= 0 = all), oldest first, and
// the count of unreadable lines among them.
func TraceRecords(store *Store, n int) ([]trace.Record, int) {
	return trace.Tail(store.stateDir(), n)
}

// traceCheck is the doctor's `trace` row: what the journal holds, whether a
// debug window is open, expired, forced, or the surface is off by config.
// It never FAILs: an expired window is housekeeping (WARN), not a fault.
func traceCheck(store *Store, now time.Time) Check {
	st := TraceStatus(store, now)
	journal := journalSummary(st.Summary)
	switch st.ConfigLevel {
	case "off":
		return Check{"trace", checkOK, fmt.Sprintf("journal off (%s: trace: off)", st.ConfigPath), ""}
	case "debug":
		return Check{"trace", checkOK, journal + "; debug forced on by " + st.ConfigPath, ""}
	case "", "journal":
	default:
		return Check{"trace", checkWarn,
			fmt.Sprintf("trace: %q in %s is not off, journal or debug; treated as journal", st.ConfigLevel, st.ConfigPath),
			"set trace to off, journal or debug in that file (or delete the line for the default)"}
	}
	switch {
	case st.Toggle.Active(now):
		return Check{"trace", checkOK,
			fmt.Sprintf("%s; debug on until %s (%s left)", journal,
				st.Toggle.Until.UTC().Format(time.RFC3339), trace.DurationString(st.Toggle.Until.Sub(now))), ""}
	case st.Toggle.Expired(now):
		return Check{"trace", checkWarn,
			fmt.Sprintf("%s; debug expired %s ago, file %d bytes", journal,
				trace.DurationString(now.Sub(st.Toggle.Until)), st.Summary.Size),
			"run `straza trace off` to clear the expired window, or `straza trace on` for a fresh one; the records stay readable with `straza trace show`"}
	}
	return Check{"trace", checkOK, journal + "; debug off", ""}
}

// journalSummary renders "journal N record(s) (last: ...)" for the doctor
// and the status verb.
func journalSummary(s trace.Summary) string {
	if s.Records == 0 {
		out := "journal on, 0 records"
		if s.Unreadable > 0 {
			out += fmt.Sprintf(" (%d unreadable line(s))", s.Unreadable)
		}
		return out
	}
	out := fmt.Sprintf("journal %d record(s) (last: %s)", s.Records, lastRecordLine(s.Last))
	if s.Unreadable > 0 {
		out += fmt.Sprintf(", %d unreadable line(s)", s.Unreadable)
	}
	return out
}

// lastRecordLine names the newest record briefly: timestamp, then tool,
// effect and rule when present (a decision), else the record kind.
func lastRecordLine(r *trace.Record) string {
	if r == nil {
		return "-"
	}
	parts := []string{r.TS.UTC().Format(time.RFC3339)}
	for _, k := range []string{"tool", "effect", "rule"} {
		if v, ok := r.Attrs[k].(string); ok && v != "" {
			parts = append(parts, v)
		}
	}
	if len(parts) == 1 {
		parts = append(parts, r.Msg)
	}
	return strings.Join(parts, " ")
}
