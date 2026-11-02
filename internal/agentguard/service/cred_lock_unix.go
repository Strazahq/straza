//go:build linux || darwin

package service

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
)

// lockStateDir takes the exclusive file lock on dir's lock file and keeps
// it for as long as the returned file stays open. A lock another process
// or another open of the file holds answers the second-service sentence.
func lockStateDir(dir string) (*os.File, error) {
	path := filepath.Join(dir, "lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- the service's own state directory
	if err != nil {
		return nil, fmt.Errorf("open the state directory lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { // #nosec G115 -- a file descriptor fits in an int
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &Refused{Status: http.StatusConflict, Sentence: secondServiceSentence}
		}
		return nil, fmt.Errorf("lock the state directory %s: %w", dir, err)
	}
	return f, nil
}
