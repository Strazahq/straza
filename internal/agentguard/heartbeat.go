package agentguard

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Daemon heartbeat: the liveness fact `straza doctor` reads. The killswitch
// check verifies the push LANE (doctorprobe.go dials it), but a lane nobody
// subscribes to delivers nothing, so the daemon stamps pid, an RFC3339 time
// and its own write interval on the poll tick it already runs (daemon.loop).
//
//   - BEST-EFFORT: a failed write must never affect refresh, drain or
//     revocation, and the error is logged once per daemon run, not per tick.
//   - ATOMIC via writeFileAtomic, so a reader never sees a torn file: a
//     half-written heartbeat is corrupt, and corrupt never reads as alive.
//   - PER STRAZA HOME, last-writer-wins; a clean shutdown removes only its OWN
//     pid's file, so an exiting duplicate cannot erase the survivor's claim.
//   - SAME-BOX CLOCK, so a stamp far in the FUTURE is not skew but a clock
//     moved backward under a writer that may be long dead: it reads
//     heartbeatFuture, never fresh. Skew within one write interval stays fresh
//     so an NTP step cannot flap the check.

// heartbeatFileName sits beside session.json: liveness is session-adjacent
// state, and the state dir already has the right permission story.
const heartbeatFileName = "daemon-heartbeat.json"

func (s *Store) heartbeatPath() string { return s.statePath(heartbeatFileName) }

// heartbeatStaleMultiple: a heartbeat older than this many write intervals
// means no live daemon. Why 3: a tick's write can be late by nearly a full
// interval when the previous tick's network work blocks the loop (refresh,
// snapshot adoption and the audit drain run inline, each bounded by the
// client's 30 s HTTP timeout), so 2x would flag a daemon that is merely slow
// behind a bad network; at 3x it has missed two consecutive writes with room
// to spare, meaning a process that is gone or wedged. The window this bound accepts
// (90 s of dead daemon still reading fresh, at the default interval) is far
// tighter than the poll/token-TTL bound the operator would otherwise be
// trusting blind.
const heartbeatStaleMultiple = 3

// daemonHeartbeat is the on-disk shape (state/daemon-heartbeat.json).
type daemonHeartbeat struct {
	PID int `json:"pid"`
	// At marshals as RFC3339 (encoding/json's time.Time format).
	At time.Time `json:"at"`
	// IntervalSeconds is the writer's own refresh cadence, recorded so doctor
	// judges staleness against the interval this daemon actually runs (an
	// operator's --poll choice), not against a constant that may not match.
	IntervalSeconds int `json:"intervalSeconds"`
}

// beat writes the heartbeat. Best-effort by contract; see the file header.
func (d *Daemon) beat() {
	secs := int((d.PollInterval + time.Second - 1) / time.Second) // ceil: a sub-second test interval records 1, never 0
	hb := daemonHeartbeat{PID: os.Getpid(), At: d.now(), IntervalSeconds: secs}
	err := d.store.writeJSON(d.store.heartbeatPath(), hb)
	if err == nil || d.beatFailed {
		return
	}
	d.beatFailed = true
	fmt.Fprintf(d.out, "daemon: heartbeat not recorded (%v). Enforcement unaffected, but `straza doctor` cannot see this daemon\n", err)
}

// clearHeartbeat removes the daemon's OWN heartbeat on a clean exit, so doctor
// flips to "no live daemon" immediately instead of after the staleness bound.
// Pid-guarded: a second daemon on the same home must not have its newer claim
// erased by this one leaving. Best-effort like every write here: an unclean
// death leaves the file to age out, which is what the bound is for.
func (d *Daemon) clearHeartbeat() {
	path := d.store.heartbeatPath()
	raw, err := os.ReadFile(path) // #nosec G304 -- our own state file under the straza home
	if err != nil {
		return
	}
	var hb daemonHeartbeat
	if json.Unmarshal(raw, &hb) != nil || hb.PID != os.Getpid() {
		return
	}
	_ = os.Remove(path)
}

// heartbeatState classifies the heartbeat for the killswitch check.
type heartbeatState int

const (
	heartbeatFresh heartbeatState = iota
	heartbeatStale
	heartbeatAbsent
	heartbeatCorrupt
	// heartbeatFuture: the stamp is further ahead of the clock than the
	// future-skew allowance, so the clock moved backward under it and the
	// writer cannot be presumed alive. See the file header.
	heartbeatFuture
)

// daemonLiveness is what the heartbeat proves, pre-read for killswitchCheck.
type daemonLiveness struct {
	state    heartbeatState
	pid      int
	age      time.Duration
	interval time.Duration
}

// readDaemonLiveness classifies the straza home's daemon heartbeat. The
// fail-safe direction is fixed: only a file that parses AND sits plausibly on
// this box's timeline (inside the staleness bound, and no further into the
// future than the skew allowance) may claim a live daemon. An unreadable or
// unparseable file is heartbeatCorrupt, a far-future stamp is heartbeatFuture;
// both mean "cannot confirm", never fresh and never a fabricated age.
func readDaemonLiveness(path string, now time.Time) daemonLiveness {
	raw, err := os.ReadFile(path) // #nosec G304 -- our own state file under the straza home
	if os.IsNotExist(err) {
		return daemonLiveness{state: heartbeatAbsent}
	}
	if err != nil {
		return daemonLiveness{state: heartbeatCorrupt}
	}
	var hb daemonHeartbeat
	if json.Unmarshal(raw, &hb) != nil || hb.At.IsZero() {
		return daemonLiveness{state: heartbeatCorrupt}
	}
	interval := time.Duration(hb.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = defaultPollInterval // hand-edited or pre-field file: judge against the default
	}
	age := now.Sub(hb.At)
	out := daemonLiveness{pid: hb.PID, age: age, interval: interval}
	switch {
	case age < -interval:
		// Further into the future than one write interval: on a same-box clock
		// that only happens when the clock moved BACKWARD under the stamp (VM
		// snapshot restore, clock set back), and clamping it to fresh would
		// read "daemon alive" for the whole regression while the daemon that
		// wrote it may be dead. One interval is the allowance because the two
		// failure costs are asymmetric inside it: a live daemon re-stamps with
		// the corrected clock on its next tick, and a dead one falls to the
		// normal staleness bound at most one interval later than usual,
		// while an NTP step correction (routine, well under an interval)
		// must not flap the check.
		out.state = heartbeatFuture
	case age > heartbeatStaleMultiple*interval:
		out.state = heartbeatStale
	default:
		if age < 0 {
			age = 0 // inside the allowance: fresh, and the next tick re-stamps
		}
		out.age = age
		out.state = heartbeatFresh
	}
	return out
}
