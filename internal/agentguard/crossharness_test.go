package agentguard

import (
	"crypto/ed25519"
	"os"
	"testing"

	"github.com/strazahq/straza/internal/policy"
)

// installUserHooks / uninstallUserHooks mirror the dispatch cmd/straza does,
// so this test exercises the writer each harness ACTUALLY installs with.
// Calling InstallHooks directly for codex would make the test circular: it
// would assert the bytes straza wrote into a filename straza chose, whether
// or not codex reads that file.
func installUserHooks(harness, path, self string) error {
	if harness == "codex" {
		_, err := InstallCodexHooks(path, self)
		return err
	}
	return InstallHooks(harness, path, self)
}

func uninstallUserHooks(harness, path, self string) error {
	if harness == "codex" {
		_, err := UninstallCodexHooks(path)
		return err
	}
	return UninstallHooks(harness, path, self)
}

// TestInstallAllHarnesses pins the install contract: user-mode hook wiring lands
// for every Tier-1 harness, merge-safe, with the harness's native event names,
// through the same per-harness dispatch the CLI uses.
func TestInstallAllHarnesses(t *testing.T) {
	setOSName(t, "linux")
	cases := []struct {
		harness   string
		envDir    string
		preEvent  string
		sessEvent string
	}{
		{"claude-code", "CLAUDE_CONFIG_DIR", "PreToolUse", "SessionStart"},
		{"codex", "CODEX_HOME", "PreToolUse", "SessionStart"},
		{"gemini", "STRAZA_GEMINI_CONFIG_DIR", "BeforeTool", "SessionStart"},
	}
	const self = "/usr/local/bin/straza"
	for _, c := range cases {
		t.Run(c.harness, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv(c.envDir, dir)
			settings, err := SettingsPath(c.harness)
			if err != nil {
				t.Fatal(err)
			}

			if err := installUserHooks(c.harness, settings, self); err != nil {
				t.Fatalf("install: %v", err)
			}
			m := readSettings(t, settings)
			hooks, _ := m["hooks"].(map[string]any)
			if hooks == nil || hooks[c.preEvent] == nil || hooks[c.sessEvent] == nil {
				t.Fatalf("%s: missing wiring for %s/%s: %v", c.harness, c.sessEvent, c.preEvent, m)
			}
			cmd := hookCommand(self, c.harness)
			if !hookHasCommand(hooks[c.preEvent].([]any)[0], cmd) {
				t.Errorf("%s: pre-tool command wrong: %v", c.harness, hooks[c.preEvent])
			}

			// Idempotent: a second install does not duplicate.
			if err := installUserHooks(c.harness, settings, self); err != nil {
				t.Fatal(err)
			}
			m = readSettings(t, settings)
			hooks = m["hooks"].(map[string]any)
			if n := len(hooks[c.preEvent].([]any)); n != 1 {
				t.Errorf("%s: re-install duplicated (%d entries)", c.harness, n)
			}

			// Uninstall removes only Straza's wiring. codex deletes a file its
			// wiring was all of, so an absent file is the passing state there.
			if err := uninstallUserHooks(c.harness, settings, self); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(settings); os.IsNotExist(err) {
				return
			}
			m = readSettings(t, settings)
			if hooks, _ := m["hooks"].(map[string]any); hooks[c.preEvent] != nil {
				t.Errorf("%s: uninstall left wiring: %v", c.harness, hooks)
			}
		})
	}
}

// scenario is one canonical action expressed in each harness's payload shape.
type scenario struct {
	name       string
	wantEffect string
	wantRule   string
	payloads   map[string]map[string]any
}

