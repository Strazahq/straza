package agentguard

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Doctor's wiring findings: which harnesses carry Straza hook registrations, at
// which layer, for which events, and whether the command
// those registrations name is a straza binary that is actually there.
//
// Each answers a question the others cannot. A file can mention straza while
// registering none of the events that matter (the capture blind
// spot), and every event can be registered against a binary that moved three
// installs ago (the ghost this file's hookBinaryCheck now catches). Both look
// identical to a healthy install from inside the settings file.

// tier1Harnesses is the roster doctor walks, in report order.
var tier1Harnesses = []string{"claude-code", "codex", "gemini"}

// wiringCheck reports which Tier-1 harnesses have Straza hook wiring, at which
// layer(s) (every wired layer is listed, the harness can run both) and
// whether the roster's events are all covered. Event coverage is judged on the
// UNION of the harness's verified layers: an event is dead only when NO layer
// registers it (a user file predating an event is fine while the managed lane
// carries it), but a layer that cannot be parsed still taints the answer:
// unknown coverage is never reported complete.
func wiringCheck() Check {
	var wired, missing, stale, staleHarnesses, partial, unverifiable []string
	var fixUser, fixManaged []string
	for _, harness := range tier1Harnesses {
		layers := wiredLayers(harness)
		if len(layers) == 0 {
			missing = append(missing, harness)
		}
		covered := map[string]bool{}
		var judged []string // layers whose per-event coverage was readable
		for _, l := range layers {
			label := harness + " (" + l.layer + ")"
			if harness == "codex" && l.layer == "user" {
				// Written ≠ running on codex: a non-managed hook waits on the
				// operator's /hooks trust step. Never let this line read as
				// "codex governed" on its own.
				label = harness + " (" + l.layer + "; trust gate, see codex-hooks)"
			}
			wired = append(wired, label)
			gaps, verified := missingHookEvents(l.path, harness)
			if !verified {
				unverifiable = append(unverifiable, harness+" ("+l.layer+"): "+l.path)
				bucketFix(&fixUser, &fixManaged, l.layer, harness)
				continue
			}
			judged = append(judged, l.layer)
			gapSet := map[string]bool{}
			for _, g := range gaps {
				gapSet[g] = true
			}
			for _, ev := range installs[harness].allEvents() {
				if !gapSet[ev] {
					covered[ev] = true
				}
			}
		}
		if len(judged) > 0 {
			var gaps []string
			for _, ev := range installs[harness].allEvents() {
				if !covered[ev] {
					gaps = append(gaps, ev)
				}
			}
			if len(gaps) > 0 {
				// Union gaps are, by construction, missing from EVERY judged
				// layer, so each one's reinstall is a fix.
				partial = append(partial, harness+" ("+strings.Join(judged, ", ")+") is missing "+strings.Join(gaps, ", "))
				for _, layer := range judged {
					bucketFix(&fixUser, &fixManaged, layer, harness)
				}
			}
		}
		if legacy := LegacyManagedSettingsPath(harness); legacy != "" && fileMentionsStraz(legacy) {
			stale = append(stale, harness+" ("+legacy+")")
			staleHarnesses = append(staleHarnesses, harness)
		}
	}
	if len(stale) > 0 {
		// Worse than unwired: it LOOKS installed but the harness never reads
		// that path anymore, so governance is silently off.
		return Check{"wiring", checkWarn,
			"retired managed wiring still present: " + strings.Join(stale, ", "),
			"current harness releases no longer read that path. Re-run `straza install --managed --server <server-url> " +
				strings.Join(staleHarnesses, " ") + "` (writes the current vendor path and removes the stale file)"}
	}
	if len(wired) == 0 {
		return Check{"wiring", checkWarn, "no harness has Straza hooks wired",
			"run `straza install claude-code` (or codex/gemini). Enterprise layouts use `straza install --managed --server <server-url> claude-code` (or codex/gemini)"}
	}
	if len(partial) > 0 {
		// The blind spot this check exists to close: an install predating an
		// event's addition to the roster leaves governance working while the
		// events registered later (conversation capture's, historically)
		// never fire. Nothing else in doctor could tell that apart from a
		// healthy install, because the settings file does mention straza.
		detail := "incomplete hook registration: " + strings.Join(partial, "; ")
		if len(unverifiable) > 0 {
			detail += "; cannot verify event coverage for " + strings.Join(unverifiable, "; ")
		}
		return Check{"wiring", checkWarn, detail,
			"these events were added to the installer after this machine last ran it, so the features behind them (conversation capture rides prompt-submit and turn-end; subagent capture rides SubagentStop) are silently off. Re-run " +
				reinstallFix(fixUser, fixManaged) + ". Upgrading the binary alone does NOT re-register hooks"}
	}
	if len(unverifiable) > 0 {
		// The OPPOSITE fail-safe direction from the readers underneath (see
		// missingHookEvents): where they refuse to manufacture a registration
		// from a line they cannot parse, this check refuses to manufacture a
		// clean bill from a file it cannot read; silence here would claim
		// complete coverage.
		return Check{"wiring", checkWarn,
			"cannot verify hook event coverage: " + strings.Join(unverifiable, "; "),
			"the file mentions straza but holds no hook registration this reader can parse (a hand-edited block may still work; doctor cannot check it per event), so an event added after that install would be silently off with no way to see it here. Re-run " +
				reinstallFix(fixUser, fixManaged) + " to rewrite straza's wiring in the shape every check here reads"}
	}
	detail := strings.Join(wired, ", ")
	if len(missing) > 0 {
		detail += "; not wired: " + strings.Join(missing, ", ")
	}
	return Check{"wiring", checkOK, detail, ""}
}

