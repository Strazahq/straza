package agentguard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Codex hook TRUST, read from outside codex: best effort, never a claim we
// cannot back. Codex skips a hook from a non-managed source (our
// $CODEX_HOME/hooks.json) until an operator trusts its exact definition in the
// `/hooks` UI, so "install wrote the file" and "codex runs our hooks" are
// different statements, and doctor must not print the second on the strength
// of the first. The decision lives in $CODEX_HOME/config.toml under
// [hooks.state], keyed "<declaring file path>:<snake_case event>:<group
// index>:<handler index>", carrying `trusted_hash` plus `enabled` (a per-hook
// off switch); only the User and SessionFlags layers are consulted for it, so
// that one file is the whole story. The hash covers a normalized identity
// codex builds internally, not the file's bytes, so a trusted_hash means
// "trusted at some point", never "codex will run it now". Nothing here parses
// TOML (no dependency; see installmcptoml.go): an unreadable shape comes back
// UNKNOWN and reports as "cannot verify", never as trusted, never untrusted.

// CodexHookTrust is what codex's own state table says about the hooks straza
// wrote. Readable is false when the state cannot be determined at all (no
// config.toml, no [hooks.state], or a shape this reader does not understand).
type CodexHookTrust struct {
	Readable  bool
	Trusted   []string // events codex records a trusted_hash for
	Untrusted []string // events wired in hooks.json with no trust record
	Disabled  []string // events an operator switched off (enabled = false)
	// HooksFeatureOff reports `hooks = false` under [features] in the user's
	// config.toml, the kill switch that disables the whole hook lane
	// regardless of what any hooks.json says.
	HooksFeatureOff bool
}

// Enforcing reports whether codex's own records say every straza hook is
// trusted and none is switched off. It is the ONLY basis on which doctor calls
// the codex hook lane green.
func (t CodexHookTrust) Enforcing() bool {
	return t.Readable && !t.HooksFeatureOff && len(t.Trusted) > 0 &&
		len(t.Untrusted) == 0 && len(t.Disabled) == 0
}

// ReadCodexHookTrust reports what codex's config.toml records about the straza
// hooks in hooksPath. It never errors: an unreadable or absent file is an
// unreadable trust state, which is a legitimate answer here.
func ReadCodexHookTrust(hooksPath, configPath string) CodexHookTrust {
	out := CodexHookTrust{}
	events := codexWrittenEntryIndex(hooksPath)
	if len(events) == 0 {
		return out
	}
	raw, err := os.ReadFile(configPath) // #nosec G304 -- reading the harness's own config file
	if err != nil {
		return out
	}
	content := string(raw)
	out.HooksFeatureOff = codexHooksFeatureDisabled(content)
	state := codexHookStateEntries(content)
	if state == nil {
		return out
	}
	out.Readable = true
	for _, ev := range installs["codex"].allEvents() {
		idx, ok := events[ev]
		if !ok {
			continue
		}
		key := fmt.Sprintf("%s:%s:%d:0", hooksPath, codexEventStateLabel(ev), idx)
		st, known := state[key]
		switch {
		case !known:
			out.Untrusted = append(out.Untrusted, ev)
		case st.disabled:
			out.Disabled = append(out.Disabled, ev)
		case st.trustedHash != "":
			out.Trusted = append(out.Trusted, ev)
		default:
			out.Untrusted = append(out.Untrusted, ev)
		}
	}
	return out
}

