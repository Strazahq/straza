package agentguard

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain prevents the installed managed configuration from overriding test
// fixtures. Managed-layout tests can still override STRAZA_SYSTEM themselves.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "straza-agent-tests-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	if err := os.Setenv("STRAZA_SYSTEM", filepath.Join(root, "system")); err != nil {
		panic(err)
	}
	m.Run()
}
