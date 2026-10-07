package agentguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMCPConfigPathPerHarness(t *testing.T) {
	t.Run("claude-code honors CLAUDE_CONFIG_DIR", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", dir)
		got, err := MCPConfigPath("claude-code")
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(dir, ".claude.json"); got != want {
			t.Errorf("path = %s, want %s", got, want)
		}
	})
	t.Run("claude-code defaults to home", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		got, err := MCPConfigPath("claude-code")
		if err != nil {
			t.Fatal(err)
		}
		home, _ := os.UserHomeDir()
		if want := filepath.Join(home, ".claude.json"); got != want {
			t.Errorf("path = %s, want %s", got, want)
		}
	})
	t.Run("gemini shares its settings.json", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("STRAZA_GEMINI_CONFIG_DIR", dir)
		got, err := MCPConfigPath("gemini")
		if err != nil {
			t.Fatal(err)
		}
		settings, _ := SettingsPath("gemini")
		if got != settings {
			t.Errorf("path = %s, want settings path %s", got, settings)
		}
	})
	// codex has no JSON MCP config: its registrations live in config.toml,
	// which installmcptoml.go owns (and installmcptoml_test.go covers).
	t.Run("codex is not a JSON registration", func(t *testing.T) {
		got, err := MCPConfigPath("codex")
		if err != nil || got != "" {
			t.Errorf("codex = (%q, %v), want (empty, nil): config.toml is CodexMCPConfigPath's", got, err)
		}
		dir := t.TempDir()
		t.Setenv("CODEX_HOME", dir)
		toml, err := CodexMCPConfigPath()
		if err != nil || toml != filepath.Join(dir, "config.toml") {
			t.Errorf("CodexMCPConfigPath = (%q, %v), want %s", toml, err, filepath.Join(dir, "config.toml"))
		}
	})
	t.Run("unknown harness errors", func(t *testing.T) {
		if _, err := MCPConfigPath("cursor"); err == nil {
			t.Error("want error for unknown harness")
		}
	})
}

func TestInstallMCPServerMergeSafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")

	// Pre-existing live harness state + an unrelated MCP server: both survive.
	pre := `{
  "numStartups": 42,
  "oauthAccount": {"email": "kim@example.com"},
  "mcpServers": {
    "github": {"type": "stdio", "command": "gh-mcp", "args": []}
  }
}`
	if err := os.WriteFile(path, []byte(pre), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := InstallMCPServer("claude-code", path, `E:\bin\agentguard.exe`)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !changed {
		t.Error("first install should report a change")
	}
	got := readSettings(t, path)
	if got["numStartups"] != float64(42) || got["oauthAccount"] == nil {
		t.Errorf("live harness state lost: %v", got)
	}
	servers := got["mcpServers"].(map[string]any)
	if servers["github"] == nil {
		t.Error("unrelated MCP server lost")
	}
	straza, _ := servers["straza"].(map[string]any)
	if straza == nil {
		t.Fatal("straza entry not written")
	}
	if straza["type"] != "stdio" || straza["command"] != `E:\bin\agentguard.exe` {
		t.Errorf("entry = %v", straza)
	}
	args, _ := straza["args"].([]any)
	if len(args) != 3 || args[0] != "mcp" || args[1] != "--harness" || args[2] != "claude-code" {
		t.Errorf("args = %v", args)
	}

	// Idempotent: unchanged entry → no rewrite reported.
	before, _ := os.ReadFile(path)
	changed, err = InstallMCPServer("claude-code", path, `E:\bin\agentguard.exe`)
	if err != nil || changed {
		t.Errorf("re-install = (changed=%v, %v), want (false, nil)", changed, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("idempotent install rewrote the file")
	}

	// A moved binary updates the entry in place.
	changed, err = InstallMCPServer("claude-code", path, `C:\ProgramData\agentguard\bin\agentguard.exe`)
	if err != nil || !changed {
		t.Fatalf("moved-binary install = (changed=%v, %v), want (true, nil)", changed, err)
	}
}

func TestInstallMCPServerFreshAndEmptyFiles(t *testing.T) {
	for name, seed := range map[string]func(path string){
		"missing file": func(string) {},
		"empty file":   func(path string) { _ = os.WriteFile(path, []byte("  \n"), 0o644) },
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".claude.json")
			seed(path)
			changed, err := InstallMCPServer("claude-code", path, "/usr/local/bin/agentguard")
			if err != nil || !changed {
				t.Fatalf("install = (changed=%v, %v), want (true, nil)", changed, err)
			}
			servers := readSettings(t, path)["mcpServers"].(map[string]any)
			if servers["straza"] == nil {
				t.Error("straza entry not written")
			}
		})
	}
}

func TestInstallMCPServerRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallMCPServer("claude-code", path, "agentguard"); err == nil {
		t.Error("want error on corrupt config, got nil (must never clobber live harness state)")
	}
}

func TestInstallMCPServerGeminiCoexistsWithHooks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STRAZA_GEMINI_CONFIG_DIR", dir)
	settings, err := SettingsPath("gemini")
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallHooks("gemini", settings, "/usr/local/bin/agentguard"); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallMCPServer("gemini", settings, "/usr/local/bin/agentguard"); err != nil {
		t.Fatal(err)
	}
	got := readSettings(t, settings)
	if got["hooks"] == nil {
		t.Error("hooks lost when registering the MCP server in the same file")
	}
	straza, _ := got["mcpServers"].(map[string]any)["straza"].(map[string]any)
	if straza == nil {
		t.Fatal("straza entry not written")
	}
	if _, hasType := straza["type"]; hasType {
		t.Error("gemini entry must carry only documented keys (no type)")
	}
}

func TestUninstallMCPServer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	if _, err := InstallMCPServer("claude-code", path, "agentguard"); err != nil {
		t.Fatal(err)
	}
	// Seed a second server that must survive.
	cfg, _ := readJSONMap(path)
	cfg["mcpServers"].(map[string]any)["github"] = map[string]any{"command": "gh-mcp"}
	if err := writeJSONMap(path, cfg, 0o750, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := UninstallMCPServer(path); err != nil {
		t.Fatal(err)
	}
	servers := readSettings(t, path)["mcpServers"].(map[string]any)
	if _, ok := servers["straza"]; ok {
		t.Error("straza entry not removed")
	}
	if servers["github"] == nil {
		t.Error("unrelated server removed")
	}

	// No-ops: already removed, and a missing file.
	if err := UninstallMCPServer(path); err != nil {
		t.Errorf("second uninstall: %v", err)
	}
	if err := UninstallMCPServer(filepath.Join(dir, "nope.json")); err != nil {
		t.Errorf("missing file: %v", err)
	}
}

func TestManagedMCPConfigPath(t *testing.T) {
	t.Run("claude-code default is the vendor path", func(t *testing.T) {
		t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", "")
		got, err := ManagedMCPConfigPath("claude-code")
		if err != nil || filepath.Base(got) != "managed-mcp.json" {
			t.Errorf("path = (%q, %v), want .../managed-mcp.json", got, err)
		}
	})
	t.Run("seam relocates beside the managed settings", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", dir)
		got, err := ManagedMCPConfigPath("claude-code")
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(dir, "claude-code", "managed-mcp.json"); got != want {
			t.Errorf("path = %s, want %s", got, want)
		}
	})
	t.Run("harnesses without a managed MCP layer", func(t *testing.T) {
		for _, harness := range []string{"codex", "gemini"} {
			if got, err := ManagedMCPConfigPath(harness); got != "" || err != nil {
				t.Errorf("%s = (%q, %v), want no managed MCP layer (empty, nil)", harness, got, err)
			}
		}
	})
	t.Run("unknown harness errors", func(t *testing.T) {
		if _, err := ManagedMCPConfigPath("cursor"); err == nil {
			t.Error("want error for unknown harness")
		}
	})
}