// codexManagedShimCheck verifies the Windows hook shim when the managed block
// references one, and flags the pre-shim direct form. Nil when there is
// nothing to say: non-Windows (the direct form is correct there, since unix hook
// shells tokenize `-c` strings properly), an unreadable block (other checks
// own that), or a healthy shim.
//
// Severity is deliberate: a REFERENCED-but-missing or drifted shim is a FAIL
// because every codex hook dies at spawn or may have had its streams
// redirected, and codex honors an exit-2 block only when stderr reaches it,
// so either shape is governance failing open. The direct form still WIRED is
// a warn: it spawns under a cmd-shaped hook shell and dies under a PowerShell
// one, which codex picks from the session's detected user shell, so working
// today is not working tomorrow (live-proven, installcodexshim.go).
func codexManagedShimCheck(reqPath string) *Check {
	if osName() != "windows" {
		return nil
	}
	shim, direct := "", false
	for _, cmd := range tomlHookCommands(reqPath, "codex") {
		switch {
		case codexShimCommand(cmd):
			shim = strings.TrimSpace(cmd)
		case strings.Contains(cmd, codexHookMarker):
			direct = true
		}
	}
	if shim == "" {
		if direct {
			return &Check{"codex-hooks", checkWarn,
				"managed hook commands in " + reqPath + " are the direct spaced form. Codex hands hook commands to the session's shell, and a PowerShell hook shell (the Windows default) kills a spaced spawn: hooks fail open with `hook exited with code 1`",
				"re-run `straza install --managed --server <server-url> codex` (elevated); it converges the block on the single-token " + codexShimName + " shim, the one spelling every shell spawns"}
		}
		return nil
	}
	raw, err := os.ReadFile(shim) // #nosec G304 -- straza's own shim file, path read from the managed block
	if err != nil {
		return &Check{"codex-hooks", checkFail,
			"the managed block invokes " + shim + " but the shim file is missing. Every codex hook dies at spawn, and codex continues the tool call (fail-open)",
			"re-run `straza install --managed --server <server-url> codex` (elevated); it rewrites the shim beside the managed binary"}
	}
	target, ok := codexShimTarget(shim)
	if !ok || string(raw) != codexHookShimContent(target) {
		return &Check{"codex-hooks", checkFail,
			shim + " does not match what install writes (hand-edited or torn). A reshaped shim can redirect hook streams, and a deny whose stderr never reaches codex is silently an allow",
			"re-run `straza install --managed --server <server-url> codex` (elevated) to restore it; the shim must stay a plain passthrough"}
	}
	return nil
}

// codexHooksCheck reports codex's hook lane, and it is the one check in here
// FORBIDDEN to go green on file presence alone. Two things make codex
// different from every other Tier-1 harness. First, straza once wrote its
// wiring into $CODEX_HOME/settings.json, which no codex release reads: dead
// weight that LOOKS like a healthy install, so while it exists this check
// warns, whatever else is true. Second, codex SKIPS a hook from a non-managed
// source until an operator trusts its exact definition in the `/hooks` UI, and
// that trust lives inside codex, so "hooks.json is written" and "codex will
// run our hooks" are different claims this check never conflates.
//
// The only evidence available from outside is a codex session that actually
// checked in: the SessionStart hook is what creates it, so its existence
// proves the hooks ran at least once. With that, ok. Without it, warn and say
// exactly what is unknown.
func codexHooksCheck(ses Session, haveSession bool) *Check {
	hooks, err := CodexHooksPath()
	if err != nil {
		return nil
	}
	stale, err := CodexStaleHooksPath()
	if err != nil {
		return nil
	}
	wired, missing := CodexHooksWritten(hooks)

	if fileMentionsStraz(stale) {
		detail := "dead Straza wiring in " + stale + ": no codex release reads settings.json"
		if !wired {
			detail += ", and " + hooks + " has none"
		}
		return &Check{"codex-hooks", checkWarn, detail,
			"this is the failure mode where wiring looks installed and governs nothing. Run `straza install codex`: it writes " + filepath.Base(hooks) + " and removes the dead file, then trust the hooks in codex's /hooks UI"}
	}
	if !wired {
		layer, layerPath := wiredLayer("codex")
		switch layer {
		case "":
			return nil // codex is simply not in use on this machine
		case "managed":
			// A managed-only box is the CORRECT enterprise state: the
			// requirements.toml lane governs, requirements-sourced hooks are
			// auto-trusted (no /hooks ceremony exists for them), and an empty
			// user hooks.json is exactly what a fleet deployment looks like.
			// A "run `straza install codex`" hint here would ADD a user
			// layer that IT does not govern.
			if c := codexManagedShimCheck(layerPath); c != nil {
				return c
			}
			return &Check{"codex-hooks", checkOK,
				"managed lane (" + layerPath + ") carries the hooks: requirements-sourced hooks are auto-trusted, and no user-scope wiring is needed", ""}
		}
		return &Check{"codex-hooks", checkWarn, "no Straza hooks in " + hooks,
			"run `straza install codex`, then trust the hooks in codex's /hooks UI"}
	}
	if len(missing) > 0 {
		return &Check{"codex-hooks", checkWarn,
			hooks + " is missing " + strings.Join(missing, ", "),
			"these events were added to the installer after this machine last ran it. Re-run `straza install codex`, and re-trust in /hooks afterwards: codex keys trust to the hook definition, so a changed definition is skipped again"}
	}
	// Codex records its trust decisions in the user's config.toml, so the
	// gate is reportable rather than merely warned about (codextrust.go,
	// which answers "cannot verify" wherever it is unsure, never "trusted").
	config, cfgErr := CodexMCPConfigPath()
	if cfgErr != nil {
		config = ""
	}
	trust := ReadCodexHookTrust(hooks, config)
	switch {
	case trust.HooksFeatureOff:
		return &Check{"codex-hooks", checkWarn,
			"wiring written to " + hooks + ", but hooks are switched OFF in " + config + " ([features] hooks = false), so codex runs none of them",
			"remove that setting (or deploy the managed lane: `sudo straza install --managed --server <server-url> codex` pins [features].hooks = true in requirements.toml, which a user config cannot override)"}
	case len(trust.Disabled) > 0:
		return &Check{"codex-hooks", checkWarn,
			"codex records the Straza hooks for " + strings.Join(trust.Disabled, ", ") + " as DISABLED by an operator",
			"re-enable them in codex's /hooks UI. A user-scope hook can always be switched off. A fleet that must not allow that wants the managed lane (`sudo straza install --managed --server <server-url> codex`), whose hooks cannot be disabled"}
	case trust.Enforcing():
		return &Check{"codex-hooks", checkOK,
			"wiring in " + hooks + ", and codex records all " + strconv.Itoa(len(trust.Trusted)) + " Straza hooks as trusted", ""}
	case trust.Readable && len(trust.Untrusted) > 0:
		return &Check{"codex-hooks", checkWarn,
			"codex has no trust record for the Straza hooks on " + strings.Join(trust.Untrusted, ", ") + ", so it will SKIP them",
			"open codex and run /hooks to review + trust the Straza hooks. Codex keys trust to the hook definition's hash, so a re-install (or a moved straza binary) re-gates them"}
	}
	// No readable trust state. A codex session that checked in is the one
	// piece of outside evidence that the hooks did run; the SessionStart hook
	// is what creates it.
	if haveSession && strings.HasPrefix(ses.Harness, "codex/") {
		return &Check{"codex-hooks", checkOK,
			"wiring written to " + hooks + " and a codex session checked in (" + ses.Harness + "), so codex is running the hooks", ""}
	}
	return &Check{"codex-hooks", checkWarn,
		"wiring written to " + hooks + "; trust cannot be verified from outside (codex skips untrusted hooks, and this machine has no readable trust record)",
		"open codex and run /hooks to review + trust the Straza hooks. Until then the codex hook lane is NOT enforcing, and there is no green light to read from out here. Once a governed codex session starts it shows up in `straza status`, and this check reports it"}
}

