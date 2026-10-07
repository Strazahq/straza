package agentguard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// osName is a seam (swapped by setOSName in tests) so OS-branched rendering,
// codex's windows_managed_dir and SystemRoot env lines, is testable on any CI
// host. Production behavior is identical to the plain runtime.GOOS call.
var osName = func() string { return runtime.GOOS }

// harnessInstall describes where a harness keeps its user-mode and
// managed/system hook settings and which native event names carry
// session-start and pre-tool. The event names mirror adapters/<harness>.yaml.
// Vendor paths drift per release; they are re-verified per harness
// release (recorded in adapters/), and the env overrides are the test seam
// and the escape hatch.
type harnessInstall struct {
	envDir       string // env var pointing at the user config dir (test override)
	relPath      []string
	sessionEvent string
	preEvent     string
	// promptEvent + endEvents are the conversation-capture wiring: the
	// prompt-submission and turn/session-end events (adapters map them to
	// prompt.submit / session.end). Without these registrations the capture
	// engine exists but organic sessions never invoke it. endEvents is
	// plural because claude-code needs BOTH the per-turn Stop (reply-delta
	// capture; without it replies land only on a clean exit, which killed
	// terminals and long-lived chats never do) and the final SessionEnd
	// (last delta + spool drain).
	promptEvent string
	endEvents   []string
	// subagentEvents is the delegation pair (SubagentStart/SubagentStop,
	// adapters map them to subagent.start/subagent.stop). SubagentStop is
	// the delegate's capture point: its reply lives in its own transcript
	// (agent_transcript_path), which no other event ever names, so without
	// this registration delegated output is silently uncaptured. Registered
	// together deliberately (one reinstall ask covers the whole subagent
	// surface). Nil for a harness with no subagent hook events (gemini:
	// vendor-documented absent; its delegate output rides tool.post).
	subagentEvents []string
	// managedPath maps GOOS → the harness's managed/system settings file,
	// which the harness merges above (and un-overridably against) user
	// settings. Missing GOOS falls back to "linux".
	managedPath map[string]string
}

var installs = map[string]harnessInstall{
	// Windows moved from C:\ProgramData\ClaudeCode with claude-code v2.1.75
	// (vendor declared the old path unsupported); the retired location lives
	// in LegacyManagedSettingsPath so install migrates and doctor warns.
	"claude-code": {"CLAUDE_CONFIG_DIR", []string{".claude", "settings.json"}, "SessionStart", "PreToolUse",
		"UserPromptSubmit", []string{"Stop", "SessionEnd"},
		[]string{"SubagentStart", "SubagentStop"},
		map[string]string{
			"linux":   "/etc/claude-code/managed-settings.json",
			"darwin":  "/Library/Application Support/ClaudeCode/managed-settings.json",
			"windows": `C:\Program Files\ClaudeCode\managed-settings.json`,
		}},
	// Codex cloned the hook contract of the row above nearly verbatim
	// (adapters/codex.yaml), but the PLUMBING differs and must not be copied:
	//   - user scope is hooks.json, the dedicated file codex reads. It is NOT
	//     settings.json, which no codex release has ever read (installcodex.go
	//     migrates the one older straza versions wrote), and codex's writer
	//     lives in installcodex.go because this row's shared writer assumes
	//     every harness keeps hooks in a settings.json.
	//   - the managed layer is requirements.toml, not a managed-settings.json.
	//     See CodexManagedRequirementsPath and InstallManaged's codex branch.
	// Stop is the per-turn capture point; SessionEnd fires on close or 30 min
	// idle and is the final drain. It never fires for subagents (root-only).
	"codex": {"CODEX_HOME", []string{".codex", codexHooksFile}, "SessionStart", "PreToolUse",
		"UserPromptSubmit", []string{"Stop", "SessionEnd"},
		[]string{"SubagentStart", "SubagentStop"},
		map[string]string{
			"linux":   "/etc/codex/requirements.toml",
			"darwin":  "/etc/codex/requirements.toml",
			"windows": `C:\ProgramData\OpenAI\Codex\requirements.toml`,
		}},
	// Gemini CLI / Antigravity: renamed events, system settings.json
	// (adapters/gemini.yaml). AfterAgent is the turn-end capture point and
	// carries the reply IN the payload (prompt_response, vendor-verified).
	// The env seam is STRAZA-prefixed because it is ours alone: gemini 0.53.0
	// reads no config-dir variable, and user scope moves with $HOME only.
	// Unlike CODEX_HOME and CLAUDE_CONFIG_DIR above, which the vendors honor,
	// a vendor-looking name would invite an operator to set it expecting
	// gemini to follow, aiming `straza install` at files gemini never opens.
	"gemini": {"STRAZA_GEMINI_CONFIG_DIR", []string{".gemini", "settings.json"}, "SessionStart", "BeforeTool",
		"BeforeAgent", []string{"AfterAgent"}, nil,
		map[string]string{
			"linux":   "/etc/gemini-cli/settings.json",
			"darwin":  "/Library/Application Support/GeminiCli/settings.json",
			"windows": `C:\ProgramData\gemini-cli\settings.json`,
		}},
}

