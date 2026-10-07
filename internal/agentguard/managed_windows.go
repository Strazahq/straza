//go:build windows

package agentguard

import "os"

// tamperProtected is a no-op on Windows: directory ACLs (%ProgramData% for
// straza's own layout, Program Files for the vendor-pinned harness
// wiring) are the protection mechanism and Go's portable FileInfo cannot
// express them (Stat reports 0666 for any writable file). Auditing the ACLs
// is a doctor concern; the managed *claim* is server-verified against
// the expected-hash registry either way.
func tamperProtected(os.FileInfo) bool { return true }