// codexEventStateLabel converts a codex event name to the snake_case label the
// state key uses (codex-rs/hooks/src/lib.rs, hook_event_key_label).
func codexEventStateLabel(event string) string {
	var b strings.Builder
	for i, r := range event {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// codexWrittenEntryIndex returns, per event, the index of straza's entry in
// that event's array in hooks.json, the group index half of the state key.
// Foreign entries ahead of ours are why this is looked up rather than assumed
// to be zero.
func codexWrittenEntryIndex(hooksPath string) map[string]int {
	out := map[string]int{}
	raw, err := os.ReadFile(hooksPath) // #nosec G304 -- reading the harness's own hook file
	if err != nil {
		return out
	}
	doc := map[string]any{}
	if json.Unmarshal(raw, &doc) != nil {
		return out
	}
	for event, entries := range codexHooksTable(doc) {
		arr, _ := entries.([]any)
		for i, item := range arr {
			if codexEntryIsStraza(item) {
				out[event] = i
				break
			}
		}
	}
	return out
}

// codexHookState is one [hooks.state] row.
type codexHookState struct {
	trustedHash string
	disabled    bool
}

// codexHookStateEntries reads the [hooks.state] table out of a config.toml,
// returning nil when the file has no such table at all (unknown, not empty).
// It understands the two shapes that occur in practice: the per-key table
// codex serializes,
//
//	[hooks.state."/home/u/.codex/hooks.json:pre_tool_use:0:0"]
//	trusted_hash = "…"
//
// and the inline form under a [hooks.state] header (or as a dotted key),
//
//	"/home/u/.codex/hooks.json:pre_tool_use:0:0" = { enabled = false }
//
// Nothing else is guessed at: an unrecognized shape simply contributes no row,
// and a hook with no row reads as untrusted, which is the safe direction.
func codexHookStateEntries(content string) map[string]codexHookState {
	var out map[string]codexHookState
	var current string // key whose sub-table we are inside
	inStateTable := false

	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			current, inStateTable = "", false
			path, ok := codexStateTableHeader(line)
			if !ok {
				continue
			}
			switch {
			case len(path) == 3 && path[0] == "hooks" && path[1] == "state":
				if out == nil {
					out = map[string]codexHookState{}
				}
				current = path[2]
				if _, seen := out[current]; !seen {
					out[current] = codexHookState{}
				}
			case len(path) == 2 && path[0] == "hooks" && path[1] == "state":
				if out == nil {
					out = map[string]codexHookState{}
				}
				inStateTable = true
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if current != "" {
			st := out[current]
			switch key {
			case "trusted_hash":
				st.trustedHash = strings.Trim(value, `"'`)
			case "enabled":
				st.disabled = strings.HasPrefix(value, "false")
			}
			out[current] = st
			continue
		}
		// An inline row, either under [hooks.state] or as a dotted key.
		segments := codexQuotedKeyPath(key)
		var name string
		switch {
		case inStateTable && len(segments) == 1:
			name = segments[0]
		case len(segments) == 3 && segments[0] == "hooks" && segments[1] == "state":
			name = segments[2]
		default:
			continue
		}
		if out == nil {
			out = map[string]codexHookState{}
		}
		out[name] = codexHookState{
			trustedHash: codexInlineField(value, "trusted_hash"),
			disabled:    strings.Contains(strings.ReplaceAll(codexInlineField(value, "enabled"), " ", ""), "false"),
		}
	}
	return out
}

// codexStateTableHeader splits a table header into its key path, honoring
// quoted segments. It cannot reuse installmcptoml.go's header splitter: that
// one splits on every "." including inside quotes, which is harmless for
// [mcp_servers.straza] but destroys these keys: a codex state key is a
// QUOTED FILE PATH, and "hooks.json" alone contains a dot.
func codexStateTableHeader(line string) ([]string, bool) {
	name := strings.TrimPrefix(strings.TrimPrefix(line, "["), "[")
	idx := strings.LastIndex(name, "]")
	if idx < 0 {
		return nil, false
	}
	return codexQuotedKeyPath(name[:idx]), true
}

// codexQuotedKeyPath splits a dotted TOML key into segments, treating a
// quoted segment as one unit and unquoting it.
func codexQuotedKeyPath(key string) []string {
	var (
		parts   []string
		start   int
		quote   byte
		escaped bool
	)
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case escaped:
			if !strings.ContainsRune(`"\bfnrtuU`, rune(c)) {
				return nil
			}
			escaped = false
		case quote == '"' && c == '\\':
			escaped = true
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '.':
			parts = append(parts, key[start:i])
			start = i + 1
		}
	}
	if quote != 0 || escaped {
		return nil
	}
	parts = append(parts, key[start:])
	for i, part := range parts {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, `"`):
			decoded, err := strconv.Unquote(part)
			if err != nil {
				return nil
			}
			parts[i] = decoded
		case strings.HasPrefix(part, "'"):
			if len(part) < 2 || !strings.HasSuffix(part, "'") || strings.ContainsAny(part[1:len(part)-1], "'\r\n") {
				return nil
			}
			parts[i] = part[1 : len(part)-1]
		default:
			if part == "" || strings.ContainsFunc(part, func(r rune) bool {
				return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
					(r < '0' || r > '9') && r != '_' && r != '-'
			}) {
				return nil
			}
			parts[i] = part
		}
	}
	return parts
}