// allEvents returns every native event this harness's installer registers,
// in roster order: the single list install, uninstall, and doctor's
// per-event wiring check all walk, so they cannot drift apart.
func (h harnessInstall) allEvents() []string {
	events := []string{h.sessionEvent, h.preEvent, h.promptEvent}
	events = append(events, h.endEvents...)
	return append(events, h.subagentEvents...)
}

// ManagedSettingsPath returns the managed/system settings file for a harness.
// $STRAZA_MANAGED_SETTINGS_DIR (test seam) relocates all of them under
// <dir>/<harness>/<leaf>.
func ManagedSettingsPath(harness string) (string, error) {
	h, ok := installs[harness]
	if !ok {
		return "", fmt.Errorf("no installer for harness %q (Tier-1: claude-code, codex, gemini)", harness)
	}
	path, ok := h.managedPath[osName()]
	if !ok {
		path = h.managedPath["linux"]
	}
	if dir := os.Getenv("STRAZA_MANAGED_SETTINGS_DIR"); dir != "" {
		return filepath.Join(dir, harness, filepath.Base(path)), nil
	}
	return path, nil
}

// LegacyManagedSettingsPath returns a harness's RETIRED managed-settings
// location (a path an older straza wrote that current harness releases
// no longer read), or "" when none applies. claude-code moved its Windows
// file from ProgramData to Program Files (v2.1.75; old path vendor-declared
// unsupported), so wiring left there is silently dead: the harness never
// loads it, governance looks installed but is off. InstallManaged migrates
// it away, UninstallManaged cleans it, doctor warns about it, always
// guarded by fileMentionsStraz, never touching a foreign file.
// $STRAZA_MANAGED_SETTINGS_DIR relocates it to <dir>/<harness>/legacy-<leaf>
// so the migration is testable on any OS.
// codex has one too: older straza versions wrote a managed-settings.json
// under paths modeled on claude-code. No codex release reads a
// managed-settings.json anywhere (its managed layer is requirements.toml),
// so any file left at those paths is pure debris, and debris that reads as
// "managed governance is installed".
func LegacyManagedSettingsPath(harness string) string {
	if harness != "claude-code" && harness != "codex" {
		return ""
	}
	if dir := os.Getenv("STRAZA_MANAGED_SETTINGS_DIR"); dir != "" {
		return filepath.Join(dir, harness, "legacy-managed-settings.json")
	}
	if harness == "codex" {
		switch osName() {
		case "windows":
			return `C:\ProgramData\Codex\managed-settings.json`
		case "darwin":
			return "/Library/Application Support/Codex/managed-settings.json"
		default:
			return "/etc/codex/managed-settings.json"
		}
	}
	if osName() != "windows" {
		return ""
	}
	return `C:\ProgramData\ClaudeCode\managed-settings.json`
}

