//go:build unix

package server

import "syscall"

// diskFreeBytes reports the free bytes on the filesystem holding path
// (unprivileged Bavail, what the server could actually write), for the
// transcript watermark pass. ok=false when the path is empty or statfs
// fails; stdlib syscall keeps CGO off and adds no dependency.
func diskFreeBytes(path string) (int64, bool) {
	if path == "" {
		return -1, false
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true //nolint:unconvert,gosec // darwin's Bsize is uint32 (conversion needed there); Bavail is a size, not an identifier, and a >8 EiB free-disk claim is not a real input
}
