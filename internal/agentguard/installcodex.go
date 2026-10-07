package agentguard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Codex hook wiring. What codex actually reads (cited in adapters/codex.yaml):
//
//	user scope : $CODEX_HOME/hooks.json (this file), also config.toml [hooks]
//	repo scope : <repo>/.codex/hooks.json, <repo>/.codex/config.toml
//	managed    : /etc/codex/requirements.toml, or on Windows
//	             %ProgramData%\OpenAI\Codex\requirements.toml
//
// hooks.json shares its envelope and entry NESTING with the other harnesses
// but not their semantics: codex matchers are REGEXES, so a glob "*" is an
// invalid pattern here (codexHookEntry). The trust gate has no analogue: a
// hook from a non-managed source is SKIPPED until an operator trusts its exact
// definition in the `/hooks` UI, keyed to its HASH, so changing our command
// re-gates it. Writing this file is necessary but not sufficient: install
// prints the /hooks step, doctor never calls the lane governed on file
// presence alone, and only managed sources are auto-trusted.

const (
	// codexHooksFile is the dedicated user-scope hook file codex reads.
	codexHooksFile = "hooks.json"
	// codexHooksDescription is the file-level description install writes when
	// the file has none: what the operator reads in the /hooks trust prompt.
	// Uninstall removes it again, but ONLY when it is still exactly this
	// string: an operator who reworded it owns it.
	codexHooksDescription = "Straza governance hooks (straza install codex)"
	// codexStaleHooksFile is the file older straza versions wrote and no
	// codex release has ever read. Install, uninstall, and doctor all know
	// about it so a machine wired by an older straza gets told the truth
	// rather than left with dead wiring that looks alive.
	codexStaleHooksFile = "settings.json"
)

// codexHookMarker is the stable tail of every straza codex hook command. Codex
// removal matches on THIS rather than on the full command the way the shared
// UninstallHooks does, because the binary path in a stale entry is frequently
// not the binary running the uninstall: straza gets staged, renamed, and
// re-copied between installs, and an entry naming a path that has since moved
// is exactly the orphan we are here to clean up. Nothing but straza writes a
// command containing this.
const codexHookMarker = "hook --harness codex"

// codexHookCommand renders the command string codex spawns for a straza hook,
// for BOTH codex lanes, and never via quoteCommandFor. A space-free path is
// written UNQUOTED on every OS: current codex re-tokenizes the command and
// cannot spawn a double-quoted exe head (exit 1, every event dead).
//
// A path WITH whitespace has no known-good spelling: the quoted head dies as
// above and the array command form is rejected by the requirements layer
// ("invalid type: sequence, expected a string"). The user lane keeps the
// quoted form, the only spelling older tokenizers accepted, plus a warning the
// installer must surface; the managed lane refuses instead
// (renderCodexRequirementsBlock), its bin dir being straza's own. Any change
// to the rendered command re-gates codex's trust hash, which is what the
// /hooks step install always prints (codexTrustNotice) makes honest.
func codexHookCommand(binPath string) (cmd, warning string) {
	if !strings.ContainsAny(binPath, " \t") {
		return binPath + " " + codexHookMarker, ""
	}
	return `"` + binPath + `" ` + codexHookMarker,
		fmt.Sprintf("WARNING: %q contains whitespace. codex >= 0.146 re-tokenizes hook commands and cannot spawn a quoted path (upstream regression), so these hooks will fail on current codex; reinstall straza from a space-free path", binPath)
}

// CodexHooksPath returns the hooks.json codex reads for user-scope hooks:
// $CODEX_HOME/hooks.json, or ~/.codex/hooks.json. It is SettingsPath("codex")
// (the installs table names hooks.json for codex) and exists as its own
// function so call sites read as what they are.
func CodexHooksPath() (string, error) { return SettingsPath("codex") }

// CodexStaleHooksPath returns the $CODEX_HOME/settings.json that older straza
// versions wrote. It is not a codex path at all: it is OUR debris, and the
// only reason to compute it is to clean it up.
func CodexStaleHooksPath() (string, error) {
	hooks, err := CodexHooksPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(hooks), codexStaleHooksFile), nil
}

// CodexStaleAction is what a stale-settings.json migration did.
type CodexStaleAction int

