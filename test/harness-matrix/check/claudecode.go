// claudecode.go: the claude-code lane's gates.
//
// The harness's telemetry surface is its debug log: `claude --debug` writes
// $CLAUDE_CONFIG_DIR/debug/<session-id>.txt (plus a `latest` copy), and that
// file, not stdout, is where per-hook accept/reject lines live. The lane
// concatenates claude's stderr with that log into one capture and passes it
// as -harness-stderr, so every gate reads its verdict from claude's own words.
//
//	accepted JSON ack   Successfully parsed and validated hook JSON output
//	context taken       provided additionalContext (N chars)
//	empty stdout        Hook output does not start with {, treating as plain
//	                    text (silence is a valid ack, not a failure)
//	rejected JSON ack   Hook JSON output validation failed, plus a user-visible
//	                    `<Event> hook [<cmd>] failed:` line on stderr
//	hook exited != 0    Hook <ev> (<ev>) error:, completed with status N
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// claudeRoster is every native event `straza install claude-code` registers,
// in install.go roster order (installs["claude-code"].allEvents()). The static
// gate asserts the written file carries exactly this set: a missing event is
// ungoverned, an unexpected one is wiring nobody asked for.
var claudeRoster = []string{
	"SessionStart", "PreToolUse", "UserPromptSubmit",
	"Stop", "SessionEnd", "SubagentStart", "SubagentStop",
}

// keyFreeClaudeEvents are the events a fake-key `claude -p` turn actually
// fires. Verified on claude 2.1.220: the session opens (SessionStart), the
// prompt submits (UserPromptSubmit), the model call dies on auth, and the
// session closes (SessionEnd). Stop, PreToolUse and the Subagent pair need a
// COMPLETING model turn and are key-gated: they belong to a live-model gate,
// and their absence here is not a failure.
var keyFreeClaudeEvents = []string{"SessionStart", "UserPromptSubmit", "SessionEnd"}

// claudePlainTextAck is what claude logs for a hook that acked with SILENCE:
// the empty-stdout success path, and the ack every
// non-enforceable event gets. Counting these lines is the positive control
// that claude processed the silent acks rather than never running the hook.
const claudePlainTextAck = "Hook output does not start with {, treating as plain text"

func init() {
	gates["claude-floor"] = func(o *opts) []string { return checkClaudeFloor(o.version) }
	gates["claude-static"] = func(o *opts) []string { return checkClaudeStatic(o.req, o.hooksJSON) }
	gates["claude-spawn"] = func(o *opts) []string {
		return checkClaudeSpawn(o.records, o.harnessErr, o.hookBin)
	}
	gates["claude-output"] = func(o *opts) []string { return checkClaudeOutput(o.records, o.harnessErr) }
}

