//go:build !linux && !darwin

package agentguard

import (
	"os"
	"time"
)

// changedAt is when the file fi describes last changed: its modification
// time, because this platform keeps no change time that a copy cannot set.
func changedAt(fi os.FileInfo) time.Time {
	return fi.ModTime()
}