// hookLayer is one wiring layer (managed or user) and the file carrying it.
type hookLayer struct{ layer, path string }

// wiredLayers reports EVERY layer carrying a harness's Straza hook wiring, in
// precedence order, managed first. The union exists because a harness can run
// hooks from BOTH sources at once (codex does, unless an operator sets
// allow_managed_hooks_only), so a dead user lane beside a healthy managed one
// is a real finding that a first-match view cannot see: the harness still
// tries the dead commands on every governed event.
func wiredLayers(harness string) []hookLayer {
	var out []hookLayer
	if p, err := managedHookPath(harness); err == nil && fileMentionsStraz(p) {
		out = append(out, hookLayer{"managed", p})
	}
	if p, err := SettingsPath(harness); err == nil && fileMentionsStraz(p) {
		out = append(out, hookLayer{"user", p})
	}
	return out
}

// wiredLayer is the headline view of wiredLayers: the winning layer (managed
// beats user) and its file. An empty layer means not wired at all.
func wiredLayer(harness string) (layer, path string) {
	layers := wiredLayers(harness)
	if len(layers) == 0 {
		return "", ""
	}
	return layers[0].layer, layers[0].path
}

// The managed hook file doctor reads resolves through managedHookPath
// (managed.go): the SAME resolver install writes through and attestation
// hashes through, so the three surfaces cannot diverge by construction.

// hookBinaryCheck resolves the command every hook registration invokes.
//
// The ghost: hook wiring names an ABSOLUTE path decided at install time, and
// nothing rewrites it when the binary moves, is renamed or re-staged. The
// registration still reads perfect from inside the settings file while it
// points at nothing, so the harness runs nothing on every governed event and
// every other doctor check reads healthy. Matching on the command TAIL rather
// than the path, which is what lets registeredHookEvents survive a relocated
// binary, is what hides it, so the path is checked here on its own line.
//
// A missing path is FAIL. A bare program name off this PATH, a command that
// does not split into one path (hookCommandBinary), and a file that exists but
// is not named straza are WARN: unverifiable is not broken. Nothing wired means
// no line. The binary is never EXECUTED: existence plus name separates a ghost
// from a live install, and running an unknown path to identify it is a footgun.
func hookBinaryCheck() *Check {
	regs := hookRegistrations()
	if len(regs) == 0 {
		return nil
	}
	var ghosts, unverified, verified []string
	var fixUser, fixManaged []string
	for _, r := range regs {
		switch classifyHookBinary(r.binary, r.exact) {
		case hookBinaryGhost:
			ghosts = append(ghosts, r.label()+" invokes "+r.binary+", which is not there")
			if r.layer == "managed" {
				fixManaged = appendOnce(fixManaged, r.harness)
			} else {
				fixUser = appendOnce(fixUser, r.harness)
			}
		case hookBinaryUnresolved:
			unverified = append(unverified, r.label()+" invokes "+r.binary+" by name, which is not on this shell's PATH")
		case hookBinaryAmbiguous:
			unverified = append(unverified, r.label()+" invokes "+r.binary+"…, which doctor cannot read as a single path (a wrapper command?)")
		case hookBinaryForeign:
			unverified = append(unverified, r.label()+" invokes "+r.binary+", which exists but is not a straza binary")
		default:
			verified = append(verified, r.label()+" → "+r.binary)
		}
	}
	switch {
	case len(ghosts) > 0:
		return &Check{"hook-binary", checkFail,
			"hook wiring points at a binary that is NOT there: " + strings.Join(ghosts, "; "),
			"straza moved or was renamed after it was installed, so the harness runs a dead command on every governed event. Governance is OFF while this reads as wired. Re-run " +
				reinstallFix(fixUser, fixManaged) + " to rewrite the path (it replaces straza's entries rather than adding another)"}
	case len(unverified) > 0:
		return &Check{"hook-binary", checkWarn, strings.Join(unverified, "; "),
			"doctor cannot confirm these registrations reach straza from here. If the harness resolves them differently that is fine; if not, re-run `straza install <harness>` to wire the absolute path of the binary you are running"}
	}
	return &Check{"hook-binary", checkOK, strings.Join(verified, ", "), ""}
}

