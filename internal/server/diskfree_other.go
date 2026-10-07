//go:build !unix

package server

// diskFreeBytes: no portable statfs off unix; the gauge stays at its -1
// unknown sentinel there (metrics.go documents the semantics).
func diskFreeBytes(string) (int64, bool) { return -1, false }