// checkClaudeFloor asserts the claude under test is a 2.x. Claude Code hooks
// have been GA since 1.x and every event straza registers exists across the 2.x
// line, so unlike codex's 0.124 floor this is not a feature gate: it guards
// against a BROKEN or empty install (a `--version` that parses as 0.x/1.x, or
// not at all, means .tools/ holds something that is not the pinned claude and
// every other gate below would be measuring the wrong binary).
func checkClaudeFloor(version string) []string {
	m := regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`).FindStringSubmatch(version)
	if m == nil {
		return []string{fmt.Sprintf("cannot parse claude version from %q", version)}
	}
	major, _ := strconv.Atoi(m[1])
	if major < 2 {
		return []string{fmt.Sprintf("claude %s is below the 2.x floor; the lane is pinned to the 2.x hook surface", m[0])}
	}
	fmt.Printf("claude version %s ≥ 2.x floor\n", m[0])
	return nil
}

// checkClaudeStatic asserts the files the REAL `straza install claude-code`
// wrote under a redirected CLAUDE_CONFIG_DIR are the shapes claude reads:
//
//	settings   valid JSON, hooks table holding exactly the roster, one straza
//	           entry per event, `<bin> hook --harness claude-code`, matcher "*"
//	           on PreToolUse and on NOTHING else, no timeout key
//	mcp        valid JSON, mcpServers.straza = the credential-free stdio entry
//
// Live acceptance by the real binary is what claude-spawn proves; this catches
// the cheap breakages without spawning claude, and it asserts the path
// resolution too, since the lane points it at whatever SettingsPath /
// MCPConfigPath chose: $CLAUDE_CONFIG_DIR/settings.json and
// $CLAUDE_CONFIG_DIR/.claude.json, both confirmed live to be the files claude
// itself reads.
func checkClaudeStatic(settingsPath, mcpPath string) (errs []string) {
	errs = append(errs, checkClaudeSettings(settingsPath)...)
	return append(errs, checkClaudeMCP(mcpPath)...)
}

func checkClaudeSettings(path string) (errs []string) {
	raw, err := os.ReadFile(path) // #nosec G304 -- test-lane file
	if err != nil {
		return []string{fmt.Sprintf("settings.json unreadable: %v", err)}
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return []string{fmt.Sprintf("settings.json is not valid JSON: %v", err)}
	}
	hooks, ok := doc["hooks"].(map[string]any)
	if !ok || len(hooks) == 0 {
		return []string{`settings.json has no populated top-level "hooks" object`}
	}
	if diff := setDiff(claudeRoster, mapKeys(hooks)); diff != "" {
		errs = append(errs, "settings.json hook events "+diff)
	}
	for _, ev := range claudeRoster {
		arr, ok := hooks[ev].([]any)
		if !ok || len(arr) != 1 {
			errs = append(errs, fmt.Sprintf("%s: want exactly one hook group, got %#v", ev, hooks[ev]))
			continue
		}
		group, ok := arr[0].(map[string]any)
		if !ok {
			errs = append(errs, fmt.Sprintf("%s: hook group is not an object", ev))
			continue
		}
		// matcher belongs to PreToolUse alone: the installer writes it there
		// and nowhere else, and claude treats a matcher on an event that has
		// no tool to match as dead weight.
		matcher, hasMatcher := group["matcher"]
		switch {
		case ev == "PreToolUse" && matcher != "*":
			errs = append(errs, fmt.Sprintf("PreToolUse: want matcher \"*\", got %#v", matcher))
		case ev != "PreToolUse" && hasMatcher:
			errs = append(errs, fmt.Sprintf("%s: carries a matcher (%#v); the installer writes one only on PreToolUse", ev, matcher))
		}
		inner, ok := group["hooks"].([]any)
		if !ok || len(inner) != 1 {
			errs = append(errs, fmt.Sprintf("%s: want exactly one hook, got %#v", ev, group["hooks"]))
			continue
		}
		hook, ok := inner[0].(map[string]any)
		if !ok {
			errs = append(errs, fmt.Sprintf("%s: hook is not an object", ev))
			continue
		}
		// Exactly type+command: no timeout, no vendor extras. A timeout key
		// straza never wrote would be someone else's edit surviving a
		// re-install, and the installer replaces its own entries wholesale.
		if diff := setDiff([]string{"type", "command"}, mapKeys(hook)); diff != "" {
			errs = append(errs, fmt.Sprintf("%s: hook keys %s", ev, diff))
		}
		if t, _ := hook["type"].(string); t != "command" {
			errs = append(errs, fmt.Sprintf("%s: want type \"command\", got %#v", ev, hook["type"]))
		}
		cmd, _ := hook["command"].(string)
		const tail = " hook --harness claude-code"
		if !strings.HasSuffix(cmd, tail) {
			errs = append(errs, fmt.Sprintf("%s: command %q does not end in %q", ev, cmd, tail))
			continue
		}
		head := strings.TrimSuffix(cmd, tail)
		if head == "" {
			errs = append(errs, fmt.Sprintf("%s: command has no binary path", ev))
		}
		// Unix render carries a BARE exe head. Windows gets the quoted form
		// (install.go quoteCommand) and codex's 0.146 kill is what a quoted
		// head can cost when the harness re-tokenizes the string; pin the
		// Linux render so a shared-quoting regression shows up here first.
		if strings.HasPrefix(head, `"`) {
			errs = append(errs, fmt.Sprintf("%s: command head is quoted on a Unix render: %q", ev, cmd))
		}
	}
	return errs
}

