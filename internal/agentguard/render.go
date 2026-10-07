package agentguard

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Server-side rendering of the managed enforcement files
// (spec/harness-config): strazad publishes exactly what `install --managed`
// writes into an empty layout, so the served artifact, the on-disk file, and
// the attestation registry row all hash identically. The GOOS is an explicit
// parameter (never the process-global osName seam): the server renders every
// platform's artifacts from one box. render_test.go pins byte equality
// against the real installers.

// SupportedGOOS lists the platforms managed artifacts are rendered for.
var SupportedGOOS = []string{"linux", "darwin", "windows"}

// RenderManagedArtifacts renders, from empty, the managed enforcement files
// for one harness on one GOOS, keyed by attestation artifact name
// ("hooks.<harness>", plus "mcp.claude-code" for the one harness with a
// vendor-documented managed MCP layer). binPath "" means the per-GOOS
// default managed binary location; a fleet on a custom --bin-dir (or a
// relocated %ProgramData%) renders locally instead, because its bytes
// legitimately differ from the published artifact.
func RenderManagedArtifacts(harness, goos, binPath string) (map[string][]byte, error) {
	h, ok := installs[harness]
	if !ok {
		return nil, fmt.Errorf("no installer for harness %q (Tier-1: claude-code, codex, gemini)", harness)
	}
	if !slices.Contains(SupportedGOOS, goos) {
		return nil, fmt.Errorf("unsupported platform %q (supported: %s)", goos, strings.Join(SupportedGOOS, ", "))
	}
	if binPath == "" {
		binPath = defaultManagedBinaryPath(goos)
	}
	out := map[string][]byte{}
	if harness == "codex" {
		block, err := renderCodexRequirementsBlockFor(goos, binPath)
		if err != nil {
			return nil, err
		}
		out["hooks.codex"] = []byte(appendCodexBlock("", block))
	} else {
		settings, err := renderManagedSettings(h, harness, goos, binPath)
		if err != nil {
			return nil, err
		}
		out["hooks."+harness] = settings
	}
	if harness == "claude-code" {
		mcp, err := json.MarshalIndent(map[string]any{
			"mcpServers": map[string]any{mcpServerName: mcpServerEntry(harness, binPath)},
		}, "", "  ")
		if err != nil {
			return nil, err
		}
		out["mcp."+harness] = mcp
	}
	return out, nil
}

// renderManagedSettings builds the managed settings JSON for the harnesses
// that keep hooks in a settings file (claude-code, gemini), through the same
// applyStrazaHooks the installer uses, so the two cannot drift.
func renderManagedSettings(h harnessInstall, harness, goos, binPath string) ([]byte, error) {
	hooks := map[string]any{}
	applyStrazaHooks(h, hooks, hookCommandFor(goos, binPath, harness))
	settings := map[string]any{"hooks": hooks}
	if harness == "gemini" {
		// The managed kill-switch pin, exactly as installHooks writes it into
		// an empty file (its comment there carries the why).
		settings["hooksConfig"] = map[string]any{"enabled": true}
	}
	return json.MarshalIndent(settings, "", "  ")
}

// defaultManagedBinaryPath is ManagedBinaryPath("") for an explicit GOOS,
// vendor-default layout only. Literals, never env (programData()): the
// published artifact is rendered for the default fleet layout, not for this
// process's relocations; a relocated-%ProgramData% box takes the local
// install lane instead (managedfetch.go).
func defaultManagedBinaryPath(goos string) string {
	if goos == "windows" {
		return `C:\ProgramData\straza\bin\straza.exe`
	}
	return "/usr/local/bin/straza"
}

// dirFor is filepath.Dir for an explicit GOOS, separator-style-preserving:
// only a windows render splits on backslash, and a path that carries forward
// slashes (host-native test paths) keeps them, so byte-equality with the
// host installers holds on any build platform.
func dirFor(goos, p string) string {
	seps := "/"
	if goos == "windows" {
		seps = `\/`
	}
	if i := strings.LastIndexAny(p, seps); i > 0 {
		return p[:i]
	}
	return "."
}