// SettingsPath returns the user-mode settings file for a harness.
func SettingsPath(harness string) (string, error) {
	h, ok := installs[harness]
	if !ok {
		return "", fmt.Errorf("no installer for harness %q (Tier-1: claude-code, codex, gemini)", harness)
	}
	if dir := os.Getenv(h.envDir); dir != "" {
		return filepath.Join(dir, h.relPath[len(h.relPath)-1]), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home: %w", err)
	}
	return filepath.Join(append([]string{home}, h.relPath...)...), nil
}

// InstallHooks writes merge-safe Straza hook wiring for a harness into its
// user settings, preserving existing settings and adding only the
// session-start and pre-tool hooks that invoke `straza hook`
// in user mode. Idempotent.
func InstallHooks(harness, settingsPath, agentguardPath string) error {
	return installHooks(harness, settingsPath, agentguardPath, 0o750, 0o600, false)
}

// InstallManagedHooks writes the same wiring into a harness's MANAGED/system
// settings file (ManagedSettingsPath), where the layout is root-owned and
// world-READABLE, like every other managed artifact (managed.go's config and
// binary, installmcp.go's managed MCP file, installcodexmanaged.go's
// requirements.toml). Only root may write it; every user's harness must be
// able to READ it.
//
// A 0600 managed file is worse than no managed file at all, and silently so:
// every other user's harness fails to read the policy wiring and runs
// ungoverned, while measureAttestation cannot hash what it cannot open and
// reports att=none, fleet-wide, from a single install. 0600 is correct only
// for the user scope.
func InstallManagedHooks(harness, settingsPath, agentguardPath string) error {
	return installHooks(harness, settingsPath, agentguardPath, 0o755, 0o644, true) // #nosec G301 G306 -- managed layout is deliberately world-readable
}