// TestInstallManagedMCPServer drives the managed-mcp.json write through the
// STRAZA_MANAGED_SETTINGS_DIR seam. Same contract as the user scope: fresh
// write, idempotent re-run, merge preservation, corrupt refusal (never
// clobber a policy file), uninstall removes only the straza entry.
func TestInstallManagedMCPServer(t *testing.T) {
	const bin = `C:\ProgramData\agentguard\bin\agentguard.exe`
	wantEntry := func(t *testing.T, path string) {
		t.Helper()
		servers, _ := readSettings(t, path)["mcpServers"].(map[string]any)
		straza, _ := servers["straza"].(map[string]any)
		if straza == nil {
			t.Fatal("straza entry not written")
		}
		if straza["type"] != "stdio" || straza["command"] != bin {
			t.Errorf("entry = %v", straza)
		}
		args, _ := straza["args"].([]any)
		if len(args) != 3 || args[0] != "mcp" || args[1] != "--harness" || args[2] != "claude-code" {
			t.Errorf("args = %v", args)
		}
	}

	for _, tc := range []struct {
		name        string
		seed        string // "" = no pre-existing file
		reinstall   bool   // run install once before the asserted run
		uninstall   bool   // uninstall after install, then verify
		wantChanged bool
		wantErr     bool
		verify      func(t *testing.T, path string)
	}{
		{
			name: "fresh write", wantChanged: true, verify: wantEntry,
		},
		{
			name: "idempotent re-run", reinstall: true, wantChanged: false, verify: wantEntry,
		},
		{
			name: "merge preserves other servers and keys",
			seed: `{
  "mcpServers": {"corp-internal": {"type": "stdio", "command": "corp-mcp", "args": []}},
  "somePolicyKey": true
}`,
			wantChanged: true,
			verify: func(t *testing.T, path string) {
				t.Helper()
				wantEntry(t, path)
				got := readSettings(t, path)
				if got["somePolicyKey"] != true {
					t.Errorf("unrelated top-level key lost: %v", got)
				}
				if got["mcpServers"].(map[string]any)["corp-internal"] == nil {
					t.Error("operator-deployed managed server lost")
				}
			},
		},
		{
			name: "corrupt file refused, never clobbered", seed: "{not json", wantErr: true,
			verify: func(t *testing.T, path string) {
				t.Helper()
				raw, err := os.ReadFile(path)
				if err != nil || string(raw) != "{not json" {
					t.Errorf("corrupt managed file was touched: %q, %v", raw, err)
				}
			},
		},
		{
			name:        "uninstall removes only the straza entry",
			seed:        `{"mcpServers": {"corp-internal": {"command": "corp-mcp"}}}`,
			wantChanged: true, uninstall: true,
			verify: func(t *testing.T, path string) {
				t.Helper()
				servers := readSettings(t, path)["mcpServers"].(map[string]any)
				if _, ok := servers["straza"]; ok {
					t.Error("straza entry not removed")
				}
				if servers["corp-internal"] == nil {
					t.Error("operator-deployed managed server removed")
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("STRAZA_MANAGED_SETTINGS_DIR", t.TempDir())
			path, err := ManagedMCPConfigPath("claude-code")
			if err != nil {
				t.Fatal(err)
			}
			if tc.seed != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.seed), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.reinstall {
				if _, err := InstallManagedMCPServer("claude-code", path, bin); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)
			changed, err := InstallManagedMCPServer("claude-code", path, bin)
			if (err != nil) != tc.wantErr || (err == nil && changed != tc.wantChanged) {
				t.Fatalf("install = (changed=%v, %v), want (changed=%v, err=%v)", changed, err, tc.wantChanged, tc.wantErr)
			}
			if !tc.wantErr && !tc.wantChanged {
				after, _ := os.ReadFile(path)
				if string(before) != string(after) {
					t.Error("idempotent install rewrote the file")
				}
			}
			if tc.uninstall {
				if err := UninstallMCPServer(path); err != nil {
					t.Fatal(err)
				}
			}
			tc.verify(t, path)
		})
	}
}