const (
	// CodexStaleNone means there was nothing of ours to clean: no file, or a
	// file that does not mention straza (someone else's; never touched).
	CodexStaleNone CodexStaleAction = iota
	// CodexStaleCleaned means straza's hook entries were removed and the file
	// was kept, because it still holds content that is not ours.
	CodexStaleCleaned
	// CodexStaleRemoved means the file held nothing but straza's dead wiring
	// and was deleted.
	CodexStaleRemoved
)

// String renders the action for install/uninstall output.
func (a CodexStaleAction) String() string {
	switch a {
	case CodexStaleCleaned:
		return "cleaned"
	case CodexStaleRemoved:
		return "removed"
	default:
		return "none"
	}
}

// InstallCodexHooks writes straza's hook wiring into codex's hooks.json,
// preserving every other hook and every other key in the file. It returns
// whether the file changed; an unchanged file is left untouched down to its
// mtime, which matters more here than elsewhere: codex keys hook trust to the
// definition's hash, so a re-install that rewrites identical bytes must not
// look like a new definition to the operator.
//
// Straza's own entries are REPLACED rather than appended to: an entry left by
// an install whose binary has since moved is dead weight the operator would
// have to trust a second time in the /hooks UI. Foreign entries on the same
// events keep their order and content.
func InstallCodexHooks(path, strazaPath string) (bool, error) {
	doc, err := readCodexHooksDoc(path)
	if err != nil {
		return false, err
	}
	hooks := codexHooksTable(doc)
	removeCodexHookEntries(hooks) // ours are rewritten, not accumulated

	cmd, warning := codexHookCommand(strazaPath)
	if warning != "" {
		// Printed here rather than returned: the exported signature is
		// print-free and every caller would have to relay it or lose it,
		// exactly how a spaced-path install would go silently dead on codex
		// >= 0.146. Stderr, so anything parsing install stdout is unaffected.
		fmt.Fprintln(os.Stderr, warning)
	} else if osName() == "windows" {
		// Space-free Windows path: the command becomes the single-token shim,
		// same finding as the managed lane (installcodexshim.go: a
		// PowerShell hook shell kills a multi-token spawn; user-scope hooks
		// ride the identical spawn path). The changed command re-gates
		// codex's trust hash, which the /hooks step install always prints
		// already covers. A SPACED user path keeps the pre-0.146 quoted form
		// plus the warning above, unchanged: a shim beside it would carry
		// the same spaces and die the same way.
		if _, err := writeCodexHookShim(strazaPath); err != nil {
			return false, err
		}
		cmd = codexHookShimPath(strazaPath)
	}
	for _, event := range installs["codex"].allEvents() {
		arr, _ := hooks[event].([]any)
		hooks[event] = append(arr, codexHookEntry(event, cmd))
	}
	if _, ok := doc["description"]; !ok {
		// What the operator sees in the /hooks trust prompt. Written only when
		// the file has none: the description belongs to whoever owns the file.
		doc["description"] = codexHooksDescription
	}
	codexSetHooksTable(doc, hooks)
	return writeCodexHooksDoc(path, doc)
}

// UninstallCodexHooks removes straza's entries from codex's hooks.json,
// leaving every foreign hook and every other key intact, and deletes the file
// when straza's wiring was all it held (an empty hooks.json is not a state the
// operator asked for). It reports whether anything changed.
func UninstallCodexHooks(path string) (bool, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- managing the harness's own hook file
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	doc := map[string]any{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false, fmt.Errorf("existing %s is not valid JSON: %w", filepath.Base(path), err)
	}
	hooks := codexHooksTable(doc)
	removed := removeCodexHookEntries(hooks)
	codexSetHooksTable(doc, hooks)
	ours := removeCodexDescription(doc)
	if removed == 0 && !ours {
		return false, nil // nothing of ours in it; leave the bytes alone
	}
	if codexDocEmpty(doc) {
		if err := os.Remove(path); err != nil {
			return false, err
		}
		return true, nil
	}
	return writeCodexHooksDoc(path, doc)
}

