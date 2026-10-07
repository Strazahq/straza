package sentinel

import (
	"container/list"
	"time"

	"github.com/strazahq/straza/internal/config"
)

// In-memory bounds. Sentinel state is DELIBERATELY volatile, with no store
// or DB involvement: a strazad restart loses the open windows, which is an
// accepted detection gap, not a correctness issue, because the durable
// JetStream cursor resumes the feed and new windows rebuild immediately.
const (
	maxSessions      = 10_000           // tracked sessions, LRU-evicted at cap
	maxUsers         = 10_000           // tool-mix baselines, LRU-evicted at cap
	sessionTTL       = time.Hour        // idle sessions evict after this
	sweepEvery       = time.Minute      // idle-eviction cadence (lazy, on observe)
	maxDenies        = 128              // per-session deny-burst refs kept
	maxDeniedCmds    = 64               // per-session denied commands remembered
	maxWrites        = 256              // per-session written-paths ring
	maxBaselineTools = 512              // per-user distinct baseline tools
	dedupWindow      = 10 * time.Minute // dedup for detectors without their own window
	minPathLen       = 3                // shorter paths are too noisy to match
	minB64CmdLen     = 8                // shorter commands make trivial base64
)

// Verdict is one sentinel finding, ready for CE emission. The data contract
// is pinned: {session, user, detector, severity, reason, evidence, window?}.
type Verdict struct {
	Session  string
	User     string
	Detector string // denyBurst|denyThenVariant|writeThenExecute|captureContent|toolMixAnomaly
	Severity string // info|warn|critical
	Reason   string
	Evidence []string // CE ids of the events that convict
	Window   string   // detector window, when one applies
}

// Detector names (the CE data `detector` vocabulary).
const (
	DetectorDenyBurst      = "denyBurst"
	DetectorDenyVariant    = "denyThenVariant"
	DetectorWriteExec      = "writeThenExecute"
	DetectorCaptureContent = "captureContent"
	DetectorToolMix        = "toolMixAnomaly"
)

// Severities (the CE data `severity` vocabulary).
const (
	SeverityInfo     = "info"
	SeverityWarn     = "warn"
	SeverityCritical = "critical"
)

// event is one parsed audit CE, the engine's only input.
type event struct {
	ID       string
	Kind     string // tool|mcp|prompt|reply
	At       time.Time
	Session  string
	User     string
	Tool     string
	App      string
	ToolName string
	Command  string
	Effect   string
	Content  string
	Paths    []string
}

// evRef is a timestamped CE reference inside a sliding window.
type evRef struct {
	at time.Time
	id string
}

// deniedCmd remembers one denied shell command for the variant detector.
type deniedCmd struct {
	at     time.Time
	id     string
	cmd    string
	tokens map[string]bool
	b64    string // RawStdEncoding of cmd (a prefix of the padded form)
}

// writeRec remembers one written path for the write-then-execute detector.
type writeRec struct {
	at   time.Time
	id   string
	path string
}

// session is the per-session sliding-window state.
type session struct {
	user     string
	lastSeen time.Time // wall clock, drives idle eviction
	denies   []evRef
	denied   []deniedCmd
	writes   []writeRec
	fired    map[string]time.Time // dedup: key → EVENT time of last verdict
}

// dedup reports whether a verdict keyed key may fire at event-time at, and
// records the fire. One verdict per detector per session per window; the
// alert lane must never itself become a flood.
func (s *session) dedup(key string, at time.Time, window time.Duration) bool {
	if last, ok := s.fired[key]; ok && at.Sub(last) < window {
		return false
	}
	s.fired[key] = at
	return true
}

// baseline is one user's coarse tool-mix profile, built in memory since boot.
// The audit payload carries no ROLE (the pdp.go and gateway.go producers do
// not stamp one), so the baseline is keyed by USER.
type baseline struct {
	lastSeen time.Time
	total    int
	tools    map[string]bool
}

// engine runs the detectors over parsed events. It is confined to the
// sentinel's single Run goroutine: no locking, by construction.
type engine struct {
	cfg config.Sentinel
	now func() time.Time

	maxSessions int
	maxUsers    int

	sessions  *lru[*session]
	users     *lru[*baseline]
	lastSweep time.Time
}

func newEngine(cfg config.Sentinel, now func() time.Time) *engine {
	return &engine{
		cfg: cfg, now: now,
		maxSessions: maxSessions, maxUsers: maxUsers,
		sessions: newLRU[*session](), users: newLRU[*baseline](),
	}
}

// observe feeds one event through every applicable detector and returns the
// verdicts it produced (usually none).
func (e *engine) observe(ev event) []Verdict {
	if ev.Session == "" {
		return nil
	}
	now := e.now()
	e.maybeSweep(now)
	s := e.sessions.getOrPut(ev.Session, e.maxSessions, func() *session {
		return &session{fired: map[string]time.Time{}}
	})
	s.lastSeen = now
	if ev.User != "" {
		s.user = ev.User
	}
	var out []Verdict
	switch ev.Kind {
	case "tool", "mcp":
		out = append(out, e.denyBurst(s, ev)...)
		out = append(out, e.denyThenVariant(s, ev)...)
		out = append(out, e.writeThenExecute(s, ev)...)
		out = append(out, e.toolMix(s, ev)...)
	case "prompt", "reply":
		out = append(out, e.captureContent(s, ev)...)
	}
	return out
}

// maybeSweep lazily evicts idle sessions (no background goroutine needed:
// a sentinel with no traffic has no state growing either). User baselines
// are deliberately kept for the process lifetime ("since boot") and are
// bounded by the LRU cap alone.
func (e *engine) maybeSweep(now time.Time) {
	if now.Sub(e.lastSweep) < sweepEvery {
		return
	}
	e.lastSweep = now
	cut := now.Add(-sessionTTL)
	e.sessions.evictIdle(cut, func(s *session) time.Time { return s.lastSeen })
}

// --- generic LRU table (recency == lastSeen order, since every touch is a
// use). Front = most recent; eviction pops the back. ---

type lruEntry[V any] struct {
	key string
	val V
}

type lru[V any] struct {
	m map[string]*list.Element
	l *list.List
}

func newLRU[V any]() *lru[V] {
	return &lru[V]{m: make(map[string]*list.Element), l: list.New()}
}

func (t *lru[V]) len() int { return len(t.m) }

// getOrPut returns the entry for key, creating it via mk if absent; at cap,
// the least-recently-used entry is evicted first.
func (t *lru[V]) getOrPut(key string, capacity int, mk func() V) V {
	if el, ok := t.m[key]; ok {
		t.l.MoveToFront(el)
		return el.Value.(*lruEntry[V]).val
	}
	if len(t.m) >= capacity {
		if back := t.l.Back(); back != nil {
			delete(t.m, back.Value.(*lruEntry[V]).key)
			t.l.Remove(back)
		}
	}
	v := mk()
	t.m[key] = t.l.PushFront(&lruEntry[V]{key: key, val: v})
	return v
}

// evictIdle removes entries whose lastSeen precedes cut, walking oldest-first
// and stopping at the first survivor (recency order makes that sound).
func (t *lru[V]) evictIdle(cut time.Time, lastSeen func(V) time.Time) {
	for el := t.l.Back(); el != nil; {
		ent := el.Value.(*lruEntry[V])
		if !lastSeen(ent.val).Before(cut) {
			return
		}
		prev := el.Prev()
		delete(t.m, ent.key)
		t.l.Remove(el)
		el = prev
	}
}