// reinstallFix renders the install command(s) that rewrite a ghost path, per
// scope: managed wiring needs root, user wiring must not ask for it.
func reinstallFix(user, managed []string) string {
	var out []string
	if len(user) > 0 {
		out = append(out, "`straza install "+strings.Join(user, " ")+"`")
	}
	if len(managed) > 0 {
		out = append(out, "`sudo straza install --managed --server <server-url> "+strings.Join(managed, " ")+"`")
	}
	return strings.Join(out, " and ")
}

func appendOnce(list []string, s string) []string {
	for _, existing := range list {
		if existing == s {
			return list
		}
	}
	return append(list, s)
}

// bucketFix records a harness under the fix list its broken layer needs:
// managed wiring needs the sudo form, user wiring must not ask for it.
func bucketFix(user, managed *[]string, layer, harness string) {
	if layer == "managed" {
		*managed = appendOnce(*managed, harness)
	} else {
		*user = appendOnce(*user, harness)
	}
}

// hookRegistration is one distinct (harness, layer, binary) a hook file
// registers. Distinct, not per-event: seven events wired to the same command
// are one fact about one binary, and reporting them seven times would bury it.
type hookRegistration struct {
	harness string
	layer   string
	binary  string
	// exact records that the command split cleanly into one executable;
	// see hookCommandBinary. Only an exact split may be called a ghost.
	exact bool
}

func (r hookRegistration) label() string { return r.harness + " (" + r.layer + ")" }

// hookRegistrations reads EVERY wiring file each harness is wired through
// (both layers when both carry straza, because the harness runs both) and
// returns the binaries the registrations invoke: a user-lane ghost beside a
// healthy managed lane still errors on every governed event.
func hookRegistrations() []hookRegistration {
	var out []hookRegistration
	for _, harness := range tier1Harnesses {
		for _, l := range wiredLayers(harness) {
			for _, cmd := range hookCommandsIn(l.path, harness) {
				binary, exact := hookCommandBinary(cmd, harness)
				if binary == "" {
					continue // nothing doctor can resolve: claim nothing
				}
				reg := hookRegistration{harness, l.layer, binary, exact}
				if !hasRegistration(out, reg) {
					out = append(out, reg)
				}
			}
		}
	}
	return out
}

func hasRegistration(list []hookRegistration, want hookRegistration) bool {
	for _, r := range list {
		if r == want {
			return true
		}
	}
	return false
}

