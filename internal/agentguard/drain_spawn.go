package agentguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// drainSpawner is the post-decision audit kick. The default fires a detached
// `straza drain` and forgets it: the hook process must exit immediately
// (its exit code IS the decision and the harness blocks on it, and audit I/O
// is forbidden on the request path), so the upload runs in a short-lived
// sibling process instead. Tests override this seam.
var drainSpawner = spawnDetachedDrain

// drainSpawnBinaryAllowed reports whether a binary basename is the client
// binary's own name, `straza` (± .exe), and therefore safe to re-exec as
// `<exe> drain`. Anything else is refused: a unit-test binary re-exec'ing
// itself would run the suite.
func drainSpawnBinaryAllowed(base string) bool {
	return strings.TrimSuffix(base, ".exe") == "straza"
}

// spawnDetachedDrain launches `<this exe> drain` without waiting. Refuses to
// run when the executable is not the client binary (drainSpawnBinaryAllowed)
// or when STRAZA_NO_DRAIN_SPAWN is set (CI/harness escape hatch). Every
// error is swallowed: the record stays spooled and rides a later trigger
// (next decision, session start/end, daemon tick).
func spawnDetachedDrain() {
	if os.Getenv("STRAZA_NO_DRAIN_SPAWN") != "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if !drainSpawnBinaryAllowed(filepath.Base(exe)) {
		return
	}
	cmd := exec.Command(exe, "drain") // #nosec G204 -- re-exec of our own binary
	applyDetach(cmd)
	if err := cmd.Start(); err != nil {
		return
	}
	_ = cmd.Process.Release()
}
