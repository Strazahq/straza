package trace

import (
	"os"
	"path/filepath"
)

// appendWriter is the rotation and append point behind the slog JSON handler,
// which hands it exactly one Write per record. Each record is one small
// O_APPEND write on a freshly opened handle (no handle is ever held, so the
// long-lived mcp proxy never pins the file against rotation, on Windows
// included) and the kernel keeps such writes atomic across concurrent hook
// processes. Rotation happens BEFORE the append so the live file stays under
// the cap (+ at most the one record whose pre-append stat raced another
// writer); a failed rename means a concurrent rotator won or the platform
// refused, and appending to the live name stays correct either way.
//
// Write always reports success: every failure is swallowed because this file
// must never alter the path it observes. No fsync: a torn last line costs a
// diagnostic record, not evidence.
type appendWriter struct{ path string }

func (w *appendWriter) Write(p []byte) (int, error) {
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return len(p), nil
	}
	if fi, err := os.Stat(w.path); err == nil && fi.Size() >= MaxBytes {
		_ = os.Rename(w.path, w.path+".1")
	}
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- our own state file under the straza home
	if err != nil {
		return len(p), nil
	}
	_, _ = f.Write(p)
	_ = f.Close()
	return len(p), nil
}
