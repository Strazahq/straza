package agentguard

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain prevents the installed managed configuration, and a system service
// on the machine running the tests, from overriding test fixtures.
// Managed-layout tests can still override STRAZA_SYSTEM themselves, and the
// trusted-home tests set systemPresent.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "straza-agent-tests-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	if err := os.Setenv("STRAZA_SYSTEM", filepath.Join(root, "system")); err != nil {
		panic(err)
	}
	systemPresent = func() bool { return false }
	managedInstalled = func() bool { return false }
	m.Run()
}
