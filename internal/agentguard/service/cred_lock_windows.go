//go:build windows

package service

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// lockStateDir takes the exclusive LockFileEx lock on dir's lock file and
// keeps it for as long as the returned file stays open. A lock another
// process or another open of the file holds answers the second-service
// sentence.
func lockStateDir(dir string) (*os.File, error) {
	path := filepath.Join(dir, "lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- the service's own state directory
	if err != nil {
		return nil, fmt.Errorf("open the state directory lock %s: %w", path, err)
	}
	const allBytes = ^uint32(0)
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, allBytes, allBytes, new(windows.Overlapped))
	if err != nil {
		_ = f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, &Refused{Status: http.StatusConflict, Sentence: secondServiceSentence}
		}
		return nil, fmt.Errorf("lock the state directory %s: %w", dir, err)
	}
	return f, nil
}
