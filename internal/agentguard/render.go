package agentguard

import (
	"encoding/json"
	"fmt"
	"maps"
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
	return renderManagedArtifacts(harness, goos, binPath, GitHostsFile(goos))
}

// renderManagedArtifacts is RenderManagedArtifacts with the git host file
// the Claude Code wiring names, which doctor's local render sets to this
// machine's own.
func renderManagedArtifacts(harness, goos, binPath, hostsFile string) (map[string][]byte, error) {
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
		settings, err := renderManagedSettings(h, harness, goos, binPath, hostsFile)
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
func renderManagedSettings(h harnessInstall, harness, goos, binPath, hostsFile string) ([]byte, error) {
	hooks := map[string]any{}
	applyStrazaHooks(h, hooks, hookCommandFor(goos, binPath, harness))
	settings := map[string]any{"hooks": hooks}
	applyStrazaEnv(settings, harness, hostsFile)
	if harness == "gemini" {
		// The managed kill-switch pin, exactly as installHooks writes it into
		// an empty file (its comment there carries the why).
		settings["hooksConfig"] = map[string]any{"enabled": true}
	}
	return json.MarshalIndent(settings, "", "  ")
}

// GitHostsFile returns the path of the git host file `straza service`
// writes on goos in the default layout, or "" where it writes none in this
// version. The file holds a straza:: rewrite per git upstream a Straza
// server serves, and the Claude Code wiring points git inside agent
// processes at it. The windows path is a literal with backslashes, as
// defaultManagedBinaryPath's is, because git takes GIT_CONFIG_VALUE_0 as it
// is.
func GitHostsFile(goos string) string {
	switch goos {
	case "linux":
		return "/run/straza-service/git/hosts.conf"
	case "windows":
		return `C:\ProgramData\straza-service\git\hosts.conf`
	case "darwin":
		return "/opt/straza/run/git/hosts.conf"
	}
	return ""
}

// strazaGitEnv is the env block of the Claude Code wiring, in both install
// scopes: one git config entry that includes the host file at file. git
// 2.31 and later read it, and a missing or empty file changes nothing, so a
// machine without `straza service` keeps plain git. It is nil where the
// service writes no host file, file "".
func strazaGitEnv(harness, file string) map[string]string {
	if harness != "claude-code" || file == "" {
		return nil
	}
	return map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "include.path", "GIT_CONFIG_VALUE_0": file}
}

// applyStrazaEnv merges the git env block of the host file at file into
// the settings' env table: the one writer shared by installHooks, for the
// user and the managed scope, and renderManagedSettings, so the managed
// file and the served artifact carry the same bytes on a default machine.
// Other env entries stay as they are. It returns, in key order, the names
// of the block's entries that held another value, which the install names
// for the person. It never returns their values: GIT_CONFIG_VALUE_0 can
// hold an http.extraHeader and GIT_CONFIG_KEY_0 a
// url.https://TOKEN@host/.insteadOf, and install output can reach rollout
// logs.
func applyStrazaEnv(settings map[string]any, harness, file string) []string {
	block := strazaGitEnv(harness, file)
	if block == nil {
		return nil
	}
	env, _ := settings["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	var replaced []string
	for _, k := range slices.Sorted(maps.Keys(block)) {
		old, ok := env[k]
		if ok && old != block[k] {
			replaced = append(replaced, k)
		}
		env[k] = block[k]
	}
	settings["env"] = env
	return replaced
}

// replacedGitEnvNotice tells the person which of their own git env entries
// in path an install replaced, so they can restore them from a backup.
func replacedGitEnvNotice(path string, replaced []string) string {
	list := replaced[len(replaced)-1]
	if n := len(replaced); n > 1 {
		list = strings.Join(replaced[:n-1], ", ") + " and " + list
	}
	return "NOTE: Straza replaced " + list + " in the env of " + path + " with its own git settings, " +
		"and uninstalling Straza does not put them back, so if you still need them, restore them from a backup " +
		"of that file before you rely on this install."
}

// removeStrazaEnv takes the git env block of the host file at file back
// out of the settings' env table, each entry only while it still holds
// Straza's value, and drops a table it emptied.
func removeStrazaEnv(settings map[string]any, harness, file string) {
	env, _ := settings["env"].(map[string]any)
	changed := false
	for k, v := range strazaGitEnv(harness, file) {
		if env[k] == v {
			delete(env, k)
			changed = true
		}
	}
	if changed && len(env) == 0 {
		delete(settings, "env")
	}
}

// defaultManagedBinaryPath is ManagedBinaryPath("") for an explicit GOOS,
// vendor-default layout only. Literals, never env (programData()): the
// published artifact is rendered for the default fleet layout, not for this
// process's relocations; a relocated-%ProgramData% box takes the local
// install lane instead (managedfetch.go).
func defaultManagedBinaryPath(goos string) string {
	switch goos {
	case "windows":
		return `C:\ProgramData\straza\bin\straza.exe`
	case "linux":
		return "/usr/bin/straza"
	case "darwin":
		return "/opt/straza/bin/straza"
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