// MigrateCodexStaleHooks removes straza's hook wiring from the
// $CODEX_HOME/settings.json older straza versions wrote, deleting the file
// when nothing but our wiring was in it. A file that does not mention straza
// is somebody else's and is never touched, deleted, or reported.
//
// This runs on install AND uninstall, and doctor warns while the file exists,
// because dead wiring that LOOKS installed is worse than no wiring: it makes
// a governed machine ungoverned without a single error message.
func MigrateCodexStaleHooks(path string) (CodexStaleAction, error) {
	if !fileMentionsStraz(path) {
		return CodexStaleNone, nil // absent, unreadable, or not ours
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- cleaning up a file straza itself wrote
	if err != nil {
		return CodexStaleNone, err
	}
	doc := map[string]any{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		// Our own file, but not parseable as ours. Say so and leave it: a
		// hand-edited file is the operator's, and codex reads it either way
		// (which is to say, not at all).
		return CodexStaleNone, fmt.Errorf("%s mentions straza but is not valid JSON (%w). Delete it by hand; no codex release reads it", path, err)
	}
	hooks := codexHooksTable(doc)
	removed := removeCodexHookEntries(hooks)
	codexSetHooksTable(doc, hooks)
	ours := removeCodexDescription(doc)
	if removed == 0 && !ours {
		// The file mentions straza for some reason of the user's own (a
		// model name, a comment, their own hook calling us) but holds no
		// wiring of ours. Rewriting it would reformat a file we do not own
		// and report a migration that did not happen.
		return CodexStaleNone, nil
	}
	if codexDocEmpty(doc) {
		if err := os.Remove(path); err != nil {
			return CodexStaleNone, err
		}
		return CodexStaleRemoved, nil
	}
	if _, err := writeCodexHooksDoc(path, doc); err != nil {
		return CodexStaleNone, err
	}
	return CodexStaleCleaned, nil
}

// CodexHooksWritten reports whether a hooks.json carries straza wiring, and
// which of the roster's events it registers. It is doctor's input and says
// nothing about TRUST; see codexHooksCheck.
func CodexHooksWritten(path string) (wired bool, missing []string) {
	present := codexRegisteredEvents(path)
	if len(present) == 0 {
		return false, nil
	}
	for _, ev := range installs["codex"].allEvents() {
		if !present[ev] {
			missing = append(missing, ev)
		}
	}
	return true, missing
}

// codexRegisteredEvents returns the events in a codex hooks.json that carry a
// straza command, matching on the stable command tail so a relocated binary
// still reads as registered (same rule as doctor's registeredHookEvents).
func codexRegisteredEvents(path string) map[string]bool {
	out := map[string]bool{}
	raw, err := os.ReadFile(path) // #nosec G304 -- reading the harness's own hook file
	if err != nil {
		return out
	}
	doc := map[string]any{}
	if json.Unmarshal(raw, &doc) != nil {
		return out
	}
	for event, entries := range codexHooksTable(doc) {
		arr, _ := entries.([]any)
		for _, item := range arr {
			if codexEntryIsStraza(item) {
				out[event] = true
			}
		}
	}
	return out
}

// codexHookEntry renders one hook entry for an event. Two deliberate
// divergences from the other harnesses' entries:
//
//  1. NO "matcher" KEY. Codex matchers are REGEXES and "*" is not a valid one;
//     matcher is Option<String> in the parser and an absent matcher matches
//     every tool, which is what a PreToolUse hook governing everything wants.
//     The MANAGED lane ships matcher = "*" on purpose, byte-matching a proven
//     working file (installcodexmanaged.go); here every changed byte re-gates
//     the operator's trust hash, so it stays omitted.
//  2. SessionEnd carries an explicit timeout: codex defaults to 600 s for most
//     events but ONE SECOND for SessionEnd (3 s maximum), and SessionEnd is
//     where straza drains the audit spool.
//
// Nothing invented: HooksFile is deny_unknown_fields, so an unknown key does
// not degrade, it makes codex skip the whole file.
func codexHookEntry(event, cmd string) map[string]any {
	handler := map[string]any{"type": "command", "command": cmd}
	if event == "SessionEnd" {
		handler["timeout"] = 3
	}
	return map[string]any{"hooks": []any{handler}}
}

// codexHooksTable returns the event→entries table inside a hooks.json
// document, creating it when absent. The envelope is the one thing this file
// must never get wrong, so it lives in exactly one pair of functions with
// its citation attached: hooks.json holds the event map under a top-level
// "hooks" key (the same envelope claude-code's settings.json uses) beside an
// optional "description", AND NOTHING ELSE. The parser struct (codex-rs/
// config/src/hook_config.rs, HooksFile) is deny_unknown_fields: a stray
// "version" or "$schema" key does not get ignored, it makes codex reject the
// whole file with a warning and run none of our hooks.
func codexHooksTable(doc map[string]any) map[string]any {
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	return hooks
}

// codexSetHooksTable puts the table back, dropping the key entirely when it is
// empty so an uninstalled file does not keep a vestigial "hooks": {}.
func codexSetHooksTable(doc map[string]any, hooks map[string]any) {
	if len(hooks) == 0 {
		delete(doc, "hooks")
		return
	}
	doc["hooks"] = hooks
}

// removeCodexHookEntries strips every straza entry from a hook table in place,
// dropping an event key that holds nothing else, and reports how many entries
// it removed. The count is what lets the callers leave a file they have no
// business rewriting completely alone; see MigrateCodexStaleHooks.
func removeCodexHookEntries(hooks map[string]any) int {
	removed := 0
	for event, entries := range hooks {
		arr, ok := entries.([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(arr))
		for _, item := range arr {
			if codexEntryIsStraza(item) {
				removed++
				continue
			}
			kept = append(kept, item)
		}
		if len(kept) == 0 {
			delete(hooks, event)
			continue
		}
		hooks[event] = kept
	}
	return removed
}

// codexEntryIsStraza reports whether one hook entry invokes straza, by the
// stable command tail rather than an exact path (see codexHookMarker).
func codexEntryIsStraza(item any) bool {
	m, ok := item.(map[string]any)
	if !ok {
		return false
	}
	inner, ok := m["hooks"].([]any)
	if !ok {
		return false
	}
	for _, h := range inner {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if c, _ := hm["command"].(string); containsMarker(c) {
			return true
		}
	}
	return false
}

// containsMarker matches both spellings a straza codex hook command can have:
// the direct form's stable "hook --harness codex" tail, and (Windows) the
// single-token shim path, whose leaf is the equally stable codexShimName.
func containsMarker(command string) bool {
	return strings.Contains(command, codexHookMarker) || codexShimCommand(command)
}

// removeCodexDescription drops the file-level description install wrote, and
// only that one: a reworded description is the operator's sentence about their
// own file, and an uninstall is not a licence to delete it. Without this an
// otherwise-emptied hooks.json would survive as a husk that still says
// "straza", which is precisely the kind of debris this change is about.
func removeCodexDescription(doc map[string]any) bool {
	if d, _ := doc["description"].(string); d == codexHooksDescription {
		delete(doc, "description")
		return true
	}
	return false
}

// codexDocEmpty reports whether nothing but empty containers is left: the
// test for "straza's wiring was all this file held".
func codexDocEmpty(doc map[string]any) bool {
	for _, v := range doc {
		switch t := v.(type) {
		case map[string]any:
			if len(t) > 0 {
				return false
			}
		case []any:
			if len(t) > 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// readCodexHooksDoc reads hooks.json, treating a missing file as an empty
// document. A file that exists but will not parse is an error: straza will not
// overwrite something it cannot read.
func readCodexHooksDoc(path string) (map[string]any, error) {
	doc := map[string]any{}
	raw, err := os.ReadFile(path) // #nosec G304 -- managing the harness's own hook file
	if os.IsNotExist(err) {
		return doc, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return doc, nil
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("existing %s is not valid JSON: %w", filepath.Base(path), err)
	}
	return doc, nil
}

// writeCodexHooksDoc writes the document, reporting whether the bytes changed.
// An unchanged file is not rewritten at all (see InstallCodexHooks on trust
// hashing). A fresh file gets 0600; an existing one keeps its owner's mode.
func writeCodexHooksDoc(path string, doc map[string]any) (bool, error) {
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false, err
	}
	out = append(out, '\n')
	mode := os.FileMode(0o600)
	if fi, statErr := os.Stat(path); statErr == nil {
		mode = fi.Mode().Perm()
		if current, readErr := os.ReadFile(path); readErr == nil && string(current) == string(out) { // #nosec G304 -- the harness's own hook file
			return false, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, out, mode) // #nosec G306 -- user-scope hook file, existing mode preserved
}
