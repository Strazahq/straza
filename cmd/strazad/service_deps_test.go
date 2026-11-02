package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestStrazadDoesNotLinkTheService pins that strazad, which imports
// agentguard for the harness config and policy event routes, never links
// `straza service`: the socket listener and the code that holds every
// enrolled user's key belong to the client binary only. agentguard itself
// is the positive control, so a scan that read nothing cannot pass.
func TestStrazadDoesNotLinkTheService(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps ./cmd/strazad: %v", err)
	}
	deps := map[string]bool{}
	for _, line := range strings.Fields(string(out)) {
		deps[line] = true
	}
	const control = "github.com/strazahq/straza/internal/agentguard"
	if !deps[control] {
		t.Fatalf("go list -deps ./cmd/strazad does not name %s, so this scan proves nothing; check how strazad links agentguard", control)
	}
	const banned = "github.com/strazahq/straza/internal/agentguard/service"
	if deps[banned] {
		t.Errorf("strazad links %s. Move whatever strazad needs out of that package, because strazad must never link the code that holds the client's keys", banned)
	}
}
