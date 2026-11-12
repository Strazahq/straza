package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// TestMain points the system service socket at a path where nothing listens
// before any test of this package runs. On a machine with a system service
// or a managed install the thin client ignores STRAZA_HOME and asks the
// system service, so a test that drives a command would otherwise reach the
// machine's real service, which enrolls at its managed server and opens a
// browser sign-in there.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "straza-nosvc-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cmd/straza tests: make the empty socket directory:", err)
		os.Exit(1)
	}
	localapi.SystemSocket = filepath.Join(dir, "service.sock")
	if runtime.GOOS == "windows" {
		localapi.SystemSocket = `\\.\pipe\` + filepath.Base(dir)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