// installHooks is the shared writer behind both scopes. dirMode/fileMode are
// the caller's scope choice (user 0750/0600, managed 0755/0644) and apply only
// when CREATING: an existing file keeps the permissions its operator chose,
// exactly like writeJSONMap: a merge into someone's settings must not
// silently retighten or loosen them.
func installHooks(harness, settingsPath, agentguardPath string, dirMode, fileMode os.FileMode, managed bool) error {
	h, ok := installs[harness]
	if !ok {
		return fmt.Errorf("no installer for harness %q", harness)
	}
	settings := map[string]any{}
	if raw, err := os.ReadFile(settingsPath); err == nil { // #nosec G304 -- managing the harness's own settings file
		if err := json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf("existing %s is not valid JSON: %w", filepath.Base(settingsPath), err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	cmd := hookCommand(agentguardPath, harness)
	// Straza's own entries are REPLACED, not accumulated (parity with
	// InstallCodexHooks and the codex MCP block). A registration naming a
	// different command is one this same installer wrote against a binary that
	// has since moved or been renamed (staged builds, a new bin dir, a hand-
	// copied straza.exe), and merging beside it leaves the harness invoking a
	// dead path on every governed event, silently, with `straza install` (the
	// documented fix) unable to clean it up. The cost is that hand-tuned flags
	// on a straza hook entry do not survive a re-install: install owns the
	// entries it wrote, and doctor's hook-binary check finds any ghost entries
	// that older installs appended.
	pruneStrazaHooks(hooks, hookMarker(harness), cmd)
	applyStrazaHooks(h, hooks, cmd)
	settings["hooks"] = hooks

	if managed && harness == "gemini" {
		// Gemini's precedence puts SYSTEM settings above user and workspace,
		// so pinning hooksConfig.enabled here defeats a user's kill switch
		// (hooksConfig.enabled=false disables every hook), the analog of the
		// [features] hooks = true pin in codex's requirements.toml. Other
		// hooksConfig keys are the operator's and survive; uninstall
		// deliberately LEAVES the pin (true equals the vendor default, and a
		// JSON file has no marker block to prove the key was ours to take).
		hc, _ := settings["hooksConfig"].(map[string]any)
		if hc == nil {
			hc = map[string]any{}
		}
		hc["enabled"] = true
		settings["hooksConfig"] = hc
	}
	if harness == "gemini" {
		// Printed here for the same reason installcodex.go prints its spaced-
		// path warning: the exported signatures are print-free, and a notice
		// every caller must relay is a notice that gets lost. Stderr only.
		// Verified against gemini-cli 0.53.0: headless gemini in an untrusted
		// folder REFUSES to start (exit 55, loud), so hooks never silently skip
		// there. The silent-skip reading survives only for the interactive TUI
		// and is unconfirmed. The kill-switch half (hooksConfig.enabled=false)
		// is verified.
		notice := "NOTE: gemini refuses to start headless in an untrusted folder, and runs NO hooks anywhere if hooksConfig.enabled=false; `straza doctor` checks both (gemini-hooks)."
		if managed {
			notice += " The managed file pins hooksConfig.enabled = true, which gemini's precedence puts above a user opt-out."
		} else {
			notice += " Trust the working folder inside gemini before relying on governance."
		}
		fmt.Fprintln(os.Stderr, notice)
	}

	if fi, err := os.Stat(settingsPath); err == nil {
		fileMode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), dirMode); err != nil { // #nosec G301 -- managed variant is deliberately world-readable
		return err
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, out, fileMode) // #nosec G306 -- mode is the caller's scope choice (user 0600, managed 0644)
}

// UninstallHooks removes the Straza hook entries for a harness, leaving all
// other settings intact. Scope-agnostic: it only ever rewrites a file that
// already exists, so the file keeps its own permissions (os.WriteFile applies
// a mode only on create) and a managed file stays world-readable.
//
// Matching is on the command's stable harness tail, not on the exact path of
// the binary running the uninstall, as UninstallCodexHooks does. Straza gets
// staged, renamed and re-copied, and an entry naming a path
// that has since moved is precisely the orphan an uninstall exists to clear;
// leaving it behind would keep the harness invoking a dead command with nothing
// left to fix it.
func UninstallHooks(harness, settingsPath, agentguardPath string) error {
	h, ok := installs[harness]
	if !ok {
		return fmt.Errorf("no installer for harness %q", harness)
	}
	raw, err := os.ReadFile(settingsPath) // #nosec G304 -- managing the harness's own settings file
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return err
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return nil
	}
	// keep = "" : every straza command goes. Hook-level, so a group an operator
	// merged one of their own hooks into survives with that hook intact.
	removeStrazaHooks(hooks, h.allEvents(), hookMarker(harness), "")
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, out, 0o600) // #nosec G306 -- existing file only; its current mode is preserved
}

// applyStrazaHooks merge-registers straza's hook entries for every rostered
// event into a hooks table: the one registration body shared by installHooks
// and RenderManagedArtifacts, so what install writes and what the server
// publishes cannot drift. The prompt/end/subagent events are the
// conversation-capture wiring, registered unconditionally: whether anything
// is RECORDED stays a policy decision (spec/policyset §6 capture block); an
// unwired event would silently disable capture no matter what the policy
// says.
func applyStrazaHooks(h harnessInstall, hooks map[string]any, cmd string) {
	entry := func() map[string]any {
		return map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd}}}
	}
	hooks[h.sessionEvent] = mergeHook(hooks[h.sessionEvent], entry(), cmd)
	pre := entry()
	pre["matcher"] = "*"
	hooks[h.preEvent] = mergeHook(hooks[h.preEvent], pre, cmd)
	hooks[h.promptEvent] = mergeHook(hooks[h.promptEvent], entry(), cmd)
	for _, ev := range append(append([]string{}, h.endEvents...), h.subagentEvents...) {
		hooks[ev] = mergeHook(hooks[ev], entry(), cmd)
	}
}

func hookCommand(agentguardPath, harness string) string {
	return hookCommandFor(osName(), agentguardPath, harness)
}

