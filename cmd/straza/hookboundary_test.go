package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
)

// A live-observed shape: codex-cli 0.146 (Windows quoting change) hands
// straza argv that cobra rejects before hookCmd's RunE; exit 1, stderr
// harness-swallowed, and without the boundary the error log records nothing
// at exactly the failing layer.
func TestHookParseFailureHitsTheBoundary(t *testing.T) {
	t.Setenv("STRAZA_HOME", t.TempDir())
	ranHook = false
	root := rootCmd()
	root.SetArgs([]string{"hook", "--harness codex"}) // one mangled token
	err := root.Execute()
	if err == nil {
		t.Fatal("mangled argv must fail Execute")
	}
	if ranHook {
		t.Fatal("parse failure must not reach RunE")
	}
	recordHookBoundary([]string{"straza", "hook", "--harness codex"}, err)

	store, openErr := agentguard.OpenStore()
	if openErr != nil {
		t.Fatal(openErr)
	}
	errs, _ := agentguard.ClientErrors(store)
	if len(errs) != 1 {
		t.Fatalf("records = %d, want 1", len(errs))
	}
	if !strings.Contains(errs[0].Err, `"--harness codex"`) {
		t.Fatalf("record must carry the mangled argv token, got %q", errs[0].Err)
	}
}

// Once RunE ran, its own recording owns the error; the boundary must not
// write a duplicate record for the same failure.
func TestHookBoundarySkipsAfterRunE(t *testing.T) {
	t.Setenv("STRAZA_HOME", t.TempDir())
	ranHook = true
	defer func() { ranHook = false }()
	recordHookBoundary([]string{"straza", "hook", "--harness", "codex"}, errors.New("bare encode error"))

	store, openErr := agentguard.OpenStore()
	if openErr != nil {
		t.Fatal(openErr)
	}
	if errs, _ := agentguard.ClientErrors(store); len(errs) != 0 {
		t.Fatalf("boundary wrote %d record(s) after RunE ran, want 0", len(errs))
	}
}

// Non-hook subcommands never reach the errorlog through the boundary.
func TestHookBoundaryIgnoresOtherCommands(t *testing.T) {
	t.Setenv("STRAZA_HOME", t.TempDir())
	ranHook = false
	recordHookBoundary([]string{"straza", "logs", "--bogus"}, errors.New("unknown flag: --bogus"))

	store, openErr := agentguard.OpenStore()
	if openErr != nil {
		t.Fatal(openErr)
	}
	if errs, _ := agentguard.ClientErrors(store); len(errs) != 0 {
		t.Fatalf("boundary recorded a non-hook failure: %d record(s)", len(errs))
	}
}
