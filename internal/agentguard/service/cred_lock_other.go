//go:build !linux && !darwin && !windows

package service

import "os"

// lockStateDir refuses on every system other than Linux, macOS and Windows,
// because the service does not run there.
func lockStateDir(string) (*os.File, error) {
	return nil, errNotOnLinux
}
