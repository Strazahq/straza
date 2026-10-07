package agentguard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// MCP server registration: the harness gets ONE credential-free entry,
// `straza mcp`, because the rotating session token lives behind the
// proxy, never in harness config. This file manages both layers: the
// user-scope registration and the managed layer (claude-code
// managed-mcp.json, written by `install --managed` pointing at the
// managed binary; deliberately not a measured attestation artifact, see
// InstallManaged). codex registers in TOML instead and is handled, end to end,
// in installmcptoml.go.

// mcpServerName is the registration key. Removal matches by this name.
const mcpServerName = "straza"

// MCPConfigPath returns the JSON file holding the harness's user-scope MCP
// server registrations, or "" for a harness that keeps them somewhere else:
// codex, whose config.toml is owned by InstallCodexMCPServer (installmcptoml.go)
// and is reached through CodexMCPConfigPath, not this function.
//
// claude-code keeps MCP servers in ~/.claude.json, NOT .claude/settings.json
// (that file carries hooks/permissions only), and moves it into
// $CLAUDE_CONFIG_DIR when set. gemini reads mcpServers from the same
// settings.json the hooks live in. Vendor-documented; re-verify per
// harness release (adapters/<harness>.yaml).
func MCPConfigPath(harness string) (string, error) {
	switch harness {
	case "claude-code":
		if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
			return filepath.Join(dir, ".claude.json"), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("agentguard: locate home: %w", err)
		}
		return filepath.Join(home, ".claude.json"), nil
	case "gemini":
		return SettingsPath(harness)
	case "codex":
		return "", nil // config.toml, not JSON; see CodexMCPConfigPath
	}
	return "", fmt.Errorf("agentguard: no installer for harness %q (Tier-1: claude-code, codex, gemini)", harness)
}

// managedMCPPaths maps GOOS → claude-code's managed MCP file. Unix paths
// share the managed-settings directory; on Windows the vendor pins Program
// Files, the same directory managed-settings.json moved to at v2.1.75.
// Vendor-documented (code.claude.com/docs/en/managed-mcp); re-verify per
// harness release (adapters/claude-code.yaml).
var managedMCPPaths = map[string]string{
	"linux":   "/etc/claude-code/managed-mcp.json",
	"darwin":  "/Library/Application Support/ClaudeCode/managed-mcp.json",
	"windows": `C:\Program Files\ClaudeCode\managed-mcp.json`,
}

// ManagedMCPConfigPath returns the managed/system MCP registration file for a
// harness, or "" when the harness has no vendor-documented managed MCP layer
// (codex, gemini; their user-scope story in MCPConfigPath is unchanged).
// $STRAZA_MANAGED_SETTINGS_DIR (test seam) relocates it to
// <dir>/<harness>/managed-mcp.json, beside the seamed managed settings.
func ManagedMCPConfigPath(harness string) (string, error) {
	switch harness {
	case "claude-code":
		path, ok := managedMCPPaths[osName()]
		if !ok {
			path = managedMCPPaths["linux"]
		}
		if dir := os.Getenv("STRAZA_MANAGED_SETTINGS_DIR"); dir != "" {
			return filepath.Join(dir, harness, filepath.Base(path)), nil
		}
		return path, nil
	case "codex", "gemini":
		return "", nil
	}
	return "", fmt.Errorf("agentguard: no installer for harness %q (Tier-1: claude-code, codex, gemini)", harness)
}

// mcpServerEntry is the per-harness registration value. claude-code entries
// carry an explicit transport type; gemini's fixed settings schema gets only
// the keys it documents.
func mcpServerEntry(harness, agentguardPath string) map[string]any {
	entry := map[string]any{
		"command": agentguardPath,
		"args":    []any{"mcp", "--harness", harness},
	}
	if harness == "claude-code" {
		entry["type"] = "stdio"
	}
	return entry
}

// InstallMCPServer merge-writes the straza MCP server entry into the
// harness's MCP config file, preserving every other key; for claude-code
// that file is ~/.claude.json, which is live harness state, not ours.
// Idempotent: returns false (and does not touch the file) when the entry is
// already current.
func InstallMCPServer(harness, path, agentguardPath string) (bool, error) {
	return installMCPServer(harness, path, agentguardPath, 0o750, 0o600)
}

// InstallManagedMCPServer merge-writes the straza entry into the harness's
// managed MCP file (ManagedMCPConfigPath), same semantics as
// InstallMCPServer. Fresh files are world-readable like the rest of the
// managed layout: every user's harness must read them, only root writes;
// an existing file keeps its permissions. CAUTION: deploying claude-code's
// managed-mcp.json puts MCP under exclusive control: the harness loads ONLY
// the servers named in that file, and users cannot add their own.
func InstallManagedMCPServer(harness, path, agentguardPath string) (bool, error) {
	return installMCPServer(harness, path, agentguardPath, 0o755, 0o644) // #nosec G301 G306 -- managed layout is deliberately world-readable
}

func installMCPServer(harness, path, agentguardPath string, dirMode, fileMode os.FileMode) (bool, error) {
	cfg, err := readJSONMap(path)
	if err != nil {
		return false, err
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	entry := mcpServerEntry(harness, agentguardPath)
	if reflect.DeepEqual(servers[mcpServerName], entry) {
		return false, nil
	}
	servers[mcpServerName] = entry
	cfg["mcpServers"] = servers
	return true, writeJSONMap(path, cfg, dirMode, fileMode)
}

// UninstallMCPServer removes the straza entry (matched by name; the binary
// may have moved since install), leaving the rest of the file intact,
// including the mcpServers key itself, empty or not: for managed-mcp.json an
// empty map means "MCP disabled by policy", and whether that file keeps
// existing is the operator's call, not ours. Works for both the user-scope
// and managed files (the rewrite keeps the file's existing permissions).
// Missing file, key, or entry is a no-op.
func UninstallMCPServer(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	cfg, err := readJSONMap(path)
	if err != nil {
		return err
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	if _, ok := servers[mcpServerName]; !ok {
		return nil
	}
	delete(servers, mcpServerName)
	return writeJSONMap(path, cfg, 0o750, 0o600)
}

// readJSONMap loads a JSON object file; missing or empty files are an empty
// map (first install creates the file).
func readJSONMap(path string) (map[string]any, error) {
	m := map[string]any{}
	raw, err := os.ReadFile(path) // #nosec G304 -- managing the harness's own config file
	if os.IsNotExist(err) || (err == nil && len(bytes.TrimSpace(raw)) == 0) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("agentguard: existing %s is not valid JSON: %w", filepath.Base(path), err)
	}
	return m, nil
}

// writeJSONMap writes m as indented JSON. dirMode/fileMode apply only when
// creating; an existing file keeps its permissions: the file may be the
// harness's own (or the operator's managed) config, and a rewrite must not
// silently retighten or loosen what they chose.
func writeJSONMap(path string, m map[string]any, dirMode, fileMode os.FileMode) error {
	if fi, err := os.Stat(path); err == nil {
		fileMode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil { // #nosec G301 -- managed variant is deliberately world-readable
		return err
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, fileMode) // #nosec G306 -- mode is the caller's scope choice (user 0600, managed 0644)
}