func checkClaudeMCP(path string) (errs []string) {
	raw, err := os.ReadFile(path) // #nosec G304 -- test-lane file
	if err != nil {
		return []string{fmt.Sprintf(".claude.json unreadable: %v", err)}
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return []string{fmt.Sprintf(".claude.json is not valid JSON: %v", err)}
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	entry, ok := servers["straza"].(map[string]any)
	if !ok {
		return []string{".claude.json has no mcpServers.straza entry"}
	}
	if diff := setDiff([]string{"command", "args", "type"}, mapKeys(entry)); diff != "" {
		errs = append(errs, "mcpServers.straza keys "+diff)
	}
	if t, _ := entry["type"].(string); t != "stdio" {
		errs = append(errs, fmt.Sprintf("mcpServers.straza: want type \"stdio\", got %#v", entry["type"]))
	}
	if c, _ := entry["command"].(string); c == "" {
		errs = append(errs, "mcpServers.straza: empty command")
	}
	// The registration is credential-free by design: argv is the proxy
	// invocation, never a token.
	want := []string{"mcp", "--harness", "claude-code"}
	var got []string
	for _, a := range asSlice(entry["args"]) {
		s, _ := a.(string)
		got = append(got, s)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		errs = append(errs, fmt.Sprintf("mcpServers.straza: want args %v, got %v", want, got))
	}
	return errs
}

// checkClaudeSpawn asserts claude EXECUTED the installer's wiring: every
// key-free event reached the hook binary with intact argv and a parseable
// payload carrying the right hook_event_name, and claude's own telemetry
// reports no hook failure of any kind.
func checkClaudeSpawn(recordsPath, telemetryPath, hookBin string) (errs []string) {
	recs, err := readRecords(recordsPath)
	if err != nil {
		return []string{err.Error()}
	}
	seen := map[string]bool{}
	for i, r := range recs {
		if len(r.Argv) != 4 || r.Argv[0] != hookBin || r.Argv[1] != "hook" || r.Argv[2] != "--harness" || r.Argv[3] != "claude-code" {
			errs = append(errs, fmt.Sprintf("record %d: argv not intact: %q", i, r.Argv))
			continue
		}
		ev := eventOf(r)
		if ev == "" {
			errs = append(errs, fmt.Sprintf("record %d: stdin is not JSON or has no hook_event_name", i))
			continue
		}
		seen[ev] = true
	}
	for _, ev := range keyFreeClaudeEvents {
		if !seen[ev] {
			errs = append(errs, fmt.Sprintf("event %s never reached the hook", ev))
		}
	}
	return append(errs, claudeHookFailures(telemetryPath)...)
}

// checkClaudeOutput asserts claude ACCEPTS what the real `straza hook` prints
// on every key-free event: the relay ran the real binary, it exited 0, the ack
// is the one the encoder promises (SILENCE, since every key-free event is
// non-enforceable), and claude's own log shows it took that ack instead of
// rejecting it.
//
// This gate is why the encoder acks with silence. claude validates hook stdout
// against a per-event hookSpecificOutput schema whose union has no SessionEnd
// variant, so an echo document there is rejected in front of the user; a
// document reappearing here is that regression coming back, a failure in itself.
//
// The lane runs in conformance mode (`straza hook --harness claude-code
// --conformance-policy`), which is store-less, so SessionStart takes the plain
// encodeAllow path too. The ENROLLED path's context document (hook.go
// encodeSessionStart) needs a checkin and belongs to a live-server gate.
func checkClaudeOutput(recordsPath, telemetryPath string) (errs []string) {
	recs, err := readRecords(recordsPath)
	if err != nil {
		return []string{err.Error()}
	}
	seen := map[string]bool{}
	silent := 0
	for i, r := range recs {
		ev := eventOf(r)
		if r.ExecExit == nil {
			errs = append(errs, fmt.Sprintf("record %d (%s): relay did not run the real hook", i, ev))
			continue
		}
		if *r.ExecExit != 0 {
			errs = append(errs, fmt.Sprintf("record %d (%s): straza hook exited %d (stderr: %s)", i, ev, *r.ExecExit, strings.TrimSpace(r.ExecStderr)))
			continue
		}
		seen[ev] = true
		if strings.TrimSpace(r.ExecStdout) == "" {
			silent++
			continue
		}
		// A document on a non-enforceable event is the old encoder shape,
		// which claude rejects on SessionEnd. Flag the regression AND
		// check the document's own shape, so the row says which half broke.
		errs = append(errs, fmt.Sprintf("%s: straza printed a document on a non-enforceable event (%q); the encoder acks these with silence because claude rejects a document on SessionEnd", ev, strings.TrimSpace(r.ExecStdout)))
		errs = append(errs, claudeAckErrs(ev, r.ExecStdout)...)
	}
	for _, ev := range keyFreeClaudeEvents {
		if !seen[ev] {
			errs = append(errs, fmt.Sprintf("event %s has no clean hook run", ev))
		}
	}
	errs = append(errs, claudeRejectedAcks(recs, telemetryPath)...)
	errs = append(errs, claudeAckAccepted(telemetryPath, silent)...)
	return append(errs, claudeHookFailures(telemetryPath)...)
}

// claudeRejectedAcks correlates claude's rejection lines with the ack straza
// actually printed for that event, so the drift report names the straza-side
// cause and not only the vendor's message. claude's user-visible stderr line
// carries the event name (`<Event> hook [<cmd>] failed: <reason>`), which its
// debug-log twin does not.
func claudeRejectedAcks(recs []record, telemetryPath string) (errs []string) {
	raw, err := os.ReadFile(telemetryPath) // #nosec G304 -- test-lane file
	if err != nil {
		return nil // claudeHookFailures reports the unreadable capture
	}
	acks := map[string]string{}
	for _, r := range recs {
		if ev := eventOf(r); ev != "" {
			acks[ev] = strings.TrimSpace(r.ExecStdout)
		}
	}
	re := regexp.MustCompile(`(\w+) hook \[[^\]]*\] failed: ([^\n]*)`)
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		errs = append(errs, fmt.Sprintf(
			"%s: claude REJECTED the ack straza printed (%q): %s. claude validates hook stdout against a per-event hookSpecificOutput schema; empty stdout is an accepted ack on every key-free event (verified on claude 2.1.220), so an event whose schema has no variant must get silence, not the echo document",
			m[1], acks[m[1]], strings.TrimSpace(m[2])))
	}
	return errs
}

