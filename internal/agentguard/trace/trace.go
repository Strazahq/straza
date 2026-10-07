// Package trace is the client kit's local diagnostic trail: a content-free
// decision journal that is always on, plus an opt-in debug window beside it.
// The file sits next to the client error log (state/errors.jsonl). It records
// tool names, rule ids, snapshot ids, effects, HTTP statuses, correlation ids
// and timings. Commands, paths, prompts, reason text and tokens never appear.
//
// The package imports only the standard library and knows nothing about
// harnesses or the store, because agentguard owns the paths and passes the
// state directory in. Every write is best-effort, so tracing can never change
// a decision, an exit code, or a byte on stdout or stderr.
//
// Resolve picks the level. The install's config `trace:` field wins when it
// says off or debug, otherwise the time-boxed toggle in state/trace.json that
// `straza trace on` writes chooses journal or debug. The level resolves once
// per process, and the mcp proxy rechecks so a toggle needs no restart.
package trace

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// Version is the record format version carried as the "v" attribute on
	// every line, so a future fleet drain can ship these records without a
	// format break.
	Version = 1
	// FileName is the live journal/trace file under the state dir; the single
	// rotated generation lives beside it with a ".1" suffix.
	FileName = "trace.jsonl"
	// ToggleName is the user's debug-window toggle under the state dir.
	ToggleName = "trace.json"
	// DefaultWindow is how long `straza trace on` keeps debug on when no
	// duration is given.
	DefaultWindow = time.Hour
	// MaxWindow bounds a debug window: a forgotten toggle expires by itself.
	MaxWindow = 24 * time.Hour
)

// MaxBytes caps one generation of the file; live + one rotation bound the
// total at about 2x this. Var, not const, so tests can shrink it.
var MaxBytes int64 = 2 << 20

// Level is the effective trace level of a process.
type Level int

const (
	// Off writes nothing (config `trace: off`).
	Off Level = iota
	// Journal writes the always-on, content-free decision journal (Info).
	Journal
	// Debug writes the journal plus the debug-window detail (Debug).
	Debug
)

// String renders the level the way config and status print it.
func (l Level) String() string {
	switch l {
	case Off:
		return "off"
	case Journal:
		return "journal"
	case Debug:
		return "debug"
	}
	return "unknown"
}

// Resolve combines the install config level (off | journal | debug | "") with
// the user's toggle: off and debug in the config win outright; anything else
// (journal, empty, or an unknown value, which the doctor names) lets an active
// toggle raise the level to debug and otherwise yields journal.
func Resolve(cfgLevel string, tg Toggle, now time.Time) Level {
	switch cfgLevel {
	case "off":
		return Off
	case "debug":
		return Debug
	}
	if tg.Active(now) {
		return Debug
	}
	return Journal
}

// levelOff is a handler threshold above every level this package emits.
const levelOff = slog.LevelError + 1

func handlerLevel(l Level) slog.Level {
	switch l {
	case Debug:
		return slog.LevelDebug
	case Journal:
		return slog.LevelInfo
	}
	return levelOff
}

// Logger writes journal (Info) and debug (Debug) records to the trace file.
// A nil *Logger is valid and writes nothing, so callers never guard.
type Logger struct {
	stateDir string
	cfgLevel string
	clock    func() time.Time

	mu      sync.Mutex
	recheck time.Duration
	checked time.Time
	level   Level
	lv      *slog.LevelVar
	lg      *slog.Logger
}

// Option configures a Logger at construction.
type Option func(*Logger)

// Recheck makes the logger re-read the toggle at most once per d (0, the
// default, resolves the level once at construction). Long-lived processes use
// it so a toggle written after they started is honoured within d.
func Recheck(d time.Duration) Option {
	return func(l *Logger) { l.recheck = d }
}

// New builds a logger over stateDir, resolving the level from cfgLevel (the
// install config's trace field) and the toggle file as of now.
func New(stateDir, cfgLevel string, now time.Time, opts ...Option) *Logger {
	l := &Logger{stateDir: stateDir, cfgLevel: cfgLevel, clock: time.Now, lv: new(slog.LevelVar)}
	for _, o := range opts {
		o(l)
	}
	h := slog.NewJSONHandler(&appendWriter{path: filepath.Join(stateDir, FileName)}, &slog.HandlerOptions{
		Level:       l.lv,
		ReplaceAttr: replaceTime,
	})
	l.lg = slog.New(h).With("v", Version)
	l.resolve(now)
	return l
}

// resolve re-reads the toggle and sets the handler threshold; callers hold mu
// or are still constructing.
func (l *Logger) resolve(now time.Time) {
	tg, _, _ := ReadToggle(l.stateDir)
	l.level = Resolve(l.cfgLevel, tg, now)
	l.lv.Set(handlerLevel(l.level))
	l.checked = now
}

func (l *Logger) maybeRecheck() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.recheck <= 0 {
		return
	}
	now := l.clock()
	if now.Sub(l.checked) >= l.recheck {
		l.resolve(now)
	}
}

// SetRecheck changes the recheck interval after construction (0 disables).
func (l *Logger) SetRecheck(d time.Duration) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.recheck = d
	l.mu.Unlock()
}

// Level reports the effective level (Off for a nil logger).
func (l *Logger) Level() Level {
	if l == nil {
		return Off
	}
	l.maybeRecheck()
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.level
}

// DebugOn reports whether debug records are being written; callers build
// debug attributes only behind it.
func (l *Logger) DebugOn() bool { return l.Level() == Debug }

// Journal writes one journal record (Info) named kind; a no-op when Off.
func (l *Logger) Journal(kind string, attrs ...slog.Attr) {
	l.emit(slog.LevelInfo, kind, attrs)
}

// Debug writes one debug record named kind; a no-op unless the level is Debug.
func (l *Logger) Debug(kind string, attrs ...slog.Attr) {
	l.emit(slog.LevelDebug, kind, attrs)
}

func (l *Logger) emit(level slog.Level, kind string, attrs []slog.Attr) {
	if l == nil {
		return
	}
	l.maybeRecheck()
	l.lg.LogAttrs(context.Background(), level, kind, attrs...)
}

// replaceTime renames slog's time key to "ts" and formats it UTC RFC3339Nano
// so every record carries one unambiguous timestamp shape.
func replaceTime(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime {
		return slog.String("ts", a.Value.Time().UTC().Format(time.RFC3339Nano))
	}
	return a
}

// DurationString renders a window or an age compactly for humans: "1h",
// "30m", "1h30m", "45s", "1m30s" (seconds rounded, zero minute/second parts
// dropped), never Go's "1h0m0s".
func DurationString(d time.Duration) string {
	s := d.Round(time.Second).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// Short bounds a name or id to n bytes (cut on a rune boundary, "..." added)
// so a record stays far below the size where an O_APPEND write could tear.
func Short(s string, n int) string {
	if n < 0 {
		n = 0
	}
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
