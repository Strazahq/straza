package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/agentguard"
)

// TestCheckClaudeSettingsOnFailure keeps the claude-static gate in step with
// what the installer writes: a fresh install passes, and the gate refuses a
// PreToolUse hook without onFailure "block" and the key on any other event.
func TestCheckClaudeSettingsOnFailure(t *testing.T) {
	cases := []struct {
		name    string
		edit    func(hooks map[string]any)
		wantErr string
	}{
		{"fresh install", func(map[string]any) {}, ""},
		{"PreToolUse without the key", func(h map[string]any) { delete(hookOf(h, "PreToolUse"), "onFailure") }, "want onFailure"},
		{"PreToolUse set to continue", func(h map[string]any) { hookOf(h, "PreToolUse")["onFailure"] = "continue" }, "want onFailure"},
		{"the key on SessionStart", func(h map[string]any) { hookOf(h, "SessionStart")["onFailure"] = "block" }, "SessionStart: hook keys"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "settings.json")
			if err := agentguard.InstallHooks("claude-code", p, "/opt/straza"); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			var settings map[string]any
			if err := json.Unmarshal(raw, &settings); err != nil {
				t.Fatal(err)
			}
			c.edit(settings["hooks"].(map[string]any))
			out, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, out, 0o600); err != nil {
				t.Fatal(err)
			}
			errs := strings.Join(checkClaudeSettings(p), "; ")
			if c.wantErr == "" && errs != "" {
				t.Fatalf("want no errors, got %s", errs)
			}
			if c.wantErr != "" && !strings.Contains(errs, c.wantErr) {
				t.Fatalf("want an error containing %q, got %q", c.wantErr, errs)
			}
		})
	}
}

func hookOf(hooks map[string]any, event string) map[string]any {
	return hooks[event].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
}