// hookCommandsIn returns the Straza hook commands a wiring file registers for a
// harness. JSON covers every user-scope file plus the claude-code and gemini
// managed files; codex's managed layer is requirements.toml, so that dialect is
// read too: a ghost there is just as dead, and it is the lane a fleet runs on.
func hookCommandsIn(path, harness string) []string {
	if filepath.Ext(path) != ".json" {
		return tomlHookCommands(path, harness)
	}
	var out []string
	raw, err := os.ReadFile(path) // #nosec G304 -- reading the harness's own settings file
	if err != nil {
		return nil
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(raw, &settings) != nil {
		return nil
	}
	for _, entries := range settings.Hooks {
		for _, entry := range entries {
			for _, hook := range entry.Hooks {
				if strazaHookCommand(hook.Command, harness) {
					out = append(out, hook.Command)
				}
			}
		}
	}
	return out
}

// tomlHookRegistration is one straza hook command found in a requirements.toml
// and the event whose [[hooks.<Event>…]] tables it sits under, or "" when the
// command appears outside any event table. An event-less command still counts
// for the binary check (a ghost outside a header is just as dead) but NEVER as
// an event registration: a mangled file must not manufacture one.
type tomlHookRegistration struct{ event, command string }

// tomlHookRegistrations pulls straza hook commands out of a requirements.toml,
// tagged with their event. Deliberately narrow, same contract this reader was
// born with (as tomlHookCommands): it reads the LITERAL strings straza itself
// writes (command = '<path> hook --harness codex'), which have no escape
// sequences, so what comes out is a path that can be stat'd verbatim. A basic
// (double-quoted) string is skipped rather than half-unescaped: a
// registration this reader mangled would be worse than none. The one error is
// an unreadable file; everything else degrades to fewer rows, never to a
// guessed one.
func tomlHookRegistrations(path, harness string) ([]tomlHookRegistration, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- reading the harness's own managed hook file
	if err != nil {
		return nil, err
	}
	var out []tomlHookRegistration
	event := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			// Any table header moves the cursor: [[hooks.<Event>]] and
			// [[hooks.<Event>.hooks]] enter that event's tables, and every
			// other header ([hooks] itself, [features], an operator's own
			// table) leaves whatever event we were in.
			event = tomlHooksEvent(line)
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "command" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) < 2 || value[0] != '\'' {
			continue
		}
		end := strings.IndexByte(value[1:], '\'')
		if end < 0 {
			continue
		}
		if cmd := value[1 : 1+end]; strazaHookCommand(cmd, harness) {
			out = append(out, tomlHookRegistration{event, cmd})
		}
	}
	return out, nil
}