// hookCommandFor renders the hook command for an explicit GOOS, so the
// server can render another platform's artifacts without touching the
// process-global osName seam (RenderManagedArtifacts).
func hookCommandFor(goos, agentguardPath, harness string) string {
	return quoteCommandFor(goos, agentguardPath) + " " + hookMarker(harness)
}

// pruneStrazaHooks drops the Straza hook commands that are not keep: the
// ghosts an earlier install left behind when the binary moved. It walks EVERY
// event, not just the current roster, so a straza command on an event since
// dropped from the roster goes too; a de-rostered event stays gone, which is
// the point. The roster's own events are re-added by the caller.
func pruneStrazaHooks(hooks map[string]any, marker, keep string) {
	removeStrazaHooks(hooks, allEventsIn(hooks), marker, keep)
}

// removeStrazaHooks strips straza's hook commands from the named events,
// keeping the one command equal to keep ("" removes them all).
//
// The unit is the hook, not the entry. Our installer writes each straza command
// in its own single-hook group, but a hooks[] group is the operator's to
// compose, and one that holds our command beside their own is a shape a human
// legitimately hand-merges. Dropping the whole group would delete a
// registration straza never wrote, and "we take back only what is ours" is the
// contract uninstall lives or dies on. So the command is removed, the group
// survives whatever else is in it, and only a group we emptied is dropped: one
// that was already empty is not ours to touch. An event left holding nothing
// loses its key rather than keeping a vestigial array.
func removeStrazaHooks(hooks map[string]any, events []string, marker, keep string) {
	for _, event := range events {
		arr, ok := hooks[event].([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(arr))
		for _, item := range arr {
			stripped, empty := stripStrazaHooks(item, marker, keep)
			if stripped && empty {
				continue // the group held nothing but ours
			}
			kept = append(kept, item)
		}
		if len(kept) == 0 {
			delete(hooks, event)
			continue
		}
		hooks[event] = kept
	}
}

// stripStrazaHooks removes straza's commands (all but keep) from ONE hook
// group, in place. It reports whether it removed anything and whether the group
// is now empty; the caller drops a group only when both are true, so a group
// that arrived empty is left exactly as it was found.
func stripStrazaHooks(item any, marker, keep string) (stripped, empty bool) {
	m, ok := item.(map[string]any)
	if !ok {
		return false, false
	}
	inner, ok := m["hooks"].([]any)
	if !ok {
		return false, false
	}
	kept := make([]any, 0, len(inner))
	for _, h := range inner {
		hm, ok := h.(map[string]any)
		if !ok {
			kept = append(kept, h)
			continue
		}
		c, _ := hm["command"].(string)
		if strings.Contains(c, marker) && c != keep {
			stripped = true
			continue
		}
		kept = append(kept, h)
	}
	if !stripped {
		return false, len(inner) == 0
	}
	m["hooks"] = kept
	return true, len(kept) == 0
}

// allEventsIn returns the event keys of a hook table.
func allEventsIn(hooks map[string]any) []string {
	events := make([]string, 0, len(hooks))
	for event := range hooks {
		events = append(events, event)
	}
	return events
}

// mergeHook appends the Straza entry to an existing hook array for the event,
// idempotently (does not duplicate if the same command is already wired).
func mergeHook(existing any, entry map[string]any, cmd string) []any {
	var arr []any
	if e, ok := existing.([]any); ok {
		arr = e
	}
	for _, item := range arr {
		if hookHasCommand(item, cmd) {
			return arr // already installed
		}
	}
	return append(arr, entry)
}

func hookHasCommand(item any, cmd string) bool {
	m, ok := item.(map[string]any)
	if !ok {
		return false
	}
	hooks, ok := m["hooks"].([]any)
	if !ok {
		return false
	}
	for _, h := range hooks {
		if hm, ok := h.(map[string]any); ok {
			if c, _ := hm["command"].(string); c == cmd {
				return true
			}
		}
	}
	return false
}

func quoteCommandFor(goos, path string) string {
	if goos == "windows" {
		return `"` + path + `"`
	}
	return path
}
