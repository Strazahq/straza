package agentguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Codex hook SHIM, Windows only, both codex lanes.
//
// Codex never tokenizes a hook command itself: it hands the whole string to a
// shell. On Windows the vendor's raw-argument quoting fix applies only to a
// cmd-shaped shell, and codex derives the hook shell from the session's
// DETECTED USER SHELL, whose Windows default is PowerShell, which takes the
// whole command as ONE std-quoted argument. A multi-token command dies at
// spawn: the shell exits 1, codex prints "hook exited with code 1", drops
// stderr, and CONTINUES THE TOOL CALL, so governance fails open with nothing
// in our error log (straza never ran). A space-free path is the one spelling
// every shell spawns identically.
//
// The shim REDIRECTS NOTHING: codex honors an exit-2 block only when the
// reason arrives on stderr (exit 2 with EMPTY stderr fails open), so stdin,
// stdout and stderr pass through untouched and the exit code propagates.

// codexShimName is the shim's file name: stable and straza-unique, so
// wiring detection can key on it the way codexHookMarker keys the direct
// form (a shim command carries no "hook --harness codex" tail of its own).
const codexShimName = "straza-hook-codex.cmd"

// codexHookShimPath returns where the shim lives: beside the binary it
// invokes. The bin dir is space-free whenever the shim lane is taken at all
// (a spaced dir is refused for managed installs and warned for user ones),
// so the shim path is itself a single spawn-safe token.
func codexHookShimPath(binPath string) string {
	return filepath.Join(filepath.Dir(binPath), codexShimName)
}

// codexShimPathFor is codexHookShimPath for an explicit GOOS (server-side
// rendering). The join separator follows the path's own style: a real
// windows path (no forward slash) joins with a backslash; a host-native test
// path keeps its forward slashes, so the rendered command equals what the
// host installer writes (render_test.go byte-equality).
func codexShimPathFor(goos, binPath string) string {
	if goos == "windows" && !strings.Contains(binPath, "/") {
		return dirFor(goos, binPath) + `\` + codexShimName
	}
	return dirFor(goos, binPath) + "/" + codexShimName
}

// codexHookShimContent is the ONE source of truth for the shim bytes; the
// doctor check compares the on-disk file against exactly this render
// (codexManagedShimCheck), so any hand edit, an added redirect included,
// reads as drift. CRLF per Windows convention; ASCII only.
func codexHookShimContent(binPath string) string {
	return "@echo off\r\n" +
		binPath + " " + codexHookMarker + "\r\n" +
		"exit /b %ERRORLEVEL%\r\n"
}

// writeCodexHookShim writes the shim beside the binary, reporting whether the
// bytes changed. 0755 like the binary it fronts: the managed bin dir is
// world-readable by design (every user's codex must spawn it) and only the
// administrator writes it; for the user lane the file sits in the user's own
// directory.
func writeCodexHookShim(binPath string) (changed bool, err error) {
	path := codexHookShimPath(binPath)
	want := codexHookShimContent(binPath)
	if current, readErr := os.ReadFile(path); readErr == nil && string(current) == want { // #nosec G304 -- straza's own shim file
		return false, nil
	}
	if err := os.WriteFile(path, []byte(want), 0o755); err != nil { // #nosec G306 -- spawned by every user's codex, deliberately world-readable
		return false, fmt.Errorf("write codex hook shim %s: %w", path, err)
	}
	return true, nil
}

// codexShimCommand reports whether a wiring command is the shim invocation:
// a bare path whose leaf is codexShimName. Case-insensitive because Windows
// paths are; suffix rather than equality so a hand-requoted-but-working
// entry still reads as ours.
func codexShimCommand(command string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(command)), codexShimName)
}

// strazaHookCommand reports whether a wiring command is straza's hook
// invocation for the harness: the direct form (stable "hook --harness <h>"
// tail) or, codex on Windows, the single-token shim. Every doctor reader
// matches through here so the two spellings cannot drift apart across checks.
func strazaHookCommand(command, harness string) bool {
	if strings.Contains(command, hookMarker(harness)) {
		return true
	}
	return harness == "codex" && codexShimCommand(command)
}

// codexShimTarget extracts the binary a shim on disk invokes: the head of
// its "<exe> hook --harness codex" line. ok=false for a missing or reshaped
// file: the caller decides what that means (doctor treats it as drift; the
// binary check falls back to statting the shim itself, so a missing shim is
// reported as exactly the ghost it is).
func codexShimTarget(shimPath string) (string, bool) {
	raw, err := os.ReadFile(shimPath) // #nosec G304 G703 -- the shim the harness wiring names; diagnostics-only read under the invoking user's own privileges, parsed for a command head and never disclosed
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line) // also strips the \r
		if head, ok := strings.CutSuffix(line, " "+codexHookMarker); ok {
			return strings.TrimSpace(head), true
		}
	}
	return "", false
}