// tomlHooksEvent returns the event a table header enters ([[hooks.<Event>]] or
// [[hooks.<Event>.hooks]]), or "" for any other header. Events straza writes
// are bare identifiers, so a plain dot-split is exact here; the quoted-path
// complexity codexStateTableHeader handles cannot occur in these headers.
func tomlHooksEvent(header string) string {
	name := strings.TrimPrefix(strings.TrimPrefix(header, "["), "[")
	end := strings.IndexByte(name, ']')
	if end < 0 {
		return ""
	}
	parts := strings.Split(name[:end], ".")
	if len(parts) < 2 || strings.TrimSpace(parts[0]) != "hooks" {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// tomlHookCommands is the binary-check view of tomlHookRegistrations: just the
// commands, event or no event.
func tomlHookCommands(path, harness string) []string {
	regs, err := tomlHookRegistrations(path, harness)
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range regs {
		out = append(out, r.command)
	}
	return out
}

// hookCommandBinary splits the executable off a Straza hook command, and
// reports whether that split is unambiguous. The harness marker is the split
// point rather than the first space, so a path with spaces in it survives
// (install quotes those only on Windows).
//
// An unquoted head with a space in it could be such a path, or it could be a
// wrapper (`/bin/sh -c '/opt/straza hook --harness codex'`) whose first token is
// not straza at all. exact is false there, and a head that is not exact is
// never called a ghost: an operator's working wrapper must not be reported as
// broken governance on a parse guess.
func hookCommandBinary(command, harness string) (binary string, exact bool) {
	if harness == "codex" && codexShimCommand(command) {
		// The command is the Windows shim: resolve THROUGH it to the binary
		// it invokes, so this check keeps answering "where does the hook
		// land" rather than "does a wrapper file exist". A shim that is
		// missing or reshaped cannot be resolved: then the shim path itself
		// is the target to stat, which reports a missing shim as exactly the
		// ghost it is (hooks reference a file that is not there).
		shim := strings.TrimSpace(command)
		if target, ok := codexShimTarget(shim); ok {
			return target, true
		}
		return shim, true
	}
	head, _, ok := strings.Cut(command, " "+hookMarker(harness))
	if !ok {
		return "", false
	}
	head = strings.TrimSpace(head)
	if len(head) >= 2 {
		if q := head[0]; (q == '"' || q == '\'') && head[len(head)-1] == q {
			return head[1 : len(head)-1], true
		}
	}
	return head, !strings.ContainsAny(head, " \t")
}

// hookMarker is the stable tail every Straza hook command carries.
func hookMarker(harness string) string { return "hook --harness " + harness }

// hookBinaryState is what doctor could establish about a registered command.
type hookBinaryState int

const (
	hookBinaryVerified hookBinaryState = iota
	hookBinaryGhost
	hookBinaryUnresolved
	hookBinaryAmbiguous
	hookBinaryForeign
)

func classifyHookBinary(binary string, exact bool) hookBinaryState {
	if !strings.ContainsAny(binary, `/\`) && exact {
		// A bare program name: the harness resolves it against its own PATH.
		// LookPath only reads the environment; it never runs anything.
		if _, err := exec.LookPath(binary); err != nil {
			return hookBinaryUnresolved
		}
		return hookBinaryVerified
	}
	fi, err := os.Stat(binary)
	if err != nil || fi.IsDir() {
		if !exact {
			return hookBinaryAmbiguous
		}
		return hookBinaryGhost
	}
	if !strazaBinaryName(binary) {
		return hookBinaryForeign
	}
	return hookBinaryVerified
}

// strazaBinaryName reports whether a path's leaf looks like the straza binary.
// A prefix match, because staged builds arrive as straza-3.1.0, straza.new and
// friends: the question here is "is this plausibly our binary", and the answer
// only ever downgrades a finding to "cannot confirm", never to a failure.
func strazaBinaryName(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return strings.HasPrefix(strings.TrimSuffix(base, ".exe"), "straza")
}

// missingHookEvents reports which of a harness's required hook events carry no
// Straza command in the given wiring file, and whether that answer may be
// trusted. verified=false means the file could not be read as hook wiring at
// all, and the caller must say "cannot verify event coverage", never render a
// clean bill.
//
// Two fail-safe directions meet here, deliberately OPPOSITE:
//   - the READERS underneath (tomlHookRegistrations, registeredHookEvents)
//     never manufacture a registration out of a line they cannot parse: a
//     mangled file must not make a dead event look wired;
//   - the GAP CHECK never manufactures a clean bill out of a file it cannot
//     read: a mangled file must not make unknown coverage look complete.
//
// Both refuse to be wrong in the reassuring direction.
func missingHookEvents(path, harness string) (gaps []string, verified bool) {
	h, ok := installs[harness]
	if !ok {
		return nil, true
	}
	present := registeredHookEvents(path, harness)
	if harness == "codex" {
		// codex's user file is hooks.json (same JSON envelope, its own reader
		// below) and its managed file is requirements.toml. That TOML is read
		// too, because skipping it would report the enterprise lane COMPLETE
		// whatever it held.
		if filepath.Ext(path) != ".json" {
			regs, err := tomlHookRegistrations(path, harness)
			if err != nil {
				return nil, false // unreadable: coverage unknown, never complete
			}
			present = map[string]bool{}
			for _, r := range regs {
				if r.event != "" { // a command outside an event header registers no event
					present[r.event] = true
				}
			}
			if len(present) == 0 {
				// The file mentions straza (that is why the caller is here) but
				// the reader can extract no event registration: a mangled or
				// hand-reshaped block. Unknown: not complete, and not "every
				// event missing" either.
				return nil, false
			}
		} else {
			present = codexRegisteredEvents(path)
		}
	}
	for _, ev := range h.allEvents() {
		if !present[ev] {
			gaps = append(gaps, ev)
		}
	}
	return gaps, true
}

// registeredHookEvents returns the events in a harness settings file that have
// a Straza hook wired. Matching is on the command's stable "hook --harness
// <harness>" tail rather than the binary path, so a straza that was moved or
// renamed since install still reads as registered: the question here is which
// EVENTS are wired, not where the binary lives (that is hookBinaryCheck's, and
// it exists because this rule hid a whole failure mode).
func registeredHookEvents(path, harness string) map[string]bool {
	out := map[string]bool{}
	raw, err := os.ReadFile(path) // #nosec G304 -- reading the harness's own settings file
	if err != nil {
		return out
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(raw, &settings) != nil {
		return out
	}
	for event, entries := range settings.Hooks {
		for _, entry := range entries {
			for _, hook := range entry.Hooks {
				if strazaHookCommand(hook.Command, harness) {
					out[event] = true
				}
			}
		}
	}
	return out
}

func fileMentionsStraz(path string) bool {
	raw, err := os.ReadFile(path) // #nosec G304 -- reading the harness's own settings file
	return err == nil && strings.Contains(string(raw), "straza")
}
