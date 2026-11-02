//go:build !windows

package agentguard

import (
	"fmt"
	"os"
	"syscall"
)

// tamperProtected is the unix half of the managed-detection heuristic:
// root-owned and not group/world-writable.
func tamperProtected(_ string, fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0 && fi.Mode().Perm()&0o022 == 0
}

// fixedManagedConfig is where a managed install puts its config on Linux
// and macOS, spelled out so no environment variable can move it.
const fixedManagedConfig = "/etc/straza/config.yaml"

// managedConfigFixed reports whether the managed config sits at its fixed
// path as a regular file that root owns and nobody else can write. It reads
// no environment variable, unlike ManagedInstalled, so a harness's
// environment cannot hide a managed install from serviceHome.
func managedConfigFixed() bool {
	fi, err := os.Lstat(fixedManagedConfig)
	return err == nil && fi.Mode().IsRegular() && tamperProtected(fixedManagedConfig, fi)
}

// managedProgramData is the ProgramData folder of a Windows layout. Off
// Windows only a test that poses as Windows reaches it, and it answers the
// vendor default.
func managedProgramData() string { return `C:\ProgramData` }

// runsAsLocalSystem is false off Windows, which has no LocalSystem account.
func runsAsLocalSystem() bool { return false }

// lockManagedRoot creates the managed root. Only root can create it here,
// so its modes keep it to root.
func lockManagedRoot(root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil { // #nosec G301 -- every user's harness must read the managed layout; root-writable only
		return fmt.Errorf("create managed root (need sudo/admin?): %w", err)
	}
	return nil
}

// sealManagedRoot has nothing to do off Windows, where what install
// writes is root's already.
func sealManagedRoot(string) error { return nil }

// managedRootUntrusted is nil off Windows: a managed config ManagedInstalled
// does not trust is no managed layout here.
func managedRootUntrusted() error { return nil }