// codexInlineField pulls one field out of an inline table ({ a = 1, b = "x" }).
func codexInlineField(value, field string) string {
	idx := strings.Index(value, field)
	if idx < 0 {
		return ""
	}
	rest := value[idx+len(field):]
	_, after, ok := strings.Cut(rest, "=")
	if !ok {
		return ""
	}
	after = strings.TrimSpace(after)
	if end := strings.IndexAny(after, ",}"); end >= 0 {
		after = after[:end]
	}
	return strings.Trim(strings.TrimSpace(after), `"'`)
}

// codexHooksFeatureDisabled reports `hooks = false` under [features] (or as the
// dotted key features.hooks), the switch that turns the whole hook lane off,
// wiring and trust regardless. A managed requirements.toml pinning
// [features].hooks = true is what overrides it, which is why the managed lane
// writes exactly that.
func codexHooksFeatureDisabled(content string) bool {
	inFeatures := false
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			path, ok := codexStateTableHeader(line)
			inFeatures = ok && len(path) == 1 && path[0] == "features"
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		segments := codexQuotedKeyPath(key)
		value = strings.TrimSpace(value)
		named := len(segments) == 2 && segments[0] == "features" &&
			(segments[1] == "hooks" || segments[1] == "codex_hooks")
		scoped := inFeatures && len(segments) == 1 &&
			(segments[0] == "hooks" || segments[0] == "codex_hooks")
		if (named || scoped) && strings.HasPrefix(value, "false") {
			return true
		}
	}
	return false
}
