//go:build darwin

package agentguard

import (
	"os"
	"syscall"
	"time"
)

// changedAt is when the file fi describes last changed: the later of its
// modification time and its change time. A copy or a restore can set the
// modification time back, but nothing in userland sets the change time.
func changedAt(fi os.FileInfo) time.Time {
	at := fi.ModTime()
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if c := time.Unix(st.Ctimespec.Unix()); c.After(at) {
			return c
		}
	}
	return at
}