// claudeAckErrs checks one ack document against the encoder's contract
// (hook.go encodeAllow, claudeResponse).
func claudeAckErrs(ev, out string) (errs []string) {
	var doc struct {
		HookSpecificOutput map[string]any `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return []string{fmt.Sprintf("%s: ack is not valid JSON (%v): %q", ev, err, out)}
	}
	if doc.HookSpecificOutput == nil {
		return []string{fmt.Sprintf("%s: ack has no hookSpecificOutput: %q", ev, out)}
	}
	if name, _ := doc.HookSpecificOutput["hookEventName"].(string); name != ev {
		errs = append(errs, fmt.Sprintf("%s: ack echoes hookEventName %q; the harness-native name is what claude matches on", ev, name))
	}
	// permissionDecision is a PreToolUse/PermissionRequest field. None of the
	// key-free events is enforceable, so emitting one here would be claiming a
	// decision on an event claude cannot enforce.
	if _, ok := doc.HookSpecificOutput["permissionDecision"]; ok {
		errs = append(errs, fmt.Sprintf("%s: ack carries permissionDecision on a non-enforceable event: %q", ev, out))
	}
	return errs
}

// claudeAckAccepted requires claude's log to show it took each ack, one line
// per hook run: an empty ack is logged as the plain-text path, a document as
// `Hook <ev>:<source> (<ev>) success:`. Absence is the failure this lane
// exists to catch: an ack claude neither accepted nor rejected means it never
// ran the hook at all, which no records check can distinguish from a run whose
// output was thrown away. Checked separately from claudeHookFailures so a
// SILENT non-acceptance cannot pass.
func claudeAckAccepted(path string, silentAcks int) (errs []string) {
	raw, err := os.ReadFile(path) // #nosec G304 -- test-lane file
	if err != nil {
		return []string{fmt.Sprintf("harness telemetry unreadable: %v", err)}
	}
	if got := strings.Count(string(raw), claudePlainTextAck); got < silentAcks {
		errs = append(errs, fmt.Sprintf("claude logged %d silent-ack acceptances for %d silent acks (%q)", got, silentAcks, claudePlainTextAck))
	}
	return errs
}

// claudeHookFailures surfaces claude's own per-hook failure telemetry. The
// harness deciding a hook failed is the ground truth this lane reads; each
// pattern below was positive-controlled live on claude 2.1.220 by making
// a hook fail that way on purpose.
func claudeHookFailures(path string) (errs []string) {
	raw, err := os.ReadFile(path) // #nosec G304 -- test-lane file
	if err != nil {
		return []string{fmt.Sprintf("harness telemetry unreadable: %v", err)}
	}
	patterns := []*regexp.Regexp{
		// Rejected stdout: claude parsed the JSON and its per-event schema
		// refused it. (Live: straza's SessionEnd ack.)
		regexp.MustCompile(`Hook JSON output validation failed[^\n]*`),
		// Same event, claude's user-visible stderr line and its [ERROR] log line.
		regexp.MustCompile(`\S+ hook \[[^\]]*\] failed:[^\n]*`),
		regexp.MustCompile(`failed to run:[^\n]*`),
		// Hook process exited non-zero: claude logs the hook's stderr under an
		// `error:` header and the exit status.
		regexp.MustCompile(`Hook \S+ \(\S+\) error:`),
		regexp.MustCompile(`completed with status [1-9][0-9]*`),
	}
	seen := map[string]bool{}
	for _, re := range patterns {
		for _, m := range re.FindAllString(string(raw), -1) {
			m = strings.TrimSpace(m)
			if seen[m] {
				continue
			}
			seen[m] = true
			errs = append(errs, "claude reported: "+m)
		}
	}
	sort.Strings(errs)
	return errs
}

// setDiff describes how got differs from want as a set, or "" when they match.
func setDiff(want, got []string) string {
	inWant := map[string]bool{}
	for _, w := range want {
		inWant[w] = true
	}
	inGot := map[string]bool{}
	for _, g := range got {
		inGot[g] = true
	}
	var missing, extra []string
	for _, w := range want {
		if !inGot[w] {
			missing = append(missing, w)
		}
	}
	for _, g := range got {
		if !inWant[g] {
			extra = append(extra, g)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return ""
	}
	sort.Strings(missing)
	sort.Strings(extra)
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, "missing "+strings.Join(missing, ","))
	}
	if len(extra) > 0 {
		parts = append(parts, "unexpected "+strings.Join(extra, ","))
	}
	return strings.Join(parts, "; ")
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
