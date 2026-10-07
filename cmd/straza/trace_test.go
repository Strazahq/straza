package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
)

func runStraza(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	root := rootCmd()
	root.SetArgs(args)
	root.SetOut(&buf)
	root.SetErr(&buf)
	err := root.Execute()
	return buf.String(), err
}

func TestTraceCmdMissingOperandListsValues(t *testing.T) {
	t.Setenv("STRAZA_HOME", t.TempDir())
	_, err := runStraza(t, "trace")
	if err == nil || !strings.Contains(err.Error(), "on, off, status, show") {
		t.Fatalf("bare `straza trace` must list the operands, got %v", err)
	}
	_, err = runStraza(t, "trace", "bogus")
	if err == nil || !strings.Contains(err.Error(), `"bogus"`) || !strings.Contains(err.Error(), "on, off, status, show") {
		t.Fatalf("unknown operand must be named and the valid ones listed, got %v", err)
	}
}

func TestTraceOnOffStatusShow(t *testing.T) {
	t.Setenv("STRAZA_HOME", t.TempDir())

	out, err := runStraza(t, "trace", "on", "--for", "2h")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "debug trace on until ") || !strings.Contains(out, " (2h); records in ") {
		t.Fatalf("on output = %q", out)
	}
	out, err = runStraza(t, "trace", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "level: debug (source: window until ") || !strings.Contains(out, "window: until ") ||
		!strings.Contains(out, "0 record(s), 0 unreadable") {
		t.Fatalf("status output = %q", out)
	}
	out, err = runStraza(t, "trace", "show")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "no trace records (file: ") {
		t.Fatalf("empty show output = %q", out)
	}

	store, err := agentguard.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	store.Trace().Journal("decision", slog.String("tool", "shell.exec"), slog.String("effect", "allow"))
	store.Trace().Debug("adapter", slog.String("harness", "codex"))
	out, err = runStraza(t, "trace", "show")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], " INFO decision ") || !strings.Contains(lines[0], "tool=shell.exec") ||
		!strings.Contains(lines[1], " DEBUG adapter ") || !strings.Contains(lines[1], "harness=codex") {
		t.Fatalf("show output = %q", out)
	}

	out, err = runStraza(t, "trace", "off")
	if err != nil || out != "debug trace off (the journal stays on)\n" {
		t.Fatalf("off output = %q err=%v", out, err)
	}
	if out, err = runStraza(t, "trace", "off"); err != nil || !strings.Contains(out, "debug trace off") {
		t.Fatalf("off must be idempotent: %q %v", out, err)
	}
	out, err = runStraza(t, "trace", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "level: journal (source: default)") || !strings.Contains(out, "window: none") ||
		!strings.Contains(out, "2 record(s), 0 unreadable") {
		t.Fatalf("status after off = %q", out)
	}

	for _, bad := range []string{"25h", "0s", "500ms"} {
		_, err = runStraza(t, "trace", "on", "--for", bad)
		if err == nil || !strings.Contains(err.Error(), "--for must be between 1s and 24h") {
			t.Fatalf("--for %s must be refused with the bounds, got %v", bad, err)
		}
	}
}

func TestTraceOnRefusedUnderManagedOff(t *testing.T) {
	t.Setenv("STRAZA_HOME", t.TempDir())
	system := t.TempDir()
	t.Setenv("STRAZA_SYSTEM", system)
	if err := os.WriteFile(filepath.Join(system, "config.yaml"), []byte("serverUrl: http://x\ntrace: off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runStraza(t, "trace", "on")
	if err == nil || !strings.Contains(err.Error(), "trace: off") || !strings.Contains(err.Error(), agentguard.ManagedConfigPath()) {
		t.Fatalf("refusal must name the managed file and the setting, got %v", err)
	}
	out, err := runStraza(t, "trace", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "level: off (source: config "+agentguard.ManagedConfigPath()+")") {
		t.Fatalf("status under managed off = %q", out)
	}
}

func TestTraceShowLimitsN(t *testing.T) {
	t.Setenv("STRAZA_HOME", t.TempDir())
	store, err := agentguard.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"a", "b", "c", "d", "e"} {
		store.Trace().Journal("decision", slog.String("tool", tool))
	}
	out, err := runStraza(t, "trace", "show", "-n", "2")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "tool=d") || !strings.Contains(lines[1], "tool=e") {
		t.Fatalf("show -n 2 = %q", out)
	}
	out, err = runStraza(t, "trace", "show", "--lines", "1")
	if err != nil || !strings.Contains(out, "tool=e") || strings.Contains(out, "tool=d") {
		t.Fatalf("show --lines 1 = %q err=%v", out, err)
	}
}
