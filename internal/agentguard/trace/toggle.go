package trace

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Toggle is the user's debug window (state/trace.json), written by
// `straza trace on` and removed by `straza trace off`. Readers evaluate the
// expiry: an expired toggle is treated as absent, so a forgotten window turns
// itself off.
type Toggle struct {
	// Level is the requested level; only "debug" has meaning.
	Level string `json:"level"`
	// Until is when the window closes.
	Until time.Time `json:"until"`
}

// Active reports whether the toggle raises the level to debug as of now.
func (t Toggle) Active(now time.Time) bool {
	return t.Level == "debug" && now.Before(t.Until)
}

// Expired reports a debug toggle whose window has closed (the doctor names
// it); an absent or non-debug toggle is not expired, it is simply not active.
func (t Toggle) Expired(now time.Time) bool {
	return t.Level == "debug" && !now.Before(t.Until)
}

// TogglePath returns the toggle file under stateDir.
func TogglePath(stateDir string) string { return filepath.Join(stateDir, ToggleName) }

// ReadToggle reads the toggle. present is false when no toggle file exists;
// a file that exists but does not parse is present with an error, and callers
// treat it as absent (the journal stays on, debug stays off).
func ReadToggle(stateDir string) (tg Toggle, present bool, err error) {
	raw, err := os.ReadFile(TogglePath(stateDir)) // #nosec G304 -- our own state file under the straza home
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Toggle{}, false, nil
		}
		return Toggle{}, false, err
	}
	if err := json.Unmarshal(raw, &tg); err != nil {
		return Toggle{}, true, err
	}
	return tg, true, nil
}

// WriteToggle writes the toggle atomically (0600, state dir 0700).
func WriteToggle(stateDir string, tg Toggle) error {
	raw, err := json.MarshalIndent(tg, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(TogglePath(stateDir), raw, 0o600)
}

// RemoveToggle deletes the toggle; an absent toggle is not an error.
func RemoveToggle(stateDir string) error {
	if err := os.Remove(TogglePath(stateDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// writeAtomic writes data via a same-directory temp file that is fsync'd and
// renamed over the target, so a reader never sees a half-written toggle. It
// mirrors agentguard's writeFileAtomic on purpose: that helper is unexported
// there and importing agentguard here would invert the dependency (agentguard
// imports trace, never the other way round).
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	cleanup := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Chmod(perm); err != nil {
		return cleanup(err)
	}
	if _, err := f.Write(data); err != nil {
		return cleanup(err)
	}
	if err := f.Sync(); err != nil {
		return cleanup(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
