//go:build !windows

package agentguard

import (
	"os"
	"syscall"
)

// tamperProtected is the unix half of the managed-detection heuristic:
// root-owned and not group/world-writable.
func tamperProtected(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0 && fi.Mode().Perm()&0o022 == 0
}