// TestCrossHarnessIdenticalDecisions pins the cross-harness contract: the SAME PolicySet
// yields IDENTICAL decisions across claude-code, codex, and gemini for each
// scenario, because all three normalize to the same canonical event fed to
// the one shared engine (internal/policy).
func TestCrossHarnessIdenticalDecisions(t *testing.T) {
	adapters := loadTestAdapters(t)
	engine := compileTestEngine(t)
	subject := policy.Subject{User: "kim", Roles: []string{"dev"}, Attestation: "advisory", Harness: "x/1"}

	scenarios := []scenario{
		{
			name: "destructive shell blocked", wantEffect: policy.EffectDeny, wantRule: "block-destructive",
			payloads: map[string]map[string]any{
				"claude-code": {"hook_event_name": "PreToolUse", "session_id": "s", "cwd": "/w", "tool_name": "Bash", "tool_input": map[string]any{"command": "rm -rf /tmp/x"}},
				"codex":       {"hook_event_name": "PreToolUse", "session_id": "s", "cwd": "/w", "tool_name": "Shell", "tool_input": map[string]any{"command": "rm -rf /tmp/x"}},
				"gemini":      {"hook_event_name": "BeforeTool", "session_id": "s", "cwd": "/w", "tool_name": "run_shell_command", "tool_input": map[string]any{"command": "rm -rf /tmp/x"}},
			},
		},
		{
			name: "benign shell allowed", wantEffect: policy.EffectAllow,
			payloads: map[string]map[string]any{
				"claude-code": {"hook_event_name": "PreToolUse", "session_id": "s", "cwd": "/w", "tool_name": "Bash", "tool_input": map[string]any{"command": "ls -la"}},
				"codex":       {"hook_event_name": "PreToolUse", "session_id": "s", "cwd": "/w", "tool_name": "LocalShell", "tool_input": map[string]any{"command": "ls -la"}},
				"gemini":      {"hook_event_name": "BeforeTool", "session_id": "s", "cwd": "/w", "tool_name": "run_shell_command", "tool_input": map[string]any{"command": "ls -la"}},
			},
		},
		{
			name: "env-file write blocked", wantEffect: policy.EffectDeny, wantRule: "fs-scope",
			payloads: map[string]map[string]any{
				"claude-code": {"hook_event_name": "PreToolUse", "session_id": "s", "cwd": "/w", "tool_name": "Write", "tool_input": map[string]any{"file_path": "/w/.env"}},
				"codex":       {"hook_event_name": "PreToolUse", "session_id": "s", "cwd": "/w", "tool_name": "Write", "tool_input": map[string]any{"path": "/w/.env"}},
				"gemini":      {"hook_event_name": "BeforeTool", "session_id": "s", "cwd": "/w", "tool_name": "write_file", "tool_input": map[string]any{"file_path": "/w/.env"}},
			},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			var first *policy.Decision
			for harness, payload := range sc.payloads {
				n, err := Normalize(adapters[harness], payload)
				if err != nil {
					t.Fatalf("%s: normalize: %v", harness, err)
				}
				d := engine.Evaluate(n.Event, subject)
				if d.Effect != sc.wantEffect {
					t.Errorf("%s: effect = %s, want %s", harness, d.Effect, sc.wantEffect)
				}
				if sc.wantRule != "" && d.RuleID != sc.wantRule {
					t.Errorf("%s: rule = %s, want %s", harness, d.RuleID, sc.wantRule)
				}
				// Every harness must produce the identical decision.
				if first == nil {
					dd := d
					first = &dd
				} else if d.Effect != first.Effect || d.RuleID != first.RuleID || d.Reason != first.Reason {
					t.Errorf("%s decided %+v; another harness decided %+v (cross-harness drift)", harness, d, *first)
				}
			}
		})
	}
}

// compileTestEngine builds an engine from a shared PolicySet covering the
// scenarios (a self-signed snapshot, opened like the client does).
func compileTestEngine(t *testing.T) *policy.Engine {
	t.Helper()
	doc := []byte(`
apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata: {name: cross-harness}
spec:
  match: {roles: [dev]}
  rules:
    - id: block-destructive
      tools: [shell.exec]
      command: {denyPatterns: ["rm -rf *"]}
      effect: deny
      reason: "Destructive commands are blocked"
    - id: fs-scope
      tools: [file.write, file.edit]
      paths: {deny: ["**/.env*"]}
      effect: deny
      reason: "Writing dotenv files is blocked"
`)
	snap, err := policy.Compile(policy.CompileInput{
		Documents: [][]byte{doc}, LocalDefault: "allow", MaxAge: 900, CreatedUnix: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signed, id, err := snap.Sign("k1", priv)
	if err != nil {
		t.Fatal(err)
	}
	eng, _, err := policy.OpenSnapshot(signed, id, func(kid string) (ed25519.PublicKey, bool) {
		return pub, kid == "k1"
	})
	if err != nil {
		t.Fatal(err)
	}
	return eng
}
