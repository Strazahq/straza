package service

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
)

// TestMain points the system service's git host file into a temporary
// folder, because every test that checks a session in as the system
// service writes it, and a test must never touch /run.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "straza-service-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	systemHostFile = func(goos string) string {
		if agentguard.GitHostsFile(goos) == "" {
			return ""
		}
		return filepath.Join(dir, "run", "git", "hosts.conf")
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
